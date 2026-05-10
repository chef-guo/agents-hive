package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"go.uber.org/zap/zapcore"
)

// ---------------------------------------------------------------------------
// TestDefault
// ---------------------------------------------------------------------------

func TestDefault(t *testing.T) {
	cfg := Default()

	// Server defaults
	if cfg.Server.Port != DefaultServerPort {
		t.Errorf("Server.Port = %d, want %d", cfg.Server.Port, DefaultServerPort)
	}
	if cfg.Server.Host != "0.0.0.0" {
		t.Errorf("Server.Host = %q, want %q", cfg.Server.Host, "0.0.0.0")
	}

	// LLM defaults
	if cfg.LLM.Model != DefaultModel {
		t.Errorf("LLM.Model = %q, want %q", cfg.LLM.Model, DefaultModel)
	}
	if cfg.LLM.APIKey != "" {
		t.Errorf("LLM.APIKey = %q, want empty string", cfg.LLM.APIKey)
	}
	if cfg.LLM.BaseURL != DefaultBaseURL {
		t.Errorf("LLM.BaseURL = %q, want %q", cfg.LLM.BaseURL, DefaultBaseURL)
	}

	// Agent/MCP 运行时默认值现在由 DB 种子提供，Default() 返回零值
	if cfg.Agent.Timeout != 0 {
		t.Errorf("Agent.Timeout = %v, want 0 (runtime defaults from DB)", cfg.Agent.Timeout)
	}
	if cfg.Agent.MaxConcurrentAgents != 0 {
		t.Errorf("Agent.MaxConcurrentAgents = %d, want 0 (runtime defaults from DB)", cfg.Agent.MaxConcurrentAgents)
	}
	if cfg.Agent.HealthInterval != 0 {
		t.Errorf("Agent.HealthInterval = %v, want 0 (runtime defaults from DB)", cfg.Agent.HealthInterval)
	}
	if cfg.MCP.Timeout != 0 {
		t.Errorf("MCP.Timeout = %v, want 0 (runtime defaults from DB)", cfg.MCP.Timeout)
	}
	if !cfg.Agent.PlanRuntime.Enabled {
		t.Error("Agent.PlanRuntime.Enabled = false, want true by default")
	}
	if cfg.Agent.PlanRuntime.AutoContinue {
		t.Error("Agent.PlanRuntime.AutoContinue = true, want false by default")
	}

	// Logging defaults
	if cfg.Logging.Level != DefaultLogLevel {
		t.Errorf("Logging.Level = %q, want %q", cfg.Logging.Level, DefaultLogLevel)
	}
	if cfg.Logging.Format != "json" {
		t.Errorf("Logging.Format = %q, want %q", cfg.Logging.Format, "json")
	}
}

func TestLoad_PlanRuntimeCanBeExplicitlyDisabled(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	raw := map[string]any{
		"agent": map[string]any{
			"plan_runtime": map[string]any{
				"enabled": false,
			},
		},
	}

	data, err := json.Marshal(raw)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load(%q) returned error: %v", path, err)
	}
	if cfg.Agent.PlanRuntime.Enabled {
		t.Fatal("Agent.PlanRuntime.Enabled = true, want explicit false config to disable it")
	}
}

func TestReflectionConfigDefaults(t *testing.T) {
	cfg := Default()
	assertReflectionDefaults(t, cfg.Agent.Reflection, "Default()")

	cli := Default()
	cli.CLIDefaults()
	assertReflectionDefaults(t, cli.Agent.Reflection, "CLIDefaults()")
}

func TestReasoningEffortAutoDefaults(t *testing.T) {
	cfg := Default()
	assertReasoningEffortAutoDefaults(t, cfg.Agent.ReasoningEffortAuto, "Default()")

	cli := Default()
	cli.CLIDefaults()
	assertReasoningEffortAutoDefaults(t, cli.Agent.ReasoningEffortAuto, "CLIDefaults()")
}

func TestToolRecallDefaultsAndNormalize(t *testing.T) {
	cfg := Default()
	cfg.Resolve()
	assertToolRecallDefaults(t, cfg.Agent.ToolRecall, "Default().Resolve()")

	cli := Default()
	cli.CLIDefaults()
	cli.Resolve()
	assertToolRecallDefaults(t, cli.Agent.ToolRecall, "CLIDefaults().Resolve()")

	normalized := NormalizeToolRecallConfig(ToolRecallConfig{
		Mode:               "bogus",
		Limit:              -1,
		MinScore:           2,
		SideEffectMinScore: 0.1,
	})
	if normalized.Mode != "off" {
		t.Fatalf("invalid mode normalized to %q, want off", normalized.Mode)
	}
	if normalized.Limit != DefaultToolRecallLimit {
		t.Fatalf("negative limit normalized to %d, want %d", normalized.Limit, DefaultToolRecallLimit)
	}
	if normalized.MinScore != 1 {
		t.Fatalf("min score normalized to %v, want 1", normalized.MinScore)
	}
	if normalized.SideEffectMinScore != 1 {
		t.Fatalf("side effect min score normalized to %v, want 1", normalized.SideEffectMinScore)
	}
}

