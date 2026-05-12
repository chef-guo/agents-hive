package master

import (
	"strings"
	"unicode/utf8"

	"github.com/chef-guo/agents-hive/internal/llm"
	"github.com/chef-guo/agents-hive/internal/router"
)

func resolveTurnIntent(session *SessionState, query string, classified router.IntentFrame) router.IntentFrame {
	classified = normalizeRouteIntent(classified)
	if isStructuredExternalSendIntent(classified) {
		return classified
	}
	if isExplicitExternalSendRequest(query) {
		return externalSendIntentFromQuery(query, "explicit_external_send_rule")
	}
	if recovered, ok := recoverExplicitSideEffectIntent(query, classified); ok {
		return recovered
	}
	if pending, ok := session.PendingExternalSendIntent(); ok && isExternalSendContinuation(query) {
		pending.Signals = appendSignalForToolVisibility(pending.Signals, "pending_external_send_continuation")
		if strings.TrimSpace(pending.Subject) == "" {
			pending.Subject = truncateRunes(strings.TrimSpace(query), 120)
		}
		return pending
	}
	if recovered, ok := recoverRecentExternalSendContinuation(session, query); ok {
		return recovered
	}
	return classified
}

func recoverExplicitSideEffectIntent(query string, classified router.IntentFrame) (router.IntentFrame, bool) {
	if classified.AllowsSideEffects || classified.Kind == router.IntentExternalWrite {
		return router.IntentFrame{}, false
	}
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return router.IntentFrame{}, false
	}
	switch {
	case isExplicitManageToolRequest(q):
		return sideEffectIntentFromQuery(router.IntentManageTool, query, "explicit_manage_tool_rule", true), true
	case isExplicitModifySkillRequest(q):
		return sideEffectIntentFromQuery(router.IntentModifySkill, query, "explicit_modify_skill_rule", false), true
	case isExplicitCreateSkillRequest(q):
		return sideEffectIntentFromQuery(router.IntentCreateSkill, query, "explicit_create_skill_rule", false), true
	default:
		return router.IntentFrame{}, false
	}
}

func sideEffectIntentFromQuery(kind router.IntentKind, query, signal string, requiresExternal bool) router.IntentFrame {
	return router.IntentFrame{
		Kind:              kind,
		Subject:           truncateRunes(strings.TrimSpace(query), 120),
		RequiresExternal:  requiresExternal,
		AllowsSideEffects: true,
		Confidence:        0.82,
		Signals:           []string{signal},
	}
}

func externalSendIntentFromQuery(query, signal string) router.IntentFrame {
	hints, multiPlatform := externalSendAllowedDomainsHint(query)
	signals := []string{signal}
	if multiPlatform {
		signals = appendSignalForToolVisibility(signals, "external_send_multi_platform_requires_question")
		hints = nil
	}
	return router.IntentFrame{
		Kind:               router.IntentExternalWrite,
		Subject:            truncateRunes(strings.TrimSpace(query), 120),
		RequiresExternal:   true,
		AllowsSideEffects:  true,
		Confidence:         0.86,
		Signals:            signals,
		AllowedDomainsHint: hints,
	}
}

func externalSendAllowedDomainsHint(query string) ([]string, bool) {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return nil, false
	}
	hits := make([]string, 0, 4)
	add := func(platform string) {
		for _, existing := range hits {
			if existing == platform {
				return
			}
		}
		hits = append(hits, platform)
	}
	if containsAny(q, "飞书", "feishu", "lark") {
		add("feishu")
	}
	hasWeCom := containsAny(q, "企业微信", "企微", "wecom", "weixin work", "wechat work")
	if hasWeCom {
		add("wecom")
	}
	if containsAny(q, "钉钉", "dingtalk", "ding talk") {
		add("dingtalk")
	}
	if (!hasWeCom && containsAny(q, "个人微信", "微信用户", "微信好友", "微信联系人", "微信里", "微信上", "wechatbot")) ||
		(containsAny(q, "微信", "wechat") && !hasWeCom) ||
		(containsAny(q, "和微信", "跟微信", "及微信", "与微信", "、微信", "/微信") && hasWeCom) {
		add("wechatbot")
	}
	if len(hits) != 1 {
		return nil, len(hits) > 1
	}
	return hits, false
}

func isExplicitExternalSendRequest(query string) bool {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" || hasExternalSendNegation(q) {
		return false
	}
	if hasBrainstormSendFalsePositive(q) {
		return false
	}
	return containsAny(q, explicitExternalSendPatterns...) || hasChineseNamedRecipientSendPattern(q)
}

func hasChineseNamedRecipientSendPattern(q string) bool {
	runes := []rune(q)
	for i, r := range runes {
		if r != '给' {
			continue
		}
		maxEnd := i + 25
		if maxEnd > len(runes) {
			maxEnd = len(runes)
		}
		for j := i + 2; j < maxEnd; j++ {
			if runes[j] != '发' {
				continue
			}
			if isExternalSendRecipient(string(runes[i+1 : j])) {
				return true
			}
		}
	}
	return false
}

