package router

import (
	"context"
	"strings"
	"time"
	"unicode/utf8"
)

const DefaultIntentClassifierTimeout = 200 * time.Millisecond

type IntentClassifierMode string

const (
	IntentClassifierRuleOnly IntentClassifierMode = "rule_only"
	IntentClassifierHybrid   IntentClassifierMode = "hybrid"
)

type IntentClassificationResult struct {
	Intent     IntentFrame
	Source     string
	Degraded   bool
	CacheHit   bool
	CostUSD    float64
	Err        error
	Duration   time.Duration
	BudgetOK   bool
	LLMAttempt bool
}

type IntentLLMClassifier interface {
	ClassifyIntent(ctx context.Context, input IntentClassifierInput) (IntentFrame, IntentClassifierUsage, error)
}

type IntentClassifierInput struct {
	SessionID string
	Message   string
}

type IntentClassifierUsage struct {
	CostUSD float64
}

type IntentClassifier struct {
	mode                 IntentClassifierMode
	cache                *IntentCache
	budget               *IntentBudgetGuard
	llm                  IntentLLMClassifier
	timeout              time.Duration
	estimatedLLMCostUSD  float64
	maxRuleMessageRunes  int
	enableCache          bool
	enableBudgetFallback bool
}

type IntentClassifierOption func(*IntentClassifier)

func NewIntentClassifier(opts ...IntentClassifierOption) *IntentClassifier {
	c := &IntentClassifier{
		mode:                 IntentClassifierRuleOnly,
		cache:                NewIntentCache(DefaultIntentCacheTTL),
		budget:               NewIntentBudgetGuard(DefaultIntentClassifierDailyBudgetUSD),
		timeout:              DefaultIntentClassifierTimeout,
		estimatedLLMCostUSD:  0.001,
		maxRuleMessageRunes:  50_000,
		enableCache:          true,
		enableBudgetFallback: true,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(c)
		}
	}
	if c.timeout <= 0 {
		c.timeout = DefaultIntentClassifierTimeout
	}
	if c.maxRuleMessageRunes <= 0 {
		c.maxRuleMessageRunes = 50_000
	}
	return c
}

func WithIntentClassifierMode(mode IntentClassifierMode) IntentClassifierOption {
	return func(c *IntentClassifier) {
		if mode != "" {
			c.mode = mode
		}
	}
}

func WithIntentLLMClassifier(llm IntentLLMClassifier) IntentClassifierOption {
	return func(c *IntentClassifier) {
		c.llm = llm
		if llm != nil && c.mode == IntentClassifierRuleOnly {
			c.mode = IntentClassifierHybrid
		}
	}
}

func WithIntentCache(cache *IntentCache) IntentClassifierOption {
	return func(c *IntentClassifier) {
		c.cache = cache
		c.enableCache = cache != nil
	}
}

func WithIntentBudgetGuard(budget *IntentBudgetGuard) IntentClassifierOption {
	return func(c *IntentClassifier) {
		c.budget = budget
		c.enableBudgetFallback = budget != nil
	}
}

func WithIntentClassifierTimeout(timeout time.Duration) IntentClassifierOption {
	return func(c *IntentClassifier) {
		c.timeout = timeout
	}
}

func WithIntentClassifierEstimatedCost(costUSD float64) IntentClassifierOption {
	return func(c *IntentClassifier) {
		if costUSD >= 0 {
			c.estimatedLLMCostUSD = costUSD
		}
	}
}

func (c *IntentClassifier) Classify(ctx context.Context, sessionID, message string) IntentClassificationResult {
	if c == nil {
		c = NewIntentClassifier()
	}
	start := time.Now()
	message = trimMessageForIntentRules(message, c.maxRuleMessageRunes)
	if c.enableCache {
		if intent, ok := c.cache.Get(sessionID, message); ok {
			intent.Signals = appendSignal(intent.Signals, "cache_hit")
			return IntentClassificationResult{
				Intent:   intent,
				Source:   "cache",
				CacheHit: true,
				BudgetOK: true,
				Duration: time.Since(start),
			}
		}
	}

	if c.mode == IntentClassifierHybrid && c.llm != nil {
		result := c.classifyWithLLM(ctx, sessionID, message, start)
		if result.Err == nil && result.Intent.Kind != "" && result.Intent.Kind != IntentUnknown {
			if c.enableCache {
				c.cache.Set(sessionID, message, result.Intent)
			}
			return result
		}
		fallback := RuleClassifyIntent(message)
		fallback.Signals = appendSignal(fallback.Signals, "llm_fallback")
		if result.Err != nil {
			fallback.Signals = appendSignal(fallback.Signals, "llm_error")
		}
		if c.enableCache {
			c.cache.Set(sessionID, message, fallback)
		}
		return IntentClassificationResult{
			Intent:     fallback,
			Source:     "rule_fallback",
			Degraded:   true,
			Err:        result.Err,
			Duration:   time.Since(start),
			BudgetOK:   result.BudgetOK,
			LLMAttempt: result.LLMAttempt,
		}
	}

	intent := RuleClassifyIntent(message)
	if c.enableCache {
		c.cache.Set(sessionID, message, intent)
	}
	return IntentClassificationResult{
		Intent:   intent,
		Source:   "rule",
		BudgetOK: true,
		Duration: time.Since(start),
	}
}

