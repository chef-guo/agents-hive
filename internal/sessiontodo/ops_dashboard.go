package sessiontodo

import (
	"math"
	"sort"
	"time"

	"github.com/chef-guo/agents-hive/internal/observability"
)

const (
	MetricSessionTodoWritesTotal           = "hive_sessiontodo_writes_total"
	MetricSessionTodoVersionConflictsTotal = "hive_sessiontodo_version_conflicts_total"
	MetricTodoSnapshotBroadcastTotal       = "hive_todo_snapshot_broadcast_total"
	MetricPlanRuntimeDecisionsTotal        = "hive_plan_runtime_decisions_total"
	MetricPlanModeGateDeniedTotal          = "hive_plan_mode_gate_denied_total"
)

type OpsDashboardInput struct {
	Now     time.Time
	Window  time.Duration
	Metrics []observability.Metric
	Spans   []observability.Span
}

type OpsDashboardSnapshot struct {
	WindowStart             time.Time          `json:"window_start"`
	WindowEnd               time.Time          `json:"window_end"`
	TodoWritesTotal         float64            `json:"todo_writes_total"`
	TodoWriteErrors         float64            `json:"todo_write_errors"`
	TodoWriteErrorRate      float64            `json:"todo_write_error_rate"`
	PlanVersionConflicts    float64            `json:"plan_version_conflicts"`
	PlanVersionConflictRate float64            `json:"plan_version_conflict_rate"`
	SnapshotBroadcastErrors float64            `json:"snapshot_broadcast_errors"`
	PlanRuntimeDecisions    map[string]float64 `json:"plan_runtime_decisions"`
	PlanModeGateDenied      float64            `json:"plan_mode_gate_denied"`
	TodoWriteAvgLatencyMs   float64            `json:"todo_write_avg_latency_ms"`
	TodoWriteP95LatencyMs   float64            `json:"todo_write_p95_latency_ms"`
	Alerts                  []OpsAlert         `json:"alerts"`
}

type OpsAlert struct {
	Code     string  `json:"code"`
	Level    string  `json:"level"`
	Message  string  `json:"message"`
	Value    float64 `json:"value"`
	Limit    float64 `json:"limit"`
	Runbook  string  `json:"runbook"`
	Disabled bool    `json:"disabled,omitempty"`
}

type OpsAlertThresholds struct {
	TodoWriteErrorRateWarn      float64
	PlanVersionConflictRateWarn float64
	SnapshotBroadcastErrorWarn  float64
	PlanRuntimeFailedWarnCount  float64
	PlanModeGateDeniedWarnCount float64
}

func DefaultOpsAlertThresholds() OpsAlertThresholds {
	return OpsAlertThresholds{
		TodoWriteErrorRateWarn:      0.05,
		PlanVersionConflictRateWarn: 0.05,
		SnapshotBroadcastErrorWarn:  0,
		PlanRuntimeFailedWarnCount:  0,
		PlanModeGateDeniedWarnCount: 10,
	}
}

func BuildOpsSnapshot(input OpsDashboardInput, thresholds OpsAlertThresholds) OpsDashboardSnapshot {
	now := input.Now
	if now.IsZero() {
		now = time.Now()
	}
	window := input.Window
	if window <= 0 {
		window = time.Hour
	}
	start := now.Add(-window)
	out := OpsDashboardSnapshot{
		WindowStart:          start,
		WindowEnd:            now,
		PlanRuntimeDecisions: make(map[string]float64),
	}
	for _, metric := range input.Metrics {
		if !withinWindow(metric.Ts, start, now) {
			continue
		}
		switch metric.Name {
		case MetricSessionTodoWritesTotal:
			out.TodoWritesTotal += metric.Value
			if labelString(metric.Labels, "status") != "ok" {
				out.TodoWriteErrors += metric.Value
			}
		case MetricSessionTodoVersionConflictsTotal:
			out.PlanVersionConflicts += metric.Value
		case MetricTodoSnapshotBroadcastTotal:
			if labelString(metric.Labels, "status") != "ok" {
				out.SnapshotBroadcastErrors += metric.Value
			}
		case MetricPlanRuntimeDecisionsTotal:
			decision := labelString(metric.Labels, "decision")
			if decision == "" {
				decision = "unknown"
			}
			out.PlanRuntimeDecisions[decision] += metric.Value
		case MetricPlanModeGateDeniedTotal:
			out.PlanModeGateDenied += metric.Value
		}
	}
	if out.TodoWritesTotal > 0 {
		out.TodoWriteErrorRate = out.TodoWriteErrors / out.TodoWritesTotal
		out.PlanVersionConflictRate = out.PlanVersionConflicts / out.TodoWritesTotal
	}
	out.TodoWriteAvgLatencyMs, out.TodoWriteP95LatencyMs = todoWriteLatencyStats(input.Spans, start, now)
	out.Alerts = buildOpsAlerts(out, thresholds)
	return out
}

