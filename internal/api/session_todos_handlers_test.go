package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/chef-guo/agents-hive/internal/config"
	"github.com/chef-guo/agents-hive/internal/errs"
	"github.com/chef-guo/agents-hive/internal/sessiontodo"
	"github.com/chef-guo/agents-hive/internal/store"
)

type fakeSessionTodoStore struct {
	snapshot sessiontodo.Snapshot
	err      error
	calledID string
}

func (f *fakeSessionTodoStore) Snapshot(ctx context.Context, sessionID string) (sessiontodo.Snapshot, error) {
	f.calledID = sessionID
	return f.snapshot, f.err
}

func newSessionTodosTestServer(enabled bool, store sessionTodoSnapshotStore) *Server {
	cfg := config.Default()
	cfg.Agent.PlanRuntime.Enabled = enabled
	s := &Server{
		config: cfg,
		logger: zap.NewNop(),
	}
	s.SetSessionTodoStore(store)
	return s
}

func TestSessionTodosGetSnapshotOK(t *testing.T) {
	updatedAt := time.Date(2026, 5, 2, 9, 30, 0, 0, time.UTC)
	store := &fakeSessionTodoStore{
		snapshot: sessiontodo.Snapshot{
			SessionID:   "sess-1",
			PlanStatus:  sessiontodo.PlanStatusExecuting,
			PlanVersion: 3,
			Todos: []sessiontodo.Todo{{
				ID:        "read-context",
				SessionID: "sess-1",
				Content:   "阅读上下文",
				Status:    sessiontodo.TodoStatusCompleted,
				Order:     0,
				Version:   1,
				CreatedAt: updatedAt,
				UpdatedAt: updatedAt,
			}},
			UpdatedAt: updatedAt,
		},
	}
	s := newSessionTodosTestServer(true, store)
	mux := http.NewServeMux()
	s.registerRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/sess-1/todos", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("期望 200，得到 %d: %s", rec.Code, rec.Body.String())
	}
	if store.calledID != "sess-1" {
		t.Fatalf("Snapshot sessionID = %q, want sess-1", store.calledID)
	}

	var got sessiontodo.Snapshot
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if got.SessionID != "sess-1" || got.PlanStatus != sessiontodo.PlanStatusExecuting || got.PlanVersion != 3 {
		t.Fatalf("snapshot 基础字段不匹配: %+v", got)
	}
	if len(got.Todos) != 1 || got.Todos[0].ID != "read-context" {
		t.Fatalf("todos 不匹配: %+v", got.Todos)
	}
}

func TestSessionTodosFeatureDisabled(t *testing.T) {
	s := newSessionTodosTestServer(false, &fakeSessionTodoStore{})
	mux := http.NewServeMux()
	s.registerRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/sess-1/todos", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("期望 disabled 返回 404，得到 %d: %s", rec.Code, rec.Body.String())
	}
	var got ErrorResponse
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("解析错误响应失败: %v", err)
	}
	if got.Code != errs.CodeNotFound || got.Error == "" {
		t.Fatalf("错误响应不明确: %+v", got)
	}
}

func TestSessionTodosStoreNil(t *testing.T) {
	s := newSessionTodosTestServer(true, nil)
	mux := http.NewServeMux()
	s.registerRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/sess-1/todos", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("期望 nil store 返回 503，得到 %d: %s", rec.Code, rec.Body.String())
	}
	var got ErrorResponse
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("解析错误响应失败: %v", err)
	}
	if got.Code != errs.CodeUnavailable || got.Error == "" {
		t.Fatalf("错误响应不明确: %+v", got)
	}
}

func TestSessionTodosSnapshotNotFound(t *testing.T) {
	s := newSessionTodosTestServer(true, &fakeSessionTodoStore{err: store.ErrNotFound})
	mux := http.NewServeMux()
	s.registerRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/sess-1/todos", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("期望 not found 返回 404，得到 %d: %s", rec.Code, rec.Body.String())
	}
}

func TestSessionTodosSnapshotStoreError(t *testing.T) {
	s := newSessionTodosTestServer(true, &fakeSessionTodoStore{err: errors.New("db down")})
	mux := http.NewServeMux()
	s.registerRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/sess-1/todos", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("期望 store error 返回 500，得到 %d: %s", rec.Code, rec.Body.String())
	}
}
