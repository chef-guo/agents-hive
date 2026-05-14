package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"

	"github.com/chef-guo/agents-hive/internal/auth"
	"github.com/chef-guo/agents-hive/internal/imcore"
	"github.com/chef-guo/agents-hive/internal/imctx"
	"github.com/chef-guo/agents-hive/internal/mcphost"
	"github.com/chef-guo/agents-hive/internal/toolctx"
)

type fakeIMAdapter struct {
	platform imcore.Platform

	lastScope  imcore.CallerScope
	lastLookup imcore.RecipientLookup
	lastTarget imcore.SendTarget
}

func (a *fakeIMAdapter) Platform() imcore.Platform {
	return a.platform
}

func (a *fakeIMAdapter) Capabilities() []imcore.Capability {
	return []imcore.Capability{
		imcore.CapabilitySearchRecipients,
		imcore.CapabilityListRecentConversations,
		imcore.CapabilitySendText,
	}
}

func (a *fakeIMAdapter) SearchRecipients(ctx context.Context, scope imcore.CallerScope, query string, limit int) ([]imcore.Recipient, error) {
	a.lastScope = scope
	return []imcore.Recipient{{
		Platform:       a.platform,
		ID:             "user-1",
		DisplayName:    query,
		Kind:           "user",
		ExternalIDType: "open_id",
		CanSend:        true,
		SendState:      "ready",
	}}, nil
}

func (a *fakeIMAdapter) ListRecentConversations(ctx context.Context, scope imcore.CallerScope, limit int) ([]imcore.Recipient, error) {
	a.lastScope = scope
	return []imcore.Recipient{{
		Platform:       a.platform,
		ID:             "conv-1",
		DisplayName:    "最近会话",
		Kind:           "conversation",
		ExternalIDType: "conversation_id",
		CanSend:        true,
		SendState:      "ready",
	}}, nil
}

func (a *fakeIMAdapter) ResolveRecipient(ctx context.Context, scope imcore.CallerScope, input imcore.RecipientLookup) (imcore.Recipient, error) {
	a.lastScope = scope
	a.lastLookup = input
	return imcore.Recipient{
		Platform:       a.platform,
		ID:             input.RecipientID,
		DisplayName:    "已解析用户",
		Kind:           "user",
		ExternalIDType: "open_id",
		CanSend:        true,
		SendState:      "ready",
	}, nil
}

func (a *fakeIMAdapter) SendMessage(ctx context.Context, scope imcore.CallerScope, target imcore.SendTarget) (imcore.SendResult, error) {
	a.lastScope = scope
	a.lastTarget = target
	return imcore.SendResult{
		Platform:   target.Platform,
		TargetID:   target.RecipientID,
		TargetKind: "user",
		MessageID:  "msg-1",
		Delivered:  !target.DryRun,
		DryRun:     target.DryRun,
	}, nil
}

func TestIMAPIListRecentConversations(t *testing.T) {
	adapter := &fakeIMAdapter{platform: imcore.PlatformWeChatBot}
	host := newIMAPITestHost(adapter)
	ctx := auth.WithUser(context.Background(), &auth.User{ID: "alice", Role: "user", Status: "active"})

	result := executeIMAPITestTool(t, host, ctx, map[string]any{
		"action":   "list_recent_conversations",
		"platform": "wechatbot",
	})
	if result.IsError {
		t.Fatalf("预期成功，但返回错误: %s", result.DecodeContent())
	}

	var got []imcore.Recipient
	decodeIMAPIResult(t, result, &got)
	if len(got) != 1 || got[0].ID != "conv-1" || !got[0].CanSend {
		t.Fatalf("unexpected conversations: %+v", got)
	}
	if adapter.lastScope.OwnerUserID != "alice" {
		t.Fatalf("OwnerUserID = %q, want alice", adapter.lastScope.OwnerUserID)
	}
}

func TestIMAPIResolveRecipient(t *testing.T) {
	adapter := &fakeIMAdapter{platform: imcore.PlatformFeishu}
	host := newIMAPITestHost(adapter)

	result := executeIMAPITestTool(t, host, context.Background(), map[string]any{
		"action":       "resolve_recipient",
		"platform":     "feishu",
		"recipient_id": "user-1",
	})
	if result.IsError {
		t.Fatalf("预期成功，但返回错误: %s", result.DecodeContent())
	}

	var got imcore.Recipient
	decodeIMAPIResult(t, result, &got)
	if got.ID != "user-1" || adapter.lastLookup.RecipientID != "user-1" {
		t.Fatalf("unexpected resolve result=%+v lookup=%+v", got, adapter.lastLookup)
	}
}

