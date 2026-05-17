package master

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/chef-guo/agents-hive/internal/compaction"
	"github.com/chef-guo/agents-hive/internal/config"
	"github.com/chef-guo/agents-hive/internal/llm"
	"github.com/chef-guo/agents-hive/internal/skills"
	"github.com/chef-guo/agents-hive/internal/store"
	"github.com/chef-guo/agents-hive/internal/subagent"
	"github.com/chef-guo/agents-hive/internal/toolruntime"
)

func TestCompactMessages_NoCompression(t *testing.T) {
	messages := []llm.MessageWithTools{
		{Role: "user", Content: llm.NewTextContent("Hello")},
		{Role: "assistant", Content: llm.NewTextContent("Hi there!")},
		{Role: "user", Content: llm.NewTextContent("How are you?")},
	}

	// With high maxTokens, no compression should occur
	result := CompactMessages(messages, 10000)
	assert.Equal(t, len(messages), len(result), "should return all messages")
}

func TestCompactMessages_WithCompression(t *testing.T) {
	// Create messages that will exceed maxTokens
	messages := make([]llm.MessageWithTools, 20)
	for i := 0; i < 20; i++ {
		messages[i] = llm.MessageWithTools{
			Role:    "user",
			Content: llm.NewTextContent(strings.Repeat("Hello world, this is a long message. ", 10)), // ~370 chars each
		}
	}

	// Compact to ~1000 tokens (should keep about 3-4 recent messages)
	result := CompactMessages(messages, 1000)

	// Should have fewer messages than original
	assert.Less(t, len(result), len(messages), "should compress messages")

	// First message should be a summary
	assert.Equal(t, "system", result[0].Role, "first message should be system summary")
	assert.Contains(t, result[0].Content.Text(), "会话摘要", "should contain summary marker")
}

// TestCompactionPackage_TruncateCompactor 测试新 compaction 包的 TruncateCompactor
func TestCompactionPackage_TruncateCompactor(t *testing.T) {
	messages := make([]llm.MessageWithTools, 30)
	for i := range messages {
		messages[i] = llm.MessageWithTools{
			Role:    "user",
			Content: llm.NewTextContent(strings.Repeat("Test message content. ", 20)),
		}
	}

	tc := &compaction.TruncateCompactor{UseTiktoken: false}
	result, err := tc.Compact(context.Background(), messages, 500)

	assert.NoError(t, err)
	assert.Less(t, len(result), len(messages), "消息应该被压缩")
	assert.Equal(t, "system", result[0].Role, "第一条应该是摘要")
}

// TestCompactionPackage_ToolBudgetCompactor 测试新 compaction 包的 ToolResultBudgetCompactor
func TestCompactionPackage_ToolBudgetCompactor(t *testing.T) {
	bigContent := strings.Repeat("x", 10000)
	messages := []llm.MessageWithTools{
		{Role: "user", Content: llm.NewTextContent("第一轮")},
		{Role: "tool", Content: llm.NewTextContent(bigContent)},
		{Role: "user", Content: llm.NewTextContent("第二轮")},
		{Role: "tool", Content: llm.NewTextContent(bigContent)},
		{Role: "user", Content: llm.NewTextContent("第三轮")},
	}

	c := &compaction.ToolResultBudgetCompactor{
		ProtectedTurns:  2,
		OutputThreshold: 100,
		ContextBudget:   50000,
	}
	result, err := c.Compact(context.Background(), messages, 0)

	assert.NoError(t, err)
	// 保护区外的工具消息应被裁剪
	assert.Contains(t, result[1].Content.Text(), "输出已裁剪", "保护区外的大工具输出应被裁剪")
	// 保护区内的工具消息不应被裁剪
	assert.Equal(t, bigContent, result[3].Content.Text(), "保护区内工具输出不应被裁剪")
}

// TestCompactionPackage_SessionMemoryCompactor 测试新 compaction 包的 SessionMemoryCompactor
func TestCompactionPackage_SessionMemoryCompactor(t *testing.T) {
	messages := make([]llm.MessageWithTools, 10)
	for i := range messages {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		messages[i] = llm.MessageWithTools{
			Role:    role,
			Content: llm.NewTextContent("Test message " + formatInt(i)),
		}
	}

	c := &compaction.SessionMemoryCompactor{MaxExtractMessages: 20}
	result, err := c.Compact(context.Background(), messages, 0)

	assert.NoError(t, err)
	// 应该在头部插入 session memory
	assert.Greater(t, len(result), len(messages), "应该插入 session memory")
	assert.Equal(t, "system", result[0].Role)
	assert.Contains(t, result[0].Content.Text(), "会话记忆")
}

