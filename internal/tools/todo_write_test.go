package tools

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/chef-guo/agents-hive/internal/mcphost"
	"github.com/chef-guo/agents-hive/internal/sessiontodo"
	"github.com/chef-guo/agents-hive/internal/taskboard"
	"github.com/chef-guo/agents-hive/internal/toolctx"
)

type fakeSessionTodoStore struct {
	replaceFunc       func(ctx context.Context, sessionID string, expectedPlanVersion int64, todos []SessionTodoInput) (SessionTodoSnapshot, error)
	snapshotFunc      func(ctx context.Context, sessionID string) (SessionTodoSnapshot, error)
	setPlanStatusFunc func(ctx context.Context, sessionID string, status PlanStatus) (SessionTodoSnapshot, error)
}

func (s *fakeSessionTodoStore) Replace(ctx context.Context, sessionID string, expectedPlanVersion int64, todos []SessionTodoInput) (SessionTodoSnapshot, error) {
	if s.replaceFunc == nil {
		return SessionTodoSnapshot{}, errors.New("replace not implemented")
	}
	return s.replaceFunc(ctx, sessionID, expectedPlanVersion, todos)
}

func (s *fakeSessionTodoStore) Snapshot(ctx context.Context, sessionID string) (SessionTodoSnapshot, error) {
	if s.snapshotFunc == nil {
		return SessionTodoSnapshot{}, errors.New("snapshot not implemented")
	}
	return s.snapshotFunc(ctx, sessionID)
}

func (s *fakeSessionTodoStore) SetPlanStatus(ctx context.Context, sessionID string, status PlanStatus) (SessionTodoSnapshot, error) {
	if s.setPlanStatusFunc == nil {
		return SessionTodoSnapshot{}, errors.New("set plan status not implemented")
	}
	return s.setPlanStatusFunc(ctx, sessionID, status)
}

func (s *fakeSessionTodoStore) SetPlanStatusWithMeta(ctx context.Context, sessionID string, status PlanStatus, meta sessiontodo.SnapshotMeta) (SessionTodoSnapshot, error) {
	return s.SetPlanStatus(ctx, sessionID, status)
}

func (s *fakeSessionTodoStore) ClaimResume(ctx context.Context, sessionID string, expectedPlanVersion int64, expectedRuntimeEpoch, runtimeEpoch, turnID string) (SessionTodoSnapshot, error) {
	return SessionTodoSnapshot{}, errors.New("claim resume not implemented")
}

type fakeTodoBroadcaster struct {
	snapshots []SessionTodoSnapshot
}

func (b *fakeTodoBroadcaster) BroadcastTodoSnapshot(ctx context.Context, snapshot SessionTodoSnapshot) error {
	b.snapshots = append(b.snapshots, snapshot)
	return nil
}

func newTodoWriteTestHost(store SessionTodoStore, broadcaster TodoSnapshotBroadcaster) *mcphost.Host {
	host := mcphost.NewHost(zap.NewNop())
	registerTodoWrite(host, zap.NewNop(), store, broadcaster)
	return host
}

func executeTodoWrite(t *testing.T, host *mcphost.Host, ctx context.Context, input map[string]any) *mcphost.ToolResult {
	t.Helper()
	raw, err := json.Marshal(input)
	require.NoError(t, err)
	result, err := host.ExecuteTool(ctx, "todo_write", raw)
	require.NoError(t, err)
	return result
}

func TestTodoWriteRequiresSessionIDFromContext(t *testing.T) {
	store := &fakeSessionTodoStore{}
	host := newTodoWriteTestHost(store, nil)

	result := executeTodoWrite(t, host, context.Background(), map[string]any{
		"expected_plan_version": 0,
		"todos": []map[string]any{
			{"id": "read", "content": "阅读上下文", "status": "pending"},
		},
	})

	require.True(t, result.IsError)
	require.Contains(t, result.DecodeContent(), "sessionID")
}

func TestTodoWriteRejectsSessionIDInput(t *testing.T) {
	called := false
	store := &fakeSessionTodoStore{
		replaceFunc: func(context.Context, string, int64, []SessionTodoInput) (SessionTodoSnapshot, error) {
			called = true
			return SessionTodoSnapshot{}, nil
		},
	}
	host := newTodoWriteTestHost(store, nil)
	ctx := toolctx.WithSessionID(context.Background(), "sess-ctx")

	result := executeTodoWrite(t, host, ctx, map[string]any{
		"session_id":            "sess-input",
		"expected_plan_version": 0,
		"todos":                 []map[string]any{{"id": "read", "content": "阅读上下文", "status": "pending"}},
	})

	require.True(t, result.IsError)
	require.Contains(t, result.DecodeContent(), "session_id")
	require.False(t, called)
}

