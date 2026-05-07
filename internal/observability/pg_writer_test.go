package observability

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"go.uber.org/zap"
)

type fakeTimelineQuerier struct {
	gotSessionID string
	gotLimit     int
	rows         pgx.Rows
	err          error
}

func (q *fakeTimelineQuerier) Query(_ context.Context, sql string, args ...any) (pgx.Rows, error) {
	if !strings.Contains(sql, "hive_traces") || !strings.Contains(sql, "hive_logs") {
		return nil, errors.New("timeline query did not join trace and log tables")
	}
	q.gotSessionID, _ = args[0].(string)
	q.gotLimit, _ = args[1].(int)
	return q.rows, q.err
}

type timelineRow struct {
	kind         string
	traceID      string
	spanID       string
	parentSpanID string
	operation    string
	service      string
	status       string
	durationMs   int
	attrs        []byte
	ts           time.Time
}

type fakeTimelineRows struct {
	items  []timelineRow
	idx    int
	closed bool
	err    error
}

func newFakeTimelineRows(items ...timelineRow) *fakeTimelineRows {
	return &fakeTimelineRows{items: items, idx: -1}
}

func (r *fakeTimelineRows) Close() { r.closed = true }

func (r *fakeTimelineRows) Err() error { return r.err }

func (r *fakeTimelineRows) CommandTag() pgconn.CommandTag { return pgconn.NewCommandTag("SELECT") }

func (r *fakeTimelineRows) FieldDescriptions() []pgconn.FieldDescription { return nil }

func (r *fakeTimelineRows) Next() bool {
	r.idx++
	if r.idx >= len(r.items) {
		r.closed = true
		return false
	}
	return true
}

func (r *fakeTimelineRows) Scan(dest ...any) error {
	if r.idx < 0 || r.idx >= len(r.items) {
		return errors.New("scan without current row")
	}
	item := r.items[r.idx]
	values := []any{
		item.kind,
		item.traceID,
		item.spanID,
		item.parentSpanID,
		item.operation,
		item.service,
		item.status,
		item.durationMs,
		item.attrs,
		item.ts,
	}
	for i, value := range values {
		switch d := dest[i].(type) {
		case *string:
			*d = value.(string)
		case *int:
			*d = value.(int)
		case *[]byte:
			*d = value.([]byte)
		case *time.Time:
			*d = value.(time.Time)
		default:
			return errors.New("unsupported scan destination")
		}
	}
	return nil
}

func (r *fakeTimelineRows) Values() ([]any, error) {
	if r.idx < 0 || r.idx >= len(r.items) {
		return nil, errors.New("no current row")
	}
	item := r.items[r.idx]
	return []any{
		item.kind,
		item.traceID,
		item.spanID,
		item.parentSpanID,
		item.operation,
		item.service,
		item.status,
		item.durationMs,
		item.attrs,
		item.ts,
	}, nil
}

func (r *fakeTimelineRows) RawValues() [][]byte { return nil }

func (r *fakeTimelineRows) Conn() *pgx.Conn { return nil }

func TestPgTracerGetSessionTimelineEmpty(t *testing.T) {
	rows := newFakeTimelineRows()
	querier := &fakeTimelineQuerier{rows: rows}
	tracer := &PgTracer{querier: querier, logger: zap.NewNop()}

	got, err := tracer.GetSessionTimeline(context.Background(), "session-1", 0)
	if err != nil {
		t.Fatalf("GetSessionTimeline failed: %v", err)
	}
	if querier.gotSessionID != "session-1" || querier.gotLimit != 2000 {
		t.Fatalf("query args = session:%q limit:%d", querier.gotSessionID, querier.gotLimit)
	}
	if got.SessionID != "session-1" || got.TraceID != "" || len(got.Items) != 0 || len(got.AgentTree) != 0 {
		t.Fatalf("empty timeline mismatch: %+v", got)
	}
	if !rows.closed {
		t.Fatal("rows should be closed")
	}
}

func TestPgTracerGetSessionTimelineAggregatesQualityEvents(t *testing.T) {
	t1 := time.Date(2026, 5, 6, 10, 0, 2, 0, time.UTC)
	t0 := time.Date(2026, 5, 6, 10, 0, 1, 0, time.UTC)
	delegationAttrs, _ := json.Marshal(map[string]any{
		"quality_event": map[string]any{
			"name":         "quality.delegation",
			"final_status": "pass",
			"delegation": map[string]any{
				"parent_trace_id": "trace-parent",
				"child_trace_id":  "trace-child",
				"agent_id":        "agent-1",
				"agent_type":      "coder",
				"group_id":        "group-1",
			},
		},
	})
	rows := newFakeTimelineRows(
		timelineRow{
			kind:       "quality_event",
			traceID:    "trace-parent",
			spanID:     "log-1",
			operation:  "quality.delegation",
			service:    "agentquality",
			status:     "ok",
			durationMs: 0,
			attrs:      delegationAttrs,
			ts:         t1,
		},
		timelineRow{
			kind:         "span",
			traceID:      "trace-parent",
			spanID:       "span-1",
			parentSpanID: "",
			operation:    "llm.call",
			service:      "master",
			status:       "ok",
			durationMs:   42,
			attrs:        []byte(`{"model":"gpt"}`),
			ts:           t0,
		},
	)
	querier := &fakeTimelineQuerier{rows: rows}
	tracer := &PgTracer{querier: querier, logger: zap.NewNop()}

	got, err := tracer.GetSessionTimeline(context.Background(), "session-1", 100)
	if err != nil {
		t.Fatalf("GetSessionTimeline failed: %v", err)
	}
	if querier.gotLimit != 100 {
		t.Fatalf("limit = %d, want 100", querier.gotLimit)
	}
	if got.TraceID != "trace-parent" {
		t.Fatalf("trace id = %q, want trace-parent", got.TraceID)
	}
	if len(got.Items) != 2 {
		t.Fatalf("items len = %d, want 2", len(got.Items))
	}
	if got.Items[0].Kind != "span" || got.Items[0].Operation != "llm.call" {
		t.Fatalf("items not sorted by timestamp: %+v", got.Items)
	}
	if got.Items[1].Kind != "quality_event" || got.Items[1].Attributes["quality_event"] == nil {
		t.Fatalf("quality event not decoded: %+v", got.Items[1])
	}
	if len(got.AgentTree) != 1 || len(got.AgentTree[0].Children) != 1 {
		t.Fatalf("agent tree mismatch: %+v", got.AgentTree)
	}
	child := got.AgentTree[0].Children[0]
	if child.TraceID != "trace-child" || child.AgentID != "agent-1" || child.Status != "pass" {
		t.Fatalf("child node mismatch: %+v", child)
	}
}