func TestMemoryConfigDefaultsAndNormalize(t *testing.T) {
	cfg := Default()
	cfg.CLIDefaults()
	cfg.Resolve()
	assertMemoryDefaults(t, cfg.Memory, "CLIDefaults().Resolve()")

	normalized := NormalizeMemoryConfig(MemoryConfig{
		MaxMemories:         -1,
		RetentionDays:       -1,
		InjectMaxTokens:     20000,
		InjectTopK:          100,
		InjectMinConfidence: 2,
		InjectMinScore:      -0.2,
		FeedbackTopK:        100,
		MemoryTopK:          100,
		FeedbackMaxTokens:   9000,
		MemoryMaxTokens:     50000,
	})
	if normalized.MaxMemories != DefaultMemoryConfig.MaxMemories {
		t.Fatalf("MaxMemories = %d, want %d", normalized.MaxMemories, DefaultMemoryConfig.MaxMemories)
	}
	if normalized.RetentionDays != DefaultMemoryConfig.RetentionDays {
		t.Fatalf("RetentionDays = %d, want %d", normalized.RetentionDays, DefaultMemoryConfig.RetentionDays)
	}
	if normalized.InjectMaxTokens != 12000 {
		t.Fatalf("InjectMaxTokens = %d, want 12000", normalized.InjectMaxTokens)
	}
	if normalized.InjectTopK != 50 {
		t.Fatalf("InjectTopK = %d, want 50", normalized.InjectTopK)
	}
	if normalized.InjectMinConfidence != DefaultMemoryConfig.InjectMinConfidence {
		t.Fatalf("InjectMinConfidence = %v, want %v", normalized.InjectMinConfidence, DefaultMemoryConfig.InjectMinConfidence)
	}
	if normalized.InjectMinScore != 0 {
		t.Fatalf("InjectMinScore = %v, want 0", normalized.InjectMinScore)
	}
	if normalized.FeedbackTopK != 20 {
		t.Fatalf("FeedbackTopK = %d, want 20", normalized.FeedbackTopK)
	}
	if normalized.MemoryTopK != 50 {
		t.Fatalf("MemoryTopK = %d, want 50", normalized.MemoryTopK)
	}
	if normalized.FeedbackMaxTokens != 4000 {
		t.Fatalf("FeedbackMaxTokens = %d, want 4000", normalized.FeedbackMaxTokens)
	}
	if normalized.MemoryMaxTokens != 12000 {
		t.Fatalf("MemoryMaxTokens = %d, want 12000", normalized.MemoryMaxTokens)
	}
	if normalized.VectorStoreType != DefaultMemoryConfig.VectorStoreType {
		t.Fatalf("VectorStoreType = %q, want %q", normalized.VectorStoreType, DefaultMemoryConfig.VectorStoreType)
	}
}

func TestLoad_ReasoningEffortAutoCanBeDisabled(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	raw := map[string]any{
		"agent": map[string]any{
			"reasoning_effort_auto": map[string]any{
				"enabled":       false,
				"default_level": "low",
			},
		},
	}

	data, err := json.Marshal(raw)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load(%q) returned error: %v", path, err)
	}
	if cfg.Agent.ReasoningEffortAuto.Enabled {
		t.Fatal("Agent.ReasoningEffortAuto.Enabled = true, want explicit false config to disable it")
	}
	if cfg.Agent.ReasoningEffortAuto.DefaultLevel != "low" {
		t.Fatalf("Agent.ReasoningEffortAuto.DefaultLevel = %q, want low", cfg.Agent.ReasoningEffortAuto.DefaultLevel)
	}
}

func assertReflectionDefaults(t *testing.T, cfg ReflectionConfig, source string) {
	t.Helper()
	if !cfg.Enabled {
		t.Fatalf("%s Agent.Reflection.Enabled = false, want true", source)
	}
	if cfg.TestDrivenShadow.Enabled {
		t.Fatalf("%s Agent.Reflection.TestDrivenShadow.Enabled = true, want false", source)
	}
	if cfg.EvaluatorShadow.Enabled {
		t.Fatalf("%s Agent.Reflection.EvaluatorShadow.Enabled = true, want false", source)
	}
}

func assertReasoningEffortAutoDefaults(t *testing.T, cfg ReasoningEffortAutoConfig, source string) {
	t.Helper()
	if !cfg.Enabled {
		t.Fatalf("%s Agent.ReasoningEffortAuto.Enabled = false, want true", source)
	}
	if cfg.DefaultLevel != "low" {
		t.Fatalf("%s Agent.ReasoningEffortAuto.DefaultLevel = %q, want low", source, cfg.DefaultLevel)
	}
}

func assertToolRecallDefaults(t *testing.T, cfg ToolRecallConfig, source string) {
	t.Helper()
	if cfg.Mode != "inject" {
		t.Fatalf("%s Agent.ToolRecall.Mode = %q, want inject", source, cfg.Mode)
	}
	if cfg.Limit != 5 {
		t.Fatalf("%s Agent.ToolRecall.Limit = %d, want 5", source, cfg.Limit)
	}
	if cfg.MinScore != 0.35 {
		t.Fatalf("%s Agent.ToolRecall.MinScore = %v, want 0.35", source, cfg.MinScore)
	}
	if cfg.SideEffectMinScore != 0.65 {
		t.Fatalf("%s Agent.ToolRecall.SideEffectMinScore = %v, want 0.65", source, cfg.SideEffectMinScore)
	}
	if !cfg.LogCandidates {
		t.Fatalf("%s Agent.ToolRecall.LogCandidates = false, want true", source)
	}
}

