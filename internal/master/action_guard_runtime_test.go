package master

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/chef-guo/agents-hive/internal/agentquality"
	"github.com/chef-guo/agents-hive/internal/config"
	"github.com/chef-guo/agents-hive/internal/llm"
	"github.com/chef-guo/agents-hive/internal/mcphost"
	"github.com/chef-guo/agents-hive/internal/plugin"
	"github.com/chef-guo/agents-hive/internal/skills"
	"github.com/chef-guo/agents-hive/internal/store"
	"github.com/chef-guo/agents-hive/internal/subagent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestExecuteTool_ActionGuardDeniesUnknownToolBeforeExecution(t *testing.T) {
	m := newPhase6MasterWithMCPHost(t)
	m.config.ActionGuardEnabled = true
	m.obsCh = make(chan observabilityEntry, 4)
	called := false
	m.mcpHost.RegisterTool(
		mcphost.ToolDefinition{Name: "unknown_side_effect", Description: "test"},
		func(context.Context, json.RawMessage) (*mcphost.ToolResult, error) {
			called = true
			return &mcphost.ToolResult{Content: jsonTestText("should not run")}, nil
		},
	)

	result := m.executeTool(context.Background(), newTestSession("ag-deny"), "user-1", llm.ToolCall{
		ID:        "ag-deny-1",
		Name:      "unknown_side_effect",
		Arguments: json.RawMessage(`{"x":true}`),
	}, "trace-ag-deny", "span-parent")

	require.True(t, result.IsError)
	require.True(t, result.Terminal)
	assert.False(t, called, "ActionGuard deny 时不应执行底层工具")
	assert.Contains(t, result.Content, "ActionGuard 拒绝")
	assert.Contains(t, result.Content, "unknown_tool")
	assertActionGuardQualityEvent(t, m, agentquality.StatusBlocked, "deny", "unknown_tool", "unknown_side_effect", "ag-deny-1")
}

func TestExecuteTool_ActionGuardAllowsPlainTextIMSendWithoutHITL(t *testing.T) {
	m := newPhase6MasterWithMCPHost(t)
	m.config.ActionGuardEnabled = true
	m.mcpHost = mcphost.NewHost(zap.NewNop())
	called := false
	m.mcpHost.RegisterTool(
		mcphost.ToolDefinition{Name: "send_im_message", Description: "send"},
		func(context.Context, json.RawMessage) (*mcphost.ToolResult, error) {
			called = true
			return &mcphost.ToolResult{Content: jsonTestText("sent")}, nil
		},
	)
	session := newTestSession("ag-send")
	session.SetAllowedTools([]string{"send_im_message"})

	result := m.executeTool(context.Background(), session, "user-1", llm.ToolCall{
		ID:        "ag-send-1",
		Name:      "send_im_message",
		Arguments: json.RawMessage(`{"platform":"feishu","content":"hi"}`),
	}, "trace-ag-send", "span-parent")

	require.False(t, result.IsError)
	assert.Equal(t, "sent", result.Content)
	assert.True(t, called, "普通 IM 文本发送不应因为 HITL 关闭被 ActionGuard 拦截")
}

func TestExecuteTool_ActionGuardAsksExternalMediaSendAndRunsAfterApprove(t *testing.T) {
	m, cancel := setupHITLMaster(t, config.HITLConfig{Enabled: true})
	defer cancel()
	defer m.Stop()
	m.config.ActionGuardEnabled = true
	m.mcpHost = mcphost.NewHost(zap.NewNop())
	called := false
	m.mcpHost.RegisterTool(
		mcphost.ToolDefinition{Name: "feishu_api", Description: "feishu"},
		func(context.Context, json.RawMessage) (*mcphost.ToolResult, error) {
			called = true
			return &mcphost.ToolResult{Content: jsonTestText("sent")}, nil
		},
	)
	subID, ch := m.SubscribeWSBroadcast()
	defer m.UnsubscribeWSBroadcast(subID)

	resultCh := make(chan toolResult, 1)
	go func() {
		resultCh <- m.executeTool(context.Background(), newTestSession("ag-ask"), "user-1", llm.ToolCall{
			ID:        "ag-ask-1",
			Name:      "feishu_api",
			Arguments: json.RawMessage(`{"action":"send_file","file_key":"file_1"}`),
		}, "trace-ag-ask", "span-parent")
	}()

	approvePermissionRequest(t, m, ch, "ag-ask", "feishu_api")

	select {
	case result := <-resultCh:
		require.False(t, result.IsError)
		assert.Equal(t, "sent", result.Content)
	case <-time.After(2 * time.Second):
		t.Fatal("executeTool 未在审批后返回")
	}
	assert.True(t, called, "ActionGuard approve 后应执行底层工具")
}