func TestTodoWriteRejectsMissingExpectedPlanVersion(t *testing.T) {
	store := &fakeSessionTodoStore{}
	host := newTodoWriteTestHost(store, nil)
	ctx := toolctx.WithSessionID(context.Background(), "sess-ctx")

	result := executeTodoWrite(t, host, ctx, map[string]any{
		"todos": []map[string]any{
			{"id": "read", "content": "阅读上下文", "status": "pending"},
		},
	})

	require.True(t, result.IsError)
	require.Contains(t, result.DecodeContent(), "expected_plan_version")
}

func TestTodoWriteRejectsMissingTodos(t *testing.T) {
	store := &fakeSessionTodoStore{}
	host := newTodoWriteTestHost(store, nil)
	ctx := toolctx.WithSessionID(context.Background(), "sess-ctx")

	result := executeTodoWrite(t, host, ctx, map[string]any{
		"expected_plan_version": 0,
	})

	require.True(t, result.IsError)
	require.Contains(t, result.DecodeContent(), "todos")
}

func TestTodoWriteValidatesTodoSnapshot(t *testing.T) {
	store := &fakeSessionTodoStore{}
	host := newTodoWriteTestHost(store, nil)
	ctx := toolctx.WithSessionID(context.Background(), "sess-ctx")

	result := executeTodoWrite(t, host, ctx, map[string]any{
		"expected_plan_version": 0,
		"todos": []map[string]any{
			{"id": "empty", "content": "  ", "status": "pending"},
		},
	})
	require.True(t, result.IsError)
	require.Contains(t, result.DecodeContent(), "content")

	result = executeTodoWrite(t, host, ctx, map[string]any{
		"expected_plan_version": 0,
		"todos": []map[string]any{
			{"id": "bad-status", "content": "阅读上下文", "status": "blocked"},
		},
	})
	require.True(t, result.IsError)
	require.Contains(t, result.DecodeContent(), "status")

	todos := make([]map[string]any, 101)
	for i := range todos {
		todos[i] = map[string]any{"id": "x", "content": "阅读上下文", "status": "pending"}
	}
	result = executeTodoWrite(t, host, ctx, map[string]any{
		"expected_plan_version": 0,
		"todos":                 todos,
	})
	require.True(t, result.IsError)
	require.Contains(t, result.DecodeContent(), "100")
}

func TestTodoWriteReturnsToolErrorOnCASConflict(t *testing.T) {
	store := &fakeSessionTodoStore{
		replaceFunc: func(context.Context, string, int64, []SessionTodoInput) (SessionTodoSnapshot, error) {
			return SessionTodoSnapshot{}, &PlanVersionConflictError{Expected: 2, Got: 3}
		},
	}
	host := newTodoWriteTestHost(store, nil)
	ctx := toolctx.WithSessionID(context.Background(), "sess-1")

	result := executeTodoWrite(t, host, ctx, map[string]any{
		"expected_plan_version": 2,
		"todos": []map[string]any{
			{"id": "read", "content": "阅读上下文", "status": "pending"},
		},
	})

	require.True(t, result.IsError)
	require.Contains(t, result.DecodeContent(), "plan_version conflict")
	require.Contains(t, result.DecodeContent(), "expected=2")
	require.Contains(t, result.DecodeContent(), "got=3")
}

