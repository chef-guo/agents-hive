package llm

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/openai/openai-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/chef-guo/agents-hive/internal/mcphost"
)

func TestEmptyContentTransformer(t *testing.T) {
	logger := zap.NewNop()
	transformer := NewEmptyContentTransformer(logger)

	tests := []struct {
		name     string
		provider string
		messages []MessageWithTools
		wantMod  bool // 是否期望修改
	}{
		{
			name:     "Anthropic 空内容替换为空格",
			provider: "anthropic",
			messages: []MessageWithTools{
				{Role: "user", Content: NewTextContent("hello")},
				{Role: "assistant", Content: NewTextContent("")},
			},
			wantMod: true,
		},
		{
			name:     "非 Anthropic 不修改",
			provider: "openai",
			messages: []MessageWithTools{
				{Role: "assistant", Content: NewTextContent("")},
			},
			wantMod: false,
		},
		{
			name:     "Anthropic tool 消息不修改",
			provider: "anthropic",
			messages: []MessageWithTools{
				{Role: "tool", Content: NewTextContent(""), ToolCallID: "call_1"},
			},
			wantMod: false,
		},
		{
			name:     "不区分大小写",
			provider: "Anthropic",
			messages: []MessageWithTools{
				{Role: "assistant", Content: NewTextContent("")},
			},
			wantMod: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := transformer.Transform(tt.messages, tt.provider)
			require.Len(t, result, len(tt.messages))

			if tt.wantMod {
				modified := false
				for i, msg := range result {
					if msg.Content.Text() != tt.messages[i].Content.Text() {
						modified = true
						assert.Equal(t, " ", msg.Content.Text())
					}
				}
				assert.True(t, modified, "期望至少有一条消息被修改")
			} else {
				for i, msg := range result {
					assert.Equal(t, tt.messages[i].Content.Text(), msg.Content.Text())
				}
			}
		})
	}
}

func TestToolCallIDTransformer(t *testing.T) {
	logger := zap.NewNop()
	transformer := NewToolCallIDTransformer(logger)

	// 记录当前计数器值，用于推算生成的 ID
	base := atomic.LoadInt64(&toolCallIDCounter)

	tests := []struct {
		name     string
		provider string
		messages []MessageWithTools
		wantIDs  []string
	}{
		{
			name:     "Mistral 注入缺失的 tool_call_id",
			provider: "mistral",
			messages: []MessageWithTools{
				{Role: "tool", Content: NewTextContent("result1"), ToolCallID: ""},
				{Role: "tool", Content: NewTextContent("result2"), ToolCallID: "existing"},
				{Role: "tool", Content: NewTextContent("result3"), ToolCallID: ""},
			},
			wantIDs: []string{
				fmt.Sprintf("call_%d", base+1),
				"existing",
				fmt.Sprintf("call_%d", base+2),
			},
		},
		{
			name:     "非 Mistral 不注入",
			provider: "openai",
			messages: []MessageWithTools{
				{Role: "tool", Content: NewTextContent("result"), ToolCallID: ""},
			},
			wantIDs: []string{""},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := transformer.Transform(tt.messages, tt.provider)
			require.Len(t, result, len(tt.messages))

			for i, wantID := range tt.wantIDs {
				assert.Equal(t, wantID, result[i].ToolCallID, "消息 %d 的 tool_call_id 不匹配", i)
			}
		})
	}
}

// TestToolCallIDTransformer_UniqueAcrossCalls 验证跨调用生成的 ID 不重复
func TestToolCallIDTransformer_UniqueAcrossCalls(t *testing.T) {
	logger := zap.NewNop()
	transformer := NewToolCallIDTransformer(logger)

	messages := []MessageWithTools{
		{Role: "tool", Content: NewTextContent("result"), ToolCallID: ""},
	}

	result1 := transformer.Transform(messages, "mistral")
	result2 := transformer.Transform(messages, "mistral")

	id1 := result1[0].ToolCallID
	id2 := result2[0].ToolCallID

	assert.NotEqual(t, id1, id2, "跨调用生成的 ID 应全局唯一")
	assert.True(t, strings.HasPrefix(id1, "call_"), "ID 应以 call_ 开头")
	assert.True(t, strings.HasPrefix(id2, "call_"), "ID 应以 call_ 开头")
}

func TestUnsupportedFieldTransformer(t *testing.T) {
	logger := zap.NewNop()
	transformer := NewUnsupportedFieldTransformer(logger)

	t.Run("Anthropic 空 arguments 替换为空对象", func(t *testing.T) {
		messages := []MessageWithTools{
			{
				Role: "assistant",
				ToolCalls: []ToolCall{
					{ID: "1", Name: "test", Arguments: nil},
					{ID: "2", Name: "test2", Arguments: json.RawMessage(`{"key":"val"}`)},
				},
			},
		}

		result := transformer.Transform(messages, "anthropic")
		require.Len(t, result, 1)
		require.Len(t, result[0].ToolCalls, 2)
		assert.Equal(t, json.RawMessage(`{}`), result[0].ToolCalls[0].Arguments)
		assert.Equal(t, json.RawMessage(`{"key":"val"}`), result[0].ToolCalls[1].Arguments)
	})

	t.Run("非 Anthropic 不修改", func(t *testing.T) {
		messages := []MessageWithTools{
			{
				Role: "assistant",
				ToolCalls: []ToolCall{
					{ID: "1", Name: "test", Arguments: nil},
				},
			},
		}

		result := transformer.Transform(messages, "openai")
		assert.Nil(t, result[0].ToolCalls[0].Arguments)
	})
}

func TestChainTransformer(t *testing.T) {
	logger := zap.NewNop()
	chain := DefaultTransformer(logger)

	t.Run("链式转换器按顺序执行", func(t *testing.T) {
		messages := []MessageWithTools{
			{Role: "assistant", Content: NewTextContent("")},
			{Role: "tool", Content: NewTextContent("result"), ToolCallID: ""},
		}

		// Anthropic provider: 应该处理空内容
		result := chain.Transform(messages, "anthropic")
		assert.Equal(t, " ", result[0].Content.Text())

		// Mistral provider: 应该注入 tool_call_id
		result = chain.Transform(messages, "mistral")
		assert.True(t, strings.HasPrefix(result[1].ToolCallID, "call_"), "tool_call_id 应以 call_ 开头")
		assert.NotEmpty(t, result[1].ToolCallID)
	})
}

