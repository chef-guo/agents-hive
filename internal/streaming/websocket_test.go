package streaming

import (
	"context"
	"encoding/json"
	"testing"

	"go.uber.org/zap"

	"github.com/chef-guo/agents-hive/internal/auth"
	"github.com/chef-guo/agents-hive/internal/config"
	"github.com/chef-guo/agents-hive/internal/master"
	"github.com/chef-guo/agents-hive/internal/skills"
	"github.com/chef-guo/agents-hive/internal/store"
	"github.com/chef-guo/agents-hive/internal/subagent"
)

// --- WSMessage TESTS ---

func TestWSMessage_Marshal(t *testing.T) {
	payload := map[string]string{"key": "value"}
	payloadBytes, _ := json.Marshal(payload)

	msg := WSMessage{
		Type:    "test-type",
		Payload: payloadBytes,
	}

	data, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("failed to marshal: %v", err)
	}

	var decoded WSMessage
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	if decoded.Type != "test-type" {
		t.Errorf("expected type 'test-type', got %s", decoded.Type)
	}

	var decodedPayload map[string]string
	if err := json.Unmarshal(decoded.Payload, &decodedPayload); err != nil {
		t.Fatalf("failed to unmarshal payload: %v", err)
	}

	if decodedPayload["key"] != "value" {
		t.Errorf("expected key=value, got %v", decodedPayload)
	}
}

func TestWSMessage_EmptyPayload(t *testing.T) {
	msg := WSMessage{
		Type:    "ping",
		Payload: nil,
	}

	data, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("failed to marshal: %v", err)
	}

	var decoded WSMessage
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	if decoded.Type != "ping" {
		t.Errorf("expected type 'ping', got %s", decoded.Type)
	}
}

// --- WSHandler TESTS ---

func TestNewWSHandler(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	skillReg := skills.NewRegistry(logger)
	agentReg := subagent.NewRegistry(logger)

	st := store.NewMemoryStore()

	m := master.NewMaster(
		master.Config{Model: "test"},
		config.HITLConfig{},
		agentReg,
		skillReg,
		st,
		logger,
	)

	handler := NewWSHandler(m, logger)
	if handler == nil {
		t.Fatal("expected non-nil WSHandler")
	}

	if handler.master != m {
		t.Error("expected master to be set")
	}

	if handler.logger != logger {
		t.Error("expected logger to be set")
	}

	if handler.insecureOrigin != false {
		t.Error("expected insecureOrigin to be false by default")
	}
}

func TestNewWSHandlerWithOptions(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	skillReg := skills.NewRegistry(logger)
	agentReg := subagent.NewRegistry(logger)

	st := store.NewMemoryStore()

	m := master.NewMaster(
		master.Config{Model: "test"},
		config.HITLConfig{},
		agentReg,
		skillReg,
		st,
		logger,
	)

	handler := NewWSHandlerWithOptions(m, logger, true)
	if handler == nil {
		t.Fatal("expected non-nil WSHandler")
	}

	if !handler.insecureOrigin {
		t.Error("expected insecureOrigin to be true")
	}
}

func TestNewWSHandlerWithOptions_Secure(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	skillReg := skills.NewRegistry(logger)
	agentReg := subagent.NewRegistry(logger)

	st := store.NewMemoryStore()

	m := master.NewMaster(
		master.Config{Model: "test"},
		config.HITLConfig{},
		agentReg,
		skillReg,
		st,
		logger,
	)

	handler := NewWSHandlerWithOptions(m, logger, false)
	if handler == nil {
		t.Fatal("expected non-nil WSHandler")
	}

	if handler.insecureOrigin {
		t.Error("expected insecureOrigin to be false")
	}
}

// Note: Full WebSocket integration tests require actual WebSocket connections
// and are better suited for end-to-end test suites. These unit tests verify
// the basic structure and initialization of the WebSocket handler.

// --- AUTHENTICATION TESTS ---

