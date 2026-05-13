package llm

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/openai/openai-go"
	"github.com/openai/openai-go/packages/param"
	"github.com/openai/openai-go/shared"
	"go.uber.org/zap"

	"github.com/chef-guo/agents-hive/internal/mcphost"
)

// MaxToolCallIDLength 是 tool_call_id 的最大允许长度。
// Responses API 对 call_id 有64字符限制，超过此长度的 ID 会被截断。
const MaxToolCallIDLength = 64

// toolCallIDCounter 包级原子计数器，确保跨 Transform 调用生成唯一 ID
var toolCallIDCounter int64

// globalModelRegistry 全局模型注册表实例，支持动态加载
// 使用 atomic.Value 保护并发读写安全
var globalModelRegistryValue atomic.Value // 存储 *ModelRegistry
var globalModelRegistryOnce sync.Once

// getGlobalModelRegistry 安全获取全局模型注册表
func getGlobalModelRegistry() *ModelRegistry {
	v := globalModelRegistryValue.Load()
	if v == nil {
		return nil
	}
	return v.(*ModelRegistry)
}

// setGlobalModelRegistryForTest 仅供测试使用，设置全局模型注册表
// reg 为 nil 时清空全局注册表（使用新的 atomic.Value 替换）
func setGlobalModelRegistryForTest(reg *ModelRegistry) {
	if reg == nil {
		globalModelRegistryValue = atomic.Value{}
	} else {
		globalModelRegistryValue.Store(reg)
	}
}

// InitModelRegistry 初始化全局模型注册表。
// remoteURL 为远程模型 API 地址；为空则跳过远程获取，仅使用内置默认模型。
func InitModelRegistry(logger *zap.Logger, remoteURL string) {
	globalModelRegistryOnce.Do(func() {
		reg := NewModelRegistry(logger)
		reg.remoteURL = remoteURL
		globalModelRegistryValue.Store(reg)
	})
}

// StartModelRegistryRefresh 在后台 goroutine 中执行远程模型元数据刷新。
// 调用方应传入可取消的 ctx 以便优雅退出。
func StartModelRegistryRefresh(ctx context.Context) {
	reg := getGlobalModelRegistry()
	if reg == nil {
		return
	}
	go func() {
		if err := reg.FetchRemote(ctx); err != nil {
			if reg.logger != nil {
				reg.logger.Warn("后台刷新模型元数据失败",
					zap.Error(err),
				)
			}
		}
	}()
}

// MessageTransformer 定义消息转换接口，用于在发送到不同 Provider 前对消息进行适配
type MessageTransformer interface {
	// Transform 对消息列表进行 provider 感知的转换
	Transform(messages []MessageWithTools, provider string) []MessageWithTools
}

// ChainTransformer 按顺序执行多个 MessageTransformer
type ChainTransformer struct {
	transformers []MessageTransformer
}

// NewChainTransformer 创建一个链式转换器
func NewChainTransformer(transformers ...MessageTransformer) *ChainTransformer {
	return &ChainTransformer{transformers: transformers}
}

// Transform 依次执行所有转换器
func (c *ChainTransformer) Transform(messages []MessageWithTools, provider string) []MessageWithTools {
	result := messages
	for _, t := range c.transformers {
		result = t.Transform(result, provider)
	}
	return result
}

// --- 内置转换器 ---

// ToolCallIDLengthTransformer 截断超过 MaxToolCallIDLength 的 tool_call_id。
// LLM 代理返回的 tool call ID 可能超长（如1002字符），而 Responses API 对 call_id 有64字符限制。
// 使用 SHA-256 哈希生成确定性短 ID，确保 assistant 和 tool 消息的 ID 配对一致。
type ToolCallIDLengthTransformer struct {
	logger *zap.Logger
}

// NewToolCallIDLengthTransformer 创建 tool_call_id 长度截断转换器
func NewToolCallIDLengthTransformer(logger *zap.Logger) *ToolCallIDLengthTransformer {
	return &ToolCallIDLengthTransformer{logger: logger}
}

// Transform 截断超长的 tool_call_id，对所有 provider 生效
func (t *ToolCallIDLengthTransformer) Transform(messages []MessageWithTools, provider string) []MessageWithTools {
	// 快速检查：如果没有超长 ID，直接返回原消息（零开销）
	hasLong := false
	for _, msg := range messages {
		if len(msg.ToolCallID) > MaxToolCallIDLength {
			hasLong = true
			break
		}
		for _, tc := range msg.ToolCalls {
			if len(tc.ID) > MaxToolCallIDLength {
				hasLong = true
				break
			}
		}
		if hasLong {
			break
		}
	}
	if !hasLong {
		return messages
	}

	if t.logger != nil {
		t.logger.Info("检测到超长 tool_call_id，开始截断",
			zap.Int("message_count", len(messages)),
			zap.String("provider", provider),
		)
	}

	result := make([]MessageWithTools, len(messages))
	for i, msg := range messages {
		result[i] = msg

		// 处理 tool 消息的 ToolCallID
		if len(msg.ToolCallID) > MaxToolCallIDLength {
			shortened := shortenToolCallID(msg.ToolCallID)
			if t.logger != nil {
				t.logger.Debug("截断超长 tool_call_id (tool 消息)",
					zap.Int("original_length", len(msg.ToolCallID)),
					zap.String("shortened_id", shortened),
					zap.Int("index", i),
				)
			}
			result[i].ToolCallID = shortened
		}

		// 处理 assistant 消息的 ToolCalls[].ID
		if len(msg.ToolCalls) > 0 {
			needsCopy := false
			for _, tc := range msg.ToolCalls {
				if len(tc.ID) > MaxToolCallIDLength {
					needsCopy = true
					break
				}
			}
			if needsCopy {
				cleaned := make([]ToolCall, len(msg.ToolCalls))
				for j, tc := range msg.ToolCalls {
					cleaned[j] = tc
					if len(tc.ID) > MaxToolCallIDLength {
						shortened := shortenToolCallID(tc.ID)
						if t.logger != nil {
							t.logger.Debug("截断超长 tool_call_id (assistant 消息)",
								zap.Int("original_length", len(tc.ID)),
								zap.String("shortened_id", shortened),
								zap.String("tool_name", tc.Name),
								zap.Int("index", i),
							)
						}
						cleaned[j].ID = shortened
					}
				}
				result[i].ToolCalls = cleaned
			}
		}
	}
	return result
}

// shortenToolCallID 使用 SHA-256 哈希生成确定性短 ID。
// 格式: "call_" + hex(sha256[:16]) = 5 + 32 = 37 字符。
// 同一长 ID 总是映射到同一短 ID，保证 assistant/tool 消息配对一致。
func shortenToolCallID(id string) string {
	h := sha256.Sum256([]byte(id))
	return "call_" + hex.EncodeToString(h[:16])
}