func TestIMAPISendMessageUsesAdapter(t *testing.T) {
	adapter := &fakeIMAdapter{platform: imcore.PlatformFeishu}
	host := newIMAPITestHost(adapter)

	result := executeIMAPITestTool(t, host, context.Background(), map[string]any{
		"action":       "send_message",
		"platform":     "feishu",
		"recipient_id": "user-1",
		"content":      "hello",
		"dry_run":      true,
	})
	if result.IsError {
		t.Fatalf("预期成功，但返回错误: %s", result.DecodeContent())
	}

	var got imcore.SendResult
	decodeIMAPIResult(t, result, &got)
	if !got.DryRun || got.Delivered {
		t.Fatalf("dry-run result = %+v, want dry_run true and delivered false", got)
	}
	if adapter.lastTarget.Platform != imcore.PlatformFeishu || adapter.lastTarget.Content != "hello" || !adapter.lastTarget.DryRun {
		t.Fatalf("unexpected send target: %+v", adapter.lastTarget)
	}
}

func TestIMAPISendMessageForceDryRun(t *testing.T) {
	adapter := &fakeIMAdapter{platform: imcore.PlatformFeishu}
	host := mcphost.NewHost(zap.NewNop())
	RegisterIMAPIToolWithOptions(host, zap.NewNop(), imcore.NewService(adapter), IMAPIToolOptions{
		ForceDryRun: true,
	})

	result := executeIMAPITestTool(t, host, context.Background(), map[string]any{
		"action":       "send_message",
		"platform":     "feishu",
		"recipient_id": "user-1",
		"content":      "hello",
	})
	if result.IsError {
		t.Fatalf("预期成功，但返回错误: %s", result.DecodeContent())
	}

	var got imcore.SendResult
	decodeIMAPIResult(t, result, &got)
	if !got.DryRun || got.Delivered {
		t.Fatalf("force dry-run result = %+v, want dry_run true and delivered false", got)
	}
	if !adapter.lastTarget.DryRun {
		t.Fatalf("adapter target dry_run = false, want true")
	}
}

func TestIMAPISendMessageRequiresConfiguredPlatform(t *testing.T) {
	host := newIMAPITestHost(&fakeIMAdapter{platform: imcore.PlatformFeishu})

	result := executeIMAPITestTool(t, host, context.Background(), map[string]any{
		"action":       "send_message",
		"platform":     "wechatbot",
		"recipient_id": "user-1",
		"content":      "hello",
	})
	if !result.IsError {
		t.Fatalf("预期未配置平台错误，got: %s", result.DecodeContent())
	}
	if !strings.Contains(result.DecodeContent(), "im platform wechatbot not configured") {
		t.Fatalf("unexpected error: %s", result.DecodeContent())
	}
}

func TestIMAPIWechatBotRequiresAuthenticatedOwner(t *testing.T) {
	host := newIMAPITestHost(&fakeIMAdapter{platform: imcore.PlatformWeChatBot})

	result := executeIMAPITestTool(t, host, context.Background(), map[string]any{
		"action":   "list_recent_conversations",
		"platform": "wechatbot",
	})
	if !result.IsError {
		t.Fatalf("预期 wechatbot 无 owner 错误，got: %s", result.DecodeContent())
	}
	if result.DecodeContent() != "wechatbot requires authenticated owner" {
		t.Fatalf("unexpected error: %s", result.DecodeContent())
	}
}

func TestIMAPIAuditSendMessageFieldsPIISafe(t *testing.T) {
	core, logs := observer.New(zapcore.DebugLevel)
	logger := zap.New(core)
	adapter := &fakeIMAdapter{platform: imcore.PlatformFeishu}
	host := newIMAPITestHostWithLogger(logger, adapter)
	rawRecipientID := "ou_raw_recipient_123"
	ctx := toolctx.WithToolContext(context.Background(), &toolctx.ToolContext{
		TraceID:      "trace-1",
		SpanID:       "span-1",
		ParentSpanID: "parent-span-1",
		TurnID:       "turn-1",
		ToolCallID:   "call-1",
	})

	result := executeIMAPITestTool(t, host, ctx, map[string]any{
		"action":       "send_message",
		"platform":     "feishu",
		"recipient_id": rawRecipientID,
		"content":      "hello",
		"dry_run":      true,
	})
	if result.IsError {
		t.Fatalf("预期成功，但返回错误: %s", result.DecodeContent())
	}

	entry := requireIMAPIAuditLog(t, logs)
	fields := entry.ContextMap()
	assertIMAPIAuditField(t, fields, "action", "send_message")
	assertIMAPIAuditField(t, fields, "platform", "feishu")
	assertIMAPIAuditField(t, fields, "status", "success")
	assertIMAPIAuditField(t, fields, "dry_run", true)
	assertIMAPIAuditField(t, fields, "target_kind", "user")
	assertIMAPIAuditField(t, fields, "content_len", int64(5))
	assertIMAPIAuditField(t, fields, "result_count", int64(1))
	assertIMAPIAuditField(t, fields, "tool", "im_api")
	assertIMAPIAuditField(t, fields, "target_id_hash", imctx.SafeSenderID(rawRecipientID))
	assertIMAPIAuditField(t, fields, "trace_id", "trace-1")
	assertIMAPIAuditField(t, fields, "span_id", "span-1")
	assertIMAPIAuditField(t, fields, "parent_span_id", "parent-span-1")
	assertIMAPIAuditField(t, fields, "turn_id", "turn-1")
	assertIMAPIAuditField(t, fields, "tool_call_id", "call-1")
	if _, exists := fields["duration_ms"]; !exists {
		t.Fatal("audit field \"duration_ms\" missing")
	}
	assertIMAPIAuditLogDoesNotContain(t, entry, rawRecipientID)
	if _, exists := fields["recipient_id"]; exists {
		t.Fatal("audit log must not include raw recipient_id field")
	}
	if _, exists := fields["target_hash"]; exists {
		t.Fatal("audit log must use target_id_hash, not target_hash")
	}
}

