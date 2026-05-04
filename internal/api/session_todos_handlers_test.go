package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/chef-guo/agents-hive/internal/config"
	"github.com/chef-guo/agents-hive/internal/errs"
	"github.com/chef-guo/agents-hive/internal/master"
	"github.com/chef-guo/agents-hive/internal/sessiontodo"
	"github.com/chef-guo/agents-hive/internal/skills"
	"github.com/chef-guo/agents-hive/internal/store"
	"github.com/chef-guo/agents-hive/internal/subagent"
)

type fakeSessionTodoStore struct {
	snapshot      sessiontodo.Snapshot
	err           error
	calledID      string
	claimSnapshot sessiontodo.Snapshot
	claimErr      error
	claimCalls    int
	claimExpected int64
	claimEpoch    string
	claimNewEpoch string
	claimTurnID   string
}

func (f *fakeSessionTodoStore) Snapshot(ctx context.Context, sessionID string) (sessiontodo.Snapshot, error) {
	f.calledID = sessionID
	return f.snapshot, f.err
}

func (f *fakeSessionTodoStore) ClaimResume(ctx context.Context, sessionID string, expectedPlanVersion int64, expectedRuntimeEpoch, runtimeEpoch, turnID string) (sessiontodo.Snapshot, error) {
	f.claimCalls++
	f.calledID = sessionID
	f.claimExpected = expectedPlanVersion
	f.claimEpoch = expectedRuntimeEpoch
	f.claimNewEpoch = runtimeEpoch
	f.claimTurnID = turnID
	if f.claimErr != nil {
		return sessiontodo.Snapshot{}, f.claimErr
	}
	if f.claimSnapshot.SessionID != "" {
		return f.claimSnapshot, nil
	}
	return f.snapshot, nil
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
	todoStore := &fakeSessionTodoStore{
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
	s := newSessionTodosTestServer(true, todoStore)
	mux := http.NewServeMux()
	s.registerRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/sess-1/todos", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("期望 200，得到 %d: %s", rec.Code, rec.Body.String())
	}
	if todoStore.calledID != "sess-1" {
		t.Fatalf("Snapshot sessionID = %q, want sess-1", todoStore.calledID)
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

func TestSessionTodosResumeReturnsContinuationPrompt(t *testing.T) {
	store := &fakeSessionTodoStore{
		snapshot: sessiontodo.Snapshot{
			SessionID:    "sess-1",
			PlanStatus:   sessiontodo.PlanStatusPaused,
			PlanVersion:  7,
			RuntimeEpoch: "epoch-1",
			Todos: []sessiontodo.Todo{
				{ID: "next", Content: "继续实现", Status: sessiontodo.TodoStatusPending},
			},
		},
	}
	s := newSessionTodosTestServer(true, store)
	mux := http.NewServeMux()
	s.registerRoutes(mux)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/sess-1/todos/resume", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("期望 resume 返回 200，得到 %d: %s", rec.Code, rec.Body.String())
	}
	var got sessiontodo.ResumeAction
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if !got.Allowed || got.Prompt == "" || got.PendingTodoIDs[0] != "next" {
		t.Fatalf("resume action 不符合预期: %+v", got)
	}
	if got.PlanVersion != 7 || got.RuntimeEpoch != "epoch-1" {
		t.Fatalf("resume action 缺少 version/epoch: %+v", got)
	}
}

func TestSessionTodosResumeRejectsNoPendingTodos(t *testing.T) {
	todoStore := &fakeSessionTodoStore{
		snapshot: sessiontodo.Snapshot{
			SessionID:  "sess-1",
			PlanStatus: sessiontodo.PlanStatusPaused,
			Todos:      []sessiontodo.Todo{{ID: "done", Status: sessiontodo.TodoStatusCompleted}},
		},
	}
	s := newSessionTodosTestServer(true, todoStore)
	mux := http.NewServeMux()
	s.registerRoutes(mux)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/sess-1/todos/resume", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("期望 no pending 返回 409，得到 %d: %s", rec.Code, rec.Body.String())
	}
}

func TestSessionTodosResumeExecuteRequiresMaster(t *testing.T) {
	todoStore := &fakeSessionTodoStore{
		snapshot: sessiontodo.Snapshot{
			SessionID:    "sess-1",
			PlanStatus:   sessiontodo.PlanStatusPaused,
			PlanVersion:  2,
			RuntimeEpoch: "epoch-1",
			Todos:        []sessiontodo.Todo{{ID: "next", Content: "继续实现", Status: sessiontodo.TodoStatusPending}},
		},
	}
	s := newSessionTodosTestServer(true, todoStore)
	mux := http.NewServeMux()
	s.registerRoutes(mux)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/sess-1/todos/resume", strings.NewReader(`{"execute":true,"expected_plan_version":2,"expected_runtime_epoch":"epoch-1"}`))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("期望 execute 无 master 返回 503，得到 %d: %s", rec.Code, rec.Body.String())
	}
	if todoStore.claimCalls != 0 {
		t.Fatalf("master nil 时不应 claim resume，got %d", todoStore.claimCalls)
	}
}

