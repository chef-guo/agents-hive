package tools

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/chef-guo/agents-hive/internal/mcphost"
)

// parallelDispatchInput 是 parallel_dispatch 工具的输入参数
type parallelDispatchInput struct {
	Tasks          []parallelTaskItem `json:"tasks"`
	MaxConcurrency int                `json:"max_concurrency,omitempty"` // 最大并发数（默认 10）
	TimeoutSeconds int                `json:"timeout_seconds,omitempty"` // 每任务超时（秒，默认 300）
}

// parallelTaskItem 单个并行任务描述
type parallelTaskItem struct {
	ID          string                 `json:"id,omitempty"`
	AgentID     string                 `json:"agent_id,omitempty"`
	Instruction string                 `json:"instruction"`
	Context     map[string]interface{} `json:"context,omitempty"`
}

// parallelTaskResult 单个任务的执行结果
type parallelTaskResult struct {
	ID      string `json:"id"`
	AgentID string `json:"agent_id"`
	Status  string `json:"status"` // "completed", "failed"
	Result  string `json:"result,omitempty"`
	Error   string `json:"error,omitempty"`
}

// ParallelDispatchBroadcaster 广播接口，用于发送任务组事件
type ParallelDispatchBroadcaster interface {
	// BroadcastTaskGroup 广播任务组生命周期事件
	BroadcastTaskGroup(groupID string, status string, total int, tasks interface{}, results interface{})
	// BroadcastTaskProgress 广播单个任务进度事件
	BroadcastTaskProgress(groupID string, taskID string, status string, err string)
}

// maxParallelTasks 最大任务数
const maxParallelTasks = 10

// defaultMaxConcurrency 默认最大并发数
const defaultMaxConcurrency = 10

// defaultTaskTimeout 默认每任务超时时间
const defaultTaskTimeout = 5 * time.Minute

// maxTaskTimeout 每任务超时上限（30 分钟）
const maxTaskTimeout = 30 * time.Minute

