package imcore

import (
	"context"
	"fmt"
	"strings"

	"github.com/chef-guo/agents-hive/internal/imctx"
	"github.com/chef-guo/agents-hive/internal/store"
)

type WechatConversationStore interface {
	GetWechatConversationByOwnerPeer(ctx context.Context, ownerUserID, peerWxid string) (*store.WechatConversationRecord, error)
	ListWechatConversationsByOwner(ctx context.Context, ownerUserID string) ([]*store.WechatConversationRecord, error)
}

type WechatBotAdapter struct {
	store  WechatConversationStore
	router MessageRouter
}

func NewWechatBotAdapter(store WechatConversationStore, router MessageRouter) Adapter {
	return &WechatBotAdapter{store: store, router: router}
}

func (a *WechatBotAdapter) Platform() Platform {
	return PlatformWeChatBot
}

func (a *WechatBotAdapter) Capabilities() []Capability {
	return []Capability{
		CapabilityListRecentConversations,
		CapabilitySendText,
		CapabilityRequiresOwnerContext,
		CapabilityRequiresConversationContext,
	}
}

func (a *WechatBotAdapter) SearchRecipients(context.Context, CallerScope, string, int) ([]Recipient, error) {
	return nil, fmt.Errorf("wechatbot does not support global recipient search; use list_recent_conversations")
}

func (a *WechatBotAdapter) ListRecentConversations(ctx context.Context, scope CallerScope, limit int) ([]Recipient, error) {
	if err := requireWechatOwner(scope); err != nil {
		return nil, err
	}
	if a.store == nil {
		return nil, fmt.Errorf("wechatbot conversation store not configured")
	}
	records, err := a.store.ListWechatConversationsByOwner(ctx, scope.OwnerUserID)
	if err != nil {
		return nil, err
	}
	if limit <= 0 || limit > len(records) {
		limit = len(records)
	}
	out := make([]Recipient, 0, limit)
	for _, rec := range records {
		if rec == nil {
			continue
		}
		out = append(out, wechatRecipientFromRecord(rec))
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

func (a *WechatBotAdapter) ResolveRecipient(ctx context.Context, scope CallerScope, input RecipientLookup) (Recipient, error) {
	if err := requireWechatOwner(scope); err != nil {
		return Recipient{}, err
	}
	peer := strings.TrimSpace(input.ConversationID)
	if peer == "" {
		peer = strings.TrimSpace(input.RecipientID)
	}
	if peer == "" {
		return Recipient{}, fmt.Errorf("wechatbot conversation_id or recipient_id is required")
	}
	rec, err := a.lookupConversation(ctx, scope.OwnerUserID, peer)
	if err != nil {
		return Recipient{}, err
	}
	return wechatRecipientFromRecord(rec), nil
}

func (a *WechatBotAdapter) SendMessage(ctx context.Context, scope CallerScope, target SendTarget) (SendResult, error) {
	if err := requireWechatOwner(scope); err != nil {
		return SendResult{}, err
	}
	peer := strings.TrimSpace(target.ConversationID)
	if peer == "" {
		peer = strings.TrimSpace(target.RecipientID)
	}
	if peer == "" {
		return SendResult{}, fmt.Errorf("wechatbot conversation_id or recipient_id is required")
	}
	rec, err := a.lookupConversation(ctx, scope.OwnerUserID, peer)
	if err != nil {
		return SendResult{}, err
	}
	if !rec.CanSend {
		return SendResult{}, fmt.Errorf("wechatbot conversation is not sendable; ask the contact to send a message first")
	}
	if target.DryRun {
		return SendResult{Platform: PlatformWeChatBot, TargetID: rec.PeerWxid, TargetKind: "conversation", Delivered: false, DryRun: true}, nil
	}
	if a.router == nil {
		return SendResult{}, fmt.Errorf("im router not configured for wechatbot")
	}
	if err := a.router.SendMessage(ctx, imctx.SendRequest{
		Platform:    imctx.PlatformWeChatBot,
		TenantKey:   scope.OwnerUserID,
		OwnerUserID: scope.OwnerUserID,
		ChatID:      rec.PeerWxid,
		Content:     target.Content,
	}); err != nil {
		return SendResult{}, err
	}
	return SendResult{Platform: PlatformWeChatBot, TargetID: rec.PeerWxid, TargetKind: "conversation", Delivered: true}, nil
}

func (a *WechatBotAdapter) lookupConversation(ctx context.Context, ownerUserID, peerWxid string) (*store.WechatConversationRecord, error) {
	if a.store == nil {
		return nil, fmt.Errorf("wechatbot conversation store not configured")
	}
	rec, err := a.store.GetWechatConversationByOwnerPeer(ctx, ownerUserID, peerWxid)
	if err != nil || rec == nil {
		return nil, fmt.Errorf("wechatbot conversation not found or not owned by current user")
	}
	return rec, nil
}

func requireWechatOwner(scope CallerScope) error {
	if strings.TrimSpace(scope.OwnerUserID) == "" {
		return fmt.Errorf("wechatbot requires authenticated owner")
	}
	return nil
}

func wechatRecipientFromRecord(rec *store.WechatConversationRecord) Recipient {
	name := strings.TrimSpace(rec.PeerNickname)
	if name == "" {
		name = "微信会话"
	}
	sendState := strings.TrimSpace(rec.SendState)
	if sendState == "" {
		sendState = "unknown"
	}
	kind := "conversation"
	if strings.EqualFold(rec.ChatType, "group") {
		kind = "group"
	}
	return Recipient{
		Platform:       PlatformWeChatBot,
		ID:             rec.PeerWxid,
		DisplayName:    name,
		Kind:           kind,
		ExternalIDType: "conversation_id",
		CanSend:        rec.CanSend,
		SendState:      sendState,
	}
}