func TestDetectProvider(t *testing.T) {
	tests := []struct {
		baseURL  string
		expected string
	}{
		{"https://api.anthropic.com/v1", "anthropic"},
		{"https://api.mistral.ai/v1", "mistral"},
		{"https://www.gmini.xyz/v1", "openai"},
		{"https://api.deepseek.com/v1", "deepseek"},
		{"https://generativelanguage.googleapis.com/v1beta", "google"},
		{"https://gemini.example.com/v1", "google"},
		{"https://custom-api.example.com/v1", "openai"},
		{"", "openai"},
	}

	for _, tt := range tests {
		t.Run(tt.baseURL, func(t *testing.T) {
			result := DetectProvider(tt.baseURL)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestGetModelMeta(t *testing.T) {
	t.Run("已注册模型返回元数据", func(t *testing.T) {
		meta := GetModelMeta("gpt-4o")
		require.NotNil(t, meta)
		assert.Equal(t, "GPT-4o", meta.Name)
		assert.Equal(t, 128000, meta.ContextWindow)
		assert.True(t, meta.SupportsVision())
		assert.True(t, meta.SupportsTools())
		assert.True(t, meta.SupportsJSON())
		assert.Greater(t, meta.CostPerInputToken, 0.0)
		// 验证 Capabilities 字段
		assert.True(t, meta.Capabilities.Vision)
		assert.True(t, meta.Capabilities.ToolUse)
		assert.True(t, meta.Capabilities.JSON)
		assert.True(t, meta.Capabilities.Streaming)
		assert.True(t, meta.Capabilities.PromptCaching)
		assert.Equal(t, "auto", meta.Capabilities.CacheType)
	})

	t.Run("未注册模型返回 nil", func(t *testing.T) {
		meta := GetModelMeta("nonexistent-model")
		assert.Nil(t, meta)
	})

	t.Run("DeepSeek Reasoner 不支持工具但支持推理", func(t *testing.T) {
		meta := GetModelMeta("deepseek-reasoner")
		require.NotNil(t, meta)
		assert.False(t, meta.SupportsTools())
		assert.False(t, meta.SupportsJSON())
		assert.True(t, meta.Capabilities.Reasoning)
		assert.False(t, meta.Capabilities.ToolUse)
		assert.Contains(t, meta.Capabilities.ReasoningEfforts, "high")
	})

	t.Run("Claude 3.5 Sonnet 支持 prompt caching 和 PDF", func(t *testing.T) {
		meta := GetModelMeta("claude-3-5-sonnet-20241022")
		require.NotNil(t, meta)
		assert.True(t, meta.Capabilities.PromptCaching)
		assert.Equal(t, "ephemeral", meta.Capabilities.CacheType)
		assert.True(t, meta.Capabilities.PDF)
		assert.True(t, meta.Capabilities.Vision)
		assert.False(t, meta.Capabilities.Reasoning)
	})

	t.Run("OpenAI o1 支持推理", func(t *testing.T) {
		meta := GetModelMeta("o1")
		require.NotNil(t, meta)
		assert.True(t, meta.Capabilities.Reasoning)
		assert.Equal(t, []string{"low", "medium", "high"}, meta.Capabilities.ReasoningEfforts)
		assert.True(t, meta.Capabilities.Vision)
		assert.True(t, meta.Capabilities.ToolUse)
	})

	t.Run("Gemini 1.5 Pro 支持音频和 PDF", func(t *testing.T) {
		meta := GetModelMeta("gemini-1.5-pro")
		require.NotNil(t, meta)
		assert.True(t, meta.Capabilities.Audio)
		assert.True(t, meta.Capabilities.PDF)
		assert.True(t, meta.Capabilities.Vision)
		assert.False(t, meta.Capabilities.Reasoning)
	})

	t.Run("Gemini 2.0 Flash 已注册", func(t *testing.T) {
		meta := GetModelMeta("gemini-2.0-flash")
		require.NotNil(t, meta)
		assert.Equal(t, "Gemini 2.0 Flash", meta.Name)
		assert.True(t, meta.Capabilities.Vision)
		assert.True(t, meta.Capabilities.Audio)
	})

	t.Run("Claude 3 Haiku 已注册", func(t *testing.T) {
		meta := GetModelMeta("claude-3-haiku-20240307")
		require.NotNil(t, meta)
		assert.Equal(t, "Claude 3 Haiku", meta.Name)
		assert.Equal(t, "ephemeral", meta.Capabilities.CacheType)
	})

	t.Run("o3-mini 支持推理", func(t *testing.T) {
		meta := GetModelMeta("o3-mini")
		require.NotNil(t, meta)
		assert.True(t, meta.Capabilities.Reasoning)
		assert.False(t, meta.Capabilities.Vision)
	})
}

func TestListModelMetas(t *testing.T) {
	metas := ListModelMetas()
	assert.Greater(t, len(metas), 10, "至少应该有 10 个模型注册")

	metas["test-model"] = ModelMeta{Name: "test"}
	assert.Nil(t, GetModelMeta("test-model"), "修改副本不应影响原始注册表")
}

// --- PromptCachingTransformer 测试 ---

func TestPromptCachingTransformer_Anthropic(t *testing.T) {
	logger := zap.NewNop()
	transformer := NewPromptCachingTransformer(true, logger)

	t.Run("Anthropic 注入 cache_control 到系统消息和最近 2 条 user 消息", func(t *testing.T) {
		sysMsg := openai.SystemMessage("你是一个助手")
		userMsg1 := openai.UserMessage("第一条用户消息")
		assistantMsg := openai.AssistantMessage("助手回复")
		userMsg2 := openai.UserMessage("第二条用户消息")
		userMsg3 := openai.UserMessage("第三条用户消息")

		messages := []openai.ChatCompletionMessageParamUnion{
			sysMsg, userMsg1, assistantMsg, userMsg2, userMsg3,
		}
		params := &openai.ChatCompletionNewParams{
			Model:    "claude-3-5-sonnet-20241022",
			Messages: messages,
		}

		ctx := &RequestTransformContext{
			Provider: "anthropic",
			Model:    "claude-3-5-sonnet-20241022",
			Messages: messages,
			Params:   params,
		}

		transformer.TransformRequest(ctx)

		// 验证系统消息被标记
		assert.NotNil(t, ctx.Messages[0].OfSystem, "系统消息应存在")
		sysExtra := ctx.Messages[0].OfSystem.ExtraFields()
		assert.Contains(t, sysExtra, "cache_control", "系统消息应有 cache_control")

		// 验证最近 2 条 user 消息被标记（索引 3 和 4）
		user3Extra := ctx.Messages[4].OfUser.ExtraFields()
		assert.Contains(t, user3Extra, "cache_control", "最后一条 user 消息应有 cache_control")

		user2Extra := ctx.Messages[3].OfUser.ExtraFields()
		assert.Contains(t, user2Extra, "cache_control", "倒数第二条 user 消息应有 cache_control")

		// 验证第一条 user 消息没有被标记
		user1Extra := ctx.Messages[1].OfUser.ExtraFields()
		assert.NotContains(t, user1Extra, "cache_control", "第一条 user 消息不应有 cache_control")
	})

	t.Run("Anthropic 没有系统消息时只标记 user 消息", func(t *testing.T) {
		userMsg := openai.UserMessage("用户消息")
		messages := []openai.ChatCompletionMessageParamUnion{userMsg}
		params := &openai.ChatCompletionNewParams{
			Model:    "claude-3-5-sonnet-20241022",
			Messages: messages,
		}

		ctx := &RequestTransformContext{
			Provider: "anthropic",
			Model:    "claude-3-5-sonnet-20241022",
			Messages: messages,
			Params:   params,
		}

		transformer.TransformRequest(ctx)

		userExtra := ctx.Messages[0].OfUser.ExtraFields()
		assert.Contains(t, userExtra, "cache_control")
	})
}

func TestPromptCachingTransformer_Bedrock(t *testing.T) {
	logger := zap.NewNop()
	transformer := NewPromptCachingTransformer(true, logger)

	t.Run("Bedrock 注入 cache_point", func(t *testing.T) {
		sysMsg := openai.SystemMessage("系统提示")
		userMsg := openai.UserMessage("用户消息")

		messages := []openai.ChatCompletionMessageParamUnion{sysMsg, userMsg}
		params := &openai.ChatCompletionNewParams{
			Model:    "anthropic.claude-3-sonnet-20240229-v1:0",
			Messages: messages,
		}

		ctx := &RequestTransformContext{
			Provider: "bedrock",
			Model:    "anthropic.claude-3-sonnet-20240229-v1:0",
			Messages: messages,
			Params:   params,
		}

		transformer.TransformRequest(ctx)

		// 验证系统消息被标记
		sysExtra := ctx.Messages[0].OfSystem.ExtraFields()
		assert.Contains(t, sysExtra, "cache_point", "系统消息应有 cache_point")

		// 验证 user 消息被标记
		userExtra := ctx.Messages[1].OfUser.ExtraFields()
		assert.Contains(t, userExtra, "cache_point", "user 消息应有 cache_point")
	})
}

func TestPromptCachingTransformer_OpenAI(t *testing.T) {
	logger := zap.NewNop()
	transformer := NewPromptCachingTransformer(true, logger)

	t.Run("OpenAI 设置 prompt_cache_key", func(t *testing.T) {
		messages := []openai.ChatCompletionMessageParamUnion{
			openai.UserMessage("hello"),
		}
		params := &openai.ChatCompletionNewParams{
			Model:    "gpt-4o",
			Messages: messages,
		}

		ctx := &RequestTransformContext{
			Provider: "openai",
			Model:    "gpt-4o",
			Messages: messages,
			Params:   params,
		}

		transformer.TransformRequest(ctx)

		// 验证 prompt_cache_key 被设置为稳定分桶 key
		assert.Equal(t, openai.String(stablePromptCacheKey("gpt-4o", "", nil, nil)), ctx.Params.PromptCacheKey)
	})
}

func TestStablePromptCacheKey(t *testing.T) {
	key := stablePromptCacheKey("gpt-4o", "user-123", []string{"b", "a"}, []mcphost.ToolDefinition{
		{Name: "zeta", InputSchema: []byte(`{"type":"object"}`)},
		{Name: "alpha", InputSchema: []byte(`{"type":"object"}`)},
	})
	same := stablePromptCacheKey("gpt-4o", "user-123", []string{"a", "b"}, []mcphost.ToolDefinition{
		{Name: "alpha", InputSchema: []byte(`{"type":"object"}`)},
		{Name: "zeta", InputSchema: []byte(`{"type":"object"}`)},
	})
	if key != same {
		t.Fatalf("stablePromptCacheKey should be stable across prompt/tool ordering, got %q vs %q", key, same)
	}
	if strings.Contains(key, "user-123") {
		t.Fatalf("prompt cache key must not contain raw user id: %q", key)
	}
}

func TestPromptCachingTransformer_SkipUnsupported(t *testing.T) {
	logger := zap.NewNop()
	transformer := NewPromptCachingTransformer(true, logger)

	t.Run("DeepSeek 跳过缓存注入", func(t *testing.T) {
		messages := []openai.ChatCompletionMessageParamUnion{
			openai.SystemMessage("系统提示"),
			openai.UserMessage("hello"),
		}
		params := &openai.ChatCompletionNewParams{
			Model:    "deepseek-chat",
			Messages: messages,
		}

		ctx := &RequestTransformContext{
			Provider: "deepseek",
			Model:    "deepseek-chat",
			Messages: messages,
			Params:   params,
		}

		transformer.TransformRequest(ctx)

		// 验证系统消息没有额外字段
		sysExtra := ctx.Messages[0].OfSystem.ExtraFields()
		assert.Empty(t, sysExtra, "DeepSeek 不应注入缓存标记")
	})
}

// --- ReasoningVariantsTransformer 测试 ---

func TestReasoningVariantsTransformer_OpenAI(t *testing.T) {
	logger := zap.NewNop()

	tests := []struct {
		name        string
		effort      string
		wantEffort  string
		wantSkipped bool
	}{
		{
			name:       "low 映射为 low",
			effort:     "low",
			wantEffort: "low",
		},
		{
			name:       "medium 映射为 medium",
			effort:     "medium",
			wantEffort: "medium",
		},
		{
			name:       "high 映射为 high",
			effort:     "high",
			wantEffort: "high",
		},
		{
			name:       "max 映射为 high（OpenAI 最高级别）",
			effort:     "max",
			wantEffort: "high",
		},
		{
			name:        "未知级别跳过",
			effort:      "turbo",
			wantSkipped: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			transformer := NewReasoningVariantsTransformer(tt.effort, logger)
			params := &openai.ChatCompletionNewParams{
				Model: "o1",
			}

			ctx := &RequestTransformContext{
				Provider: "openai",
				Model:    "o1",
				Params:   params,
			}

			transformer.TransformRequest(ctx)

			if tt.wantSkipped {
				assert.Empty(t, string(ctx.Params.ReasoningEffort), "未知级别不应设置 reasoning_effort")
			} else {
				assert.Equal(t, tt.wantEffort, string(ctx.Params.ReasoningEffort))
			}
		})
	}
}

func TestReasoningVariantsTransformer_Anthropic(t *testing.T) {
	logger := zap.NewNop()
	transformer := NewReasoningVariantsTransformer("high", logger)

	params := &openai.ChatCompletionNewParams{
		Model: "claude-3-5-sonnet-20241022",
	}

	ctx := &RequestTransformContext{
		Provider: "anthropic",
		Model:    "claude-3-5-sonnet-20241022",
		Params:   params,
	}

	transformer.TransformRequest(ctx)

	// 验证 thinking 配置被注入
	extra := ctx.Params.ExtraFields()
	assert.Contains(t, extra, "thinking", "Anthropic 应注入 thinking 配置")

	thinking, ok := extra["thinking"].(map[string]any)
	require.True(t, ok, "thinking 应为 map")
	assert.Equal(t, "enabled", thinking["type"])
	assert.Equal(t, 10240, thinking["budget_tokens"])
}

func TestReasoningVariantsTransformer_Gemini(t *testing.T) {
	logger := zap.NewNop()
	transformer := NewReasoningVariantsTransformer("medium", logger)

	params := &openai.ChatCompletionNewParams{
		Model: "gemini-1.5-pro",
	}

	ctx := &RequestTransformContext{
		Provider: "google",
		Model:    "gemini-1.5-pro",
		Params:   params,
	}

	transformer.TransformRequest(ctx)

	// 验证 thinkingConfig 被注入
	extra := ctx.Params.ExtraFields()
	assert.Contains(t, extra, "thinkingConfig", "Gemini 应注入 thinkingConfig")

	config, ok := extra["thinkingConfig"].(map[string]any)
	require.True(t, ok, "thinkingConfig 应为 map")
	assert.Equal(t, 4096, config["thinkingBudget"])
}

func TestReasoningVariantsTransformer_Bedrock(t *testing.T) {
	logger := zap.NewNop()
	transformer := NewReasoningVariantsTransformer("low", logger)

	params := &openai.ChatCompletionNewParams{
		Model: "anthropic.claude-3-sonnet-20240229-v1:0",
	}

	ctx := &RequestTransformContext{
		Provider: "bedrock",
		Model:    "anthropic.claude-3-sonnet-20240229-v1:0",
		Params:   params,
	}

	transformer.TransformRequest(ctx)

	// 验证 reasoningConfig 被注入
	extra := ctx.Params.ExtraFields()
	assert.Contains(t, extra, "reasoningConfig", "Bedrock 应注入 reasoningConfig")

	config, ok := extra["reasoningConfig"].(map[string]any)
	require.True(t, ok, "reasoningConfig 应为 map")
	assert.Equal(t, 1024, config["budget"])
}

func TestReasoningVariantsTransformer_EmptyEffort(t *testing.T) {
	logger := zap.NewNop()
	transformer := NewReasoningVariantsTransformer("", logger)

	params := &openai.ChatCompletionNewParams{
		Model: "o1",
	}

	ctx := &RequestTransformContext{
		Provider: "openai",
		Model:    "o1",
		Params:   params,
	}

	transformer.TransformRequest(ctx)

	// 空 effort 不应设置任何字段
	assert.Empty(t, string(ctx.Params.ReasoningEffort), "空 effort 不应设置 reasoning_effort")
}

func TestReasoningVariantsTransformer_SkipUnsupported(t *testing.T) {
	logger := zap.NewNop()
	transformer := NewReasoningVariantsTransformer("high", logger)

	params := &openai.ChatCompletionNewParams{
		Model: "mistral-large-latest",
	}

	ctx := &RequestTransformContext{
		Provider: "mistral",
		Model:    "mistral-large-latest",
		Params:   params,
	}

	transformer.TransformRequest(ctx)

	// Mistral 不支持 reasoning，不应注入任何字段
	extra := ctx.Params.ExtraFields()
	assert.Empty(t, extra, "Mistral 不应注入 reasoning 配置")
	assert.Empty(t, string(ctx.Params.ReasoningEffort))
}

// --- effortToBudgetTokens 测试 ---

func TestEffortToBudgetTokens(t *testing.T) {
	tests := []struct {
		effort string
		want   int
	}{
		{"low", 1024},
		{"medium", 4096},
		{"high", 10240},
		{"max", 32768},
		{"LOW", 1024},     // 不区分大小写
		{"unknown", 4096}, // 默认中等
		{"", 4096},        // 空字符串默认中等
	}

	for _, tt := range tests {
		t.Run(tt.effort, func(t *testing.T) {
			got := effortToBudgetTokens(tt.effort)
			assert.Equal(t, tt.want, got)
		})
	}
}

// --- ChainRequestTransformer 测试 ---

func TestChainRequestTransformer(t *testing.T) {
	logger := zap.NewNop()

	t.Run("链式请求转换器按顺序执行", func(t *testing.T) {
		chain := DefaultRequestTransformer("high", logger, WithPromptCacheKey(true))

		messages := []openai.ChatCompletionMessageParamUnion{
			openai.SystemMessage("系统提示"),
			openai.UserMessage("用户消息"),
		}
		params := &openai.ChatCompletionNewParams{
			Model:    "o1",
			Messages: messages,
		}

		ctx := &RequestTransformContext{
			Provider: "openai",
			Model:    "o1",
			Messages: messages,
			Params:   params,
		}

		chain.TransformRequest(ctx)

		// 验证缓存转换器执行（设置了 prompt_cache_key）
		assert.Equal(t, openai.String(stablePromptCacheKey("o1", "", nil, nil)), ctx.Params.PromptCacheKey)

		// 验证 reasoning 转换器执行（设置了 reasoning_effort）
		assert.Equal(t, "high", string(ctx.Params.ReasoningEffort))
	})

	t.Run("无 reasoning effort 时仅执行缓存转换器", func(t *testing.T) {
		chain := DefaultRequestTransformer("", logger, WithPromptCacheKey(true))

		messages := []openai.ChatCompletionMessageParamUnion{
			openai.UserMessage("hello"),
		}
		params := &openai.ChatCompletionNewParams{
			Model:    "gpt-4o",
			Messages: messages,
		}

		ctx := &RequestTransformContext{
			Provider: "openai",
			Model:    "gpt-4o",
			Messages: messages,
			Params:   params,
		}

		chain.TransformRequest(ctx)

		// 缓存转换器应执行
		assert.Equal(t, openai.String(stablePromptCacheKey("gpt-4o", "", nil, nil)), ctx.Params.PromptCacheKey)

		// reasoning 不应设置
		assert.Empty(t, string(ctx.Params.ReasoningEffort))
	})
}

// --- DetectProvider Bedrock 测试 ---

func TestDetectProvider_Bedrock(t *testing.T) {
	result := DetectProvider("https://bedrock-runtime.us-east-1.amazonaws.com")
	assert.Equal(t, "bedrock", result)
}

// --- ReasoningContentTransformer 测试 ---

func TestReasoningContentTransformer(t *testing.T) {
	logger := zap.NewNop()
	transformer := NewReasoningContentTransformer(logger)

	t.Run("提取已有的 reasoning_content (OpenAI)", func(t *testing.T) {
		messages := []MessageWithTools{
			{
				Role:    "assistant",
				Content: NewTextContent("最终答案"),
				Metadata: map[string]string{
					"reasoning_content": "让我思考一下这个问题...",
				},
			},
		}

		result := transformer.Transform(messages, "openai")
		require.Len(t, result, 1)
		assert.Equal(t, "让我思考一下这个问题...", result[0].Metadata["reasoning_content"])
	})

	t.Run("提取已有的 reasoning_content (DeepSeek)", func(t *testing.T) {
		messages := []MessageWithTools{
			{
				Role:    "assistant",
				Content: NewTextContent("计算结果"),
				Metadata: map[string]string{
					"reasoning_content": "首先分析问题结构...",
				},
			},
		}

		result := transformer.Transform(messages, "deepseek")
		require.Len(t, result, 1)
		assert.Equal(t, "首先分析问题结构...", result[0].Metadata["reasoning_content"])
	})

	t.Run("提取已有的 reasoning_content (Anthropic)", func(t *testing.T) {
		messages := []MessageWithTools{
			{
				Role:    "assistant",
				Content: NewTextContent("回答"),
				Metadata: map[string]string{
					"reasoning_content": "thinking block 内容",
				},
			},
		}

		result := transformer.Transform(messages, "anthropic")
		require.Len(t, result, 1)
		assert.Equal(t, "thinking block 内容", result[0].Metadata["reasoning_content"])
	})

	t.Run("无 reasoning 内容不修改", func(t *testing.T) {
		messages := []MessageWithTools{
			{
				Role:    "assistant",
				Content: NewTextContent("普通回答"),
			},
		}

		result := transformer.Transform(messages, "openai")
		require.Len(t, result, 1)
		assert.Nil(t, result[0].Metadata)
	})

	t.Run("非 assistant 消息不处理", func(t *testing.T) {
		messages := []MessageWithTools{
			{
				Role:    "user",
				Content: NewTextContent("用户消息"),
				Metadata: map[string]string{
					"reasoning_content": "不应该被处理",
				},
			},
		}

		result := transformer.Transform(messages, "openai")
		require.Len(t, result, 1)
		// user 消息的 Metadata 保持不变（但不会被转换器处理）
	})

	t.Run("空 reasoning_content 不注入", func(t *testing.T) {
		messages := []MessageWithTools{
			{
				Role:    "assistant",
				Content: NewTextContent("回答"),
				Metadata: map[string]string{
					"reasoning_content": "",
				},
			},
		}

		result := transformer.Transform(messages, "openai")
		require.Len(t, result, 1)
		// 空字符串不会触发注入
		assert.Empty(t, result[0].Metadata["reasoning_content"])
	})
}

// --- ModalityFilterTransformer 测试 ---

func TestModalityFilterTransformer(t *testing.T) {
	logger := zap.NewNop()

	t.Run("模型不支持 vision 时替换图片为文本", func(t *testing.T) {
		// deepseek-chat 不支持 vision
		transformer := NewModalityFilterTransformer("deepseek-chat", logger)

		messages := []MessageWithTools{
			{
				Role: "user",
				Content: NewMultiContent(
					TextPart("请描述这张图片"),
					ImageURLPart("https://example.com/image.png"),
				),
			},
		}

		result := transformer.Transform(messages, "deepseek")
		require.Len(t, result, 1)
		assert.True(t, result[0].Content.IsMultimodal())

		parts := result[0].Content.Parts()
		require.Len(t, parts, 2)
		assert.Equal(t, ContentText, parts[0].Type)
		assert.Equal(t, "请描述这张图片", parts[0].Text)
		assert.Equal(t, ContentText, parts[1].Type)
		assert.Contains(t, parts[1].Text, "图片内容已省略")
	})

	t.Run("模型支持 vision 时不替换图片", func(t *testing.T) {
		// gpt-4o 支持 vision
		transformer := NewModalityFilterTransformer("gpt-4o", logger)

		messages := []MessageWithTools{
			{
				Role: "user",
				Content: NewMultiContent(
					TextPart("请描述这张图片"),
					ImageURLPart("https://example.com/image.png"),
				),
			},
		}

		result := transformer.Transform(messages, "openai")
		require.Len(t, result, 1)

		parts := result[0].Content.Parts()
		require.Len(t, parts, 2)
		assert.Equal(t, ContentImage, parts[1].Type)
	})

	t.Run("模型不支持 audio 时替换音频为文本", func(t *testing.T) {
		// deepseek-chat 不支持 audio
		transformer := NewModalityFilterTransformer("deepseek-chat", logger)

		messages := []MessageWithTools{
			{
				Role: "user",
				Content: NewMultiContent(
					TextPart("请转录这段音频"),
					AudioPart("base64data", "wav"),
				),
			},
		}

		result := transformer.Transform(messages, "deepseek")
		require.Len(t, result, 1)

		parts := result[0].Content.Parts()
		require.Len(t, parts, 2)
		assert.Equal(t, ContentText, parts[1].Type)
		assert.Contains(t, parts[1].Text, "音频内容已省略")
	})

	t.Run("模型同时支持 vision 和 audio 时不过滤", func(t *testing.T) {
		// gemini-1.5-pro 支持 vision 和 audio
		transformer := NewModalityFilterTransformer("gemini-1.5-pro", logger)

		messages := []MessageWithTools{
			{
				Role: "user",
				Content: NewMultiContent(
					TextPart("分析以下内容"),
					ImageURLPart("https://example.com/image.png"),
					AudioPart("base64data", "mp3"),
				),
			},
		}

		result := transformer.Transform(messages, "google")
		require.Len(t, result, 1)

		parts := result[0].Content.Parts()
		require.Len(t, parts, 3)
		assert.Equal(t, ContentImage, parts[1].Type)
		assert.Equal(t, ContentAudio, parts[2].Type)
	})

	t.Run("未知模型不过滤", func(t *testing.T) {
		transformer := NewModalityFilterTransformer("unknown-model-xyz", logger)

		messages := []MessageWithTools{
			{
				Role: "user",
				Content: NewMultiContent(
					ImageURLPart("https://example.com/image.png"),
				),
			},
		}

		result := transformer.Transform(messages, "custom")
		require.Len(t, result, 1)

		parts := result[0].Content.Parts()
		require.Len(t, parts, 1)
		assert.Equal(t, ContentImage, parts[0].Type)
	})

	t.Run("纯文本消息不处理", func(t *testing.T) {
		transformer := NewModalityFilterTransformer("deepseek-chat", logger)

		messages := []MessageWithTools{
			{
				Role:    "user",
				Content: NewTextContent("纯文本消息"),
			},
		}

		result := transformer.Transform(messages, "deepseek")
		require.Len(t, result, 1)
		assert.False(t, result[0].Content.IsMultimodal())
		assert.Equal(t, "纯文本消息", result[0].Content.Text())
	})
}

// --- TemperatureDefaultsTransformer 测试 ---

func TestTemperatureDefaultsTransformer(t *testing.T) {
	logger := zap.NewNop()
	transformer := NewTemperatureDefaultsTransformer(logger)

	t.Run("DeepSeek 默认温度 0.7", func(t *testing.T) {
		params := &openai.ChatCompletionNewParams{
			Model: "deepseek-chat",
		}
		ctx := &RequestTransformContext{
			Provider: "deepseek",
			Model:    "deepseek-chat",
			Params:   params,
		}

		transformer.TransformRequest(ctx)

		assert.True(t, ctx.Params.Temperature.Valid())
		assert.Equal(t, 0.7, ctx.Params.Temperature.Value)
	})

	t.Run("OpenAI 默认温度 1.0", func(t *testing.T) {
		params := &openai.ChatCompletionNewParams{
			Model: "gpt-4o",
		}
		ctx := &RequestTransformContext{
			Provider: "openai",
			Model:    "gpt-4o",
			Params:   params,
		}

		transformer.TransformRequest(ctx)

		assert.True(t, ctx.Params.Temperature.Valid())
		assert.Equal(t, 1.0, ctx.Params.Temperature.Value)
	})

	t.Run("Qwen 默认温度 0.55", func(t *testing.T) {
		params := &openai.ChatCompletionNewParams{
			Model: "qwen-max",
		}
		ctx := &RequestTransformContext{
			Provider: "qwen",
			Model:    "qwen-max",
			Params:   params,
		}

		transformer.TransformRequest(ctx)

		assert.True(t, ctx.Params.Temperature.Valid())
		assert.Equal(t, 0.55, ctx.Params.Temperature.Value)
	})

	t.Run("Qwen 模型名检测（provider 为 openai 兼容）", func(t *testing.T) {
		params := &openai.ChatCompletionNewParams{
			Model: "qwen-plus",
		}
		ctx := &RequestTransformContext{
			Provider: "openai", // Qwen 可能通过 OpenAI 兼容 API
			Model:    "qwen-plus",
			Params:   params,
		}

		transformer.TransformRequest(ctx)

		assert.True(t, ctx.Params.Temperature.Valid())
		assert.Equal(t, 0.55, ctx.Params.Temperature.Value)
	})

	t.Run("已设置 Temperature 不覆盖", func(t *testing.T) {
		params := &openai.ChatCompletionNewParams{
			Model:       "deepseek-chat",
			Temperature: openai.Float(0.3),
		}
		ctx := &RequestTransformContext{
			Provider: "deepseek",
			Model:    "deepseek-chat",
			Params:   params,
		}

		transformer.TransformRequest(ctx)

		assert.Equal(t, 0.3, ctx.Params.Temperature.Value)
	})

	t.Run("Reasoning 模型跳过", func(t *testing.T) {
		params := &openai.ChatCompletionNewParams{
			Model: "o1",
		}
		ctx := &RequestTransformContext{
			Provider: "openai",
			Model:    "o1",
			Params:   params,
		}

		transformer.TransformRequest(ctx)

		assert.False(t, ctx.Params.Temperature.Valid())
	})

	t.Run("DeepSeek Reasoner 跳过", func(t *testing.T) {
		params := &openai.ChatCompletionNewParams{
			Model: "deepseek-reasoner",
		}
		ctx := &RequestTransformContext{
			Provider: "deepseek",
			Model:    "deepseek-reasoner",
			Params:   params,
		}

		transformer.TransformRequest(ctx)

		assert.False(t, ctx.Params.Temperature.Valid())
	})
}

// --- MaxOutputTokensTransformer 测试 ---

func TestMaxOutputTokensTransformer(t *testing.T) {
	logger := zap.NewNop()
	transformer := NewMaxOutputTokensTransformer(logger)

	t.Run("未设置 MaxTokens 时使用默认值", func(t *testing.T) {
		params := &openai.ChatCompletionNewParams{
			Model: "gpt-4o",
		}
		ctx := &RequestTransformContext{
			Provider: "openai",
			Model:    "gpt-4o",
			Params:   params,
		}

		transformer.TransformRequest(ctx)

		// gpt-4o MaxOutput=16384, defaultMaxOutputTokens=16384, min=16384
		assert.True(t, ctx.Params.MaxTokens.Valid())
		assert.Equal(t, int64(16384), ctx.Params.MaxTokens.Value)
	})

	t.Run("MaxTokens 超过模型限制时截断", func(t *testing.T) {
		params := &openai.ChatCompletionNewParams{
			Model:     "gpt-4-turbo",
			MaxTokens: openai.Int(8192),
		}
		ctx := &RequestTransformContext{
			Provider: "openai",
			Model:    "gpt-4-turbo", // MaxOutput=4096
			Params:   params,
		}

		transformer.TransformRequest(ctx)

		assert.True(t, ctx.Params.MaxTokens.Valid())
		assert.Equal(t, int64(4096), ctx.Params.MaxTokens.Value)
	})

	t.Run("MaxTokens 未超过模型限制时保持不变", func(t *testing.T) {
		params := &openai.ChatCompletionNewParams{
			Model:     "gpt-4o",
			MaxTokens: openai.Int(8000),
		}
		ctx := &RequestTransformContext{
			Provider: "openai",
			Model:    "gpt-4o", // MaxOutput=16384
			Params:   params,
		}

		transformer.TransformRequest(ctx)

		assert.True(t, ctx.Params.MaxTokens.Valid())
		assert.Equal(t, int64(8000), ctx.Params.MaxTokens.Value)
	})

	t.Run("Reasoning 模型使用 max_completion_tokens", func(t *testing.T) {
		params := &openai.ChatCompletionNewParams{
			Model: "o1",
		}
		ctx := &RequestTransformContext{
			Provider: "openai",
			Model:    "o1", // MaxOutput=100000
			Params:   params,
		}

		transformer.TransformRequest(ctx)

		// max_tokens 应被清除
		assert.False(t, ctx.Params.MaxTokens.Valid())
		// max_completion_tokens 通过 extra fields 设置
		extra := ctx.Params.ExtraFields()
		require.Contains(t, extra, "max_completion_tokens")
		// 默认值: min(100000, 16384) = 16384
		assert.Equal(t, int64(16384), extra["max_completion_tokens"])
	})

	t.Run("未知模型不调整", func(t *testing.T) {
		params := &openai.ChatCompletionNewParams{
			Model:     "unknown-model",
			MaxTokens: openai.Int(999),
		}
		ctx := &RequestTransformContext{
			Provider: "custom",
			Model:    "unknown-model",
			Params:   params,
		}

		transformer.TransformRequest(ctx)

		assert.True(t, ctx.Params.MaxTokens.Valid())
		assert.Equal(t, int64(999), ctx.Params.MaxTokens.Value)
	})

	t.Run("模型 MaxOutput 小于默认值时使用模型限制", func(t *testing.T) {
		params := &openai.ChatCompletionNewParams{
			Model: "gpt-4-turbo", // MaxOutput=4096
		}
		ctx := &RequestTransformContext{
			Provider: "openai",
			Model:    "gpt-4-turbo",
			Params:   params,
		}

		transformer.TransformRequest(ctx)

		require.True(t, ctx.Params.MaxTokens.Valid())
		// min(4096, 16384) = 4096
		assert.Equal(t, int64(4096), ctx.Params.MaxTokens.Value)
	})
}

// --- isReasoningModel 测试 ---

func TestIsReasoningModel(t *testing.T) {
	tests := []struct {
		model string
		want  bool
	}{
		{"o1", true},
		{"o3-mini", true},
		{"deepseek-reasoner", true},
		{"gpt-4o", false},
		{"gpt-4o-mini", false},
		{"claude-3-5-sonnet-20241022", false},
		{"deepseek-chat", false},
		{"gemini-1.5-pro", false},
		{"unknown-o1-model", true}, // 包含 o1 的未知模型
		{"unknown-model", false},
	}

	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			assert.Equal(t, tt.want, isReasoningModel(tt.model))
		})
	}
}

