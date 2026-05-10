package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"go.uber.org/zap"

	"github.com/chef-guo/agents-hive/internal/errs"
	"github.com/chef-guo/agents-hive/internal/mcphost"
	"github.com/chef-guo/agents-hive/internal/router"
)

// taskInput 是 task 工具的输入参数
type taskInput struct {
	AgentID     string                 `json:"agent_id"`          // SubAgent ID（如 "research", "code-review"）
	Instruction string                 `json:"instruction"`       // 任务描述
	Context     map[string]interface{} `json:"context,omitempty"` // 可选的上下文信息
}

// TaskExecutor 接口定义执行任务的能力
// Master Agent 需要实现此接口
type TaskExecutor interface {
	// ExecuteTask 执行一个子任务并返回结果
	// agentID: SubAgent 的 ID
	// instruction: 任务描述
	// context: 任务上下文（可选）
	ExecuteTask(ctx context.Context, agentID string, instruction string, taskContext map[string]interface{}) (string, error)
}

// maxDepth 是允许的最大调用深度，防止无限递归
const maxDepth = 3

// registerTask 注册 task 工具到 MCP host
// executor: TaskExecutor 实现（通常是 Master）
// logger: 日志记录器
func registerTask(host *mcphost.Host, executor TaskExecutor, logger *zap.Logger, observer DelegationObserver, timeout time.Duration) {
	if timeout <= 0 {
		timeout = defaultDelegationTimeout
	}

	schema, _ := json.Marshal(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"agent_id": map[string]any{
				"type":        "string",
				"description": "要调用的 SubAgent ID（如 explore）或 spawn_agent 创建的动态 Agent ID",
			},
			"instruction": map[string]any{
				"type":        "string",
				"description": "要执行的任务描述",
			},
			"context": map[string]any{
				"type":        "object",
				"description": "可选的任务上下文信息",
			},
		},
		"required": []string{"agent_id", "instruction"},
	})

	host.RegisterTool(
		mcphost.ToolDefinition{
			Name:        "task",
			Description: "派发子任务到指定的 SubAgent 执行。仅 Master Agent 可以调用此工具。",
			InputSchema: schema,
		},
		func(ctx context.Context, input json.RawMessage) (*mcphost.ToolResult, error) {
			// 检查调用者权限：允许 Master 和固定 Agent
			toolCtx := GetToolContext(ctx)
			if toolCtx.CallerType != CallerMaster && toolCtx.CallerType != CallerFixedAgent {
				logger.Warn("task 工具调用被拒绝：非授权调用者",
					zap.String("caller_type", string(toolCtx.CallerType)),
					zap.String("caller_name", toolCtx.CallerName),
				)
				return &mcphost.ToolResult{
					Content: jsonText("错误：task 工具仅允许 Master Agent 和固定 Agent 调用"),
					IsError: true,
				}, nil
			}

			// 检查调用深度，防止递归
			if toolCtx.Depth >= maxDepth {
				logger.Warn("task 工具调用被拒绝：超过最大深度",
					zap.Int("depth", toolCtx.Depth),
					zap.Int("max_depth", maxDepth),
				)
				return &mcphost.ToolResult{
					Content: jsonText(fmt.Sprintf("错误：task 调用深度超过最大限制 (%d)", maxDepth)),
					IsError: true,
				}, nil
			}

			// 解析输入参数
			var params taskInput
			if err := json.Unmarshal(input, &params); err != nil {
				return errorResult("输入无效: " + err.Error()), nil
			}

			// 验证必填参数
			if params.AgentID == "" {
				return errorResult("agent_id 不能为空，请指定目标 Agent（如 explore）或使用 spawn_agent 创建临时 Agent"), nil
			}
			if router.IsSystemDelegationAgent(params.AgentID) {
				return errorResult(fmt.Sprintf("agent_id %q 是系统服务 Agent，不接受用户任务委派。请使用 explore 或 spawn_agent 创建临时 Agent", params.AgentID)), nil
			}
			if params.Instruction == "" {
				return errorResult("instruction 不能为空"), nil
			}

			// 防止自委托死锁：Agent 不能委托给自己
			if toolCtx.CallerType == CallerFixedAgent && toolCtx.CallerName == params.AgentID {
				logger.Warn("task 工具调用被拒绝：自委托会导致死锁",
					zap.String("caller", toolCtx.CallerName),
					zap.String("target", params.AgentID),
				)
				return &mcphost.ToolResult{
					Content: jsonText(fmt.Sprintf("错误：Agent %q 不能委托任务给自己（会导致死锁）", params.AgentID)),
					IsError: true,
				}, nil
			}

			logger.Info("执行子任务",
				zap.String("agent_id", params.AgentID),
				zap.String("caller", toolCtx.CallerName),
				zap.Int("depth", toolCtx.Depth),
			)

			// 执行任务（由 runtime policy 控制兜底超时，防止子代理 LLM 卡死无限阻塞 Master 循环）
			execCtx, execCancel := context.WithTimeout(ctx, timeout)
			defer execCancel()
			result, err := executor.ExecuteTask(execCtx, params.AgentID, params.Instruction, params.Context)
			if err != nil {
				// 检查是否是结构化错误
				if e, ok := err.(*errs.Error); ok {
					logger.Error("子任务执行失败",
						zap.String("agent_id", params.AgentID),
						zap.Int("code", e.Code),
						zap.Error(err),
					)
				} else {
					logger.Error("子任务执行失败",
						zap.String("agent_id", params.AgentID),
						zap.Error(err),
					)
				}
				if observer != nil {
					observer.RecordDelegation(ctx, DelegationEvent{
						AgentID:     params.AgentID,
						AgentType:   "subagent",
						SpawnDepth:  toolCtx.Depth + 1,
						Status:      "failed",
						FailureType: "runtime",
						Error:       err.Error(),
					})
				}
				return &mcphost.ToolResult{
					Content: jsonText(fmt.Sprintf("任务执行失败: %v", err)),
					IsError: true,
				}, nil
			}

			logger.Info("子任务执行成功",
				zap.String("agent_id", params.AgentID),
				zap.Int("result_len", len(result)),
			)
			if observer != nil {
				observer.RecordDelegation(ctx, DelegationEvent{
					AgentID:    params.AgentID,
					AgentType:  "subagent",
					SpawnDepth: toolCtx.Depth + 1,
					Status:     "completed",
				})
			}

			return textResult(result), nil
		},
	)
}
