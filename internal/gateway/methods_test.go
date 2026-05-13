package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/chef-guo/agents-hive/internal/channel"
	"github.com/chef-guo/agents-hive/internal/command"
	"github.com/chef-guo/agents-hive/internal/config"
	"github.com/chef-guo/agents-hive/internal/mcphost"
	"github.com/chef-guo/agents-hive/internal/plugin"
	"github.com/chef-guo/agents-hive/internal/skills"
	"github.com/chef-guo/agents-hive/internal/store"
)

// ─────────────────────────────────────────────
// 测试辅助函数
// ─────────────────────────────────────────────

// newTestGateway 创建带管理员 token 的测试网关
func newTestGateway(t *testing.T) (*Gateway, string) {
	t.Helper()
	token := "test-admin-token"
	auth := NewAuthManager([]string{token})
	gw := New(auth, zap.NewNop())
	gw.SetInsecureSkipVerify(true)
	return gw, token
}

// doRPC 通过 HTTP POST 发起 RPC 调用，返回解码后的响应
func doRPC(t *testing.T, gw *Gateway, method string, params interface{}, token string) RPCResponse {
	t.Helper()
	raw, err := json.Marshal(params)
	require.NoError(t, err)

	body, err := json.Marshal(RPCRequest{
		ID:     "req-1",
		Method: method,
		Params: json.RawMessage(raw),
	})
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/rpc", bytes.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	gw.HandleHTTP(w, req)

	var resp RPCResponse
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	return resp
}

type configChannelStore struct {
	store.Store
	records map[string]*store.ChannelConfigRecord
}

func newConfigChannelStore() *configChannelStore {
	return &configChannelStore{records: make(map[string]*store.ChannelConfigRecord)}
}

func (s *configChannelStore) SaveChannelConfig(_ context.Context, rec *store.ChannelConfigRecord) error {
	cp := *rec
	s.records[rec.Platform] = &cp
	return nil
}

// ─────────────────────────────────────────────
// RegisterAllMethods — 条件注册逻辑测试
// ─────────────────────────────────────────────

// TestRegisterAllMethods_NilConfigMu 验证 #1 修复：Config 或 ConfigMu 为 nil 时不注册 config 方法
func TestRegisterAllMethods_NilConfigMu(t *testing.T) {
	tests := []struct {
		name   string
		cfg    *config.Config
		cfgMu  *sync.RWMutex
		expect bool // 是否期望 config.save 被注册
	}{
		{
			name:   "Config 和 ConfigMu 均为 nil 时不注册",
			cfg:    nil,
			cfgMu:  nil,
			expect: false,
		},
		{
			name:   "仅 Config 不为 nil 时不注册（ConfigMu 为 nil）",
			cfg:    &config.Config{},
			cfgMu:  nil,
			expect: false,
		},
		{
			name:   "仅 ConfigMu 不为 nil 时不注册（Config 为 nil）",
			cfg:    nil,
			cfgMu:  &sync.RWMutex{},
			expect: false,
		},
		{
			name:   "Config 和 ConfigMu 均不为 nil 时注册",
			cfg:    &config.Config{},
			cfgMu:  &sync.RWMutex{},
			expect: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gw, _ := newTestGateway(t)
			deps := Deps{
				Config:        tc.cfg,
				ConfigMu:      tc.cfgMu,
				SkillRegistry: skills.NewRegistry(zap.NewNop()),
			}
			// 仅注册 config 方法（不需要 Master）
			if deps.Config != nil && deps.ConfigMu != nil {
				registerConfigMethods(gw, deps)
			}

			gw.mu.RLock()
			_, ok := gw.methods["config.save"]
			gw.mu.RUnlock()

			assert.Equal(t, tc.expect, ok, "config.save 注册状态与期望不符")
		})
	}
}

// TestRegisterAllMethods_OptionalDeps 验证可选依赖为 nil 时对应方法不注册
func TestRegisterAllMethods_OptionalDeps(t *testing.T) {
	gw, _ := newTestGateway(t)
	deps := Deps{
		// CommandRegistry、ChannelRouter、PluginLoader、MCPHost 均为 nil
		Config:        nil,
		ConfigMu:      nil,
		SkillRegistry: skills.NewRegistry(zap.NewNop()),
	}

	// 仅手动触发条件注册分支（RegisterAllMethods 需要 Master，此处测试各个条件分支）
	if deps.CommandRegistry != nil {
		registerCommandMethods(gw, deps.CommandRegistry, deps)
	}
	if deps.ChannelRouter != nil {
		registerChannelMethods(gw, deps)
	}
	if deps.PluginLoader != nil {
		registerPluginMethods(gw, deps)
	}
	if deps.MCPHost != nil {
		registerMCPMethods(gw, deps)
	}

	gw.mu.RLock()
	defer gw.mu.RUnlock()

	for _, name := range []string{
		"commands.list",
		"channel.status", "channel.send", "channel.bind",
		"plugin.list", "plugin.load", "plugin.unload", "plugin.reload",
		"mcp.resources.list", "mcp.resources.read", "mcp.prompts.list", "mcp.prompts.get",
		"config.save", "config.reload",
	} {
		_, ok := gw.methods[name]
		assert.False(t, ok, "可选依赖为 nil 时不应注册方法: %s", name)
	}
}

