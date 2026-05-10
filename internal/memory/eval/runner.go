package eval

import (
	"context"

	"github.com/chef-guo/agents-hive/internal/memory"
	"go.uber.org/zap"
)

const RequiredFixtureCount = 45

// Result 是单条 memory/context fixture 的执行结果。
type Result struct {
	CaseID  string   `json:"case_id"`
	Name    string   `json:"name,omitempty"`
	Passed  bool     `json:"passed"`
	Reason  string   `json:"reason,omitempty"`
	Tags    []string `json:"tags,omitempty"`
	Target  string   `json:"target,omitempty"`
	Kind    string   `json:"kind,omitempty"`
	Fixture string   `json:"fixture,omitempty"`
}

// Summary 汇总 memory/context eval 执行结果，可被 CLI 或质量门禁消费。
type Summary struct {
	Total          int      `json:"total"`
	Passed         int      `json:"passed"`
	RequiredTotal  int      `json:"required_total"`
	RequiredPassed int      `json:"required_passed"`
	RequiredFailed []string `json:"required_failed,omitempty"`
	OptionalFailed []string `json:"optional_failed,omitempty"`
	Results        []Result `json:"results"`
}

// RunCases 加载并执行指定目录下的 memory/context eval fixtures。
func RunCases(ctx context.Context, dir string) (Summary, error) {
	loaded, err := LoadCases(dir)
	if err != nil {
		return Summary{}, err
	}

	summary := Summary{
		Total:   len(loaded),
		Results: make([]Result, 0, len(loaded)),
	}
	for _, lc := range loaded {
		if lc.Case.Required {
			summary.RequiredTotal++
		}

		result := Result{
			CaseID:  lc.Case.ID,
			Name:    lc.Case.Name,
			Passed:  true,
			Tags:    caseTags(lc.Case),
			Target:  lc.Case.Target,
			Kind:    lc.Case.Kind,
			Fixture: lc.Path,
		}
		if err := runCase(ctx, lc); err != nil {
			result.Passed = false
			result.Reason = err.Error()
			if lc.Case.Required {
				summary.RequiredFailed = append(summary.RequiredFailed, lc.Case.ID)
			} else {
				summary.OptionalFailed = append(summary.OptionalFailed, lc.Case.ID)
			}
		} else {
			summary.Passed++
			if lc.Case.Required {
				summary.RequiredPassed++
			}
		}
		summary.Results = append(summary.Results, result)
	}
	return summary, nil
}

func runCase(ctx context.Context, loaded LoadedCase) error {
	if err := ValidateCase(loaded.Case); err != nil {
		return err
	}
	records, err := BuildRecords(loaded.Case)
	if err != nil {
		return err
	}
	if err := AssertMetadata(loaded.Case, records); err != nil {
		return err
	}
	if err := AssertScope(loaded.Case, records); err != nil {
		return err
	}
	if loaded.Case.SkipInjection {
		return nil
	}

	store := &fixtureMemoryStore{
		records:         records,
		exposeCrossUser: loaded.Case.ExposeCrossUser,
	}
	sessionID := loaded.Case.SessionID
	if sessionID == "" {
		sessionID = "eval-session"
	}
	rctx := fixtureRuntimeContext(loaded.Case)
	if rctx.SessionID == "" {
		rctx.SessionID = sessionID
	}
	ctx = memory.WithRuntimeContext(ctx, rctx)

	cfg := memory.DefaultInjectionConfig()
	cfg.FeedbackMaxTokens = 600
	cfg.MemoryMaxTokens = 2000
	cfg.FeedbackTopK = 3
	cfg.MemoryTopK = 10
	if loaded.Case.MinScore != nil {
		cfg.MinScore = *loaded.Case.MinScore
	}
	inj := memory.NewInjectorWithConfig(store, cfg, zap.NewNop())
	recorder := &fixtureMetricRecorder{}
	if loaded.Case.Hybrid != nil && loaded.Case.Hybrid.Enabled {
		embed, vec := buildHybridFixtures(*loaded.Case.Hybrid)
		hybrid := memory.NewHybridSearcher(store, vec, embed, zap.NewNop())
		hybrid.SetMetrics(recorder)
		if loaded.Case.Hybrid.VectorSpace != "" {
			hybrid.SetMetricsConfig(memory.MetricsConfig{
				Recorder:    recorder,
				VectorSpace: loaded.Case.Hybrid.VectorSpace,
			})
		}
		inj.SetHybridSearcher(hybrid)
	}

	got, err := inj.InjectContextDetailed(ctx, loaded.Case.Query, sessionID, loaded.Case.UserID)
	if err != nil {
		return err
	}
	if err := AssertResult(loaded.Case, got); err != nil {
		return err
	}
	if err := AssertStoreExpectations(loaded.Case, store); err != nil {
		return err
	}
	return AssertMetrics(loaded.Case, recorder)
}

func caseTags(c Case) []string {
	seen := map[string]bool{}
	var out []string
	for _, mem := range c.Memories {
		for _, tag := range mem.Tags {
			if !seen[tag] {
				seen[tag] = true
				out = append(out, tag)
			}
		}
	}
	return out
}
