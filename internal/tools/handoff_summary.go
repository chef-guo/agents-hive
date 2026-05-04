package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"go.uber.org/zap"

	"github.com/chef-guo/agents-hive/internal/mcphost"
	"github.com/chef-guo/agents-hive/internal/toolctx"
)

const handoffSummaryToolName = "create_handoff_summary"

type handoffSummaryInput struct {
	Goal      string   `json:"goal,omitempty"`
	Decisions []string `json:"decisions,omitempty"`
	Risks     []string `json:"risks,omitempty"`
}

func registerHandoffSummary(host *mcphost.Host, logger *zap.Logger, store SessionTodoStore) {
	schema, _ := json.Marshal(map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"goal": map[string]any{
				"type":        "string",
				"description": "本次交接的目标或上下文标题。",
			},
			"decisions": map[string]any{
				"type":        "array",
				"description": "已经确定、下游必须继承的关键决策。",
				"items":       map[string]any{"type": "string"},
			},
			"risks": map[string]any{
				"type":        "array",
				"description": "剩余风险、外部依赖或需要人工关注的事项。",
				"items":       map[string]any{"type": "string"},
			},
		},
	})

	host.RegisterTool(
		mcphost.ToolDefinition{
			Name:              handoffSummaryToolName,
			Description:       "基于当前 session todo snapshot 生成交接摘要。仅 Master Agent 可调用；session_id 由运行时上下文提供。",
			InputSchema:       schema,
			Core:              true,
			IsConcurrencySafe: true,
		},
		func(ctx context.Context, input json.RawMessage) (*mcphost.ToolResult, error) {
			if err := requireMasterCaller(ctx, handoffSummaryToolName, logger); err != nil {
				return errorResult(err.Error()), nil
			}
			if store == nil {
				return errorResult("create_handoff_summary 未启用：session todo store 未配置"), nil
			}
			sessionID := toolctx.GetSessionID(ctx)
			if sessionID == "" {
				return errorResult("create_handoff_summary 缺少 sessionID：必须由运行时上下文提供"), nil
			}

			var params handoffSummaryInput
			if err := json.Unmarshal(input, &params); err != nil {
				return errorResult("输入无效: " + err.Error()), nil
			}
			snapshot, err := store.Snapshot(ctx, sessionID)
			if err != nil {
				return errorResult("create_handoff_summary 读取 snapshot 失败: " + err.Error()), nil
			}
			return textResult(renderHandoffSummary(snapshot, params)), nil
		},
	)
}

func renderHandoffSummary(snapshot SessionTodoSnapshot, params handoffSummaryInput) string {
	var b strings.Builder
	b.WriteString("# Handoff Summary\n\n")
	fmt.Fprintf(&b, "Session: %s\n", snapshot.SessionID)
	fmt.Fprintf(&b, "Plan status: %s\n", snapshot.PlanStatus)
	fmt.Fprintf(&b, "Plan version: %d\n", snapshot.PlanVersion)
	if goal := strings.TrimSpace(params.Goal); goal != "" {
		fmt.Fprintf(&b, "Goal: %s\n", goal)
	}

	b.WriteString("\n## Todos\n")
	if len(snapshot.Todos) == 0 {
		b.WriteString("- No session todos.\n")
	} else {
		for _, todo := range snapshot.Todos {
			fmt.Fprintf(&b, "- [%s] %s: %s\n", handoffTodoMarker(todo.Status), todo.ID, todo.Content)
		}
	}

	writeHandoffList(&b, "Decisions", params.Decisions)
	writeHandoffList(&b, "Risks", params.Risks)
	return strings.TrimRight(b.String(), "\n")
}

func handoffTodoMarker(status TodoStatus) string {
	switch status {
	case TodoStatusCompleted:
		return "x"
	case TodoStatusInProgress:
		return "~"
	case TodoStatusCancelled:
		return "-"
	default:
		return " "
	}
}

func writeHandoffList(b *strings.Builder, title string, items []string) {
	filtered := make([]string, 0, len(items))
	for _, item := range items {
		if s := strings.TrimSpace(item); s != "" {
			filtered = append(filtered, s)
		}
	}
	if len(filtered) == 0 {
		return
	}
	fmt.Fprintf(b, "\n## %s\n", title)
	for _, item := range filtered {
		fmt.Fprintf(b, "- %s\n", item)
	}
}