func assertMemoryDefaults(t *testing.T, cfg MemoryConfig, source string) {
	t.Helper()
	if !cfg.Enabled {
		t.Fatalf("%s Memory.Enabled = false, want true", source)
	}
	if !cfg.AutoExtract {
		t.Fatalf("%s Memory.AutoExtract = false, want true", source)
	}
	if cfg.InjectMinConfidence != 0.5 {
		t.Fatalf("%s Memory.InjectMinConfidence = %v, want 0.5", source, cfg.InjectMinConfidence)
	}
	if cfg.InjectMinScore != 0 {
		t.Fatalf("%s Memory.InjectMinScore = %v, want 0", source, cfg.InjectMinScore)
	}
	if cfg.FeedbackTopK != 3 || cfg.MemoryTopK != 8 {
		t.Fatalf("%s Memory topK = feedback %d memory %d, want 3/8", source, cfg.FeedbackTopK, cfg.MemoryTopK)
	}
	if cfg.FeedbackMaxTokens != 600 || cfg.MemoryMaxTokens != 1800 {
		t.Fatalf("%s Memory tokens = feedback %d memory %d, want 600/1800", source, cfg.FeedbackMaxTokens, cfg.MemoryMaxTokens)
	}
}

// ---------------------------------------------------------------------------
// TestLoad
// ---------------------------------------------------------------------------

func TestLoad_EmptyPath(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("OPENAI_BASE_URL", "")
	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load(\"\") returned error: %v", err)
	}

	want := Default()
	want.Resolve()
	assertConfigEqual(t, cfg, want)
}

func TestLoad_NonexistentFile(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("OPENAI_BASE_URL", "")
	cfg, err := Load("nonexistent.json")
	if err != nil {
		t.Fatalf("Load(\"nonexistent.json\") returned error: %v", err)
	}

	want := Default()
	want.Resolve()
	assertConfigEqual(t, cfg, want)
}

func TestLoad_ValidJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	raw := map[string]any{
		"server": map[string]any{
			"port": 9090,
			"host": "127.0.0.1",
		},
		"llm": map[string]any{
			"api_key": "sk-test-key-123",
			"model":   "gpt-4-turbo",
		},
		"agent": map[string]any{
			"timeout":               60000000000, // 60s in nanoseconds (time.Duration)
			"max_concurrent_agents": 20,
			"health_interval":       5000000000, // 5s in nanoseconds
		},
		"mcp": map[string]any{
			"timeout": 15000000000, // 15s in nanoseconds
		},
		"logging": map[string]any{
			"level":  "debug",
			"format": "console",
		},
	}

	data, err := json.Marshal(raw)
	if err != nil {
		t.Fatalf("marshal test config: %v", err)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatalf("write test config: %v", err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load(%q) returned error: %v", path, err)
	}

	// Server
	if cfg.Server.Port != 9090 {
		t.Errorf("Server.Port = %d, want 9090", cfg.Server.Port)
	}
	if cfg.Server.Host != "127.0.0.1" {
		t.Errorf("Server.Host = %q, want %q", cfg.Server.Host, "127.0.0.1")
	}

	// LLM
	if cfg.LLM.APIKey != "sk-test-key-123" {
		t.Errorf("LLM.APIKey = %q, want %q", cfg.LLM.APIKey, "sk-test-key-123")
	}
	if cfg.LLM.Model != "gpt-4-turbo" {
		t.Errorf("LLM.Model = %q, want %q", cfg.LLM.Model, "gpt-4-turbo")
	}

	// Agent
	if cfg.Agent.Timeout != 60*time.Second {
		t.Errorf("Agent.Timeout = %v, want %v", cfg.Agent.Timeout, 60*time.Second)
	}
	if cfg.Agent.MaxConcurrentAgents != 20 {
		t.Errorf("Agent.MaxConcurrentAgents = %d, want 20", cfg.Agent.MaxConcurrentAgents)
	}
	if cfg.Agent.HealthInterval != 5*time.Second {
		t.Errorf("Agent.HealthInterval = %v, want %v", cfg.Agent.HealthInterval, 5*time.Second)
	}

	// MCP
	if cfg.MCP.Timeout != 15*time.Second {
		t.Errorf("MCP.Timeout = %v, want %v", cfg.MCP.Timeout, 15*time.Second)
	}

	// Logging
	if cfg.Logging.Level != "debug" {
		t.Errorf("Logging.Level = %q, want %q", cfg.Logging.Level, "debug")
	}
	if cfg.Logging.Format != "console" {
		t.Errorf("Logging.Format = %q, want %q", cfg.Logging.Format, "console")
	}
}

func TestLoad_PartialJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "partial.json")

	// Only override server port; everything else should remain default.
	raw := map[string]any{
		"server": map[string]any{
			"port": 3000,
		},
	}

	data, err := json.Marshal(raw)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load(%q) error: %v", path, err)
	}

	if cfg.Server.Port != 3000 {
		t.Errorf("Server.Port = %d, want 3000", cfg.Server.Port)
	}
	// Defaults preserved for unset fields (after Resolve, provider fills in model)
	if cfg.LLM.Model != "gpt-5.2" {
		t.Errorf("LLM.Model = %q, want default %q (resolved from provider)", cfg.LLM.Model, "gpt-5.2")
	}
	if cfg.Logging.Level != DefaultLogLevel {
		t.Errorf("Logging.Level = %q, want default %q", cfg.Logging.Level, DefaultLogLevel)
	}
}

func TestLoad_InvalidJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.json")

	if err := os.WriteFile(path, []byte(`{invalid json!!!`), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	cfg, err := Load(path)
	if err == nil {
		t.Fatal("Load with invalid JSON should return error, got nil")
	}
	if cfg != nil {
		t.Errorf("Load with invalid JSON should return nil config, got %+v", cfg)
	}
}