// EmptyContentTransformer 确保消息内容非空。
// Anthropic API 不接受空字符串的 content 字段。
type EmptyContentTransformer struct {
	logger *zap.Logger
}

// NewEmptyContentTransformer 创建空内容转换器
func NewEmptyContentTransformer(logger *zap.Logger) *EmptyContentTransformer {
	return &EmptyContentTransformer{logger: logger}
}

// Transform 将空内容替换为单个空格（仅对 anthropic provider 生效）
func (t *EmptyContentTransformer) Transform(messages []MessageWithTools, provider string) []MessageWithTools {
	if !isProvider(provider, "anthropic") {
		return messages
	}

	result := make([]MessageWithTools, len(messages))
	for i, msg := range messages {
		result[i] = msg
		if msg.Content.Text() == "" && msg.Role != "tool" && !msg.Content.IsMultimodal() {
			result[i].Content = NewTextContent(" ")
			if t.logger != nil {
				t.logger.Debug("Anthropic 适配: 将空内容替换为空格",
					zap.String("role", msg.Role),
					zap.Int("index", i),
				)
			}
		}
	}
	return result
}

// ToolCallIDTransformer 为缺失 tool_call_id 的 tool 消息注入 ID。
// Mistral API 要求所有 tool 消息必须有 tool_call_id。
type ToolCallIDTransformer struct {
	logger *zap.Logger
}

// NewToolCallIDTransformer 创建 tool_call_id 转换器
func NewToolCallIDTransformer(logger *zap.Logger) *ToolCallIDTransformer {
	return &ToolCallIDTransformer{logger: logger}
}

// Transform 为 tool 消息注入缺失的 tool_call_id（仅对 mistral provider 生效）。
// 使用包级原子计数器确保跨调用生成全局唯一 ID。
func (t *ToolCallIDTransformer) Transform(messages []MessageWithTools, provider string) []MessageWithTools {
	if !isProvider(provider, "mistral") {
		return messages
	}

	result := make([]MessageWithTools, len(messages))
	for i, msg := range messages {
		result[i] = msg
		if msg.Role == "tool" && msg.ToolCallID == "" {
			seq := atomic.AddInt64(&toolCallIDCounter, 1)
			result[i].ToolCallID = fmt.Sprintf("call_%d", seq)
			if t.logger != nil {
				t.logger.Debug("Mistral 适配: 注入缺失的 tool_call_id",
					zap.String("generated_id", result[i].ToolCallID),
					zap.Int("index", i),
				)
			}
		}
	}
	return result
}

// UnsupportedFieldTransformer 移除指定 provider 不支持的字段
type UnsupportedFieldTransformer struct {
	logger *zap.Logger
}

// NewUnsupportedFieldTransformer 创建不支持字段移除转换器
func NewUnsupportedFieldTransformer(logger *zap.Logger) *UnsupportedFieldTransformer {
	return &UnsupportedFieldTransformer{logger: logger}
}

// Transform 移除 provider 不支持的字段
func (t *UnsupportedFieldTransformer) Transform(messages []MessageWithTools, provider string) []MessageWithTools {
	result := make([]MessageWithTools, len(messages))
	for i, msg := range messages {
		result[i] = msg

		// Anthropic 不支持 tool_calls 中的空 arguments
		if isProvider(provider, "anthropic") && len(msg.ToolCalls) > 0 {
			cleaned := make([]ToolCall, len(msg.ToolCalls))
			for j, tc := range msg.ToolCalls {
				cleaned[j] = tc
				if len(tc.Arguments) == 0 || string(tc.Arguments) == "" {
					cleaned[j].Arguments = []byte("{}")
				}
			}
			result[i].ToolCalls = cleaned
		}
	}
	return result
}

// --- ReasoningContentTransformer ---

// ReasoningContentTransformer 提取不同 Provider 返回的 reasoning 内容字段，统一映射到消息的 Metadata 中。
//
// 不同 Provider 的 reasoning 内容位置不同：
//   - OpenAI o1/o3: reasoning_content 字段存储在消息 Content 前缀（SDK 已处理，此处通过启发式检测）
//   - Anthropic: thinking blocks（type="thinking" 的 content block）
//   - DeepSeek: reasoning_content 字段
//
// 提取后将 reasoning 内容存储在 Metadata["reasoning_content"] 中，
// 便于 compaction 时保留推理摘要。
type ReasoningContentTransformer struct {
	logger *zap.Logger
}

// NewReasoningContentTransformer 创建 reasoning 内容提取转换器
func NewReasoningContentTransformer(logger *zap.Logger) *ReasoningContentTransformer {
	return &ReasoningContentTransformer{logger: logger}
}

// Transform 遍历 assistant 消息，提取 reasoning 内容到 Metadata
func (t *ReasoningContentTransformer) Transform(messages []MessageWithTools, provider string) []MessageWithTools {
	result := make([]MessageWithTools, len(messages))
	for i, msg := range messages {
		result[i] = msg
		if msg.Role != "assistant" {
			continue
		}

		var reasoning string

		switch {
		case isProvider(provider, "anthropic"):
			// Anthropic: 检查多模态 parts 中是否有 thinking block
			// thinking block 的 type 通常为 "thinking"，在我们的 Content 模型中
			// 没有对应的 ContentPartType，但可以通过 Metadata 传递。
			// 当前 Content 模型不直接支持 thinking block，
			// 依赖上游在解析 Anthropic 响应时将 thinking 内容注入 Metadata。
			if msg.Metadata != nil && msg.Metadata["reasoning_content"] != "" {
				reasoning = msg.Metadata["reasoning_content"]
			}

		case isProvider(provider, "deepseek"):
			// DeepSeek: reasoning_content 字段
			// DeepSeek reasoning_content 依赖上游解析时注入 Metadata。
			if msg.Metadata != nil && msg.Metadata["reasoning_content"] != "" {
				reasoning = msg.Metadata["reasoning_content"]
			}

		case isProvider(provider, "openai"):
			// OpenAI o1/o3: reasoning_content 在 SDK 响应的 extra fields 中
			// OpenAI reasoning_content 依赖上游解析时注入 Metadata。
			if msg.Metadata != nil && msg.Metadata["reasoning_content"] != "" {
				reasoning = msg.Metadata["reasoning_content"]
			}
		}

		if reasoning != "" {
			// 确保 Metadata map 已初始化
			if result[i].Metadata == nil {
				result[i].Metadata = make(map[string]string)
			}
			result[i].Metadata["reasoning_content"] = reasoning

			if t.logger != nil {
				t.logger.Debug("提取到 reasoning 内容",
					zap.String("provider", provider),
					zap.Int("index", i),
					zap.Int("reasoning_len", len(reasoning)),
				)
			}
		}
	}
	return result
}

// --- ModalityFilterTransformer ---