// --- ToolCallIDLengthTransformer 测试 ---

func TestToolCallIDLengthTransformer_LongIDTruncated(t *testing.T) {
	logger := zap.NewNop()
	transformer := NewToolCallIDLengthTransformer(logger)

	// 生成超长 ID（1002字符，模拟实际场景）
	longID := "call_" + strings.Repeat("a", 997) // 1002 字符
	// 65字符边界
	borderID := "call_" + strings.Repeat("b", 60) // 65 字符

	tests := []struct {
		name     string
		messages []MessageWithTools
		checkIdx int
		checkTC  bool // 检查 ToolCalls 还是 ToolCallID
	}{
		{
			name: "1002字符 tool_call_id 被截断 (tool 消息)",
			messages: []MessageWithTools{
				{Role: "tool", Content: NewTextContent("result"), ToolCallID: longID},
			},
			checkIdx: 0,
		},
		{
			name: "65字符 tool_call_id 被截断 (tool 消息)",
			messages: []MessageWithTools{
				{Role: "tool", Content: NewTextContent("result"), ToolCallID: borderID},
			},
			checkIdx: 0,
		},
		{
			name: "超长 ID 被截断 (assistant 消息 ToolCalls)",
			messages: []MessageWithTools{
				{
					Role: "assistant",
					ToolCalls: []ToolCall{
						{ID: longID, Name: "test_tool", Arguments: json.RawMessage(`{}`)},
					},
				},
			},
			checkIdx: 0,
			checkTC:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := transformer.Transform(tt.messages, "openai")
			require.Len(t, result, len(tt.messages))

			if tt.checkTC {
				shortened := result[tt.checkIdx].ToolCalls[0].ID
				assert.LessOrEqual(t, len(shortened), MaxToolCallIDLength)
				assert.True(t, strings.HasPrefix(shortened, "call_"))
			} else {
				shortened := result[tt.checkIdx].ToolCallID
				assert.LessOrEqual(t, len(shortened), MaxToolCallIDLength)
				assert.True(t, strings.HasPrefix(shortened, "call_"))
			}
		})
	}
}

