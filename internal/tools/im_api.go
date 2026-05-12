package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"go.uber.org/zap"

	"github.com/chef-guo/agents-hive/internal/imcore"
	"github.com/chef-guo/agents-hive/internal/imctx"
	"github.com/chef-guo/agents-hive/internal/mcphost"
	"github.com/chef-guo/agents-hive/internal/toolctx"
)

type imAPIInput struct {
	Action         string `json:"action"`
	Platform       string `json:"platform"`
	Query          string `json:"query,omitempty"`
	RecipientID    string `json:"recipient_id,omitempty"`
	ConversationID string `json:"conversation_id,omitempty"`
	ExternalIDType string `json:"external_id_type,omitempty"`
	Content        string `json:"content,omitempty"`
	Limit          int    `json:"limit,omitempty"`
	DryRun         bool   `json:"dry_run,omitempty"`
}

type IMAPIToolOptions struct {
	ForceDryRun bool
}

func RegisterIMAPITool(host *mcphost.Host, logger *zap.Logger, service *imcore.Service) {
	RegisterIMAPIToolWithOptions(host, logger, service, IMAPIToolOptions{})
}

func RegisterIMAPIToolWithOptions(host *mcphost.Host, logger *zap.Logger, service *imcore.Service, options IMAPIToolOptions) {
	schema, _ := json.Marshal(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"action": map[string]any{
				"type":        "string",
				"enum":        []string{"search_recipients", "list_recent_conversations", "resolve_recipient", "send_message"},
				"description": "IM 操作",
			},
			"platform": map[string]any{
				"type":        "string",
				"enum":        []string{"feishu", "wechatbot", "wecom", "dingtalk"},
				"description": "目标 IM 平台",
			},
			"query":           map[string]any{"type": "string", "description": "联系人或会话搜索关键词"},
			"recipient_id":    map[string]any{"type": "string", "description": "im_api 返回的平台中立 recipient id"},
			"conversation_id": map[string]any{"type": "string", "description": "im_api 返回的平台中立 conversation id 或平台 chat id"},
			"external_id_type": map[string]any{
				"type":        "string",
				"description": "可选的平台 ID 类型，兼容旧入口迁移；模型通常不需要填写",
			},
			"content": map[string]any{"type": "string", "description": "要发送的纯文本内容"},
			"limit":   map[string]any{"type": "integer", "description": "返回数量，默认 10"},
			"dry_run": map[string]any{"type": "boolean", "description": "为 true 时只校验目标和权限，不真实发送"},
		},
		"required": []string{"action", "platform"},
	})

	host.RegisterTool(mcphost.ToolDefinition{
		Name:        "im_api",
		Description: "统一 IM 工具。用于飞书、个人微信、企业微信、钉钉的联系人/会话发现和消息发送。按平台能力执行；不支持的能力会返回明确错误。",
		InputSchema: schema,
	}, func(ctx context.Context, input json.RawMessage) (*mcphost.ToolResult, error) {
		startedAt := time.Now()
		writeAudit := func(audit imAPIAuditFields) {
			logIMAPIAudit(ctx, logger, startedAt, audit)
		}
		var params imAPIInput
		if err := json.Unmarshal(input, &params); err != nil {
			writeAudit(imAPIAuditFields{
				Status: "error",
			})
			return errorResult("解析参数失败: " + err.Error()), nil
		}
		if service == nil {
			writeAudit(imAPIAuditFields{
				Action:     params.Action,
				Platform:   params.Platform,
				Status:     "error",
				DryRun:     params.DryRun,
				TargetKind: imAPITargetKind(params, ""),
				ContentLen: len(params.Content),
				TargetHash: imAPITargetHash(params, ""),
			})
			return errorResult("im_api 未配置 IM service"), nil
		}
		platform := imcore.Platform(params.Platform)
		switch params.Action {
		case "search_recipients":
			items, err := service.SearchRecipients(ctx, platform, params.Query, params.Limit)
			if err != nil {
				writeAudit(imAPIAuditFields{
					Action:     params.Action,
					Platform:   params.Platform,
					Status:     "error",
					DryRun:     params.DryRun,
					TargetKind: imAPITargetKind(params, ""),
					ContentLen: len(params.Content),
					TargetHash: imAPITargetHash(params, ""),
				})
				return errorResult(err.Error()), nil
			}
			writeAudit(imAPIAuditFields{
				Action:      params.Action,
				Platform:    params.Platform,
				Status:      "success",
				DryRun:      params.DryRun,
				TargetKind:  imAPITargetKind(params, ""),
				ContentLen:  len(params.Content),
				ResultCount: len(items),
				TargetHash:  imAPITargetHash(params, ""),
			})
			return jsonToolResult(items), nil
		case "list_recent_conversations":
			items, err := service.ListRecentConversations(ctx, platform, params.Limit)
			if err != nil {
				writeAudit(imAPIAuditFields{
					Action:     params.Action,
					Platform:   params.Platform,
					Status:     "error",
					DryRun:     params.DryRun,
					TargetKind: imAPITargetKind(params, ""),
					ContentLen: len(params.Content),
					TargetHash: imAPITargetHash(params, ""),
				})
				return errorResult(err.Error()), nil
			}
			writeAudit(imAPIAuditFields{
				Action:      params.Action,
				Platform:    params.Platform,
				Status:      "success",
				DryRun:      params.DryRun,
				TargetKind:  imAPITargetKind(params, ""),
				ContentLen:  len(params.Content),
				ResultCount: len(items),
				TargetHash:  imAPITargetHash(params, ""),
			})
			return jsonToolResult(items), nil
		case "resolve_recipient":
			item, err := service.ResolveRecipient(ctx, platform, imcore.RecipientLookup{
				Query:          params.Query,
				RecipientID:    params.RecipientID,
				ConversationID: params.ConversationID,
				ExternalIDType: params.ExternalIDType,
			})
			if err != nil {
				writeAudit(imAPIAuditFields{
					Action:     params.Action,
					Platform:   params.Platform,
					Status:     "error",
					DryRun:     params.DryRun,
					TargetKind: imAPITargetKind(params, ""),
					ContentLen: len(params.Content),
					TargetHash: imAPITargetHash(params, ""),
				})
				return errorResult(err.Error()), nil
			}
			writeAudit(imAPIAuditFields{
				Action:      params.Action,
				Platform:    params.Platform,
				Status:      "success",
				DryRun:      params.DryRun,
				TargetKind:  imAPITargetKind(params, item.Kind),
				ContentLen:  len(params.Content),
				ResultCount: 1,
				TargetHash:  imAPITargetHash(params, item.ID),
			})
			return jsonToolResult(item), nil
		case "send_message":
			dryRun := params.DryRun || options.ForceDryRun
			result, err := service.SendMessage(ctx, imcore.SendTarget{
				Platform:       platform,
				RecipientID:    params.RecipientID,
				ConversationID: params.ConversationID,
				ExternalIDType: params.ExternalIDType,
				Content:        params.Content,
				DryRun:         dryRun,
			})
			if err != nil {
				writeAudit(imAPIAuditFields{
					Action:     params.Action,
					Platform:   params.Platform,
					Status:     "error",
					DryRun:     dryRun,
					TargetKind: imAPITargetKind(params, ""),
					ContentLen: len(params.Content),
					TargetHash: imAPITargetHash(params, ""),
				})
				return errorResult(err.Error()), nil
			}
			writeAudit(imAPIAuditFields{
				Action:      params.Action,
				Platform:    params.Platform,
				Status:      "success",
				DryRun:      dryRun,
				TargetKind:  imAPITargetKind(params, result.TargetKind),
				ContentLen:  len(params.Content),
				ResultCount: 1,
				TargetHash:  imAPITargetHash(params, result.TargetID),
			})
			return jsonToolResult(result), nil
		default:
			writeAudit(imAPIAuditFields{
				Action:     params.Action,
				Platform:   params.Platform,
				Status:     "error",
				DryRun:     params.DryRun,
				TargetKind: imAPITargetKind(params, ""),
				ContentLen: len(params.Content),
				TargetHash: imAPITargetHash(params, ""),
			})
			return errorResult(fmt.Sprintf("不支持的 im_api action: %s", params.Action)), nil
		}
	})
}

