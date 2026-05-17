package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode"

	"go.uber.org/zap"

	"github.com/chef-guo/agents-hive/internal/mcphost"
	"github.com/chef-guo/agents-hive/internal/router"
	"github.com/chef-guo/agents-hive/internal/toolruntime"
)

// toolSearchInput 是 tool_search 的入参。
// Query 对 name/description 做 substring case-insensitive 匹配；空 query 列出所有工具。
type toolSearchInput struct {
	Query string `json:"query,omitempty"`
	Limit int    `json:"limit,omitempty"`
}

type toolSearchHit struct {
	Name                 string                       `json:"name"`
	Description          string                       `json:"description,omitempty"`
	DangerLevel          string                       `json:"danger_level"`
	RequiresApproval     bool                         `json:"requires_approval"`
	MayRequireApproval   bool                         `json:"may_require_approval"`
	DangerousActions     []string                     `json:"dangerous_actions,omitempty"`
	ActionField          string                       `json:"action_field,omitempty"`
	ReadOnlyActions      []string                     `json:"read_only_actions,omitempty"`
	LocalWriteActions    []string                     `json:"local_write_actions,omitempty"`
	ExternalSendActions  []string                     `json:"external_send_actions,omitempty"`
	ExternalWriteActions []string                     `json:"external_write_actions,omitempty"`
	ActionCapabilities   []toolSearchActionCapability `json:"action_capabilities,omitempty"`
	IsConcurrencySafe    bool                         `json:"is_concurrency_safe"`
	Core                 bool                         `json:"core,omitempty"`
	Kind                 string                       `json:"kind"`
	Domain               string                       `json:"domain,omitempty"`
	Source               string                       `json:"source"`
	Risk                 string                       `json:"risk"`
	Visibility           string                       `json:"visibility"`
	Invocation           string                       `json:"invocation"`
	RouteStatus          string                       `json:"route_status"`
	CallableNow          bool                         `json:"callable_now"`
	ExecutionNote        string                       `json:"execution_note,omitempty"`
	Score                float64                      `json:"score"`
}

type toolSearchActionCapability struct {
	ToolName           string                                 `json:"tool_name"`
	Action             string                                 `json:"action"`
	ActionField        string                                 `json:"action_field,omitempty"`
	CapabilityID       string                                 `json:"capability_id"`
	Resource           string                                 `json:"resource,omitempty"`
	Operation          string                                 `json:"operation,omitempty"`
	RiskClass          string                                 `json:"risk_class,omitempty"`
	RequiredFields     []string                               `json:"required_fields,omitempty"`
	ParameterHints     []router.ActionCapabilityParameterHint `json:"parameter_hints,omitempty"`
	PreparatoryActions []string                               `json:"preparatory_actions,omitempty"`
	ExampleArgs        map[string]any                         `json:"example_args,omitempty"`
	ExampleToolCall    *toolSearchExampleToolCall             `json:"example_tool_call,omitempty"`
	InvocationHint     string                                 `json:"invocation_hint,omitempty"`
	RepairHint         string                                 `json:"repair_hint,omitempty"`
}

type toolSearchExampleToolCall struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments,omitempty"`
}

// ToolRecallHit 是工具目录召回结果，供 tool_search 和 Master 每轮候选召回复用。
type ToolRecallHit struct {
	Tool  mcphost.ToolDefinition
	Score float64
}

func registerToolSearch(host *mcphost.Host, logger *zap.Logger) {
	schema, _ := json.Marshal(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"query": map[string]any{
				"type":        "string",
				"description": "按工具 name/description 做不区分大小写的子串搜索；为空列出所有已注册工具",
			},
			"limit": map[string]any{
				"type":        "integer",
				"description": "返回 top N（按 score desc）；0 不限",
			},
		},
	})

	host.RegisterTool(
		mcphost.ToolDefinition{
			Name:              "tool_search",
			Description:       "搜索/列出当前已注册工具的名称、描述和可用安全元数据。仅用于 discovery，不授权执行；搜索结果不会让工具变成可调用。只读，不执行、不隐藏、不改变工具注册表。",
			InputSchema:       schema,
			Core:              true,
			IsConcurrencySafe: true,
		},
		func(ctx context.Context, raw json.RawMessage) (*mcphost.ToolResult, error) {
			return handleToolSearch(host, raw)
		},
	)
	if logger != nil {
		logger.Info("已注册 tool_search 工具")
	}
}

