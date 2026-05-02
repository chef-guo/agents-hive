package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"go.uber.org/zap"

	"github.com/chef-guo/agents-hive/internal/mcphost"
	"github.com/chef-guo/agents-hive/internal/subagent"
)

// mockAgentSpawner 模拟 AgentSpawner 接口
type mockAgentSpawner struct {
	createFn func(ctx context.Context, spec subagent.AgentSpec) (subagent.Agent, error)
	created  []subagent.AgentSpec
}

func (m *mockAgentSpawner) CreateAgent(ctx context.Context, spec subagent.AgentSpec) (subagent.Agent, error) {
	m.created = append(m.created, spec)
	if m.createFn != nil {
		return m.createFn(ctx, spec)
	}
	// 返回一个简单的 mock agent
	card := subagent.AgentCard{ID: "dyn-mock", Name: spec.Name, Description: spec.Description}
	agent := subagent.NewBaseAgent(card, func(ctx context.Context, req subagent.TaskRequest) subagent.TaskResponse {
		return subagent.TaskResponse{Status: "completed", Result: json.RawMessage(`"ok"`)}
	}, nil, zap.NewNop())
	return agent, nil
}

func (m *mockAgentSpawner) DestroyAgent(id string) error {
	return nil
}

func TestRegisterSpawnAgent(t *testing.T) {
	logger := zap.NewNop()
	host := mcphost.NewHost(logger)

	spawner := &mockAgentSpawner{}
	executor := &mockTaskExecutor{}
	registerSpawnAgent(host, executor, spawner, logger, nil, 0)

	// 验证工具已注册
	tools := host.ListTools()
	found := false
	for _, tool := range tools {
		if tool.Name == "spawn_agent" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("spawn_agent 工具未注册")
	}
}

func TestSpawnAgent_MasterCanCall(t *testing.T) {
	logger := zap.NewNop()
	host := mcphost.NewHost(logger)

	spawner := &mockAgentSpawner{}
	executor := &mockTaskExecutor{
		executeFn: func(ctx context.Context, agentID string, instruction string, taskContext map[string]interface{}) (string, error) {
			return "任务执行完成: " + instruction, nil
		},
	}
	registerSpawnAgent(host, executor, spawner, logger, nil, 0)

	ctx := WithToolContext(context.Background(), &ToolContext{
		CallerType: CallerMaster,
		CallerName: "master",
	})

	input, _ := json.Marshal(spawnAgentInput{
		Name:         "数据分析师",
		Description:  "专注数据分析",
		SystemPrompt: "你是数据分析专家",
		Instruction:  "分析数据",
	})

	result, err := host.ExecuteTool(ctx, "spawn_agent", input)
	if err != nil {
		t.Fatalf("调用工具失败: %v", err)
	}
	if result.IsError {
		t.Fatalf("工具返回错误: %s", string(result.Content))
	}

	// 验证 spawner 被调用
	if len(spawner.created) != 1 {
		t.Fatalf("期望创建 1 个 agent，实际创建 %d 个", len(spawner.created))
	}
	if spawner.created[0].Name != "数据分析师" {
		t.Errorf("期望名称=数据分析师，实际=%s", spawner.created[0].Name)
	}
	if spawner.created[0].SystemPrompt != "你是数据分析专家" {
		t.Errorf("期望系统提示词=你是数据分析专家，实际=%s", spawner.created[0].SystemPrompt)
	}
}

func TestSpawnAgent_NonMasterRejected(t *testing.T) {
	logger := zap.NewNop()
	host := mcphost.NewHost(logger)

	spawner := &mockAgentSpawner{}
	executor := &mockTaskExecutor{}
	registerSpawnAgent(host, executor, spawner, logger, nil, 0)

	// SubAgent 上下文
	ctx := WithToolContext(context.Background(), &ToolContext{
		CallerType: CallerSubAgent,
		CallerName: "research",
	})

	input, _ := json.Marshal(spawnAgentInput{
		Name:         "test",
		SystemPrompt: "test",
		Instruction:  "test",
	})

	result, err := host.ExecuteTool(ctx, "spawn_agent", input)
	if err != nil {
		t.Fatalf("调用工具失败: %v", err)
	}
	if !result.IsError {
		t.Fatal("非 Master 调用应返回错误")
	}

	// 验证 spawner 未被调用
	if len(spawner.created) != 0 {
		t.Errorf("非 Master 调用不应创建 agent，实际创建 %d 个", len(spawner.created))
	}
}

