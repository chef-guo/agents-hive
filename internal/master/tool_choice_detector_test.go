package master

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/chef-guo/agents-hive/internal/config"
	"github.com/chef-guo/agents-hive/internal/imctx"
	"github.com/chef-guo/agents-hive/internal/llm"
	"github.com/chef-guo/agents-hive/internal/mcphost"
	"github.com/chef-guo/agents-hive/internal/router"
)

func TestDetectToolChoice_Required(t *testing.T) {
	// 必须触发 required 的 10 条（计划 §2 P0-A 验收集）
	cases := []string{
		// .skill 引用
		"女娲.skill 是什么",
		"女娲.skll 怎么用",
		"帮我看一下 openspec.skill 的定义",
		// URL / 仓库
		"https://example.com/女娲 是什么项目",
		"看看 github.com/chef-guo/agents-hive 的 README",
		// 文件路径
		"./internal/master/react_processor.go 里有什么",
		"/etc/nginx.conf 怎么配置",
		"internal/tools/websearch.go 的 parseSearchResults 实现",
		// "X 是什么" + 未知术语
		"LangGraph Checkpointer 是什么",
		"ReAct middleware pipeline 怎么用",
	}
	for _, q := range cases {
		t.Run(q, func(t *testing.T) {
			got := detectToolChoice(q, nil)
			if got != ToolChoiceRequired {
				t.Fatalf("want required, got %q for query %q", got, q)
			}
		})
	}
}

func TestDetectToolChoice_None(t *testing.T) {
	cases := []string{
		"你好",
		"您好",
		"谢谢",
		"多谢",
		"ok",
		"好的",
		"收到",
		"hi",
		"hello",
		"thanks",
	}
	for _, q := range cases {
		t.Run(q, func(t *testing.T) {
			got := detectToolChoice(q, nil)
			if got != ToolChoiceNone {
				t.Fatalf("want none, got %q for query %q", got, q)
			}
		})
	}
}

func TestDetectToolChoice_Auto(t *testing.T) {
	// 既不明确闲聊也无明显工具信号的正常对话
	cases := []string{
		"帮我写一首关于春天的诗",
		"总结一下我们刚才讨论的内容",
		"请继续",
		"给我一些关于创业的建议",
		"",
	}
	for _, q := range cases {
		t.Run(q, func(t *testing.T) {
			got := detectToolChoice(q, nil)
			if got != ToolChoiceAuto {
				t.Fatalf("want auto, got %q for query %q", got, q)
			}
		})
	}
}

func TestDetectToolChoice_WithIMReferencesForcesRequired(t *testing.T) {
	refs := []imctx.DocRef{
		{Type: imctx.RefDocx, Token: "docx_123"},
	}
	got := detectToolChoiceWithContext("分析一下这个文档", nil, refs)
	if got != ToolChoiceRequired {
		t.Fatalf("want required, got %q", got)
	}
}

func TestDetectToolChoice_UsesStructuredExternalSendIntent(t *testing.T) {
	intent := router.IntentFrame{Kind: router.IntentExternalWrite, AllowsSideEffects: true, RequiresExternal: true}
	if got := detectToolChoiceWithIntent("给郭松发一下今天的天气信息", nil, nil, intent); got != ToolChoiceRequired {
		t.Fatalf("structured external-send intent should require tools, got %q", got)
	}
	if got := detectToolChoiceWithIntent("给郭松发一下今天的天气信息", nil, nil, router.IntentFrame{Kind: router.IntentAnswer}); got != ToolChoiceAuto {
		t.Fatalf("answer intent should not require tools from send-like text alone, got %q", got)
	}
}

