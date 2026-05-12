package master

import (
	"encoding/json"
	"testing"

	"github.com/chef-guo/agents-hive/internal/llm"
	"github.com/chef-guo/agents-hive/internal/router"
)

func externalSendIntentForTest() router.IntentFrame {
	return router.IntentFrame{
		Kind:              router.IntentExternalWrite,
		AllowsSideEffects: true,
		RequiresExternal:  true,
		Confidence:        0.95,
		Signals:           []string{"test_structured_intent"},
	}
}

func mustExternalSendContract(t *testing.T) IntentContract {
	t.Helper()
	contract, ok := NewIntentContract(externalSendIntentForTest())
	if !ok {
		t.Fatal("external-send intent should create a contract")
	}
	return contract
}

func TestIntentContract_ExternalSendWeatherOnlyIncomplete(t *testing.T) {
	contract := mustExternalSendContract(t)
	eval := contract.Evaluate([]llm.MessageWithTools{
		{Role: "user", Content: llm.NewTextContent("给郭松发一下今天的天气信息")},
		{Role: "tool", ToolName: "websearch", Content: llm.NewTextContent("weather result")},
		{Role: "tool", ToolName: "webfetch", Content: llm.NewTextContent("weather detail")},
	}, llm.ChatWithToolsResponse{Content: "今天北京天气晴。", FinishReason: "stop"})

	if eval.Status != ContractIncomplete {
		t.Fatalf("Status = %q, want incomplete; eval=%+v", eval.Status, eval)
	}
	assertMissingRequirement(t, eval, MissingRecipient)
	assertMissingRequirement(t, eval, MissingSendAttempt)
	if !eval.Evidence.ContentPrepared {
		t.Fatalf("weather/search tool result should count as content preparation evidence: %+v", eval.Evidence)
	}
}

func TestIntentContract_ExternalSendUniqueLookupStillNeedsSend(t *testing.T) {
	contract := mustExternalSendContract(t)
	eval := contract.Evaluate([]llm.MessageWithTools{
		{Role: "user", Content: llm.NewTextContent("给郭松发一下今天的天气信息")},
		{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "lookup-1", Name: "feishu_api", Arguments: json.RawMessage(`{"action":"search_contacts","query":"郭松"}`)}}},
		{Role: "tool", ToolCallID: "lookup-1", ToolName: "feishu_api", Content: llm.NewTextContent(`{"contacts":[{"name":"郭松","open_id":"ou_1"}]}`)},
		{Role: "tool", ToolName: "websearch", Content: llm.NewTextContent("weather result")},
	}, llm.ChatWithToolsResponse{Content: "今天北京天气晴。", FinishReason: "stop"})

	if eval.Status != ContractIncomplete {
		t.Fatalf("Status = %q, want incomplete; eval=%+v", eval.Status, eval)
	}
	if !eval.Evidence.RecipientResolved {
		t.Fatalf("unique contact lookup should resolve recipient: %+v", eval.Evidence)
	}
	assertMissingRequirement(t, eval, MissingSendAttempt)
}

func TestIntentContract_ExternalSendAmbiguousLookupWithQuestionNeedsUser(t *testing.T) {
	contract := mustExternalSendContract(t)
	eval := contract.Evaluate([]llm.MessageWithTools{
		{Role: "user", Content: llm.NewTextContent("给郭松发一下今天的天气信息")},
		{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "lookup-1", Name: "feishu_api", Arguments: json.RawMessage(`{"action":"search_contacts","query":"郭松"}`)}}},
		{Role: "tool", ToolCallID: "lookup-1", ToolName: "feishu_api", Content: llm.NewTextContent(`{"contacts":[{"name":"郭松","open_id":"ou_1"},{"name":"郭松","open_id":"ou_2"}]}`)},
		{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "question-1", Name: "question", Arguments: json.RawMessage(`{"question":"请选择联系人","options":["ou_1","ou_2"]}`)}}},
	}, llm.ChatWithToolsResponse{FinishReason: "tool_calls"})

	if eval.Status != ContractNeedsUser {
		t.Fatalf("Status = %q, want needs_user; eval=%+v", eval.Status, eval)
	}
	if !eval.Evidence.RecipientAmbiguous || !eval.Evidence.QuestionAsked {
		t.Fatalf("ambiguous lookup + question tool should be structured needs_user evidence: %+v", eval.Evidence)
	}
}