func (c *IntentClassifier) classifyWithLLM(ctx context.Context, sessionID, message string, start time.Time) IntentClassificationResult {
	if c.enableBudgetFallback && !c.budget.Allow(c.estimatedLLMCostUSD) {
		return IntentClassificationResult{
			Intent:     IntentFrame{Kind: IntentUnknown, Confidence: 0, Signals: []string{"budget_exceeded"}},
			Source:     "budget",
			Degraded:   true,
			BudgetOK:   false,
			LLMAttempt: false,
			Duration:   time.Since(start),
		}
	}
	callCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	intent, usage, err := c.llm.ClassifyIntent(callCtx, IntentClassifierInput{
		SessionID: sessionID,
		Message:   message,
	})
	if usage.CostUSD > 0 {
		c.budget.Record(usage.CostUSD)
	}
	if err != nil {
		return IntentClassificationResult{
			Intent:     IntentFrame{Kind: IntentUnknown, Confidence: 0},
			Source:     "llm",
			Degraded:   true,
			Err:        err,
			CostUSD:    usage.CostUSD,
			BudgetOK:   true,
			LLMAttempt: true,
			Duration:   time.Since(start),
		}
	}
	intent = normalizeClassifiedIntent(intent)
	intent.Signals = appendSignal(intent.Signals, "llm")
	return IntentClassificationResult{
		Intent:     intent,
		Source:     "llm",
		CostUSD:    usage.CostUSD,
		BudgetOK:   true,
		LLMAttempt: true,
		Duration:   time.Since(start),
	}
}

func RuleClassifyIntent(message string) IntentFrame {
	q := strings.ToLower(strings.TrimSpace(message))
	intent := IntentFrame{
		Kind:       IntentAnswer,
		Confidence: 0.45,
		Subject:    truncateIntentSubject(strings.TrimSpace(message)),
		Signals:    []string{"rule"},
	}
	if q == "" {
		intent.Kind = IntentUnknown
		intent.Confidence = 0
		return intent
	}
	if hasAny(q, "不要发送", "别发送", "不用发送", "只是写", "只写", "只生成", "don't send", "do not send") {
		intent.Kind = IntentWriteLocal
		intent.NegatedActions = []string{"send"}
		intent.AllowsSideEffects = false
		intent.Confidence = 0.75
		return intent
	}
	if isCreateSkillIntent(q) {
		intent.Kind = IntentCreateSkill
		intent.Subject = "skill"
		intent.AllowsSideEffects = true
		intent.Confidence = 0.85
		return intent
	}
	if isManageToolIntent(q) {
		intent.Kind = IntentManageTool
		intent.Subject = "tool"
		intent.RequiresExternal = strings.Contains(q, "api") || strings.Contains(q, "github") || strings.Contains(q, "外部")
		intent.AllowsSideEffects = true
		intent.Confidence = 0.8
		return intent
	}
	if hasAny(q, "发送", "send ", "通知", "发给") {
		intent.Kind = IntentExternalWrite
		intent.AllowsSideEffects = true
		intent.RequiresExternal = true
		intent.Confidence = 0.75
		return intent
	}
	if hasAny(q, "读取", "查询", "搜索", "查看", "read ", "search ", "list ") {
		intent.Kind = IntentRead
		intent.AllowsSideEffects = false
		intent.Confidence = 0.65
		return intent
	}
	return intent
}

func normalizeClassifiedIntent(intent IntentFrame) IntentFrame {
	switch intent.Kind {
	case IntentAnswer, IntentRead, IntentWriteLocal, IntentExternalRead, IntentExternalWrite, IntentCreateSkill, IntentModifySkill, IntentManageTool, IntentPlan:
	case "":
		intent.Kind = IntentUnknown
	default:
		intent.Kind = IntentUnknown
	}
	if intent.Confidence < 0 {
		intent.Confidence = 0
	}
	if intent.Confidence > 1 {
		intent.Confidence = 1
	}
	return intent
}

func isCreateSkillIntent(q string) bool {
	hasSkill := hasAny(q, "skill", "技能")
	hasCreate := hasAny(q, "创建", "新建", "生成", "写一个", "做一个", "create", "build", "make")
	return hasSkill && hasCreate
}

func isManageToolIntent(q string) bool {
	hasMCP := hasAny(q, "mcp server", "mcp服务器", "mcp 服务", "mcp tool", "mcp工具")
	hasTool := hasAny(q, "工具", "tool")
	hasBuild := hasAny(q, "创建", "新建", "封装", "接入", "调试", "注册", "create", "build", "register", "integrate")
	return hasBuild && (hasMCP || (hasTool && hasAny(q, "api", "server", "服务器", "transport")))
}

func hasAny(s string, terms ...string) bool {
	for _, term := range terms {
		if strings.Contains(s, term) {
			return true
		}
	}
	return false
}

func trimMessageForIntentRules(message string, maxRunes int) string {
	if maxRunes <= 0 || utf8.RuneCountInString(message) <= maxRunes {
		return message
	}
	runes := []rune(message)
	return string(runes[:maxRunes])
}

func truncateIntentSubject(subject string) string {
	const maxRunes = 120
	return trimMessageForIntentRules(subject, maxRunes)
}

func appendSignal(signals []string, signal string) []string {
	if signal == "" {
		return signals
	}
	for _, existing := range signals {
		if existing == signal {
			return signals
		}
	}
	return append(signals, signal)
}