func TestDetectToolChoice_ExternalSendRelaxesAfterValidSendAttempt(t *testing.T) {
	intent := router.IntentFrame{Kind: router.IntentExternalWrite, AllowsSideEffects: true, RequiresExternal: true}
	messages := []llm.MessageWithTools{
		{Role: "user", Content: llm.NewTextContent("现在能不能发")},
		{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "send-1", Name: "feishu_api", Arguments: json.RawMessage(`{"action":"send_message","open_id":"ou_1","content":"天气"}`)}}},
		{Role: "tool", ToolCallID: "send-1", ToolName: "feishu_api", Content: llm.NewTextContent("消息已发送到 ou_1")},
	}
	got := detectToolChoiceWithIntentAndMessages("现在能不能发", nil, nil, intent, messages)
	if got == ToolChoiceRequired {
		t.Fatalf("external-send 已有有效发送证据后不应继续 required，got %q", got)
	}
	if trigger := toolChoiceRequiredTriggerWithMessages("现在能不能发", nil, nil, intent, messages); trigger != "auto" {
		t.Fatalf("trigger = %q, want auto", trigger)
	}
}

func TestDetectToolChoice_ExternalSendRelaxesOnlyAfterSamePlatformSend(t *testing.T) {
	intent := router.IntentFrame{
		Kind:               router.IntentExternalWrite,
		AllowsSideEffects:  true,
		RequiresExternal:   true,
		AllowedDomainsHint: []string{"wechatbot"},
	}
	messages := []llm.MessageWithTools{
		{Role: "user", Content: llm.NewTextContent("给微信用户也发一条")},
		{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "send-1", Name: "feishu_api", Arguments: json.RawMessage(`{"action":"send_message","open_id":"ou_1","content":"hi"}`)}}},
		{Role: "tool", ToolCallID: "send-1", ToolName: "feishu_api", Content: llm.NewTextContent("消息已发送到 ou_1")},
	}
	got := detectToolChoiceWithIntentAndMessages("给微信用户也发一条", nil, nil, intent, messages)
	if got != ToolChoiceRequired {
		t.Fatalf("wrong-platform send must keep required, got %q", got)
	}
}

func TestDetectToolChoice_ExternalSendStopsRequiredWhenNoSendableRecipient(t *testing.T) {
	intent := router.IntentFrame{
		Kind:               router.IntentExternalWrite,
		AllowsSideEffects:  true,
		RequiresExternal:   true,
		AllowedDomainsHint: []string{"wechatbot"},
	}
	messages := []llm.MessageWithTools{
		{Role: "user", Content: llm.NewTextContent("给微信用户也发一条")},
		{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "list-1", Name: "im_api", Arguments: json.RawMessage(`{"action":"list_recent_conversations","platform":"wechatbot"}`)}}},
		{Role: "tool", ToolCallID: "list-1", ToolName: "im_api", Content: llm.NewTextContent(`[]`)},
	}
	got := detectToolChoiceWithIntentAndMessages("给微信用户也发一条", nil, nil, intent, messages)
	if got == ToolChoiceRequired {
		t.Fatalf("no sendable recipient should stop required loop, got %q", got)
	}
}

func TestDetectToolChoice_ExternalSendRelaxesAfterMessagingFailure(t *testing.T) {
	intent := router.IntentFrame{Kind: router.IntentExternalWrite, AllowsSideEffects: true, RequiresExternal: true}
	messages := []llm.MessageWithTools{
		{Role: "user", Content: llm.NewTextContent("现在能不能发")},
		{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "send-1", Name: "feishu_api", Arguments: json.RawMessage(`{"action":"send_message","user_id":"afde2a69","content":"天气"}`)}}},
		{Role: "tool", ToolCallID: "send-1", ToolName: "feishu_api", IsError: true, Content: llm.NewTextContent("发送失败")},
	}
	got := detectToolChoiceWithIntentAndMessages("现在能不能发", nil, nil, intent, messages)
	if got == ToolChoiceRequired {
		t.Fatalf("external-send 发送失败后应允许模型直接报告失败/下一步，不应继续强制工具，got %q", got)
	}
}

