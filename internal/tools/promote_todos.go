package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"go.uber.org/zap"

	"github.com/chef-guo/agents-hive/internal/mcphost"
	"github.com/chef-guo/agents-hive/internal/taskboard"
	"github.com/chef-guo/agents-hive/internal/toolctx"
)

const promoteTodosToolName = "promote_todos_to_taskboard"

type promoteTodosInput struct {
	TodoIDs  []string `json:"todo_ids"`
	Priority string   `json:"priority,omitempty"`
	Tags     []string `json:"tags,omitempty"`
}

type promoteTodosResult struct {
	PromotedCount int      `json:"promoted_count"`
	TaskIDs       []string `json:"task_ids"`
	PlanVersion   int64    `json:"plan_version"`
}

func registerPromoteTodosToTaskboard(host *mcphost.Host, logger *zap.Logger, store SessionTodoStore, broadcaster TodoSnapshotBroadcaster, board taskboard.TaskBoard) {
	schema, _ := json.Marshal(map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"todo_ids": map[string]any{
				"type":        "array",
				"description": "要晋升为持久 taskboard 工作项的 session todo ID 列表。",
				"minItems":    1,
				"items":       map[string]any{"type": "string"},
			},
			"priority": map[string]any{
				"type":        "string",
				"enum":        []string{"low", "medium", "high"},
				"description": "创建 taskboard 任务的优先级，默认 medium。",
			},
			"tags": map[string]any{
				"type":        "array",
				"description": "追加到 taskboard 任务上的标签；系统会自动添加 sessiontodo。",
				"items":       map[string]any{"type": "string"},
			},
		},
		"required": []string{"todo_ids"},
	})

	host.RegisterTool(
		mcphost.ToolDefinition{
			Name:              promoteTodosToolName,
			Description:       "把当前 session 中指定 todo 晋升为持久 taskboard 工作项，并将原 todo 标记为 cancelled。仅 Master Agent 可调用。",
			InputSchema:       schema,
			Core:              true,
			IsConcurrencySafe: false,
		},
		func(ctx context.Context, input json.RawMessage) (*mcphost.ToolResult, error) {
			if err := requireMasterCaller(ctx, promoteTodosToolName, logger); err != nil {
				return errorResult(err.Error()), nil
			}
			if store == nil {
				return errorResult("promote_todos_to_taskboard 未启用：session todo store 未配置"), nil
			}
			if board == nil {
				return errorResult("promote_todos_to_taskboard 未启用：taskboard 未配置"), nil
			}
			sessionID := toolctx.GetSessionID(ctx)
			if sessionID == "" {
				return errorResult("promote_todos_to_taskboard 缺少 sessionID：必须由运行时上下文提供"), nil
			}

			var params promoteTodosInput
			if err := json.Unmarshal(input, &params); err != nil {
				return errorResult("输入无效: " + err.Error()), nil
			}
			todoIDs, err := normalizePromoteTodoIDs(params.TodoIDs)
			if err != nil {
				return errorResult(err.Error()), nil
			}
			priority, err := normalizePromotePriority(params.Priority)
			if err != nil {
				return errorResult(err.Error()), nil
			}
			tags := normalizePromoteTags(params.Tags)

			snapshot, err := store.Snapshot(ctx, sessionID)
			if err != nil {
				return errorResult("promote_todos_to_taskboard 读取 snapshot 失败: " + err.Error()), nil
			}
			promoteSet := make(map[string]struct{}, len(todoIDs))
			for _, id := range todoIDs {
				promoteSet[id] = struct{}{}
			}
			todosByID := make(map[string]SessionTodo, len(snapshot.Todos))
			for _, todo := range snapshot.Todos {
				todosByID[todo.ID] = todo
			}
			for _, id := range todoIDs {
				if _, ok := todosByID[id]; !ok {
					return errorResult(fmt.Sprintf("unknown todo id: %s", id)), nil
				}
			}

			taskIDs := make([]string, 0, len(todoIDs))
			for _, id := range todoIDs {
				todo := todosByID[id]
				taskID, err := board.Create(ctx, &taskboard.Task{
					SessionID:   sessionID,
					Title:       todo.Content,
					Description: fmt.Sprintf("Promoted from session todo %s", todo.ID),
					Priority:    priority,
					Tags:        tags,
				})
				if err != nil {
					return errorResult("创建 taskboard 任务失败: " + err.Error()), nil
				}
				taskIDs = append(taskIDs, taskID)
			}

			nextTodos := make([]SessionTodoInput, 0, len(snapshot.Todos))
			for _, todo := range snapshot.Todos {
				status := todo.Status
				if _, ok := promoteSet[todo.ID]; ok {
					status = TodoStatusCancelled
				}
				nextTodos = append(nextTodos, SessionTodoInput{
					ID:               todo.ID,
					Content:          todo.Content,
					Status:           status,
					Order:            todo.Order,
					Source:           todo.Source,
					TraceID:          todo.TraceID,
					SpanID:           todo.SpanID,
					TurnID:           todo.TurnID,
					RuntimeEpoch:     todo.RuntimeEpoch,
					SourceChangeID:   todo.SourceChangeID,
					SourceRevision:   todo.SourceRevision,
					SourceToolCallID: todo.SourceToolCallID,
				})
			}
			updated, err := store.Replace(ctx, sessionID, snapshot.PlanVersion, nextTodos)
			if err != nil {
				rollbackPromotedTasks(ctx, board, taskIDs, logger)
				logger.Error("promote_todos_to_taskboard 写入 session todo snapshot 失败",
					zap.String("session_id", sessionID),
					zap.Int64("expected_plan_version", snapshot.PlanVersion),
					zap.Error(err),
				)
				return errorResult("promote_todos_to_taskboard 写入 snapshot 失败: " + err.Error()), nil
			}
			if err := broadcastTodoSnapshot(ctx, broadcaster, updated, logger); err != nil {
				rollbackPromotedTasks(ctx, board, taskIDs, logger)
				return errorResult("promote_todos_to_taskboard 广播失败: " + err.Error()), nil
			}

			data, err := json.Marshal(promoteTodosResult{
				PromotedCount: len(taskIDs),
				TaskIDs:       taskIDs,
				PlanVersion:   updated.PlanVersion,
			})
			if err != nil {
				return errorResult("序列化 promote 结果失败: " + err.Error()), nil
			}
			return textResult(string(data)), nil
		},
	)
}