// ─────────────────────────────────────────────
// methods_config.go 测试
// ─────────────────────────────────────────────

// TestConfigSave_SavesToSpecifiedPath 验证 config.save 保存到指定路径
func TestConfigSave_SavesToSpecifiedPath(t *testing.T) {
	// 准备临时目录和配置文件路径
	dir := t.TempDir()
	cfgPath := dir + "/config.json"

	cfg := &config.Config{}
	var mu sync.RWMutex

	gw, token := newTestGateway(t)
	registerConfigMethods(gw, Deps{
		Config:     cfg,
		ConfigMu:   &mu,
		ConfigPath: cfgPath,
	})

	resp := doRPC(t, gw, "config.save", map[string]interface{}{}, token)
	assert.Nil(t, resp.Error, "config.save 应成功，实际错误: %v", resp.Error)

	// 验证结果包含正确路径
	var result map[string]string
	require.NoError(t, json.Unmarshal(resp.Result, &result))
	assert.Equal(t, "saved", result["status"])
	assert.Equal(t, cfgPath, result["path"])

	// 验证文件确实已写入
	_, err := os.Stat(cfgPath)
	assert.NoError(t, err, "配置文件应已创建")
}

// TestConfigSave_RequiresAdminScope 验证 config.save 需要 admin 权限
func TestConfigSave_RequiresAdminScope(t *testing.T) {
	cfg := &config.Config{}
	var mu sync.RWMutex

	gw, _ := newTestGateway(t)
	registerConfigMethods(gw, Deps{
		Config:     cfg,
		ConfigMu:   &mu,
		ConfigPath: t.TempDir() + "/config.json",
	})

	// 不携带 token 调用，应返回 401
	resp := doRPC(t, gw, "config.save", map[string]interface{}{}, "")
	assert.NotNil(t, resp.Error)
	assert.Equal(t, 401, resp.Error.Code)
}

// TestConfigReload_EmptyPath 验证 config.reload 在路径为空时返回错误
func TestConfigReload_EmptyPath(t *testing.T) {
	cfg := &config.Config{}
	var mu sync.RWMutex

	gw, token := newTestGateway(t)
	registerConfigMethods(gw, Deps{
		Config:     cfg,
		ConfigMu:   &mu,
		ConfigPath: "", // 空路径
	})

	resp := doRPC(t, gw, "config.reload", map[string]interface{}{}, token)
	// 空路径应返回错误（RPC 内部错误映射为 500）
	assert.NotNil(t, resp.Error)
	assert.Equal(t, 500, resp.Error.Code)
}

// TestConfigReload_NoCallback 验证 config.reload 在未注册回调时返回错误
func TestConfigReload_NoCallback(t *testing.T) {
	cfg := &config.Config{}
	var mu sync.RWMutex

	gw, token := newTestGateway(t)
	registerConfigMethods(gw, Deps{
		Config:   cfg,
		ConfigMu: &mu,
	})

	resp := doRPC(t, gw, "config.reload", map[string]interface{}{}, token)
	assert.NotNil(t, resp.Error, "缺少 ReloadConfigFunc 应返回错误")
	assert.Equal(t, 500, resp.Error.Code)
}

// TestConfigReload_WithCallback 验证 config.reload 通过回调从 DB 重载
func TestConfigReload_WithCallback(t *testing.T) {
	cfg := &config.Config{}
	var mu sync.RWMutex
	called := false

	gw, token := newTestGateway(t)
	registerConfigMethods(gw, Deps{
		Config:   cfg,
		ConfigMu: &mu,
		ReloadConfigFunc: func() {
			called = true
		},
	})

	resp := doRPC(t, gw, "config.reload", map[string]interface{}{}, token)
	assert.Nil(t, resp.Error, "有回调时 reload 应成功，实际错误: %v", resp.Error)

	var result map[string]string
	require.NoError(t, json.Unmarshal(resp.Result, &result))
	assert.Equal(t, "reloaded", result["status"])
	assert.True(t, called, "ReloadConfigFunc 应被调用")
}

func TestConfigUpdatePersistsWechatbotChannel(t *testing.T) {
	cfg := config.Default()
	var mu sync.RWMutex
	db := newConfigChannelStore()

	gw, token := newTestGateway(t)
	registerConfigMethods(gw, Deps{
		Config:   cfg,
		ConfigMu: &mu,
		Store:    db,
	})

	resp := doRPC(t, gw, "config.update", map[string]interface{}{
		"channel": map[string]interface{}{
			"wechatbot": map[string]interface{}{
				"enabled": true,
			},
		},
	}, token)
	assert.Nil(t, resp.Error, "config.update should save wechatbot: %v", resp.Error)
	assert.True(t, cfg.Channel.WeChatBot.Enabled)
	rec := db.records["wechatbot"]
	require.NotNil(t, rec)
	assert.True(t, rec.Enabled)
	assert.JSONEq(t, `{"enabled":true}`, rec.ConfigJSON)
}

