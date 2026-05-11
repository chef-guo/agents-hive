package tools

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"go.uber.org/zap"

	"github.com/chef-guo/agents-hive/internal/auth"
	"github.com/chef-guo/agents-hive/internal/imctx"
	"github.com/chef-guo/agents-hive/internal/mcphost"
	"github.com/chef-guo/agents-hive/internal/store"
)

// MockIMRouter 模拟 IM 路由器
type MockIMRouter struct {
	sendFunc func(ctx context.Context, req imctx.SendRequest) error
	sentMsgs []imctx.SendRequest
}

func (m *MockIMRouter) SendMessage(ctx context.Context, req imctx.SendRequest) error {
	// 记录发送的消息
	m.sentMsgs = append(m.sentMsgs, req)

	// 如果有自定义发送逻辑，使用它
	if m.sendFunc != nil {
		return m.sendFunc(ctx, req)
	}

	return nil
}

// TestSendIMMessageSuccess 测试成功发送消息
func TestSendIMMessageSuccess(t *testing.T) {
	logger := zap.NewNop()
	host := mcphost.NewHost(logger)
	mockRouter := &MockIMRouter{}

	// 注册工具
	RegisterSendIMMessage(host, logger, mockRouter)

	// 构造输入参数
	input, _ := json.Marshal(map[string]any{
		"platform": "dingtalk",
		"chat_id":  "chat123",
		"content":  "测试消息",
	})

	// 调用工具
	result, err := host.ExecuteTool(context.Background(), "send_im_message", input)
	if err != nil {
		t.Fatalf("工具调用失败: %v", err)
	}

	if result.IsError {
		t.Errorf("预期成功，但返回错误: %v", result.Content)
	}

	// 验证消息已发送
	if len(mockRouter.sentMsgs) != 1 {
		t.Fatalf("预期发送1条消息，实际发送: %d", len(mockRouter.sentMsgs))
	}

	sent := mockRouter.sentMsgs[0]
	if sent.Platform != imctx.PlatformDingTalk {
		t.Errorf("预期平台 dingtalk，实际: %s", sent.Platform)
	}
	if sent.ChatID != "chat123" {
		t.Errorf("预期 chat_id chat123，实际: %s", sent.ChatID)
	}
	if sent.Content != "测试消息" {
		t.Errorf("预期内容 '测试消息'，实际: %s", sent.Content)
	}
}

// TestSendIMMessageAllPlatforms 测试所有支持的平台
func TestSendIMMessageAllPlatforms(t *testing.T) {
	platforms := []string{
		"dingtalk",
		"feishu",
		"wecom",
	}

	for _, platform := range platforms {
		t.Run(platform, func(t *testing.T) {
			logger := zap.NewNop()
			host := mcphost.NewHost(logger)
			mockRouter := &MockIMRouter{}

			RegisterSendIMMessage(host, logger, mockRouter)

			input, _ := json.Marshal(map[string]any{
				"platform": platform,
				"chat_id":  "chat456",
				"content":  "测试消息",
			})

			result, err := host.ExecuteTool(context.Background(), "send_im_message", input)
			if err != nil {
				t.Fatalf("工具调用失败: %v", err)
			}

			if result.IsError {
				t.Errorf("预期成功，但返回错误: %v", result.Content)
			}

			if len(mockRouter.sentMsgs) != 1 {
				t.Fatalf("预期发送1条消息，实际: %d", len(mockRouter.sentMsgs))
			}

			if mockRouter.sentMsgs[0].Platform != imctx.Platform(platform) {
				t.Errorf("预期平台 %s，实际: %s", platform, mockRouter.sentMsgs[0].Platform)
			}
		})
	}
}

// TestSendIMMessageMissingParams 测试缺少参数
func TestSendIMMessageMissingParams(t *testing.T) {
	tests := []struct {
		name   string
		input  map[string]any
		errMsg string
	}{
		{
			name:   "缺少platform",
			input:  map[string]any{"chat_id": "chat123", "content": "测试"},
			errMsg: "platform 参数不能为空",
		},
		{
			name:   "缺少chat_id",
			input:  map[string]any{"platform": "dingtalk", "content": "测试"},
			errMsg: "chat_id 参数不能为空",
		},
		{
			name:   "缺少content",
			input:  map[string]any{"platform": "dingtalk", "chat_id": "chat123"},
			errMsg: "content 参数不能为空",
		},
		{
			name:   "platform为空字符串",
			input:  map[string]any{"platform": "", "chat_id": "chat123", "content": "测试"},
			errMsg: "platform 参数不能为空",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logger := zap.NewNop()
			host := mcphost.NewHost(logger)
			mockRouter := &MockIMRouter{}

			RegisterSendIMMessage(host, logger, mockRouter)

			input, _ := json.Marshal(tt.input)

			result, err := host.ExecuteTool(context.Background(), "send_im_message", input)
			if err != nil {
				t.Fatalf("工具调用失败: %v", err)
			}

			if !result.IsError {
				t.Error("预期返回错误，但成功了")
			}

			var errMsg string
			_ = json.Unmarshal(result.Content, &errMsg)
			if errMsg != tt.errMsg {
				t.Errorf("预期错误信息 '%s'，实际: '%s'", tt.errMsg, errMsg)
			}
		})
	}
}