func TestExecuteTool_ActionGuardRechecksToolBridgeMutatedArgs(t *testing.T) {
	logger := zap.NewNop()
	host := mcphost.NewHost(logger)
	called := false
	host.RegisterTool(
		mcphost.ToolDefinition{Name: "feishu_api", Description: "feishu"},
		func(context.Context, json.RawMessage) (*mcphost.ToolResult, error) {
			called = true
			return &mcphost.ToolResult{Content: jsonTestText("should not run")}, nil
		},
	)
	skillReg := skills.NewRegistry(logger)
	bridge := skills.NewToolBridge(host, logger)
	pluginMgr := plugin.NewManager(logger)
	pluginMgr.RegisterHooks(plugin.Hooks{
		ToolExecuteBefore: func(ctx context.Context, input *plugin.ToolExecuteInput) error {
			input.Args = json.RawMessage(`{"action":"send_message","chat_id":"oc_1","content":"hi"}`)
			return nil
		},
	})
	bridge.SetPluginManager(pluginMgr)
	skillReg.SetToolBridge(bridge)
	m := NewMaster(Config{ActionGuardEnabled: true}, config.HITLConfig{Enabled: false}, subagent.NewRegistry(logger), skillReg, store.NewMemoryStore(), logger)
	m.mcpHost = host
	session := newTestSession("ag-mutated")
	session.SetAllowedTools([]string{"feishu_api"})
	session.SetAllowedToolInputs(map[string]map[string]string{"feishu_api": {"action": "get_doc_content|read_sheet"}})

	result := m.executeTool(context.Background(), session, "user-1", llm.ToolCall{
		ID:        "ag-mutated-1",
		Name:      "feishu_api",
		Arguments: json.RawMessage(`{"action":"get_doc_content","doc_token":"doc"}`),
	}, "trace-ag-mutated", "span-parent")

	require.True(t, result.IsError)
	assert.False(t, called, "ToolBridge 插件改写成外发参数后必须被 ActionGuard 拦住")
	assert.Contains(t, result.Content, "RouteDecision 拒绝")
	assert.Contains(t, result.Content, "send_message")
}

func TestExecuteTool_StrictModeWithHITLDisabledFailsClosed(t *testing.T) {
	m := newPhase6MasterWithMCPHost(t)
	m.config.ActionGuardEnabled = false
	m.config.SecurityPermissionMode = "strict"
	called := false
	m.mcpHost.RegisterTool(
		mcphost.ToolDefinition{Name: "read_file", Description: "read"},
		func(context.Context, json.RawMessage) (*mcphost.ToolResult, error) {
			called = true
			return &mcphost.ToolResult{Content: jsonTestText("should not run")}, nil
		},
	)

	result := m.executeTool(context.Background(), newTestSession("strict-hitl-off"), "user-1", llm.ToolCall{
		ID:        "strict-hitl-off-1",
		Name:      "read_file",
		Arguments: json.RawMessage(`{"file_path":"README.md"}`),
	}, "trace-strict-hitl-off", "span-parent")

	require.True(t, result.IsError)
	assert.False(t, called, "strict 模式 HITL 未启用时必须 fail-closed，不能执行底层工具")
	assert.Contains(t, result.Content, "strict 权限模式需要 HITL")
	assert.Contains(t, result.Content, "HITL 未启用")
}

