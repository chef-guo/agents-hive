package api

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/chef-guo/agents-hive/internal/agentquality"
	"github.com/chef-guo/agents-hive/internal/asset"
	"github.com/chef-guo/agents-hive/internal/auth"
	"github.com/chef-guo/agents-hive/internal/config"
	"github.com/chef-guo/agents-hive/internal/errs"
	"github.com/chef-guo/agents-hive/internal/journal"
	"github.com/chef-guo/agents-hive/internal/master"
	"github.com/chef-guo/agents-hive/internal/skills"
	"github.com/chef-guo/agents-hive/internal/store"
	"github.com/chef-guo/agents-hive/internal/subagent"
	"github.com/chef-guo/agents-hive/internal/toolruntime"
)

// newTestServerForSessions creates a test server with session support
func newTestServerForSessions(t *testing.T) (http.Handler, *master.Master, func()) {
	t.Helper()
	handler, m, _, cleanup := newTestServerForSessionsWithStore(t)
	return handler, m, cleanup
}

func newTestServerForSessionsWithStore(t *testing.T) (http.Handler, *master.Master, *store.MemoryStore, func()) {
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
		st,
		nil, // authEngine 在这些测试中不需要
		logger,
	)
	srv.SetQualityEvalRunner(agentquality.AgentRunEvalRunner{
		Adapter: master.NewAgentQualityRunAdapter(m),
	})
	srv.SetQualityShadowEvalStore(agentquality.NewInMemoryShadowEvalResultStore())

	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	return mux, m, st, func() {
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

func TestHandleListSessions_HidesIMSessions(t *testing.T) {
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
	save("im-dingtalk-tenant-chat", "owner-1", "钉钉会话")
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
		t.Fatalf("飞书 IM 会话不应在 Web 列表展示")
	}
	if _, ok := seen["im-dingtalk-tenant-chat"]; ok {
		t.Fatalf("钉钉 IM 会话不应在 Web 列表展示")
	}
	if _, ok := seen["im-wechatbot-owner-1-wx-peer"]; ok {
		t.Fatalf("微信 IM 会话不应在 Web 列表展示")
	}
	if _, ok := seen["im-wechatbot-owner-2-wx-peer"]; ok {
		t.Fatalf("其他 owner 的微信会话不应可见")
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

func TestHandleGetSessionIncludesKBDomainID(t *testing.T) {
	handler, _, st, cleanup := newTestServerForSessionsWithStore(t)
	defer cleanup()

	now := time.Now().Format(time.RFC3339)
	sessionID := "kb-domain-session"
	if err := st.SaveSession(context.Background(), &store.SessionRecord{
		ID:             sessionID,
		Name:           "kb domain",
		CreatedAt:      now,
		UpdatedAt:      now,
		LastAccessedAt: now,
		KBDomainID:     "support",
	}); err != nil {
		t.Fatalf("save session: %v", err)
	}

	req := httptest.NewRequest("GET", "/api/v1/sessions/"+sessionID, nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d; body: %s", rec.Code, rec.Body.String())
	}
	var resp SessionDetailResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.KBDomainID != "support" {
		t.Fatalf("kb_domain_id = %q, want support", resp.KBDomainID)
	}
}

func TestHandleUpdateSessionPersistsKBDomainID(t *testing.T) {
	handler, _, st, cleanup := newTestServerForSessionsWithStore(t)
	defer cleanup()

	now := time.Now().Format(time.RFC3339)
	sessionID := "kb-domain-update-session"
	if err := st.SaveSession(context.Background(), &store.SessionRecord{
		ID:             sessionID,
		Name:           "kb domain",
		CreatedAt:      now,
		UpdatedAt:      now,
		LastAccessedAt: now,
	}); err != nil {
		t.Fatalf("save session: %v", err)
	}

	req := httptest.NewRequest(http.MethodPatch, "/api/v1/sessions/"+sessionID, strings.NewReader(`{"kb_domain_id":"support"}`))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d; body: %s", rec.Code, rec.Body.String())
	}
	record, err := st.LoadSession(context.Background(), sessionID)
	if err != nil {
		t.Fatalf("load session: %v", err)
	}
	if record.KBDomainID != "support" {
		t.Fatalf("kb_domain_id = %q, want support", record.KBDomainID)
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

func TestWebSessionHandlersRejectIMSessions(t *testing.T) {
	logger := zap.NewNop()
	st := store.NewMemoryStore()
	m := master.NewMaster(master.Config{Model: "test"}, config.HITLConfig{}, subagent.NewRegistry(logger), skills.NewRegistry(logger), st, logger)
	srv := NewServer(config.ServerConfig{Port: 0}, config.HITLConfig{}, config.WebUIConfig{}, m, nil, config.Default(), "", nil, st, nil, logger)

	now := time.Now().Format(time.RFC3339)
	for _, sessionID := range []string{"im-wechatbot-owner-1-wx-peer", "im-feishu-tenant-chat"} {
		if err := st.SaveSession(context.Background(), &store.SessionRecord{
			ID:             sessionID,
			Name:           "IM 会话",
			CreatedAt:      now,
			UpdatedAt:      now,
			LastAccessedAt: now,
			UserID:         "owner-1",
		}); err != nil {
			t.Fatalf("save session %s: %v", sessionID, err)
		}
		if err := st.AddMessage(context.Background(), sessionID, "user", "不应出现在 Web", nil); err != nil {
			t.Fatalf("save message %s: %v", sessionID, err)
		}
	}

	tests := []struct {
		name      string
		sessionID string
		call      func(http.ResponseWriter, *http.Request)
		method    string
		path      string
		body      string
	}{
		{
			name:      "微信详情不可读",
			sessionID: "im-wechatbot-owner-1-wx-peer",
			call:      srv.handleGetSession,
			method:    http.MethodGet,
			path:      "/api/v1/sessions/im-wechatbot-owner-1-wx-peer",
		},
		{
			name:      "微信消息不可读",
			sessionID: "im-wechatbot-owner-1-wx-peer",
			call:      srv.handleGetMessages,
			method:    http.MethodGet,
			path:      "/api/v1/sessions/im-wechatbot-owner-1-wx-peer/messages",
		},
		{
			name:      "微信不可从 Web 发送",
			sessionID: "im-wechatbot-owner-1-wx-peer",
			call:      srv.handleSendMessage,
			method:    http.MethodPost,
			path:      "/api/v1/sessions/im-wechatbot-owner-1-wx-peer/messages",
			body:      `{"content":"你好"}`,
		},
		{
			name:      "飞书详情不可读",
			sessionID: "im-feishu-tenant-chat",
			call:      srv.handleGetSession,
			method:    http.MethodGet,
			path:      "/api/v1/sessions/im-feishu-tenant-chat",
		},
		{
			name:      "飞书消息不可读",
			sessionID: "im-feishu-tenant-chat",
			call:      srv.handleGetMessages,
			method:    http.MethodGet,
			path:      "/api/v1/sessions/im-feishu-tenant-chat/messages",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			body := strings.NewReader(tc.body)
			req := httptest.NewRequest(tc.method, tc.path, body)
			req.SetPathValue("id", tc.sessionID)
			req = req.WithContext(auth.WithUser(auth.WithAuthEnabled(req.Context()), &auth.User{ID: "owner-1", Status: "active"}))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()

			tc.call(rec, req)

			if rec.Code != http.StatusNotFound {
				t.Fatalf("status = %d, want 404; body=%s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestHandleGetMessagesPreservesRecoverableToolMetadata(t *testing.T) {
	handler, _, st, cleanup := newTestServerForSessionsWithStore(t)
	defer cleanup()

	now := time.Now().Format(time.RFC3339)
	sessionID := "recoverable-tool-session"
	if err := st.SaveSession(context.Background(), &store.SessionRecord{
		ID:             sessionID,
		Name:           "recoverable tool metadata",
		CreatedAt:      now,
		UpdatedAt:      now,
		LastAccessedAt: now,
	}); err != nil {
		t.Fatalf("save session: %v", err)
	}
	content := toolruntime.RecoverableToolCallErrorContent("approval_channel_missing", "需要审批")
	if err := st.AddMessage(context.Background(), sessionID, "tool", content, map[string]any{
		"tool_call_id": "call-send",
		"tool_name":    "feishu_api",
		"is_error":     true,
		"recoverable":  true,
		"terminal":     false,
		"error_kind":   "approval_channel_missing",
	}); err != nil {
		t.Fatalf("save message: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/"+sessionID+"/messages", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp MessagesListResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp.Messages) != 1 {
		t.Fatalf("messages = %d, want 1", len(resp.Messages))
	}
	msg := resp.Messages[0]
	if !msg.IsError || !msg.Recoverable || msg.Terminal || msg.ErrorKind != "approval_channel_missing" {
		t.Fatalf("message metadata = %+v, want recoverable non-terminal approval_channel_missing", msg)
	}
}

func TestHandleGetMessagesExposesAssistantCitations(t *testing.T) {
	handler, _, st, cleanup := newTestServerForSessionsWithStore(t)
	defer cleanup()

	now := time.Now().Format(time.RFC3339)
	sessionID := "assistant-citations-session"
	if err := st.SaveSession(context.Background(), &store.SessionRecord{
		ID:             sessionID,
		Name:           "assistant citations",
		CreatedAt:      now,
		UpdatedAt:      now,
		LastAccessedAt: now,
	}); err != nil {
		t.Fatalf("save session: %v", err)
	}
	citations := `[{"token":"kbref-token","doc_id":"doc-1","node_id":"0000","verified":true}]`
	if err := st.AddMessage(context.Background(), sessionID, "assistant", "answer", map[string]any{
		"citations": citations,
	}); err != nil {
		t.Fatalf("save message: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/"+sessionID+"/messages", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp MessagesListResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp.Messages) != 1 || len(resp.Messages[0].Citations) != 1 {
		t.Fatalf("messages = %+v, want one citation", resp.Messages)
	}
	citation, _ := resp.Messages[0].Citations[0].(map[string]any)
	if citation["token"] != "kbref-token" || citation["doc_id"] != "doc-1" {
		t.Fatalf("citation = %+v", citation)
	}
}

func TestHandleGetMessagesExposesAssistantArtifacts(t *testing.T) {
	handler, _, st, cleanup := newTestServerForSessionsWithStore(t)
	defer cleanup()

	now := time.Now().Format(time.RFC3339)
	sessionID := "assistant-artifacts-session"
	if err := st.SaveSession(context.Background(), &store.SessionRecord{
		ID:             sessionID,
		Name:           "assistant artifacts",
		CreatedAt:      now,
		UpdatedAt:      now,
		LastAccessedAt: now,
	}); err != nil {
		t.Fatalf("save session: %v", err)
	}
	artifacts := `[{"uri":"asset://agent/user/u1/session/s1/a.txt","title":"Report","type":"markdown","mime_type":"text/markdown","size":6,"content_hash":"abc"}]`
	if err := st.AddMessage(context.Background(), sessionID, "assistant", "answer", map[string]any{
		"artifacts": artifacts,
	}); err != nil {
		t.Fatalf("save message: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/"+sessionID+"/messages", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp MessagesListResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp.Messages) != 1 || len(resp.Messages[0].Artifacts) != 1 {
		t.Fatalf("messages = %+v, want one artifact", resp.Messages)
	}
	artifact, _ := resp.Messages[0].Artifacts[0].(map[string]any)
	if artifact["uri"] == "" || artifact["title"] != "Report" {
		t.Fatalf("artifact = %+v", artifact)
	}
}

func TestPersistChatAttachmentsUploadsToAssetService(t *testing.T) {
	ctx := context.Background()
	local, err := asset.NewLocalStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewLocalStore() error = %v", err)
	}
	svc, err := asset.NewService(local, newAPIAssetMetaStore())
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	srv := &Server{assetService: svc}
	data := []byte("hello attachment")
	attachments := []master.FileAttachment{{
		Filename: "note.txt",
		MimeType: "text/plain",
		Data:     base64.StdEncoding.EncodeToString(data),
	}}
	if err := srv.persistChatAttachments(ctx, "session-1", &store.SessionRecord{UserID: "u1"}, attachments, [][]byte{data}); err != nil {
		t.Fatalf("persistChatAttachments() error = %v", err)
	}
	if attachments[0].AssetURI == "" || attachments[0].ContentHash != attachmentContentHash(data) || attachments[0].Size != int64(len(data)) {
		t.Fatalf("attachment metadata = %+v", attachments[0])
	}
	uri := asset.AssetURI(attachments[0].AssetURI)
	downloaded, rec, err := svc.Download(ctx, uri)
	if err != nil {
		t.Fatalf("Download() error = %v", err)
	}
	if string(downloaded) != string(data) || rec.Tags["source_kind"] != "chat_attachment" || rec.Tags["session_id"] != "session-1" {
		t.Fatalf("downloaded=%q rec=%+v", downloaded, rec)
	}
}

func TestHandleGetMessagesReturnsAttachmentManifestWithoutBase64(t *testing.T) {
	handler, _, st, cleanup := newTestServerForSessionsWithStore(t)
	defer cleanup()

	sessionID := "attachment-history-session"
	now := time.Now().UTC().Format(time.RFC3339)
	if err := st.SaveSession(context.Background(), &store.SessionRecord{
		ID:             sessionID,
		Name:           "attachments",
		CreatedAt:      now,
		UpdatedAt:      now,
		LastAccessedAt: now,
	}); err != nil {
		t.Fatalf("save session: %v", err)
	}
	metadata := master.AttachmentMetadataForTest([]master.FileAttachment{{
		Filename:    "chart.png",
		MimeType:    "image/png",
		Data:        base64.StdEncoding.EncodeToString([]byte("png-data")),
		Size:        8,
		AssetURI:    "asset://chat/user/local/session/" + sessionID + "/hash.png",
		ContentHash: "hash",
	}})
	if err := st.AddMessage(context.Background(), sessionID, "user", "看图", map[string]any{
		"attachments": metadata["attachments"],
	}); err != nil {
		t.Fatalf("save message: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/"+sessionID+"/messages", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp MessagesListResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp.Messages) != 1 || len(resp.Messages[0].Attachments) != 1 {
		t.Fatalf("messages = %+v, want attachment manifest", resp.Messages)
	}
	got := resp.Messages[0].Attachments[0]
	if got.Data != "" || got.AssetURI == "" || got.Filename != "chart.png" {
		t.Fatalf("attachment = %+v, want manifest without base64 data", got)
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

type testSessionJournal struct {
	called bool
}

func (j *testSessionJournal) StartSession(context.Context, string, string) error { return nil }
func (j *testSessionJournal) LogToolCall(context.Context, journal.ToolCallEntry) error {
	return nil
}
func (j *testSessionJournal) LogFileChange(context.Context, journal.FileChangeEntry) error {
	return nil
}
func (j *testSessionJournal) LogDecision(context.Context, journal.DecisionEntry) error {
	return nil
}
func (j *testSessionJournal) EndSession(context.Context, string, string) error { return nil }
func (j *testSessionJournal) GetJournal(context.Context, string, int) (*journal.SessionJournal, error) {
	return nil, nil
}
func (j *testSessionJournal) DeleteSession(context.Context, string) error { return nil }
func (j *testSessionJournal) GetJournalEvents(context.Context, string, int, time.Time) ([]journal.JournalEvent, error) {
	j.called = true
	return []journal.JournalEvent{{Type: "decision", Decision: "ok", Timestamp: time.Now()}}, nil
}
func (j *testSessionJournal) GetJournalStats(context.Context, []string) (map[string]*journal.JournalStats, error) {
	return nil, nil
}

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

func TestSessionJournalRejectsCrossOwner(t *testing.T) {
	handler, m, cleanup := newTestServerForSessions(t)
	defer cleanup()

	ownerCtx := auth.WithUser(auth.WithAuthEnabled(context.Background()), &auth.User{ID: "owner-1", Role: "user"})
	sessionID, err := m.CreateSession(ownerCtx, "journal-owner", "direct")
	if err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}
	j := &testSessionJournal{}
	m.SetJournal(j)

	req := httptest.NewRequest("GET", "/api/v1/sessions/"+sessionID+"/journal", nil)
	req = req.WithContext(auth.WithUser(auth.WithAuthEnabled(req.Context()), &auth.User{ID: "other-1", Role: "user"}))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body: %s", rec.Code, rec.Body.String())
	}
	if j.called {
		t.Fatal("journal must not be queried before ownership passes")
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