// TestSendIMMessageSendError 测试发送失败
func TestSendIMMessageSendError(t *testing.T) {
	logger := zap.NewNop()
	host := mcphost.NewHost(logger)

	// Mock 路由器返回错误
	mockRouter := &MockIMRouter{
		sendFunc: func(ctx context.Context, req imctx.SendRequest) error {
			return errors.New("网络错误")
		},
	}

	RegisterSendIMMessage(host, logger, mockRouter)

	input, _ := json.Marshal(map[string]any{
		"platform": "dingtalk",
		"chat_id":  "chat123",
		"content":  "测试消息",
	})

	result, err := host.ExecuteTool(context.Background(), "send_im_message", input)
	if err != nil {
		t.Fatalf("工具调用失败: %v", err)
	}

	if !result.IsError {
		t.Error("预期返回错误，但成功了")
	}

	var errMsg string
	_ = json.Unmarshal(result.Content, &errMsg)
	if errMsg != "发送失败: 网络错误" {
		t.Errorf("预期错误信息包含 '网络错误'，实际: %s", errMsg)
	}
}

// TestSendIMMessageInvalidJSON 测试无效的 JSON 输入
func TestSendIMMessageInvalidJSON(t *testing.T) {
	logger := zap.NewNop()
	host := mcphost.NewHost(logger)
	mockRouter := &MockIMRouter{}

	RegisterSendIMMessage(host, logger, mockRouter)

	// 无效的 JSON
	input := json.RawMessage(`{invalid json}`)

	result, err := host.ExecuteTool(context.Background(), "send_im_message", input)
	if err != nil {
		t.Fatalf("工具调用失败: %v", err)
	}

	if !result.IsError {
		t.Error("预期返回错误，但成功了")
	}

	var errMsg string
	_ = json.Unmarshal(result.Content, &errMsg)
	if errMsg == "" {
		t.Error("预期有错误信息，但为空")
	}
}

// TestSendIMMessageLongContent 测试发送长消息
func TestSendIMMessageLongContent(t *testing.T) {
	logger := zap.NewNop()
	host := mcphost.NewHost(logger)
	mockRouter := &MockIMRouter{}

	RegisterSendIMMessage(host, logger, mockRouter)

	// 创建一个很长的消息（5000个字符）
	longContent := string(make([]byte, 5000))
	for i := range longContent {
		longContent = longContent[:i] + "测"
	}

	input, _ := json.Marshal(map[string]any{
		"platform": "feishu",
		"chat_id":  "chat789",
		"content":  longContent[:5000], // 取前5000个字节
	})

	result, err := host.ExecuteTool(context.Background(), "send_im_message", input)
	if err != nil {
		t.Fatalf("工具调用失败: %v", err)
	}

	if result.IsError {
		t.Errorf("预期成功，但返回错误: %v", result.Content)
	}

	// 验证消息已发送
	if len(mockRouter.sentMsgs) != 1 {
		t.Fatalf("预期发送1条消息，实际: %d", len(mockRouter.sentMsgs))
	}
}