func TestShouldEvaluateToolChoiceForTurn_StructuredExternalSendBypassesQualityGuardFlag(t *testing.T) {
	intent := router.IntentFrame{Kind: router.IntentExternalWrite, AllowsSideEffects: true, RequiresExternal: true}
	if !shouldEvaluateToolChoiceForTurn("给郭松发一下今天的天气信息", nil, config.QualityGuardsConfig{}, intent) {
		t.Fatal("structured external-send intent must evaluate tool choice even when ToolChoiceForce is disabled")
	}
	if !shouldEvaluateToolChoiceForTurn("分析一下这个文档", []imctx.DocRef{{URL: "https://example.com/doc"}}, config.QualityGuardsConfig{}, router.IntentFrame{}) {
		t.Fatal("IM refs must still evaluate tool choice")
	}
	if shouldEvaluateToolChoiceForTurn("给郭松发一下今天的天气信息", nil, config.QualityGuardsConfig{}, router.IntentFrame{Kind: router.IntentAnswer}) {
		t.Fatal("send-like text without structured external-send intent must not force tools")
	}
	if shouldEvaluateToolChoiceForTurn("LangGraph Checkpointer 是什么", nil, config.QualityGuardsConfig{}, router.IntentFrame{}) {
		t.Fatal("non-send broad heuristics should remain behind ToolChoiceForce")
	}
	if !shouldEvaluateToolChoiceForTurn("LangGraph Checkpointer 是什么", nil, config.QualityGuardsConfig{ToolChoiceForce: true}, router.IntentFrame{}) {
		t.Fatal("ToolChoiceForce still enables broad required heuristics")
	}
}

func TestRefsForToolChoice_IMRefsExpireAfterSuccessfulRead(t *testing.T) {
	imCtx := &imctx.IMMessageContext{
		References: []imctx.DocRef{
			{Type: imctx.RefWiki, Token: "wiki_123"},
		},
	}

	activeRefs := refsForToolChoice(imCtx, false)
	if len(activeRefs) != 1 {
		t.Fatalf("未读取前应保留 refs 强制首轮工具调用，got %d", len(activeRefs))
	}
	if got := detectToolChoiceWithContext("分析一下这个文档", nil, activeRefs); got != ToolChoiceRequired {
		t.Fatalf("未读取前 want required, got %q", got)
	}

	expiredRefs := refsForToolChoice(imCtx, true)
	if len(expiredRefs) != 0 {
		t.Fatalf("成功读取后 refs 应过期，got %d", len(expiredRefs))
	}
	if got := detectToolChoiceWithContext("还不行么", nil, expiredRefs); got != ToolChoiceAuto {
		t.Fatalf("成功读取后普通追问应回落 auto，got %q", got)
	}
}

func TestIsSuccessfulIMReferenceRead(t *testing.T) {
	cases := []struct {
		name string
		call llm.ToolCall
		want bool
	}{
		{
			name: "read_sheet success satisfies IM reference read",
			call: llm.ToolCall{
				Name:      "feishu_api",
				Arguments: json.RawMessage(`{"action":"read_sheet","spreadsheet_token":"sht_123","range":"A1:Z1000"}`),
			},
			want: true,
		},
		{
			name: "get_doc_content success satisfies IM reference read",
			call: llm.ToolCall{
				Name:      "feishu_api",
				Arguments: json.RawMessage(`{"action":"get_doc_content","document_id":"docx_123"}`),
			},
			want: true,
		},
		{
			name: "wiki_get_node only resolves metadata and does not satisfy content read",
			call: llm.ToolCall{
				Name:      "feishu_api",
				Arguments: json.RawMessage(`{"action":"wiki_get_node","node_token":"wiki_123"}`),
			},
			want: false,
		},
		{
			name: "non feishu tool does not satisfy IM reference read",
			call: llm.ToolCall{
				Name:      "websearch",
				Arguments: json.RawMessage(`{"query":"agents hive"}`),
			},
			want: false,
		},
		{
			name: "invalid args do not satisfy IM reference read",
			call: llm.ToolCall{
				Name:      "feishu_api",
				Arguments: json.RawMessage(`{bad`),
			},
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isSuccessfulIMReferenceRead(tc.call, false); got != tc.want {
				t.Fatalf("isSuccessfulIMReferenceRead() = %v, want %v", got, tc.want)
			}
			if got := isSuccessfulIMReferenceRead(tc.call, true); got {
				t.Fatalf("工具失败时不应满足 IM reference read")
			}
		})
	}
}

