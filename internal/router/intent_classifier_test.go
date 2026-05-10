package router

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestIntentClassifierRuleFallbackClassifiesCoreIntents(t *testing.T) {
	classifier := NewIntentClassifier(WithIntentClassifierMode(IntentClassifierRuleOnly))

	tests := []struct {
		name string
		msg  string
		want IntentKind
	}{
		{name: "create skill", msg: "创建一个跟我打招呼的技能", want: IntentCreateSkill},
		{name: "mcp server", msg: "创建 MCP server 接入 GitHub API", want: IntentManageTool},
		{name: "negated send", msg: "帮我写飞书通知文案，不要发送", want: IntentWriteLocal},
		{name: "external send", msg: "发送给飞书用户郭松", want: IntentExternalWrite},
		{name: "read", msg: "读取本地配置", want: IntentRead},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := classifier.Classify(context.Background(), "s1", tt.msg)
			if got.Intent.Kind != tt.want {
				t.Fatalf("Kind = %q, want %q; result=%+v", got.Intent.Kind, tt.want, got)
			}
			if got.Source != "rule" {
				t.Fatalf("Source = %q, want rule", got.Source)
			}
		})
	}
}

func TestIntentClassifierDoesNotTreatSkillWithMCPContextAsManageTool(t *testing.T) {
	got := RuleClassifyIntent("创建一个 skill，MCP 只是实现背景，不要创建 MCP server")
	if got.Kind != IntentCreateSkill {
		t.Fatalf("Kind = %q, want create_skill; intent=%+v", got.Kind, got)
	}
}

func TestIntentCacheKeyIncludesSessionAndExpires(t *testing.T) {
	now := time.Date(2026, 5, 8, 10, 0, 0, 0, time.UTC)
	cache := NewIntentCache(time.Minute)
	cache.SetNowForTest(func() time.Time { return now })
	cache.Set("s1", "读取配置", IntentFrame{Kind: IntentRead})

	if got, ok := cache.Get("s1", "读取配置"); !ok || got.Kind != IntentRead {
		t.Fatalf("cache miss before expiry: got=%+v ok=%v", got, ok)
	}
	if _, ok := cache.Get("s2", "读取配置"); ok {
		t.Fatal("cache key must include session id")
	}

	now = now.Add(2 * time.Minute)
	if _, ok := cache.Get("s1", "读取配置"); ok {
		t.Fatal("cache should expire after ttl")
	}
}

func TestIntentClassifierUsesCache(t *testing.T) {
	cache := NewIntentCache(time.Minute)
	classifier := NewIntentClassifier(WithIntentCache(cache))

	first := classifier.Classify(context.Background(), "s1", "读取配置")
	second := classifier.Classify(context.Background(), "s1", "读取配置")

	if first.CacheHit {
		t.Fatal("first classify should not be cache hit")
	}
	if !second.CacheHit || second.Source != "cache" {
		t.Fatalf("second classify should be cache hit, got %+v", second)
	}
}

func TestIntentBudgetGuardResetsDaily(t *testing.T) {
	now := time.Date(2026, 5, 8, 10, 0, 0, 0, time.UTC)
	guard := NewIntentBudgetGuard(1.0)
	guard.SetNowForTest(func() time.Time { return now })
	guard.Record(0.8)
	if guard.Allow(0.3) {
		t.Fatal("budget should reject when daily spend would exceed limit")
	}
	now = now.Add(24 * time.Hour)
	if !guard.Allow(0.3) {
		t.Fatal("budget should reset on new UTC day")
	}
}

func TestIntentClassifierBudgetFallbackSkipsLLM(t *testing.T) {
	budget := NewIntentBudgetGuard(0.01)
	budget.Record(0.01)
	llm := &fakeIntentLLM{t: t}
	classifier := NewIntentClassifier(
		WithIntentLLMClassifier(llm),
		WithIntentBudgetGuard(budget),
		WithIntentClassifierEstimatedCost(0.01),
	)

	got := classifier.Classify(context.Background(), "s1", "读取配置")
	if got.Intent.Kind != IntentRead {
		t.Fatalf("fallback kind = %q, want read; result=%+v", got.Intent.Kind, got)
	}
	if !got.Degraded || got.Source != "rule_fallback" || got.LLMAttempt {
		t.Fatalf("budget fallback metadata wrong: %+v", got)
	}
	if llm.calls != 0 {
		t.Fatalf("llm calls = %d, want 0", llm.calls)
	}
}

func TestIntentClassifierLLMFailureFallsBackToRules(t *testing.T) {
	llm := &fakeIntentLLM{err: errors.New("boom")}
	classifier := NewIntentClassifier(WithIntentLLMClassifier(llm))

	got := classifier.Classify(context.Background(), "s1", "创建一个跟我打招呼的技能")
	if got.Intent.Kind != IntentCreateSkill {
		t.Fatalf("fallback kind = %q, want create_skill; result=%+v", got.Intent.Kind, got)
	}
	if !got.Degraded || got.Source != "rule_fallback" || !got.LLMAttempt {
		t.Fatalf("llm failure fallback metadata wrong: %+v", got)
	}
}

func TestIntentClassifierLLMSuccessRecordsBudgetAndCaches(t *testing.T) {
	budget := NewIntentBudgetGuard(1.0)
	llm := &fakeIntentLLM{intent: IntentFrame{Kind: IntentPlan, Confidence: 0.9}, cost: 0.2}
	classifier := NewIntentClassifier(
		WithIntentLLMClassifier(llm),
		WithIntentBudgetGuard(budget),
	)

	first := classifier.Classify(context.Background(), "s1", "制定计划")
	second := classifier.Classify(context.Background(), "s1", "制定计划")

	if first.Intent.Kind != IntentPlan || first.Source != "llm" {
		t.Fatalf("first result = %+v, want llm plan", first)
	}
	if budget.SpentUSD() != 0.2 {
		t.Fatalf("spent = %v, want 0.2", budget.SpentUSD())
	}
	if !second.CacheHit || second.Intent.Kind != IntentPlan {
		t.Fatalf("second result = %+v, want cached plan", second)
	}
	if llm.calls != 1 {
		t.Fatalf("llm calls = %d, want 1", llm.calls)
	}
}

type fakeIntentLLM struct {
	t      *testing.T
	intent IntentFrame
	cost   float64
	err    error
	calls  int
}

func (f *fakeIntentLLM) ClassifyIntent(ctx context.Context, input IntentClassifierInput) (IntentFrame, IntentClassifierUsage, error) {
	f.calls++
	if f.t != nil {
		f.t.Helper()
		f.t.Fatal("LLM should not have been called")
	}
	if input.Message == "" {
		return IntentFrame{}, IntentClassifierUsage{}, errors.New("empty message")
	}
	if f.err != nil {
		return IntentFrame{}, IntentClassifierUsage{}, f.err
	}
	return f.intent, IntentClassifierUsage{CostUSD: f.cost}, nil
}