func TestWSHandler_TokenAuthentication_Valid(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	skillReg := skills.NewRegistry(logger)
	agentReg := subagent.NewRegistry(logger)
	st := store.NewMemoryStore()
	m := master.NewMaster(
		master.Config{Model: "test"},
		config.HITLConfig{},
		agentReg,
		skillReg,
		st,
		logger,
	)

	handler := NewWSHandler(m, logger)
	handler.SetAuthToken("test-secret-token")

	// 模拟带有正确 token 的请求
	// 注意：完整的 WebSocket 升级测试需要实际的 HTTP 服务器
	// 这里我们只验证 token 配置被正确设置
	if handler.token != "test-secret-token" {
		t.Errorf("expected token to be 'test-secret-token', got %s", handler.token)
	}
}

func TestWSHandler_MaxConnectionsPerIP(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	skillReg := skills.NewRegistry(logger)
	agentReg := subagent.NewRegistry(logger)
	st := store.NewMemoryStore()
	m := master.NewMaster(
		master.Config{Model: "test"},
		config.HITLConfig{},
		agentReg,
		skillReg,
		st,
		logger,
	)

	handler := NewWSHandler(m, logger)

	// 测试默认值
	if handler.maxConnectionsPerIP != 5 {
		t.Errorf("expected default maxConnectionsPerIP to be 5, got %d", handler.maxConnectionsPerIP)
	}

	// 测试设置自定义值
	handler.SetMaxConnectionsPerIP(10)
	if handler.maxConnectionsPerIP != 10 {
		t.Errorf("expected maxConnectionsPerIP to be 10, got %d", handler.maxConnectionsPerIP)
	}

	// 测试设置无效值（应该被忽略）
	handler.SetMaxConnectionsPerIP(0)
	if handler.maxConnectionsPerIP != 10 {
		t.Errorf("expected maxConnectionsPerIP to remain 10, got %d", handler.maxConnectionsPerIP)
	}

	handler.SetMaxConnectionsPerIP(-1)
	if handler.maxConnectionsPerIP != 10 {
		t.Errorf("expected maxConnectionsPerIP to remain 10, got %d", handler.maxConnectionsPerIP)
	}
}

func TestWSHandler_ConnectionCounting(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	skillReg := skills.NewRegistry(logger)
	agentReg := subagent.NewRegistry(logger)
	st := store.NewMemoryStore()
	m := master.NewMaster(
		master.Config{Model: "test"},
		config.HITLConfig{},
		agentReg,
		skillReg,
		st,
		logger,
	)

	handler := NewWSHandler(m, logger)

	// 验证初始状态
	if handler.ipConnections == nil {
		t.Fatal("expected ipConnections map to be initialized")
	}

	if len(handler.ipConnections) != 0 {
		t.Errorf("expected ipConnections to be empty, got %d entries", len(handler.ipConnections))
	}
}

func TestWSHandler_AuthorizeSessionSubscription(t *testing.T) {
	logger := zap.NewNop()
	skillReg := skills.NewRegistry(logger)
	agentReg := subagent.NewRegistry(logger)
	st := store.NewMemoryStore()
	if err := st.SaveSession(context.Background(), &store.SessionRecord{
		ID:     "sess-owner",
		Name:   "owner session",
		UserID: "user-owner",
	}); err != nil {
		t.Fatalf("save session: %v", err)
	}
	m := master.NewMaster(
		master.Config{Model: "test"},
		config.HITLConfig{},
		agentReg,
		skillReg,
		st,
		logger,
	)
	handler := NewWSHandler(m, logger)

	owner := &auth.User{ID: "user-owner", Status: "active"}
	ownerCtx := auth.WithUser(context.Background(), owner)
	if err := handler.authorizeSessionSubscription(ownerCtx, "sess-owner", owner.ID); err != nil {
		t.Fatalf("owner subscription should be allowed: %v", err)
	}

	other := &auth.User{ID: "user-other", Status: "active"}
	otherCtx := auth.WithUser(context.Background(), other)
	if err := handler.authorizeSessionSubscription(otherCtx, "sess-owner", other.ID); err == nil {
		t.Fatal("cross-user subscription should be rejected")
	}

	if err := handler.authorizeSessionSubscription(context.Background(), "sess-owner", owner.ID); err == nil {
		t.Fatal("missing authenticated user should be rejected")
	}
}
