package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"go.uber.org/zap"

	"github.com/chef-guo/agents-hive/internal/imcore"
	"github.com/chef-guo/agents-hive/internal/imctx"
	"github.com/chef-guo/agents-hive/internal/mcphost"
	"github.com/chef-guo/agents-hive/internal/store"
)

// IMRouter IM 路由器接口（避免直接依赖 channel 包）
type IMRouter interface {
	SendMessage(ctx context.Context, req imctx.SendRequest) error
}

type wechatConversationLookup interface {
	GetWechatConversationByOwnerPeer(ctx context.Context, ownerUserID, peerWxid string) (*store.WechatConversationRecord, error)
	ListWechatConversationsByOwner(ctx context.Context, ownerUserID string) ([]*store.WechatConversationRecord, error)
}

// sendIMMessageInput send_im_message 工具的输入参数
type sendIMMessageInput struct {
	Platform string `json:"platform"`
	ChatID   string `json:"chat_id"`
	Content  string `json:"content"`
}

// RegisterSendIMMessage 注册 send_im_message 工具（导出函数，供 bootstrap 延迟调用）
// 允许 Agent 主动发送 IM 消息
func RegisterSendIMMessage(host *mcphost.Host, logger *zap.Logger, router IMRouter) {
	RegisterSendIMMessageWithStore(host, logger, router, nil)
}

func RegisterSendIMMessageWithStore(host *mcphost.Host, logger *zap.Logger, router IMRouter, convStore wechatConversationLookup) {
	service := imcore.NewService(
		imcore.NewSendOnlyAdapter(imcore.PlatformDingTalk, router),
		imcore.NewSendOnlyAdapter(imcore.PlatformFeishu, router),
		imcore.NewSendOnlyAdapter(imcore.PlatformWeCom, router),
		imcore.NewWechatBotAdapter(convStore, router),
	)

	schema, _ := json.Marshal(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"platform": map[string]any{
				"type": "string",
				"enum": []string{
					"dingtalk",
					"feishu",
					"wecom",
					"wechatbot",
				},
				"description": "IM 平台名称",
			},
			"chat_id": map[string]any{
				"type":        "string",
				"description": "聊天 ID（群 ID 或用户 ID，从 webhook 消息中获取）",
			},
			"content": map[string]any{
				"type":        "string",
				"description": "消息内容（纯文本）",
			},
		},
		"required": []string{"platform", "chat_id", "content"},
	})

	host.RegisterTool(
		mcphost.ToolDefinition{
			Name:        "send_im_message",
			Description: "发送消息到 IM 平台（钉钉/飞书/企业微信/个人微信）",
			InputSchema: schema,
		},
		func(ctx context.Context, input json.RawMessage) (*mcphost.ToolResult, error) {
			var params sendIMMessageInput
			if err := json.Unmarshal(input, &params); err != nil {
				return errorResult("解析参数失败: " + err.Error()), nil
			}

			// 验证参数
			if params.Platform == "" {
				return errorResult("platform 参数不能为空"), nil
			}
			if params.ChatID == "" {
				return errorResult("chat_id 参数不能为空"), nil
			}
			if params.Content == "" {
				return errorResult("content 参数不能为空"), nil
			}

			result, err := service.SendMessage(ctx, imcore.SendTarget{
				Platform:       imcore.Platform(params.Platform),
				ConversationID: params.ChatID,
				Content:        params.Content,
			})
			if err != nil {
				logger.Error("发送 IM 消息失败",
					zap.String("platform", params.Platform),
					zap.String("chat_id_hash", imctx.SafeSenderID(params.ChatID)),
					zap.Error(err))

				return errorResult(legacySendIMError(params.Platform, err)), nil
			}

			logger.Info("IM 消息发送成功",
				zap.String("platform", params.Platform),
				zap.String("chat_id_hash", imctx.SafeSenderID(params.ChatID)),
				zap.Int("content_len", len(params.Content)))

			targetID := result.TargetID
			if targetID == "" {
				targetID = params.ChatID
			}
			return textResult(fmt.Sprintf("✅ 消息已发送到 %s (chat: %s)", params.Platform, targetID)), nil
		},
	)
}

func legacySendIMError(platform string, err error) string {
	msg := err.Error()
	if strings.TrimSpace(platform) != "wechatbot" {
		return fmt.Sprintf("发送失败: %v", err)
	}
	switch {
	case msg == "wechatbot requires authenticated owner":
		return "wechatbot 发送需要已登录用户上下文，无法从模型输入 owner_user_id"
	case strings.Contains(msg, "not found or not owned"):
		return "无权访问此微信会话，或该联系人尚未形成可发送会话"
	case strings.Contains(msg, "not sendable"):
		return "该联系人暂无可发送上下文，请先让对方在微信中发一条消息"
	default:
		return fmt.Sprintf("发送失败: %v", err)
	}
}