// TestCompactionPackage_HistorySnipCompactor 测试新 compaction 包的 HistorySnipCompactor
func TestCompactionPackage_HistorySnipCompactor(t *testing.T) {
	messages := make([]llm.MessageWithTools, 20)
	for i := range messages {
		messages[i] = llm.MessageWithTools{
			Role:    "user",
			Content: llm.NewTextContent("Message " + formatInt(i)),
		}
	}

	c := &compaction.HistorySnipCompactor{
		KeepFirst: true,
		KeepLast:  4,
	}
	result, err := c.Compact(context.Background(), messages, 0)

	assert.NoError(t, err)
	// 应该保留首条 + 摘要 + 最后4条 = 6条
	assert.Equal(t, 6, len(result), "应该保留首条+摘要+最后4条")
	assert.Equal(t, "Message 0", result[0].Content.Text(), "第一条应该是原始首条")
	assert.Equal(t, "system", result[1].Role, "第二条应该是摘要")
}

// TestCompactionPackage_Pipeline 测试新 compaction 包的 Pipeline
func TestCompactionPackage_Pipeline(t *testing.T) {
	messages := make([]llm.MessageWithTools, 10)
	for i := range messages {
		messages[i] = llm.MessageWithTools{
			Role:    "user",
			Content: llm.NewTextContent("Test message " + formatInt(i)),
		}
	}

	registry := map[string]compaction.Compactor{
		"tool_budget":    &compaction.ToolResultBudgetCompactor{ProtectedTurns: 2},
		"session_memory": &compaction.SessionMemoryCompactor{},
		"truncate":       &compaction.TruncateCompactor{UseTiktoken: false},
	}

	pipeline, skipped := compaction.NewPipeline(registry, []string{"tool_budget", "session_memory", "truncate"})
	assert.Empty(t, skipped, "不应有跳过的阶段")
	result, err := pipeline.Compact(context.Background(), messages, 5000)

	assert.NoError(t, err)
	assert.NotNil(t, result)
	assert.Equal(t, []string{"tool_budget", "session_memory", "truncate"}, pipeline.StageNames())
}

