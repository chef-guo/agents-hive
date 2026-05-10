package master

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/chef-guo/agents-hive/internal/llm"
	"github.com/chef-guo/agents-hive/internal/mcphost"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type resultMutatingToolMiddleware struct{}

func (resultMutatingToolMiddleware) WrapToolCall(ctx context.Context, call *ToolCall, next ToolExecutor) (*ToolResult, error) {
	result, err := next(ctx, call)
	if err != nil || result == nil || result.Result == nil {
		return result, err
	}
	decoded := mcphost.DecodeToolContent(result.Result.Content)
	result.Result.Content = jsonTestText("wrapped:" + decoded)
	return result, nil
}

type blockingToolMiddleware struct{}

func (blockingToolMiddleware) WrapToolCall(context.Context, *ToolCall, ToolExecutor) (*ToolResult, error) {
	return nil, errors.New("middleware blocked tool")
}

func TestExecuteTool_RunsToolCallMiddleware(t *testing.T) {
	m := newPhase6MasterWithMCPHost(t)
	m.middlewarePipeline = NewMiddlewarePipeline(resultMutatingToolMiddleware{})
	m.mcpHost.RegisterTool(
		mcphost.ToolDefinition{Name: "echo", Description: "test"},
		func(context.Context, json.RawMessage) (*mcphost.ToolResult, error) {
			return &mcphost.ToolResult{Content: jsonTestText("core")}, nil
		},
	)

	result := m.executeTool(context.Background(), newTestSession("mw-wrap"), "", llm.ToolCall{
		ID:        "call-1",
		Name:      "echo",
		Arguments: json.RawMessage(`{}`),
	}, "", "")

	require.False(t, result.IsError)
	assert.Equal(t, "wrapped:core", result.Content)
}

func TestExecuteTool_ToolCallMiddlewareCanBlockExecution(t *testing.T) {
	m := newPhase6MasterWithMCPHost(t)
	m.middlewarePipeline = NewMiddlewarePipeline(blockingToolMiddleware{})
	called := false
	m.mcpHost.RegisterTool(
		mcphost.ToolDefinition{Name: "dangerous", Description: "test"},
		func(context.Context, json.RawMessage) (*mcphost.ToolResult, error) {
			called = true
			return &mcphost.ToolResult{Content: jsonTestText("should not run")}, nil
		},
	)

	result := m.executeTool(context.Background(), newTestSession("mw-block"), "", llm.ToolCall{
		ID:        "call-2",
		Name:      "dangerous",
		Arguments: json.RawMessage(`{}`),
	}, "", "")

	require.True(t, result.IsError)
	assert.False(t, called, "middleware 阻断时不应执行底层工具")
	assert.Contains(t, result.Content, "middleware blocked tool")
}

func TestExecuteTool_RejectsSkillNameOutsideRouteDecision(t *testing.T) {
	m := newPhase6MasterWithMCPHost(t)
	called := false
	m.mcpHost.RegisterTool(
		mcphost.ToolDefinition{Name: "skill", Description: "call skill"},
		func(context.Context, json.RawMessage) (*mcphost.ToolResult, error) {
			called = true
			return &mcphost.ToolResult{Content: jsonTestText("should not run")}, nil
		},
	)
	session := newTestSession("route-skill")
	session.SetAllowedToolInputs(map[string]map[string]string{"skill": {"name": "skill-creator"}})

	result := m.executeTool(context.Background(), session, "", llm.ToolCall{
		ID:        "call-route-denied",
		Name:      "skill",
		Arguments: json.RawMessage(`{"name":"mcp-builder","arguments":"create greeting skill"}`),
	}, "", "")

	require.True(t, result.IsError)
	require.True(t, result.Terminal)
	assert.False(t, called, "RouteDecision 拒绝时不应执行 skill 工具")
	assert.Contains(t, result.Content, "RouteDecision 拒绝")
	assert.Contains(t, result.Content, "mcp-builder")
	assert.Contains(t, result.Content, "skill-creator")
}