// ModalityFilterTransformer 过滤模型不支持的内容类型。
//
// 根据模型元数据（ModelMeta.Capabilities）检查消息中的多模态内容：
//   - 如果模型不支持 vision，将图片 content block 替换为文本说明
//   - 如果模型不支持 audio，将音频 content block 替换为文本说明
//
// 这样可以防止发送不支持的内容类型导致 API 错误。
type ModalityFilterTransformer struct {
	model  string // 当前模型 ID，用于查询 ModelMeta
	logger *zap.Logger
}

// NewModalityFilterTransformer 创建模态过滤转换器
// model 为当前使用的模型 ID，用于查询 ModelMeta 能力信息
func NewModalityFilterTransformer(model string, logger *zap.Logger) *ModalityFilterTransformer {
	return &ModalityFilterTransformer{model: model, logger: logger}
}

// Transform 遍历消息，将模型不支持的多模态内容替换为文本说明
func (t *ModalityFilterTransformer) Transform(messages []MessageWithTools, provider string) []MessageWithTools {
	meta := GetModelMeta(t.model)
	if meta == nil {
		if t.logger != nil {
			t.logger.Info("[DEBUG-UPLOAD] 模态过滤: 未知模型，跳过过滤",
				zap.String("model", t.model),
			)
		}
		return messages
	}

	supportsVision := meta.Capabilities.Vision
	supportsAudio := meta.Capabilities.Audio

	if t.logger != nil {
		t.logger.Info("[DEBUG-UPLOAD] 模态过滤器",
			zap.String("model", t.model),
			zap.Bool("supports_vision", supportsVision),
			zap.Bool("supports_audio", supportsAudio),
		)
	}

	// 如果模型支持所有模态，直接返回
	if supportsVision && supportsAudio {
		return messages
	}

	result := make([]MessageWithTools, len(messages))
	for i, msg := range messages {
		result[i] = msg

		if !msg.Content.IsMultimodal() {
			continue
		}

		parts := msg.Content.Parts()
		filtered := make([]ContentPart, 0, len(parts))
		modified := false

		for _, part := range parts {
			switch part.Type {
			case ContentImage:
				if !supportsVision {
					filtered = append(filtered, ContentPart{
						Type: ContentText,
						Text: "[图片内容已省略: 当前模型不支持图片输入]",
					})
					modified = true
					if t.logger != nil {
						t.logger.Debug("模态过滤: 替换不支持的图片内容",
							zap.String("model", t.model),
							zap.Int("index", i),
						)
					}
				} else {
					filtered = append(filtered, part)
				}

			case ContentAudio:
				if !supportsAudio {
					filtered = append(filtered, ContentPart{
						Type: ContentText,
						Text: "[音频内容已省略: 当前模型不支持音频输入]",
					})
					modified = true
					if t.logger != nil {
						t.logger.Debug("模态过滤: 替换不支持的音频内容",
							zap.String("model", t.model),
							zap.Int("index", i),
						)
					}
				} else {
					filtered = append(filtered, part)
				}

			default:
				filtered = append(filtered, part)
			}
		}

		if modified {
			result[i].Content = NewMultiContent(filtered...)
		}
	}
	return result
}

// DefaultTransformer 创建包含所有内置转换规则的默认链式转换器
func DefaultTransformer(logger *zap.Logger) *ChainTransformer {
	return NewChainTransformer(
		NewToolCallIDLengthTransformer(logger),
		NewEmptyContentTransformer(logger),
		NewToolCallIDTransformer(logger),
		NewUnsupportedFieldTransformer(logger),
		NewReasoningContentTransformer(logger),
		// 注意: ModalityFilterTransformer 需要模型信息，
		// 在 DefaultTransformerWithModel 中添加
	)
}

// DefaultTransformerWithModel 创建包含模型感知转换器的默认链式转换器
func DefaultTransformerWithModel(model string, logger *zap.Logger) *ChainTransformer {
	return NewChainTransformer(
		NewToolCallIDLengthTransformer(logger),
		NewEmptyContentTransformer(logger),
		NewToolCallIDTransformer(logger),
		NewUnsupportedFieldTransformer(logger),
		NewReasoningContentTransformer(logger),
		NewModalityFilterTransformer(model, logger),
	)
}

// --- 请求级转换器 ---

// RequestTransformer 定义请求级转换接口，用于修改 API 请求参数（而非消息内容）。
// 与 MessageTransformer 不同，RequestTransformer 可以修改 SDK 请求参数（如 reasoning_effort、cache_control 等）。
type RequestTransformer interface {
	// TransformRequest 对 SDK 请求参数进行 provider 感知的转换。
	// messages 为已转换为 SDK 格式的消息列表（可原地修改），params 为请求参数（可原地修改）。
	TransformRequest(params *RequestTransformContext)
}

// RequestTransformContext 封装请求转换所需的上下文信息
type RequestTransformContext struct {
	Provider string                                   // provider 名称（如 "anthropic"、"openai"）
	Model    string                                   // 当前使用的模型 ID
	Messages []openai.ChatCompletionMessageParamUnion // SDK 消息列表（可修改）
	Params   *openai.ChatCompletionNewParams          // SDK 请求参数（可修改）
	CacheKey string                                   // 去标识化、稳定的 prompt_cache_key
}

// ChainRequestTransformer 按顺序执行多个 RequestTransformer
type ChainRequestTransformer struct {
	transformers []RequestTransformer
}

// NewChainRequestTransformer 创建链式请求转换器
func NewChainRequestTransformer(transformers ...RequestTransformer) *ChainRequestTransformer {
	return &ChainRequestTransformer{transformers: transformers}
}

// TransformRequest 依次执行所有请求转换器
func (c *ChainRequestTransformer) TransformRequest(ctx *RequestTransformContext) {
	for _, t := range c.transformers {
		t.TransformRequest(ctx)
	}
}

// --- PromptCachingTransformer ---

// PromptCachingTransformer 按 Provider 注入缓存控制标记，可降低 40%+ token 成本。
//
// 策略:
//   - Anthropic: 对系统消息和最近 2 条 user 消息的 content block 注入 cache_control: {"type": "ephemeral"}
//   - OpenAI: 设置 prompt_cache_key（OpenAI 自动缓存，此字段提升命中率）
//   - 其他 Provider: 跳过（不支持或自动处理）
type PromptCachingTransformer struct {
	enabled bool
	logger  *zap.Logger
}

// NewPromptCachingTransformer 创建 Prompt 缓存转换器
func NewPromptCachingTransformer(enabled bool, logger *zap.Logger) *PromptCachingTransformer {
	return &PromptCachingTransformer{enabled: enabled, logger: logger}
}

