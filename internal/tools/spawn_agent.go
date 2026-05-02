package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"go.uber.org/zap"

	"github.com/chef-guo/agents-hive/internal/mcphost"
	"github.com/chef-guo/agents-hive/internal/subagent"
)

const defaultDelegationTimeout = 30 * time.Minute

// spawnAgentInput 是 spawn_agent 工具的输入参数
type spawnAgentInput struct {
	Name         string   `json:"name"`                // Agent 名称
	Description  string   `json:"description"`         // 能力描述
	SystemPrompt string   `json:"system_prompt"`       // 系统提示词
	Tools        []string `json:"tools,omitempty"`     // 工具白名单
	Instruction  string   `json:"instruction"`         // 创建后立即执行的任务指令
	MaxTurns     int      `json:"max_turns,omitempty"` // 最大迭代轮次
}

// AgentSpawner 接口定义创建和销毁动态 Agent 的能力
type AgentSpawner interface {
	CreateAgent(ctx context.Context, spec subagent.AgentSpec) (subagent.Agent, error)
	DestroyAgent(id string) error
}

// registerSpawnAgent 注册 spawn_agent 工具到 MCP host
func registerSpawnAgent(host *mcphost.Host, executor TaskExecutor, spawner AgentSpawner, logger *zap.Logger, observer DelegationObserver, timeout time.Duration) {
	if timeout <= 0 {
		timeout = defaultDelegationTimeout
	}

	schema, _ := json.Marshal(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"name": map[string]any{
				"type":        "string",
				"description": "新 Agent 的名称",
			},
			"description": map[string]any{
				"type":        "string",
				"description": "Agent 的能力描述",
			},
			"system_prompt": map[string]any{
				"type":        "string",
				"description": "Agent 的系统提示词，定义其角色和行为",
			},
			"tools": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "允许使用的工具白名单（空表示允许所有工具）",
			},
			"instruction": map[string]any{
				"type":        "string",
				"description": "创建 Agent 后立即执行的任务指令",
			},
			"max_turns": map[string]any{
				"type":        "integer",
				"description": "子 Agent 最大迭代轮次，空或 <=0 使用系统默认值",
			},
		},
		"required": []string{"name", "system_prompt", "instruction"},
	})

	host.RegisterTool(
		mcphost.ToolDefinition{
			Name:        "spawn_agent",
			Description: "动态创建专用 Agent 并立即执行任务。当你需要访问数据库（MySQL/PostgreSQL/Redis等）、查询监控系统（Grafana/Prometheus）、调用外部API、或执行任何需要专业知识的操作时，创建一个带有专用 system_prompt 的 Agent。该 Agent 通常使用 bash 工具通过命令行完成实际操作。",
			InputSchema: schema,
			Core:        true,
		},
		func(ctx context.Context, input json.RawMessage) (*mcphost.ToolResult, error) {
			// 检查调用者权限：仅 Master 可调用
			toolCtx := GetToolContext(ctx)
			if toolCtx.CallerType != CallerMaster {
				logger.Warn("spawn_agent 工具调用被拒绝：非 Master 调用",
					zap.String("caller_type", string(toolCtx.CallerType)),
				)
				return &mcphost.ToolResult{
					Content: jsonText("错误：spawn_agent 工具仅允许 Master Agent 调用"),
					IsError: true,
				}, nil
			}

			var params spawnAgentInput
			if err := json.Unmarshal(input, &params); err != nil {
				return errorResult("输入无效: " + err.Error()), nil
			}

			if params.Name == "" {
				return errorResult("name 不能为空"), nil
			}
			if params.SystemPrompt == "" {
				return errorResult("system_prompt 不能为空"), nil
			}
			if params.Instruction == "" {
				return errorResult("instruction 不能为空"), nil
			}
			if denied := deniedPlanControlTools(params.Tools); len(denied) > 0 {
				return errorResult(fmt.Sprintf("tools 包含 SubAgent 不允许使用的 plan/todo 控制工具: %v", denied)), nil
			}

			logger.Info("spawn_agent: 创建动态 Agent",
				zap.String("name", params.Name),
				zap.Int("tools", len(params.Tools)),
			)

			// 创建 Agent
			spec := subagent.AgentSpec{
				Name:         params.Name,
				Description:  params.Description,
				SystemPrompt: params.SystemPrompt,
				Tools:        params.Tools,
				MaxTurns:     params.MaxTurns,
				SpawnDepth:   toolCtx.Depth + 1,
			}

			agent, err := spawner.CreateAgent(ctx, spec)
			if err != nil {
				logger.Error("spawn_agent: 创建 Agent 失败",
					zap.String("name", params.Name),
					zap.Error(err),
				)
				if observer != nil {
					observer.RecordDelegation(ctx, DelegationEvent{
						AgentID:       params.Name,
						AgentType:     "subagent",
						ToolWhitelist: append([]string(nil), params.Tools...),
						SpawnDepth:    toolCtx.Depth + 1,
						MaxTurns:      params.MaxTurns,
						Status:        "failed",
						FailureType:   "runtime",
						Error:         err.Error(),
					})
				}
				return &mcphost.ToolResult{
					Content: jsonText(fmt.Sprintf("创建 Agent 失败: %v", err)),
					IsError: true,
				}, nil
			}

			// 立即执行任务（由 runtime policy 控制兜底超时，防止子代理 LLM 卡死无限阻塞 Master 循环）
			execCtx, execCancel := context.WithTimeout(ctx, timeout)
			defer execCancel()
			result, err := executor.ExecuteTask(execCtx, agent.ID(), params.Instruction, nil)

			// 执行完成后立即销毁 agent，释放 per-session 配额
			// （无论成功失败都释放，避免顺序 spawn 耗尽配额）
			agentID := agent.ID()
			if destroyErr := spawner.DestroyAgent(agentID); destroyErr != nil {
				logger.Warn("spawn_agent: 销毁 Agent 失败",
					zap.String("agent_id", agentID),
					zap.Error(destroyErr),
				)
			}

			if err != nil {
				logger.Error("spawn_agent: 任务执行失败",
					zap.String("agent_id", agentID),
					zap.Error(err),
				)
				if observer != nil {
					observer.RecordDelegation(ctx, DelegationEvent{
						AgentID:       agentID,
						AgentType:     "subagent",
						ToolWhitelist: append([]string(nil), params.Tools...),
						SpawnDepth:    toolCtx.Depth + 1,
						MaxTurns:      params.MaxTurns,
						Status:        "failed",
						FailureType:   "runtime",
						Error:         err.Error(),
					})
				}
				return &mcphost.ToolResult{
					Content: jsonText(fmt.Sprintf("Agent %q 已创建但任务执行失败: %v", agentID, err)),
					IsError: true,
				}, nil
			}

			logger.Info("spawn_agent: 任务执行完成",
				zap.String("agent_id", agentID),
				zap.Int("result_len", len(result)),
			)
			if observer != nil {
				observer.RecordDelegation(ctx, DelegationEvent{
					AgentID:       agentID,
					AgentType:     "subagent",
					ToolWhitelist: append([]string(nil), params.Tools...),
					SpawnDepth:    toolCtx.Depth + 1,
					MaxTurns:      params.MaxTurns,
					Status:        "completed",
				})
			}

			return textResult(fmt.Sprintf("Agent %q (%s) 已创建并执行完成。\n\n结果:\n%s", agentID, params.Name, result)), nil
		},
	)
}

func deniedPlanControlTools(toolNames []string) []string {
	if len(toolNames) == 0 {
		return nil
	}
	denied := make([]string, 0)
	for _, name := range toolNames {
		switch name {
		case todoWriteToolName, finishPlanToolName, enterPlanModeToolName, exitPlanModeToolName:
			denied = append(denied, name)
		}
	}
	return denied
}