func TestLoad_UnreadableFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "unreadable.json")

	if err := os.WriteFile(path, []byte(`{}`), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	// Remove read permission.
	if err := os.Chmod(path, 0000); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() {
		// Restore so TempDir cleanup can remove the file.
		os.Chmod(path, 0644) //nolint:errcheck
	})

	cfg, err := Load(path)
	if err == nil {
		t.Fatal("Load with unreadable file should return error, got nil")
	}
	if cfg != nil {
		t.Errorf("Load with unreadable file should return nil config, got %+v", cfg)
	}
}

func TestLoad_EnvFallback(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "no_key.json")

	// Write a config file without api_key.
	raw := map[string]any{
		"llm": map[string]any{
			"model": "gpt-5.2",
		},
	}
	data, err := json.Marshal(raw)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	t.Setenv("OPENAI_API_KEY", "sk-env-fallback-key")

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}
	if cfg.LLM.APIKey != "sk-env-fallback-key" {
		t.Errorf("LLM.APIKey = %q, want %q", cfg.LLM.APIKey, "sk-env-fallback-key")
	}
}

func TestLoad_APIKeyInFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "with_key.json")

	raw := map[string]any{
		"llm": map[string]any{
			"api_key": "sk-file-key",
			"model":   "gpt-5.2",
		},
	}
	data, err := json.Marshal(raw)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	// Set env var to a different value; the file key should take precedence.
	t.Setenv("OPENAI_API_KEY", "sk-env-should-not-be-used")

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}
	if cfg.LLM.APIKey != "sk-file-key" {
		t.Errorf("LLM.APIKey = %q, want %q (file should take precedence over env)", cfg.LLM.APIKey, "sk-file-key")
	}
}

func TestLoad_BaseURLEnvFallback(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "no_base_url.json")

	// Write a config that explicitly sets base_url to empty string.
	raw := map[string]any{
		"llm": map[string]any{
			"model":    "gpt-5.2",
			"base_url": "",
		},
	}
	data, err := json.Marshal(raw)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	t.Setenv("OPENAI_BASE_URL", "https://custom.api.example.com/v1")

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}
	if cfg.LLM.BaseURL != "https://custom.api.example.com/v1" {
		t.Errorf("LLM.BaseURL = %q, want %q", cfg.LLM.BaseURL, "https://custom.api.example.com/v1")
	}
}

func TestLoad_BaseURLInFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "with_base_url.json")

	raw := map[string]any{
		"llm": map[string]any{
			"model":    "gpt-5.2",
			"base_url": "https://file.api.example.com/v1",
		},
	}
	data, err := json.Marshal(raw)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	// Set env var to a different value; the file value should take precedence.
	t.Setenv("OPENAI_BASE_URL", "https://env-should-not-be-used.com/v1")

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}
	if cfg.LLM.BaseURL != "https://file.api.example.com/v1" {
		t.Errorf("LLM.BaseURL = %q, want %q (file should take precedence over env)", cfg.LLM.BaseURL, "https://file.api.example.com/v1")
	}
}

// ---------------------------------------------------------------------------
// TestLoad CLAW_* env vars
// ---------------------------------------------------------------------------

func TestLoad_ClawModelEnv(t *testing.T) {
	t.Setenv("CLAW_MODEL", "deepseek-chat")

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}
	if cfg.LLM.Model != "deepseek-chat" {
		t.Errorf("LLM.Model = %q, want %q", cfg.LLM.Model, "deepseek-chat")
	}
}

func TestLoad_ClawModelEnvOverridesFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	raw := map[string]any{
		"llm": map[string]any{
			"model": "gpt-5.2",
		},
	}
	data, _ := json.Marshal(raw)
	os.WriteFile(path, data, 0644)

	t.Setenv("CLAW_MODEL", "deepseek-chat")

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}
	if cfg.LLM.Model != "deepseek-chat" {
		t.Errorf("CLAW_MODEL should override config file: got %q, want %q", cfg.LLM.Model, "deepseek-chat")
	}
}

func TestLoad_ClawBaseURLEnv(t *testing.T) {
	t.Setenv("CLAW_BASE_URL", "https://api.deepseek.com/v1")

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}
	if cfg.LLM.BaseURL != "https://api.deepseek.com/v1" {
		t.Errorf("LLM.BaseURL = %q, want %q", cfg.LLM.BaseURL, "https://api.deepseek.com/v1")
	}
}

func TestLoad_ClawAPIKeyEnv(t *testing.T) {
	t.Setenv("CLAW_API_KEY", "sk-claw-key")

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}
	if cfg.LLM.APIKey != "sk-claw-key" {
		t.Errorf("LLM.APIKey = %q, want %q", cfg.LLM.APIKey, "sk-claw-key")
	}
}

func TestLoad_ClawAPIKeyOverridesOpenAI(t *testing.T) {
	t.Setenv("CLAW_API_KEY", "sk-claw-key")
	t.Setenv("OPENAI_API_KEY", "sk-openai-key")

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}
	if cfg.LLM.APIKey != "sk-claw-key" {
		t.Errorf("CLAW_API_KEY should override OPENAI_API_KEY: got %q, want %q", cfg.LLM.APIKey, "sk-claw-key")
	}
}

func TestLoad_ClawLogLevelEnv(t *testing.T) {
	t.Setenv("CLAW_LOG_LEVEL", "debug")

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}
	if cfg.Logging.Level != "debug" {
		t.Errorf("Logging.Level = %q, want %q", cfg.Logging.Level, "debug")
	}
}

