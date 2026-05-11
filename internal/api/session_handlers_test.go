package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/chef-guo/agents-hive/internal/auth"
	"github.com/chef-guo/agents-hive/internal/channel"
	"github.com/chef-guo/agents-hive/internal/config"
	"github.com/chef-guo/agents-hive/internal/errs"
	"github.com/chef-guo/agents-hive/internal/master"
	"github.com/chef-guo/agents-hive/internal/skills"
	"github.com/chef-guo/agents-hive/internal/store"
	"github.com/chef-guo/agents-hive/internal/subagent"
)

type apiWechatSendPlugin struct {
	sent []channel.OutboundMessage
	err  error
}

type masterStubProcessor struct{}

func (p *masterStubProcessor) ProcessMessage(context.Context, string, string) (master.TaskResponse, error) {
	return master.TaskResponse{Completed: true}, nil
}

func (p *apiWechatSendPlugin) Platform() channel.Platform { return channel.PlatformWeChatBot }
func (p *apiWechatSendPlugin) Send(_ context.Context, msg channel.OutboundMessage) error {
	p.sent = append(p.sent, msg)
	return p.err
}
func (p *apiWechatSendPlugin) WebhookHandler() http.HandlerFunc {
	return func(http.ResponseWriter, *http.Request) {}
}
func (p *apiWechatSendPlugin) Verify(*http.Request) bool { return true }

// newTestServerForSessions creates a test server with session support
func newTestServerForSessions(t *testing.T) (http.Handler, *master.Master, func()) {
	t.Helper()

	logger, _ := zap.NewDevelopment()
	skillReg := skills.NewOverlayRegistry(logger)
	agentReg := subagent.NewRegistry(logger)

	st := store.NewMemoryStore()

	m := master.NewMaster(
		master.Config{Model: "test"},
		config.HITLConfig{},
		agentReg,
		skillReg.Registry,
		st,
		logger,
	)

	ctx, cancel := context.WithCancel(context.Background())
	m.Start(ctx)

	sessionDone := make(chan struct{})
	go func() {
		defer close(sessionDone)
		if err := m.SessionLoop(ctx); err != nil && err != context.Canceled {
			logger.Error("session loop error", zap.Error(err))
		}
	}()

	time.Sleep(50 * time.Millisecond)

	srv := NewServer(
		config.ServerConfig{Port: 0},
		config.HITLConfig{},
		config.WebUIConfig{},
		m,
		skillReg,
		config.Default(),
		"",  // configPath 空字符串用于测试
		nil, // channelRouter 在这些测试中不需要
		nil, // store 在这些测试中不需要
		nil, // authEngine 在这些测试中不需要
		logger,
	)

	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	return mux, m, func() {
		cancel()
		// 等待 SessionLoop 完成，确保所有后台 goroutine 停止
		select {
		case <-sessionDone:
		case <-time.After(5 * time.Second):
		}
		m.Stop()
	}
}

// --- CREATE SESSION TESTS ---

func TestHandleCreateSession_Valid(t *testing.T) {
	handler, _, cleanup := newTestServerForSessions(t)
	defer cleanup()

	reqBody := `{"name":"test-session","profile":"builder"}`
	req := httptest.NewRequest("POST", "/api/v1/sessions", strings.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d; body: %s", rec.Code, rec.Body.String())
	}

	var resp CreateSessionResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if resp.SessionID == "" {
		t.Error("expected non-empty session_id")
	}
	if resp.Name != "test-session" {
		t.Errorf("expected name 'test-session', got %s", resp.Name)
	}
}

func TestHandleCreateSession_DefaultName(t *testing.T) {
	handler, _, cleanup := newTestServerForSessions(t)
	defer cleanup()

	reqBody := `{"profile":"direct"}`
	req := httptest.NewRequest("POST", "/api/v1/sessions", strings.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d", rec.Code)
	}

	var resp CreateSessionResponse
	json.NewDecoder(rec.Body).Decode(&resp)

	if resp.Name != "新会话" {
		t.Errorf("expected default name '新会话', got %s", resp.Name)
	}
}

func TestHandleCreateSession_InvalidJSON(t *testing.T) {
	handler, _, cleanup := newTestServerForSessions(t)
	defer cleanup()

	req := httptest.NewRequest("POST", "/api/v1/sessions", strings.NewReader("not-json{"))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}
}

// --- LIST SESSIONS TESTS ---