func TestTodoWritePassesTraceFieldsAndBroadcastsSnapshot(t *testing.T) {
	var capturedSessionID string
	var capturedExpected int64
	var capturedTodos []SessionTodoInput
	snapshot := SessionTodoSnapshot{
		SessionID:        "sess-ctx",
		PlanStatus:       PlanStatusExecuting,
		PlanVersion:      8,
		Source:           "agent",
		TraceID:          "trace-1",
		SpanID:           "span-1",
		TurnID:           "trace-1",
		SourceToolCallID: "call-1",
		UpdatedAt:        time.Unix(100, 0),
	}
	store := &fakeSessionTodoStore{
		replaceFunc: func(_ context.Context, sessionID string, expectedPlanVersion int64, todos []SessionTodoInput) (SessionTodoSnapshot, error) {
			capturedSessionID = sessionID
			capturedExpected = expectedPlanVersion
			capturedTodos = append([]SessionTodoInput(nil), todos...)
			return snapshot, nil
		},
	}
	broadcaster := &fakeTodoBroadcaster{}
	host := newTodoWriteTestHost(store, broadcaster)
	ctx := toolctx.WithSessionID(context.Background(), "sess-ctx")
	ctx = toolctx.WithToolContext(ctx, &toolctx.ToolContext{
		CallerType:   toolctx.CallerMaster,
		CallerName:   "master",
		TraceID:      "trace-1",
		SpanID:       "span-1",
		ParentSpanID: "parent-1",
		ToolCallID:   "call-1",
	})

	result := executeTodoWrite(t, host, ctx, map[string]any{
		"expected_plan_version": 7,
		"todos": []map[string]any{
			{"id": "read", "content": "阅读上下文", "status": "completed"},
			{"id": "write", "content": "写工具测试", "status": "in_progress"},
		},
	})

	require.False(t, result.IsError, result.DecodeContent())
	require.Equal(t, "sess-ctx", capturedSessionID)
	require.Equal(t, int64(7), capturedExpected)
	require.Equal(t, []SessionTodoInput{
		{ID: "read", Content: "阅读上下文", Status: TodoStatusCompleted, Order: 0, Source: "agent", TraceID: "trace-1", SpanID: "span-1", TurnID: "trace-1", SourceToolCallID: "call-1"},
		{ID: "write", Content: "写工具测试", Status: TodoStatusInProgress, Order: 1, Source: "agent", TraceID: "trace-1", SpanID: "span-1", TurnID: "trace-1", SourceToolCallID: "call-1"},
	}, capturedTodos)
	require.Len(t, broadcaster.snapshots, 1)
	require.Equal(t, snapshot, broadcaster.snapshots[0])
	require.Contains(t, result.DecodeContent(), `"plan_version":8`)
}

func TestTodoWriteRejectsNonMasterCaller(t *testing.T) {
	store := &fakeSessionTodoStore{
		replaceFunc: func(context.Context, string, int64, []SessionTodoInput) (SessionTodoSnapshot, error) {
			t.Fatal("Replace should not be called")
			return SessionTodoSnapshot{}, nil
		},
	}
	host := newTodoWriteTestHost(store, nil)
	ctx := toolctx.WithSessionID(context.Background(), "sess-1")
	ctx = toolctx.WithToolContext(ctx, &toolctx.ToolContext{CallerType: toolctx.CallerSubAgent, CallerName: "worker"})

	result := executeTodoWrite(t, host, ctx, map[string]any{
		"expected_plan_version": 0,
		"todos":                 []map[string]any{{"id": "read", "content": "阅读上下文", "status": "pending"}},
	})

	require.True(t, result.IsError)
	require.Contains(t, result.DecodeContent(), "仅允许 Master Agent 调用")
}

func TestRegisterBuiltinToolsRegistersPlanRuntimeToolsWhenStoreProvided(t *testing.T) {
	host := mcphost.NewHost(zap.NewNop())
	store := &fakeSessionTodoStore{}

	RegisterBuiltinTools(host, zap.NewNop(), nil, nil, nil, "", nil, nil, nil, nil, nil, store)

	for _, name := range []string{"todo_write", "enter_plan_mode", "exit_plan_mode", "finish_plan", "create_handoff_summary"} {
		def, err := host.GetTool(name)
		require.NoError(t, err, "expected %s to be registered", name)
		require.True(t, def.Core, "expected %s to be model-visible core tool", name)
	}
	_, err := host.GetTool("promote_todos_to_taskboard")
	require.Error(t, err, "promote_todos_to_taskboard 必须等 taskboard 注入后才注册")
}

func TestRegisterBuiltinToolsRegistersPromoteTodosWhenTaskboardProvided(t *testing.T) {
	host := mcphost.NewHost(zap.NewNop())
	store := &fakeSessionTodoStore{}

	RegisterBuiltinTools(host, zap.NewNop(), nil, nil, nil, "", nil, nil, nil, nil, nil, store, taskboard.NewInMemoryTaskBoard())

	def, err := host.GetTool("promote_todos_to_taskboard")
	require.NoError(t, err)
	require.True(t, def.Core)
}