func TestSpawnAgent_MissingRequiredFields(t *testing.T) {
	logger := zap.NewNop()
	host := mcphost.NewHost(logger)

	spawner := &mockAgentSpawner{}
	executor := &mockTaskExecutor{}
	registerSpawnAgent(host, executor, spawner, logger, nil, 0)

	ctx := WithToolContext(context.Background(), &ToolContext{
		CallerType: CallerMaster,
		CallerName: "master",
	})

	tests := []struct {
		name  string
		input spawnAgentInput
	}{
		{"缺少 name", spawnAgentInput{SystemPrompt: "test", Instruction: "test"}},
		{"缺少 system_prompt", spawnAgentInput{Name: "test", Instruction: "test"}},
		{"缺少 instruction", spawnAgentInput{Name: "test", SystemPrompt: "test"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input, _ := json.Marshal(tt.input)
			result, err := host.ExecuteTool(ctx, "spawn_agent", input)
			if err != nil {
				t.Fatalf("调用工具失败: %v", err)
			}
			if !result.IsError {
				t.Errorf("缺少必填字段应返回错误")
			}
		})
	}
}

func TestSpawnAgent_CreateFailure(t *testing.T) {
	logger := zap.NewNop()
	host := mcphost.NewHost(logger)

	spawner := &mockAgentSpawner{
		createFn: func(ctx context.Context, spec subagent.AgentSpec) (subagent.Agent, error) {
			return nil, fmt.Errorf("达到上限")
		},
	}
	executor := &mockTaskExecutor{}
	registerSpawnAgent(host, executor, spawner, logger, nil, 0)

	ctx := WithToolContext(context.Background(), &ToolContext{
		CallerType: CallerMaster,
		CallerName: "master",
	})

	input, _ := json.Marshal(spawnAgentInput{
		Name:         "test",
		SystemPrompt: "test",
		Instruction:  "test",
	})

	result, err := host.ExecuteTool(ctx, "spawn_agent", input)
	if err != nil {
		t.Fatalf("调用工具失败: %v", err)
	}
	if !result.IsError {
		t.Fatal("创建失败时应返回错误")
	}
}

func TestSpawnAgent_ExecuteFailure(t *testing.T) {
	logger := zap.NewNop()
	host := mcphost.NewHost(logger)

	spawner := &mockAgentSpawner{}
	executor := &mockTaskExecutor{
		executeFn: func(ctx context.Context, agentID string, instruction string, taskContext map[string]interface{}) (string, error) {
			return "", fmt.Errorf("执行超时")
		},
	}
	registerSpawnAgent(host, executor, spawner, logger, nil, 0)

	ctx := WithToolContext(context.Background(), &ToolContext{
		CallerType: CallerMaster,
		CallerName: "master",
	})

	input, _ := json.Marshal(spawnAgentInput{
		Name:         "test",
		SystemPrompt: "test",
		Instruction:  "test",
	})

	result, err := host.ExecuteTool(ctx, "spawn_agent", input)
	if err != nil {
		t.Fatalf("调用工具失败: %v", err)
	}
	if !result.IsError {
		t.Fatal("执行失败时应返回错误")
	}
}