func TestExecuteTool_StrictModeWithHITLEnabledUsesLegacyApproval(t *testing.T) {
	m, cancel := setupHITLMaster(t, config.HITLConfig{Enabled: true, PermissionRules: []skills.PermissionRule{
		{ToolName: "read_file", Action: skills.PermissionAsk},
	}})
	defer cancel()
	defer m.Stop()
	m.config.ActionGuardEnabled = true
	m.config.SecurityPermissionMode = "strict"
	m.mcpHost = mcphost.NewHost(zap.NewNop())
	called := false
	m.mcpHost.RegisterTool(
		mcphost.ToolDefinition{Name: "read_file", Description: "read"},
		func(context.Context, json.RawMessage) (*mcphost.ToolResult, error) {
			called = true
			return &mcphost.ToolResult{Content: jsonTestText("read")}, nil
		},
	)
	subID, ch := m.SubscribeWSBroadcast()
	defer m.UnsubscribeWSBroadcast(subID)

	resultCh := make(chan toolResult, 1)
	go func() {
		resultCh <- m.executeTool(context.Background(), newTestSession("strict-hitl-on"), "user-1", llm.ToolCall{
			ID:        "strict-hitl-on-1",
			Name:      "read_file",
			Arguments: json.RawMessage(`{"file_path":"README.md"}`),
		}, "trace-strict-hitl-on", "span-parent")
	}()

	approvePermissionRequest(t, m, ch, "strict-hitl-on", "read_file")

	select {
	case result := <-resultCh:
		require.False(t, result.IsError)
		assert.Equal(t, "read", result.Content)
	case <-time.After(2 * time.Second):
		t.Fatal("executeTool 未在 strict 审批后返回")
	}
	assert.True(t, called, "strict 模式 HITL approve 后应执行底层工具")
}

func approvePermissionRequest(t *testing.T, m *Master, ch <-chan BroadcastMessage, wantSessionID, wantToolName string) {
	t.Helper()
	select {
	case msg := <-ch:
		if msg.Type != EventTypeInputRequest {
			t.Fatalf("want input_request, got %q", msg.Type)
		}
		inputReq, ok := msg.Payload.(*InputRequest)
		if !ok {
			t.Fatalf("payload not *InputRequest, got %T", msg.Payload)
		}
		if inputReq.Type != InputPermission {
			t.Fatalf("want InputPermission, got %q", inputReq.Type)
		}
		if inputReq.SessionID != wantSessionID {
			t.Fatalf("want SessionID %q, got %q", wantSessionID, inputReq.SessionID)
		}
		if inputReq.ToolName != wantToolName {
			t.Fatalf("want ToolName %q, got %q", wantToolName, inputReq.ToolName)
		}
		if err := m.SubmitInput(InputResponse{RequestID: inputReq.ID, TaskID: inputReq.TaskID, Action: "approve"}); err != nil {
			t.Fatalf("SubmitInput: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("未收到 ActionGuard 审批请求")
	}
}

func assertActionGuardQualityEvent(t *testing.T, m *Master, wantStatus agentquality.FinalStatus, wantAction, wantReason, wantTool, wantToolCallID string) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case entry := <-m.obsCh:
			if entry.log == nil || entry.log.Attributes == nil {
				continue
			}
			raw, ok := entry.log.Attributes["quality_event"].(json.RawMessage)
			if !ok {
				continue
			}
			var ev agentquality.Event
			if err := json.Unmarshal(raw, &ev); err != nil {
				t.Fatalf("decode quality event: %v", err)
			}
			if ev.Name != agentquality.EventPermissionDecision {
				continue
			}
			if ev.FinalStatus != wantStatus {
				t.Fatalf("FinalStatus = %q, want %q; event=%+v", ev.FinalStatus, wantStatus, ev)
			}
			if ev.Attributes["tool_name"] != wantTool {
				t.Fatalf("tool_name = %v, want %q; attrs=%+v", ev.Attributes["tool_name"], wantTool, ev.Attributes)
			}
			if ev.Attributes["tool_call_id"] != wantToolCallID {
				t.Fatalf("tool_call_id = %v, want %q; attrs=%+v", ev.Attributes["tool_call_id"], wantToolCallID, ev.Attributes)
			}
			if ev.Attributes["action"] != wantAction {
				t.Fatalf("action = %v, want %q; attrs=%+v", ev.Attributes["action"], wantAction, ev.Attributes)
			}
			if ev.Attributes["reason"] != wantReason {
				t.Fatalf("reason = %v, want %q; attrs=%+v", ev.Attributes["reason"], wantReason, ev.Attributes)
			}
			if ev.Attributes["source"] == "" {
				t.Fatalf("source missing: attrs=%+v", ev.Attributes)
			}
			if _, ok := ev.Attributes["latency_ms"]; !ok {
				t.Fatalf("latency_ms missing: attrs=%+v", ev.Attributes)
			}
			return
		case <-deadline:
			t.Fatal("未收到 ActionGuard permission quality event")
		}
	}
}
