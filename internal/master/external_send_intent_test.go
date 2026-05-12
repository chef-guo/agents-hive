package master

import (
	"encoding/json"
	"testing"

	"github.com/chef-guo/agents-hive/internal/llm"
	"github.com/chef-guo/agents-hive/internal/router"
)

func TestResolveTurnIntent_DetectsExplicitExternalSendRequest(t *testing.T) {
	intent := resolveTurnIntent(&SessionState{ID: "s1"}, "给郭松发一下今天的天气信息", router.IntentFrame{Kind: router.IntentAnswer})

	if !isStructuredExternalSendIntent(intent) {
		t.Fatalf("explicit send request should become external-send intent: %+v", intent)
	}
}

func TestExternalSendIntentFromQuery_PlatformHints(t *testing.T) {
	cases := []struct {
		name string
		q    string
		want []string
	}{
		{name: "feishu", q: "给飞书用户郭松发一条消息", want: []string{"feishu"}},
		{name: "wechatbot", q: "给微信用户也发一条", want: []string{"wechatbot"}},
		{name: "wecom", q: "给企微用户张三发通知", want: []string{"wecom"}},
		{name: "dingtalk", q: "给钉钉群发一下", want: []string{"dingtalk"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			intent := externalSendIntentFromQuery(tc.q, "test_signal")
			if !equalStringSlices(intent.AllowedDomainsHint, tc.want) {
				t.Fatalf("AllowedDomainsHint = %#v, want %#v; intent=%+v", intent.AllowedDomainsHint, tc.want, intent)
			}
			if hasString(intent.Signals, "external_send_multi_platform_requires_question") {
				t.Fatalf("single-platform query should not require multi-platform question: %+v", intent)
			}
		})
	}
}

func TestExternalSendIntentFromQuery_WeComDoesNotImplyWechatBot(t *testing.T) {
	intent := externalSendIntentFromQuery("给企业微信用户张三发通知", "test_signal")

	if !equalStringSlices(intent.AllowedDomainsHint, []string{"wecom"}) {
		t.Fatalf("企业微信/企微 must map only to wecom, got %#v", intent.AllowedDomainsHint)
	}
}

func TestExternalSendIntentFromQuery_MultiPlatformRequiresQuestion(t *testing.T) {
	intent := externalSendIntentFromQuery("飞书和微信都发一遍", "test_signal")

	if len(intent.AllowedDomainsHint) != 0 {
		t.Fatalf("multi-platform send must not directly authorize domains, got %#v", intent.AllowedDomainsHint)
	}
	if !hasString(intent.Signals, "external_send_multi_platform_requires_question") {
		t.Fatalf("multi-platform send should require question signal: %+v", intent)
	}
}

func TestResolveTurnIntent_ContinuationRequiresPendingExternalSend(t *testing.T) {
	session := &SessionState{ID: "s1"}
	withoutPending := resolveTurnIntent(session, "现在能不能发", router.IntentFrame{Kind: router.IntentAnswer})
	if isStructuredExternalSendIntent(withoutPending) {
		t.Fatalf("continuation without pending external-send must not expose send intent: %+v", withoutPending)
	}

	session.RememberPendingExternalSendIntent(router.IntentFrame{
		Kind:              router.IntentExternalWrite,
		RequiresExternal:  true,
		AllowsSideEffects: true,
		Subject:           "给郭松发天气",
	})
	withPending := resolveTurnIntent(session, "现在能不能发", router.IntentFrame{Kind: router.IntentAnswer})
	if !isStructuredExternalSendIntent(withPending) {
		t.Fatalf("continuation with pending external-send should inherit send intent: %+v", withPending)
	}
}

func TestResolveTurnIntent_DoesNotTreatBrainstormAsExternalSend(t *testing.T) {
	intent := resolveTurnIntent(&SessionState{ID: "s1"}, "给我发散一下思路", router.IntentFrame{Kind: router.IntentAnswer})
	if isStructuredExternalSendIntent(intent) {
		t.Fatalf("brainstorm wording must not become external-send intent: %+v", intent)
	}
}

func TestResolveTurnIntent_RecoversRecentUnfinishedExternalSendFromHistory(t *testing.T) {
	session := &SessionState{ID: "s1", Messages: []llm.MessageWithTools{
		{Role: "user", Content: llm.NewTextContent("给郭松发一下今天的天气信息")},
		{Role: "assistant", Content: llm.NewTextContent("我现在没法直接在飞书里发。")},
		{Role: "user", Content: llm.NewTextContent("现在能不能发")},
	}}

	intent := resolveTurnIntent(session, "现在能不能发", router.IntentFrame{Kind: router.IntentAnswer})
	if !isStructuredExternalSendIntent(intent) {
		t.Fatalf("recent unfinished send should be recovered as external-send: %+v", intent)
	}
}

func TestResolveTurnIntent_DoesNotRecoverAlreadySatisfiedExternalSend(t *testing.T) {
	session := &SessionState{ID: "s1", Messages: []llm.MessageWithTools{
		{Role: "user", Content: llm.NewTextContent("给郭松发一下今天的天气信息")},
		{Role: "assistant", ToolCalls: []llm.ToolCall{{
			ID:        "send-1",
			Name:      "feishu_api",
			Arguments: json.RawMessage(`{"action":"send_message","chat_id":"oc_1","content":"天气"}`),
		}}},
		{Role: "tool", ToolCallID: "send-1", ToolName: "feishu_api", Content: llm.NewTextContent(`{"ok":true}`)},
		{Role: "user", Content: llm.NewTextContent("现在能不能发")},
	}}

	intent := resolveTurnIntent(session, "现在能不能发", router.IntentFrame{Kind: router.IntentAnswer})
	if isStructuredExternalSendIntent(intent) {
		t.Fatalf("already satisfied send must not be recovered: %+v", intent)
	}
}

func TestResolveTurnIntent_RecoversExplicitSkillAndToolManagementIntents(t *testing.T) {
	cases := []struct {
		name string
		q    string
		want router.IntentKind
	}{
		{name: "create skill", q: "创建一个跟我打招呼的技能", want: router.IntentCreateSkill},
		{name: "modify skill", q: "修改 skill-creator 让它支持审核", want: router.IntentModifySkill},
		{name: "manage tool", q: "创建 MCP server 接入 GitHub API", want: router.IntentManageTool},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			intent := resolveTurnIntent(&SessionState{ID: "s1"}, tc.q, router.IntentFrame{Kind: router.IntentAnswer})
			if intent.Kind != tc.want || !intent.AllowsSideEffects {
				t.Fatalf("intent = %+v, want kind=%s with side effects", intent, tc.want)
			}
		})
	}
}

func TestResolveTurnIntent_DoesNotRecoverNegatedMCPManagement(t *testing.T) {
	intent := resolveTurnIntent(&SessionState{ID: "s1"}, "创建一个 skill，MCP 只是实现背景，不要创建 MCP server", router.IntentFrame{Kind: router.IntentAnswer})
	if intent.Kind == router.IntentManageTool {
		t.Fatalf("negated MCP wording must not become manage-tool intent: %+v", intent)
	}
}

func hasString(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}

func equalStringSlices(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