func TestSpawnAgent_WithToolWhitelist(t *testing.T) {
	logger := zap.NewNop()
	host := mcphost.NewHost(logger)

	spawner := &mockAgentSpawner{}
	executor := &mockTaskExecutor{
		executeFn: func(ctx context.Context, agentID string, instruction string, taskContext map[string]interface{}) (string, error) {
			return "done", nil
		},
	}
	registerSpawnAgent(host, executor, spawner, logger, nil, 0)

	ctx := WithToolContext(context.Background(), &ToolContext{
		CallerType: CallerMaster,
		CallerName: "master",
	})

	input, _ := json.Marshal(spawnAgentInput{
		Name:         "受限 Agent",
		SystemPrompt: "test",
		Tools:        []string{"read_file", "grep"},
		Instruction:  "搜索代码",
	})

	result, err := host.ExecuteTool(ctx, "spawn_agent", input)
	if err != nil {
		t.Fatalf("调用工具失败: %v", err)
	}
	if result.IsError {
		t.Fatalf("工具返回错误: %s", string(result.Content))
	}

	// 验证 tools 白名单传递正确
	if len(spawner.created) != 1 {
		t.Fatalf("期望 1 个 agent")
	}
	if len(spawner.created[0].Tools) != 2 {
		t.Errorf("期望 2 个工具白名单，实际 %d", len(spawner.created[0].Tools))
	}
}

func TestSpawnAgent_RejectsPlanControlToolWhitelist(t *testing.T) {
	logger := zap.NewNop()
	host := mcphost.NewHost(logger)

	spawner := &mockAgentSpawner{}
	executor := &mockTaskExecutor{}
	registerSpawnAgent(host, executor, spawner, logger, nil, 0)

	ctx := WithToolContext(context.Background(), &ToolContext{
		CallerType: CallerMaster,
		CallerName: "master",
	})

	input, _ := json.Marshal(spawnAgentInput{
		Name:         "受限 Agent",
		SystemPrompt: "test",
		Tools:        []string{"read_file", "todo_write"},
		Instruction:  "搜索代码",
	})

	result, err := host.ExecuteTool(ctx, "spawn_agent", input)
	if err != nil {
		t.Fatalf("调用工具失败: %v", err)
	}
	if !result.IsError {
		t.Fatal("SubAgent 工具白名单包含 plan/todo 控制工具时应返回错误")
	}
	if len(spawner.created) != 0 {
		t.Fatalf("拒绝后不应创建 agent，实际创建 %d 个", len(spawner.created))
	}

	var errMsg string
	if err := json.Unmarshal(result.Content, &errMsg); err != nil {
		t.Fatalf("解析错误消息失败: %v", err)
	}
	if !contains(errMsg, "todo_write") {
		t.Fatalf("错误消息应包含被拒绝工具名: %q", errMsg)
	}
}

func TestSpawnAgent_PropagatesDepthAndMaxTurns(t *testing.T) {
	logger := zap.NewNop()
	host := mcphost.NewHost(logger)
	observer := &recordingDelegationObserver{}
	spawner := &mockAgentSpawner{}
	executor := &mockTaskExecutor{
		executeFn: func(ctx context.Context, agentID string, instruction string, taskContext map[string]interface{}) (string, error) {
			return "ok", nil
		},
	}
	registerSpawnAgent(host, executor, spawner, logger, observer, 0)

	ctx := WithToolContext(context.Background(), &ToolContext{
		CallerType: CallerMaster,
		CallerName: "master",
		Depth:      2,
	})
	input, _ := json.Marshal(spawnAgentInput{
		Name:         "researcher",
		SystemPrompt: "test",
		Instruction:  "test",
		MaxTurns:     7,
	})

	result, err := host.ExecuteTool(ctx, "spawn_agent", input)

	if err != nil {
		t.Fatalf("调用工具失败: %v", err)
	}
	if result.IsError {
		t.Fatalf("工具返回错误: %s", string(result.Content))
	}
	if len(spawner.created) != 1 {
		t.Fatalf("created specs = %d, want 1", len(spawner.created))
	}
	if spawner.created[0].SpawnDepth != 3 {
		t.Fatalf("SpawnDepth = %d, want 3", spawner.created[0].SpawnDepth)
	}
	if spawner.created[0].MaxTurns != 7 {
		t.Fatalf("MaxTurns = %d, want 7", spawner.created[0].MaxTurns)
	}
	events := observer.snapshot()
	if len(events) != 1 {
		t.Fatalf("delegation events = %d, want 1", len(events))
	}
	if events[0].SpawnDepth != 3 {
		t.Fatalf("event SpawnDepth = %d, want 3", events[0].SpawnDepth)
	}
	if events[0].MaxTurns != 7 {
		t.Fatalf("event MaxTurns = %d, want 7", events[0].MaxTurns)
	}
}