func TestBuildRequiredToolRetryMessage_WithFeishuRefs(t *testing.T) {
	refs := []imctx.DocRef{
		{Type: imctx.RefDocx, Token: "docx_123"},
		{Type: imctx.RefSheet, Token: "sheet_456"},
	}
	got := buildRequiredToolRetryMessage(refs)
	for _, want := range []string{
		"上一轮必须调用工具",
		"feishu_api",
		"docx_123",
		"sheet_456",
		"get_doc_content",
		"read_sheet",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("message missing %q: %s", want, got)
		}
	}
}

func TestBuildRequiredToolRetryMessage_WithoutRefs(t *testing.T) {
	got := buildRequiredToolRetryMessage(nil)
	if !strings.Contains(got, "websearch / skill / 文件读取") {
		t.Fatalf("unexpected fallback message: %s", got)
	}
}

func TestDetectToolChoice_SkillsIndexHit(t *testing.T) {
	// "X 是什么" 模式 + X 已在 skillsIndex 内 → 不强制 required
	idx := map[string]bool{
		"plan-ceo-review": true,
		"qa":              true,
	}
	got := detectToolChoice("plan-ceo-review 是什么", idx)
	if got != ToolChoiceAuto {
		t.Fatalf("want auto (skill known), got %q", got)
	}
	// 未在 index 内仍 required
	got2 := detectToolChoice("未知技能 是什么", idx)
	if got2 != ToolChoiceRequired {
		t.Fatalf("want required (skill unknown), got %q", got2)
	}
}

func TestIsChitchat_NotTrippedByMixedQuestion(t *testing.T) {
	// "你好，X 是什么" 不能被判成闲聊（有疑问词）
	if isChitchat("你好 女娲.skill 是什么") {
		t.Fatal("mixed greeting + question must not be chitchat")
	}
	if isChitchat("hi, what is LangGraph") {
		t.Fatal("mixed greeting + english question must not be chitchat")
	}
}