func handleToolSearch(host *mcphost.Host, raw json.RawMessage) (*mcphost.ToolResult, error) {
	var in toolSearchInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return errorResult("tool_search 输入无效: " + err.Error()), nil
	}

	qLower := strings.ToLower(strings.TrimSpace(in.Query))
	recalls := RecallToolCatalog(host.ListTools(), qLower, in.Limit)
	hits := make([]toolSearchHit, 0, len(recalls))
	for _, recall := range recalls {
		def := recall.Tool
		admission := toolruntime.Admit(toolruntime.DescriptorFromDefinition(def), toolSearchPolicyContext())
		profile := admission.Descriptor.Profile
		policy := admission.Policy
		kind, domain, source, risk, visibility, invocation, routeStatus, callableNow, executionNote := inferToolSearchMetadata(profile, policy, qLower)
		hits = append(hits, toolSearchHit{
			Name:                 def.Name,
			Description:          def.Description,
			DangerLevel:          inferToolDangerLevel(profile),
			RequiresApproval:     policy.RequiresApproval,
			MayRequireApproval:   policy.MayRequireApproval,
			DangerousActions:     router.StructuredDangerousActions(profile.Name),
			ActionField:          router.MixedActionField(profile.Name),
			ReadOnlyActions:      router.MixedReadOnlyActions(profile.Name),
			LocalWriteActions:    router.MixedLocalWriteActions(profile.Name),
			ExternalSendActions:  router.ExternalSendActions(profile.Name),
			ExternalWriteActions: router.ExternalWriteActions(profile.Name),
			ActionCapabilities:   toolSearchActionCapabilities(profile.Name, qLower),
			IsConcurrencySafe:    def.IsConcurrencySafe,
			Core:                 def.Core,
			Kind:                 kind,
			Domain:               domain,
			Source:               source,
			Risk:                 risk,
			Visibility:           visibility,
			Invocation:           invocation,
			RouteStatus:          routeStatus,
			CallableNow:          callableNow,
			ExecutionNote:        executionNote,
			Score:                recall.Score,
		})
	}

	out, _ := json.Marshal(map[string]any{
		"count":   len(hits),
		"results": hits,
	})
	return textResult(string(out)), nil
}

func inferToolDangerLevel(profile router.ToolProfile) string {
	if router.IsMixedReadWriteTool(profile.Name) {
		return "mixed"
	}
	if profile.OpenWorld || profile.Destructive || profile.Risk == router.RiskDestructive {
		return "dangerous"
	}
	switch profile.Risk {
	case router.RiskReadOnly:
		return "read_only"
	case router.RiskLocalWrite:
		return "local_write"
	case router.RiskExternalWrite:
		return "external_write"
	case router.RiskRuntimeExec:
		return "runtime_exec"
	case router.RiskUnknown:
		return "unknown"
	}
	return "unknown"
}

func toolSearchPolicyContext() router.ToolPolicyContext {
	return router.ToolPolicyContext{
		Intent:   router.IntentFrame{Kind: router.IntentRead},
		ForRoute: true,
	}
}

func inferToolSearchMetadata(profile router.ToolProfile, policy router.ToolPolicyDecision, queryLower string) (kind, domain, source, risk, visibility, invocation, routeStatus string, callableNow bool, executionNote string) {
	kind = string(profile.Kind)
	domain = profile.Domain
	source = string(profile.Source)
	risk = string(profile.Risk)
	visibility = profile.Visibility
	invocation = string(profile.Invocation)
	routeStatus, callableNow = inferToolRouteStatus(policy)
	executionNote = toolSearchExecutionNote(routeStatus, callableNow)
	return kind, domain, source, risk, visibility, invocation, routeStatus, callableNow, executionNote
}

func inferToolRouteStatus(_ router.ToolPolicyDecision) (string, bool) {
	return string(router.ToolRouteDiscoveryOnly), false
}

func toolSearchExecutionNote(routeStatus string, callableNow bool) string {
	switch routeStatus {
	case "discovery_only":
		return "tool_search 只返回工具目录和调用契约，不授权执行；是否可调用只由本轮 RouteDecision、可见工具列表、plan mode 与权限审批决定。"
	case "callable_read_only":
		return "只读安全工具；召回进入本轮候选后通常可直接调用，仍受 RouteDecision、plan mode 和运行时 allow-list 约束。"
	case "callable_with_action_constraints":
		return "混合工具；只读动作可直接调用，写入/发送动作必须匹配用户意图并可能需要审批。"
	case "requires_side_effect_intent":
		return "有副作用工具；只有用户明确要求写入/发送等动作时才会进入可调用路径，并由 ActionGuard 决定是否确认。"
	case "requires_matching_intent":
		return "能力入口工具；需要匹配对应意图后通过受限参数调用。"
	case "blocked_dangerous":
		return "危险或开放世界工具；不会因发现而直接可调用。"
	default:
		if callableNow {
			return "该工具可进入可调用路径，仍受 RouteDecision、plan mode 和运行时 allow-list 约束。"
		}
		return "当前仅作为目录信息展示；是否可调用由 RouteDecision、plan mode 和权限审批决定。"
	}
}

