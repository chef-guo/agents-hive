//go:build integration

package master

import (
	"context"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/chef-guo/agents-hive/internal/llm"
	"github.com/chef-guo/agents-hive/internal/mcphost"
	"github.com/chef-guo/agents-hive/internal/skills"
	"github.com/chef-guo/agents-hive/internal/store"
	"github.com/chef-guo/agents-hive/internal/subagent"
	"github.com/chef-guo/agents-hive/internal/toolctx"
)

// testRegistrar 简单的 AgentRegistrar 实现，用于集成测试
type testRegistrar struct {
	mu         sync.Mutex
	registered map[string]bool
}

func newTestRegistrar() *testRegistrar {
	return &testRegistrar{registered: make(map[string]bool)}
}

func (r *testRegistrar) RegisterDynamic(agent subagent.Agent) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.registered[agent.ID()] = true
	return nil
}

func (r *testRegistrar) UnregisterDynamic(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.registered, id)
}

// loadLLMConfigFromDB 从本地 Postgres 加载 LLM 配置（用于集成测试）
func loadLLMConfigFromDB(t *testing.T, logger *zap.Logger) (apiKey, baseURL, model, provider string) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	db, err := store.NewPostgresStore(ctx, store.PostgresConfig{
		Host:     "localhost",
		Port:     5432,
		Database: "claw",
		User:     "claw",
		Password: "claw123",
		SSLMode:  "disable",
		MaxConns: 2,
	}, logger)
	if err != nil {
		t.Skipf("无法连接到数据库，跳过集成测试: %v", err)
	}
	defer db.Close()

	providers, err := db.ListLLMProviders(ctx)
	if err != nil || len(providers) == 0 {
		t.Skip("数据库中没有 LLM provider 配置，跳过集成测试")
	}

	// 找到默认且启用的 provider
	for _, p := range providers {
		if p.IsDefault && p.Enabled && p.APIKey != "" {
			apiKey = p.APIKey
			baseURL = p.BaseURL
			provider = p.ProviderType

			// 从 LLM models 表获取默认模型
			models, _ := db.ListLLMModels(ctx)
			for _, m := range models {
				if m.IsDefault {
					model = m.Model
					break
				}
			}
			if model == "" {
				model = "gpt-4o-mini" // fallback
			}
			return
		}
	}

	t.Skip("未找到默认启用的 LLM provider，跳过集成测试")
	return
}

// TestForkExecutor_SuccessPath_Integration 使用真实 LLM 测试 fork 成功路径
func TestForkExecutor_SuccessPath_Integration(t *testing.T) {
	if testing.Short() {
		t.Skip("跳过集成测试（-short 模式）")
	}

	logger, _ := zap.NewDevelopment()

	// 从数据库加载真实 LLM 配置
	apiKey, baseURL, model, provider := loadLLMConfigFromDB(t, logger)
	t.Logf("使用 LLM: provider=%s, model=%s, baseURL=%s", provider, model, baseURL)

	// 创建真实 LLM client
	provDef := llm.LookupProvider(provider)
	llmClient := llm.NewClient(llm.ClientConfig{
		APIKey:   apiKey,
		BaseURL:  baseURL,
		Model:    model,
		Provider: provDef,
	}, logger)

	// 创建 AgentFactory 所需的依赖
	mcpHost := mcphost.NewHost(logger)
	skillReg := skills.NewRegistry(logger)
	toolBridge := skills.NewToolBridge(mcpHost, logger)

	factory := subagent.NewAgentFactory(
		llmClient,
		toolBridge,
		nil, // permMgr: 测试中不需要权限检查
		skillReg,
		newTestRegistrar(),
		logger,
	)

	// 创建 ForkExecutor
	fork := NewForkExecutor(factory, logger)

	// 构建一个简单的 skill
	skill := &skills.Skill{
		Metadata: skills.SkillMetadata{
			Name:    "test-fork-integration",
			Context: "fork",
		},
		Content: "你是一个简单的测试助手。请直接回复：\"fork 执行成功\"。不要回复其他内容。",
		Loaded:  skills.LevelFullContent,
	}

	// 执行 fork（给 60 秒超时，因为真实 LLM 调用需要时间）
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	ctx = toolctx.WithSessionID(ctx, "test-fork-session")
	defer cancel()

	result, err := fork.ExecuteForked(ctx, skill, skills.RenderContext{}, nil)
	if err != nil {
		t.Fatalf("fork 执行失败: %v", err)
	}

	t.Logf("fork 执行结果: %s", result)

	// 验证有返回结果（具体内容取决于 LLM 响应，不做精确匹配）
	if result == "" {
		t.Error("fork 返回了空结果")
	}
}

// TestForkExecutor_Timeout_Integration 测试 fork 超时处理
func TestForkExecutor_Timeout_Integration(t *testing.T) {
	if testing.Short() {
		t.Skip("跳过集成测试（-short 模式）")
	}

	logger, _ := zap.NewDevelopment()

	apiKey, baseURL, model, provider := loadLLMConfigFromDB(t, logger)
	t.Logf("使用 LLM: provider=%s, model=%s", provider, model)

	provDef := llm.LookupProvider(provider)
	llmClient := llm.NewClient(llm.ClientConfig{
		APIKey:   apiKey,
		BaseURL:  baseURL,
		Model:    model,
		Provider: provDef,
	}, logger)

	mcpHost := mcphost.NewHost(logger)
	skillReg := skills.NewRegistry(logger)
	toolBridge := skills.NewToolBridge(mcpHost, logger)

	factory := subagent.NewAgentFactory(
		llmClient,
		toolBridge,
		nil,
		skillReg,
		newTestRegistrar(),
		logger,
	)

	fork := NewForkExecutor(factory, logger)

	skill := &skills.Skill{
		Metadata: skills.SkillMetadata{
			Name:    "test-fork-timeout",
			Context: "fork",
		},
		Content: "请详细描述宇宙的起源。",
		Loaded:  skills.LevelFullContent,
	}

	// 给极短的超时，测试超时处理
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Millisecond)
	ctx = toolctx.WithSessionID(ctx, "test-fork-timeout-session")
	defer cancel()

	// 等一下让 context 先超时
	time.Sleep(5 * time.Millisecond)

	_, err := fork.ExecuteForked(ctx, skill, skills.RenderContext{}, nil)
	if err == nil {
		t.Fatal("预期超时错误，但执行成功了")
	}
	t.Logf("超时错误（预期）: %v", err)
}