func buildOpsAlerts(snapshot OpsDashboardSnapshot, thresholds OpsAlertThresholds) []OpsAlert {
	alerts := make([]OpsAlert, 0)
	alerts = appendRateAlert(alerts, "todo_write_error_rate_high", "warn", "todo_write 错误率升高", snapshot.TodoWriteErrorRate, thresholds.TodoWriteErrorRateWarn)
	alerts = appendRateAlert(alerts, "plan_version_conflict_rate_high", "warn", "sessiontodo CAS 冲突率升高", snapshot.PlanVersionConflictRate, thresholds.PlanVersionConflictRateWarn)
	alerts = appendCountAlert(alerts, "todo_snapshot_broadcast_failed", "warn", "todo_snapshot 广播失败", snapshot.SnapshotBroadcastErrors, thresholds.SnapshotBroadcastErrorWarn)
	alerts = appendCountAlert(alerts, "plan_runtime_failed_decisions", "warn", "Plan Runtime failed 决策出现", snapshot.PlanRuntimeDecisions["failed"], thresholds.PlanRuntimeFailedWarnCount)
	alerts = appendCountAlert(alerts, "plan_mode_gate_denied_spike", "info", "plan_mode gate 拦截次数升高", snapshot.PlanModeGateDenied, thresholds.PlanModeGateDeniedWarnCount)
	return alerts
}

func appendRateAlert(alerts []OpsAlert, code, level, message string, value, limit float64) []OpsAlert {
	if limit < 0 || value <= limit {
		return alerts
	}
	return append(alerts, OpsAlert{Code: code, Level: level, Message: message, Value: round4(value), Limit: limit, Runbook: "docs/运维手册/sessiontodo-observability.md"})
}

func appendCountAlert(alerts []OpsAlert, code, level, message string, value, limit float64) []OpsAlert {
	if limit < 0 || value <= limit {
		return alerts
	}
	return append(alerts, OpsAlert{Code: code, Level: level, Message: message, Value: value, Limit: limit, Runbook: "docs/运维手册/sessiontodo-observability.md"})
}

func todoWriteLatencyStats(spans []observability.Span, start, end time.Time) (avg, p95 float64) {
	values := make([]int, 0)
	total := 0
	for _, span := range spans {
		if span.Operation != "todo_write.execute" || !withinWindow(span.Ts, start, end) {
			continue
		}
		values = append(values, span.DurationMs)
		total += span.DurationMs
	}
	if len(values) == 0 {
		return 0, 0
	}
	sort.Ints(values)
	idx := int(math.Ceil(float64(len(values))*0.95)) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(values) {
		idx = len(values) - 1
	}
	return float64(total) / float64(len(values)), float64(values[idx])
}

func withinWindow(ts, start, end time.Time) bool {
	if ts.IsZero() {
		return true
	}
	return !ts.Before(start) && !ts.After(end)
}

func labelString(labels map[string]any, key string) string {
	if labels == nil {
		return ""
	}
	if s, ok := labels[key].(string); ok {
		return s
	}
	return ""
}

func round4(v float64) float64 {
	return math.Round(v*10000) / 10000
}