func toolSearchActionCapabilities(toolName, queryLower string) []toolSearchActionCapability {
	rules := router.ActionCapabilityRulesForTool(toolName)
	if len(rules) == 0 {
		return nil
	}
	queryLower = strings.ToLower(strings.TrimSpace(queryLower))
	actionField := router.MixedActionField(toolName)
	out := make([]toolSearchActionCapability, 0, len(rules))
	for _, rule := range rules {
		if queryLower != "" && !toolSearchActionCapabilityMatchesQuery(rule, queryLower) {
			continue
		}
		out = append(out, toolSearchActionCapabilityFromRule(rule, actionField))
	}
	if len(out) == 0 && queryLower != "" {
		for _, rule := range rules {
			out = append(out, toolSearchActionCapabilityFromRule(rule, actionField))
		}
	}
	return out
}

func toolSearchActionCapabilityFromRule(rule router.ActionCapabilityRule, actionField string) toolSearchActionCapability {
	return toolSearchActionCapability{
		ToolName:           rule.ToolName,
		Action:             rule.Action,
		ActionField:        actionField,
		CapabilityID:       rule.CapabilityID,
		Resource:           rule.Resource,
		Operation:          rule.Operation,
		RiskClass:          string(rule.RiskClass),
		RequiredFields:     append([]string(nil), rule.RequiredFields...),
		ParameterHints:     cloneToolSearchParameterHints(rule.ParameterHints),
		PreparatoryActions: append([]string(nil), rule.PreparatoryActions...),
		ExampleArgs:        cloneToolSearchExampleArgs(rule.ExampleArgs),
		ExampleToolCall:    toolSearchExampleToolCallForRule(rule, actionField),
		InvocationHint:     toolSearchInvocationHint(rule.ToolName, actionField, rule.Action),
		RepairHint:         rule.RepairHint,
	}
}

func toolSearchExampleToolCallForRule(rule router.ActionCapabilityRule, actionField string) *toolSearchExampleToolCall {
	toolName := strings.TrimSpace(rule.ToolName)
	if toolName == "" {
		return nil
	}
	args := cloneToolSearchExampleArgs(rule.ExampleArgs)
	if args == nil {
		args = make(map[string]any)
	}
	if actionField != "" && strings.TrimSpace(rule.Action) != "" {
		if _, ok := args[actionField]; !ok {
			args[actionField] = rule.Action
		}
	}
	return &toolSearchExampleToolCall{Name: toolName, Arguments: args}
}

func toolSearchInvocationHint(toolName, actionField, action string) string {
	toolName = strings.TrimSpace(toolName)
	actionField = strings.TrimSpace(actionField)
	action = strings.TrimSpace(action)
	if toolName == "" || action == "" {
		return ""
	}
	if actionField == "" {
		return fmt.Sprintf("调用工具 %s；不要把 action capability 当作独立工具名。", toolName)
	}
	return fmt.Sprintf("调用工具 %s，并在 arguments.%s 中设置 %s；不要调用 %s.%s，%s 不是独立工具名。", toolName, actionField, action, toolName, action, action)
}

func toolSearchActionCapabilityMatchesQuery(rule router.ActionCapabilityRule, queryLower string) bool {
	for _, candidate := range []string{rule.Action, rule.CapabilityID, rule.Resource, rule.Operation} {
		if candidate != "" && scoreToolSearchTerms(queryLower, []string{candidate}) > 0 {
			return true
		}
	}
	for _, alias := range rule.IntentAliases {
		for _, part := range strings.Split(alias, "|") {
			if scoreToolSearchTerms(queryLower, []string{part}) > 0 {
				return true
			}
		}
	}
	return false
}

