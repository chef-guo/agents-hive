package memoryobs

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/chef-guo/agents-hive/internal/memory"
)

func TestBuildProductionMetricsAggregatesSnapshotSeriesAndAlerts(t *testing.T) {
	since := time.Date(2026, 5, 9, 8, 0, 0, 0, time.UTC)
	until := since.Add(2 * time.Hour)
	events := []memory.MetricEvent{
		{Name: memory.MetricEmbeddingDroppedTotal, Value: 2, Labels: map[string]any{"reason": memory.EmbeddingDroppedReasonSemaphoreTimeout}, Time: since.Add(10 * time.Minute)},
		{Name: memory.MetricEmbeddingDroppedTotal, Value: 1, Labels: map[string]any{"reason": memory.EmbeddingDroppedReasonProviderError}, Time: since.Add(70 * time.Minute)},
		{Name: memory.MetricEmbeddingLatencySeconds, Value: 0.1, Time: since.Add(15 * time.Minute)},
		{Name: memory.MetricEmbeddingLatencySeconds, Value: 0.4, Time: since.Add(80 * time.Minute)},
		{Name: memory.MetricEmbeddingBacklogDepth, Value: 9, Labels: map[string]any{"status": "total"}, Time: since.Add(20 * time.Minute)},
		{Name: memory.MetricEmbeddingBacklogDepth, Value: 3, Labels: map[string]any{"status": "failed"}, Time: since.Add(20 * time.Minute)},
		{Name: memory.MetricVectorSpaceMismatchTotal, Value: 1, Labels: map[string]any{"operation": "search"}, Time: since.Add(90 * time.Minute)},
		{Name: memory.MetricHybridSearchFallbackTotal, Value: 4, Labels: map[string]any{"reason": memory.HybridFallbackReasonVectorError}, Time: since.Add(90 * time.Minute)},
	}

	got := BuildProductionMetrics(events, since, until, time.Hour, "test")

	require.Equal(t, "test", got.Source)
	require.Equal(t, 120, got.WindowMinutes)
	require.Equal(t, float64(3), got.Snapshot.EmbeddingDroppedTotal)
	require.Equal(t, float64(2), got.Snapshot.DropReasons[memory.EmbeddingDroppedReasonSemaphoreTimeout])
	require.Equal(t, float64(1), got.Snapshot.VectorSpaceMismatchTotal)
	require.Equal(t, float64(4), got.Snapshot.HybridSearchFallbackTotal)
	require.Equal(t, 2, got.Snapshot.EmbeddingLatencyCount)
	require.InDelta(t, 0.25, got.Snapshot.EmbeddingLatencyAvgSec, 0.001)
	require.Equal(t, 0.4, got.Snapshot.EmbeddingLatencyP95Sec)
	require.Equal(t, float64(9), got.Snapshot.BacklogDepthTotal)
	require.Equal(t, float64(3), got.Snapshot.BacklogDepthByStatus["failed"])
	require.Len(t, got.Series, 2)
	require.Equal(t, float64(2), got.Series[0].EmbeddingDroppedTotal)
	require.Equal(t, float64(1), got.Series[1].EmbeddingDroppedTotal)
	require.NotEmpty(t, got.Alerts)
	require.Contains(t, alertCodes(got.Alerts), "vector_space_mismatch")
}

func alertCodes(alerts []ProductionMetricAlert) []string {
	codes := make([]string, 0, len(alerts))
	for _, alert := range alerts {
		codes = append(codes, alert.Code)
	}
	return codes
}
