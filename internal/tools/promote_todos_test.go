package tools

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/chef-guo/agents-hive/internal/mcphost"
	"github.com/chef-guo/agents-hive/internal/taskboard"
	"github.com/chef-guo/agents-hive/internal/toolctx"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestPromoteTodosToTaskboardCreatesTasksAndCancelsTodos(t *testing.T) {
	ctx := toolctx.WithSessionID(context.Background(), "sess-promote")
	ctx = toolctx.WithToolContext(ctx, &toolctx.ToolContext{CallerType: toolctx.CallerMaster, CallerName: "master"})
	board := taskboard.NewInMemoryTaskBoard()
	original := SessionTodoSnapshot{
		SessionID:   "sess-promote",
		PlanStatus:  PlanStatusExecuting,
		PlanVersion: 7,
		Source:      "agent",
		TraceID:     "trace-promote",
		SpanID:      "span-promote",
		Todos: []SessionTodo{
			{ID: "keep", Content: "继续留在 session todo", Status: TodoStatusPending},
			{ID: "promote", Content: "长期跟踪的工作项", Status: TodoStatusPending, TurnID: "turn-promote", RuntimeEpoch: "epoch-promote", SourceChangeID: "change-promote", SourceRevision: 3},
		},
	}
	var replaced []SessionTodoInput
	store := &fakeSessionTodoStore{
		snapshotFunc: func(context.Context, string) (SessionTodoSnapshot, error) {
			return original, nil
		},
		replaceFunc: func(_ context.Context, sessionID string, expectedPlanVersion int64, todos []SessionTodoInput) (SessionTodoSnapshot, error) {
			require.Equal(t, "sess-promote", sessionID)
			require.Equal(t, int64(7), expectedPlanVersion)
			replaced = append([]SessionTodoInput(nil), todos...)
			return SessionTodoSnapshot{SessionID: sessionID, PlanStatus: PlanStatusExecuting, PlanVersion: 8, Todos: []SessionTodo{
				{ID: "keep", Content: "继续留在 session todo", Status: TodoStatusPending},
				{ID: "promote", Content: "长期跟踪的工作项", Status: TodoStatusCancelled},
			}}, nil
		},
	}
	broadcaster := &fakeTodoBroadcaster{}
	host := mcphost.NewHost(zap.NewNop())
	registerPromoteTodosToTaskboard(host, zap.NewNop(), store, broadcaster, board)

	result, err := host.ExecuteTool(ctx, "promote_todos_to_taskboard", json.RawMessage(`{"todo_ids":["promote"],"priority":"high","tags":["follow-up"]}`))

	require.NoError(t, err)
	require.False(t, result.IsError, result.DecodeContent())
	require.Len(t, replaced, 2)
	require.Equal(t, TodoStatusPending, replaced[0].Status)
	require.Equal(t, TodoStatusCancelled, replaced[1].Status)
	require.Equal(t, "turn-promote", replaced[1].TurnID)
	require.Equal(t, "epoch-promote", replaced[1].RuntimeEpoch)
	require.Equal(t, "change-promote", replaced[1].SourceChangeID)
	require.Equal(t, int64(3), replaced[1].SourceRevision)
	require.Len(t, broadcaster.snapshots, 1)
	tasks, err := board.List(context.Background(), taskboard.TaskFilter{SessionID: "sess-promote"})
	require.NoError(t, err)
	require.Len(t, tasks, 1)
	require.Equal(t, "长期跟踪的工作项", tasks[0].Title)
	require.Equal(t, "Promoted from session todo promote", tasks[0].Description)
	require.Equal(t, taskboard.PriorityHigh, tasks[0].Priority)
	require.Equal(t, []string{"sessiontodo", "follow-up"}, tasks[0].Tags)
	require.Contains(t, result.DecodeContent(), `"promoted_count":1`)
}

func TestPromoteTodosToTaskboardRejectsUnknownTodoID(t *testing.T) {
	store := &fakeSessionTodoStore{
		snapshotFunc: func(context.Context, string) (SessionTodoSnapshot, error) {
			return SessionTodoSnapshot{
				SessionID:   "sess-promote",
				PlanVersion: 1,
				Todos:       []SessionTodo{{ID: "known", Content: "已知", Status: TodoStatusPending}},
			}, nil
		},
		replaceFunc: func(context.Context, string, int64, []SessionTodoInput) (SessionTodoSnapshot, error) {
			t.Fatal("Replace should not be called for unknown todo id")
			return SessionTodoSnapshot{}, nil
		},
	}
	host := mcphost.NewHost(zap.NewNop())
	registerPromoteTodosToTaskboard(host, zap.NewNop(), store, nil, taskboard.NewInMemoryTaskBoard())
	ctx := toolctx.WithSessionID(context.Background(), "sess-promote")
	ctx = toolctx.WithToolContext(ctx, &toolctx.ToolContext{CallerType: toolctx.CallerMaster, CallerName: "master"})

	result, err := host.ExecuteTool(ctx, "promote_todos_to_taskboard", json.RawMessage(`{"todo_ids":["missing"]}`))

	require.NoError(t, err)
	require.True(t, result.IsError)
	require.Contains(t, result.DecodeContent(), "unknown todo id")
}

func TestPromoteTodosToTaskboardRollsBackCreatedTasksOnReplaceFailure(t *testing.T) {
	ctx := toolctx.WithSessionID(context.Background(), "sess-promote")
	ctx = toolctx.WithToolContext(ctx, &toolctx.ToolContext{CallerType: toolctx.CallerMaster, CallerName: "master"})
	board := taskboard.NewInMemoryTaskBoard()
	store := &fakeSessionTodoStore{
		snapshotFunc: func(context.Context, string) (SessionTodoSnapshot, error) {
			return SessionTodoSnapshot{
				SessionID:   "sess-promote",
				PlanStatus:  PlanStatusExecuting,
				PlanVersion: 3,
				Todos: []SessionTodo{
					{ID: "promote", Content: "长期跟踪的工作项", Status: TodoStatusPending},
				},
			}, nil
		},
		replaceFunc: func(context.Context, string, int64, []SessionTodoInput) (SessionTodoSnapshot, error) {
			return SessionTodoSnapshot{}, errors.New("cas conflict")
		},
	}
	host := mcphost.NewHost(zap.NewNop())
	registerPromoteTodosToTaskboard(host, zap.NewNop(), store, nil, board)

	result, err := host.ExecuteTool(ctx, "promote_todos_to_taskboard", json.RawMessage(`{"todo_ids":["promote"]}`))

	require.NoError(t, err)
	require.True(t, result.IsError)
	tasks, err := board.List(context.Background(), taskboard.TaskFilter{SessionID: "sess-promote"})
	require.NoError(t, err)
	require.Empty(t, tasks)
}