func cloneToolSearchExampleArgs(in map[string]any) map[string]any {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]any, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func cloneToolSearchParameterHints(in []router.ActionCapabilityParameterHint) []router.ActionCapabilityParameterHint {
	if len(in) == 0 {
		return nil
	}
	out := make([]router.ActionCapabilityParameterHint, len(in))
	for i, hint := range in {
		hint.Enum = append([]string(nil), hint.Enum...)
		out[i] = hint
	}
	return out
}

// RecallToolCatalog 基于工具 name/description/schema 召回当前 query 相关工具。
// 它是只读纯函数，不执行工具、不改变注册表。
func RecallToolCatalog(catalog []mcphost.ToolDefinition, query string, limit int) []ToolRecallHit {
	if len(catalog) == 0 {
		return nil
	}
	qLower := strings.ToLower(strings.TrimSpace(query))
	hits := make([]ToolRecallHit, 0, len(catalog))
	for _, def := range catalog {
		schemaTerms := router.SanitizedSchemaTerms(def.InputSchema).Terms
		score := scoreToolSearchHit(qLower, def, schemaTerms)
		if score <= 0 {
			continue
		}
		hits = append(hits, ToolRecallHit{Tool: def, Score: score})
	}

	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].Score == hits[j].Score {
			return hits[i].Tool.Name < hits[j].Tool.Name
		}
		return hits[i].Score > hits[j].Score
	})
	if limit > 0 && len(hits) > limit {
		hits = hits[:limit]
	}
	return hits
}

func matchesToolSearchQuery(qLower string, def mcphost.ToolDefinition, schemaTerms []string) bool {
	return scoreToolSearchHit(qLower, def, schemaTerms) > 0
}

func scoreToolSearchHit(qLower string, def mcphost.ToolDefinition, schemaTerms []string) float64 {
	score := scoreHit(qLower, def.Name, def.Description, nil, false, 0)
	if qLower == "" {
		return score + scoreToolSearchTerms(qLower, schemaTerms) + scoreToolSearchIntentBoost(qLower, def.Name)
	}
	score += scoreToolSearchTerms(qLower, schemaTerms)
	for _, term := range toolSearchQueryTerms(qLower) {
		if term == qLower {
			continue
		}
		score += 0.65 * scoreHit(term, def.Name, def.Description, nil, false, 0)
		score += 0.65 * scoreToolSearchTerms(term, schemaTerms)
	}
	return score + scoreToolSearchIntentBoost(qLower, def.Name)
}

func matchesToolSearchTerms(qLower string, terms []string) bool {
	return scoreToolSearchTerms(qLower, terms) > 0
}

func scoreToolSearchTerms(qLower string, terms []string) float64 {
	if qLower == "" {
		return 0.5
	}
	normalizedQuery := normalizeToolSearchTerm(qLower)
	score := 0.0
	for _, term := range terms {
		termLower := strings.ToLower(strings.TrimSpace(term))
		if termLower == "" {
			continue
		}
		normalizedTerm := normalizeToolSearchTerm(termLower)
		switch {
		case termLower == qLower || normalizedTerm == normalizedQuery:
			score += 1.2
		case strings.Contains(termLower, qLower) || strings.Contains(normalizedTerm, normalizedQuery) || strings.Contains(normalizedQuery, normalizedTerm):
			score += 0.7
		}
		if score >= 2.4 {
			break
		}
	}
	return score
}

func scoreToolSearchIntentBoost(query, toolName string) float64 {
	normalizedQuery := normalizeToolSearchTerm(query)
	toolName = strings.TrimSpace(toolName)
	if normalizedQuery == "" || !queryMentionsKnowledgeBase(normalizedQuery) {
		return 0
	}
	switch toolName {
	case "kb.doc.meta":
		if queryMentionsKBSectionText(normalizedQuery) || queryMentionsKBStructure(normalizedQuery) {
			return 0.5
		}
		return 5
	case "kb.doc.structure":
		if queryMentionsKBStructure(normalizedQuery) {
			return 5
		}
	case "kb.section.text":
		if queryMentionsKBSectionText(normalizedQuery) {
			return 5
		}
	}
	return 0
}

func queryMentionsKnowledgeBase(normalized string) bool {
	for _, term := range []string{"kb", "知识库", "文档库", "namespace", "上传文档"} {
		if strings.Contains(normalized, normalizeToolSearchTerm(term)) {
			return true
		}
	}
	return false
}

func queryMentionsKBStructure(normalized string) bool {
	for _, term := range []string{"目录", "结构", "tree", "structure"} {
		if strings.Contains(normalized, normalizeToolSearchTerm(term)) {
			return true
		}
	}
	return false
}