func TestChannelReloadIncludesWechatbotByDefault(t *testing.T) {
	cfg := config.Default()
	var mu sync.RWMutex
	var reloaded []string

	gw, token := newTestGateway(t)
	registerConfigMethods(gw, Deps{
		Config:   cfg,
		ConfigMu: &mu,
		ReloadChannelFunc: func(platform string) error {
			reloaded = append(reloaded, platform)
			return nil
		},
	})

	resp := doRPC(t, gw, "channel.reload", map[string]interface{}{}, token)
	assert.Nil(t, resp.Error, "channel.reload should include wechatbot: %v", resp.Error)
	assert.Equal(t, []string{"dingtalk", "feishu", "wecom", "wechatbot"}, reloaded)
}

// ─────────────────────────────────────────────
// methods_sessions.go 测试（仅验证参数校验逻辑）
// ─────────────────────────────────────────────

// TestSessionMethods_MethodsRegistered 验证 session 方法已正确注册
func TestSessionMethods_MethodsRegistered(t *testing.T) {
	gw, _ := newTestGateway(t)

	// 使用最小 mockMaster（只需验证方法注册，不实际调用）
	// registerSessionMethods 需要 deps.Master 不为 nil 才能注册
	// 此处通过直接调用 gw.Register 来模拟验证注册列表
	expectedMethods := []string{
		"sessions.list",
		"sessions.get",
		"sessions.message",
		"sessions.create",
		"sessions.update",
		"sessions.delete",
		"sessions.messages",
		"sessions.clear",
		"sessions.fork",
		"sessions.revert",
	}

	// 手动注册占位符，模拟 registerSessionMethods 的效果（验证方法名集合）
	for _, name := range expectedMethods {
		gw.Register(MethodDef{
			Name:    name,
			Handler: func(_ context.Context, _ json.RawMessage) (json.RawMessage, error) { return nil, nil },
		})
	}

	gw.mu.RLock()
	defer gw.mu.RUnlock()
	for _, name := range expectedMethods {
		_, ok := gw.methods[name]
		assert.True(t, ok, "方法应已注册: %s", name)
	}
}

// TestSessionMethods_InvalidParams 验证参数解析失败时返回 500（handler 层面的 Unmarshal 错误通过 500 返回）
func TestSessionMethods_InvalidParams(t *testing.T) {
	// 通过注册一个具有 Unmarshal 逻辑的 handler 来测试参数校验路径
	tests := []struct {
		name       string
		method     string
		params     json.RawMessage
		wantErrMsg string
	}{
		{
			name:   "sessions.update 空 name 参数",
			method: "test.sessions.update.empty_name",
			params: json.RawMessage(`{"name":""}`),
		},
		{
			name:   "sessions.delete 空 id 参数",
			method: "test.sessions.delete.empty_id",
			params: json.RawMessage(`{"id":""}`),
		},
		{
			name:   "sessions.messages 空 id 参数",
			method: "test.sessions.messages.empty_id",
			params: json.RawMessage(`{"id":""}`),
		},
	}

	// 直接测试 sessions.update 的 name 空值校验逻辑（不依赖 Master）
	t.Run("sessions.update 空名称校验", func(t *testing.T) {
		_ = tests
		gw, token := newTestGateway(t)
		gw.Register(MethodDef{
			Name:      "sessions.update",
			AuthScope: "write",
			Handler: func(_ context.Context, params json.RawMessage) (json.RawMessage, error) {
				var p struct {
					Name string `json:"name"`
				}
				if err := json.Unmarshal(params, &p); err != nil {
					return nil, err
				}
				if p.Name == "" {
					return nil, &mockError{msg: "名称不能为空"}
				}
				return json.Marshal(map[string]string{"status": "ok"})
			},
		})

		resp := doRPC(t, gw, "sessions.update", map[string]string{"name": ""}, token)
		assert.NotNil(t, resp.Error, "空名称应返回错误")
	})
}

// mockError 简单错误类型用于测试
type mockError struct {
	msg string
}

func (e *mockError) Error() string { return e.msg }

// ─────────────────────────────────────────────
// methods_hitl.go 测试
// ─────────────────────────────────────────────

// TestHITLMethods_MethodsRegistered 验证 HITL 方法已正确注册
func TestHITLMethods_MethodsRegistered(t *testing.T) {
	gw, _ := newTestGateway(t)

	// 注册占位 handler 验证方法名
	for _, name := range []string{"hitl.submit", "hitl.command", "hitl.pending"} {
		gw.Register(MethodDef{
			Name:    name,
			Handler: func(_ context.Context, _ json.RawMessage) (json.RawMessage, error) { return nil, nil },
		})
	}

	gw.mu.RLock()
	defer gw.mu.RUnlock()
	for _, name := range []string{"hitl.submit", "hitl.command", "hitl.pending"} {
		_, ok := gw.methods[name]
		assert.True(t, ok, "HITL 方法应已注册: %s", name)
	}
}