func TestToolChoiceRequiredTrigger(t *testing.T) {
	cases := []struct {
		name   string
		q      string
		refs   []imctx.DocRef
		intent router.IntentFrame
		want   string
	}{
		{name: "refs", q: "分析一下这个文档", refs: []imctx.DocRef{{URL: "https://example.com/doc"}}, want: "refs"},
		{name: "skill ref", q: "女娲.skill 是什么", want: "skill_ref"},
		{name: "url", q: "看看 https://example.com", want: "url"},
		{name: "file", q: "./internal/master/react_processor.go 里有什么", want: "file_path"},
		{name: "what is", q: "LangGraph Checkpointer 是什么", want: "what_is"},
		{name: "external send", q: "给郭松发一下今天的天气信息", intent: router.IntentFrame{Kind: router.IntentExternalWrite, AllowsSideEffects: true, RequiresExternal: true}, want: "external_send"},
		{name: "send-like text without structured intent", q: "给郭松发一下今天的天气信息", intent: router.IntentFrame{Kind: router.IntentAnswer}, want: "auto"},
		{name: "auto", q: "今天天气怎么样", want: "auto"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := toolChoiceRequiredTrigger(tc.q, nil, tc.refs, tc.intent); got != tc.want {
				t.Fatalf("trigger = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestHasRequiredIntentCallableTool(t *testing.T) {
	intent := router.IntentFrame{Kind: router.IntentExternalWrite, AllowsSideEffects: true, RequiresExternal: true}
	if !hasRequiredIntentCallableTool([]mcphost.ToolDefinition{{
		Name:        "feishu_api",
		Description: "飞书应用 API 工具。search_contacts send_message",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"action":{"type":"string","enum":["search_contacts","send_message"]}}}`),
	}}, intent) {
		t.Fatal("feishu_api should satisfy external-send required intent")
	}
	if hasRequiredIntentCallableTool([]mcphost.ToolDefinition{{Name: "tool_search", Core: true}}, intent) {
		t.Fatal("tool_search alone must not satisfy external-send required intent")
	}
}

// M6：去前缀启发式 —— "好的继续"、"收到请继续"、"ok 再来" 等指令类短句不应被判闲聊。
func TestIsChitchat_M6_NoPrefixHeuristic(t *testing.T) {
	cases := []string{
		"好的继续",
		"收到请继续",
		"ok再来",
		"好的帮我",
		"谢谢你帮我",
		"hi 再来一次",
	}
	for _, q := range cases {
		t.Run(q, func(t *testing.T) {
			if isChitchat(q) {
				t.Fatalf("prefix heuristic removed: %q 不应被判闲聊", q)
			}
		})
	}
}

// M7：whatIs 口语变体扩招 —— 是啥 / 干嘛的 / 咋用 / 怎么搞 等都应触发 required（未知术语）。
func TestDetectToolChoice_M7_Colloquial(t *testing.T) {
	cases := []string{
		"LangGraph 是啥",
		"Checkpointer 干嘛的",
		"ReAct pipeline 咋用",
		"Skill 到底是什么",
		"OpenSpec 是什么吗",
		"RAG 是干什么的",
		"ReAct 怎么搞",
		"what does LangGraph do",
		"tell me about OpenSpec",
	}
	for _, q := range cases {
		t.Run(q, func(t *testing.T) {
			got := detectToolChoice(q, nil)
			if got != ToolChoiceRequired {
				t.Fatalf("want required, got %q for query %q", got, q)
			}
		})
	}
}

// M7 Contains 兜底：非锚定句里含 "X 是什么" 也应抓到 X。
func TestExtractWhatIsTarget_M7_ContainsFallback(t *testing.T) {
	cases := map[string]string{
		"我想知道 LangGraph 是什么呢":   "LangGraph",
		"那 Checkpointer 到底怎么用啊": "Checkpointer",
	}
	for q, want := range cases {
		t.Run(q, func(t *testing.T) {
			got := extractWhatIsTarget(q)
			if got != want {
				t.Fatalf("extractWhatIsTarget(%q) = %q, want %q", q, got, want)
			}
		})
	}
}

// M7-MED：fallback 分支的 lastTokenBefore 结果必须再走 stripFillerPrefix，
// 否则中文口语化连写 "那女娲到底怎么用啊" 会抓成 "那女娲"，
// H3 skillsIndex 比对走裸名 "女娲" 时就命不中，等于绕过 whatIs→auto 回落。
func TestExtractWhatIsTarget_M7_FallbackStripFiller(t *testing.T) {
	cases := map[string]string{
		"那女娲到底怎么用啊":        "女娲",
		"那个 LangGraph 是啥呢": "LangGraph",
		"这个 OpenSpec 是什么":  "OpenSpec",
		"我想知道 Claude 是啥":   "Claude",
	}
	for q, want := range cases {
		t.Run(q, func(t *testing.T) {
			got := extractWhatIsTarget(q)
			if got != want {
				t.Fatalf("extractWhatIsTarget(%q) = %q, want %q", q, got, want)
			}
		})
	}
}

// M7-MED 端到端：query 含 filler 前缀 + X 已在 skillsIndex 时必须回落 auto。
// 修复前该 case 会抓到 "那女娲" / "那个 OpenSpec"，skillsIndex 查不到，错误停在 required。
func TestDetectToolChoice_M7_FallbackFillerHitsSkillsIndex(t *testing.T) {
	skillsIdx := map[string]bool{
		"女娲":        true,
		"langgraph": true,
		"openspec":  true,
	}
	cases := []string{
		"那女娲到底怎么用啊",
		"这个 OpenSpec 是什么呢",
		"我想知道 LangGraph 是啥",
	}
	for _, q := range cases {
		t.Run(q, func(t *testing.T) {
			got := detectToolChoice(q, skillsIdx)
			if got != ToolChoiceAuto {
				t.Fatalf("want auto（skill 已知应回落），got %q for query %q", got, q)
			}
		})
	}
}

// --- P0-A C2-HIGH-A / C2-HIGH-B: evaluateRequiredGuard 纯函数测试 ---

// C2-HIGH-B 锁死：required+0 无条件触发 guard，计数器递增。
// 修复前 guard 藏在 shouldExitTask 门内，length/empty finish_reason 会绕过。
// 这里直接用纯函数做表驱动，finish_reason 本来就不在参数里——拆纯函数的收益。
func TestEvaluateRequiredGuard_RequiredZeroCallsAlwaysFires(t *testing.T) {
	for _, breach := range []int{0, 1, 2} {
		t.Run(fmt.Sprintf("breach=%d", breach), func(t *testing.T) {
			action, next := evaluateRequiredGuard(ToolChoiceRequired, 0, breach)
			if breach == 0 {
				if action != requiredGuardRetry {
					t.Fatalf("breach=0 应 retry，得 %v", action)
				}
				if next != 1 {
					t.Fatalf("breach=0 的 nextBreach 应为 1，得 %d", next)
				}
				return
			}
			if action != requiredGuardFail {
				t.Fatalf("breach>=1 应 fail，得 %v", action)
			}
			if next != breach+1 {
				t.Fatalf("breach>=1 的 nextBreach 应为 %d，得 %d", breach+1, next)
			}
		})
	}
}

// C2-HIGH-A 锁死：只要模型产出 tool_calls，breach 计数立即归零。
// 修复前计数器永不重置，长任务中间偶发 required+0 会累计触发误杀。
func TestEvaluateRequiredGuard_ToolCallsResetsBreach(t *testing.T) {
	cases := []struct {
		name       string
		toolChoice string
		toolCalls  int
		inBreach   int
	}{
		{"required + 1 call, breach=5", ToolChoiceRequired, 1, 5},
		{"required + 3 calls, breach=0", ToolChoiceRequired, 3, 0},
		{"auto + 2 calls, breach=9", ToolChoiceAuto, 2, 9},
		{"none + 1 call, breach=3", ToolChoiceNone, 1, 3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			action, next := evaluateRequiredGuard(tc.toolChoice, tc.toolCalls, tc.inBreach)
			if action != requiredGuardPass {
				t.Fatalf("有 tool_calls 时应 pass，得 %v", action)
			}
			if next != 0 {
				t.Fatalf("有 tool_calls 时 breach 必须归零，得 %d（in=%d）", next, tc.inBreach)
			}
		})
	}
}

// 非 required + 0 tool_calls：不触发 guard，breach 保持原值传递给下一轮。
func TestEvaluateRequiredGuard_NonRequiredZeroCallsPreservesBreach(t *testing.T) {
	cases := []struct {
		name       string
		toolChoice string
	}{
		{"auto", ToolChoiceAuto},
		{"none", ToolChoiceNone},
		{"empty", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, breach := range []int{0, 1, 7} {
				action, next := evaluateRequiredGuard(tc.toolChoice, 0, breach)
				if action != requiredGuardPass {
					t.Fatalf("非 required 不应触发 guard，得 %v", action)
				}
				if next != breach {
					t.Fatalf("breach 应保持 %d，得 %d", breach, next)
				}
			}
		})
	}
}

// P0-A/C3 round-4 shouldSuppressStreamPartial 表驱动：
// tool_choice=required 时必须屏蔽流式 partial assistant，避免坏回答在 guard 判定前泄露到前端 UI。
// 其它值保持原流式体验。
func TestShouldSuppressStreamPartial(t *testing.T) {
	cases := []struct {
		name       string
		toolChoice string
		want       bool
	}{
		{"required → suppress", ToolChoiceRequired, true},
		{"auto → stream", ToolChoiceAuto, false},
		{"none → stream", ToolChoiceNone, false},
		{"empty (flag off) → stream", "", false},
		{"unknown value → stream", "weird", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := shouldSuppressStreamPartial(tc.toolChoice); got != tc.want {
				t.Fatalf("shouldSuppressStreamPartial(%q) = %v, want %v", tc.toolChoice, got, tc.want)
			}
		})
	}
}

// P0-A/C3 emitAssistantMessage 表驱动：
// - pass → true  (正常轮次，persist + broadcast)
// - retry → false (required+0 追责，丢 assistant 避免污染历史与 UI)
// - fail → false (连续 required+0，不能让坏回答在失败前先泄露给用户)
func TestEmitAssistantMessage(t *testing.T) {
	cases := []struct {
		name   string
		action requiredGuardAction
		want   bool
	}{
		{"pass → emit", requiredGuardPass, true},
		{"retry → suppress", requiredGuardRetry, false},
		{"fail → suppress", requiredGuardFail, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := emitAssistantMessage(tc.action); got != tc.want {
				t.Fatalf("emitAssistantMessage(%v) = %v, want %v", tc.action, got, tc.want)
			}
		})
	}
}

// P0-A/C3 组合回归：guard 分支必须与 emit 决策耦合一致。
// 修复前 evaluateRequiredGuard 结果只影响 continue/return，assistant message 的
// persist + broadcast 走独立路径，这里锁死"两条链路必须由同一 action 驱动"。
func TestEmitAssistantMessage_CoupledWithGuardAction(t *testing.T) {
	cases := []struct {
		name       string
		toolChoice string
		toolCalls  int
		breach     int
		wantEmit   bool
	}{
		{"auto + 1 call → emit", ToolChoiceAuto, 1, 0, true},
		{"required + 2 calls → emit", ToolChoiceRequired, 2, 0, true},
		{"required + 0 calls, breach=0 (retry) → suppress", ToolChoiceRequired, 0, 0, false},
		{"required + 0 calls, breach=1 (fail) → suppress", ToolChoiceRequired, 0, 1, false},
		{"none + 0 calls → emit", ToolChoiceNone, 0, 0, true},
		{"auto + 0 calls → emit (stop/end_turn 终态不归 guard 管)", ToolChoiceAuto, 0, 3, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			action, _ := evaluateRequiredGuard(tc.toolChoice, tc.toolCalls, tc.breach)
			got := emitAssistantMessage(action)
			if got != tc.wantEmit {
				t.Fatalf("emit=%v, want %v (action=%v)", got, tc.wantEmit, action)
			}
		})
	}
}

// 回归场景组合：模拟"长任务跨多轮 + 偶发 required+0"的路径，确保不会累计误杀。
// 轮次序列：required+0 → required+1 (用工具) → ... → required+0 应当 retry 而非 fail。
func TestEvaluateRequiredGuard_LongTaskIntermittentBreachNoFalseFail(t *testing.T) {
	breach := 0
	// 第 1 轮：required+0 → retry，breach=1
	action, breach := evaluateRequiredGuard(ToolChoiceRequired, 0, breach)
	if action != requiredGuardRetry || breach != 1 {
		t.Fatalf("轮1 期望 retry/1，得 %v/%d", action, breach)
	}
	// 第 2 轮：required+2 → pass，breach=0（模型在追责下终于出工具）
	action, breach = evaluateRequiredGuard(ToolChoiceRequired, 2, breach)
	if action != requiredGuardPass || breach != 0 {
		t.Fatalf("轮2 期望 pass/0，得 %v/%d", action, breach)
	}
	// 第 3-5 轮：正常 auto+工具，breach 保持 0
	action, breach = evaluateRequiredGuard(ToolChoiceAuto, 1, breach)
	if action != requiredGuardPass || breach != 0 {
		t.Fatalf("轮3 期望 pass/0，得 %v/%d", action, breach)
	}
	// 第 6 轮：又一次 required+0 → 必须 retry，不应 fail（连续性断过）
	action, breach = evaluateRequiredGuard(ToolChoiceRequired, 0, breach)
	if action != requiredGuardRetry {
		t.Fatalf("长任务中间偶发 required+0，应 retry 不应 fail，得 %v", action)
	}
}
