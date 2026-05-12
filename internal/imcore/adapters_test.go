package imcore

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/chef-guo/agents-hive/internal/auth"
	"github.com/chef-guo/agents-hive/internal/imctx"
	"github.com/chef-guo/agents-hive/internal/store"
	"github.com/chef-guo/agents-hive/internal/toolctx"
)

type mockMessageRouter struct {
	calls []imctx.SendRequest
}

func (r *mockMessageRouter) SendMessage(_ context.Context, req imctx.SendRequest) error {
	r.calls = append(r.calls, req)
	return nil
}

type mockFeishuProvider struct {
	searchRaw json.RawMessage
	userRaw   json.RawMessage
	sentTo    string
	sentText  string
}

func (p *mockFeishuProvider) SearchContacts(context.Context, string, int) (json.RawMessage, error) {
	return p.searchRaw, nil
}

func (p *mockFeishuProvider) GetUserInfo(context.Context, string) (json.RawMessage, error) {
	return p.userRaw, nil
}

func (p *mockFeishuProvider) SendMessage(_ context.Context, chatID, content string) error {
	p.sentTo = chatID
	p.sentText = content
	return nil
}

func TestCallerScopeFromContextDerivesOwnerTenantAndTrace(t *testing.T) {
	ctx := auth.WithUser(context.Background(), &auth.User{ID: "owner-a", Role: "user", Status: "active"})
	ctx = toolctx.WithToolContext(ctx, &toolctx.ToolContext{
		TraceID: "trace-1",
		TurnID:  "turn-1",
	})

	scope, err := CallerScopeFromContext(ctx, PlatformFeishu)
	if err != nil {
		t.Fatalf("scope: %v", err)
	}
	if scope.OwnerUserID != "owner-a" || scope.TenantKey != "owner-a" || scope.TraceID != "turn-1" {
		t.Fatalf("unexpected scope: %+v", scope)
	}
}

func TestCallerScopeFromContextWechatBotRequiresOwner(t *testing.T) {
	_, err := CallerScopeFromContext(context.Background(), PlatformWeChatBot)
	if err == nil || err.Error() != "wechatbot requires authenticated owner" {
		t.Fatalf("err = %v, want authenticated owner error", err)
	}
}

func TestWechatBotAdapterRequiresOwner(t *testing.T) {
	adapter := NewWechatBotAdapter(store.NewMemoryStore(), &mockMessageRouter{})

	_, err := adapter.ListRecentConversations(context.Background(), CallerScope{}, 10)
	if err == nil || err.Error() != "wechatbot requires authenticated owner" {
		t.Fatalf("err = %v, want authenticated owner error", err)
	}
}

func TestWechatBotAdapterCrossOwnerSendRejected(t *testing.T) {
	st := store.NewMemoryStore()
	if err := st.UpsertWechatConversation(context.Background(), &store.WechatConversationRecord{
		OwnerUserID:        "owner-a",
		OwnerAccountID:     "acct-a",
		PeerWxid:           "peer-1",
		SessionID:          "im-wechatbot-owner-a-peer-1",
		PeerNickname:       "客户A",
		ChatType:           "direct",
		LastMessagePreview: "hi",
		LastMessageAt:      ptrTime(time.Now()),
		CanSend:            true,
		SendState:          "ready",
	}); err != nil {
		t.Fatalf("upsert conversation: %v", err)
	}
	router := &mockMessageRouter{}
	adapter := NewWechatBotAdapter(st, router)

	_, err := adapter.SendMessage(context.Background(), CallerScope{OwnerUserID: "owner-b"}, SendTarget{
		Platform:       PlatformWeChatBot,
		ConversationID: "peer-1",
		Content:        "hello",
	})
	if err == nil || !strings.Contains(err.Error(), "not found or not owned") {
		t.Fatalf("err = %v, want cross owner rejection", err)
	}
	if len(router.calls) != 0 {
		t.Fatalf("router calls = %d, want 0", len(router.calls))
	}
}

