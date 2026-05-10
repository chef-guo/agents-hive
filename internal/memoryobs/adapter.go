package memoryobs

import (
	"context"

	"github.com/chef-guo/agents-hive/internal/memory"
	"github.com/chef-guo/agents-hive/internal/observability"
)

type Writer struct {
	writer observability.MetricsWriter
}

func NewWriter(writer observability.MetricsWriter) *Writer {
	if writer == nil {
		return nil
	}
	return &Writer{writer: writer}
}

func (w *Writer) Record(ctx context.Context, event memory.MetricEvent) error {
	if w == nil || w.writer == nil {
		return nil
	}
	return w.writer.Record(ctx, observability.Metric{
		Name:   event.Name,
		Value:  event.Value,
		Labels: event.Labels,
		Ts:     event.Time,
	})
}