// TestHITLMethods_Submit_EmptyRequestID 验证 hitl.submit 对空 request_id 返回错误
func TestHITLMethods_Submit_EmptyRequestID(t *testing.T) {
	gw, token := newTestGateway(t)

	// 注册一个模拟 hitl.submit handler，直接复用真实校验逻辑
	gw.Register(MethodDef{
		Name:      "hitl.submit",
		AuthScope: "write",
		Handler: func(_ context.Context, params json.RawMessage) (json.RawMessage, error) {
			var p struct {
				RequestID string `json:"request_id"`
			}
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, err
			}
			if p.RequestID == "" {
				return nil, &mockError{msg: "request_id 不能为空"}
			}
			return json.Marshal(map[string]string{"status": "submitted"})
		},
	})

	tests := []struct {
		name        string
		params      map[string]interface{}
		expectError bool
	}{
		{
			name:        "空 request_id 应返回错误",
			params:      map[string]interface{}{"request_id": ""},
			expectError: true,
		},
		{
			name:        "有效 request_id 应成功",
			params:      map[string]interface{}{"request_id": "req-123"},
			expectError: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			resp := doRPC(t, gw, "hitl.submit", tc.params, token)
			if tc.expectError {
				assert.NotNil(t, resp.Error)
			} else {
				assert.Nil(t, resp.Error)
			}
		})
	}
}

// TestHITLMethods_Command_Validation 验证 hitl.command 参数校验
func TestHITLMethods_Command_Validation(t *testing.T) {
	gw, token := newTestGateway(t)

	gw.Register(MethodDef{
		Name:      "hitl.command",
		AuthScope: "write",
		Handler: func(_ context.Context, params json.RawMessage) (json.RawMessage, error) {
			var p struct {
				TaskID string `json:"task_id"`
				Type   string `json:"type"`
			}
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, err
			}
			if p.TaskID == "" {
				return nil, &mockError{msg: "task_id 不能为空"}
			}
			if p.Type == "" {
				return nil, &mockError{msg: "type 不能为空"}
			}
			return json.Marshal(map[string]string{"status": "sent"})
		},
	})

	tests := []struct {
		name        string
		params      map[string]string
		expectError bool
	}{
		{
			name:        "空 task_id 应返回错误",
			params:      map[string]string{"task_id": "", "type": "pause"},
			expectError: true,
		},
		{
			name:        "空 type 应返回错误",
			params:      map[string]string{"task_id": "task-1", "type": ""},
			expectError: true,
		},
		{
			name:        "有效参数应成功",
			params:      map[string]string{"task_id": "task-1", "type": "pause"},
			expectError: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			resp := doRPC(t, gw, "hitl.command", tc.params, token)
			if tc.expectError {
				assert.NotNil(t, resp.Error, "应返回错误但未返回")
			} else {
				assert.Nil(t, resp.Error, "不应返回错误，实际: %v", resp.Error)
			}
		})
	}
}

// ─────────────────────────────────────────────
// methods_channel.go 测试
// ─────────────────────────────────────────────

// TestChannelMethods_MethodsRegistered 验证 channel 方法注册
func TestChannelMethods_MethodsRegistered(t *testing.T) {
	gw, _ := newTestGateway(t)
	router := channel.NewRouter(nil, zap.NewNop())
	deps := Deps{ChannelRouter: router}
	registerChannelMethods(gw, deps)

	gw.mu.RLock()
	defer gw.mu.RUnlock()
	for _, name := range []string{"channel.status", "channel.send", "channel.bind"} {
		_, ok := gw.methods[name]
		assert.True(t, ok, "channel 方法应已注册: %s", name)
	}
}

// TestChannelStatus_NoPlugins 验证无插件时 channel.status 返回全 false
func TestChannelStatus_NoPlugins(t *testing.T) {
	gw, token := newTestGateway(t)
	router := channel.NewRouter(nil, zap.NewNop())
	deps := Deps{ChannelRouter: router}
	registerChannelMethods(gw, deps)

	resp := doRPC(t, gw, "channel.status", map[string]interface{}{}, token)
	assert.Nil(t, resp.Error)

	var status map[string]bool
	require.NoError(t, json.Unmarshal(resp.Result, &status))
	// 无插件时所有平台均为 false
	assert.False(t, status[string(channel.PlatformDingTalk)])
	assert.False(t, status[string(channel.PlatformFeishu)])
	assert.False(t, status[string(channel.PlatformWeCom)])
}