func TestIMAPIAuditErrorFieldsPIISafeForConversationID(t *testing.T) {
	core, logs := observer.New(zapcore.DebugLevel)
	logger := zap.New(core)
	host := newIMAPITestHostWithLogger(logger, &fakeIMAdapter{platform: imcore.PlatformFeishu})
	rawConversationID := "oc_raw_conversation_456"

	result := executeIMAPITestTool(t, host, context.Background(), map[string]any{
		"action":          "send_message",
		"platform":        "wechatbot",
		"conversation_id": rawConversationID,
		"content":         "hello world",
	})
	if !result.IsError {
		t.Fatalf("预期失败，但返回成功: %s", result.DecodeContent())
	}

	entry := requireIMAPIAuditLog(t, logs)
	fields := entry.ContextMap()
	assertIMAPIAuditField(t, fields, "action", "send_message")
	assertIMAPIAuditField(t, fields, "platform", "wechatbot")
	assertIMAPIAuditField(t, fields, "status", "error")
	assertIMAPIAuditField(t, fields, "dry_run", false)
	assertIMAPIAuditField(t, fields, "target_kind", "conversation")
	assertIMAPIAuditField(t, fields, "content_len", int64(11))
	assertIMAPIAuditField(t, fields, "result_count", int64(0))
	assertIMAPIAuditField(t, fields, "target_id_hash", imctx.SafeSenderID(rawConversationID))
	assertIMAPIAuditLogDoesNotContain(t, entry, rawConversationID)
	if _, exists := fields["conversation_id"]; exists {
		t.Fatal("audit log must not include raw conversation_id field")
	}
}

func newIMAPITestHost(adapters ...imcore.Adapter) *mcphost.Host {
	return newIMAPITestHostWithLogger(zap.NewNop(), adapters...)
}

func newIMAPITestHostWithLogger(logger *zap.Logger, adapters ...imcore.Adapter) *mcphost.Host {
	host := mcphost.NewHost(logger)
	RegisterIMAPITool(host, logger, imcore.NewService(adapters...))
	return host
}

func executeIMAPITestTool(t *testing.T, host *mcphost.Host, ctx context.Context, input map[string]any) *mcphost.ToolResult {
	t.Helper()
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatalf("marshal input: %v", err)
	}
	result, err := host.ExecuteTool(ctx, "im_api", raw)
	if err != nil {
		t.Fatalf("execute im_api: %v", err)
	}
	return result
}

func decodeIMAPIResult(t *testing.T, result *mcphost.ToolResult, out any) {
	t.Helper()
	if err := json.Unmarshal([]byte(result.DecodeContent()), out); err != nil {
		t.Fatalf("decode im_api result: %v; content=%s", err, result.DecodeContent())
	}
}

func requireIMAPIAuditLog(t *testing.T, logs *observer.ObservedLogs) observer.LoggedEntry {
	t.Helper()
	entries := logs.FilterMessage("im_api 审计").All()
	if len(entries) != 1 {
		t.Fatalf("expected one im_api audit log, got %d", len(entries))
	}
	return entries[0]
}

func assertIMAPIAuditField(t *testing.T, fields map[string]any, key string, want any) {
	t.Helper()
	got, exists := fields[key]
	if !exists {
		t.Fatalf("audit field %q missing; fields=%v", key, fields)
	}
	if got != want {
		t.Fatalf("audit field %q = %v (%T), want %v (%T)", key, got, got, want, want)
	}
}