// TransformRequest 根据 Provider 注入缓存控制参数。
// 仅当模型元数据表明支持 prompt caching 时才注入。
func (t *PromptCachingTransformer) TransformRequest(ctx *RequestTransformContext) {
	if !t.enabled {
		return
	}

	// 检查模型是否支持 prompt caching
	if meta := GetModelMeta(ctx.Model); meta != nil {
		if !meta.Capabilities.PromptCaching {
			if t.logger != nil {
				t.logger.Debug("Prompt 缓存: 跳过，模型不支持",
					zap.String("model", ctx.Model),
				)
			}
			return
		}
	}
	// 未知模型默认尝试注入（不阻塞新模型）

	provider := ctx.Provider

	switch {
	case isProvider(provider, "anthropic"):
		t.applyAnthropicCaching(ctx)
	case isProvider(provider, "openai"):
		t.applyOpenAICaching(ctx)
	case isProvider(provider, "bedrock"):
		t.applyBedrockCaching(ctx)
	default:
		// 其他 Provider 不需要显式缓存标记
		if t.logger != nil {
			t.logger.Debug("Prompt 缓存: 跳过，当前 provider 无需显式缓存标记",
				zap.String("provider", provider),
			)
		}
	}
}

// applyBedrockCaching 为 AWS Bedrock 上的 Anthropic Claude 消息注入 cache_point 标记。
//
// Bedrock Converse API 不用 Anthropic 原生的 cache_control，而是通过 `cachePoint`
// (openai-go SDK 透传时以 snake_case `cache_point` 键落到 ExtraFields) 控制分段缓存。
// 缓存规则对齐 Anthropic：系统消息 + 最近 2 条 user 消息。
func (t *PromptCachingTransformer) applyBedrockCaching(ctx *RequestTransformContext) {
	cachedCount := 0

	// 1. 系统消息
	for i := range ctx.Messages {
		msg := &ctx.Messages[i]
		if msg.OfSystem != nil {
			msg.OfSystem.SetExtraFields(map[string]any{
				"cache_point": map[string]string{"type": "default"},
			})
			cachedCount++
			break
		}
	}

	// 2. 最近 2 条 user 消息
	userCached := 0
	for i := len(ctx.Messages) - 1; i >= 0 && userCached < 2; i-- {
		msg := &ctx.Messages[i]
		if msg.OfUser != nil {
			msg.OfUser.SetExtraFields(map[string]any{
				"cache_point": map[string]string{"type": "default"},
			})
			userCached++
			cachedCount++
		}
	}

	if t.logger != nil && cachedCount > 0 {
		t.logger.Debug("Bedrock 缓存适配: 已注入 cache_point",
			zap.Int("cached_messages", cachedCount),
		)
	}
}

// applyAnthropicCaching 为 Anthropic 消息注入 cache_control 标记。
// 对系统消息和最近 2 条 user 消息的最后一个 content block 添加 cache_control。
func (t *PromptCachingTransformer) applyAnthropicCaching(ctx *RequestTransformContext) {
	cachedCount := 0

	// 1. 标记系统消息（通常在第一条）
	for i := range ctx.Messages {
		msg := &ctx.Messages[i]
		if msg.OfSystem != nil {
			injectCacheControlOnSystemMessage(msg)
			cachedCount++
			break
		}
	}

	// 2. 标记最近的 2 条 user 消息（从后往前遍历）
	userCached := 0
	for i := len(ctx.Messages) - 1; i >= 0 && userCached < 2; i-- {
		msg := &ctx.Messages[i]
		if msg.OfUser != nil {
			injectCacheControlOnUserMessage(msg)
			userCached++
			cachedCount++
		}
	}

	if t.logger != nil && cachedCount > 0 {
		t.logger.Debug("Anthropic 缓存适配: 已注入 cache_control",
			zap.Int("cached_messages", cachedCount),
		)
	}
}

// injectCacheControlOnSystemMessage 为系统消息的最后一个 content block 注入 Anthropic cache_control
func injectCacheControlOnSystemMessage(msg *openai.ChatCompletionMessageParamUnion) {
	if msg.OfSystem == nil {
		return
	}

	// 系统消息的 Content 可能是 string 或 parts 数组
	// 通过 SetExtraFields 在消息级别注入 cache_control
	// Anthropic API 会将 system message 视为单个 content block
	// openai-go SDK 不直接支持 Anthropic 的 cache_control 字段，
	// 通过 SetExtraFields 注入，Anthropic 兼容代理层会正确解析。
	msg.OfSystem.SetExtraFields(map[string]any{
		"cache_control": map[string]string{"type": "ephemeral"},
	})
}

// injectCacheControlOnUserMessage 为 user 消息注入 Anthropic cache_control
func injectCacheControlOnUserMessage(msg *openai.ChatCompletionMessageParamUnion) {
	if msg.OfUser == nil {
		return
	}

	// 通过 SetExtraFields 在消息级别注入 cache_control
	msg.OfUser.SetExtraFields(map[string]any{
		"cache_control": map[string]string{"type": "ephemeral"},
	})
}

// applyOpenAICaching 为 OpenAI 设置 prompt_cache_key 以提升缓存命中率。
// OpenAI 自动缓存，此字段为可选优化。
func (t *PromptCachingTransformer) applyOpenAICaching(ctx *RequestTransformContext) {
	cacheKey := ctx.CacheKey
	if cacheKey == "" {
		cacheKey = stablePromptCacheKey(ctx.Model, "", nil, nil)
	}
	if cacheKey != "" {
		ctx.Params.PromptCacheKey = openai.String(cacheKey)
		if t.logger != nil {
			t.logger.Debug("OpenAI 缓存适配: 已设置 prompt_cache_key",
				zap.String("cache_key", cacheKey),
			)
		}
	}
}

func stablePromptCacheKey(model, userID string, promptVersions []string, tools []mcphost.ToolDefinition) string {
	model = strings.TrimSpace(model)
	if model == "" {
		return ""
	}
	return strings.Join([]string{
		"v2",
		"model=" + model,
		"scope=" + shortHash(strings.TrimSpace(userID)),
		"prompt=" + shortHash(strings.Join(stableStringList(promptVersions), "\x00")),
		"tools=" + stableToolsetHash(tools),
	}, ":")
}

func stableToolsetHash(tools []mcphost.ToolDefinition) string {
	if len(tools) == 0 {
		return "none"
	}
	parts := make([]string, 0, len(tools))
	for _, tool := range stableToolDefinitions(tools) {
		parts = append(parts, strings.Join([]string{
			strings.TrimSpace(tool.Name),
			strings.TrimSpace(tool.Description),
			strings.TrimSpace(string(tool.InputSchema)),
		}, "\x00"))
	}
	return shortHash(strings.Join(parts, "\x1f"))
}

func stableStringList(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			out = append(out, value)
		}
	}
	sort.Strings(out)
	return out
}

func shortHash(value string) string {
	if value == "" {
		return "none"
	}
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])[:16]
}

// --- ReasoningVariantsTransformer ---

// ReasoningVariantsTransformer 将统一的 reasoning effort 级别映射到不同 Provider 的参数格式。
//
// 映射规则:
//   - OpenAI (o1/o3 系列): 设置 ReasoningEffort 字段 → "low"/"medium"/"high"
//   - Anthropic: 通过 extra fields 注入 thinking.type="enabled", thinking.budget_tokens 映射
//   - Google Gemini: 通过 extra fields 注入 thinkingConfig.thinkingBudget
//   - 其他 Provider: 跳过
type ReasoningVariantsTransformer struct {
	effort string // "low", "medium", "high"
	logger *zap.Logger
}