func TestHandleListSessions_Empty(t *testing.T) {
	handler, _, cleanup := newTestServerForSessions(t)
	defer cleanup()

	req := httptest.NewRequest("GET", "/api/v1/sessions", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var resp SessionListResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if len(resp.Sessions) < 1 {
		t.Errorf("expected at least 1 session, got %d", len(resp.Sessions))
	}
}

func TestHandleListSessions_WechatbotVisibleOnlyForOwner(t *testing.T) {
	logger := zap.NewNop()
	st := store.NewMemoryStore()
	m := master.NewMaster(master.Config{Model: "test"}, config.HITLConfig{}, subagent.NewRegistry(logger), skills.NewRegistry(logger), st, logger)
	srv := NewServer(config.ServerConfig{Port: 0}, config.HITLConfig{}, config.WebUIConfig{}, m, nil, config.Default(), "", nil, st, nil, logger)

	now := time.Now().UTC()
	save := func(id, owner, name string) {
		t.Helper()
		ts := now.Format(time.RFC3339)
		if err := st.SaveSession(context.Background(), &store.SessionRecord{
			ID:             id,
			Name:           name,
			CreatedAt:      ts,
			UpdatedAt:      ts,
			LastAccessedAt: ts,
			UserID:         owner,
		}); err != nil {
			t.Fatalf("save session %s: %v", id, err)
		}
	}
	save("web-owner-1", "owner-1", "Web 会话")
	save("im-wechatbot-owner-1-wx-peer", "owner-1", "微信会话")
	save("im-feishu-tenant-chat", "owner-1", "飞书会话")
	save("im-wechatbot-owner-2-wx-peer", "owner-2", "其他人的微信")

	lastMessageAt := now.Add(time.Minute)
	if err := st.UpsertWechatConversation(context.Background(), &store.WechatConversationRecord{
		OwnerUserID:        "owner-1",
		OwnerAccountID:     "wx-owner",
		PeerWxid:           "wx-peer",
		SessionID:          "im-wechatbot-owner-1-wx-peer",
		PeerNickname:       "客户 A",
		PeerAvatarURL:      "https://example.com/a.png",
		LastMessagePreview: "最近一条微信",
		LastMessageAt:      &lastMessageAt,
		CanSend:            true,
		SendState:          "ready",
	}); err != nil {
		t.Fatalf("save conversation: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions", nil)
	req = req.WithContext(auth.WithUser(auth.WithAuthEnabled(req.Context()), &auth.User{ID: "owner-1", Status: "active"}))
	rec := httptest.NewRecorder()

	srv.handleListSessions(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp SessionListResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	seen := map[string]SessionListItem{}
	for _, item := range resp.Sessions {
		seen[item.ID] = item
	}
	if _, ok := seen["web-owner-1"]; !ok {
		t.Fatalf("普通 Web 会话应继续可见: %+v", seen)
	}
	if _, ok := seen["im-feishu-tenant-chat"]; ok {
		t.Fatalf("非 wechatbot IM 会话不应在 Web 列表展示")
	}
	if _, ok := seen["im-wechatbot-owner-2-wx-peer"]; ok {
		t.Fatalf("其他 owner 的微信会话不应可见")
	}
	wx, ok := seen["im-wechatbot-owner-1-wx-peer"]
	if !ok {
		t.Fatalf("当前 owner 的 wechatbot 会话应可见: %+v", seen)
	}
	if wx.Source != "wechatbot" || wx.SourceLabel != "微信" || wx.Name != "客户 A" || wx.PeerAvatarURL == "" {
		t.Fatalf("unexpected wechat metadata: %+v", wx)
	}
	if wx.CanSend == nil || !*wx.CanSend || wx.SendState != "ready" || wx.LastMessagePreview != "最近一条微信" {
		t.Fatalf("unexpected wechat send state: %+v", wx)
	}
}

// --- GET SESSION TESTS ---

func TestHandleGetSession_Found(t *testing.T) {
	handler, _, cleanup := newTestServerForSessions(t)
	defer cleanup()

	createReq := httptest.NewRequest("POST", "/api/v1/sessions", strings.NewReader(`{"name":"test"}`))
	createReq.Header.Set("Content-Type", "application/json")
	createRec := httptest.NewRecorder()
	handler.ServeHTTP(createRec, createReq)

	var createResp CreateSessionResponse
	json.NewDecoder(createRec.Body).Decode(&createResp)

	time.Sleep(50 * time.Millisecond)

	req := httptest.NewRequest("GET", "/api/v1/sessions/"+createResp.SessionID, nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d; body: %s", rec.Code, rec.Body.String())
	}

	var resp SessionDetailResponse
	json.NewDecoder(rec.Body).Decode(&resp)

	if resp.ID != createResp.SessionID {
		t.Errorf("expected id %s, got %s", createResp.SessionID, resp.ID)
	}
}

func TestHandleGetSession_NotFound(t *testing.T) {
	handler, _, cleanup := newTestServerForSessions(t)
	defer cleanup()

	req := httptest.NewRequest("GET", "/api/v1/sessions/nonexistent-id", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", rec.Code)
	}
}

func TestHandleGetSession_MissingID(t *testing.T) {
	handler, _, cleanup := newTestServerForSessions(t)
	defer cleanup()

	req := httptest.NewRequest("GET", "/api/v1/sessions/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	// Go 1.22 router: GET /sessions/ doesn't match GET /sessions/{id}
	if rec.Code != http.StatusNotFound && rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 404 or 405, got %d", rec.Code)
	}
}

// --- DELETE SESSION TESTS ---

func TestHandleDeleteSession_Success(t *testing.T) {
	handler, _, cleanup := newTestServerForSessions(t)
	defer cleanup()

	// 创建要删除的会话
	createReq := httptest.NewRequest("POST", "/api/v1/sessions", strings.NewReader(`{"name":"to-delete"}`))
	createReq.Header.Set("Content-Type", "application/json")
	createRec := httptest.NewRecorder()
	handler.ServeHTTP(createRec, createReq)

	var createResp CreateSessionResponse
	json.NewDecoder(createRec.Body).Decode(&createResp)
	toDeleteID := createResp.SessionID

	time.Sleep(50 * time.Millisecond)

	// 创建另一个会话，使 "to-delete" 不再是活跃会话
	createReq2 := httptest.NewRequest("POST", "/api/v1/sessions", strings.NewReader(`{"name":"keep"}`))
	createReq2.Header.Set("Content-Type", "application/json")
	createRec2 := httptest.NewRecorder()
	handler.ServeHTTP(createRec2, createReq2)

	time.Sleep(50 * time.Millisecond)

	// 删除非活跃会话
	req := httptest.NewRequest("DELETE", "/api/v1/sessions/"+toDeleteID, nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Errorf("expected 204, got %d; body: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleDeleteSession_NotFound(t *testing.T) {
	handler, _, cleanup := newTestServerForSessions(t)
	defer cleanup()

	req := httptest.NewRequest("DELETE", "/api/v1/sessions/nonexistent", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent && rec.Code != http.StatusNotFound {
		t.Errorf("expected 204 or 404, got %d", rec.Code)
	}
}

// --- SEND MESSAGE TESTS ---

func TestHandleSendMessage_EmptyContent(t *testing.T) {
	handler, _, cleanup := newTestServerForSessions(t)
	defer cleanup()

	req := httptest.NewRequest("POST", "/api/v1/sessions/test-id/messages", strings.NewReader(`{"content":""}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}
}

func TestHandleSendMessage_InvalidJSON(t *testing.T) {
	handler, _, cleanup := newTestServerForSessions(t)
	defer cleanup()

	req := httptest.NewRequest("POST", "/api/v1/sessions/test-id/messages", strings.NewReader("not-json"))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}
}

func TestHandleSendMessage_WechatbotSessionRoutesToIM(t *testing.T) {
	logger := zap.NewNop()
	st := store.NewMemoryStore()
	router := channel.NewRouter(&masterStubProcessor{}, logger)
	plugin := &apiWechatSendPlugin{}
	router.RegisterPlugin(plugin)
	m := master.NewMaster(master.Config{Model: "test"}, config.HITLConfig{}, subagent.NewRegistry(logger), skills.NewRegistry(logger), st, logger)
	subID, broadcasts := m.SubscribeWSBroadcast()
	defer m.UnsubscribeWSBroadcast(subID)
	srv := NewServer(config.ServerConfig{Port: 0}, config.HITLConfig{}, config.WebUIConfig{}, m, nil, config.Default(), "", router, st, nil, logger)

	sessionID := "im-wechatbot-owner-1-wx-peer"
	now := time.Now().Format(time.RFC3339)
	if err := st.SaveSession(context.Background(), &store.SessionRecord{
		ID:             sessionID,
		Name:           "微信会话",
		CreatedAt:      now,
		UpdatedAt:      now,
		LastAccessedAt: now,
		UserID:         "owner-1",
		Tags:           []string{"wechat"},
	}); err != nil {
		t.Fatalf("save session: %v", err)
	}
	if err := st.UpsertWechatConversation(context.Background(), &store.WechatConversationRecord{
		OwnerUserID:    "owner-1",
		OwnerAccountID: "wx-owner",
		PeerWxid:       "wx-peer",
		SessionID:      sessionID,
		CanSend:        true,
		SendState:      "ready",
	}); err != nil {
		t.Fatalf("save conversation: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/"+sessionID+"/messages", strings.NewReader(`{"content":"你好"}`))
	req.SetPathValue("id", sessionID)
	req = req.WithContext(auth.WithUser(auth.WithAuthEnabled(req.Context()), &auth.User{ID: "owner-1", Status: "active"}))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	srv.handleSendMessage(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if len(plugin.sent) != 1 {
		t.Fatalf("sent = %d, want 1", len(plugin.sent))
	}
	got := plugin.sent[0]
	if got.OwnerUserID != "owner-1" || got.TenantKey != "owner-1" || got.ChatID != "wx-peer" || got.Content != "你好" {
		t.Fatalf("unexpected outbound: %+v", got)
	}
	msgs, err := st.GetMessages(context.Background(), sessionID, 0)
	if err != nil || len(msgs) != 1 || msgs[0].Content != "你好" {
		t.Fatalf("message not appended, len=%d err=%v", len(msgs), err)
	}
	select {
	case msg := <-broadcasts:
		if msg.Type != master.EventTypeMessage || msg.SessionID != sessionID {
			t.Fatalf("unexpected broadcast: %+v", msg)
		}
	case <-time.After(time.Second):
		t.Fatal("web wechat outbound did not broadcast user message")
	}
}

func TestHandleSendMessage_WechatbotNoContextReturns409(t *testing.T) {
	logger := zap.NewNop()
	st := store.NewMemoryStore()
	router := channel.NewRouter(&masterStubProcessor{}, logger)
	router.RegisterPlugin(&apiWechatSendPlugin{})
	m := master.NewMaster(master.Config{Model: "test"}, config.HITLConfig{}, subagent.NewRegistry(logger), skills.NewRegistry(logger), st, logger)
	srv := NewServer(config.ServerConfig{Port: 0}, config.HITLConfig{}, config.WebUIConfig{}, m, nil, config.Default(), "", router, st, nil, logger)

	sessionID := "im-wechatbot-owner-1-wx-peer"
	now := time.Now().Format(time.RFC3339)
	_ = st.SaveSession(context.Background(), &store.SessionRecord{
		ID:             sessionID,
		Name:           "微信会话",
		CreatedAt:      now,
		UpdatedAt:      now,
		LastAccessedAt: now,
		UserID:         "owner-1",
	})
	_ = st.UpsertWechatConversation(context.Background(), &store.WechatConversationRecord{
		OwnerUserID:    "owner-1",
		OwnerAccountID: "wx-owner",
		PeerWxid:       "wx-peer",
		SessionID:      sessionID,
		CanSend:        false,
		SendState:      "no_context",
	})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/"+sessionID+"/messages", strings.NewReader(`{"content":"你好"}`))
	req.SetPathValue("id", sessionID)
	req = req.WithContext(auth.WithUser(auth.WithAuthEnabled(req.Context()), &auth.User{ID: "owner-1", Status: "active"}))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	srv.handleSendMessage(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandleSendMessage_WechatbotSendFailureRecordsAndBroadcasts(t *testing.T) {
	logger := zap.NewNop()
	st := store.NewMemoryStore()
	router := channel.NewRouter(&masterStubProcessor{}, logger)
	plugin := &apiWechatSendPlugin{err: errors.New("sdk temporary unavailable")}
	router.RegisterPlugin(plugin)
	m := master.NewMaster(master.Config{Model: "test"}, config.HITLConfig{}, subagent.NewRegistry(logger), skills.NewRegistry(logger), st, logger)
	subID, broadcasts := m.SubscribeWSBroadcast()
	defer m.UnsubscribeWSBroadcast(subID)
	srv := NewServer(config.ServerConfig{Port: 0}, config.HITLConfig{}, config.WebUIConfig{}, m, nil, config.Default(), "", router, st, nil, logger)

	sessionID := "im-wechatbot-owner-1-wx-peer"
	now := time.Now().Format(time.RFC3339)
	if err := st.SaveSession(context.Background(), &store.SessionRecord{
		ID:             sessionID,
		Name:           "微信会话",
		CreatedAt:      now,
		UpdatedAt:      now,
		LastAccessedAt: now,
		UserID:         "owner-1",
	}); err != nil {
		t.Fatalf("save session: %v", err)
	}
	if err := st.UpsertWechatConversation(context.Background(), &store.WechatConversationRecord{
		OwnerUserID:    "owner-1",
		OwnerAccountID: "wx-owner",
		PeerWxid:       "wx-peer",
		SessionID:      sessionID,
		CanSend:        true,
		SendState:      "ready",
	}); err != nil {
		t.Fatalf("save conversation: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/"+sessionID+"/messages", strings.NewReader(`{"content":"你好"}`))
	req.SetPathValue("id", sessionID)
	req = req.WithContext(auth.WithUser(auth.WithAuthEnabled(req.Context()), &auth.User{ID: "owner-1", Status: "active"}))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	srv.handleSendMessage(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502; body=%s", rec.Code, rec.Body.String())
	}
	msgs, err := st.GetMessages(context.Background(), sessionID, 0)
	if err != nil {
		t.Fatalf("get messages: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("messages len = %d, want 1", len(msgs))
	}
	if msgs[0].Role != "system" || !strings.Contains(msgs[0].Content, "微信发送失败") {
		t.Fatalf("unexpected failure message: %+v", msgs[0])
	}
	var meta map[string]any
	if err := json.Unmarshal(msgs[0].Metadata, &meta); err != nil {
		t.Fatalf("unmarshal metadata: %v", err)
	}
	if meta["send_status"] != "failed" || meta["is_error"] != true {
		t.Fatalf("unexpected failure metadata: %+v", meta)
	}
	conv, err := st.GetWechatConversationByOwnerPeer(context.Background(), "owner-1", "wx-peer")
	if err != nil {
		t.Fatalf("get conversation: %v", err)
	}
	if conv.CanSend || conv.SendState != "failed" {
		t.Fatalf("send state = can_send=%v state=%q, want false/failed", conv.CanSend, conv.SendState)
	}
	select {
	case msg := <-broadcasts:
		if msg.Type != master.EventTypeError || msg.SessionID != sessionID {
			t.Fatalf("unexpected broadcast: %+v", msg)
		}
	case <-time.After(time.Second):
		t.Fatal("web wechat outbound failure did not broadcast error")
	}
}

func TestHandleSendMessage_WechatbotWrappedNoContextReturns409(t *testing.T) {
	logger := zap.NewNop()
	st := store.NewMemoryStore()
	router := channel.NewRouter(&masterStubProcessor{}, logger)
	plugin := &apiWechatSendPlugin{err: errors.New("send failed: no context_token for user wx-peer")}
	router.RegisterPlugin(plugin)
	m := master.NewMaster(master.Config{Model: "test"}, config.HITLConfig{}, subagent.NewRegistry(logger), skills.NewRegistry(logger), st, logger)
	srv := NewServer(config.ServerConfig{Port: 0}, config.HITLConfig{}, config.WebUIConfig{}, m, nil, config.Default(), "", router, st, nil, logger)

	sessionID := "im-wechatbot-owner-1-wx-peer"
	now := time.Now().Format(time.RFC3339)
	_ = st.SaveSession(context.Background(), &store.SessionRecord{
		ID:             sessionID,
		Name:           "微信会话",
		CreatedAt:      now,
		UpdatedAt:      now,
		LastAccessedAt: now,
		UserID:         "owner-1",
	})
	_ = st.UpsertWechatConversation(context.Background(), &store.WechatConversationRecord{
		OwnerUserID:    "owner-1",
		OwnerAccountID: "wx-owner",
		PeerWxid:       "wx-peer",
		SessionID:      sessionID,
		CanSend:        true,
		SendState:      "ready",
	})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/"+sessionID+"/messages", strings.NewReader(`{"content":"你好"}`))
	req.SetPathValue("id", sessionID)
	req = req.WithContext(auth.WithUser(auth.WithAuthEnabled(req.Context()), &auth.User{ID: "owner-1", Status: "active"}))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	srv.handleSendMessage(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body=%s", rec.Code, rec.Body.String())
	}
	conv, err := st.GetWechatConversationByOwnerPeer(context.Background(), "owner-1", "wx-peer")
	if err != nil {
		t.Fatalf("get conversation: %v", err)
	}
	if conv.CanSend || conv.SendState != "no_context" {
		t.Fatalf("send state = can_send=%v state=%q, want false/no_context", conv.CanSend, conv.SendState)
	}
}

func TestWebSendToWechatRejectsCrossUser(t *testing.T) {
	logger := zap.NewNop()
	st := store.NewMemoryStore()
	router := channel.NewRouter(&masterStubProcessor{}, logger)
	plugin := &apiWechatSendPlugin{}
	router.RegisterPlugin(plugin)
	m := master.NewMaster(master.Config{Model: "test"}, config.HITLConfig{}, subagent.NewRegistry(logger), skills.NewRegistry(logger), st, logger)
	srv := NewServer(config.ServerConfig{Port: 0}, config.HITLConfig{}, config.WebUIConfig{}, m, nil, config.Default(), "", router, st, nil, logger)

	sessionID := "im-wechatbot-owner-1-wx-peer"
	now := time.Now().Format(time.RFC3339)
	if err := st.SaveSession(context.Background(), &store.SessionRecord{
		ID:             sessionID,
		Name:           "微信会话",
		CreatedAt:      now,
		UpdatedAt:      now,
		LastAccessedAt: now,
		UserID:         "owner-1",
		Tags:           []string{"wechat"},
	}); err != nil {
		t.Fatalf("save session: %v", err)
	}
	if err := st.UpsertWechatConversation(context.Background(), &store.WechatConversationRecord{
		OwnerUserID:    "owner-1",
		OwnerAccountID: "wx-owner",
		PeerWxid:       "wx-peer",
		SessionID:      sessionID,
		CanSend:        true,
		SendState:      "ready",
	}); err != nil {
		t.Fatalf("save conversation: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/"+sessionID+"/messages", strings.NewReader(`{"content":"越权"}`))
	req.SetPathValue("id", sessionID)
	req = req.WithContext(auth.WithUser(auth.WithAuthEnabled(req.Context()), &auth.User{ID: "owner-2", Status: "active"}))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	srv.handleSendMessage(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", rec.Code, rec.Body.String())
	}
	if len(plugin.sent) != 0 {
		t.Fatalf("cross-user send should not reach plugin, sent=%d", len(plugin.sent))
	}
}

// --- INTEGRATION TEST ---

func TestSessionAPI_FullWorkflow(t *testing.T) {
	handler, _, cleanup := newTestServerForSessions(t)
	defer cleanup()

	// 1. Create session
	createReq := httptest.NewRequest("POST", "/api/v1/sessions", bytes.NewBufferString(`{"name":"workflow-test"}`))
	createReq.Header.Set("Content-Type", "application/json")
	createRec := httptest.NewRecorder()
	handler.ServeHTTP(createRec, createReq)

	if createRec.Code != http.StatusCreated {
		t.Fatalf("create failed: %d", createRec.Code)
	}

	var createResp CreateSessionResponse
	json.NewDecoder(createRec.Body).Decode(&createResp)

	time.Sleep(100 * time.Millisecond) // Wait for session processing

	// 2. List sessions
	listReq := httptest.NewRequest("GET", "/api/v1/sessions", nil)
	listRec := httptest.NewRecorder()
	handler.ServeHTTP(listRec, listReq)

	if listRec.Code != http.StatusOK {
		t.Fatalf("list failed: %d", listRec.Code)
	}

	// 3. Get session details
	getReq := httptest.NewRequest("GET", "/api/v1/sessions/"+createResp.SessionID, nil)
	getRec := httptest.NewRecorder()
	handler.ServeHTTP(getRec, getReq)

	if getRec.Code != http.StatusOK {
		t.Fatalf("get failed: %d", getRec.Code)
	}

	// 4. Create another session so the first one is no longer active
	createReq2 := httptest.NewRequest("POST", "/api/v1/sessions", bytes.NewBufferString(`{"name":"keep"}`))
	createReq2.Header.Set("Content-Type", "application/json")
	createRec2 := httptest.NewRecorder()
	handler.ServeHTTP(createRec2, createReq2)

	if createRec2.Code != http.StatusCreated {
		t.Fatalf("create second session failed: %d", createRec2.Code)
	}

	time.Sleep(50 * time.Millisecond)

	// 5. Delete first session (now inactive)
	deleteReq := httptest.NewRequest("DELETE", "/api/v1/sessions/"+createResp.SessionID, nil)
	deleteRec := httptest.NewRecorder()
	handler.ServeHTTP(deleteRec, deleteReq)

	if deleteRec.Code != http.StatusNoContent {
		t.Fatalf("delete failed: %d; body: %s", deleteRec.Code, deleteRec.Body.String())
	}
}

// --- JOURNAL TESTS ---

func TestHandleGetSessionJournal_EmptyID(t *testing.T) {
	handler, _, cleanup := newTestServerForSessions(t)
	defer cleanup()

	// Go 1.22 路由: GET /api/v1/sessions/{id}/journal 中 {id} 为空时
	// "/api/v1/sessions//journal" 会被 ServeMux 清理为 301 重定向
	// 这验证了空 ID 不会到达 handler 返回 200
	req := httptest.NewRequest("GET", "/api/v1/sessions//journal", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest && rec.Code != http.StatusNotFound &&
		rec.Code != http.StatusMovedPermanently {
		t.Errorf("expected 400, 404, or 301, got %d; body: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleGetSessionJournal_NotFound(t *testing.T) {
	handler, _, cleanup := newTestServerForSessions(t)
	defer cleanup()

	req := httptest.NewRequest("GET", "/api/v1/sessions/nonexistent-id/journal", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d; body: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleGetSessionJournal_JournalNotAvailable(t *testing.T) {
	handler, _, cleanup := newTestServerForSessions(t)
	defer cleanup()

	// 先创建 session
	createReq := httptest.NewRequest("POST", "/api/v1/sessions", strings.NewReader(`{"name":"journal-test"}`))
	createReq.Header.Set("Content-Type", "application/json")
	createRec := httptest.NewRecorder()
	handler.ServeHTTP(createRec, createReq)

	if createRec.Code != http.StatusCreated {
		t.Fatalf("create session failed: %d", createRec.Code)
	}

	var createResp CreateSessionResponse
	json.NewDecoder(createRec.Body).Decode(&createResp)

	time.Sleep(50 * time.Millisecond)

	// newTestServerForSessions 不注入 journal，所以 m.journal==nil → 501
	req := httptest.NewRequest("GET", "/api/v1/sessions/"+createResp.SessionID+"/journal", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotImplemented {
		t.Errorf("expected 501, got %d; body: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleGetSessionJournal_Success(t *testing.T) {
	handler, _, cleanup := newTestServerForSessions(t)
	defer cleanup()

	// 先创建 session
	createReq := httptest.NewRequest("POST", "/api/v1/sessions", strings.NewReader(`{"name":"journal-success"}`))
	createReq.Header.Set("Content-Type", "application/json")
	createRec := httptest.NewRecorder()
	handler.ServeHTTP(createRec, createReq)

	if createRec.Code != http.StatusCreated {
		t.Fatalf("create session failed: %d", createRec.Code)
	}

	var createResp CreateSessionResponse
	json.NewDecoder(createRec.Body).Decode(&createResp)

	time.Sleep(50 * time.Millisecond)

	// 无真实 PG journal，session 存在 + journal 未启用 → 501（非 404）
	req := httptest.NewRequest("GET", "/api/v1/sessions/"+createResp.SessionID+"/journal", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	// 验证：session 存在时不返回 404，而是 501（journal 未启用）
	if rec.Code == http.StatusNotFound {
		t.Errorf("session exists but got 404; expected 501")
	}
	if rec.Code != http.StatusNotImplemented {
		t.Errorf("expected 501 (journal not available), got %d; body: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleGetSessionJournal_LimitParam(t *testing.T) {
	handler, _, cleanup := newTestServerForSessions(t)
	defer cleanup()

	// 先创建 session
	createReq := httptest.NewRequest("POST", "/api/v1/sessions", strings.NewReader(`{"name":"limit-test"}`))
	createReq.Header.Set("Content-Type", "application/json")
	createRec := httptest.NewRecorder()
	handler.ServeHTTP(createRec, createReq)

	if createRec.Code != http.StatusCreated {
		t.Fatalf("create session failed: %d", createRec.Code)
	}

	var createResp CreateSessionResponse
	json.NewDecoder(createRec.Body).Decode(&createResp)

	time.Sleep(50 * time.Millisecond)

	// limit=5000 超过 2000 应被截断；由于 journal 未启用，仍返回 501
	// 但关键是 limit 参数不会导致 400 错误
	req := httptest.NewRequest("GET", "/api/v1/sessions/"+createResp.SessionID+"/journal?limit=5000", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	// 不应因 limit 参数返回 400
	if rec.Code == http.StatusBadRequest {
		t.Errorf("limit=5000 should not cause 400; got body: %s", rec.Body.String())
	}
	// journal 未启用 → 501
	if rec.Code != http.StatusNotImplemented {
		t.Errorf("expected 501, got %d; body: %s", rec.Code, rec.Body.String())
	}

	// 测试无效 limit 参数（非数字）也不应报错，应被忽略
	req2 := httptest.NewRequest("GET", "/api/v1/sessions/"+createResp.SessionID+"/journal?limit=abc", nil)
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, req2)

	if rec2.Code == http.StatusBadRequest {
		t.Errorf("invalid limit should be ignored, not cause 400; got body: %s", rec2.Body.String())
	}
}

func TestHandleGetJournalStats_EmptyParam(t *testing.T) {
	handler, _, cleanup := newTestServerForSessions(t)
	defer cleanup()

	req := httptest.NewRequest("GET", "/api/v1/journal/stats", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d; body: %s", rec.Code, rec.Body.String())
	}

	// 空字符串参数也应返回 400
	req2 := httptest.NewRequest("GET", "/api/v1/journal/stats?session_ids=", nil)
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, req2)

	if rec2.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for empty session_ids, got %d; body: %s", rec2.Code, rec2.Body.String())
	}
}

func TestHandleGetJournalStats_JournalNotAvailable(t *testing.T) {
	handler, _, cleanup := newTestServerForSessions(t)
	defer cleanup()

	req := httptest.NewRequest("GET", "/api/v1/journal/stats?session_ids=id1,id2", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	// newTestServerForSessions 不注入 journal → 501
	if rec.Code != http.StatusNotImplemented {
		t.Errorf("expected 501, got %d; body: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleGetJournalStats_Success(t *testing.T) {
	handler, _, cleanup := newTestServerForSessions(t)
	defer cleanup()

	// 创建 session 获取真实 ID
	createReq := httptest.NewRequest("POST", "/api/v1/sessions", strings.NewReader(`{"name":"stats-test"}`))
	createReq.Header.Set("Content-Type", "application/json")
	createRec := httptest.NewRecorder()
	handler.ServeHTTP(createRec, createReq)

	if createRec.Code != http.StatusCreated {
		t.Fatalf("create session failed: %d", createRec.Code)
	}

	var createResp CreateSessionResponse
	json.NewDecoder(createRec.Body).Decode(&createResp)

	time.Sleep(50 * time.Millisecond)

	// 用真实 session ID 查询 stats；journal 未启用 → 501（非 400）
	req := httptest.NewRequest("GET", "/api/v1/journal/stats?session_ids="+createResp.SessionID, nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	// 验证：有效 session_ids 参数不返回 400
	if rec.Code == http.StatusBadRequest {
		t.Errorf("valid session_ids should not cause 400; got body: %s", rec.Body.String())
	}
	// journal 未启用 → 501
	if rec.Code != http.StatusNotImplemented {
		t.Errorf("expected 501 (journal not available), got %d; body: %s", rec.Code, rec.Body.String())
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// checkSessionOwnership 单元测试
// ─────────────────────────────────────────────────────────────────────────────

// TestCheckSessionOwnership 验证 P1 安全隔离：跨用户访问应被阻断。
func TestCheckSessionOwnership(t *testing.T) {
	srv := &Server{} // checkSessionOwnership 不依赖 Server 字段

	userA := &auth.User{ID: "user-a", Role: "user"}
	userB := &auth.User{ID: "user-b", Role: "user"}
	admin := &auth.User{ID: "admin-1", Role: "admin"}

	sessOwnedByA := &store.SessionRecord{ID: "sess-1", UserID: "user-a"}
	sessNoOwner := &store.SessionRecord{ID: "sess-2", UserID: ""}

	// 构造带 auth context 的 request 的辅助函数
	reqWith := func(u *auth.User) *http.Request {
		r := httptest.NewRequest("GET", "/", nil)
		ctx := auth.WithAuthEnabled(r.Context())
		if u != nil {
			ctx = auth.WithUser(ctx, u)
		}
		return r.WithContext(ctx)
	}
	reqNoAuth := func() *http.Request {
		return httptest.NewRequest("GET", "/", nil)
	}

	tests := []struct {
		name       string
		req        *http.Request
		session    *store.SessionRecord
		wantAllow  bool
		wantStatus int // 0 表示不检查（允许时 w 未写入）
	}{
		{
			name:      "auth 未启用 → 放行",
			req:       reqNoAuth(),
			session:   sessOwnedByA,
			wantAllow: true,
		},
		{
			name:      "auth 启用 + 合法 owner → 放行",
			req:       reqWith(userA),
			session:   sessOwnedByA,
			wantAllow: true,
		},
		{
			name:       "auth 启用 + 跨用户访问 → 403",
			req:        reqWith(userB),
			session:    sessOwnedByA,
			wantAllow:  false,
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "admin 访问他人 session → 403（admin 也只能看自己的）",
			req:        reqWith(admin),
			session:    sessOwnedByA,
			wantAllow:  false,
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "无主 session（旧数据）→ 403（无主 session 不可见）",
			req:        reqWith(userB),
			session:    sessNoOwner,
			wantAllow:  false,
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "auth 启用 + user==nil → 401",
			req:        reqWith(nil),
			session:    sessOwnedByA,
			wantAllow:  false,
			wantStatus: http.StatusUnauthorized,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			got := srv.checkSessionOwnership(w, tc.req, tc.session)
			if got != tc.wantAllow {
				t.Errorf("checkSessionOwnership() = %v, want %v; body: %s", got, tc.wantAllow, w.Body.String())
			}
			if !tc.wantAllow && tc.wantStatus != 0 && w.Code != tc.wantStatus {
				t.Errorf("status = %d, want %d", w.Code, tc.wantStatus)
			}
			// 验证 403 响应的错误码是 CodePermissionDenied（不是 CodeNotFound）
			if !tc.wantAllow && w.Code == http.StatusForbidden {
				var resp ErrorResponse
				if err := json.NewDecoder(w.Body).Decode(&resp); err == nil {
					if resp.Code != errs.CodePermissionDenied {
						t.Errorf("403 body.code = %d, want CodePermissionDenied (%d)", resp.Code, errs.CodePermissionDenied)
					}
				}
			}
		})
	}
}