func queryMentionsKBSectionText(normalized string) bool {
	for _, term := range []string{"原文", "正文", "内容", "取证", "引用", "section", "text"} {
		if strings.Contains(normalized, normalizeToolSearchTerm(term)) {
			return true
		}
	}
	return false
}

var toolSearchTermSeparator = regexp.MustCompile(`[^a-z0-9\p{Han}]+`)

var toolSearchQueryAliases = map[string][]string{
	"kb":   {"知识库", "文档库", "namespace", "document", "doc", "meta"},
	"知识库":  {"kb", "文档库", "namespace", "document", "doc", "meta"},
	"文档库":  {"kb", "知识库", "namespace", "document", "doc", "meta"},
	"上传文档": {"kb", "知识库", "document", "doc", "meta"},
	"目录":   {"structure"},
	"结构":   {"structure"},
	"文档目录": {"kb", "知识库", "structure"},
	"原文":   {"section"},
	"取证":   {"section"},
	"发送":   {"send"},
	"发给":   {"send"},
	"消息":   {"message", "im"},
	"通知":   {"message", "notify"},
	"飞书":   {"feishu", "lark"},
	"lark": {"feishu"},
	"钉钉":   {"dingtalk"},
	"微信":   {"wechat"},
	"企微":   {"wecom"},
	"企业微信": {"wecom"},
	"用户":   {"user", "contact", "member"},
	"联系人":  {"contact", "user"},
	"群聊":   {"chat", "group"},
	"群组":   {"chat", "group"},
	"聊天":   {"chat"},
}

var toolSearchQueryStopwords = map[string]bool{
	"a": true, "an": true, "and": true, "for": true, "of": true, "the": true, "to": true,
}

func appendToolSearchTermVariants(terms *[]string, raw string) {
	term := strings.TrimSpace(strings.ToLower(raw))
	if term == "" {
		return
	}
	*terms = append(*terms, term)

	spaced := strings.TrimSpace(toolSearchTermSeparator.ReplaceAllString(term, " "))
	if spaced != "" && spaced != term {
		*terms = append(*terms, spaced)
	}

	compact := normalizeToolSearchTerm(term)
	if compact != "" && compact != term && compact != spaced {
		*terms = append(*terms, compact)
	}
}

func normalizeToolSearchTerm(s string) string {
	return toolSearchTermSeparator.ReplaceAllString(strings.ToLower(strings.TrimSpace(s)), "")
}

func toolSearchQueryTerms(query string) []string {
	query = strings.TrimSpace(strings.ToLower(query))
	if query == "" {
		return nil
	}

	seen := make(map[string]struct{})
	terms := make([]string, 0, 12)
	var add func(string)
	add = func(raw string) {
		raw = strings.TrimSpace(strings.ToLower(raw))
		if !isUsefulToolSearchQueryTerm(raw) {
			return
		}
		var variants []string
		appendToolSearchTermVariants(&variants, raw)
		for _, variant := range variants {
			if !isUsefulToolSearchQueryTerm(variant) {
				continue
			}
			if _, ok := seen[variant]; ok {
				continue
			}
			seen[variant] = struct{}{}
			terms = append(terms, variant)
			for _, alias := range toolSearchQueryAliases[normalizeToolSearchTerm(variant)] {
				add(alias)
			}
		}
	}

	for _, term := range strings.Fields(toolSearchTermSeparator.ReplaceAllString(query, " ")) {
		add(term)
	}
	for _, term := range toolSearchHanNGrams(query) {
		add(term)
	}
	return terms
}

func isUsefulToolSearchQueryTerm(term string) bool {
	term = strings.TrimSpace(strings.ToLower(term))
	if term == "" {
		return false
	}
	normalized := normalizeToolSearchTerm(term)
	if normalized == "" || toolSearchQueryStopwords[normalized] {
		return false
	}
	return len([]rune(normalized)) > 1
}

func toolSearchHanNGrams(s string) []string {
	var out []string
	var seq []rune
	flush := func() {
		if len(seq) < 2 {
			seq = seq[:0]
			return
		}
		for n := 2; n <= 3; n++ {
			if len(seq) < n {
				continue
			}
			for i := 0; i+n <= len(seq); i++ {
				out = append(out, string(seq[i:i+n]))
			}
		}
		seq = seq[:0]
	}

	for _, r := range s {
		if unicode.Is(unicode.Han, r) {
			seq = append(seq, r)
			continue
		}
		flush()
	}
	flush()
	return out
}