// NewReasoningVariantsTransformer 创建推理能力映射转换器
// effort 为统一的推理努力级别: "low", "medium", "high"
func NewReasoningVariantsTransformer(effort string, logger *zap.Logger) *ReasoningVariantsTransformer {
	return &ReasoningVariantsTransformer{effort: effort, logger: logger}
}

// TransformRequest 根据 Provider 映射 reasoning 参数
func (t *ReasoningVariantsTransformer) TransformRequest(ctx *RequestTransformContext) {
	if t.effort == "" {
		return
	}

	provider := ctx.Provider

	switch {
	case isProvider(provider, "openai"):
		t.applyOpenAIReasoning(ctx)
	case isProvider(provider, "anthropic"):
		t.applyAnthropicReasoning(ctx)
	case isProvider(provider, "google"):
		t.applyGeminiReasoning(ctx)
	case isProvider(provider, "bedrock"):
		t.applyBedrockReasoning(ctx)
	default:
		if t.logger != nil {
			t.logger.Debug("Reasoning 映射: 跳过，当前 provider 不支持 reasoning 配置",
				zap.String("provider", provider),
				zap.String("effort", t.effort),
			)
		}
	}
}

// applyOpenAIReasoning 为 OpenAI o-series 模型设置 ReasoningEffort
func (t *ReasoningVariantsTransformer) applyOpenAIReasoning(ctx *RequestTransformContext) {
	// OpenAI 仅支持 "low"、"medium"、"high"
	var effort shared.ReasoningEffort
	switch strings.ToLower(t.effort) {
	case "low":
		effort = shared.ReasoningEffortLow
	case "medium":
		effort = shared.ReasoningEffortMedium
	case "high", "max":
		// "max" 映射为 "high"（OpenAI 最高级别）
		effort = shared.ReasoningEffortHigh
	default:
		if t.logger != nil {
			t.logger.Warn("OpenAI Reasoning: 未知的 effort 级别，跳过",
				zap.String("effort", t.effort),
			)
		}
		return
	}

	ctx.Params.ReasoningEffort = effort
	if t.logger != nil {
		t.logger.Debug("OpenAI Reasoning 适配: 已设置 reasoning_effort",
			zap.String("effort", string(effort)),
		)
	}
}

// applyAnthropicReasoning 为 Anthropic 模型注入 thinking 配置。
// Anthropic 使用 thinking.type = "enabled" + thinking.budget_tokens 控制推理。
func (t *ReasoningVariantsTransformer) applyAnthropicReasoning(ctx *RequestTransformContext) {
	budgetTokens := effortToBudgetTokens(t.effort)

	// Anthropic thinking 参数通过 SetExtraFields 注入（SDK 不原生支持）。
	ctx.Params.SetExtraFields(map[string]any{
		"thinking": map[string]any{
			"type":          "enabled",
			"budget_tokens": budgetTokens,
		},
	})

	if t.logger != nil {
		t.logger.Debug("Anthropic Reasoning 适配: 已注入 thinking 配置",
			zap.String("effort", t.effort),
			zap.Int("budget_tokens", budgetTokens),
		)
	}
}

// applyBedrockReasoning 为 AWS Bedrock 上的 Anthropic Claude 模型注入 reasoningConfig。
// Bedrock 通过 additionalModelRequestFields.reasoningConfig.budget 控制 thinking token 预算，
// 字段名与 Bedrock Claude API 规范一致（非 Anthropic 原生的 thinking.budget_tokens）。
func (t *ReasoningVariantsTransformer) applyBedrockReasoning(ctx *RequestTransformContext) {
	budgetTokens := effortToBudgetTokens(t.effort)

	ctx.Params.SetExtraFields(map[string]any{
		"reasoningConfig": map[string]any{
			"budget": budgetTokens,
		},
	})

	if t.logger != nil {
		t.logger.Debug("Bedrock Reasoning 适配: 已注入 reasoningConfig",
			zap.String("effort", t.effort),
			zap.Int("budget", budgetTokens),
		)
	}
}

// applyGeminiReasoning 为 Google Gemini 模型注入 thinkingConfig。
// Gemini 使用 thinkingConfig.thinkingBudget 控制推理 token 预算。
func (t *ReasoningVariantsTransformer) applyGeminiReasoning(ctx *RequestTransformContext) {
	budgetTokens := effortToBudgetTokens(t.effort)

	// Gemini thinkingConfig 通过 SetExtraFields 注入（SDK 不原生支持）。
	ctx.Params.SetExtraFields(map[string]any{
		"thinkingConfig": map[string]any{
			"thinkingBudget": budgetTokens,
		},
	})

	if t.logger != nil {
		t.logger.Debug("Gemini Reasoning 适配: 已注入 thinkingConfig",
			zap.String("effort", t.effort),
			zap.Int("budget_tokens", budgetTokens),
		)
	}
}

// effortToBudgetTokens 将统一的 effort 级别映射为 token 预算。
// 用于 Anthropic thinking.budget_tokens、Gemini thinkingConfig.thinkingBudget 等。
func effortToBudgetTokens(effort string) int {
	switch strings.ToLower(effort) {
	case "low":
		return 1024
	case "medium":
		return 4096
	case "high":
		return 10240
	case "max":
		return 32768
	default:
		return 4096 // 默认中等
	}
}

// --- TemperatureDefaultsTransformer ---

// TemperatureDefaultsTransformer 按 Provider 设置合理的默认 Temperature。
//
// 默认值来源:
//   - OpenAI GPT-4o: 1.0（默认）
//   - Anthropic Claude: 1.0（默认）
//   - Google Gemini: 1.0（默认）
//   - DeepSeek: 0.7（官方推荐较低温度）
//   - Qwen: 0.55（官方推荐）
//   - GLM-4: 1.0
//
// 对 reasoning 模型（o1, o3, DeepSeek R1）不设置 temperature，
// 这些模型不接受此参数。
type TemperatureDefaultsTransformer struct {
	logger *zap.Logger
}

// NewTemperatureDefaultsTransformer 创建温度默认值转换器
func NewTemperatureDefaultsTransformer(logger *zap.Logger) *TemperatureDefaultsTransformer {
	return &TemperatureDefaultsTransformer{logger: logger}
}

