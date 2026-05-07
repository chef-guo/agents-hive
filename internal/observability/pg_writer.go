package observability

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"
)

type pgTracerQuerier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// PgTracer 基于 PostgreSQL 的 Tracer 实现
type PgTracer struct {
	pool    *pgxpool.Pool
	querier pgTracerQuerier
	logger  *zap.Logger
}

// NewPgTracer 创建 PG Tracer
func NewPgTracer(pool *pgxpool.Pool, logger *zap.Logger) *PgTracer {
	return &PgTracer{pool: pool, querier: pool, logger: logger}
}

// StartSpan 开始一个 Span，返回 SpanContext
func (t *PgTracer) StartSpan(_ context.Context, traceID, spanID, parentSpanID, operation, service, sessionID string) SpanContext {
	return SpanContext{
		TraceID:      traceID,
		SpanID:       spanID,
		ParentSpanID: parentSpanID,
		Operation:    operation,
		Service:      service,
		SessionID:    sessionID,
		StartTime:    time.Now(),
		tracer:       t,
	}
}

// RecordSpan 写入一条完整 Span 到 hive_traces
func (t *PgTracer) RecordSpan(ctx context.Context, span Span) error {
	attrsJSON, _ := json.Marshal(span.Attributes)
	_, err := t.pool.Exec(ctx,
		`INSERT INTO hive_traces
		 (trace_id, span_id, parent_span_id, operation, service, session_id, user_id, duration_ms, status, attributes, ts)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`,
		span.TraceID, span.SpanID, nullStr(span.ParentSpanID), span.Operation, span.Service,
		nullStr(span.SessionID), span.UserID, span.DurationMs, span.Status, attrsJSON, span.Ts,
	)
	if err != nil {
		t.logger.Warn("写入 trace span 失败", zap.Error(err))
	}
	return err
}

// GetSessionTimeline 返回 session 维度的 span + quality log 统一时间线。
func (t *PgTracer) GetSessionTimeline(ctx context.Context, sessionID string, limit int) (TraceTimeline, error) {
	if limit <= 0 {
		limit = 2000
	}
	rows, err := t.querier.Query(ctx, `
		WITH span_items AS (
			SELECT 'span' AS kind,
			       trace_id,
			       span_id,
			       COALESCE(parent_span_id, '') AS parent_span_id,
			       operation,
			       service,
			       status,
			       duration_ms,
			       attributes,
			       ts
			  FROM hive_traces
			 WHERE session_id = $1
		), quality_items AS (
			SELECT 'quality_event' AS kind,
			       COALESCE(trace_id, '') AS trace_id,
			       COALESCE(span_id, '') AS span_id,
			       '' AS parent_span_id,
			       message AS operation,
			       'agentquality' AS service,
			       'ok' AS status,
			       0 AS duration_ms,
			       attributes,
			       ts
			  FROM hive_logs
			 WHERE session_id = $1
			   AND message LIKE 'quality.%'
		)
		SELECT kind, trace_id, span_id, parent_span_id, operation, service, status, duration_ms, attributes, ts
		  FROM (
			SELECT * FROM span_items
			UNION ALL
			SELECT * FROM quality_items
		  ) AS timeline
		 ORDER BY ts ASC
		 LIMIT $2`, sessionID, limit)
	if err != nil {
		return TraceTimeline{}, err
	}
	defer rows.Close()

	out := TraceTimeline{SessionID: sessionID, Items: []TraceTimelineItem{}}
	for rows.Next() {
		var item TraceTimelineItem
		var attrs []byte
		if err := rows.Scan(&item.Kind, &item.TraceID, &item.SpanID, &item.ParentSpanID, &item.Operation, &item.Service, &item.Status, &item.DurationMs, &attrs, &item.Timestamp); err != nil {
			return TraceTimeline{}, err
		}
		if len(attrs) > 0 && string(attrs) != "null" {
			var decoded map[string]any
			if err := json.Unmarshal(attrs, &decoded); err == nil {
				item.Attributes = decoded
			}
		}
		if out.TraceID == "" && item.TraceID != "" {
			out.TraceID = item.TraceID
		}
		out.Items = append(out.Items, item)
	}
	if err := rows.Err(); err != nil {
		return TraceTimeline{}, err
	}
	SortTraceTimelineItems(out.Items)
	out.AgentTree = BuildAgentTraceTree(out.Items)
	return out, nil
}

// PgMetricsWriter 基于 PostgreSQL 的 MetricsWriter 实现
type PgMetricsWriter struct {
	pool   *pgxpool.Pool
	logger *zap.Logger
}

// NewPgMetricsWriter 创建 PG MetricsWriter
func NewPgMetricsWriter(pool *pgxpool.Pool, logger *zap.Logger) *PgMetricsWriter {
	return &PgMetricsWriter{pool: pool, logger: logger}
}

// Record 写入一条指标到 hive_metrics
func (w *PgMetricsWriter) Record(ctx context.Context, metric Metric) error {
	labelsJSON, _ := json.Marshal(SanitizeMetricLabels(metric.Name, metric.Labels))
	ts := metric.Ts
	if ts.IsZero() {
		ts = time.Now()
	}
	_, err := w.pool.Exec(ctx,
		`INSERT INTO hive_metrics (name, value, labels, ts) VALUES ($1,$2,$3,$4)`,
		metric.Name, metric.Value, labelsJSON, ts,
	)
	if err != nil {
		w.logger.Warn("写入 metric 失败", zap.Error(err))
	}
	return err
}

// PgLogWriter 基于 PostgreSQL 的 LogWriter 实现
type PgLogWriter struct {
	pool   *pgxpool.Pool
	logger *zap.Logger
}

// NewPgLogWriter 创建 PG LogWriter
func NewPgLogWriter(pool *pgxpool.Pool, logger *zap.Logger) *PgLogWriter {
	return &PgLogWriter{pool: pool, logger: logger}
}

// Write 写入一条日志到 hive_logs
func (w *PgLogWriter) Write(ctx context.Context, entry LogEntry) error {
	attrsJSON, _ := json.Marshal(entry.Attributes)
	ts := entry.Ts
	if ts.IsZero() {
		ts = time.Now()
	}
	_, err := w.pool.Exec(ctx,
		`INSERT INTO hive_logs (level, message, trace_id, span_id, session_id, attributes, ts) VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		entry.Level, entry.Message, nullStr(entry.TraceID), nullStr(entry.SpanID),
		nullStr(entry.SessionID), attrsJSON, ts,
	)
	if err != nil {
		w.logger.Warn("写入 log entry 失败", zap.Error(err))
	}
	return err
}

// nullStr 将空字符串转为 nil（PG NULL）
func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}
