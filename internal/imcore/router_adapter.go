package imcore

import (
	"context"
	"fmt"
	"strings"

	"github.com/chef-guo/agents-hive/internal/imctx"
)

type MessageRouter interface {
	SendMessage(ctx context.Context, req imctx.SendRequest) error
}

type SendOnlyAdapter struct {
	platform Platform
	router   MessageRouter
}

func NewSendOnlyAdapter(platform Platform, router MessageRouter) Adapter {
	return &SendOnlyAdapter{platform: platform, router: router}
}

func (a *SendOnlyAdapter) Platform() Platform {
	return a.platform
}

func (a *SendOnlyAdapter) Capabilities() []Capability {
	return []Capability{CapabilitySendText}
}

func (a *SendOnlyAdapter) SearchRecipients(context.Context, CallerScope, string, int) ([]Recipient, error) {
	return nil, fmt.Errorf("im platform %s does not support recipient search", a.platform)
}

func (a *SendOnlyAdapter) ListRecentConversations(context.Context, CallerScope, int) ([]Recipient, error) {
	return nil, fmt.Errorf("im platform %s does not support recent conversation listing", a.platform)
}

func (a *SendOnlyAdapter) ResolveRecipient(_ context.Context, _ CallerScope, input RecipientLookup) (Recipient, error) {
	id, kind, idType := firstTargetID(input.RecipientID, input.ConversationID)
	if id == "" {
		return Recipient{}, fmt.Errorf("recipient_id or conversation_id is required")
	}
	return Recipient{
		Platform:       a.platform,
		ID:             id,
		DisplayName:    id,
		Kind:           kind,
		ExternalIDType: idType,
		CanSend:        true,
		SendState:      "ready",
	}, nil
}

func (a *SendOnlyAdapter) SendMessage(ctx context.Context, _ CallerScope, target SendTarget) (SendResult, error) {
	id, kind, _ := firstTargetID(target.RecipientID, target.ConversationID)
	if id == "" {
		return SendResult{}, fmt.Errorf("recipient_id or conversation_id is required")
	}
	if target.DryRun {
		return SendResult{Platform: a.platform, TargetID: id, TargetKind: kind, Delivered: false, DryRun: true}, nil
	}
	if a.router == nil {
		return SendResult{}, fmt.Errorf("im router not configured for %s", a.platform)
	}
	if err := a.router.SendMessage(ctx, imctx.SendRequest{
		Platform: imctx.Platform(a.platform),
		ChatID:   id,
		Content:  target.Content,
	}); err != nil {
		return SendResult{}, err
	}
	return SendResult{Platform: a.platform, TargetID: id, TargetKind: kind, Delivered: true}, nil
}

func firstTargetID(recipientID, conversationID string) (id, kind, idType string) {
	if v := strings.TrimSpace(conversationID); v != "" {
		return v, "conversation", "conversation_id"
	}
	if v := strings.TrimSpace(recipientID); v != "" {
		return v, "user", "recipient_id"
	}
	return "", "", ""
}