// TransformRequest 如果请求中没有显式设置 Temperature，按 Provider/模型 设置默认值
func (t *TemperatureDefaultsTransformer) TransformRequest(ctx *RequestTransformContext) {
	// 如果已经显式设置了 Temperature，不覆盖
	if ctx.Params.Temperature.Valid() {
		return
	}

	// 检查是否为 reasoning 模型 —— reasoning 模型不接受 temperature 参数
	if isReasoningModel(ctx.Model) {
		if t.logger != nil {
			t.logger.Debug("Temperature 默认值: 跳过 reasoning 模型",
				zap.String("model", ctx.Model),
			)
		}
		return
	}

	// 按 Provider 设置默认温度
	var defaultTemp float64
	provider := ctx.Provider

	switch {
	case isProvider(provider, "deepseek"):
		defaultTemp = 0.7
	case isProvider(provider, "qwen") || strings.Contains(strings.ToLower(ctx.Model), "qwen"):
		defaultTemp = 0.55
	default:
		// OpenAI、Anthropic、Gemini、GLM 等默认 1.0
		defaultTemp = 1.0
	}

	ctx.Params.Temperature = openai.Float(defaultTemp)

	if t.logger != nil {
		t.logger.Debug("Temperature 默认值: 已设置",
			zap.String("provider", provider),
			zap.String("model", ctx.Model),
			zap.Float64("temperature", defaultTemp),
		)
	}
}

// --- MaxOutputTokensTransformer ---

// MaxOutputTokensTransformer 限制 MaxTokens 不超过模型的最大输出 token 上限。
//
// 处理逻辑:
//  1. 从 ModelMeta 获取模型的 MaxOutput 限制
//  2. 如果请求设置了 MaxTokens 且超过模型限制，截断到模型限制
//  3. 如果请求没设置 MaxTokens，设置一个合理的默认值: min(model.MaxOutput, 16384)
//  4. 对 reasoning 模型，通过 SetExtraFields 设置 max_completion_tokens 而非 max_tokens
type MaxOutputTokensTransformer struct {
	logger *zap.Logger
}

// NewMaxOutputTokensTransformer 创建最大输出 token 转换器
func NewMaxOutputTokensTransformer(logger *zap.Logger) *MaxOutputTokensTransformer {
	return &MaxOutputTokensTransformer{logger: logger}
}

// defaultMaxOutputTokens 默认最大输出 token 数
const defaultMaxOutputTokens = 16384

// TransformRequest 调整请求的 MaxTokens，确保不超过模型上限
func (t *MaxOutputTokensTransformer) TransformRequest(ctx *RequestTransformContext) {
	meta := GetModelMeta(ctx.Model)
	if meta == nil {
		// 未知模型，不做调整
		return
	}

	maxOutput := int64(meta.MaxOutput)
	if maxOutput <= 0 {
		return
	}

	isReasoning := isReasoningModel(ctx.Model)

	// 确定目标 token 数
	var targetTokens int64
	if ctx.Params.MaxTokens.Valid() {
		// 已设置，检查是否超过模型限制
		currentMax := ctx.Params.MaxTokens.Value
		if currentMax > maxOutput {
			targetTokens = maxOutput
			if t.logger != nil {
				t.logger.Debug("MaxOutputTokens: 截断超出模型限制的 MaxTokens",
					zap.String("model", ctx.Model),
					zap.Int64("requested", currentMax),
					zap.Int64("capped", maxOutput),
				)
			}
		} else {
			targetTokens = currentMax
		}
	} else {
		// 未设置，使用默认值: min(model.MaxOutput, defaultMaxOutputTokens)
		targetTokens = maxOutput
		if targetTokens > defaultMaxOutputTokens {
			targetTokens = defaultMaxOutputTokens
		}
		if t.logger != nil {
			t.logger.Debug("MaxOutputTokens: 设置默认值",
				zap.String("model", ctx.Model),
				zap.Int64("default", targetTokens),
			)
		}
	}

	if isReasoning {
		// reasoning 模型使用 max_completion_tokens 而非 max_tokens
		// reasoning 模型通过 SetExtraFields 注入 max_completion_tokens（部分模型如 o1 拒绝 max_tokens）。
		ctx.Params.MaxTokens = param.Opt[int64]{} // 清除 max_tokens
		ctx.Params.SetExtraFields(map[string]any{
			"max_completion_tokens": targetTokens,
		})
		if t.logger != nil {
			t.logger.Debug("MaxOutputTokens: reasoning 模型使用 max_completion_tokens",
				zap.String("model", ctx.Model),
				zap.Int64("max_completion_tokens", targetTokens),
			)
		}
	} else {
		ctx.Params.MaxTokens = openai.Int(targetTokens)
	}
}

// isReasoningModel 判断模型是否为 reasoning 类型（不接受 temperature/max_tokens 等参数）
func isReasoningModel(model string) bool {
	// 先查 ModelMeta
	if meta := GetModelMeta(model); meta != nil {
		return meta.Capabilities.Reasoning
	}

	// 基于模型名启发式判断
	lower := strings.ToLower(model)
	reasoningPrefixes := []string{"o1", "o3", "deepseek-reasoner", "deepseek-r1"}
	for _, prefix := range reasoningPrefixes {
		if strings.HasPrefix(lower, prefix) || strings.Contains(lower, prefix) {
			return true
		}
	}
	return false
}

// DefaultRequestTransformer 创建包含所有内置请求级转换器的默认链式转换器。
// reasoningEffort 为空字符串时不启用 reasoning 转换。
// storePrivacy 为 true 时为 OpenAI/Copilot 请求设置 store=false。
func DefaultRequestTransformer(reasoningEffort string, logger *zap.Logger, opts ...RequestTransformerOption) *ChainRequestTransformer {
	// 解析可选参数
	var options requestTransformerOptions
	for _, opt := range opts {
		opt(&options)
	}

	transformers := []RequestTransformer{
		NewPromptCachingTransformer(options.promptCacheKey, logger),
		NewTemperatureDefaultsTransformer(logger),
		NewMaxOutputTokensTransformer(logger),
		NewStorePrivacyTransformer(options.storePrivacy),
	}

	// 仅在配置了 reasoning effort 时才添加 reasoning 转换器
	if reasoningEffort != "" {
		transformers = append(transformers, NewReasoningVariantsTransformer(reasoningEffort, logger))
	}

	return NewChainRequestTransformer(transformers...)
}

// requestTransformerOptions 请求转换器可选参数
type requestTransformerOptions struct {
	storePrivacy   bool
	promptCacheKey bool
}

// RequestTransformerOption 请求转换器可选配置函数
type RequestTransformerOption func(*requestTransformerOptions)

// WithStorePrivacy 设置隐私保护选项
func WithStorePrivacy(enabled bool) RequestTransformerOption {
	return func(o *requestTransformerOptions) {
		o.storePrivacy = enabled
	}
}

func WithPromptCacheKey(enabled bool) RequestTransformerOption {
	return func(o *requestTransformerOptions) {
		o.promptCacheKey = enabled
	}
}

// --- Provider 检测辅助函数 ---

// isProvider 检查 provider 字符串是否匹配目标（不区分大小写）
func isProvider(provider, target string) bool {
	return strings.EqualFold(provider, target)
}