func TestIntentContract_ExternalSendQuestionWithoutLookupIncomplete(t *testing.T) {
	contract := mustExternalSendContract(t)
	eval := contract.Evaluate([]llm.MessageWithTools{
		{Role: "user", Content: llm.NewTextContent("给郭松发一下今天的天气信息")},
		{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "question-1", Name: "question", Arguments: json.RawMessage(`{"question":"请选择联系人"}`)}}},
	}, llm.ChatWithToolsResponse{FinishReason: "tool_calls"})

	if eval.Status != ContractIncomplete {
		t.Fatalf("question without lookup must not satisfy ambiguity handling; eval=%+v", eval)
	}
	assertMissingRequirement(t, eval, MissingRecipient)
}

func TestIntentContract_ExternalSendSatisfiedBySendAttempt(t *testing.T) {
	contract := mustExternalSendContract(t)
	for _, tc := range []struct {
		name string
		call llm.ToolCall
	}{
		{
			name: "feishu send message",
			call: llm.ToolCall{ID: "send-1", Name: "feishu_api", Arguments: json.RawMessage(`{"action":"send_message","chat_id":"oc_1","content":"天气"}`)},
		},
		{
			name: "feishu send message with open_id",
			call: llm.ToolCall{ID: "send-1", Name: "feishu_api", Arguments: json.RawMessage(`{"action":"send_message","open_id":"ou_1","content":"天气"}`)},
		},
		{
			name: "feishu send message with user_id",
			call: llm.ToolCall{ID: "send-1", Name: "feishu_api", Arguments: json.RawMessage(`{"action":"send_message","user_id":"u_1","content":"天气"}`)},
		},
		{
			name: "generic im send",
			call: llm.ToolCall{ID: "send-1", Name: "send_im_message", Arguments: json.RawMessage(`{"platform":"feishu","chat_id":"oc_1","content":"天气"}`)},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			eval := contract.Evaluate([]llm.MessageWithTools{
				{Role: "user", Content: llm.NewTextContent("给郭松发一下今天的天气信息")},
				{Role: "assistant", ToolCalls: []llm.ToolCall{tc.call}},
			}, llm.ChatWithToolsResponse{Content: "已发送。", FinishReason: "stop"})
			if eval.Status != ContractSatisfied {
				t.Fatalf("Status = %q, want satisfied; eval=%+v", eval.Status, eval)
			}
		})
	}
}

func TestIntentContract_ExternalSendSuccessfulRetryOverridesEarlierFailure(t *testing.T) {
	contract := mustExternalSendContract(t)
	eval := contract.Evaluate([]llm.MessageWithTools{
		{Role: "user", Content: llm.NewTextContent("现在能不能发")},
		{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "bad-send", Name: "feishu_api", Arguments: json.RawMessage(`{"action":"send_message","chat_id":"afde2a69","content":"天气"}`)}}},
		{Role: "tool", ToolCallID: "bad-send", ToolName: "feishu_api", IsError: true, Content: llm.NewTextContent("invalid receive_id")},
		{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "good-send", Name: "feishu_api", Arguments: json.RawMessage(`{"action":"send_message","open_id":"ou_1","content":"天气"}`)}}},
		{Role: "tool", ToolCallID: "good-send", ToolName: "feishu_api", Content: llm.NewTextContent("消息已发送到 ou_1")},
	}, llm.ChatWithToolsResponse{Content: "已发送。", FinishReason: "stop"})

	if eval.Status != ContractSatisfied {
		t.Fatalf("成功重试应覆盖早先发送失败；eval=%+v", eval)
	}
}