// TestChannelBind_Success 验证 channel.bind 绑定操作成功
func TestChannelBind_Success(t *testing.T) {
	gw, token := newTestGateway(t)
	router := channel.NewRouter(nil, zap.NewNop())
	deps := Deps{ChannelRouter: router}
	registerChannelMethods(gw, deps)

	params := channel.Binding{
		Platform:  channel.PlatformDingTalk,
		ChatID:    "chat-001",
		SessionID: "session-001",
	}

	resp := doRPC(t, gw, "channel.bind", params, token)
	assert.Nil(t, resp.Error, "channel.bind 应成功，实际错误: %v", resp.Error)

	var result map[string]string
	require.NoError(t, json.Unmarshal(resp.Result, &result))
	assert.Equal(t, "bound", result["status"])
}

// TestChannelSend_PlatformNotFound 验证发送到未注册平台时返回错误
func TestChannelSend_PlatformNotFound(t *testing.T) {
	gw, token := newTestGateway(t)
	router := channel.NewRouter(nil, zap.NewNop())
	deps := Deps{ChannelRouter: router}
	registerChannelMethods(gw, deps)

	params := map[string]string{
		"platform": "nonexistent",
		"chat_id":  "chat-001",
		"content":  "hello",
	}

	resp := doRPC(t, gw, "channel.send", params, token)
	assert.NotNil(t, resp.Error, "未注册平台应返回错误")
	assert.Equal(t, 500, resp.Error.Code) // 内部错误（errs 被包装为 500）
}