func TestFormatInt(t *testing.T) {
	tests := []struct {
		input    int
		expected string
	}{
		{0, "0"},
		{1, "1"},
		{10, "10"},
		{123, "123"},
		{-1, "-1"},
		{-123, "-123"},
	}

	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			result := formatInt(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestPrepareMessagesForLLM(t *testing.T) {
	// Create a large message history
	messages := make([]llm.MessageWithTools, 50)
	for i := 0; i < 50; i++ {
		messages[i] = llm.MessageWithTools{
			Role:    "user",
			Content: llm.NewTextContent(strings.Repeat("Test message content. ", 50)), // ~1100 chars each
		}
	}

	result := PrepareMessagesForLLM(messages)

	// Should be compressed
	assert.Less(t, len(result), len(messages), "should compress large history")
}

// 测试新的压缩上下文 API
func TestCompactMessagesWithContext_Disabled(t *testing.T) {
	messages := []llm.MessageWithTools{
		{Role: "user", Content: llm.NewTextContent("Hello")},
		{Role: "assistant", Content: llm.NewTextContent("Hi!")},
	}

	ctx := CompactionContext{
		Config: config.CompactionConfig{
			Enabled: false,
		},
	}

	result, stats := CompactMessagesWithContext(context.Background(), messages, ctx)

	assert.Equal(t, messages, result)
	assert.Equal(t, 2, stats.Original)
	assert.Equal(t, 2, stats.Remaining)
	assert.Equal(t, 0, stats.Compressed)
}

func TestCompactMessagesWithContext_TruncateStrategy(t *testing.T) {
	// 创建超限消息
	messages := make([]llm.MessageWithTools, 30)
	for i := range messages {
		messages[i] = llm.MessageWithTools{
			Role:    "user",
			Content: llm.NewTextContent(strings.Repeat("Test. ", 50)), // ~300 chars
		}
	}

	ctx := CompactionContext{
		Config: config.CompactionConfig{
			Enabled:       true,
			Strategy:      config.StrategyTruncate,
			MaxTokens:     1000,
			ReserveTokens: 100,
			UseTiktoken:   false,
		},
	}

	result, stats := CompactMessagesWithContext(context.Background(), messages, ctx)

	// 应该被压缩
	assert.Less(t, len(result), len(messages))
	assert.Equal(t, 30, stats.Original)
	assert.Greater(t, stats.Compressed, 0)
	assert.Equal(t, string(config.StrategyTruncate), stats.Strategy)

	// 第一条应该是摘要
	assert.Equal(t, "system", result[0].Role)
	assert.Contains(t, result[0].Content.Text(), "会话摘要")
}

func TestCompactMessagesWithContext_WithTiktoken(t *testing.T) {
	tc, err := llm.NewTokenCounter()
	if err != nil {
		t.Skip("tiktoken 初始化失败，跳过测试")
	}

	// 创建足够多的消息以触发压缩（每条约 8 tokens）
	messages := make([]llm.MessageWithTools, 100)
	for i := range messages {
		messages[i] = llm.MessageWithTools{
			Role:    "user",
			Content: llm.NewTextContent("Hello, this is a test message with enough content to trigger compaction."),
		}
	}

	ctx := CompactionContext{
		Config: config.CompactionConfig{
			Enabled:     true,
			Strategy:    config.StrategyTruncate,
			MaxTokens:   500,
			UseTiktoken: true,
		},
		TokenCounter: tc,
	}

	result, stats := CompactMessagesWithContext(context.Background(), messages, ctx)

	assert.Less(t, len(result), len(messages))
	assert.Greater(t, stats.Compressed, 0, "应该有消息被压缩")
}

func TestCompactMessagesWithContext_LLMSummary_Fallback(t *testing.T) {
	// 测试 LLM 摘要策略在没有 LLMClient 时降级到截断
	messages := make([]llm.MessageWithTools, 100)
	for i := range messages {
		messages[i] = llm.MessageWithTools{
			Role:    "user",
			Content: llm.NewTextContent("Test message with enough content to trigger compaction. " + formatInt(i)),
		}
	}

	logger, _ := zap.NewDevelopment()

	ctx := CompactionContext{
		Config: config.CompactionConfig{
			Enabled:   true,
			Strategy:  config.StrategyLLMSummary,
			MaxTokens: 500,
		},
		LLMClient: nil, // 没有 LLM Client
		Logger:    logger,
	}

	result, stats := CompactMessagesWithContext(context.Background(), messages, ctx)

	// 应该降级到截断策略
	assert.Less(t, len(result), len(messages), "消息应该被压缩")
	assert.Equal(t, string(config.StrategyTruncate), stats.Strategy, "应该使用截断策略")
	assert.Greater(t, stats.Compressed, 0, "应该有消息被压缩")
}

func TestCompactionStats(t *testing.T) {
	stats := &CompactionStats{
		Original:       50,
		Remaining:      15,
		Compressed:     35,
		Strategy:       "truncate",
		OriginalToken:  10000,
		RemainingToken: 2000,
	}

	assert.Equal(t, 50, stats.Original)
	assert.Equal(t, 15, stats.Remaining)
	assert.Equal(t, 35, stats.Compressed)
	assert.Equal(t, "truncate", stats.Strategy)
	assert.Equal(t, 10000, stats.OriginalToken)
	assert.Equal(t, 2000, stats.RemainingToken)
}

// 基准测试
func BenchmarkCompactMessages_Small(b *testing.B) {
	messages := make([]llm.MessageWithTools, 10)
	for i := range messages {
		messages[i] = llm.MessageWithTools{
			Role:    "user",
			Content: llm.NewTextContent("Short message"),
		}
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		CompactMessages(messages, 1000)
	}
}

func BenchmarkCompactMessages_Large(b *testing.B) {
	messages := make([]llm.MessageWithTools, 100)
	for i := range messages {
		messages[i] = llm.MessageWithTools{
			Role:    "user",
			Content: llm.NewTextContent(strings.Repeat("This is a longer test message. ", 10)),
		}
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		CompactMessages(messages, 8000)
	}
}

func BenchmarkCompactMessagesWithContext_Heuristic(b *testing.B) {
	messages := make([]llm.MessageWithTools, 50)
	for i := range messages {
		messages[i] = llm.MessageWithTools{
			Role:    "user",
			Content: llm.NewTextContent("Test message with some content."),
		}
	}

	ctx := CompactionContext{
		Config: config.CompactionConfig{
			Enabled:     true,
			Strategy:    config.StrategyTruncate,
			MaxTokens:   5000,
			UseTiktoken: false,
		},
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		CompactMessagesWithContext(context.Background(), messages, ctx)
	}
}

// 懒惰模式测试

func TestCompactMessagesWithContext_LazyMode_SkipBelowThreshold(t *testing.T) {
	messages := make([]llm.MessageWithTools, 5)
	for i := range messages {
		messages[i] = llm.MessageWithTools{
			Role:    "user",
			Content: llm.NewTextContent("Short test message."),
		}
	}

	ctx := CompactionContext{
		Config: config.CompactionConfig{
			Enabled:       true,
			Strategy:      config.StrategyTruncate,
			MaxTokens:     5000,
			UseTiktoken:   false,
			LazyMode:      true,
			LazyThreshold: 1000,
		},
	}

	result, stats := CompactMessagesWithContext(context.Background(), messages, ctx)

	assert.Equal(t, len(messages), len(result), "消息数量不应改变")
	assert.True(t, stats.LazySkipped, "应该标记为懒惰跳过")
	assert.Equal(t, 0, stats.Compressed, "不应该有消息被压缩")
	assert.Equal(t, 5, stats.Original)
	assert.Equal(t, 5, stats.Remaining)
}

func TestCompactMessagesWithContext_LazyMode_TriggerAboveThreshold(t *testing.T) {
	messages := make([]llm.MessageWithTools, 100)
	for i := range messages {
		messages[i] = llm.MessageWithTools{
			Role:    "user",
			Content: llm.NewTextContent(strings.Repeat("Long test message with enough content. ", 10)),
		}
	}

	ctx := CompactionContext{
		Config: config.CompactionConfig{
			Enabled:       true,
			Strategy:      config.StrategyTruncate,
			MaxTokens:     500,
			UseTiktoken:   false,
			LazyMode:      true,
			LazyThreshold: 100,
		},
	}

	result, stats := CompactMessagesWithContext(context.Background(), messages, ctx)

	assert.Less(t, len(result), len(messages), "消息应该被压缩")
	assert.False(t, stats.LazySkipped, "不应该标记为懒惰跳过")
	assert.Greater(t, stats.Compressed, 0, "应该有消息被压缩")
}

func TestCompactMessagesWithContext_LazyMode_Disabled(t *testing.T) {
	messages := make([]llm.MessageWithTools, 5)
	for i := range messages {
		messages[i] = llm.MessageWithTools{
			Role:    "user",
			Content: llm.NewTextContent("Test message."),
		}
	}

	ctx := CompactionContext{
		Config: config.CompactionConfig{
			Enabled:       true,
			Strategy:      config.StrategyTruncate,
			MaxTokens:     5000,
			UseTiktoken:   false,
			LazyMode:      false,
			LazyThreshold: 100,
		},
	}

	result, stats := CompactMessagesWithContext(context.Background(), messages, ctx)

	assert.False(t, stats.LazySkipped, "懒惰模式禁用时不应该有 LazySkipped 标记")
	assert.Equal(t, 0, stats.Compressed)
	assert.Equal(t, len(messages), len(result))
}

func TestEvaluateCompactionNeed_BelowThreshold(t *testing.T) {
	messages := make([]llm.MessageWithTools, 5)
	for i := range messages {
		messages[i] = llm.MessageWithTools{
			Role:    "user",
			Content: llm.NewTextContent("Short message."),
		}
	}

	ctx := CompactionContext{
		Config: config.CompactionConfig{
			Enabled:       true,
			LazyMode:      true,
			LazyThreshold: 1000,
			UseTiktoken:   false,
		},
	}

	rec := EvaluateCompactionNeed(messages, ctx)

	assert.False(t, rec.ShouldCompact, "不应该建议压缩")
	assert.Less(t, rec.CurrentTokens, rec.Threshold, "当前 token 应该低于阈值")
	assert.Equal(t, "未达到压缩阈值", rec.Reason)
}

func TestEvaluateCompactionNeed_AboveThreshold(t *testing.T) {
	messages := make([]llm.MessageWithTools, 100)
	for i := range messages {
		messages[i] = llm.MessageWithTools{
			Role:    "user",
			Content: llm.NewTextContent(strings.Repeat("Long message. ", 20)),
		}
	}

	ctx := CompactionContext{
		Config: config.CompactionConfig{
			Enabled:       true,
			LazyMode:      true,
			LazyThreshold: 100,
			UseTiktoken:   false,
		},
	}

	rec := EvaluateCompactionNeed(messages, ctx)

	assert.True(t, rec.ShouldCompact, "应该建议压缩")
	assert.Greater(t, rec.CurrentTokens, rec.Threshold, "当前 token 应该超过阈值")
	assert.Equal(t, "超过压缩阈值", rec.Reason)
}

func TestEvaluateCompactionNeed_Disabled(t *testing.T) {
	messages := make([]llm.MessageWithTools, 10)
	for i := range messages {
		messages[i] = llm.MessageWithTools{
			Role:    "user",
			Content: llm.NewTextContent("Test."),
		}
	}

	ctx := CompactionContext{
		Config: config.CompactionConfig{
			Enabled: false,
		},
	}

	rec := EvaluateCompactionNeed(messages, ctx)

	assert.False(t, rec.ShouldCompact, "压缩禁用时不应该建议压缩")
	assert.Equal(t, "压缩已禁用", rec.Reason)
}

// 集成测试：验证懒惰模式统计追踪

func TestLazyMode_StatisticsTracking(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	skillReg := skills.NewRegistry(logger)
	agentReg := subagent.NewRegistry(logger)
	st := store.NewMemoryStore()

	m := NewMaster(Config{
		ContextCompression: config.CompactionConfig{
			Enabled:        true,
			LazyMode:       true,
			LazyThreshold:  1000,
			MaxTokens:      500,
			UseTiktoken:    false,
			PipelineStages: []string{"tool_budget", "truncate"},
		},
	}, config.HITLConfig{}, agentReg, skillReg, st, logger)

	// 重置统计
	m.ResetCompactionStats()

	// 测试 1: 少量消息，应该跳过
	smallMessages := make([]llm.MessageWithTools, 5)
	for i := range smallMessages {
		smallMessages[i] = llm.MessageWithTools{
			Role:    "user",
			Content: llm.NewTextContent("Short."),
		}
	}
	_ = m.prepareMessagesWithCompression(context.Background(), nil, smallMessages)

	stats := m.GetCompactionStats()
	assert.Equal(t, uint64(0), stats.TriggerCount, "小消息不应触发压缩")
	assert.Equal(t, uint64(1), stats.SkippedCount, "应该跳过一次")

	// 测试 2: 大量消息，应该触发
	largeMessages := make([]llm.MessageWithTools, 100)
	for i := range largeMessages {
		largeMessages[i] = llm.MessageWithTools{
			Role:    "user",
			Content: llm.NewTextContent(strings.Repeat("Long message. ", 20)),
		}
	}
	_ = m.prepareMessagesWithCompression(context.Background(), nil, largeMessages)

	stats = m.GetCompactionStats()
	assert.Equal(t, uint64(1), stats.TriggerCount, "大消息应触发压缩")
	assert.Equal(t, uint64(1), stats.SkippedCount, "跳过次数不变")
	assert.Greater(t, stats.AverageDelay, time.Duration(0), "应该有平均延迟")

	// 测试 3: 再次小消息，跳过次数增加
	_ = m.prepareMessagesWithCompression(context.Background(), nil, smallMessages)

	stats = m.GetCompactionStats()
	assert.Equal(t, uint64(1), stats.TriggerCount, "触发次数不变")
	assert.Equal(t, uint64(2), stats.SkippedCount, "跳过次数增加")
}

func TestRemoveStaleKBNoBindingResultsWhenSessionBound(t *testing.T) {
	staleContent := toolruntime.RecoverableToolCallErrorContent("kb_unavailable_or_not_bound", "kb.doc.meta 未执行: kb: no kb bound")
	messages := []llm.MessageWithTools{
		{Role: "user", Content: llm.NewTextContent("知识库有哪些内容")},
		{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "call-kb-old", Name: "kb.doc.meta", Arguments: json.RawMessage(`{}`)}}},
		{Role: "tool", ToolCallID: "call-kb-old", ToolName: "kb.doc.meta", IsError: true, Content: llm.NewTextContent(staleContent), Metadata: map[string]string{"error_kind": "kb_unavailable_or_not_bound"}},
		{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "call-read", Name: "read_file", Arguments: json.RawMessage(`{"path":"README.md"}`)}}},
		{Role: "tool", ToolCallID: "call-read", ToolName: "read_file", Content: llm.NewTextContent("README")},
		{Role: "user", Content: llm.NewTextContent("再列一下知识库")},
	}

	got := removeStaleKBNoBindingResults(messages, true, zap.NewNop())

	require.Len(t, got, 4)
	for _, msg := range got {
		require.NotContains(t, msg.Content.Text(), "kb_unavailable_or_not_bound")
		require.NotEqual(t, "call-kb-old", msg.ToolCallID)
		for _, call := range msg.ToolCalls {
			require.NotEqual(t, "call-kb-old", call.ID)
		}
	}
	require.Equal(t, "call-read", got[1].ToolCalls[0].ID)
	require.Equal(t, "call-read", got[2].ToolCallID)
}

