package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"go.uber.org/zap"

	"github.com/chef-guo/agents-hive/internal/auth"
	"github.com/chef-guo/agents-hive/internal/mcphost"
	"github.com/chef-guo/agents-hive/internal/memory"
)

// memoryInput 是 memory 工具的输入参数
type memoryInput struct {
	Operation string   `json:"operation"`            // 操作类型: save, search, update, delete, list
	Type      string   `json:"type,omitempty"`       // 记忆类型: preference, project, feedback, reference
	Content   string   `json:"content,omitempty"`    // 记忆内容
	Tags      []string `json:"tags,omitempty"`       // 标签列表
	Query     string   `json:"query,omitempty"`      // 搜索查询
	ID        int64    `json:"id,omitempty"`         // 记忆 ID（用于 update/delete）
	Limit     int      `json:"limit,omitempty"`      // 返回结果数量限制
	SessionID string   `json:"session_id,omitempty"` // 关联的会话 ID（可选）
}

// registerMemory 注册 memory 工具到 MCP host
func registerMemory(host *mcphost.Host, logger *zap.Logger, store memory.MemoryStore) {
	schema, _ := json.Marshal(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"operation": map[string]any{
				"type":        "string",
				"enum":        []string{"save", "search", "update", "delete", "list"},
				"description": "操作类型: save=保存记忆, search=搜索记忆, update=更新记忆, delete=删除记忆, list=列出记忆",
			},
			"type": map[string]any{
				"type":        "string",
				"description": "记忆类型: preference(用户偏好), project(项目信息), feedback(反馈), reference(参考资料)",
			},
			"content": map[string]any{
				"type":        "string",
				"description": "记忆内容（save/update 操作需要）",
			},
			"tags": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "标签列表，用于分类和过滤",
			},
			"query": map[string]any{
				"type":        "string",
				"description": "搜索查询文本（search 操作需要）",
			},
			"id": map[string]any{
				"type":        "integer",
				"description": "记忆 ID（update/delete 操作需要）",
			},
			"limit": map[string]any{
				"type":        "integer",
				"description": "返回结果数量限制（默认 10）",
			},
			"session_id": map[string]any{
				"type":        "string",
				"description": "关联的会话 ID（可选，save 操作时使用）",
			},
		},
		"required": []string{"operation"},
	})

	host.RegisterTool(
		mcphost.ToolDefinition{
			Name:        "memory",
			Description: "管理用户长期记忆：保存用户偏好、工作方式反馈、项目信息片段和参考备注。memory 不是项目知识库/KB 文档库；用户询问知识库、KB、namespace、上传文档或文档目录时，应使用 kb.doc.meta / kb.doc.structure / kb.section.text。",
			InputSchema: schema,
		},
		func(ctx context.Context, input json.RawMessage) (*mcphost.ToolResult, error) {
			var params memoryInput
			if err := json.Unmarshal(input, &params); err != nil {
				return errorResult("输入无效: " + err.Error()), nil
			}

			switch params.Operation {
			case "save":
				return memorySave(ctx, store, params)
			case "search":
				return memorySearch(ctx, store, params)
			case "update":
				return memoryUpdate(ctx, store, params)
			case "delete":
				return memoryDelete(ctx, store, params)
			case "list":
				return memoryList(ctx, store, params)
			default:
				return errorResult(fmt.Sprintf("无效的操作: %s，支持: save, search, update, delete, list", params.Operation)), nil
			}
		},
	)

	logger.Info("已注册 memory 工具")
}

// memorySave 保存新记忆
func memorySave(ctx context.Context, store memory.MemoryStore, params memoryInput) (*mcphost.ToolResult, error) {
	if params.Content == "" {
		return errorResult("save 操作需要 content 参数"), nil
	}
	if params.Type == "" {
		params.Type = "reference" // 默认类型
	}

	record := &memory.MemoryRecord{
		Type:      memory.MemoryType(params.Type),
		Content:   params.Content,
		Tags:      params.Tags,
		SessionID: params.SessionID,
		UserID:    auth.UserIDFrom(ctx),
	}
	record.Metadata = memory.EncodeGovernance(nil, memory.Governance{
		Source:        "memory_tool",
		Evidence:      params.Content,
		Confidence:    0.9,
		ExtractedBy:   "memory_tool",
		SourceUserID:  record.UserID,
		SourceMessage: params.SessionID,
		RunID:         params.SessionID,
	})
	if record.Type == memory.MemoryTypeFeedback {
		record.Metadata = memory.EncodeMemoryKind(record.Metadata, memory.MemoryKind("feedback"))
	}
	id, err := store.Save(ctx, record)
	if err != nil {
		return errorResult(fmt.Sprintf("保存记忆失败: %v", err)), nil
	}

	result := map[string]any{
		"status": "saved",
		"id":     id,
		"type":   params.Type,
	}
	if len(params.Tags) > 0 {
		result["tags"] = params.Tags
	}
	result["governance"] = map[string]any{
		"source":     "memory_tool",
		"confidence": 0.9,
	}
	data, _ := json.Marshal(result)
	return textResult(string(data)), nil
}

