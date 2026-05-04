package sessiontodo

import (
	"testing"
	"time"

	"github.com/chef-guo/agents-hive/internal/observability"
	"github.com/stretchr/testify/require"
)

func TestBuildOpsSnapshotAggregatesMetricsAndAlerts(t *testing.T) {
	now := time.Date(2026, 5, 2, 10, 0, 0, 0, time.UTC)
	input := OpsDashboardInput{
		Now:    now,
		Window: time.Hour,
		Metrics: []observability.Metric{
			{Name: MetricSessionTodoWritesTotal, Value: 90, Labels: map[string]any{"status": "ok"}, Ts: now.Add(-50 * time.Minute)},
			{Name: MetricSessionTodoWritesTotal, Value: 10, Labels: map[string]any{"status": "error"}, Ts: now.Add(-40 * time.Minute)},
			{Name: MetricSessionTodoVersionConflictsTotal, Value: 8, Ts: now.Add(-30 * time.Minute)},
			{Name: MetricTodoSnapshotBroadcastTotal, Value: 20, Labels: map[string]any{"status": "ok"}, Ts: now.Add(-20 * time.Minute)},
			{Name: MetricTodoSnapshotBroadcastTotal, Value: 1, Labels: map[string]any{"status": "error"}, Ts: now.Add(-10 * time.Minute)},
			{Name: MetricPlanRuntimeDecisionsTotal, Value: 3, Labels: map[string]any{"decision": "paused"}, Ts: now.Add(-9 * time.Minute)},
			{Name: MetricPlanRuntimeDecisionsTotal, Value: 7, Labels: map[string]any{"decision": "completed"}, Ts: now.Add(-8 * time.Minute)},
			{Name: MetricPlanRuntimeDecisionsTotal, Value: 1, Labels: map[string]any{"decision": "failed"}, Ts: now.Add(-7 * time.Minute)},
			{Name: MetricPlanModeGateDeniedTotal, Value: 2, Labels: map[string]any{"tool_name": "write_file"}, Ts: now.Add(-6 * time.Minute)},
			{Name: MetricSessionTodoWritesTotal, Value: 100, Labels: map[string]any{"status": "error"}, Ts: now.Add(-2 * time.Hour)},
		},
		Spans: []observability.Span{
			{Operation: "todo_write.execute", DurationMs: 100, Status: "ok", Ts: now.Add(-50 * time.Minute)},
			{Operation: "todo_write.execute", DurationMs: 300, Status: "ok", Ts: now.Add(-40 * time.Minute)},
			{Operation: "plan_runtime.decide_turn_completion", DurationMs: 25, Status: "ok", Ts: now.Add(-30 * time.Minute)},
			{Operation: "todo_write.execute", DurationMs: 9999, Status: "ok", Ts: now.Add(-2 * time.Hour)},
		},
	}

	snapshot := BuildOpsSnapshot(input, DefaultOpsAlertThresholds())

	require.Equal(t, now.Add(-time.Hour), snapshot.WindowStart)
	require.Equal(t, now, snapshot.WindowEnd)
	require.Equal(t, 100.0, snapshot.TodoWritesTotal)
	require.Equal(t, 10.0, snapshot.TodoWriteErrors)
	require.InDelta(t, 0.10, snapshot.TodoWriteErrorRate, 0.0001)
	require.Equal(t, 8.0, snapshot.PlanVersionConflicts)
	require.InDelta(t, 0.08, snapshot.PlanVersionConflictRate, 0.0001)
	require.Equal(t, 1.0, snapshot.SnapshotBroadcastErrors)
	require.Equal(t, map[string]float64{"paused": 3, "completed": 7, "failed": 1}, snapshot.PlanRuntimeDecisions)
	require.Equal(t, 200.0, snapshot.TodoWriteAvgLatencyMs)
	require.Equal(t, 300.0, snapshot.TodoWriteP95LatencyMs)
	requireAlertCodes(t, snapshot.Alerts,
		"todo_write_error_rate_high",
		"plan_version_conflict_rate_high",
		"todo_snapshot_broadcast_failed",
		"plan_runtime_failed_decisions",
	)
}

func TestBuildOpsSnapshotCanDisableNoisyPlanModeGateAlert(t *testing.T) {
	now := time.Date(2026, 5, 2, 10, 0, 0, 0, time.UTC)
	snapshot := BuildOpsSnapshot(OpsDashboardInput{
		Now:    now,
		Window: time.Hour,
		Metrics: []observability.Metric{
			{Name: MetricPlanModeGateDeniedTotal, Value: 99, Labels: map[string]any{"tool_name": "write_file"}, Ts: now.Add(-10 * time.Minute)},
		},
	}, OpsAlertThresholds{PlanModeGateDeniedWarnCount: -1})

	require.Equal(t, 99.0, snapshot.PlanModeGateDenied)
	require.Empty(t, snapshot.Alerts)
}

func requireAlertCodes(t *testing.T, alerts []OpsAlert, codes ...string) {
	t.Helper()
	got := make(map[string]bool, len(alerts))
	for _, alert := range alerts {
		got[alert.Code] = true
	}
	for _, code := range codes {
		require.True(t, got[code], "expected alert %s in %#v", code, alerts)
	}
}