// PruneToolOutputs 测试

func TestPruneToolOutputs(t *testing.T) {
	t.Run("空消息返回空", func(t *testing.T) {
		result := PruneToolOutputs(nil, 2, 100, 500)
		assert.Nil(t, result)

		result = PruneToolOutputs([]llm.MessageWithTools{}, 2, 100, 500)
		assert.Empty(t, result)
	})

	t.Run("无工具消息不裁剪", func(t *testing.T) {
		messages := []llm.MessageWithTools{
			{Role: "user", Content: llm.NewTextContent("你好")},
			{Role: "assistant", Content: llm.NewTextContent("你好！")},
			{Role: "user", Content: llm.NewTextContent("再见")},
		}
		result := PruneToolOutputs(messages, 2, 100, 500)
		assert.Equal(t, len(messages), len(result))
		for i := range messages {
			assert.Equal(t, messages[i].Content.Text(), result[i].Content.Text())
		}
	})

	t.Run("保护最近对话不被裁剪", func(t *testing.T) {
		bigContent := strings.Repeat("x", 10000)
		messages := []llm.MessageWithTools{
			{Role: "user", Content: llm.NewTextContent("第一轮")},
			{Role: "tool", Content: llm.NewTextContent(bigContent)},
			{Role: "user", Content: llm.NewTextContent("第二轮")},
			{Role: "tool", Content: llm.NewTextContent(bigContent)},
			{Role: "user", Content: llm.NewTextContent("第三轮")},
		}
		result := PruneToolOutputs(messages, 2, 100, 50000)

		assert.Equal(t, bigContent, result[3].Content.Text(), "保护区内工具输出不应被裁剪")
		assert.Contains(t, result[1].Content.Text(), "输出已裁剪", "保护区外的大工具输出应被裁剪")
	})

	t.Run("小输出不裁剪", func(t *testing.T) {
		messages := []llm.MessageWithTools{
			{Role: "user", Content: llm.NewTextContent("问题")},
			{Role: "tool", Content: llm.NewTextContent("小输出")},
			{Role: "user", Content: llm.NewTextContent("最近问题1")},
			{Role: "user", Content: llm.NewTextContent("最近问题2")},
		}
		result := PruneToolOutputs(messages, 2, 1000, 50000)
		assert.Equal(t, "小输出", result[1].Content.Text(), "小于阈值的输出不应被裁剪")
	})

	t.Run("累积预算超限时裁剪", func(t *testing.T) {
		messages := []llm.MessageWithTools{
			{Role: "tool", Content: llm.NewTextContent(strings.Repeat("a", 300))},
			{Role: "tool", Content: llm.NewTextContent(strings.Repeat("b", 300))},
			{Role: "tool", Content: llm.NewTextContent(strings.Repeat("c", 300))},
			{Role: "user", Content: llm.NewTextContent("最近1")},
			{Role: "user", Content: llm.NewTextContent("最近2")},
		}
		result := PruneToolOutputs(messages, 2, 10000, 500)

		pruned := false
		for i := 0; i < 3; i++ {
			if strings.Contains(result[i].Content.Text(), "输出已裁剪") {
				pruned = true
			}
		}
		assert.True(t, pruned, "累积预算超限时应有工具输出被裁剪")
	})

	t.Run("不修改原始消息", func(t *testing.T) {
		bigContent := strings.Repeat("x", 10000)
		messages := []llm.MessageWithTools{
			{Role: "tool", Content: llm.NewTextContent(bigContent)},
			{Role: "user", Content: llm.NewTextContent("最近1")},
			{Role: "user", Content: llm.NewTextContent("最近2")},
		}
		original := messages[0].Content.Text()
		_ = PruneToolOutputs(messages, 2, 100, 50000)
		assert.Equal(t, original, messages[0].Content.Text(), "原始消息不应被修改")
	})
}