func TestIntentContract_ExternalSendSendAttemptWithMissingPayloadIncomplete(t *testing.T) {
	contract := mustExternalSendContract(t)
	eval := contract.Evaluate([]llm.MessageWithTools{
		{Role: "user", Content: llm.NewTextContent("给郭松发一下今天的天气信息")},
		{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "send-1", Name: "feishu_api", Arguments: json.RawMessage(`{"action":"send_message","content":"天气"}`)}}},
	}, llm.ChatWithToolsResponse{Content: "已发送。", FinishReason: "stop"})

	if eval.Status != ContractIncomplete {
		t.Fatalf("send attempt without recipient must be incomplete; eval=%+v", eval)
	}
	assertMissingRequirement(t, eval, MissingRecipient)
	assertMissingRequirement(t, eval, MissingSendPayload)
	assertNotMissingRequirement(t, eval, MissingContent)
}

func TestIntentContract_ExternalSendIgnoresPreviousTurnSend(t *testing.T) {
	contract := mustExternalSendContract(t)
	eval := contract.Evaluate([]llm.MessageWithTools{
		{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "old-send", Name: "feishu_api", Arguments: json.RawMessage(`{"action":"send_message","chat_id":"oc_old","content":"old"}`)}}},
		{Role: "tool", ToolCallID: "old-send", ToolName: "feishu_api", Content: llm.NewTextContent(`{"ok":true}`)},
		{Role: "user", Content: llm.NewTextContent("给郭松发一下今天的天气信息")},
		{Role: "tool", ToolName: "websearch", Content: llm.NewTextContent("weather result")},
	}, llm.ChatWithToolsResponse{Content: "今天北京天气晴。", FinishReason: "stop"})

	if eval.Status != ContractIncomplete {
		t.Fatalf("previous-turn send must not satisfy current turn; eval=%+v", eval)
	}
	assertMissingRequirement(t, eval, MissingSendAttempt)
}

func TestIntentContract_ExternalSendBlockedAfterMessagingFailure(t *testing.T) {
	contract := mustExternalSendContract(t)
	eval := contract.Evaluate([]llm.MessageWithTools{
		{Role: "user", Content: llm.NewTextContent("给郭松发一下今天的天气信息")},
		{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "send-1", Name: "feishu_api", Arguments: json.RawMessage(`{"action":"send_message","chat_id":"oc_1","content":"天气"}`)}}},
		{Role: "tool", ToolCallID: "send-1", ToolName: "feishu_api", IsError: true, Content: llm.NewTextContent("permission denied")},
	}, llm.ChatWithToolsResponse{Content: "发送失败。", FinishReason: "stop"})

	if eval.Status != ContractBlocked {
		t.Fatalf("Status = %q, want blocked; eval=%+v", eval.Status, eval)
	}
	if eval.Reason != "external_send_tool_failed" {
		t.Fatalf("Reason = %q", eval.Reason)
	}
}

func TestIntentContract_ExternalSendWrongPlatformDoesNotSatisfy(t *testing.T) {
	intent := externalSendIntentForTest()
	intent.AllowedDomainsHint = []string{"wechatbot"}
	contract := IntentContract{Kind: IntentContractExternalSend, Intent: intent}

	eval := contract.Evaluate([]llm.MessageWithTools{
		{Role: "user", Content: llm.NewTextContent("给微信用户也发一条")},
		{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "send-1", Name: "feishu_api", Arguments: json.RawMessage(`{"action":"send_message","open_id":"ou_1","content":"hi"}`)}}},
		{Role: "tool", ToolCallID: "send-1", ToolName: "feishu_api", Content: llm.NewTextContent("消息已发送到 ou_1")},
	}, llm.ChatWithToolsResponse{Content: "已发送", FinishReason: "stop"})

	if eval.Status == ContractSatisfied {
		t.Fatalf("feishu send must not satisfy wechatbot send intent: %+v", eval)
	}
	if eval.Reason != "external_send_wrong_platform" {
		t.Fatalf("Reason = %q, want external_send_wrong_platform; eval=%+v", eval.Reason, eval)
	}
	if !eval.Evidence.WrongPlatform || eval.Evidence.SendPlatform != "feishu" {
		t.Fatalf("wrong-platform evidence missing: %+v", eval.Evidence)
	}
}