func isExternalSendRecipient(recipient string) bool {
	recipient = strings.TrimSpace(recipient)
	if utf8.RuneCountInString(recipient) < 2 {
		return false
	}
	for _, prefix := range []string{"我", "你", "自己", "我们", "你们", "咱"} {
		if strings.HasPrefix(recipient, prefix) {
			return false
		}
	}
	return true
}

func recoverRecentExternalSendContinuation(session *SessionState, query string) (router.IntentFrame, bool) {
	if session == nil || !isExternalSendContinuation(query) {
		return router.IntentFrame{}, false
	}
	session.mu.RLock()
	messages := append([]llm.MessageWithTools(nil), session.Messages...)
	session.mu.RUnlock()
	if len(messages) == 0 {
		return router.IntentFrame{}, false
	}

	currentQuery := strings.TrimSpace(query)
	skippedCurrentUser := false
	const maxLookback = 30
	seen := 0
	for i := len(messages) - 1; i >= 0 && seen < maxLookback; i-- {
		seen++
		msg := messages[i]
		if msg.Role != "user" {
			continue
		}
		msgText := strings.TrimSpace(msg.Content.Text())
		if !skippedCurrentUser && msgText == currentQuery {
			skippedCurrentUser = true
			continue
		}
		if !isExplicitExternalSendRequest(msgText) {
			continue
		}
		if hasSuccessfulExternalSend(messages[i+1:]) {
			return router.IntentFrame{}, false
		}
		intent := externalSendIntentFromQuery(msgText, "recent_external_send_continuation")
		return intent, true
	}
	return router.IntentFrame{}, false
}

func hasSuccessfulExternalSend(messages []llm.MessageWithTools) bool {
	validSendCalls := map[string]bool{}
	validSendWithoutID := false
	for _, msg := range messages {
		for _, call := range msg.ToolCalls {
			fact := classifyExternalSendToolCall(call)
			if fact.Kind != externalSendCallSend || !sendCallHasContentAndRecipient(call) {
				continue
			}
			if call.ID == "" {
				validSendWithoutID = true
				continue
			}
			validSendCalls[call.ID] = true
		}
		if msg.Role != "tool" || msg.IsError {
			continue
		}
		if validSendCalls[msg.ToolCallID] || (msg.ToolCallID == "" && validSendWithoutID && isExternalMessagingTool(msg.ToolName)) {
			return true
		}
	}
	return false
}

func isExternalSendContinuation(query string) bool {
	q := strings.ToLower(stripPunctSpace(strings.TrimSpace(query)))
	if q == "" || hasExternalSendNegation(q) || utf8.RuneCountInString(q) > 18 {
		return false
	}
	switch q {
	case "现在能不能发", "现在可以发吗", "现在可以发么", "现在能发吗", "现在能发么",
		"能不能发", "能发吗", "能发么", "可以发吗", "可以发么", "发吧",
		"现在发", "直接发", "可以发送", "继续发", "发一下", "那就发",
		"可以了发吧", "可以了", "继续":
		return true
	default:
		return strings.HasPrefix(q, "现在") && strings.Contains(q, "发")
	}
}

func hasExternalSendNegation(q string) bool {
	return containsAny(q,
		"不要发送", "别发送", "不用发送", "先别发", "不要发", "别发",
		"只是写", "只写", "只生成", "不要真的发", "别真的发",
		"don't send", "do not send",
	)
}

func hasBrainstormSendFalsePositive(q string) bool {
	return containsAny(q, "发散一下", "发散思路", "发散下", "发一下散")
}

func containsAny(s string, patterns ...string) bool {
	for _, pattern := range patterns {
		if pattern != "" && strings.Contains(s, pattern) {
			return true
		}
	}
	return false
}

var explicitExternalSendPatterns = []string{
	"发送给", "发给", "发到", "发送到", "转发给", "转给", "推送到", "通知到",
	"给郭松发", "给对方发", "给客户发", "给用户发", "给群里发",
	"send this to", "send to", "send it to", "forward this to", "forward to",
}

func isExplicitCreateSkillRequest(q string) bool {
	return containsAny(q,
		"创建一个skill", "创建一个 skill", "创建 skill", "创建一个技能", "创建技能",
		"新增一个skill", "新增一个 skill", "新增 skill", "新增技能",
		"写一个skill", "写一个 skill", "做一个skill", "做一个 skill",
		"create a skill", "create skill", "build a skill", "new skill",
	) || (containsAny(q, "创建", "新增", "写一个", "做一个", "create", "build", "new") && containsAny(q, "技能", "skill"))
}

func isExplicitModifySkillRequest(q string) bool {
	return containsAny(q,
		"修改skill", "修改 skill", "更新skill", "更新 skill", "优化skill", "优化 skill",
		"修改技能", "更新技能", "优化技能",
		"edit skill", "modify skill", "update skill", "improve skill",
	)
}

func isExplicitManageToolRequest(q string) bool {
	if containsAny(q, "不要创建mcp", "不要创建 mcp", "不是创建mcp", "不是创建 mcp") {
		return false
	}
	return containsAny(q,
		"创建mcp server", "创建 mcp server", "创建mcp工具", "创建 mcp 工具",
		"接入mcp", "接入 mcp", "注册工具", "创建工具", "新增工具",
		"create mcp server", "build mcp server", "create tool", "register tool",
	)
}