func normalizePromoteTodoIDs(ids []string) ([]string, error) {
	if len(ids) == 0 {
		return nil, fmt.Errorf("todo_ids 至少需要一个 todo id")
	}
	seen := make(map[string]struct{}, len(ids))
	out := make([]string, 0, len(ids))
	for i, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			return nil, fmt.Errorf("todo_ids[%d] 不能为空", i)
		}
		if _, ok := seen[id]; ok {
			return nil, fmt.Errorf("duplicate todo id: %s", id)
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out, nil
}

func normalizePromotePriority(priority string) (taskboard.Priority, error) {
	switch taskboard.Priority(strings.TrimSpace(priority)) {
	case "":
		return taskboard.PriorityMedium, nil
	case taskboard.PriorityLow:
		return taskboard.PriorityLow, nil
	case taskboard.PriorityMedium:
		return taskboard.PriorityMedium, nil
	case taskboard.PriorityHigh:
		return taskboard.PriorityHigh, nil
	default:
		return "", fmt.Errorf("priority 非法：仅允许 low/medium/high")
	}
}

func normalizePromoteTags(tags []string) []string {
	out := []string{"sessiontodo"}
	seen := map[string]struct{}{"sessiontodo": {}}
	for _, tag := range tags {
		tag = strings.TrimSpace(tag)
		if tag == "" {
			continue
		}
		if _, ok := seen[tag]; ok {
			continue
		}
		seen[tag] = struct{}{}
		out = append(out, tag)
	}
	return out
}

func rollbackPromotedTasks(ctx context.Context, board taskboard.TaskBoard, taskIDs []string, logger *zap.Logger) {
	for _, taskID := range taskIDs {
		if taskID == "" {
			continue
		}
		if err := board.Delete(ctx, taskID); err != nil && logger != nil {
			logger.Warn("promote_todos_to_taskboard 回滚 taskboard 任务失败",
				zap.String("task_id", taskID),
				zap.Error(err),
			)
		}
	}
}