func TestIntentContract_ExternalSendSamePlatformSatisfiedWithIMAPI(t *testing.T) {
	intent := externalSendIntentForTest()
	intent.AllowedDomainsHint = []string{"wechatbot"}
	contract := IntentContract{Kind: IntentContractExternalSend, Intent: intent}

	eval := contract.Evaluate([]llm.MessageWithTools{
		{Role: "user", Content: llm.NewTextContent("给微信用户发 hello")},
		{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "send-1", Name: "im_api", Arguments: json.RawMessage(`{"action":"send_message","platform":"wechatbot","conversation_id":"wxid_peer","content":"hello"}`)}}},
		{Role: "tool", ToolCallID: "send-1", ToolName: "im_api", Content: llm.NewTextContent(`{"platform":"wechatbot","target_id":"wxid_peer","delivered":true}`)},
	}, llm.ChatWithToolsResponse{Content: "已发送", FinishReason: "stop"})

	if eval.Status != ContractSatisfied {
		t.Fatalf("same-platform im_api send should satisfy intent: %+v", eval)
	}
}

func TestIntentContract_ExternalSendNoWechatRecipientNeedsUser(t *testing.T) {
	intent := externalSendIntentForTest()
	intent.AllowedDomainsHint = []string{"wechatbot"}
	contract := IntentContract{Kind: IntentContractExternalSend, Intent: intent}

	eval := contract.Evaluate([]llm.MessageWithTools{
		{Role: "user", Content: llm.NewTextContent("给微信用户也发一条")},
		{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "list-1", Name: "im_api", Arguments: json.RawMessage(`{"action":"list_recent_conversations","platform":"wechatbot"}`)}}},
		{Role: "tool", ToolCallID: "list-1", ToolName: "im_api", Content: llm.NewTextContent(`[]`)},
	}, llm.ChatWithToolsResponse{Content: "当前没有可发送微信会话", FinishReason: "stop"})

	if eval.Status != ContractNeedsUser {
		t.Fatalf("empty wechat conversations should need user, got %+v", eval)
	}
	if eval.Reason != "external_send_no_sendable_recipient" {
		t.Fatalf("Reason = %q", eval.Reason)
	}
	if !eval.Evidence.NoSendableRecipient {
		t.Fatalf("NoSendableRecipient evidence missing: %+v", eval.Evidence)
	}
}

func TestIntentContract_ExternalSendWechatRecentConversationsRequireQuestion(t *testing.T) {
	intent := externalSendIntentForTest()
	intent.AllowedDomainsHint = []string{"wechatbot"}
	contract := IntentContract{Kind: IntentContractExternalSend, Intent: intent}

	eval := contract.Evaluate([]llm.MessageWithTools{
		{Role: "user", Content: llm.NewTextContent("给微信用户也发一条")},
		{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "list-1", Name: "im_api", Arguments: json.RawMessage(`{"action":"list_recent_conversations","platform":"wechatbot"}`)}}},
		{Role: "tool", ToolCallID: "list-1", ToolName: "im_api", Content: llm.NewTextContent(`[{"conversation_id":"wxid_1","name":"张三"},{"conversation_id":"wxid_2","name":"张三工作号"}]`)},
	}, llm.ChatWithToolsResponse{Content: "找到了两个微信会话", FinishReason: "stop"})

	if eval.Status != ContractIncomplete {
		t.Fatalf("ambiguous wechat conversations without question should be incomplete, got %+v", eval)
	}
	if eval.Reason != "external_send_ambiguous_recipient_without_question" {
		t.Fatalf("Reason = %q", eval.Reason)
	}
	assertMissingRequirement(t, eval, MissingQuestion)
	if !eval.Evidence.RecipientAmbiguous || eval.Evidence.QuestionAsked {
		t.Fatalf("ambiguous wechat evidence mismatch: %+v", eval.Evidence)
	}
}