// ---------------------------------------------------------------------------
// TestApplyOverrides
// ---------------------------------------------------------------------------

func TestApplyOverrides(t *testing.T) {
	cfg := Default()

	cfg.ApplyOverrides("deepseek-chat", "https://api.deepseek.com/v1", "sk-override", "debug")

	if cfg.LLM.Model != "deepseek-chat" {
		t.Errorf("Model = %q, want %q", cfg.LLM.Model, "deepseek-chat")
	}
	if cfg.LLM.BaseURL != "https://api.deepseek.com/v1" {
		t.Errorf("BaseURL = %q, want %q", cfg.LLM.BaseURL, "https://api.deepseek.com/v1")
	}
	if cfg.LLM.APIKey != "sk-override" {
		t.Errorf("APIKey = %q, want %q", cfg.LLM.APIKey, "sk-override")
	}
	if cfg.Logging.Level != "debug" {
		t.Errorf("Level = %q, want %q", cfg.Logging.Level, "debug")
	}
}

func TestApplyOverrides_EmptyValuesPreserveExisting(t *testing.T) {
	cfg := Default()
	cfg.LLM.Model = "gpt-5.2"
	cfg.LLM.BaseURL = "http://45.205.26.177:9999"

	cfg.ApplyOverrides("", "", "", "")

	if cfg.LLM.Model != "gpt-5.2" {
		t.Errorf("empty override should preserve existing model: got %q", cfg.LLM.Model)
	}
	if cfg.LLM.BaseURL != "http://45.205.26.177:9999" {
		t.Errorf("empty override should preserve existing baseURL: got %q", cfg.LLM.BaseURL)
	}
}

func TestApplyOverrides_OverridesEnvAndFile(t *testing.T) {
	t.Setenv("CLAW_MODEL", "env-model")

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}
	if cfg.LLM.Model != "env-model" {
		t.Fatalf("env model not applied: got %q", cfg.LLM.Model)
	}

	// CLI flag should win over env var
	cfg.ApplyOverrides("cli-model", "", "", "")
	if cfg.LLM.Model != "cli-model" {
		t.Errorf("CLI flag should override env: got %q, want %q", cfg.LLM.Model, "cli-model")
	}
}

// ---------------------------------------------------------------------------
// TestNewLogger (table-driven)
// ---------------------------------------------------------------------------

func TestNewLogger_JSON(t *testing.T) {
	cfg := Default()
	cfg.Logging.Format = "json"
	cfg.Logging.Level = "warn"

	logger, err := cfg.NewLogger()
	if err != nil {
		t.Fatalf("NewLogger() error: %v", err)
	}
	defer logger.Sync() //nolint:errcheck

	if !logger.Core().Enabled(zapcore.WarnLevel) {
		t.Error("logger should be enabled at warn level")
	}
	if logger.Core().Enabled(zapcore.InfoLevel) {
		t.Error("logger should not be enabled at info level when set to warn")
	}
}

func TestNewLogger_Console(t *testing.T) {
	cfg := Default()
	cfg.Logging.Format = "console"
	cfg.Logging.Level = "debug"

	logger, err := cfg.NewLogger()
	if err != nil {
		t.Fatalf("NewLogger() error: %v", err)
	}
	defer logger.Sync() //nolint:errcheck

	if !logger.Core().Enabled(zapcore.DebugLevel) {
		t.Error("logger should be enabled at debug level")
	}
}

func TestNewLogger_InvalidLevel(t *testing.T) {
	cfg := Default()
	cfg.Logging.Level = "not-a-real-level"

	logger, err := cfg.NewLogger()
	if err != nil {
		t.Fatalf("NewLogger() error: %v", err)
	}
	defer logger.Sync() //nolint:errcheck

	// Invalid level should fall back to info.
	if !logger.Core().Enabled(zapcore.InfoLevel) {
		t.Error("logger should be enabled at info level (fallback)")
	}
	if logger.Core().Enabled(zapcore.DebugLevel) {
		t.Error("logger should not be enabled at debug level when fallen back to info")
	}
}