type imAPIAuditFields struct {
	Action      string
	Platform    string
	Status      string
	DryRun      bool
	TargetKind  string
	ContentLen  int
	ResultCount int
	TargetHash  string
}

func logIMAPIAudit(ctx context.Context, logger *zap.Logger, startedAt time.Time, audit imAPIAuditFields) {
	if logger == nil {
		return
	}
	if audit.TargetKind == "" {
		audit.TargetKind = "none"
	}

	fields := []zap.Field{
		zap.String("tool", "im_api"),
		zap.String("action", audit.Action),
		zap.String("platform", audit.Platform),
		zap.String("status", audit.Status),
		zap.Bool("dry_run", audit.DryRun),
		zap.String("target_kind", audit.TargetKind),
		zap.Int("content_len", audit.ContentLen),
		zap.Int("result_count", audit.ResultCount),
		zap.Int64("duration_ms", time.Since(startedAt).Milliseconds()),
	}
	if audit.TargetHash != "" {
		fields = append(fields, zap.String("target_id_hash", audit.TargetHash))
	}
	if tc := toolctx.GetToolContext(ctx); tc != nil {
		if tc.TraceID != "" {
			fields = append(fields, zap.String("trace_id", tc.TraceID))
		}
		if tc.SpanID != "" {
			fields = append(fields, zap.String("span_id", tc.SpanID))
		}
		if tc.ParentSpanID != "" {
			fields = append(fields, zap.String("parent_span_id", tc.ParentSpanID))
		}
		if turnID := tc.TurnIDOrTraceID(); turnID != "" {
			fields = append(fields, zap.String("turn_id", turnID))
		}
		if tc.ToolCallID != "" {
			fields = append(fields, zap.String("tool_call_id", tc.ToolCallID))
		}
	}

	logger.Info("im_api 审计", fields...)
}

func imAPITargetKind(params imAPIInput, resultKind string) string {
	if resultKind != "" {
		return resultKind
	}
	if params.ConversationID != "" {
		return "conversation"
	}
	if params.RecipientID != "" {
		return "recipient"
	}
	switch params.Action {
	case "search_recipients", "resolve_recipient":
		return "recipient"
	case "list_recent_conversations":
		return "conversation"
	default:
		return "none"
	}
}

func imAPITargetHash(params imAPIInput, resultID string) string {
	if resultID != "" {
		return imctx.SafeSenderID(resultID)
	}
	if params.ConversationID != "" {
		return imctx.SafeSenderID(params.ConversationID)
	}
	if params.RecipientID != "" {
		return imctx.SafeSenderID(params.RecipientID)
	}
	return ""
}

func jsonToolResult(v any) *mcphost.ToolResult {
	raw, _ := json.Marshal(v)
	return textResult(string(raw))
}