func TestToolCallIDLengthTransformer_NormalIDUnchanged(t *testing.T) {
	logger := zap.NewNop()
	transformer := NewToolCallIDLengthTransformer(logger)

	// 64字符边界：刚好不超长
	exactID := "call_" + strings.Repeat("x", 59) // 64 字符
	shortID := "call_abc123"

	messages := []MessageWithTools{
		{Role: "tool", Content: NewTextContent("r1"), ToolCallID: exactID},
		{Role: "tool", Content: NewTextContent("r2"), ToolCallID: shortID},
		{
			Role: "assistant",
			ToolCalls: []ToolCall{
				{ID: exactID, Name: "tool1", Arguments: json.RawMessage(`{}`)},
				{ID: shortID, Name: "tool2", Arguments: json.RawMessage(`{}`)},
			},
		},
	}

	result := transformer.Transform(messages, "openai")

	// 所有 ID 应保持不变
	assert.Equal(t, exactID, result[0].ToolCallID)
	assert.Equal(t, shortID, result[1].ToolCallID)
	assert.Equal(t, exactID, result[2].ToolCalls[0].ID)
	assert.Equal(t, shortID, result[2].ToolCalls[1].ID)
}

func TestToolCallIDLengthTransformer_ConsistentPairing(t *testing.T) {
	logger := zap.NewNop()
	transformer := NewToolCallIDLengthTransformer(logger)

	longID := "call_" + strings.Repeat("z", 997)

	// 模拟真实场景：assistant 消息包含 tool call，后续 tool 消息引用同一 ID
	messages := []MessageWithTools{
		{
			Role: "assistant",
			ToolCalls: []ToolCall{
				{ID: longID, Name: "search", Arguments: json.RawMessage(`{"q":"test"}`)},
			},
		},
		{Role: "tool", Content: NewTextContent("搜索结果"), ToolCallID: longID},
	}

	result := transformer.Transform(messages, "openai")

	assistantID := result[0].ToolCalls[0].ID
	toolID := result[1].ToolCallID

	// 两者必须一致，否则 API 会报错
	assert.Equal(t, assistantID, toolID, "assistant 和 tool 消息的 ID 必须一致")
	assert.LessOrEqual(t, len(assistantID), MaxToolCallIDLength)
}