func TestNewLogger_Levels(t *testing.T) {
	tests := []struct {
		name       string
		level      string
		format     string
		enabledAt  zapcore.Level
		disabledAt zapcore.Level
	}{
		{
			name:       "error level json",
			level:      "error",
			format:     "json",
			enabledAt:  zapcore.ErrorLevel,
			disabledAt: zapcore.WarnLevel,
		},
		{
			name:       "debug level console",
			level:      "debug",
			format:     "console",
			enabledAt:  zapcore.DebugLevel,
			disabledAt: zapcore.Level(-2), // nothing below debug
		},
		{
			name:       "warn level json",
			level:      "warn",
			format:     "json",
			enabledAt:  zapcore.WarnLevel,
			disabledAt: zapcore.InfoLevel,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Default()
			cfg.Logging.Level = tt.level
			cfg.Logging.Format = tt.format

			logger, err := cfg.NewLogger()
			if err != nil {
				t.Fatalf("NewLogger() error: %v", err)
			}
			defer logger.Sync() //nolint:errcheck

			if !logger.Core().Enabled(tt.enabledAt) {
				t.Errorf("logger should be enabled at %v", tt.enabledAt)
			}
			// Only check disabled if the level is meaningful (debug has nothing below).
			if tt.disabledAt >= zapcore.DebugLevel {
				if logger.Core().Enabled(tt.disabledAt) {
					t.Errorf("logger should NOT be enabled at %v", tt.disabledAt)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TestModelProfiles
// ---------------------------------------------------------------------------

func TestFindProfile(t *testing.T) {
	cfg := Default()
	cfg.LLM.Models = []ModelProfile{
		{Name: "gpt4o", Model: "gpt-5.2", BaseURL: "http://45.205.26.177:9999"},
		{Name: "deepseek", Model: "deepseek-chat", BaseURL: "https://api.deepseek.com/v1", APIKey: "sk-ds"},
		{Name: "claude", Model: "claude-3-5-sonnet", BaseURL: "https://api.anthropic.com/v1"},
	}

	tests := []struct {
		name      string
		query     string
		wantFound bool
		wantModel string
	}{
		{"exact match", "deepseek", true, "deepseek-chat"},
		{"case insensitive", "DeepSeek", true, "deepseek-chat"},
		{"not found", "nonexistent", false, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			profile, found := cfg.FindProfile(tt.query)
			if found != tt.wantFound {
				t.Errorf("FindProfile(%q) found = %v, want %v", tt.query, found, tt.wantFound)
			}
			if found && profile.Model != tt.wantModel {
				t.Errorf("FindProfile(%q) model = %q, want %q", tt.query, profile.Model, tt.wantModel)
			}
		})
	}
}

func TestActiveProfileName(t *testing.T) {
	cfg := Default()
	cfg.LLM.Model = "deepseek-chat"
	cfg.LLM.BaseURL = "https://api.deepseek.com/v1"
	cfg.LLM.Models = []ModelProfile{
		{Name: "gpt4o", Model: "gpt-5.2", BaseURL: "http://45.205.26.177:9999"},
		{Name: "deepseek", Model: "deepseek-chat", BaseURL: "https://api.deepseek.com/v1"},
	}

	if got := cfg.ActiveProfileName(); got != "deepseek" {
		t.Errorf("ActiveProfileName() = %q, want %q", got, "deepseek")
	}

	// If no match, returns raw model ID
	cfg.LLM.Model = "unknown-model"
	if got := cfg.ActiveProfileName(); got != "unknown-model" {
		t.Errorf("ActiveProfileName() = %q, want %q", got, "unknown-model")
	}
}

func TestEnsureActiveInProfiles(t *testing.T) {
	cfg := Default()
	cfg.Resolve() // Fills in Model = "gpt-5.2" from provider
	cfg.LLM.Models = []ModelProfile{
		{Name: "deepseek", Model: "deepseek-chat", BaseURL: "https://api.deepseek.com/v1"},
	}

	cfg.EnsureActiveInProfiles()

	if len(cfg.LLM.Models) != 2 {
		t.Fatalf("expected 2 profiles, got %d", len(cfg.LLM.Models))
	}
	// Active model should be prepended
	if cfg.LLM.Models[0].Model != "gpt-5.2" {
		t.Errorf("first profile model = %q, want %q", cfg.LLM.Models[0].Model, "gpt-5.2")
	}
}

func TestEnsureActiveInProfiles_AlreadyPresent(t *testing.T) {
	cfg := Default()
	cfg.Resolve() // Fills in Model = "gpt-5.2" from provider
	cfg.LLM.Models = []ModelProfile{
		{Name: "gpt4o", Model: "gpt-5.2", BaseURL: "http://45.205.26.177:9999"},
	}

	cfg.EnsureActiveInProfiles()

	if len(cfg.LLM.Models) != 1 {
		t.Errorf("should not duplicate: got %d profiles, want 1", len(cfg.LLM.Models))
	}
}

func TestLoad_ModelsFromConfigFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	raw := map[string]any{
		"llm": map[string]any{
			"model":    "gpt-5.2",
			"base_url": "http://45.205.26.177:9999",
			"models": []map[string]any{
				{"name": "gpt4o", "model": "gpt-5.2", "base_url": "http://45.205.26.177:9999"},
				{"name": "deepseek", "model": "deepseek-chat", "base_url": "https://api.deepseek.com/v1"},
			},
		},
	}
	data, _ := json.Marshal(raw)
	os.WriteFile(path, data, 0644)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}
	if len(cfg.LLM.Models) != 2 {
		t.Fatalf("expected 2 model profiles, got %d", len(cfg.LLM.Models))
	}
	if cfg.LLM.Models[1].Name != "deepseek" {
		t.Errorf("second profile name = %q, want %q", cfg.LLM.Models[1].Name, "deepseek")
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func assertConfigEqual(t *testing.T, got, want *Config) {
	t.Helper()

	// Server
	if got.Server.Port != want.Server.Port {
		t.Errorf("Server.Port = %d, want %d", got.Server.Port, want.Server.Port)
	}
	if got.Server.Host != want.Server.Host {
		t.Errorf("Server.Host = %q, want %q", got.Server.Host, want.Server.Host)
	}

	// LLM
	if got.LLM.APIKey != want.LLM.APIKey {
		t.Errorf("LLM.APIKey = %q, want %q", got.LLM.APIKey, want.LLM.APIKey)
	}
	if got.LLM.Model != want.LLM.Model {
		t.Errorf("LLM.Model = %q, want %q", got.LLM.Model, want.LLM.Model)
	}
	if got.LLM.BaseURL != want.LLM.BaseURL {
		t.Errorf("LLM.BaseURL = %q, want %q", got.LLM.BaseURL, want.LLM.BaseURL)
	}

	// Agent
	if got.Agent.Timeout != want.Agent.Timeout {
		t.Errorf("Agent.Timeout = %v, want %v", got.Agent.Timeout, want.Agent.Timeout)
	}
	if got.Agent.MaxConcurrentAgents != want.Agent.MaxConcurrentAgents {
		t.Errorf("Agent.MaxConcurrentAgents = %d, want %d", got.Agent.MaxConcurrentAgents, want.Agent.MaxConcurrentAgents)
	}
	if got.Agent.HealthInterval != want.Agent.HealthInterval {
		t.Errorf("Agent.HealthInterval = %v, want %v", got.Agent.HealthInterval, want.Agent.HealthInterval)
	}

	// MCP
	if got.MCP.Timeout != want.MCP.Timeout {
		t.Errorf("MCP.Timeout = %v, want %v", got.MCP.Timeout, want.MCP.Timeout)
	}

	// Logging
	if got.Logging.Level != want.Logging.Level {
		t.Errorf("Logging.Level = %q, want %q", got.Logging.Level, want.Logging.Level)
	}
	if got.Logging.Format != want.Logging.Format {
		t.Errorf("Logging.Format = %q, want %q", got.Logging.Format, want.Logging.Format)
	}
}

// ---------------------------------------------------------------------------
// TestHITLConfig_Validate
// ---------------------------------------------------------------------------

func TestHITLConfig_Validate_Valid(t *testing.T) {
	tests := []struct {
		name string
		cfg  HITLConfig
	}{
		{"empty step_confirmation", HITLConfig{StepConfirmation: "", InputTimeout: time.Minute}},
		{"none step_confirmation", HITLConfig{StepConfirmation: "none", InputTimeout: time.Minute}},
		{"all step_confirmation", HITLConfig{StepConfirmation: "all", InputTimeout: time.Minute}},
		{"zero timeout", HITLConfig{StepConfirmation: "none", InputTimeout: 0}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.cfg.Validate(); err != nil {
				t.Errorf("Validate() returned unexpected error: %v", err)
			}
		})
	}
}

func TestHITLConfig_Validate_InvalidStepConfirmation(t *testing.T) {
	cfg := HITLConfig{StepConfirmation: "invalid_value"}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error for invalid step_confirmation")
	}
}

func TestHITLConfig_Validate_NegativeTimeout(t *testing.T) {
	cfg := HITLConfig{StepConfirmation: "none", InputTimeout: -1 * time.Second}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error for negative input_timeout")
	}
}

// ---------------------------------------------------------------------------
// TestNewProviderEnvOverrides - 测试新增 Provider 的环境变量支持
// ---------------------------------------------------------------------------

func TestNewProviderEnvOverrides_Google(t *testing.T) {
	t.Setenv("GOOGLE_API_KEY", "google-key-from-env")
	t.Setenv("CLAW_PROVIDER", "google")

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}

	if cfg.LLM.Provider != "google" {
		t.Errorf("Provider = %q, want %q", cfg.LLM.Provider, "google")
	}
	if cfg.LLM.GoogleAPIKey != "google-key-from-env" {
		t.Errorf("GoogleAPIKey = %q, want %q", cfg.LLM.GoogleAPIKey, "google-key-from-env")
	}
}