// DetectProvider 根据 baseURL 推断 LLM provider 名称
func DetectProvider(baseURL string) string {
	lower := strings.ToLower(baseURL)
	switch {
	case strings.Contains(lower, "anthropic"):
		return "anthropic"
	case strings.Contains(lower, "mistral"):
		return "mistral"
	// AWS Bedrock endpoint 形如 bedrock-runtime.{region}.amazonaws.com，
	// 必须排在 openai 分支之前，避免被通用匹配回落。
	case strings.Contains(lower, "bedrock-runtime") && strings.Contains(lower, "amazonaws.com"):
		return "bedrock"
	case strings.Contains(lower, "openai"):
		return "openai"
	case strings.Contains(lower, "deepseek"):
		return "deepseek"
	case strings.Contains(lower, "google") || strings.Contains(lower, "gemini"):
		return "google"
	default:
		return "openai" // 默认假设 OpenAI 兼容
	}
}

// --- 模型元数据 ---

// --- StorePrivacyTransformer ---

// StorePrivacyTransformer 为 OpenAI/Copilot 请求设置 store=false，防止数据用于模型训练。
// 当配置启用 StorePrivacy 时，对 openai/copilot/azure provider 的请求注入 store=false 参数。
type StorePrivacyTransformer struct {
	enabled bool
}

// NewStorePrivacyTransformer 创建隐私保护转换器
func NewStorePrivacyTransformer(enabled bool) *StorePrivacyTransformer {
	return &StorePrivacyTransformer{enabled: enabled}
}

// TransformRequest 为支持的 provider 注入 store=false 参数
func (t *StorePrivacyTransformer) TransformRequest(ctx *RequestTransformContext) {
	if !t.enabled {
		return
	}

	// 仅对 OpenAI 兼容的 provider 设置 store=false
	switch ctx.Provider {
	case "openai", "copilot", "azure":
		ctx.Params.Store = param.Null[bool]()
	}
}

// --- 模型元数据 ---

// ModelCapabilities 描述模型支持的能力特性
type ModelCapabilities struct {
	Reasoning     bool `json:"reasoning"`      // 是否支持 reasoning/thinking
	PromptCaching bool `json:"prompt_caching"` // 是否支持 prompt caching
	Vision        bool `json:"vision"`         // 是否支持图片输入
	Audio         bool `json:"audio"`          // 是否支持音频输入
	PDF           bool `json:"pdf"`            // 是否支持 PDF 输入
	ToolUse       bool `json:"tool_use"`       // 是否支持工具调用
	JSON          bool `json:"json"`           // 是否支持 JSON mode
	Streaming     bool `json:"streaming"`      // 是否支持流式输出

	// Reasoning 相关：支持的 effort 级别，如 ["low","medium","high","max"]
	ReasoningEfforts []string `json:"reasoning_efforts,omitempty"`

	// 缓存类型: "ephemeral" (Anthropic), "auto" (OpenAI), "" (不支持)
	CacheType string `json:"cache_type,omitempty"`
}

// ModelMeta 描述模型的能力和成本信息
type ModelMeta struct {
	Name               string            `json:"name"`                  // 模型显示名称
	ContextWindow      int               `json:"context_window"`        // 上下文窗口大小（token 数）
	MaxOutput          int               `json:"max_output"`            // 最大输出 token 数
	CostPerInputToken  float64           `json:"cost_per_input_token"`  // 每个输入 token 的成本（美元）
	CostPerOutputToken float64           `json:"cost_per_output_token"` // 每个输出 token 的成本（美元）
	Capabilities       ModelCapabilities `json:"capabilities"`          // 扩展能力元数据
}

// SupportsVision 返回模型是否支持图像输入
func (m ModelMeta) SupportsVision() bool { return m.Capabilities.Vision }

// SupportsTools 返回模型是否支持工具调用
func (m ModelMeta) SupportsTools() bool { return m.Capabilities.ToolUse }

// SupportsJSON 返回模型是否支持 JSON mode
func (m ModelMeta) SupportsJSON() bool { return m.Capabilities.JSON }

