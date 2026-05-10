package memoryobs

import (
	"context"
	"encoding/json"
	"math"
	"sort"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/chef-guo/agents-hive/internal/memory"
)

type ProductionMetricsReader interface {
	LoadProductionMetrics(ctx context.Context, since, until time.Time, bucketSize time.Duration) (ProductionMetrics, error)
}

type ProductionMetrics struct {
	Source        string                         `json:"source"`
	Since         time.Time                      `json:"since"`
	Until         time.Time                      `json:"until"`
	WindowMinutes int                            `json:"window_minutes"`
	Snapshot      ProductionMetricsSnapshot      `json:"snapshot"`
	Series        []ProductionMetricsSeriesPoint `json:"series"`
	Alerts        []ProductionMetricAlert        `json:"alerts"`
}

type ProductionMetricsSnapshot struct {
	EmbeddingDroppedTotal     float64            `json:"embedding_dropped_total"`
	HybridSearchFallbackTotal float64            `json:"hybrid_search_fallback_total"`
	VectorSpaceMismatchTotal  float64            `json:"vector_space_mismatch_total"`
	EmbeddingLatencyCount     int                `json:"embedding_latency_count"`
	EmbeddingLatencyAvgSec    float64            `json:"embedding_latency_avg_seconds"`
	EmbeddingLatencyP95Sec    float64            `json:"embedding_latency_p95_seconds"`
	BacklogDepthTotal         float64            `json:"backlog_depth_total"`
	BacklogDepthByStatus      map[string]float64 `json:"backlog_depth_by_status"`
	DropReasons               map[string]float64 `json:"drop_reasons"`
	FallbackReasons           map[string]float64 `json:"fallback_reasons"`
	MismatchOperations        map[string]float64 `json:"mismatch_operations"`
}

type ProductionMetricsSeriesPoint struct {
	Since                     time.Time `json:"since"`
	Until                     time.Time `json:"until"`
	EmbeddingDroppedTotal     float64   `json:"embedding_dropped_total"`
	HybridSearchFallbackTotal float64   `json:"hybrid_search_fallback_total"`
	VectorSpaceMismatchTotal  float64   `json:"vector_space_mismatch_total"`
	EmbeddingLatencyAvgSec    float64   `json:"embedding_latency_avg_seconds"`
	BacklogDepthTotal         float64   `json:"backlog_depth_total"`
}

type ProductionMetricAlert struct {
	Level   string  `json:"level"`
	Code    string  `json:"code"`
	Message string  `json:"message"`
	Value   float64 `json:"value"`
}

type PGMetricsReader struct {
	pool *pgxpool.Pool
}

func NewPGMetricsReader(pool *pgxpool.Pool) *PGMetricsReader {
	if pool == nil {
		return nil
	}
	return &PGMetricsReader{pool: pool}
}

func (r *PGMetricsReader) LoadProductionMetrics(ctx context.Context, since, until time.Time, bucketSize time.Duration) (ProductionMetrics, error) {
	if r == nil || r.pool == nil {
		return BuildProductionMetrics(nil, since, until, bucketSize, "unavailable"), nil
	}
	events, err := r.load(ctx, since, until)
	if err != nil {
		return ProductionMetrics{}, err
	}
	return BuildProductionMetrics(events, since, until, bucketSize, "hive_metrics"), nil
}