func TestSessionTodosResumeExecuteRejectsMissingEpochBeforeClaim(t *testing.T) {
	todoStore := &fakeSessionTodoStore{
		snapshot: sessiontodo.Snapshot{
			SessionID:    "sess-1",
			PlanStatus:   sessiontodo.PlanStatusPaused,
			PlanVersion:  2,
			RuntimeEpoch: "epoch-1",
			Todos:        []sessiontodo.Todo{{ID: "next", Content: "继续实现", Status: sessiontodo.TodoStatusPending}},
		},
	}
	s := newSessionTodosTestServer(true, todoStore)
	mux := http.NewServeMux()
	s.registerRoutes(mux)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/sess-1/todos/resume", strings.NewReader(`{"execute":true,"expected_plan_version":2}`))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("期望 missing expected epoch 返回 409，得到 %d: %s", rec.Code, rec.Body.String())
	}
	if todoStore.claimCalls != 0 {
		t.Fatalf("epoch 缺失不应 claim resume，got %d", todoStore.claimCalls)
	}
}

func TestSessionTodosResumeExecuteClaimsBroadcastsAndReturnsSnapshot(t *testing.T) {
	claimed := sessiontodo.Snapshot{
		SessionID:    "sess-1",
		PlanStatus:   sessiontodo.PlanStatusExecuting,
		PlanVersion:  3,
		RuntimeEpoch: "epoch-new",
		TurnID:       "turn-resume",
		Todos:        []sessiontodo.Todo{{ID: "next", Content: "继续实现", Status: sessiontodo.TodoStatusPending}},
	}
	todoStore := &fakeSessionTodoStore{
		snapshot: sessiontodo.Snapshot{
			SessionID:    "sess-1",
			PlanStatus:   sessiontodo.PlanStatusPaused,
			PlanVersion:  2,
			RuntimeEpoch: "epoch-1",
			Todos:        []sessiontodo.Todo{{ID: "next", Content: "继续实现", Status: sessiontodo.TodoStatusPending}},
		},
		claimSnapshot: claimed,
	}
	appStore := store.NewMemoryStore()
	if err := appStore.CreateSession(context.Background(), &store.SessionRecord{ID: "sess-1", Name: "resume test"}); err != nil {
		t.Fatalf("create session record: %v", err)
	}
	m := master.NewMaster(master.Config{Model: "test"}, config.HITLConfig{Enabled: false}, subagent.NewRegistry(zap.NewNop()), skills.NewRegistry(zap.NewNop()), appStore, zap.NewNop())
	subID, events := m.SubscribeWSBroadcast()
	defer m.UnsubscribeWSBroadcast(subID)

	s := newSessionTodosTestServer(true, todoStore)
	s.master = m
	mux := http.NewServeMux()
	s.registerRoutes(mux)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/sess-1/todos/resume", strings.NewReader(`{"execute":true,"expected_plan_version":2,"expected_runtime_epoch":"epoch-1"}`))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("期望 execute resume 返回 200，得到 %d: %s", rec.Code, rec.Body.String())
	}
	if todoStore.claimCalls != 1 || todoStore.claimExpected != 2 || todoStore.claimEpoch != "epoch-1" || todoStore.claimNewEpoch == "" || todoStore.claimTurnID == "" {
		t.Fatalf("claim resume 参数不符合预期: %+v", todoStore)
	}

	var got struct {
		Action   sessiontodo.ResumeAction `json:"action"`
		Snapshot sessiontodo.Snapshot     `json:"snapshot"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if got.Snapshot.PlanStatus != sessiontodo.PlanStatusExecuting || got.Snapshot.PlanVersion != 3 || got.Snapshot.RuntimeEpoch != "epoch-new" {
		t.Fatalf("返回 snapshot 不符合预期: %+v", got.Snapshot)
	}
	assertTodoSnapshotBroadcasted(t, events, claimed)
}

func assertTodoSnapshotBroadcasted(t *testing.T, events <-chan master.BroadcastMessage, want sessiontodo.Snapshot) {
	t.Helper()
	deadline := time.After(time.Second)
	for {
		select {
		case msg := <-events:
			if msg.Type != master.EventTypeTodoSnapshot {
				continue
			}
			got, ok := msg.Payload.(sessiontodo.Snapshot)
			if !ok {
				t.Fatalf("todo_snapshot payload type = %T", msg.Payload)
			}
			if got.SessionID == want.SessionID && got.PlanStatus == want.PlanStatus && got.PlanVersion == want.PlanVersion {
				return
			}
		case <-deadline:
			t.Fatalf("未收到 todo_snapshot 广播: %+v", want)
		}
	}
}