func TestNewProviderEnvOverrides_Azure(t *testing.T) {
	t.Setenv("AZURE_OPENAI_API_KEY", "azure-key")
	t.Setenv("AZURE_DEPLOYMENT", "gpt-4-deployment")
	t.Setenv("AZURE_OPENAI_ENDPOINT", "https://my-resource.openai.azure.com")
	t.Setenv("CLAW_PROVIDER", "azure")

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}

	if cfg.LLM.Provider != "azure" {
		t.Errorf("Provider = %q, want %q", cfg.LLM.Provider, "azure")
	}
	if cfg.LLM.AzureAPIKey != "azure-key" {
		t.Errorf("AzureAPIKey = %q, want %q", cfg.LLM.AzureAPIKey, "azure-key")
	}
	if cfg.LLM.AzureDeployment != "gpt-4-deployment" {
		t.Errorf("AzureDeployment = %q, want %q", cfg.LLM.AzureDeployment, "gpt-4-deployment")
	}
	if cfg.LLM.AzureEndpoint != "https://my-resource.openai.azure.com" {
		t.Errorf("AzureEndpoint = %q, want %q", cfg.LLM.AzureEndpoint, "https://my-resource.openai.azure.com")
	}
}

func TestNewProviderEnvOverrides_PriorityGoogle(t *testing.T) {
	// GOOGLE_API_KEY 优先级高于 CLAW_GOOGLE_API_KEY
	t.Setenv("GOOGLE_API_KEY", "google-standard-key")
	t.Setenv("CLAW_GOOGLE_API_KEY", "claw-google-key")

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}

	if cfg.LLM.GoogleAPIKey != "google-standard-key" {
		t.Errorf("GoogleAPIKey = %q, want %q (GOOGLE_API_KEY should take priority)", cfg.LLM.GoogleAPIKey, "google-standard-key")
	}
}

func TestNewProviderEnvOverrides_PriorityAzure(t *testing.T) {
	// AZURE_OPENAI_API_KEY 优先级高于 CLAW_AZURE_API_KEY
	t.Setenv("AZURE_OPENAI_API_KEY", "azure-standard-key")
	t.Setenv("CLAW_AZURE_API_KEY", "claw-azure-key")

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}

	if cfg.LLM.AzureAPIKey != "azure-standard-key" {
		t.Errorf("AzureAPIKey = %q, want %q (AZURE_OPENAI_API_KEY should take priority)", cfg.LLM.AzureAPIKey, "azure-standard-key")
	}
}