func assertIMAPIAuditLogDoesNotContain(t *testing.T, entry observer.LoggedEntry, raw string) {
	t.Helper()
	if strings.Contains(entry.Entry.Message, raw) {
		t.Fatalf("audit message leaked raw identifier %q", raw)
	}
	for _, field := range entry.Context {
		if strings.Contains(field.String, raw) {
			t.Fatalf("audit field %q leaked raw identifier %q", field.Key, raw)
		}
	}
}

// TestIMAPI_AuditDoesNotLeakPII 是 G8 grep gate：遍历所有 action 路径与已知 PII 模式，
// 任意 audit 字段值（含 message）出现 raw recipient/conversation/content/query/phone/
// email/wxid/open_id 哨兵字符串即视为 PII 泄漏。新增 audit 字段时必须先确保不会触发
// 黑名单，否则审计日志会暴露平台 raw ID 或用户内容。
func TestIMAPI_AuditDoesNotLeakPII(t *testing.T) {
	const (
		piiOpenID         = "ou_pii_leak_canary_zzz"
		piiChatID         = "oc_pii_leak_canary_zzz"
		piiWxid           = "wxid_pii_leak_canary_zzz"
		piiFeishuUserID   = "afde2a69-pii-leak-canary"
		piiPhone          = "+8613800138000"
		piiPhoneLocal     = "13800138000"
		piiEmail          = "leak.canary@example.com"
		piiSensitiveText  = "极其敏感的消息内容PII泄漏哨兵 with phone +8613800138000 and email leak.canary@example.com"
		piiSensitiveQuery = "敏感查询关键字PII哨兵 wxid_pii_leak_canary_zzz"
	)
	piiBlacklist := []string{
		piiOpenID, piiChatID, piiWxid, piiFeishuUserID,
		piiPhone, piiPhoneLocal, piiEmail,
		piiSensitiveText, piiSensitiveQuery,
	}

	authedCtx := auth.WithUser(context.Background(), &auth.User{ID: "alice", Role: "user", Status: "active"})

	cases := []struct {
		name    string
		ctx     context.Context
		input   map[string]any
		wantErr bool
	}{
		{
			name: "search_recipients",
			ctx:  context.Background(),
			input: map[string]any{
				"action":   "search_recipients",
				"platform": "feishu",
				"query":    piiSensitiveQuery,
			},
		},
		{
			name: "list_recent_conversations",
			ctx:  authedCtx,
			input: map[string]any{
				"action":   "list_recent_conversations",
				"platform": "wechatbot",
			},
		},
		{
			name: "resolve_recipient",
			ctx:  context.Background(),
			input: map[string]any{
				"action":       "resolve_recipient",
				"platform":     "feishu",
				"recipient_id": piiOpenID,
			},
		},
		{
			name: "send_message_recipient",
			ctx:  context.Background(),
			input: map[string]any{
				"action":       "send_message",
				"platform":     "feishu",
				"recipient_id": piiOpenID,
				"content":      piiSensitiveText,
				"dry_run":      true,
			},
		},
		{
			name: "send_message_conversation",
			ctx:  authedCtx,
			input: map[string]any{
				"action":          "send_message",
				"platform":        "wechatbot",
				"conversation_id": piiChatID,
				"content":         piiSensitiveText,
				"dry_run":         true,
			},
		},
		{
			name: "send_message_unconfigured_platform_error_path",
			ctx:  context.Background(),
			input: map[string]any{
				"action":       "send_message",
				"platform":     "dingtalk",
				"recipient_id": piiFeishuUserID,
				"content":      piiSensitiveText,
			},
			wantErr: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			core, logs := observer.New(zapcore.DebugLevel)
			logger := zap.New(core)
			host := newIMAPITestHostWithLogger(logger,
				&fakeIMAdapter{platform: imcore.PlatformFeishu},
				&fakeIMAdapter{platform: imcore.PlatformWeChatBot},
			)

			result := executeIMAPITestTool(t, host, tc.ctx, tc.input)
			if tc.wantErr && !result.IsError {
				t.Fatalf("预期失败但成功: %s", result.DecodeContent())
			}
			if !tc.wantErr && result.IsError {
				t.Fatalf("预期成功但失败: %s", result.DecodeContent())
			}

			entry := requireIMAPIAuditLog(t, logs)
			for _, pii := range piiBlacklist {
				if strings.Contains(entry.Entry.Message, pii) {
					t.Fatalf("audit message leaked PII canary %q in %q", pii, entry.Entry.Message)
				}
				for _, field := range entry.Context {
					values := []string{field.String}
					if field.Interface != nil {
						values = append(values, fmt.Sprintf("%v", field.Interface))
					}
					for _, v := range values {
						if v != "" && strings.Contains(v, pii) {
							t.Fatalf("audit field %q leaked PII canary %q (value=%q)", field.Key, pii, v)
						}
					}
				}
			}
		})
	}
}