func TestToolCallIDLengthTransformer_Deterministic(t *testing.T) {
	logger := zap.NewNop()
	transformer := NewToolCallIDLengthTransformer(logger)

	longID := "call_" + strings.Repeat("m", 500)

	messages := []MessageWithTools{
		{Role: "tool", Content: NewTextContent("result"), ToolCallID: longID},
	}

	result1 := transformer.Transform(messages, "openai")
	result2 := transformer.Transform(messages, "openai")

	assert.Equal(t, result1[0].ToolCallID, result2[0].ToolCallID, "哈希应具有确定性")

	// 验证哈希格式正确
	shortened := result1[0].ToolCallID
	assert.True(t, strings.HasPrefix(shortened, "call_"))
	// "call_" (5) + hex(sha256[:16]) (32) = 37 字符
	assert.Equal(t, 37, len(shortened))

	// 手动验证哈希值
	h := sha256.Sum256([]byte(longID))
	expected := "call_" + hex.EncodeToString(h[:16])
	assert.Equal(t, expected, shortened)
}

func TestToolCallIDLengthTransformer_AllProviders(t *testing.T) {
	logger := zap.NewNop()
	transformer := NewToolCallIDLengthTransformer(logger)

	longID := "call_" + strings.Repeat("p", 200)

	providers := []string{"openai", "anthropic", "mistral", "deepseek", "google", "bedrock", "custom"}

	for _, provider := range providers {
		t.Run(provider, func(t *testing.T) {
			messages := []MessageWithTools{
				{Role: "tool", Content: NewTextContent("result"), ToolCallID: longID},
			}
			result := transformer.Transform(messages, provider)
			assert.LessOrEqual(t, len(result[0].ToolCallID), MaxToolCallIDLength,
				"provider %s 应截断超长 ID", provider)
		})
	}
}