// 预定义常用模型的元数据
var modelRegistry = map[string]ModelMeta{
	"gpt-4o": {
		Name:          "GPT-4o",
		ContextWindow: 128000,
		MaxOutput:     16384,

		CostPerInputToken:  2.5e-6,
		CostPerOutputToken: 10e-6,
		Capabilities: ModelCapabilities{
			Vision:        true,
			ToolUse:       true,
			JSON:          true,
			Streaming:     true,
			PromptCaching: true,
			CacheType:     "auto",
		},
	},
	"gpt-4o-mini": {
		Name:          "GPT-4o Mini",
		ContextWindow: 128000,
		MaxOutput:     16384,

		CostPerInputToken:  0.15e-6,
		CostPerOutputToken: 0.6e-6,
		Capabilities: ModelCapabilities{
			Vision:        true,
			ToolUse:       true,
			JSON:          true,
			Streaming:     true,
			PromptCaching: true,
			CacheType:     "auto",
		},
	},
	"gpt-4-turbo": {
		Name:          "GPT-4 Turbo",
		ContextWindow: 128000,
		MaxOutput:     4096,

		CostPerInputToken:  10e-6,
		CostPerOutputToken: 30e-6,
		Capabilities: ModelCapabilities{
			Vision:    true,
			ToolUse:   true,
			JSON:      true,
			Streaming: true,
		},
	},
	"o1": {
		Name:          "OpenAI o1",
		ContextWindow: 200000,
		MaxOutput:     100000,

		CostPerInputToken:  15e-6,
		CostPerOutputToken: 60e-6,
		Capabilities: ModelCapabilities{
			Reasoning:        true,
			ReasoningEfforts: []string{"low", "medium", "high"},
			Vision:           true,
			ToolUse:          true,
			JSON:             true,
			Streaming:        true,
			PromptCaching:    true,
			CacheType:        "auto",
		},
	},
	"o3-mini": {
		Name:          "OpenAI o3-mini",
		ContextWindow: 200000,
		MaxOutput:     100000,

		CostPerInputToken:  1.1e-6,
		CostPerOutputToken: 4.4e-6,
		Capabilities: ModelCapabilities{
			Reasoning:        true,
			ReasoningEfforts: []string{"low", "medium", "high"},
			ToolUse:          true,
			JSON:             true,
			Streaming:        true,
			PromptCaching:    true,
			CacheType:        "auto",
		},
	},
	"claude-3-5-sonnet-20241022": {
		Name:          "Claude 3.5 Sonnet",
		ContextWindow: 200000,
		MaxOutput:     8192,

		CostPerInputToken:  3e-6,
		CostPerOutputToken: 15e-6,
		Capabilities: ModelCapabilities{
			Vision:        true,
			PDF:           true,
			ToolUse:       true,
			JSON:          true,
			Streaming:     true,
			PromptCaching: true,
			CacheType:     "ephemeral",
		},
	},
	"claude-3-opus-20240229": {
		Name:          "Claude 3 Opus",
		ContextWindow: 200000,
		MaxOutput:     4096,

		CostPerInputToken:  15e-6,
		CostPerOutputToken: 75e-6,
		Capabilities: ModelCapabilities{
			Vision:        true,
			ToolUse:       true,
			JSON:          true,
			Streaming:     true,
			PromptCaching: true,
			CacheType:     "ephemeral",
		},
	},
	"claude-3-haiku-20240307": {
		Name:          "Claude 3 Haiku",
		ContextWindow: 200000,
		MaxOutput:     4096,

		CostPerInputToken:  0.25e-6,
		CostPerOutputToken: 1.25e-6,
		Capabilities: ModelCapabilities{
			Vision:        true,
			ToolUse:       true,
			JSON:          true,
			Streaming:     true,
			PromptCaching: true,
			CacheType:     "ephemeral",
		},
	},
	"deepseek-chat": {
		Name:          "DeepSeek Chat",
		ContextWindow: 64000,
		MaxOutput:     8192,

		CostPerInputToken:  0.14e-6,
		CostPerOutputToken: 0.28e-6,
		Capabilities: ModelCapabilities{
			ToolUse:   true,
			JSON:      true,
			Streaming: true,
		},
	},
	"deepseek-reasoner": {
		Name:          "DeepSeek Reasoner",
		ContextWindow: 64000,
		MaxOutput:     8192,

		CostPerInputToken:  0.55e-6,
		CostPerOutputToken: 2.19e-6,
		Capabilities: ModelCapabilities{
			Reasoning:        true,
			ReasoningEfforts: []string{"low", "medium", "high"},
			Streaming:        true,
		},
	},
	"mistral-large-latest": {
		Name:          "Mistral Large",
		ContextWindow: 128000,
		MaxOutput:     4096,

		CostPerInputToken:  2e-6,
		CostPerOutputToken: 6e-6,
		Capabilities: ModelCapabilities{
			ToolUse:   true,
			JSON:      true,
			Streaming: true,
		},
	},
	"gemini-1.5-pro": {
		Name:          "Gemini 1.5 Pro",
		ContextWindow: 2000000,
		MaxOutput:     8192,

		CostPerInputToken:  1.25e-6,
		CostPerOutputToken: 5e-6,
		Capabilities: ModelCapabilities{
			Vision:    true,
			Audio:     true,
			PDF:       true,
			ToolUse:   true,
			JSON:      true,
			Streaming: true,
		},
	},
	"gemini-2.0-flash": {
		Name:          "Gemini 2.0 Flash",
		ContextWindow: 1000000,
		MaxOutput:     8192,

		CostPerInputToken:  0.075e-6,
		CostPerOutputToken: 0.3e-6,
		Capabilities: ModelCapabilities{
			Vision:    true,
			Audio:     true,
			ToolUse:   true,
			JSON:      true,
			Streaming: true,
		},
	},

	// --- 中国 LLM Provider ---

	// 字节豆包 (Doubao / 火山引擎 ARK)
	"doubao-1.5-pro-32k": {
		Name:          "豆包 1.5 Pro 32K",
		ContextWindow: 32000,
		MaxOutput:     4096,
		Capabilities: ModelCapabilities{
			ToolUse:   true,
			Streaming: true,
		},
	},
	"doubao-1.5-pro-256k": {
		Name:          "豆包 1.5 Pro 256K",
		ContextWindow: 256000,
		MaxOutput:     4096,
		Capabilities: ModelCapabilities{
			ToolUse:   true,
			Streaming: true,
		},
	},

	// 通义千问 (Qwen / 阿里云 DashScope)
	"qwen-max": {
		Name:          "通义千问 Max",
		ContextWindow: 32000,
		MaxOutput:     8192,
		Capabilities: ModelCapabilities{
			Vision:    true,
			ToolUse:   true,
			JSON:      true,
			Streaming: true,
		},
	},
	"qwen-plus": {
		Name:          "通义千问 Plus",
		ContextWindow: 131072,
		MaxOutput:     8192,
		Capabilities: ModelCapabilities{
			Vision:    true,
			ToolUse:   true,
			JSON:      true,
			Streaming: true,
		},
	},
	"qwen-turbo": {
		Name:          "通义千问 Turbo",
		ContextWindow: 131072,
		MaxOutput:     8192,
		Capabilities: ModelCapabilities{
			ToolUse:   true,
			JSON:      true,
			Streaming: true,
		},
	},

	// 百度千帆 (Qianfan / 文心一言 ERNIE)
	"ernie-4.0-8k": {
		Name:          "文心一言 4.0",
		ContextWindow: 8192,
		MaxOutput:     4096,
		Capabilities: ModelCapabilities{
			ToolUse:   true,
			Streaming: true,
		},
	},
	"ernie-4.0-turbo-8k": {
		Name:          "文心一言 4.0 Turbo",
		ContextWindow: 8192,
		MaxOutput:     4096,
		Capabilities: ModelCapabilities{
			ToolUse:   true,
			Streaming: true,
		},
	},

	// Moonshot / Kimi
	"moonshot-v1-128k": {
		Name:          "Moonshot v1 128K",
		ContextWindow: 128000,
		MaxOutput:     4096,
		Capabilities: ModelCapabilities{
			ToolUse:   true,
			JSON:      true,
			Streaming: true,
		},
	},
	"moonshot-v1-32k": {
		Name:          "Moonshot v1 32K",
		ContextWindow: 32000,
		MaxOutput:     4096,
		Capabilities: ModelCapabilities{
			ToolUse:   true,
			JSON:      true,
			Streaming: true,
		},
	},

	// MiniMax / 海螺 AI
	"MiniMax-Text-01": {
		Name:          "MiniMax Text 01",
		ContextWindow: 1000000,
		MaxOutput:     4096,
		Capabilities: ModelCapabilities{
			Vision:    true,
			ToolUse:   true,
			Streaming: true,
		},
	},
}

// GetModelMeta 根据模型 ID 获取元数据。
// 优先从全局动态注册表查询，若未初始化则回退到静态注册表。
// 如果模型 ID 未在注册表中，返回 nil。
func GetModelMeta(modelID string) *ModelMeta {
	// 优先使用动态注册表
	if reg := getGlobalModelRegistry(); reg != nil {
		return reg.Get(modelID)
	}

	// 回退到静态注册表（向后兼容）
	meta, ok := modelRegistry[modelID]
	if !ok {
		return nil
	}
	return &meta
}

// ListModelMetas 返回所有已注册的模型元数据。
// 优先从全局动态注册表查询，若未初始化则回退到静态注册表。
func ListModelMetas() map[string]ModelMeta {
	// 优先使用动态注册表
	if reg := getGlobalModelRegistry(); reg != nil {
		return reg.List()
	}

	// 回退到静态注册表（向后兼容）
	result := make(map[string]ModelMeta, len(modelRegistry))
	for k, v := range modelRegistry {
		result[k] = v
	}
	return result
}
