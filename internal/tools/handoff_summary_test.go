package tools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/chef-guo/agents-hive/internal/mcphost"
	"github.com/chef-guo/agents-hive/internal/toolctx"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestCreateHandoffSummaryRendersCurrentSnapshot(t *testing.T) {
	store := &fakeSessionTodoStore{
		snapshotFunc: func(_ context.Context, sessionID string) (SessionTodoSnapshot, error) {
			require.Equal(t, "sess-handoff", sessionID)
			return SessionTodoSnapshot{
				SessionID:   sessionID,
				PlanStatus: PlanStatusExecuting,
				PlanVersion: 4,
				Todos: []SessionTodo{
					{ID: "read", Content: "阅读上下文", Status: TodoStatusCompleted},
					{ID: "write", Content: "实现工具", Status: TodoStatusInProgress},
					{ID: "test", Content: "补测试", Status: TodoStatusPending},
				},
			}, nil
		},
	}
	host := mcphost.NewHost(zap.NewNop())
	registerHandoffSummary(host, zap.NewNop(), store)
	ctx := toolctx.WithSessionID(context.Background(), "sess-handoff")
	ctx = toolctx.WithToolContext(ctx, &toolctx.ToolContext{CallerType: toolctx.CallerMaster, CallerName: "master"})

	result, err := host.ExecuteTool(ctx, "create_handoff_summary", json.RawMessage(`{"goal":"交接当前 Wave 2 实施","decisions":["只晋升指定 todo"],"risks":["剩余 Wave 4 有外部依赖"]}`))

	require.NoError(t, err)
	require.False(t, result.IsError, result.DecodeContent())
	text := result.DecodeContent()
	require.Contains(t, text, "# Handoff Summary")
	require.Contains(t, text, "Session: sess-handoff")
	require.Contains(t, text, "Plan status: executing")
	require.Contains(t, text, "- [x] read: 阅读上下文")
	require.Contains(t, text, "- [~] write: 实现工具")
	require.Contains(t, text, "- [ ] test: 补测试")
	require.Contains(t, text, "只晋升指定 todo")
	require.Contains(t, text, "剩余 Wave 4 有外部依赖")
}

func TestCreateHandoffSummaryRejectsNonMasterCaller(t *testing.T) {
	host := mcphost.NewHost(zap.NewNop())
	registerHandoffSummary(host, zap.NewNop(), &fakeSessionTodoStore{})
	ctx := toolctx.WithSessionID(context.Background(), "sess-handoff")
	ctx = toolctx.WithToolContext(ctx, &toolctx.ToolContext{CallerType: toolctx.CallerSubAgent, CallerName: "worker"})

	result, err := host.ExecuteTool(ctx, "create_handoff_summary", json.RawMessage(`{}`))

	require.NoError(t, err)
	require.True(t, result.IsError)
	require.Contains(t, result.DecodeContent(), "仅允许 Master Agent 调用")
}