func (r *PGMetricsReader) load(ctx context.Context, since, until time.Time) ([]memory.MetricEvent, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT name, value, labels, ts
		FROM hive_metrics
		WHERE ts >= $1 AND ts <= $2
		  AND name = ANY($3::text[])
		ORDER BY ts ASC`, since, until, productionMetricNames())
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	events := make([]memory.MetricEvent, 0)
	for rows.Next() {
		var event memory.MetricEvent
		var labels []byte
		if err := rows.Scan(&event.Name, &event.Value, &labels, &event.Time); err != nil {
			return nil, err
		}
		if len(labels) > 0 {
			_ = json.Unmarshal(labels, &event.Labels)
		}
		events = append(events, event)
	}
	return events, rows.Err()
}

func BuildProductionMetrics(events []memory.MetricEvent, since, until time.Time, bucketSize time.Duration, source string) ProductionMetrics {
	if until.IsZero() {
		until = time.Now()
	}
	if since.IsZero() || !since.Before(until) {
		since = until.Add(-24 * time.Hour)
	}
	if bucketSize <= 0 {
		bucketSize = time.Hour
	}
	if source == "" {
		source = "memory"
	}

	out := ProductionMetrics{
		Source:        source,
		Since:         since,
		Until:         until,
		WindowMinutes: int(math.Round(until.Sub(since).Minutes())),
		Snapshot: ProductionMetricsSnapshot{
			BacklogDepthByStatus: map[string]float64{},
			DropReasons:          map[string]float64{},
			FallbackReasons:      map[string]float64{},
			MismatchOperations:   map[string]float64{},
		},
	}

	latencies := make([]float64, 0)
	for _, event := range events {
		if event.Time.IsZero() || event.Time.Before(since) || event.Time.After(until) {
			continue
		}
		switch event.Name {
		case memory.MetricEmbeddingDroppedTotal:
			out.Snapshot.EmbeddingDroppedTotal += event.Value
			out.Snapshot.DropReasons[labelString(event.Labels, "reason", "unknown")] += event.Value
		case memory.MetricHybridSearchFallbackTotal:
			out.Snapshot.HybridSearchFallbackTotal += event.Value
			out.Snapshot.FallbackReasons[labelString(event.Labels, "reason", "unknown")] += event.Value
		case memory.MetricVectorSpaceMismatchTotal:
			out.Snapshot.VectorSpaceMismatchTotal += event.Value
			out.Snapshot.MismatchOperations[labelString(event.Labels, "operation", "unknown")] += event.Value
		case memory.MetricEmbeddingLatencySeconds:
			if event.Value >= 0 {
				latencies = append(latencies, event.Value)
			}
		case memory.MetricEmbeddingBacklogDepth:
			status := labelString(event.Labels, "status", "unknown")
			out.Snapshot.BacklogDepthByStatus[status] = event.Value
			if status == "total" {
				out.Snapshot.BacklogDepthTotal = event.Value
			}
		}
	}
	out.Snapshot.EmbeddingLatencyCount = len(latencies)
	out.Snapshot.EmbeddingLatencyAvgSec = average(latencies)
	out.Snapshot.EmbeddingLatencyP95Sec = percentile(latencies, 0.95)
	out.Series = buildSeries(events, since, until, bucketSize)
	out.Alerts = buildAlerts(out.Snapshot)
	return out
}

func buildSeries(events []memory.MetricEvent, since, until time.Time, bucketSize time.Duration) []ProductionMetricsSeriesPoint {
	points := make([]ProductionMetricsSeriesPoint, 0)
	for start := since; start.Before(until); start = start.Add(bucketSize) {
		end := start.Add(bucketSize)
		if end.After(until) {
			end = until
		}
		point := ProductionMetricsSeriesPoint{Since: start, Until: end}
		latencies := make([]float64, 0)
		for _, event := range events {
			if event.Time.IsZero() || event.Time.Before(start) || !event.Time.Before(end) {
				continue
			}
			switch event.Name {
			case memory.MetricEmbeddingDroppedTotal:
				point.EmbeddingDroppedTotal += event.Value
			case memory.MetricHybridSearchFallbackTotal:
				point.HybridSearchFallbackTotal += event.Value
			case memory.MetricVectorSpaceMismatchTotal:
				point.VectorSpaceMismatchTotal += event.Value
			case memory.MetricEmbeddingLatencySeconds:
				if event.Value >= 0 {
					latencies = append(latencies, event.Value)
				}
			case memory.MetricEmbeddingBacklogDepth:
				if labelString(event.Labels, "status", "") == "total" {
					point.BacklogDepthTotal = event.Value
				}
			}
		}
		point.EmbeddingLatencyAvgSec = average(latencies)
		points = append(points, point)
	}
	return points
}

func buildAlerts(snapshot ProductionMetricsSnapshot) []ProductionMetricAlert {
	alerts := make([]ProductionMetricAlert, 0)
	if snapshot.EmbeddingDroppedTotal > 0 {
		alerts = append(alerts, ProductionMetricAlert{
			Level:   "warning",
			Code:    "embedding_dropped",
			Message: "embedding 写入存在丢弃，需要检查 provider、并发阈值或 backlog",
			Value:   snapshot.EmbeddingDroppedTotal,
		})
	}
	if snapshot.VectorSpaceMismatchTotal > 0 {
		alerts = append(alerts, ProductionMetricAlert{
			Level:   "critical",
			Code:    "vector_space_mismatch",
			Message: "检测到向量空间不匹配，hybrid 召回可能已降级",
			Value:   snapshot.VectorSpaceMismatchTotal,
		})
	}
	if snapshot.BacklogDepthByStatus["failed"] > 0 {
		alerts = append(alerts, ProductionMetricAlert{
			Level:   "warning",
			Code:    "embedding_backlog_failed",
			Message: "embedding backlog 存在失败任务，需要查看错误原因并重试",
			Value:   snapshot.BacklogDepthByStatus["failed"],
		})
	}
	return alerts
}

func productionMetricNames() []string {
	return []string{
		memory.MetricEmbeddingDroppedTotal,
		memory.MetricEmbeddingLatencySeconds,
		memory.MetricEmbeddingBacklogDepth,
		memory.MetricVectorSpaceMismatchTotal,
		memory.MetricHybridSearchFallbackTotal,
	}
}

func labelString(labels map[string]any, key, fallback string) string {
	if labels == nil {
		return fallback
	}
	value, ok := labels[key]
	if !ok || value == nil {
		return fallback
	}
	switch typed := value.(type) {
	case string:
		if typed != "" {
			return typed
		}
	case []byte:
		if len(typed) > 0 {
			return string(typed)
		}
	}
	return fallback
}

func average(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	var total float64
	for _, value := range values {
		total += value
	}
	return total / float64(len(values))
}

func percentile(values []float64, p float64) float64 {
	if len(values) == 0 {
		return 0
	}
	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)
	if p <= 0 {
		return sorted[0]
	}
	if p >= 1 {
		return sorted[len(sorted)-1]
	}
	idx := int(math.Ceil(float64(len(sorted))*p)) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}
