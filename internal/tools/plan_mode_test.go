package tools

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/chef-guo/agents-hive/internal/mcphost"
	"github.com/chef-guo/agents-hive/internal/toolctx"
)

func newPlanModeTestHost(store SessionTodoStore, broadcaster TodoSnapshotBroadcaster) *mcphost.Host {
	host := mcphost.NewHost(zap.NewNop())
	registerPlanModeTools(host, zap.NewNop(), store, broadcaster)
	return host
}

func executePlanTool(t *testing.T, host *mcphost.Host, ctx context.Context, name string) *mcphost.ToolResult {
	t.Helper()
	result, err := host.ExecuteTool(ctx, name, json.RawMessage(`{}`))
	require.NoError(t, err)
	return result
}

func TestEnterPlanModeSetsPlanningStatus(t *testing.T) {
	var gotSessionID string
	var gotStatus PlanStatus
	snapshot := SessionTodoSnapshot{
		SessionID:   "sess-1",
		PlanStatus:  PlanStatusPlanning,
		PlanVersion: 1,
		TraceID:     "trace-1",
		SpanID:      "span-1",
		UpdatedAt:   time.Unix(100, 0),
	}
	store := &fakeSessionTodoStore{
		setPlanStatusFunc: func(_ context.Context, sessionID string, status PlanStatus) (SessionTodoSnapshot, error) {
			gotSessionID = sessionID
			gotStatus = status
			return snapshot, nil
		},
	}
	broadcaster := &fakeTodoBroadcaster{}
	host := newPlanModeTestHost(store, broadcaster)
	ctx := toolctx.WithSessionID(context.Background(), "sess-1")
	ctx = toolctx.WithToolContext(ctx, &toolctx.ToolContext{
		CallerType:   toolctx.CallerMaster,
		CallerName:   "master",
		TraceID:      "trace-1",
		SpanID:       "span-1",
		ParentSpanID: "parent-1",
		ToolCallID:   "call-1",
	})

	result := executePlanTool(t, host, ctx, "enter_plan_mode")

	require.False(t, result.IsError, result.DecodeContent())
	require.Equal(t, "sess-1", gotSessionID)
	require.Equal(t, PlanStatusPlanning, gotStatus)
	require.Len(t, broadcaster.snapshots, 1)
	require.Equal(t, snapshot, broadcaster.snapshots[0])
	require.Contains(t, result.DecodeContent(), `"plan_status":"planning"`)
}

func TestExitPlanModeSetsExecutingStatus(t *testing.T) {
	var gotStatus PlanStatus
	store := &fakeSessionTodoStore{
		setPlanStatusFunc: func(_ context.Context, _ string, status PlanStatus) (SessionTodoSnapshot, error) {
			gotStatus = status
			return SessionTodoSnapshot{SessionID: "sess-1", PlanStatus: status, PlanVersion: 2}, nil
		},
	}
	host := newPlanModeTestHost(store, nil)
	ctx := toolctx.WithSessionID(context.Background(), "sess-1")

	result := executePlanTool(t, host, ctx, "exit_plan_mode")

	require.False(t, result.IsError, result.DecodeContent())
	require.Equal(t, PlanStatusExecuting, gotStatus)
	require.NotContains(t, result.DecodeContent(), `"plan_status":"completed"`)
}

func TestFinishPlanFailsWhenTodosRemainOpen(t *testing.T) {
	setCalled := false
	store := &fakeSessionTodoStore{
		snapshotFunc: func(context.Context, string) (SessionTodoSnapshot, error) {
			return SessionTodoSnapshot{
				SessionID:   "sess-1",
				PlanStatus:  PlanStatusExecuting,
				PlanVersion: 2,
				Todos: []SessionTodo{
					{ID: "read", Content: "阅读上下文", Status: TodoStatusCompleted},
					{ID: "write", Content: "写实现", Status: TodoStatusInProgress},
					{ID: "test", Content: "跑测试", Status: TodoStatusPending},
				},
			}, nil
		},
		setPlanStatusFunc: func(context.Context, string, PlanStatus) (SessionTodoSnapshot, error) {
			setCalled = true
			return SessionTodoSnapshot{}, nil
		},
	}
	host := newPlanModeTestHost(store, nil)
	ctx := toolctx.WithSessionID(context.Background(), "sess-1")

	result := executePlanTool(t, host, ctx, "finish_plan")

	require.True(t, result.IsError)
	require.Contains(t, result.DecodeContent(), "仍有未完成 todo")
	require.Contains(t, result.DecodeContent(), "write")
	require.Contains(t, result.DecodeContent(), "test")
	require.False(t, setCalled)
}

func TestFinishPlanCompletesWhenTodosAreClosed(t *testing.T) {
	var gotStatus PlanStatus
	snapshot := SessionTodoSnapshot{SessionID: "sess-1", PlanStatus: PlanStatusCompleted, PlanVersion: 3}
	store := &fakeSessionTodoStore{
		snapshotFunc: func(context.Context, string) (SessionTodoSnapshot, error) {
			return SessionTodoSnapshot{
				SessionID:   "sess-1",
				PlanStatus:  PlanStatusExecuting,
				PlanVersion: 2,
				Todos: []SessionTodo{
					{ID: "read", Content: "阅读上下文", Status: TodoStatusCompleted},
					{ID: "skip", Content: "跳过可选项", Status: TodoStatusCancelled},
				},
			}, nil
		},
		setPlanStatusFunc: func(_ context.Context, _ string, status PlanStatus) (SessionTodoSnapshot, error) {
			gotStatus = status
			return snapshot, nil
		},
	}
	broadcaster := &fakeTodoBroadcaster{}
	host := newPlanModeTestHost(store, broadcaster)
	ctx := toolctx.WithSessionID(context.Background(), "sess-1")

	result := executePlanTool(t, host, ctx, "finish_plan")

	require.False(t, result.IsError, result.DecodeContent())
	require.Equal(t, PlanStatusCompleted, gotStatus)
	require.Len(t, broadcaster.snapshots, 1)
	require.Equal(t, snapshot, broadcaster.snapshots[0])
}

func TestPlanModeToolsRejectMissingSessionID(t *testing.T) {
	store := &fakeSessionTodoStore{}
	host := newPlanModeTestHost(store, nil)

	result := executePlanTool(t, host, context.Background(), "enter_plan_mode")

	require.True(t, result.IsError)
	require.Contains(t, result.DecodeContent(), "sessionID")
}

func TestPlanModeToolsRejectNonMasterCaller(t *testing.T) {
	store := &fakeSessionTodoStore{}
	host := newPlanModeTestHost(store, nil)
	ctx := toolctx.WithSessionID(context.Background(), "sess-1")
	ctx = toolctx.WithToolContext(ctx, &toolctx.ToolContext{CallerType: toolctx.CallerSubAgent, CallerName: "worker"})

	result := executePlanTool(t, host, ctx, "finish_plan")

	require.True(t, result.IsError)
	require.Contains(t, result.DecodeContent(), "仅允许 Master Agent 调用")
}