// memorySearch 搜索记忆
func memorySearch(ctx context.Context, store memory.MemoryStore, params memoryInput) (*mcphost.ToolResult, error) {
	if params.Query == "" {
		return errorResult("search 操作需要 query 参数"), nil
	}

	limit := params.Limit
	if limit <= 0 {
		limit = 10
	}

	opts := memory.SearchOptions{
		Query:  params.Query,
		Limit:  limit,
		UserID: auth.UserIDFrom(ctx),
	}
	if params.Type != "" {
		opts.Type = memory.MemoryType(params.Type)
	}
	if len(params.Tags) > 0 {
		opts.Tags = params.Tags
	}

	searchResult, err := store.Search(ctx, opts)
	if err != nil {
		return errorResult(fmt.Sprintf("搜索记忆失败: %v", err)), nil
	}

	if len(searchResult.Memories) == 0 {
		return textResult("未找到匹配的记忆"), nil
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("找到 %d 条记忆:\n\n", len(searchResult.Memories)))
	for i, m := range searchResult.Memories {
		sb.WriteString(fmt.Sprintf("[%d] ID=%d 类型=%s", i+1, m.ID, string(m.Type)))
		if m.Score > 0 {
			sb.WriteString(fmt.Sprintf(" 相关度=%.2f", m.Score))
		}
		if len(m.Tags) > 0 {
			sb.WriteString(fmt.Sprintf(" 标签=[%s]", strings.Join(m.Tags, ", ")))
		}
		sb.WriteString("\n")
		sb.WriteString(m.Content)
		sb.WriteString("\n\n")
	}

	return textResult(sb.String()), nil
}

// memoryUpdate 更新已有记忆
func memoryUpdate(ctx context.Context, store memory.MemoryStore, params memoryInput) (*mcphost.ToolResult, error) {
	if params.ID == 0 {
		return errorResult("update 操作需要 id 参数"), nil
	}
	if params.Content == "" && len(params.Tags) == 0 {
		return errorResult("update 操作需要 content 或 tags 参数"), nil
	}

	record := &memory.MemoryRecord{
		ID:      params.ID,
		Content: params.Content,
		Tags:    params.Tags,
		UserID:  auth.UserIDFrom(ctx),
	}
	if err := store.Update(ctx, record); err != nil {
		return errorResult(fmt.Sprintf("更新记忆失败: %v", err)), nil
	}

	return textResult(fmt.Sprintf("已更新记忆 ID=%d", params.ID)), nil
}

// memoryDelete 删除记忆
func memoryDelete(ctx context.Context, store memory.MemoryStore, params memoryInput) (*mcphost.ToolResult, error) {
	if params.ID == 0 {
		return errorResult("delete 操作需要 id 参数"), nil
	}

	// Delete SQL 已含 user_id 过滤，无权限时返回 NotFound
	_ = auth.UserIDFrom(ctx) // 确保 ctx 中的 userID 通过 pg_store.Delete 的 auth.UserIDFrom(ctx) 生效
	if err := store.Delete(ctx, params.ID); err != nil {
		return errorResult(fmt.Sprintf("删除记忆失败: %v", err)), nil
	}

	return textResult(fmt.Sprintf("已删除记忆 ID=%d", params.ID)), nil
}

// memoryList 列出记忆
func memoryList(ctx context.Context, store memory.MemoryStore, params memoryInput) (*mcphost.ToolResult, error) {
	limit := params.Limit
	if limit <= 0 {
		limit = 10
	}

	opts := memory.SearchOptions{
		Limit:  limit,
		UserID: auth.UserIDFrom(ctx),
	}
	if params.Type != "" {
		opts.Type = memory.MemoryType(params.Type)
	}
	if len(params.Tags) > 0 {
		opts.Tags = params.Tags
	}

	listResult, err := store.List(ctx, opts)
	if err != nil {
		return errorResult(fmt.Sprintf("列出记忆失败: %v", err)), nil
	}

	if len(listResult.Memories) == 0 {
		return textResult("暂无记忆"), nil
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("共 %d 条记忆:\n\n", len(listResult.Memories)))
	for i, m := range listResult.Memories {
		sb.WriteString(fmt.Sprintf("[%d] ID=%d 类型=%s", i+1, m.ID, string(m.Type)))
		if len(m.Tags) > 0 {
			sb.WriteString(fmt.Sprintf(" 标签=[%s]", strings.Join(m.Tags, ", ")))
		}
		sb.WriteString("\n")
		sb.WriteString(m.Content)
		sb.WriteString("\n\n")
	}

	return textResult(sb.String()), nil
}
