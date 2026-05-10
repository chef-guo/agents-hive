package tools

import (
	"context"
	"encoding/json"
	"regexp"
	"sort"
	"strings"
	"unicode"

	"go.uber.org/zap"

	"github.com/chef-guo/agents-hive/internal/mcphost"
	"github.com/chef-guo/agents-hive/internal/router"
)

// toolSearchInput 是 tool_search 的入参。
// Query 对 name/description 做 substring case-insensitive 匹配；空 query 列出所有工具。
type toolSearchInput struct {
	Query string `json:"query,omitempty"`
	Limit int    `json:"limit,omitempty"`
}

type toolSearchHit struct {
	Name              string  `json:"name"`
	Description       string  `json:"description,omitempty"`
	DangerLevel       string  `json:"danger_level"`
	RequiresApproval  bool    `json:"requires_approval"`
	IsConcurrencySafe bool    `json:"is_concurrency_safe"`
	Core              bool    `json:"core,omitempty"`
	Kind              string  `json:"kind"`
	Domain            string  `json:"domain,omitempty"`
	Source            string  `json:"source"`
	Invocation        string  `json:"invocation"`
	RouteStatus       string  `json:"route_status"`
	Score             float64 `json:"score"`
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
		profile := router.InferToolProfile(def, router.ProfileHint{})
		kind, domain, source, invocation, routeStatus := inferToolSearchMetadata(profile, qLower)
		hits = append(hits, toolSearchHit{
			Name:              def.Name,
			Description:       def.Description,
			DangerLevel:       inferToolDangerLevel(profile),
			RequiresApproval:  router.ProfileHasSideEffect(profile) && !def.IsConcurrencySafe,
			IsConcurrencySafe: def.IsConcurrencySafe,
			Core:              def.Core,
			Kind:              kind,
			Domain:            domain,
			Source:            source,
			Invocation:        invocation,
			RouteStatus:       routeStatus,
			Score:             recall.Score,
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

func inferToolSearchMetadata(profile router.ToolProfile, queryLower string) (kind, domain, source, invocation, routeStatus string) {
	kind = string(profile.Kind)
	domain = profile.Domain
	source = string(profile.Source)
	invocation = string(profile.Invocation)
	if queryLower == "" {
		routeStatus = "discoverable"
	} else {
		routeStatus = "recommended"
	}
	return kind, domain, source, invocation, routeStatus
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
		return score + scoreToolSearchTerms(qLower, schemaTerms)
	}
	score += scoreToolSearchTerms(qLower, schemaTerms)
	for _, term := range toolSearchQueryTerms(qLower) {
		if term == qLower {
			continue
		}
		score += 0.65 * scoreHit(term, def.Name, def.Description, nil, false, 0)
		score += 0.65 * scoreToolSearchTerms(term, schemaTerms)
	}
	return score
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

var toolSearchTermSeparator = regexp.MustCompile(`[^a-z0-9\p{Han}]+`)

var toolSearchQueryAliases = map[string][]string{
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
