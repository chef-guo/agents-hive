package sessiontodo

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/chef-guo/agents-hive/internal/observability"
)

type PGOpsReader struct {
	pool *pgxpool.Pool
}

func NewPGOpsReader(pool *pgxpool.Pool) *PGOpsReader {
	return &PGOpsReader{pool: pool}
}

func (r *PGOpsReader) LoadOps(ctx context.Context, since, until time.Time) (OpsDashboardInput, error) {
	input := OpsDashboardInput{Now: until, Window: until.Sub(since)}
	if r == nil || r.pool == nil {
		return input, nil
	}
	metrics, err := r.loadMetrics(ctx, since, until)
	if err != nil {
		return input, err
	}
	spans, err := r.loadSpans(ctx, since, until)
	if err != nil {
		return input, err
	}
	input.Metrics = metrics
	input.Spans = spans
	return input, nil
}

func (r *PGOpsReader) loadMetrics(ctx context.Context, since, until time.Time) ([]observability.Metric, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT name, value, labels, ts
		FROM hive_metrics
		WHERE ts >= $1 AND ts <= $2
		  AND name IN (
			'hive_sessiontodo_writes_total',
			'hive_sessiontodo_version_conflicts_total',
			'hive_todo_snapshot_broadcast_total',
			'hive_plan_runtime_decisions_total',
			'hive_plan_mode_gate_denied_total'
		  )
		ORDER BY ts ASC`, since, until)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]observability.Metric, 0)
	for rows.Next() {
		var metric observability.Metric
		var labels []byte
		if err := rows.Scan(&metric.Name, &metric.Value, &labels, &metric.Ts); err != nil {
			return nil, err
		}
		if len(labels) > 0 {
			_ = json.Unmarshal(labels, &metric.Labels)
		}
		out = append(out, metric)
	}
	return out, rows.Err()
}

func (r *PGOpsReader) loadSpans(ctx context.Context, since, until time.Time) ([]observability.Span, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT operation, duration_ms, status, ts
		FROM hive_traces
		WHERE ts >= $1 AND ts <= $2
		  AND operation IN ('todo_write.execute', 'plan_runtime.decide_turn_completion')
		ORDER BY ts ASC`, since, until)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]observability.Span, 0)
	for rows.Next() {
		var span observability.Span
		if err := rows.Scan(&span.Operation, &span.DurationMs, &span.Status, &span.Ts); err != nil {
			return nil, err
		}
		out = append(out, span)
	}
	return out, rows.Err()
}