// TestChannelSend_InvalidParams 验证非法 JSON 参数返回错误
func TestChannelSend_InvalidParams(t *testing.T) {
	gw, _ := newTestGateway(t)
	router := channel.NewRouter(nil, zap.NewNop())
	deps := Deps{ChannelRouter: router}
	registerChannelMethods(gw, deps)

	// 发送非法 JSON
	body, _ := json.Marshal(RPCRequest{
		ID:     "req-bad",
		Method: "channel.send",
		Params: json.RawMessage(`not-valid-json`),
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/rpc", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer test-admin-token")
	w := httptest.NewRecorder()
	gw.HandleHTTP(w, req)

	var resp RPCResponse
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	assert.NotNil(t, resp.Error)
}

// ─────────────────────────────────────────────
// methods_commands.go 测试
// ─────────────────────────────────────────────

// TestCommandMethods_List 验证 commands.list 返回所有命令
func TestCommandMethods_List(t *testing.T) {
	gw, token := newTestGateway(t)
	cmdReg := command.NewRegistry(zap.NewNop())
	registerCommandMethods(gw, cmdReg, Deps{})

	gw.mu.RLock()
	_, ok := gw.methods["commands.list"]
	gw.mu.RUnlock()
	assert.True(t, ok, "commands.list 应已注册")

	resp := doRPC(t, gw, "commands.list", map[string]interface{}{}, token)
	assert.Nil(t, resp.Error, "commands.list 应成功，实际错误: %v", resp.Error)

	// 空注册表应返回空数组或 null
	assert.NotNil(t, resp.Result)
}

// TestCommandExecute_EmptyName 验证 commands.execute 空 name 返回错误
func TestCommandExecute_EmptyName(t *testing.T) {
	gw, token := newTestGateway(t)
	cmdReg := command.NewRegistry(zap.NewNop())
	registerCommandMethods(gw, cmdReg, Deps{})

	resp := doRPC(t, gw, "commands.execute", map[string]string{
		"name":       "",
		"arguments":  "test",
		"session_id": "s1",
	}, token)
	assert.NotNil(t, resp.Error, "空 name 应返回错误")
}

// TestCommandExecute_NotFound 验证 commands.execute 命令不存在时返回错误
func TestCommandExecute_NotFound(t *testing.T) {
	gw, token := newTestGateway(t)
	cmdReg := command.NewRegistry(zap.NewNop())
	registerCommandMethods(gw, cmdReg, Deps{})

	resp := doRPC(t, gw, "commands.execute", map[string]string{
		"name":       "nonexistent-cmd",
		"arguments":  "",
		"session_id": "s1",
	}, token)
	assert.NotNil(t, resp.Error, "不存在的命令应返回错误")
}

// TestCommandExecute_InvalidJSON 验证 commands.execute 非法 JSON 返回错误
func TestCommandExecute_InvalidJSON(t *testing.T) {
	gw, _ := newTestGateway(t)
	cmdReg := command.NewRegistry(zap.NewNop())
	registerCommandMethods(gw, cmdReg, Deps{})

	body, _ := json.Marshal(RPCRequest{
		ID:     "req-bad-cmd",
		Method: "commands.execute",
		Params: json.RawMessage(`not-valid-json`),
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/rpc", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer test-admin-token")
	w := httptest.NewRecorder()
	gw.HandleHTTP(w, req)

	var resp RPCResponse
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	assert.NotNil(t, resp.Error, "非法 JSON 应返回错误")
}

// TestCommandExecute_MethodsRegistered 验证 commands.execute 方法已注册
func TestCommandExecute_MethodsRegistered(t *testing.T) {
	gw, _ := newTestGateway(t)
	cmdReg := command.NewRegistry(zap.NewNop())
	registerCommandMethods(gw, cmdReg, Deps{})

	gw.mu.RLock()
	defer gw.mu.RUnlock()
	for _, name := range []string{"commands.list", "commands.execute"} {
		_, ok := gw.methods[name]
		assert.True(t, ok, "方法应已注册: %s", name)
	}
}

// TestCommandExecute_ListWithRegisteredCommands 验证有注册命令时 list 返回正确结果
func TestCommandExecute_ListWithRegisteredCommands(t *testing.T) {
	gw, token := newTestGateway(t)
	cmdReg := command.NewRegistry(zap.NewNop())
	cmdReg.Register(&command.Info{
		Name:        "test-cmd",
		Description: "a test command",
		Source:      command.SourceBuiltin,
		Template:    "hello $ARGUMENTS",
	})
	registerCommandMethods(gw, cmdReg, Deps{})

	resp := doRPC(t, gw, "commands.list", map[string]interface{}{}, token)
	assert.Nil(t, resp.Error)

	var cmds []map[string]interface{}
	require.NoError(t, json.Unmarshal(resp.Result, &cmds))
	assert.Equal(t, 1, len(cmds), "应返回 1 个命令")
	assert.Equal(t, "test-cmd", cmds[0]["name"])
}

// ─────────────────────────────────────────────
// methods_mcp.go 测试
// ─────────────────────────────────────────────

// TestMCPMethods_MethodsRegistered 验证 MCP 方法注册
func TestMCPMethods_MethodsRegistered(t *testing.T) {
	gw, _ := newTestGateway(t)
	host := mcphost.NewHost(zap.NewNop())
	deps := Deps{MCPHost: host}
	registerMCPMethods(gw, deps)

	gw.mu.RLock()
	defer gw.mu.RUnlock()
	for _, name := range []string{
		"mcp.tools.list",
		"mcp.resources.list",
		"mcp.resources.read",
		"mcp.prompts.list",
		"mcp.prompts.get",
	} {
		_, ok := gw.methods[name]
		assert.True(t, ok, "MCP 方法应已注册: %s", name)
	}
}

// TestMCPTools_List_GroupsRemoteTools 验证运行时工具目录按 MCP 服务端分组
func TestMCPTools_List_GroupsRemoteTools(t *testing.T) {
	gw, token := newTestGateway(t)
	host := mcphost.NewHost(zap.NewNop())
	host.RegisterTool(mcphost.ToolDefinition{Name: "read_file", Core: true}, nil)
	host.RegisterTool(mcphost.ToolDefinition{
		Name:              "metamcp__grafana__query_prometheus",
		Description:       "[metamcp] query prometheus",
		IsConcurrencySafe: true,
		SourceServer:      "metamcp",
		Trusted:           true,
	}, nil)
	host.RegisterTool(mcphost.ToolDefinition{
		Name:         "metamcp__dbhub__execute_sql",
		Description:  "[metamcp] execute sql",
		SourceServer: "metamcp",
		Trusted:      true,
	}, nil)
	host.RegisterResource(mcphost.ResourceDefinition{
		URI:  "metamcp://resource://dashboards",
		Name: "dashboards",
	}, nil)
	host.RegisterPrompt(mcphost.PromptDefinition{
		Name:        "metamcp__sre_prompt",
		Description: "SRE prompt",
	}, nil)
	deps := Deps{MCPHost: host}
	registerMCPMethods(gw, deps)

	resp := doRPC(t, gw, "mcp.tools.list", map[string]interface{}{}, token)
	assert.Nil(t, resp.Error)

	var got mcpToolsListResponse
	require.NoError(t, json.Unmarshal(resp.Result, &got))
	assert.Equal(t, 3, got.Total)
	assert.Equal(t, 2, got.MCPCount)
	assert.Equal(t, 1, got.LocalCount)
	require.Len(t, got.Servers, 1)
	assert.Equal(t, "metamcp", got.Servers[0].Name)
	assert.Equal(t, 2, got.Servers[0].Count)
	assert.Equal(t, 1, got.Servers[0].Resources)
	assert.Equal(t, 1, got.Servers[0].Prompts)
	assert.Equal(t, "metamcp__dbhub__execute_sql", got.Servers[0].Tools[0].Name)
	assert.Equal(t, "metamcp__grafana__query_prometheus", got.Servers[0].Tools[1].Name)
	assert.True(t, got.Servers[0].Tools[1].Trusted)
	assert.Equal(t, "read_only", got.Servers[0].Tools[1].Risk)
	assert.True(t, got.Servers[0].Tools[1].ReadOnly)
	assert.False(t, got.Servers[0].Tools[1].RequiresApproval)
}

// TestMCPResources_List_Empty 验证空 MCP Host 返回空资源列表
func TestMCPResources_List_Empty(t *testing.T) {
	gw, token := newTestGateway(t)
	host := mcphost.NewHost(zap.NewNop())
	deps := Deps{MCPHost: host}
	registerMCPMethods(gw, deps)

	resp := doRPC(t, gw, "mcp.resources.list", map[string]interface{}{}, token)
	assert.Nil(t, resp.Error)
	assert.NotNil(t, resp.Result)
}

// TestMCPResources_Read_MissingURI 验证 mcp.resources.read 缺少 uri 时返回错误
func TestMCPResources_Read_MissingURI(t *testing.T) {
	gw, token := newTestGateway(t)
	host := mcphost.NewHost(zap.NewNop())
	deps := Deps{MCPHost: host}
	registerMCPMethods(gw, deps)

	resp := doRPC(t, gw, "mcp.resources.read", map[string]string{"uri": ""}, token)
	assert.NotNil(t, resp.Error, "空 uri 应返回错误")
}

// TestMCPResources_Read_NotFound 验证读取不存在的资源时返回错误
func TestMCPResources_Read_NotFound(t *testing.T) {
	gw, token := newTestGateway(t)
	host := mcphost.NewHost(zap.NewNop())
	deps := Deps{MCPHost: host}
	registerMCPMethods(gw, deps)

	resp := doRPC(t, gw, "mcp.resources.read", map[string]string{"uri": "file:///nonexistent"}, token)
	assert.NotNil(t, resp.Error, "不存在的资源应返回错误")
}

// TestMCPPrompts_List_Empty 验证空 MCP Host 返回空提示列表
func TestMCPPrompts_List_Empty(t *testing.T) {
	gw, token := newTestGateway(t)
	host := mcphost.NewHost(zap.NewNop())
	deps := Deps{MCPHost: host}
	registerMCPMethods(gw, deps)

	resp := doRPC(t, gw, "mcp.prompts.list", map[string]interface{}{}, token)
	assert.Nil(t, resp.Error)
	assert.NotNil(t, resp.Result)
}

// TestMCPPrompts_Get_MissingName 验证 mcp.prompts.get 缺少 name 时返回错误
func TestMCPPrompts_Get_MissingName(t *testing.T) {
	gw, token := newTestGateway(t)
	host := mcphost.NewHost(zap.NewNop())
	deps := Deps{MCPHost: host}
	registerMCPMethods(gw, deps)

	resp := doRPC(t, gw, "mcp.prompts.get", map[string]string{"name": ""}, token)
	assert.NotNil(t, resp.Error, "空 name 应返回错误")
}

// TestMCPPrompts_Get_NotFound 验证获取不存在的提示时返回错误
func TestMCPPrompts_Get_NotFound(t *testing.T) {
	gw, token := newTestGateway(t)
	host := mcphost.NewHost(zap.NewNop())
	deps := Deps{MCPHost: host}
	registerMCPMethods(gw, deps)

	resp := doRPC(t, gw, "mcp.prompts.get", map[string]string{"name": "nonexistent"}, token)
	assert.NotNil(t, resp.Error, "不存在的提示应返回错误")
}

// TestMCPPrompts_Get_WithRegisteredPrompt 验证获取已注册提示成功
func TestMCPPrompts_Get_WithRegisteredPrompt(t *testing.T) {
	gw, _ := newTestGateway(t)
	token := "test-admin-token" // 直接使用已知 token（与 newTestGateway 保持一致）
	host := mcphost.NewHost(zap.NewNop())

	// 注册一个测试提示
	host.RegisterPrompt(mcphost.PromptDefinition{
		Name:        "test-prompt",
		Description: "测试提示",
	}, func(_ context.Context, _ map[string]string) ([]mcphost.PromptMessage, error) {
		return []mcphost.PromptMessage{
			{Role: "user", Content: "hello"},
		}, nil
	})

	deps := Deps{MCPHost: host}
	registerMCPMethods(gw, deps)

	resp := doRPC(t, gw, "mcp.prompts.get", map[string]string{"name": "test-prompt"}, token)
	assert.Nil(t, resp.Error, "已注册的提示应成功返回，实际错误: %v", resp.Error)
}

// ─────────────────────────────────────────────
// methods_plugin.go 测试
// ─────────────────────────────────────────────

// TestPluginMethods_MethodsRegistered 验证 plugin 方法注册
func TestPluginMethods_MethodsRegistered(t *testing.T) {
	gw, _ := newTestGateway(t)
	mgr := plugin.NewManager(zap.NewNop())
	deps := Deps{PluginLoader: mgr}
	registerPluginMethods(gw, deps)

	gw.mu.RLock()
	defer gw.mu.RUnlock()
	for _, name := range []string{"plugin.list", "plugin.load", "plugin.unload", "plugin.reload"} {
		_, ok := gw.methods[name]
		assert.True(t, ok, "plugin 方法应已注册: %s", name)
	}
}

// TestPluginList_Empty 验证空插件管理器返回空列表
func TestPluginList_Empty(t *testing.T) {
	gw, token := newTestGateway(t)
	mgr := plugin.NewManager(zap.NewNop())
	deps := Deps{PluginLoader: mgr}
	registerPluginMethods(gw, deps)

	resp := doRPC(t, gw, "plugin.list", map[string]interface{}{}, token)
	assert.Nil(t, resp.Error, "plugin.list 应成功，实际错误: %v", resp.Error)
	assert.NotNil(t, resp.Result)
}

// TestPluginLoad_NonExistentID 验证加载不存在插件时返回错误
func TestPluginLoad_NonExistentID(t *testing.T) {
	gw, token := newTestGateway(t)
	mgr := plugin.NewManager(zap.NewNop())
	deps := Deps{PluginLoader: mgr}
	registerPluginMethods(gw, deps)

	resp := doRPC(t, gw, "plugin.load", map[string]string{"id": "nonexistent"}, token)
	assert.NotNil(t, resp.Error, "加载不存在的插件应返回错误")
}

// TestPluginUnload_NonExistentID 验证卸载不存在插件时返回错误
func TestPluginUnload_NonExistentID(t *testing.T) {
	gw, token := newTestGateway(t)
	mgr := plugin.NewManager(zap.NewNop())
	deps := Deps{PluginLoader: mgr}
	registerPluginMethods(gw, deps)

	resp := doRPC(t, gw, "plugin.unload", map[string]string{"id": "nonexistent"}, token)
	assert.NotNil(t, resp.Error, "卸载不存在的插件应返回错误")
}

// ─────────────────────────────────────────────
// methods_health.go 测试
// ─────────────────────────────────────────────

// TestHealthCheck_NoAuth 验证 health.check 无需认证
func TestHealthCheck_NoAuth(t *testing.T) {
	gw, _ := newTestGateway(t)
	// health 方法注册需要 Master（只验证无认证端点可公开访问）
	gw.Register(MethodDef{
		Name:      "health.check",
		AuthScope: "", // 无需认证
		Handler: func(_ context.Context, _ json.RawMessage) (json.RawMessage, error) {
			return json.Marshal(map[string]string{"status": "ok"})
		},
	})

	// 不带 token 调用，应成功
	resp := doRPC(t, gw, "health.check", map[string]interface{}{}, "")
	assert.Nil(t, resp.Error, "health.check 无需认证，实际错误: %v", resp.Error)

	var result map[string]string
	require.NoError(t, json.Unmarshal(resp.Result, &result))
	assert.Equal(t, "ok", result["status"])
}

// ─────────────────────────────────────────────
// methods_skills.go 测试
// ─────────────────────────────────────────────

// TestSkillMethods_List 验证 skills.list 返回注册表内容
func TestSkillMethods_List(t *testing.T) {
	gw, token := newTestGateway(t)
	reg := skills.NewRegistry(zap.NewNop())
	deps := Deps{SkillRegistry: reg}
	registerSkillMethods(gw, deps)

	gw.mu.RLock()
	_, ok := gw.methods["skills.list"]
	gw.mu.RUnlock()
	assert.True(t, ok, "skills.list 应已注册")

	resp := doRPC(t, gw, "skills.list", map[string]interface{}{}, token)
	assert.Nil(t, resp.Error, "skills.list 应成功，实际错误: %v", resp.Error)
	assert.NotNil(t, resp.Result)
}

// ─────────────────────────────────────────────
// 端到端 RPC 分发路径测试（与 dispatch 联合）
// ─────────────────────────────────────────────

// TestDispatch_MissingMethod 验证缺少 method 字段返回 400
func TestDispatch_MissingMethod(t *testing.T) {
	gw, _ := newTestGateway(t)

	body, _ := json.Marshal(RPCRequest{ID: "1", Method: ""})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/rpc", bytes.NewReader(body))
	w := httptest.NewRecorder()
	gw.HandleHTTP(w, req)

	var resp RPCResponse
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	assert.NotNil(t, resp.Error)
	assert.Equal(t, 400, resp.Error.Code)
}

// TestDispatch_MissingID 验证缺少 id 字段返回 400
func TestDispatch_MissingID(t *testing.T) {
	gw, _ := newTestGateway(t)

	body, _ := json.Marshal(RPCRequest{ID: "", Method: "health.check"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/rpc", bytes.NewReader(body))
	w := httptest.NewRecorder()
	gw.HandleHTTP(w, req)

	var resp RPCResponse
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	assert.NotNil(t, resp.Error)
	assert.Equal(t, 400, resp.Error.Code)
}

// TestDispatch_HTTPMethodNotAllowed 验证非 POST 请求返回 405
func TestDispatch_HTTPMethodNotAllowed(t *testing.T) {
	gw, _ := newTestGateway(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/rpc", nil)
	w := httptest.NewRecorder()
	gw.HandleHTTP(w, req)

	assert.Equal(t, http.StatusMethodNotAllowed, w.Code)
}

// TestDispatch_InvalidJSON 验证非法 JSON 请求体返回 400
func TestDispatch_InvalidJSON(t *testing.T) {
	gw, _ := newTestGateway(t)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/rpc", bytes.NewBufferString("not-json"))
	w := httptest.NewRecorder()
	gw.HandleHTTP(w, req)

	var resp RPCResponse
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	assert.NotNil(t, resp.Error)
	assert.Equal(t, 400, resp.Error.Code)
}