func TestSendIMMessageSchemaPlatformsAndOwnerInput(t *testing.T) {
	logger := zap.NewNop()
	host := mcphost.NewHost(logger)
	RegisterSendIMMessage(host, logger, &MockIMRouter{})

	def, err := host.GetTool("send_im_message")
	if err != nil {
		t.Fatalf("获取工具定义失败: %v", err)
	}

	var schema struct {
		Properties map[string]struct {
			Enum []string `json:"enum"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(def.InputSchema, &schema); err != nil {
		t.Fatalf("解析 schema 失败: %v", err)
	}

	if _, ok := schema.Properties["owner_user_id"]; ok {
		t.Fatal("schema 不应暴露 owner_user_id")
	}
	wantEnum := []string{"dingtalk", "feishu", "wecom", "wechatbot"}
	gotEnum := schema.Properties["platform"].Enum
	if !reflect.DeepEqual(gotEnum, wantEnum) {
		t.Fatalf("platform enum = %#v, want %#v", gotEnum, wantEnum)
	}
	for _, got := range gotEnum {
		if got != "wechatbot" && strings.Contains(got, "wechat") {
			t.Fatalf("非官方微信平台 %q 不应出现在 platform enum 中", got)
		}
	}
}

func TestSendIMMessageIgnoresExplicitOwnerUserID(t *testing.T) {
	logger := zap.NewNop()
	host := mcphost.NewHost(logger)
	mockRouter := &MockIMRouter{}
	RegisterSendIMMessage(host, logger, mockRouter)

	input, _ := json.Marshal(map[string]any{
		"platform":      "feishu",
		"chat_id":       "chat123",
		"content":       "测试消息",
		"owner_user_id": "attacker",
	})
	ctx := auth.WithUser(context.Background(), &auth.User{ID: "alice", Role: "user", Status: "active"})
	result, err := host.ExecuteTool(ctx, "send_im_message", input)
	if err != nil {
		t.Fatalf("工具调用失败: %v", err)
	}
	if result.IsError {
		t.Fatalf("预期成功，但返回错误: %s", result.DecodeContent())
	}
	if len(mockRouter.sentMsgs) != 1 {
		t.Fatalf("预期发送1条消息，实际发送: %d", len(mockRouter.sentMsgs))
	}
	sent := mockRouter.sentMsgs[0]
	if sent.OwnerUserID != "" {
		t.Fatalf("显式 owner_user_id 不应被使用，实际 OwnerUserID=%q", sent.OwnerUserID)
	}
	if sent.TenantKey != "" {
		t.Fatalf("feishu 不应从 auth ctx 派生 TenantKey，实际 TenantKey=%q", sent.TenantKey)
	}
}

func TestSendIMMessageWeChatBotDerivesOwnerFromAuthContext(t *testing.T) {
	logger := zap.NewNop()
	host := mcphost.NewHost(logger)
	mockRouter := &MockIMRouter{}
	convStore := store.NewMemoryStore()
	if err := convStore.UpsertWechatConversation(context.Background(), &store.WechatConversationRecord{
		OwnerUserID:    "alice",
		OwnerAccountID: "wx-owner",
		PeerWxid:       "wxid_peer",
		SessionID:      "im-wechatbot-alice-wxid_peer",
		CanSend:        true,
		SendState:      "ready",
	}); err != nil {
		t.Fatalf("upsert conversation: %v", err)
	}
	RegisterSendIMMessageWithStore(host, logger, mockRouter, convStore)

	input, _ := json.Marshal(map[string]any{
		"platform":      "wechatbot",
		"chat_id":       "wxid_peer",
		"content":       "测试消息",
		"owner_user_id": "attacker",
	})
	ctx := auth.WithUser(context.Background(), &auth.User{ID: "alice", Role: "user", Status: "active"})
	result, err := host.ExecuteTool(ctx, "send_im_message", input)
	if err != nil {
		t.Fatalf("工具调用失败: %v", err)
	}
	if result.IsError {
		t.Fatalf("预期成功，但返回错误: %s", result.DecodeContent())
	}
	if len(mockRouter.sentMsgs) != 1 {
		t.Fatalf("预期发送1条消息，实际发送: %d", len(mockRouter.sentMsgs))
	}

	sent := mockRouter.sentMsgs[0]
	if sent.Platform != imctx.PlatformWeChatBot {
		t.Fatalf("Platform = %q, want %q", sent.Platform, imctx.PlatformWeChatBot)
	}
	if sent.OwnerUserID != "alice" {
		t.Fatalf("OwnerUserID = %q, want alice", sent.OwnerUserID)
	}
	if sent.TenantKey != "alice" {
		t.Fatalf("TenantKey = %q, want alice", sent.TenantKey)
	}
	if sent.ChatID != "wxid_peer" || sent.Content != "测试消息" {
		t.Fatalf("发送请求内容不正确: %#v", sent)
	}
}

func TestSendIm_CrossOwnerRejected(t *testing.T) {
	logger := zap.NewNop()
	host := mcphost.NewHost(logger)
	mockRouter := &MockIMRouter{}
	convStore := store.NewMemoryStore()
	if err := convStore.UpsertWechatConversation(context.Background(), &store.WechatConversationRecord{
		OwnerUserID:    "bob",
		OwnerAccountID: "wx-bob",
		PeerWxid:       "wxid_peer",
		SessionID:      "im-wechatbot-bob-wxid_peer",
		CanSend:        true,
		SendState:      "ready",
	}); err != nil {
		t.Fatalf("upsert conversation: %v", err)
	}
	RegisterSendIMMessageWithStore(host, logger, mockRouter, convStore)

	input, _ := json.Marshal(map[string]any{
		"platform": "wechatbot",
		"chat_id":  "wxid_peer",
		"content":  "测试消息",
	})
	ctx := auth.WithUser(context.Background(), &auth.User{ID: "alice", Role: "user", Status: "active"})
	result, err := host.ExecuteTool(ctx, "send_im_message", input)
	if err != nil {
		t.Fatalf("工具调用失败: %v", err)
	}
	if !result.IsError {
		t.Fatal("预期返回错误，但成功了")
	}
	if got := result.DecodeContent(); got != "无权访问此微信会话，或该联系人尚未形成可发送会话" {
		t.Fatalf("错误信息不清晰: %q", got)
	}
	if len(mockRouter.sentMsgs) != 0 {
		t.Fatalf("跨 owner 不应调用 Router，实际发送: %d", len(mockRouter.sentMsgs))
	}
}

func TestSendIMMessageWeChatBotRequiresContext(t *testing.T) {
	logger := zap.NewNop()
	host := mcphost.NewHost(logger)
	mockRouter := &MockIMRouter{}
	convStore := store.NewMemoryStore()
	if err := convStore.UpsertWechatConversation(context.Background(), &store.WechatConversationRecord{
		OwnerUserID:    "alice",
		OwnerAccountID: "wx-owner",
		PeerWxid:       "wxid_peer",
		SessionID:      "im-wechatbot-alice-wxid_peer",
		CanSend:        false,
		SendState:      "no_context",
	}); err != nil {
		t.Fatalf("upsert conversation: %v", err)
	}
	RegisterSendIMMessageWithStore(host, logger, mockRouter, convStore)

	input, _ := json.Marshal(map[string]any{
		"platform": "wechatbot",
		"chat_id":  "wxid_peer",
		"content":  "测试消息",
	})
	ctx := auth.WithUser(context.Background(), &auth.User{ID: "alice", Role: "user", Status: "active"})
	result, err := host.ExecuteTool(ctx, "send_im_message", input)
	if err != nil {
		t.Fatalf("工具调用失败: %v", err)
	}
	if !result.IsError {
		t.Fatal("预期返回错误，但成功了")
	}
	if got := result.DecodeContent(); got != "该联系人暂无可发送上下文，请先让对方在微信中发一条消息" {
		t.Fatalf("错误信息不清晰: %q", got)
	}
	if len(mockRouter.sentMsgs) != 0 {
		t.Fatalf("无 context 时不应调用 Router，实际发送: %d", len(mockRouter.sentMsgs))
	}
}

func TestSendIMMessageWeChatBotRequiresAuthUser(t *testing.T) {
	logger := zap.NewNop()
	host := mcphost.NewHost(logger)
	mockRouter := &MockIMRouter{}
	RegisterSendIMMessage(host, logger, mockRouter)

	input, _ := json.Marshal(map[string]any{
		"platform": "wechatbot",
		"chat_id":  "wxid_peer",
		"content":  "测试消息",
	})
	result, err := host.ExecuteTool(context.Background(), "send_im_message", input)
	if err != nil {
		t.Fatalf("工具调用失败: %v", err)
	}
	if !result.IsError {
		t.Fatal("预期返回错误，但成功了")
	}
	errMsg := result.DecodeContent()
	if errMsg != "wechatbot 发送需要已登录用户上下文，无法从模型输入 owner_user_id" {
		t.Fatalf("错误信息不清晰: %q", errMsg)
	}
	if len(mockRouter.sentMsgs) != 0 {
		t.Fatalf("缺少 auth user 时不应发送消息，实际发送: %d", len(mockRouter.sentMsgs))
	}
}

func TestSendIMMessageTenantScopedPlatformsLeaveOwnerEmpty(t *testing.T) {
	platforms := []string{"dingtalk", "feishu", "wecom"}
	for _, platform := range platforms {
		t.Run(platform, func(t *testing.T) {
			logger := zap.NewNop()
			host := mcphost.NewHost(logger)
			mockRouter := &MockIMRouter{}
			RegisterSendIMMessage(host, logger, mockRouter)

			input, _ := json.Marshal(map[string]any{
				"platform": platform,
				"chat_id":  "chat123",
				"content":  "测试消息",
			})
			ctx := auth.WithUser(context.Background(), &auth.User{ID: "alice", Role: "user", Status: "active"})
			result, err := host.ExecuteTool(ctx, "send_im_message", input)
			if err != nil {
				t.Fatalf("工具调用失败: %v", err)
			}
			if result.IsError {
				t.Fatalf("预期成功，但返回错误: %s", result.DecodeContent())
			}
			if len(mockRouter.sentMsgs) != 1 {
				t.Fatalf("预期发送1条消息，实际发送: %d", len(mockRouter.sentMsgs))
			}
			sent := mockRouter.sentMsgs[0]
			if sent.OwnerUserID != "" {
				t.Fatalf("%s OwnerUserID = %q, want empty", platform, sent.OwnerUserID)
			}
			if sent.TenantKey != "" {
				t.Fatalf("%s TenantKey = %q, want empty", platform, sent.TenantKey)
			}
		})
	}
}
