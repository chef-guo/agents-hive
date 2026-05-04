package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/chef-guo/agents-hive/internal/config"
	"github.com/chef-guo/agents-hive/internal/observability"
	"github.com/chef-guo/agents-hive/internal/sessiontodo"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

type fakeSessionTodoOpsReader struct {
	input sessiontodo.OpsDashboardInput
	err   error
	since time.Time
	until time.Time
}

func (r *fakeSessionTodoOpsReader) LoadOps(ctx context.Context, since, until time.Time) (sessiontodo.OpsDashboardInput, error) {
	r.since = since
	r.until = until
	return r.input, r.err
}

func TestAdminSessionTodoOpsSnapshot(t *testing.T) {
	now := time.Date(2026, 5, 2, 10, 0, 0, 0, time.UTC)
	reader := &fakeSessionTodoOpsReader{
		input: sessiontodo.OpsDashboardInput{
			Metrics: []observability.Metric{
				{Name: sessiontodo.MetricSessionTodoWritesTotal, Value: 10, Labels: map[string]any{"status": "ok"}, Ts: now.Add(-10 * time.Minute)},
				{Name: sessiontodo.MetricPlanRuntimeDecisionsTotal, Value: 1, Labels: map[string]any{"decision": "paused"}, Ts: now.Add(-5 * time.Minute)},
			},
		},
	}
	srv := newSessionTodoOpsTestServer(true, reader)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/sessiontodo/ops/snapshot?until=2026-05-02T10:00:00Z&window_minutes=60", nil)
	rec := httptest.NewRecorder()

	srv.handleAdminSessionTodoOpsSnapshot(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, now.Add(-time.Hour), reader.since)
	require.Equal(t, now, reader.until)
	var got sessiontodo.OpsDashboardSnapshot
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&got))
	require.Equal(t, 10.0, got.TodoWritesTotal)
	require.Equal(t, 1.0, got.PlanRuntimeDecisions["paused"])
}

func TestAdminSessionTodoOpsSnapshotUnavailableWithoutReader(t *testing.T) {
	srv := newSessionTodoOpsTestServer(true, nil)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/sessiontodo/ops/snapshot", nil)
	rec := httptest.NewRecorder()

	srv.handleAdminSessionTodoOpsSnapshot(rec, req)

	require.Equal(t, http.StatusServiceUnavailable, rec.Code, rec.Body.String())
}

func TestAdminSessionTodoOpsSnapshotReaderError(t *testing.T) {
	srv := newSessionTodoOpsTestServer(true, &fakeSessionTodoOpsReader{err: errors.New("db down")})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/sessiontodo/ops/snapshot", nil)
	rec := httptest.NewRecorder()

	srv.handleAdminSessionTodoOpsSnapshot(rec, req)

	require.Equal(t, http.StatusInternalServerError, rec.Code, rec.Body.String())
}

func newSessionTodoOpsTestServer(enabled bool, reader sessionTodoOpsReader) *Server {
	cfg := config.Default()
	cfg.Agent.PlanRuntime.Enabled = enabled
	s := &Server{config: cfg, logger: zap.NewNop()}
	s.SetSessionTodoOpsReader(reader)
	return s
}
