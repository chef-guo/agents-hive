package master

import (
	"strings"
	"testing"

	"go.uber.org/zap/zaptest"

	"github.com/chef-guo/agents-hive/internal/airouter"
	"github.com/chef-guo/agents-hive/internal/config"
	"github.com/chef-guo/agents-hive/internal/llm"
	"github.com/chef-guo/agents-hive/internal/skills"
	"github.com/chef-guo/agents-hive/internal/store"
	"github.com/chef-guo/agents-hive/internal/subagent"
)

func newReasoningEffortTestMaster(t *testing.T, cfg Config) *Master {
	t.Helper()
	logger := zaptest.NewLogger(t)
	return NewMaster(
		cfg,
		config.HITLConfig{},
		subagent.NewRegistry(logger),
		skills.NewRegistry(logger),
		store.NewMemoryStore(),
		logger,
	)
}

func TestAutoReasoningEffortClassifiesByPromptComplexity(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "short factual prompt uses low",
			input: "ping",
			want:  "low",
		},
		{
			name:  "implementation prompt uses medium",
			input: "Implement a retry wrapper for the API client and add focused tests.",
			want:  "medium",
		},
		{
			name:  "architecture prompt uses high",
			input: "Design the migration plan, compare tradeoffs, identify edge cases, and produce a step-by-step rollout.",
			want:  "high",
		},
		{
			name:  "long prompt uses high",
			input: strings.Repeat("analyze ", 180),
			want:  "high",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := autoReasoningEffort(tt.input); got != tt.want {
				t.Fatalf("autoReasoningEffort() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestResolveReasoningEffortKeepsManualOverride(t *testing.T) {
	if got := resolveReasoningEffort("manual-high", "hello", "o3-mini"); got != "manual-high" {
		t.Fatalf("manual override = %q, want manual-high", got)
	}
}

func TestResolveReasoningEffortNoOpsForUnsupportedModel(t *testing.T) {
	if got := resolveReasoningEffort("", "Design the migration plan and compare tradeoffs.", "gpt-4o"); got != "" {
		t.Fatalf("unsupported model auto effort = %q, want empty", got)
	}
}

func TestResolveReasoningEffortNoOpsForUnknownModel(t *testing.T) {
	if got := resolveReasoningEffort("", "Design the migration plan and compare tradeoffs.", "unknown-reasoning-model"); got != "" {
		t.Fatalf("unknown model auto effort = %q, want empty", got)
	}
}

func TestMasterResolveRequestReasoningEffortUsesActiveRouterModel(t *testing.T) {
	logger := zaptest.NewLogger(t)
	router := airouter.NewRouter(airouter.RouterConfig{
		DefaultModel:    "o3-mini",
		DefaultProvider: "openai",
		DefaultAPIKey:   "test-key",
		Logger:          logger,
	})
	m := newReasoningEffortTestMaster(t, Config{Router: router})

	got := m.resolveRequestReasoningEffort("Design the migration plan and compare tradeoffs.")
	if got != "high" {
		t.Fatalf("resolveRequestReasoningEffort() = %q, want high", got)
	}
}

func TestMasterResolveRequestReasoningEffortNoOpsWhenActiveModelUnsupported(t *testing.T) {
	m := newReasoningEffortTestMaster(t, Config{
		Model:    "gpt-4o",
		APIKey:   "test-key",
		Provider: "openai",
		ReasoningEffortAuto: config.ReasoningEffortAutoConfig{
			Enabled:      true,
			DefaultLevel: "low",
		},
	})
	if m.llmClient == nil {
		t.Fatal("test setup did not create llm client")
	}

	got := m.resolveRequestReasoningEffort("Design the migration plan and compare tradeoffs.")
	if got != "" {
		t.Fatalf("resolveRequestReasoningEffort() = %q, want empty", got)
	}
}

func TestMasterResolveRequestReasoningEffortHonorsConfigDisable(t *testing.T) {
	logger := zaptest.NewLogger(t)
	router := airouter.NewRouter(airouter.RouterConfig{
		DefaultModel:    "o3-mini",
		DefaultProvider: "openai",
		DefaultAPIKey:   "test-key",
		Logger:          logger,
	})
	m := newReasoningEffortTestMaster(t, Config{
		Router: router,
		ReasoningEffortAuto: config.ReasoningEffortAutoConfig{
			Enabled:      false,
			DefaultLevel: "low",
		},
	})

	got := m.resolveRequestReasoningEffort("Design the migration plan and compare tradeoffs.")
	if got != "" {
		t.Fatalf("resolveRequestReasoningEffort() = %q, want empty when disabled", got)
	}
}

func TestMasterResolveRequestReasoningEffortUsesConfiguredDefaultLevel(t *testing.T) {
	logger := zaptest.NewLogger(t)
	router := airouter.NewRouter(airouter.RouterConfig{
		DefaultModel:    "o3-mini",
		DefaultProvider: "openai",
		DefaultAPIKey:   "test-key",
		Logger:          logger,
	})
	m := newReasoningEffortTestMaster(t, Config{
		Router: router,
		ReasoningEffortAuto: config.ReasoningEffortAutoConfig{
			Enabled:      true,
			DefaultLevel: "medium",
		},
	})

	got := m.resolveRequestReasoningEffort("ping")
	if got != "medium" {
		t.Fatalf("resolveRequestReasoningEffort() = %q, want configured default medium", got)
	}
}

func TestRunReActLoopResolverPreservesManualOverrideShape(t *testing.T) {
	req := llm.ChatWithToolsRequest{
		ReasoningEffort: resolveReasoningEffort("low", "Design the migration plan and compare tradeoffs.", "o3-mini"),
	}
	if req.ReasoningEffort != "low" {
		t.Fatalf("ChatWithToolsRequest ReasoningEffort = %q, want low", req.ReasoningEffort)
	}
}