func TestIntentContract_ExternalSendWechatRecentConversationsWithQuestionNeedsUser(t *testing.T) {
	intent := externalSendIntentForTest()
	intent.AllowedDomainsHint = []string{"wechatbot"}
	contract := IntentContract{Kind: IntentContractExternalSend, Intent: intent}

	eval := contract.Evaluate([]llm.MessageWithTools{
		{Role: "user", Content: llm.NewTextContent("给微信用户也发一条")},
		{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "list-1", Name: "im_api", Arguments: json.RawMessage(`{"action":"list_recent_conversations","platform":"wechatbot"}`)}}},
		{Role: "tool", ToolCallID: "list-1", ToolName: "im_api", Content: llm.NewTextContent(`[{"conversation_id":"wxid_1","name":"张三"},{"conversation_id":"wxid_2","name":"张三工作号"}]`)},
		{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "question-1", Name: "question", Arguments: json.RawMessage(`{"question":"请选择要发送的微信会话","options":["wxid_1","wxid_2"]}`)}}},
	}, llm.ChatWithToolsResponse{FinishReason: "tool_calls"})

	if eval.Status != ContractNeedsUser {
		t.Fatalf("ambiguous wechat conversations with question should need user, got %+v", eval)
	}
	if !eval.Evidence.RecipientAmbiguous || !eval.Evidence.QuestionAsked {
		t.Fatalf("ambiguous wechat question evidence mismatch: %+v", eval.Evidence)
	}
}

func TestIntentContract_ExternalSendPlatformEvidenceAggregatesAcrossTools(t *testing.T) {
	intent := externalSendIntentForTest()
	intent.AllowedDomainsHint = []string{"feishu"}
	contract := IntentContract{Kind: IntentContractExternalSend, Intent: intent}

	eval := contract.Evaluate([]llm.MessageWithTools{
		{Role: "user", Content: llm.NewTextContent("给飞书用户郭松发消息")},
		{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "bad-send", Name: "im_api", Arguments: json.RawMessage(`{"action":"send_message","platform":"feishu","recipient_id":"u_1","content":"hi"}`)}}},
		{Role: "tool", ToolCallID: "bad-send", ToolName: "im_api", IsError: true, Content: llm.NewTextContent("invalid receive_id")},
		{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "good-send", Name: "feishu_api", Arguments: json.RawMessage(`{"action":"send_message","open_id":"ou_1","content":"hi"}`)}}},
		{Role: "tool", ToolCallID: "good-send", ToolName: "feishu_api", Content: llm.NewTextContent("消息已发送到 ou_1")},
	}, llm.ChatWithToolsResponse{Content: "已发送", FinishReason: "stop"})

	if eval.Status != ContractSatisfied {
		t.Fatalf("same-platform success from legacy tool should satisfy after im_api failure: %+v", eval)
	}
}

func assertMissingRequirement(t *testing.T, eval ContractEvaluation, want MissingRequirement) {
	t.Helper()
	for _, got := range eval.Missing {
		if got == want {
			return
		}
	}
	t.Fatalf("missing requirement %q not present in %+v", want, eval.Missing)
}

func assertNotMissingRequirement(t *testing.T, eval ContractEvaluation, want MissingRequirement) {
	t.Helper()
	for _, got := range eval.Missing {
		if got == want {
			t.Fatalf("missing requirement %q should not be present in %+v", want, eval.Missing)
		}
	}
}