// registerParallelDispatch 注册 parallel_dispatch 工具到 MCP host
// executor: TaskExecutor 实现（通常是 Master）
// broadcaster: 广播接口（可选，用于实时广播任务组事件）
// logger: 日志记录器
func registerParallelDispatch(host *mcphost.Host, executor TaskExecutor, broadcaster ParallelDispatchBroadcaster, logger *zap.Logger, observer DelegationObserver, gates ...NestedToolGate) {
	var gate NestedToolGate
	if len(gates) > 0 {
		gate = gates[0]
	}

	schema, _ := json.Marshal(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"tasks": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"id": map[string]any{
							"type":        "string",
							"description": "任务唯一标识（可选，自动生成）",
						},
						"agent_id": map[string]any{
							"type":        "string",
							"description": "要调用的 SubAgent ID（如 explore）或 spawn_agent 创建的动态 Agent ID",
						},
						"instruction": map[string]any{
							"type":        "string",
							"description": "任务描述",
						},
						"context": map[string]any{
							"type":        "object",
							"description": "可选的任务上下文",
						},
					},
					"required": []string{"agent_id", "instruction"},
				},
				"maxItems": maxParallelTasks,
			},
			"max_concurrency": map[string]any{
				"type":        "integer",
				"description": "最大并发数（默认 10）",
			},
			"timeout_seconds": map[string]any{
				"type":        "integer",
				"description": "每任务超时时间（秒，默认 300）",
			},
		},
		"required": []string{"tasks"},
	})

	host.RegisterTool(
		mcphost.ToolDefinition{
			Name:        "parallel_dispatch",
			Description: "并行派发多个子任务到 SubAgent 执行。仅 Master Agent 可以调用此工具。",
			InputSchema: schema,
		},
		func(ctx context.Context, input json.RawMessage) (*mcphost.ToolResult, error) {
			// 检查调用者权限：允许 Master 和固定 Agent
			toolCtx := GetToolContext(ctx)
			if toolCtx.CallerType != CallerMaster && toolCtx.CallerType != CallerFixedAgent {
				logger.Warn("parallel_dispatch 工具调用被拒绝：非授权调用者",
					zap.String("caller_type", string(toolCtx.CallerType)),
					zap.String("caller_name", toolCtx.CallerName),
				)
				return &mcphost.ToolResult{
					Content: jsonText("错误：parallel_dispatch 工具仅允许 Master Agent 和固定 Agent 调用"),
					IsError: true,
				}, nil
			}

			if gate != nil {
				if err := gate.CheckNestedToolAllowed(ctx, "parallel_dispatch"); err != nil {
					logger.Warn("parallel_dispatch 工具调用被 plan mode gate 拒绝",
						zap.String("caller_type", string(toolCtx.CallerType)),
						zap.String("caller_name", toolCtx.CallerName),
						zap.Error(err),
					)
					return &mcphost.ToolResult{
						Content: jsonText(err.Error()),
						IsError: true,
					}, nil
				}
			}

			// 检查调用深度，防止递归
			if toolCtx.Depth >= maxDepth {
				logger.Warn("parallel_dispatch 工具调用被拒绝：超过最大深度",
					zap.Int("depth", toolCtx.Depth),
					zap.Int("max_depth", maxDepth),
				)
				return &mcphost.ToolResult{
					Content: jsonText(fmt.Sprintf("错误：parallel_dispatch 调用深度超过最大限制 (%d)", maxDepth)),
					IsError: true,
				}, nil
			}

			// 解析输入参数
			var params parallelDispatchInput
			if err := json.Unmarshal(input, &params); err != nil {
				return errorResult("输入无效: " + err.Error()), nil
			}

			// 验证任务列表
			if len(params.Tasks) == 0 {
				return errorResult("tasks 不能为空"), nil
			}
			if len(params.Tasks) > maxParallelTasks {
				return errorResult(fmt.Sprintf("任务数超过最大限制 (%d)", maxParallelTasks)), nil
			}

			// 设置并发数
			maxConc := defaultMaxConcurrency
			if params.MaxConcurrency > 0 && params.MaxConcurrency < maxConc {
				maxConc = params.MaxConcurrency
			}

			// 设置超时（上限 30 分钟）
			timeout := defaultTaskTimeout
			if params.TimeoutSeconds > 0 {
				timeout = time.Duration(params.TimeoutSeconds) * time.Second
				if timeout > maxTaskTimeout {
					timeout = maxTaskTimeout
				}
			}

			// 生成 GroupID（使用随机字节避免高并发冲突）
			randBytes := make([]byte, 8)
			_, _ = rand.Read(randBytes)
			groupID := fmt.Sprintf("taskgroup-%s", hex.EncodeToString(randBytes))

			// 补全任务 ID，验证 agent_id
			for i := range params.Tasks {
				if params.Tasks[i].ID == "" {
					params.Tasks[i].ID = fmt.Sprintf("task-%d", i+1)
				}
				if params.Tasks[i].AgentID == "" {
					return errorResult(fmt.Sprintf("tasks[%d].agent_id 不能为空，请指定目标 Agent（如 explore）或使用 spawn_agent 创建临时 Agent", i)), nil
				}
				if systemAgentDenyList[params.Tasks[i].AgentID] {
					return errorResult(fmt.Sprintf("tasks[%d].agent_id %q 是系统服务 Agent，不接受用户任务委派。请使用 explore 或 spawn_agent 创建临时 Agent", i, params.Tasks[i].AgentID)), nil
				}
			}

			logger.Info("开始并行任务组",
				zap.String("group_id", groupID),
				zap.Int("task_count", len(params.Tasks)),
				zap.Int("max_concurrency", maxConc),
			)

			// 广播 TaskGroupEvent{Status: "started"}
			if broadcaster != nil {
				briefs := make([]interface{}, len(params.Tasks))
				for i, t := range params.Tasks {
					briefs[i] = map[string]string{
						"id":          t.ID,
						"agent_id":    t.AgentID,
						"instruction": t.Instruction,
						"status":      "pending",
					}
				}
				broadcaster.BroadcastTaskGroup(groupID, "started", len(params.Tasks), briefs, nil)
			}

			// 并发执行任务
			results := make([]parallelTaskResult, len(params.Tasks))
			sem := make(chan struct{}, maxConc) // 信号量控制并发
			var wg sync.WaitGroup

			for i, task := range params.Tasks {
				wg.Add(1)
				go func(idx int, t parallelTaskItem) {
					defer wg.Done()

					// 获取信号量
					sem <- struct{}{}
					defer func() { <-sem }()

					// 广播任务开始
					if broadcaster != nil {
						broadcaster.BroadcastTaskProgress(groupID, t.ID, "running", "")
					}

					// 带超时执行
					taskCtx, cancel := context.WithTimeout(ctx, timeout)
					defer cancel()

					result, err := executor.ExecuteTask(taskCtx, t.AgentID, t.Instruction, t.Context)

					if err != nil {
						logger.Warn("并行任务失败",
							zap.String("group_id", groupID),
							zap.String("task_id", t.ID),
							zap.String("agent_id", t.AgentID),
							zap.Error(err),
						)
						if observer != nil {
							observer.RecordDelegation(ctx, DelegationEvent{
								ParentTraceID: toolCtx.TraceID,
								ChildTraceID:  DeriveChildTraceID(toolCtx.TraceID, t.AgentID),
								AgentID:       t.AgentID,
								AgentType:     "subagent",
								GroupID:       groupID,
								SpawnDepth:    toolCtx.Depth + 1,
								Status:        "failed",
								FailureType:   "runtime",
								Error:         err.Error(),
							})
						}
						results[idx] = parallelTaskResult{
							ID:      t.ID,
							AgentID: t.AgentID,
							Status:  "failed",
							Error:   err.Error(),
						}
						// 广播任务失败
						if broadcaster != nil {
							broadcaster.BroadcastTaskProgress(groupID, t.ID, "failed", err.Error())
						}
						return
					}

					results[idx] = parallelTaskResult{
						ID:      t.ID,
						AgentID: t.AgentID,
						Status:  "completed",
						Result:  result,
					}
					if observer != nil {
						observer.RecordDelegation(ctx, DelegationEvent{
							ParentTraceID: toolCtx.TraceID,
							ChildTraceID:  DeriveChildTraceID(toolCtx.TraceID, t.AgentID),
							AgentID:       t.AgentID,
							AgentType:     "subagent",
							GroupID:       groupID,
							SpawnDepth:    toolCtx.Depth + 1,
							Status:        "completed",
						})
					}
					// 广播任务完成
					if broadcaster != nil {
						broadcaster.BroadcastTaskProgress(groupID, t.ID, "completed", "")
					}

					logger.Info("并行任务完成",
						zap.String("group_id", groupID),
						zap.String("task_id", t.ID),
						zap.String("agent_id", t.AgentID),
					)
				}(i, task)
			}

			wg.Wait()

			// 广播 TaskGroupEvent{Status: "completed"}
			if broadcaster != nil {
				broadcaster.BroadcastTaskGroup(groupID, "completed", len(params.Tasks), nil, results)
			}

			// 统计结果
			completedCount := 0
			failedCount := 0
			for _, r := range results {
				if r.Status == "completed" {
					completedCount++
				} else {
					failedCount++
				}
			}

			logger.Info("并行任务组完成",
				zap.String("group_id", groupID),
				zap.Int("completed", completedCount),
				zap.Int("failed", failedCount),
			)
			if observer != nil {
				status := "completed"
				failureType := ""
				if failedCount > 0 {
					status = "failed"
					failureType = "runtime"
				}
				observer.RecordDelegation(ctx, DelegationEvent{
					ParentTraceID: toolCtx.TraceID,
					ChildTraceID:  DeriveChildTraceID(toolCtx.TraceID, groupID),
					AgentType:     "subagent_group",
					GroupID:       groupID,
					SpawnDepth:    toolCtx.Depth + 1,
					Status:        status,
					FailureType:   failureType,
					StopReason:    fmt.Sprintf("completed=%d failed=%d", completedCount, failedCount),
				})
			}

			// 返回聚合结果 JSON
			resultJSON, _ := json.Marshal(map[string]interface{}{
				"group_id":  groupID,
				"total":     len(params.Tasks),
				"completed": completedCount,
				"failed":    failedCount,
				"results":   results,
			})

			return textResult(string(resultJSON)), nil
		},
	)
}