// TestProviderResolve 测试 Provider 自动推断和配置填充
func TestProviderResolve_Google(t *testing.T) {
	cfg := Default()
	cfg.LLM.Provider = "google"
	cfg.Resolve()

	if cfg.LLM.BaseURL != "https://generativelanguage.googleapis.com" {
		t.Errorf("BaseURL = %q, want %q", cfg.LLM.BaseURL, "https://generativelanguage.googleapis.com")
	}
	if cfg.LLM.Model != "gemini-1.5-pro-latest" {
		t.Errorf("Model = %q, want %q", cfg.LLM.Model, "gemini-1.5-pro-latest")
	}
}

func TestProviderResolve_Azure(t *testing.T) {
	cfg := Default()
	cfg.LLM.Provider = "azure"
	cfg.Resolve()

	// Azure 没有默认 BaseURL（需要用户提供自定义 endpoint）
	if cfg.LLM.BaseURL != "" {
		t.Errorf("BaseURL = %q, want empty (Azure requires custom endpoint)", cfg.LLM.BaseURL)
	}
	if cfg.LLM.Model != "gpt-4o" {
		t.Errorf("Model = %q, want %q", cfg.LLM.Model, "gpt-4o")
	}
}

func TestProviderResolve_Groq(t *testing.T) {
	cfg := Default()
	cfg.LLM.Provider = "groq"
	cfg.Resolve()

	if cfg.LLM.BaseURL != "https://api.groq.com/openai" {
		t.Errorf("BaseURL = %q, want %q", cfg.LLM.BaseURL, "https://api.groq.com/openai")
	}
	if cfg.LLM.Model != "llama3-70b-8192" {
		t.Errorf("Model = %q, want %q", cfg.LLM.Model, "llama3-70b-8192")
	}
}

func TestProviderResolve_Mistral(t *testing.T) {
	cfg := Default()
	cfg.LLM.Provider = "mistral"
	cfg.Resolve()

	if cfg.LLM.BaseURL != "https://api.mistral.ai" {
		t.Errorf("BaseURL = %q, want %q", cfg.LLM.BaseURL, "https://api.mistral.ai")
	}
	if cfg.LLM.Model != "mistral-large-latest" {
		t.Errorf("Model = %q, want %q", cfg.LLM.Model, "mistral-large-latest")
	}
}

func TestProviderResolve_Bedrock(t *testing.T) {
	cfg := Default()
	cfg.LLM.Provider = "bedrock"
	cfg.Resolve()

	// Bedrock 没有默认 BaseURL（需要 AWS SDK 配置）
	if cfg.LLM.BaseURL != "" {
		t.Errorf("BaseURL = %q, want empty (Bedrock uses AWS SDK)", cfg.LLM.BaseURL)
	}
	if cfg.LLM.Model != "anthropic.claude-3-sonnet-20240229-v1:0" {
		t.Errorf("Model = %q, want %q", cfg.LLM.Model, "anthropic.claude-3-sonnet-20240229-v1:0")
	}
}

// ---------------------------------------------------------------------------
// TestInstructionURLs 和 StorePrivacy 配置解析
// ---------------------------------------------------------------------------

func TestLoad_InstructionURLs(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	raw := map[string]any{
		"instruction_urls": []string{
			"https://example.com/instructions.md",
			"https://example.com/rules.txt",
		},
	}
	data, _ := json.Marshal(raw)
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatalf("写入测试配置失败: %v", err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load(%q) 失败: %v", path, err)
	}

	if len(cfg.InstructionURLs) != 2 {
		t.Fatalf("InstructionURLs 长度期望 2，实际 %d", len(cfg.InstructionURLs))
	}
	if cfg.InstructionURLs[0] != "https://example.com/instructions.md" {
		t.Errorf("InstructionURLs[0] = %q，期望 %q", cfg.InstructionURLs[0], "https://example.com/instructions.md")
	}
	if cfg.InstructionURLs[1] != "https://example.com/rules.txt" {
		t.Errorf("InstructionURLs[1] = %q，期望 %q", cfg.InstructionURLs[1], "https://example.com/rules.txt")
	}
}

func TestLoad_InstructionURLs_Default(t *testing.T) {
	cfg := Default()
	if cfg.InstructionURLs != nil {
		t.Errorf("默认 InstructionURLs 应为 nil，实际 %v", cfg.InstructionURLs)
	}
}

func TestLoad_StorePrivacy(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	raw := map[string]any{
		"llm": map[string]any{
			"store_privacy": true,
		},
	}
	data, _ := json.Marshal(raw)
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatalf("写入测试配置失败: %v", err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load(%q) 失败: %v", path, err)
	}

	if !cfg.LLM.StorePrivacy {
		t.Error("StorePrivacy 期望 true，实际 false")
	}
}

func TestLoad_StorePrivacy_Default(t *testing.T) {
	cfg := Default()
	if cfg.LLM.StorePrivacy != DefaultStorePrivacy {
		t.Errorf("默认 StorePrivacy = %v，期望 %v", cfg.LLM.StorePrivacy, DefaultStorePrivacy)
	}
}

func TestLoad_ExpandsEnvVars(t *testing.T) {
	t.Setenv("TEST_API_KEY", "test-key-123")
	t.Setenv("CLAW_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")

	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	content := `{"llm": {"api_key": "${TEST_API_KEY}"}}`
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}
	if cfg.LLM.APIKey != "test-key-123" {
		t.Errorf("LLM.APIKey = %q，期望 %q（${TEST_API_KEY} 应被展开）", cfg.LLM.APIKey, "test-key-123")
	}
}
