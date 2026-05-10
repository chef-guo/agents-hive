package memory

import (
	"context"
	"strings"
	"time"
)

const (
	MetricEmbeddingDroppedTotal     = "memory_embedding_dropped_total"
	MetricEmbeddingLatencySeconds   = "memory_embedding_latency_seconds"
	MetricEmbeddingBacklogDepth     = "memory_embedding_backlog_depth"
	MetricVectorSpaceMismatchTotal  = "memory_vector_space_mismatch_total"
	MetricHybridSearchFallbackTotal = "memory_hybrid_search_fallback_total"
)

const (
	EmbeddingDroppedReasonSemaphoreTimeout = "semaphore_timeout"
	EmbeddingDroppedReasonProviderError    = "provider_error"
	EmbeddingDroppedReasonEmptyVector      = "empty_vector"
	EmbeddingDroppedReasonStoreError       = "store_error"
	EmbeddingDroppedReasonVectorIndexError = "vector_index_error"
)

const (
	HybridFallbackReasonFTSError    = "fts_error"
	HybridFallbackReasonEmbedError  = "embed_error"
	HybridFallbackReasonVectorError = "vector_error"
	HybridFallbackReasonEmptyVector = "empty_vector"
	HybridFallbackReasonUnavailable = "vector_unavailable"
)

// MetricEvent 是 memory 包对生产指标的最小抽象。
// 调用方可以接 PostgreSQL、Prometheus、OpenTelemetry 或测试 recorder。
type MetricEvent struct {
	Name   string
	Value  float64
	Labels map[string]any
	Time   time.Time
}

type MetricRecorder interface {
	RecordMemoryMetric(ctx context.Context, event MetricEvent)
}

type ExternalMetricWriter interface {
	Record(ctx context.Context, metric MetricEvent) error
}

type externalMetricRecorder struct {
	writer ExternalMetricWriter
}

func NewExternalMetricRecorder(writer ExternalMetricWriter) MetricRecorder {
	if writer == nil {
		return nil
	}
	return externalMetricRecorder{writer: writer}
}

func (r externalMetricRecorder) RecordMemoryMetric(ctx context.Context, event MetricEvent) {
	if r.writer == nil {
		return
	}
	_ = r.writer.Record(ctx, event)
}

type MetricsConfig struct {
	Recorder    MetricRecorder
	VectorSpace string
}

type MetricAwareStore interface {
	SetMetrics(MetricRecorder)
	SetMetricsConfig(MetricsConfig)
}

func (s *PostgresMemoryStore) SetMetrics(recorder MetricRecorder) {
	if s == nil {
		return
	}
	s.metrics = recorder
}

func (s *PostgresMemoryStore) SetMetricsConfig(cfg MetricsConfig) {
	if s == nil {
		return
	}
	if cfg.Recorder != nil {
		s.metrics = cfg.Recorder
	}
	if strings.TrimSpace(cfg.VectorSpace) != "" {
		s.vectorSpace = strings.TrimSpace(cfg.VectorSpace)
	}
}

func (h *HybridSearcher) SetMetrics(recorder MetricRecorder) {
	if h == nil {
		return
	}
	h.metrics = recorder
}

func (h *HybridSearcher) SetMetricsConfig(cfg MetricsConfig) {
	if h == nil {
		return
	}
	if cfg.Recorder != nil {
		h.metrics = cfg.Recorder
	}
	if strings.TrimSpace(cfg.VectorSpace) != "" {
		h.vectorSpace = strings.TrimSpace(cfg.VectorSpace)
	}
}

func metricVectorSpace(space string) string {
	space = strings.TrimSpace(space)
	if space == "" {
		return DefaultVectorSpaceName
	}
	return space
}

func metricEmbeddingBacklogStatus(status EmbeddingBacklogStatus) string {
	switch status {
	case EmbeddingBacklogStatusPending, EmbeddingBacklogStatusClaimed, EmbeddingBacklogStatusDone, EmbeddingBacklogStatusFailed:
		return string(status)
	default:
		return "unknown"
	}
}

func recordMetric(ctx context.Context, recorder MetricRecorder, name string, value float64, labels map[string]any) {
	if recorder == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	recorder.RecordMemoryMetric(ctx, MetricEvent{
		Name:   name,
		Value:  value,
		Labels: labels,
		Time:   time.Now(),
	})
}

func recordEmbeddingDropped(ctx context.Context, recorder MetricRecorder, reason string) {
	recordMetric(ctx, recorder, MetricEmbeddingDroppedTotal, 1, map[string]any{"reason": reason})
}

func recordEmbeddingLatency(ctx context.Context, recorder MetricRecorder, mode string, status string, duration time.Duration) {
	if mode == "" {
		mode = "unknown"
	}
	if status == "" {
		status = "unknown"
	}
	recordMetric(ctx, recorder, MetricEmbeddingLatencySeconds, duration.Seconds(), map[string]any{
		"operation": mode,
		"status":    status,
	})
}

func recordBacklogDepth(ctx context.Context, recorder MetricRecorder, stats EmbeddingBacklogStats) {
	if recorder == nil {
		return
	}
	seen := map[EmbeddingBacklogStatus]bool{}
	for status, count := range stats.ByState {
		seen[status] = true
		recordMetric(ctx, recorder, MetricEmbeddingBacklogDepth, float64(count), map[string]any{
			"status": metricEmbeddingBacklogStatus(status),
		})
	}
	for _, status := range []EmbeddingBacklogStatus{
		EmbeddingBacklogStatusPending,
		EmbeddingBacklogStatusClaimed,
		EmbeddingBacklogStatusDone,
		EmbeddingBacklogStatusFailed,
	} {
		if !seen[status] {
			recordMetric(ctx, recorder, MetricEmbeddingBacklogDepth, 0, map[string]any{
				"status": string(status),
			})
		}
	}
	recordMetric(ctx, recorder, MetricEmbeddingBacklogDepth, float64(stats.Total), map[string]any{"status": "total"})
}
