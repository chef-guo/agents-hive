package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/chef-guo/agents-hive/internal/auth"
	"github.com/chef-guo/agents-hive/internal/channel/wechatbot"
	"github.com/chef-guo/agents-hive/internal/config"
	"github.com/chef-guo/agents-hive/internal/store"
	"go.uber.org/zap"
)

type fakeWeChatService struct {
	statusOwner        string
	loginOwner         string
	loginForce         bool
	listOwner          string
	status             wechatbot.ConnectionStatus
	conversations      []wechatbot.Conversation
	conversationsError error
}

func (s *fakeWeChatService) Status(_ context.Context, ownerUserID string) (wechatbot.ConnectionStatus, error) {
	s.statusOwner = ownerUserID
	return s.status, nil
}

func (s *fakeWeChatService) Login(_ context.Context, ownerUserID string, force bool) (wechatbot.ConnectionStatus, error) {
	s.loginOwner = ownerUserID
	s.loginForce = force
	return s.status, nil
}

func (s *fakeWeChatService) Logout(context.Context, string) error { return nil }
func (s *fakeWeChatService) Subscribe(string) (<-chan wechatbot.Event, func()) {
	ch := make(chan wechatbot.Event)
	close(ch)
	return ch, func() {}
}
func (s *fakeWeChatService) ListConversations(_ context.Context, ownerUserID string) ([]wechatbot.Conversation, error) {
	s.listOwner = ownerUserID
	if s.conversationsError != nil {
		return nil, s.conversationsError
	}
	return s.conversations, nil
}

func TestWeChatStatusRequiresService(t *testing.T) {
	srv := NewServer(config.ServerConfig{Port: 0}, config.HITLConfig{}, config.WebUIConfig{}, nil, nil, config.Default(), "", nil, nil, nil, zap.NewNop())
	req := httptest.NewRequest(http.MethodGet, "/api/v1/wechat/status", nil)
	req = req.WithContext(auth.WithUser(auth.WithAuthEnabled(req.Context()), &auth.User{ID: "owner-1", Status: "active"}))
	rec := httptest.NewRecorder()

	srv.handleWeChatStatus(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503; body=%s", rec.Code, rec.Body.String())
	}
}

func TestWeChatStatusUsesAuthenticatedOwner(t *testing.T) {
	service := &fakeWeChatService{status: wechatbot.ConnectionStatus{Enabled: true, Status: wechatbot.StatusOnline}}
	srv := NewServer(config.ServerConfig{Port: 0}, config.HITLConfig{}, config.WebUIConfig{}, nil, nil, config.Default(), "", nil, nil, nil, zap.NewNop())
	srv.SetWeChatBotService(service)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/wechat/status", nil)
	req = req.WithContext(auth.WithUser(auth.WithAuthEnabled(req.Context()), &auth.User{ID: "owner-1", Status: "active"}))
	rec := httptest.NewRecorder()

	srv.handleWeChatStatus(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if service.statusOwner != "owner-1" {
		t.Fatalf("owner = %q, want owner-1", service.statusOwner)
	}
	if !strings.Contains(rec.Body.String(), `"status":"online"`) {
		t.Fatalf("missing online status: %s", rec.Body.String())
	}
}

func TestWeChatReloginForcesLogin(t *testing.T) {
	service := &fakeWeChatService{status: wechatbot.ConnectionStatus{Enabled: true, Status: wechatbot.StatusWaitingQRScan}}
	srv := NewServer(config.ServerConfig{Port: 0}, config.HITLConfig{}, config.WebUIConfig{}, nil, nil, config.Default(), "", nil, nil, nil, zap.NewNop())
	srv.SetWeChatBotService(service)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/wechat/relogin", nil)
	req = req.WithContext(auth.WithUser(auth.WithAuthEnabled(req.Context()), &auth.User{ID: "owner-1", Status: "active"}))
	rec := httptest.NewRecorder()

	srv.handleWeChatRelogin(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if service.loginOwner != "owner-1" || !service.loginForce {
		t.Fatalf("login owner/force = %q/%v, want owner-1/true", service.loginOwner, service.loginForce)
	}
}

func TestWeChatConversationsExposeOnlyContactStatus(t *testing.T) {
	st := store.NewMemoryStore()
	now := time.Date(2026, 5, 11, 10, 0, 0, 0, time.UTC)
	if err := st.UpsertWechatConversation(context.Background(), &store.WechatConversationRecord{
		OwnerUserID:        "owner-1",
		OwnerAccountID:     "wx-owner",
		PeerWxid:           "wx-peer",
		SessionID:          "im-wechatbot-owner-1-wx-peer",
		PeerNickname:       "客户 A",
		PeerAvatarURL:      "https://example.test/avatar.png",
		ChatType:           "direct",
		LastMessagePreview: "这条微信正文不能到浏览器",
		LastMessageAt:      &now,
		CanSend:            true,
		SendState:          "ready",
	}); err != nil {
		t.Fatalf("save conversation: %v", err)
	}

	service := wechatbot.NewService(nil, st)
	srv := NewServer(config.ServerConfig{Port: 0}, config.HITLConfig{}, config.WebUIConfig{}, nil, nil, config.Default(), "", nil, st, nil, zap.NewNop())
	srv.SetWeChatBotService(service)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/wechat/conversations", nil)
	req = req.WithContext(auth.WithUser(auth.WithAuthEnabled(req.Context()), &auth.User{ID: "owner-1", Status: "active"}))
	rec := httptest.NewRecorder()

	srv.handleWeChatConversations(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, forbidden := range []string{"last_message_preview", "这条微信正文不能到浏览器", "session_id", "im-wechatbot-owner-1-wx-peer", "can_send", "send_state"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("wechat conversations leaked %q in body: %s", forbidden, body)
		}
	}

	var resp struct {
		Conversations []wechatbot.Conversation `json:"conversations"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp.Conversations) != 1 {
		t.Fatalf("conversations len = %d, want 1", len(resp.Conversations))
	}
	if got := resp.Conversations[0].PeerWxid; got != "wx-peer" {
		t.Fatalf("peer wxid = %q, want wx-peer", got)
	}
	if resp.Conversations[0].LastMessageAt == nil {
		t.Fatalf("last_message_at should remain available as contact status")
	}
}