func TestWechatBotAdapterDryRunDoesNotSend(t *testing.T) {
	st := store.NewMemoryStore()
	if err := st.UpsertWechatConversation(context.Background(), &store.WechatConversationRecord{
		OwnerUserID:    "owner-a",
		OwnerAccountID: "acct-a",
		PeerWxid:       "peer-1",
		SessionID:      "im-wechatbot-owner-a-peer-1",
		PeerNickname:   "客户A",
		ChatType:       "direct",
		CanSend:        true,
		SendState:      "ready",
	}); err != nil {
		t.Fatalf("upsert conversation: %v", err)
	}
	router := &mockMessageRouter{}
	adapter := NewWechatBotAdapter(st, router)

	result, err := adapter.SendMessage(context.Background(), CallerScope{OwnerUserID: "owner-a"}, SendTarget{
		Platform:       PlatformWeChatBot,
		ConversationID: "peer-1",
		Content:        "hello",
		DryRun:         true,
	})
	if err != nil {
		t.Fatalf("send dry run: %v", err)
	}
	if !result.DryRun || result.Delivered {
		t.Fatalf("result = %+v, want dry_run true delivered false", result)
	}
	if len(router.calls) != 0 {
		t.Fatalf("router calls = %d, want 0", len(router.calls))
	}
}

func TestWechatBotAdapterSendUsesOwnerScope(t *testing.T) {
	st := store.NewMemoryStore()
	if err := st.UpsertWechatConversation(context.Background(), &store.WechatConversationRecord{
		OwnerUserID:    "owner-a",
		OwnerAccountID: "acct-a",
		PeerWxid:       "peer-1",
		SessionID:      "im-wechatbot-owner-a-peer-1",
		PeerNickname:   "客户A",
		ChatType:       "direct",
		CanSend:        true,
		SendState:      "ready",
	}); err != nil {
		t.Fatalf("upsert conversation: %v", err)
	}
	router := &mockMessageRouter{}
	adapter := NewWechatBotAdapter(st, router)

	result, err := adapter.SendMessage(context.Background(), CallerScope{OwnerUserID: "owner-a"}, SendTarget{
		Platform:       PlatformWeChatBot,
		ConversationID: "peer-1",
		Content:        "hello",
	})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if !result.Delivered {
		t.Fatalf("result = %+v, want delivered", result)
	}
	if len(router.calls) != 1 {
		t.Fatalf("router calls = %d, want 1", len(router.calls))
	}
	got := router.calls[0]
	if got.Platform != imctx.PlatformWeChatBot || got.OwnerUserID != "owner-a" || got.TenantKey != "owner-a" || got.ChatID != "peer-1" {
		t.Fatalf("unexpected send request: %+v", got)
	}
}

func TestSendOnlyAdapterSearchUnsupported(t *testing.T) {
	adapter := NewSendOnlyAdapter(PlatformWeCom, &mockMessageRouter{})

	_, err := adapter.SearchRecipients(context.Background(), CallerScope{}, "张三", 10)
	if err == nil || !strings.Contains(err.Error(), "does not support recipient search") {
		t.Fatalf("err = %v, want unsupported search", err)
	}
}

func TestFeishuAdapterSearchAndSend(t *testing.T) {
	provider := &mockFeishuProvider{
		searchRaw: json.RawMessage(`[{"user_id":"u_1","open_id":"ou_1","name":"郭松"}]`),
	}
	adapter := NewFeishuAdapter(provider)

	recipients, err := adapter.SearchRecipients(context.Background(), CallerScope{}, "郭松", 10)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(recipients) != 1 || recipients[0].ID != "ou_1" || recipients[0].ExternalIDType != "open_id" {
		t.Fatalf("unexpected recipients: %+v", recipients)
	}
	result, err := adapter.SendMessage(context.Background(), CallerScope{}, SendTarget{
		Platform:    PlatformFeishu,
		RecipientID: "ou_1",
		Content:     "hello",
	})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if !result.Delivered || provider.sentTo != "ou_1" || provider.sentText != "hello" {
		t.Fatalf("result=%+v sentTo=%q sentText=%q", result, provider.sentTo, provider.sentText)
	}
}

func ptrTime(t time.Time) *time.Time {
	return &t
}
