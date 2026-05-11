package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/chef-guo/agents-hive/internal/auth"
	"github.com/chef-guo/agents-hive/internal/channel/wechatbot"
	"github.com/chef-guo/agents-hive/internal/config"
	"go.uber.org/zap"
)

type fakeWeChatService struct {
	statusOwner string
	loginOwner  string
	loginForce  bool
	status      wechatbot.ConnectionStatus
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
func (s *fakeWeChatService) ListConversations(context.Context, string) ([]wechatbot.Conversation, error) {
	return nil, nil
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
