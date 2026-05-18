package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
	"go.uber.org/zap"

	"github.com/chef-guo/agents-hive/internal/errs"
	"github.com/chef-guo/agents-hive/internal/mcphost"
	"github.com/chef-guo/agents-hive/internal/resilience"
)

// 包级缓存，跨所有 Client 实例共享
// 记录哪些 BaseURL 不支持 JSON mode（response_format 参数）
// 存储 time.Time 值表示标记时间，24 小时后过期
var unsupportedJSONModeCache sync.Map // map[string]time.Time

// jsonModeCacheTTL JSON mode 缓存过期时间
const jsonModeCacheTTL = 24 * time.Hour

// retryableStatusCodes 可重试的 HTTP 状态码
var retryableStatusCodes = map[int]bool{
	429: true, // Rate Limit
	500: true, // Internal Server Error
	502: true, // Bad Gateway
	503: true, // Service Unavailable
}

// isRetryableError 判断错误是否为可重试的 LLM API 错误。
// 对 429、500、502、503 返回 true；对 400、401、403 等客户端错误返回 false。
// 同时检查 errs.Error 的 Retryable 标记，以支持流式重试场景中被包装的错误。
func isRetryableError(err error) bool {
	if errs.IsRetryable(err) {
		return true
	}
	var apiErr *openai.Error
	if !errors.As(err, &apiErr) {
		return false
	}
	return retryableStatusCodes[apiErr.StatusCode]
}

// llmRetryPolicy 是 LLM API 调用的默认重试策略。
var llmRetryPolicy = &resilience.ExponentialBackoff{
	MaxAttempts:  3,
	BaseDelay:    1 * time.Second,
	MaxDelay:     30 * time.Second,
	MaxJitterPct: 0.3,
	IsRetryable:  isRetryableError,
}

// retryableAPICall 对 fn 执行带指数退避的重试（委托给 resilience.Do）。
func retryableAPICall[T any](ctx context.Context, logger *zap.Logger, callName string, fn func() (T, error)) (T, error) {
	return resilience.Do(ctx, llmRetryPolicy, logger, callName, fn)
}

// Client 封装 OpenAI API，用于计划生成和代理推理。
type Client struct {
	mu                 sync.RWMutex
	client             openai.Client
	model              string
	apiKey             string // 保存用于 Reconfigure 时重建 client
	logger             *zap.Logger
	baseURL            string // 用于缓存键，标识 API 端点
	disableJSONMode    bool   // 显式禁用 JSON mode
	provider           ProviderDef
	transformer        *ChainTransformer        // Provider 感知的消息转换器
	requestTransformer *ChainRequestTransformer // 请求级转换器（缓存、推理等）
	reasoningEffort    string                   // 统一的推理努力级别
	storePrivacy       bool                     // 隐私保护选项
	promptCacheKey     bool                     // 是否设置 prompt_cache_key
	serviceTier        string                   // 交互式请求 service_tier
}

// isResponseFormatError 检测错误是否由不支持的 response_format 参数引起。
//
// 检测标准：
// 1. HTTP 状态码为 400（Bad Request）
// 2. 错误消息包含 "response_format" 关键词，或包含中文"不合法"/"unsupported"
//
// 适用场景：
// - OpenAI 兼容 API 不支持 response_format 参数
// - 例如 gmini.xyz、DeepSeek 等第三方 API
func isResponseFormatError(err error) bool {
	var apiErr *openai.Error
	if !errors.As(err, &apiErr) {
		return false
	}

	// 必须是 400 错误
	if apiErr.StatusCode != 400 {
		return false
	}

	// 检查错误消息关键词（支持中英文）
	msg := strings.ToLower(apiErr.Message)

	// 必须包含 response_format，或包含明确的中文/英文不支持提示
	if strings.Contains(msg, "response_format") {
		return true
	}

	// 中文错误："不合法的response_format"
	if strings.Contains(msg, "不合法") && strings.Contains(msg, "response") {
		return true
	}

	// 英文错误："unsupported response_format"
	if strings.Contains(msg, "unsupported") && strings.Contains(msg, "response") {
		return true
	}

	return false
}

// shouldSkipJSONMode 判断是否应跳过 JSON mode。
//
// 决策优先级：
// 1. 显式配置 disableJSONMode = true → 跳过
// 2. 缓存中标记该 BaseURL 不支持 → 跳过
// 3. 其他情况 → 不跳过
func (c *Client) shouldSkipJSONMode() bool {
	// 优先检查显式配置
	if c.disableJSONMode {
		return true
	}

	// 查询缓存（BaseURL 为空时不检查）
	if c.baseURL == "" {
		return false
	}

	if markedAt, ok := unsupportedJSONModeCache.Load(c.baseURL); ok {
		// 检查 TTL，过期则清除
		if t, ok := markedAt.(time.Time); ok && time.Since(t) < jsonModeCacheTTL {
			return true
		}
		unsupportedJSONModeCache.Delete(c.baseURL)
	}
	return false
}

// markJSONModeUnsupported 标记当前 API 端点不支持 JSON mode。
//
// 副作用：
// - 更新包级缓存 unsupportedJSONModeCache
// - 记录 Info 级别日志
func (c *Client) markJSONModeUnsupported() {
	if c.baseURL == "" {
		return
	}

	unsupportedJSONModeCache.Store(c.baseURL, time.Now())
	c.logger.Info("检测到 API 端点不支持 JSON mode，后续请求将自动跳过",
		zap.String("base_url", c.baseURL),
	)
}

// normalizeBaseURL 规范化 LLM API 的 baseURL，确保包含 /v1 路径。
//
// 处理逻辑：
// - 空字符串 → 返回空（使用 SDK 默认）
// - https://gmini.xyz → https://gmini.xyz/v1
// - https://gmini.xyz/ → https://gmini.xyz/v1
// - https://gmini.xyz/v1 → 保持不变
// - http://localhost:18080 → http://localhost:18080/v1
// - https://api.custom.com/custom/path → 保持不变（自定义路径）
func normalizeBaseURL(baseURL string) string {
	if baseURL == "" {
		return ""
	}

	u, err := url.Parse(baseURL)
	if err != nil {
		// 无效 URL，让 SDK 处理错误
		return baseURL
	}

	// 如果没有 scheme（http/https），说明不是有效的 URL，保持原样
	if u.Scheme == "" {
		return baseURL
	}

	// 路径为空或仅为 /，添加 /v1
	if u.Path == "" || u.Path == "/" {
		u.Path = "/v1"
		return u.String()
	}

	// 检查是否已有 /v1 后缀（忽略大小写）
	pathLower := strings.ToLower(u.Path)
	if strings.HasSuffix(pathLower, "/v1") || strings.HasSuffix(pathLower, "/v1/") {
		// 已经有 /v1 后缀，保持不变
		return baseURL
	}

	// 仅对简单路径（1个段或更少）自动添加 /v1
	// 复杂路径（如 /custom/endpoint）视为用户有意为之
	segments := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(segments) <= 1 {
		u.Path = strings.TrimSuffix(u.Path, "/") + "/v1"
		return u.String()
	}

	// 复杂自定义路径，保持不变
	return baseURL
}

// ClientConfig 保存创建新 Client 的参数。
type ClientConfig struct {
	APIKey          string
	BaseURL         string
	Model           string
	DisableJSONMode bool
	Provider        ProviderDef
	ReasoningEffort string // 统一的推理努力级别: "low"/"medium"/"high"/"max"，空字符串表示不启用
	StorePrivacy    bool   // 隐私保护：为 OpenAI/Copilot 请求设置 store=false
	PromptCacheKey  bool   // 是否设置 prompt_cache_key
	ServiceTier     string // 交互式请求 service_tier，空表示不设置
}

// NewClient 创建一个新的 LLM client。
func NewClient(cfg ClientConfig, logger *zap.Logger) *Client {
	// 模型模糊匹配
	matcher := NewModelMatcher()
	if matched, suggestions := matcher.Match(cfg.Model); matched != "" {
		logger.Debug("模型名匹配成功",
			zap.String("input", cfg.Model),
			zap.String("matched", matched))
		cfg.Model = matched
	} else if len(suggestions) > 0 {
		// 给出建议但不阻止创建（可能是自定义模型）
		logger.Warn("模型名未找到精确匹配，可能的选项",
			zap.String("input", cfg.Model),
			zap.Strings("suggestions", suggestions))
	}

	opts := []option.RequestOption{
		option.WithAPIKey(cfg.APIKey),
	}

	var normalizedURL string
	if cfg.BaseURL != "" {
		normalizedURL = normalizeBaseURL(cfg.BaseURL)
		logger.Debug("规范化 LLM API baseURL",
			zap.String("original", cfg.BaseURL),
			zap.String("normalized", normalizedURL),
		)
		opts = append(opts, option.WithBaseURL(normalizedURL))
	}

	return &Client{
		client:             openai.NewClient(opts...),
		model:              cfg.Model,
		apiKey:             cfg.APIKey,
		logger:             logger,
		baseURL:            normalizedURL,
		disableJSONMode:    cfg.DisableJSONMode,
		provider:           cfg.Provider,
		transformer:        DefaultTransformerWithModel(cfg.Model, logger),
		requestTransformer: DefaultRequestTransformer(cfg.ReasoningEffort, logger, WithStorePrivacy(cfg.StorePrivacy), WithPromptCacheKey(cfg.PromptCacheKey)),
		reasoningEffort:    cfg.ReasoningEffort,
		storePrivacy:       cfg.StorePrivacy,
		promptCacheKey:     cfg.PromptCacheKey,
		serviceTier:        strings.TrimSpace(cfg.ServiceTier),
	}
}

// HasCapability 检查当前 provider 是否支持指定功能
func (c *Client) HasCapability(cap Capability) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.provider.HasCapability(cap)
}

// SetModel 更新当前激活的模型。
func (c *Client) SetModel(model string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.model = model
}

// Model 返回当前激活的模型 ID。
func (c *Client) Model() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.model
}

// snapshot 在读锁下返回可变字段的一致性快照。
func (c *Client) snapshot() (model string, baseURL string, provider ProviderDef) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.model, c.baseURL, c.provider
}

// useResponsesAPI 返回当前 Provider 是否配置为使用 Responses API。
func (c *Client) useResponsesAPI() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.provider.UseResponsesAPI()
}

// Reconfigure 重建底层 Client，支持 baseURL 热切换。
func (c *Client) Reconfigure(model, baseURL string, provider ProviderDef) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.model = model
	c.provider = provider

	// 重建消息转换器和请求转换器（provider/model 可能已切换）
	c.transformer = DefaultTransformerWithModel(model, c.logger)
	c.requestTransformer = DefaultRequestTransformer(c.reasoningEffort, c.logger, WithStorePrivacy(c.storePrivacy), WithPromptCacheKey(c.promptCacheKey))

	if baseURL != "" && baseURL != c.baseURL {
		c.baseURL = baseURL
		normalized := normalizeBaseURL(baseURL)
		opts := []option.RequestOption{option.WithBaseURL(normalized)}
		if c.apiKey != "" {
			opts = append(opts, option.WithAPIKey(c.apiKey))
		}
		c.client = openai.NewClient(opts...)
	}
}

// logAPIError 记录详细的 LLM API 错误信息，包括原始响应内容。
// 这对于调试非标准 API 端点（返回 HTML 错误页面等）特别有用。
func (c *Client) logAPIError(err error, context string) {
	var apiErr *openai.Error
	if errors.As(err, &apiErr) {
		c.logger.Error("LLM API 错误",
			zap.String("context", context),
			zap.Int("status_code", apiErr.StatusCode),
			zap.String("error_type", apiErr.Type),
			zap.String("error_code", apiErr.Code),
			zap.String("error_message", apiErr.Message),
			zap.String("raw_response", apiErr.RawJSON()),
		)

		// Debug 级别记录完整响应（包括 headers）
		// 注意：DumpResponse 可能在某些情况下返回 nil（如测试环境），需要安全检查
		if c.logger.Core().Enabled(zap.DebugLevel) {
			responseDump := apiErr.DumpResponse(true)
			if len(responseDump) > 0 {
				c.logger.Debug("完整 HTTP 响应转储",
					zap.String("context", context),
					zap.ByteString("response", responseDump),
				)
			}
		}
	}
}

// validateContent 检查多模态内容是否被当前 provider 支持。
// 在 Chat/ChatWithTools 入口调用，需在 snapshot 之后使用 provider。
func (c *Client) validateContent(content Content, provider ProviderDef) error {
	if !content.IsMultimodal() {
		return nil
	}
	for _, part := range content.Parts() {
		switch part.Type {
		case ContentImage:
			if !provider.HasCapability(CapVision) {
				return errs.New(errs.CodeInvalidInput, fmt.Sprintf("provider %q 不支持 vision", provider.Name))
			}
		case ContentAudio:
			if !provider.HasCapability(CapAudio) {
				return errs.New(errs.CodeInvalidInput, fmt.Sprintf("provider %q 不支持 audio", provider.Name))
			}
		case ContentFile:
			if !provider.HasCapability(CapFile) {
				return errs.New(errs.CodeInvalidInput, fmt.Sprintf("provider %q 不支持 file", provider.Name))
			}
		}
	}
	return nil
}

// Message 表示一条聊天消息。
type Message struct {
	Role    string  `json:"role"`
	Content Content `json:"content"`
}

// ChatRequest 保存聊天补全的参数。
type ChatRequest struct {
	SystemPrompt   string
	Messages       []Message
	Temperature    float64
	MaxTokens      int64
	JSONMode       bool
	UserID         string
	PromptVersions []string
}

// ChatResponse 保存聊天补全的结果。
type ChatResponse struct {
	Content      string
	FinishReason string
	Usage        Usage
}

// Usage 跟踪 Token 消耗。
type Usage struct {
	PromptTokens     int64
	CompletionTokens int64
	TotalTokens      int64
}

// Chat 发送聊天补全请求并返回响应。
func (c *Client) Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error) {
	// Responses API 分发
	if c.useResponsesAPI() {
		return c.chatViaResponses(ctx, req)
	}

	// 获取当前 model 快照（线程安全）
	snapModel, _, snapProvider := c.snapshot()

	// 检查 reasoning 模型是否配置了推理努力级别
	if meta := GetModelMeta(snapModel); meta != nil && meta.Capabilities.Reasoning && c.reasoningEffort == "" {
		c.logger.Debug("当前模型支持 reasoning，可通过配置 reasoning_effort 启用推理能力",
			zap.String("model", snapModel),
			zap.Strings("supported_efforts", meta.Capabilities.ReasoningEfforts),
		)
	}

	// 验证多模态内容是否被 provider 支持
	for _, msg := range req.Messages {
		if msg.Role == "user" {
			if err := c.validateContent(msg.Content, snapProvider); err != nil {
				return nil, err
			}
		}
	}

	// 1. 构建消息列表
	messages := make([]openai.ChatCompletionMessageParamUnion, 0, len(req.Messages)+1)

	// JSON mode fallback prompt（当不使用 response_format 时增强 prompt）
	const jsonFallbackSuffix = "\n\nCRITICAL: 你必须只返回有效的 JSON。不要在 JSON 对象之前或之后包含任何文本。格式示例: {\"key\": \"value\", ...}"

	// 检查是否应跳过 JSON mode
	shouldSkip := req.JSONMode && c.shouldSkipJSONMode()
	systemPrompt := req.SystemPrompt
	if shouldSkip {
		// 使用 prompt engineering 替代
		systemPrompt += jsonFallbackSuffix
		c.logger.Debug("跳过 JSON mode，使用 prompt 增强",
			zap.String("base_url", c.baseURL),
			zap.Bool("from_cache", !c.disableJSONMode),
		)
	}

	if systemPrompt != "" {
		messages = append(messages, openai.SystemMessage(systemPrompt))
	}

	for mi, msg := range req.Messages {
		switch msg.Role {
		case "system":
			messages = append(messages, openai.SystemMessage(msg.Content.Text()))
		case "user":
			if msg.Content.IsMultimodal() {
				c.logger.Info("[DEBUG-UPLOAD] 非流式: 发送多模态 user 消息",
					zap.Int("msg_index", mi),
					zap.Int("parts", len(msg.Content.Parts())),
				)
			}
			messages = append(messages, toSDKUserMessage(msg.Content))
		case "assistant":
			messages = append(messages, openai.AssistantMessage(msg.Content.Text()))
		}
	}

	// 2. 构建请求参数
	params := openai.ChatCompletionNewParams{
		Model:    snapModel,
		Messages: messages,
	}

	if req.Temperature > 0 {
		params.Temperature = openai.Float(req.Temperature)
	}
	if req.MaxTokens > 0 {
		params.MaxTokens = openai.Int(req.MaxTokens)
	}

	// 条件设置 response_format（仅当 JSON mode 且不应跳过时）
	useJSONMode := req.JSONMode && !shouldSkip
	if useJSONMode {
		params.ResponseFormat = openai.ChatCompletionNewParamsResponseFormatUnion{
			OfJSONObject: &openai.ResponseFormatJSONObjectParam{},
		}
	}

	c.logger.Debug("发送聊天补全请求",
		zap.String("model", snapModel),
		zap.Int("message_count", len(messages)),
		zap.Bool("json_mode", useJSONMode),
	)

	// 2.5 请求级转换（缓存控制、推理参数等）
	if c.requestTransformer != nil {
		providerName := snapProvider.Name
		if providerName == "" {
			providerName = DetectProvider(c.baseURL)
		}
		c.requestTransformer.TransformRequest(&RequestTransformContext{
			Provider: providerName,
			Model:    snapModel,
			Messages: messages,
			Params:   &params,
			CacheKey: stablePromptCacheKey(snapModel, req.UserID, req.PromptVersions, nil),
		})
		// 回写可能被修改的 messages
		params.Messages = messages
	}

	// 3. 发送请求（带指数退避重试）
	completion, err := retryableAPICall(ctx, c.logger, "chat_completion", func() (*openai.ChatCompletion, error) {
		return c.client.Chat.Completions.New(ctx, params)
	})

	// 4. 自动重试逻辑：如果是 response_format 错误，重试不带 JSON mode
	if err != nil && isResponseFormatError(err) {
		c.logger.Warn("API 端点不支持 response_format，重试不带 JSON mode",
			zap.String("base_url", c.baseURL),
			zap.Error(err),
		)

		// 标记为不支持，未来请求将跳过
		c.markJSONModeUnsupported()

		// 重试：移除 response_format，增强 prompt
		if req.JSONMode {
			// 重新构建消息（增强 system prompt）
			retryMessages := make([]openai.ChatCompletionMessageParamUnion, 0, len(req.Messages)+1)
			if req.SystemPrompt != "" {
				retryMessages = append(retryMessages, openai.SystemMessage(req.SystemPrompt+jsonFallbackSuffix))
			}
			for _, msg := range req.Messages {
				switch msg.Role {
				case "system":
					retryMessages = append(retryMessages, openai.SystemMessage(msg.Content.Text()))
				case "user":
					retryMessages = append(retryMessages, toSDKUserMessage(msg.Content))
				case "assistant":
					retryMessages = append(retryMessages, openai.AssistantMessage(msg.Content.Text()))
				}
			}

			// 重新构建参数（不带 ResponseFormat）
			retryParams := openai.ChatCompletionNewParams{
				Model:    snapModel,
				Messages: retryMessages,
			}
			if req.Temperature > 0 {
				retryParams.Temperature = openai.Float(req.Temperature)
			}
			if req.MaxTokens > 0 {
				retryParams.MaxTokens = openai.Int(req.MaxTokens)
			}

			c.logger.Debug("重试请求（不带 JSON mode）",
				zap.String("model", snapModel),
			)

			completion, err = retryableAPICall(ctx, c.logger, "chat_completion_json_fallback", func() (*openai.ChatCompletion, error) {
				return c.client.Chat.Completions.New(ctx, retryParams)
			})
		}
	}

	// 5. 处理最终错误
	if err != nil {
		c.logAPIError(err, "chat_completion")
		return nil, errs.Wrap(errs.CodePlanGenFailed, "聊天补全失败", err)
	}

	if len(completion.Choices) == 0 {
		return nil, errs.New(errs.CodePlanGenFailed, "补全响应中没有 choices")
	}

	// 6. 解析响应
	choice := completion.Choices[0]
	cleanedContent, _ := stripThinkTags(choice.Message.Content)

	// 验证降级模式返回的 JSON 格式
	if shouldSkip && req.JSONMode && !json.Valid([]byte(cleanedContent)) {
		return nil, errs.New(errs.CodeLLMResponseInvalid, "JSON 模式降级后返回的内容不是有效 JSON")
	}

	resp := &ChatResponse{
		Content:      cleanedContent,
		FinishReason: string(choice.FinishReason),
		Usage: Usage{
			PromptTokens:     completion.Usage.PromptTokens,
			CompletionTokens: completion.Usage.CompletionTokens,
			TotalTokens:      completion.Usage.TotalTokens,
		},
	}

	c.logger.Debug("收到聊天补全响应",
		zap.Int64("prompt_tokens", resp.Usage.PromptTokens),
		zap.Int64("completion_tokens", resp.Usage.CompletionTokens),
	)

	return resp, nil
}

// ChatJSON 发送聊天请求并将 JSON 响应反序列化到 dst。
func (c *Client) ChatJSON(ctx context.Context, req ChatRequest, dst any) error {
	req.JSONMode = true
	resp, err := c.Chat(ctx, req)
	if err != nil {
		return err
	}
	return json.Unmarshal([]byte(resp.Content), dst)
}

// ToolCall 表示 LLM 请求的工具调用。
type ToolCall struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

// MessageWithTools 表示带有可选工具调用的对话消息。
type MessageWithTools struct {
	Role             string            `json:"role"` // "user" | "assistant" | "tool"
	Content          Content           `json:"content,omitempty"`
	ToolCallID       string            `json:"tool_call_id,omitempty"`      // 用于 role="tool"
	ToolCalls        []ToolCall        `json:"tool_calls,omitempty"`        // 用于 role="assistant"
	Metadata         map[string]string `json:"metadata,omitempty"`          // 扩展元数据
	ReasoningContent string            `json:"reasoning_content,omitempty"` // 推理内容（reasoning 模型）
	IsError          bool              `json:"is_error,omitempty"`          // 错误标记（tool 消息）
	ToolName         string            `json:"tool_name,omitempty"`         // 工具名称（tool 消息）
	CreatedAt        string            `json:"created_at,omitempty"`        // 消息创建时间（RFC3339），贯穿 WS 广播和 DB 存储
}

// ChatWithToolsRequest 保存带工具调用的聊天补全参数。
type ChatWithToolsRequest struct {
	SystemPrompt    string
	Messages        []MessageWithTools
	Temperature     float64
	MaxTokens       int64
	Tools           []mcphost.ToolDefinition // 可用工具
	ReasoningEffort string                   // 单次请求覆盖的推理努力级别（可选，覆盖客户端默认值）
	UserID          string                   // 去标识化后参与 prompt_cache_key 分桶
	PromptVersions  []string                 // prompt/cache 版本标识，影响 prompt_cache_key
	// ToolChoice 控制工具调用策略，空字符串表示 auto（与旧行为一致）。
	// 合法值："" / "auto" / "required" / "none" / 具体工具名（强制指定单个工具）。
	// 见 docs/计划与路线/Agent-质量护栏治理计划.md P0-A。
	ToolChoice string
}

// ChatWithToolsResponse 保存包含潜在工具调用的结果。
type ChatWithToolsResponse struct {
	Content          string     `json:"content"`
	ReasoningContent string     `json:"reasoning_content,omitempty"` // <think>...</think> 推理内容
	ToolCalls        []ToolCall `json:"tool_calls"`
	FinishReason     string     `json:"finish_reason"`
	Usage            Usage      `json:"usage"`
}

// StreamChunk 流式响应的单个增量片段
type StreamChunk struct {
	ContentDelta     string     // 本次增量文本
	ContentSoFar     string     // 累积文本（前端需要完整内容替换显示）
	ReasoningContent string     // 累积推理内容
	ToolCalls        []ToolCall // 累积的工具调用；非 Done 时可能是部分参数，仅用于诊断/预览
	FinishReason     string     // 仅最后一个 chunk 有值
	Usage            Usage      // 仅最后一个 chunk 有值
	Done             bool       // 是否为最终 chunk
}

// StreamCallback 每个流式 chunk 的回调，返回 error 可提前终止流
type StreamCallback func(chunk StreamChunk) error

// ChatWithTools 发送支持工具调用的聊天补全请求。
func (c *Client) ChatWithTools(ctx context.Context, req ChatWithToolsRequest) (*ChatWithToolsResponse, error) {
	// Responses API 分发
	if c.useResponsesAPI() {
		return c.chatWithToolsViaResponses(ctx, req)
	}

	// 获取当前 model 快照（线程安全）
	snapModel, _, snapProvider := c.snapshot()

	// 检查模型是否支持 tool use，不支持时记录警告日志
	if meta := GetModelMeta(snapModel); meta != nil && !meta.Capabilities.ToolUse {
		c.logger.Warn("当前模型不支持工具调用，请求可能失败",
			zap.String("model", snapModel),
		)
	}

	// 验证多模态内容是否被 provider 支持
	for _, msg := range req.Messages {
		if msg.Role == "user" {
			if err := c.validateContent(msg.Content, snapProvider); err != nil {
				return nil, err
			}
		}
	}

	// 0. Provider 感知的消息转换（处理不同 Provider 的差异）
	transformedMessages := req.Messages
	if c.transformer != nil {
		providerName := snapProvider.Name
		if providerName == "" {
			providerName = DetectProvider(c.baseURL)
		}
		transformedMessages = c.transformer.Transform(transformedMessages, providerName)
	}

	// 1. 转换 mcphost.ToolDefinition → openai.ChatCompletionTool
	aliases := toolNameAliasesForTools(req.Tools)
	tools, err := convertToolsForChatCompletions(req.Tools)
	if err != nil {
		return nil, err
	}

	// 2. 转换 Messages → openai messages
	var messages []openai.ChatCompletionMessageParamUnion
	if req.SystemPrompt != "" {
		messages = append(messages, openai.SystemMessage(req.SystemPrompt))
	}

	for _, msg := range transformedMessages {
		switch msg.Role {
		case "system":
			messages = append(messages, openai.SystemMessage(msg.Content.Text()))
		case "user":
			messages = append(messages, toSDKUserMessage(msg.Content))
		case "assistant":
			if len(msg.ToolCalls) > 0 {
				// 带工具调用的 Assistant
				var toolCalls []openai.ChatCompletionMessageToolCallParam
				for _, tc := range msg.ToolCalls {
					tcID := tc.ID
					if len(tcID) > MaxToolCallIDLength {
						tcID = shortenToolCallID(tcID)
						c.logger.Info("截断超长 tool_call_id (assistant)",
							zap.Int("original_length", len(tc.ID)),
							zap.String("shortened", tcID),
							zap.String("tool_name", tc.Name),
						)
					}
					toolCalls = append(toolCalls, openai.ChatCompletionMessageToolCallParam{
						ID: tcID,
						Function: openai.ChatCompletionMessageToolCallFunctionParam{
							Name:      aliases.APIName(tc.Name),
							Arguments: string(tc.Arguments),
						},
						// Type 字段可以省略 - 默认为 "function"
					})
				}

				assistantMsg := openai.ChatCompletionAssistantMessageParam{
					ToolCalls: toolCalls,
				}
				// 仅在非空时设置 content
				if text := msg.Content.Text(); text != "" {
					assistantMsg.Content = openai.ChatCompletionAssistantMessageParamContentUnion{
						OfString: openai.String(text),
					}
				}
				messages = append(messages, openai.ChatCompletionMessageParamUnion{
					OfAssistant: &assistantMsg,
				})
			} else {
				messages = append(messages, openai.AssistantMessage(msg.Content.Text()))
			}
		case "tool":
			toolCallID := msg.ToolCallID
			if len(toolCallID) > MaxToolCallIDLength {
				toolCallID = shortenToolCallID(toolCallID)
				c.logger.Info("截断超长 tool_call_id (tool)",
					zap.Int("original_length", len(msg.ToolCallID)),
					zap.String("shortened", toolCallID),
				)
			}
			content := msg.Content.Text()
			if msg.IsError {
				content = "[TOOL_ERROR] " + content
			}
			messages = append(messages, openai.ToolMessage(content, toolCallID))
		}
	}

	// 3. 调用 OpenAI
	params := openai.ChatCompletionNewParams{
		Model:    snapModel,
		Messages: messages,
	}

	if len(tools) > 0 {
		params.Tools = tools
	}
	// P0-A：ToolChoice 透传（空字符串时跳过，保持旧 auto 行为）
	if tc, ok := buildChatCompletionsToolChoiceWithAliases(req.ToolChoice, aliases); ok {
		params.ToolChoice = tc
	}
	if req.Temperature > 0 {
		params.Temperature = openai.Float(req.Temperature)
	}
	if req.MaxTokens > 0 {
		params.MaxTokens = openai.Int(req.MaxTokens)
	}

	c.logger.Debug("发送带工具的聊天补全请求",
		zap.String("model", snapModel),
		zap.Int("message_count", len(messages)),
		zap.Int("tool_count", len(tools)),
		zap.String("tool_choice", req.ToolChoice),
	)

	// 3.5 请求级转换（缓存控制、推理参数等）
	providerName := snapProvider.Name
	if providerName == "" {
		providerName = DetectProvider(c.baseURL)
	}
	if c.requestTransformer != nil {
		c.requestTransformer.TransformRequest(&RequestTransformContext{
			Provider: providerName,
			Model:    snapModel,
			Messages: messages,
			Params:   &params,
		})
		// 回写可能被修改的 messages
		params.Messages = messages
	}

	// 3.6 单次请求推理努力级别覆盖
	if req.ReasoningEffort != "" {
		overrideTransformer := NewReasoningVariantsTransformer(req.ReasoningEffort, c.logger)
		overrideTransformer.TransformRequest(&RequestTransformContext{
			Provider: providerName,
			Model:    snapModel,
			Messages: messages,
			Params:   &params,
		})
		c.logger.Debug("单次请求推理努力级别覆盖",
			zap.String("effort", req.ReasoningEffort),
		)
	}

	// 4. 发送请求（带指数退避重试）
	completion, err := retryableAPICall(ctx, c.logger, "chat_completion_with_tools", func() (*openai.ChatCompletion, error) {
		return c.client.Chat.Completions.New(ctx, params)
	})
	if err != nil {
		c.logAPIError(err, "chat_completion_with_tools")
		return nil, errs.Wrap(errs.CodePlanGenFailed, "聊天补全失败", err)
	}

	if len(completion.Choices) == 0 {
		return nil, errs.New(errs.CodePlanGenFailed, "补全响应中没有 choices")
	}

	// 4. 解析响应
	choice := completion.Choices[0]
	cleanedContent, reasoning := stripThinkTags(choice.Message.Content)
	result := &ChatWithToolsResponse{
		Content:          cleanedContent,
		ReasoningContent: reasoning,
		FinishReason:     string(choice.FinishReason),
		Usage: Usage{
			PromptTokens:     completion.Usage.PromptTokens,
			CompletionTokens: completion.Usage.CompletionTokens,
			TotalTokens:      completion.Usage.TotalTokens,
		},
	}

	// 解析 tool calls
	if len(choice.Message.ToolCalls) > 0 {
		for _, tc := range choice.Message.ToolCalls {
			if len(tc.ID) > MaxToolCallIDLength {
				c.logger.Warn("收到超长 tool_call_id，将在发送时截断",
					zap.Int("id_length", len(tc.ID)),
					zap.String("tool_name", aliases.InternalName(tc.Function.Name)),
					zap.String("id_preview", tc.ID[:64]+"..."),
				)
			}
			result.ToolCalls = append(result.ToolCalls, ToolCall{
				ID:        tc.ID,
				Name:      aliases.InternalName(tc.Function.Name),
				Arguments: json.RawMessage(tc.Function.Arguments),
			})
		}
	}

	c.logger.Debug("收到聊天补全响应",
		zap.Int64("prompt_tokens", result.Usage.PromptTokens),
		zap.Int64("completion_tokens", result.Usage.CompletionTokens),
		zap.Int("tool_calls", len(result.ToolCalls)),
	)

	return result, nil
}

// ChatWithToolsStream 发送支持工具调用的流式聊天补全请求。
// onChunk 回调在每个增量 chunk 到达时触发，可用于实时推送给前端。
// 最终返回完整的 ChatWithToolsResponse（与非流式接口一致）。
// 内置重试机制：对 429/500/502/503 等可重试错误进行指数退避重试。
func (c *Client) ChatWithToolsStream(ctx context.Context, req ChatWithToolsRequest, onChunk StreamCallback) (*ChatWithToolsResponse, error) {
	streamFn := c.chatWithToolsStreamViaCompletions
	if c.useResponsesAPI() {
		streamFn = c.chatWithToolsStreamViaResponses
	}

	return resilience.Do(ctx, llmRetryPolicy, c.logger, "chat_stream_with_tools", func() (*ChatWithToolsResponse, error) {
		return streamFn(ctx, req, onChunk)
	})
}

// GenerateWithTemperature 简单的文本生成，支持自定义温度参数
// 用于标题生成等需要稳定输出的场景
func (c *Client) GenerateWithTemperature(ctx context.Context, prompt string, temperature float64) (string, Usage, error) {
	snapModel, _, _ := c.snapshot()

	messages := []openai.ChatCompletionMessageParamUnion{
		openai.UserMessage(prompt),
	}

	params := openai.ChatCompletionNewParams{
		Model:       snapModel,
		Messages:    messages,
		Temperature: openai.Float(temperature),
	}

	c.logger.Debug("发送文本生成请求",
		zap.String("model", snapModel),
		zap.Float64("temperature", temperature),
	)

	completion, err := retryableAPICall(ctx, c.logger, "generate_with_temperature", func() (*openai.ChatCompletion, error) {
		return c.client.Chat.Completions.New(ctx, params)
	})
	if err != nil {
		c.logAPIError(err, "generate_with_temperature")
		return "", Usage{}, errs.Wrap(errs.CodePlanGenFailed, "文本生成失败", err)
	}

	if len(completion.Choices) == 0 {
		return "", Usage{}, errs.New(errs.CodePlanGenFailed, "生成响应中没有 choices")
	}

	usage := Usage{
		PromptTokens:     completion.Usage.PromptTokens,
		CompletionTokens: completion.Usage.CompletionTokens,
	}
	return completion.Choices[0].Message.Content, usage, nil
}

// stripThinkTags 从 LLM 输出中提取并去除 <think>...</think> 推理块。
// 部分推理模型（如 DeepSeek-R1、QwQ）会把推理过程直接嵌入 Content 字段。
// 返回清理后的内容（无推理块）和提取出的推理内容（可能为空）。
func stripThinkTags(content string) (cleaned string, reasoning string) {
	var reasoningParts []string
	var cleanedParts []string

	remaining := content
	for {
		start := strings.Index(remaining, "<think>")
		if start == -1 {
			cleanedParts = append(cleanedParts, remaining)
			break
		}
		cleanedParts = append(cleanedParts, remaining[:start])
		remaining = remaining[start+len("<think>"):]
		end := strings.Index(remaining, "</think>")
		if end == -1 {
			// 没有闭合标签，把剩余内容视为推理内容
			reasoningParts = append(reasoningParts, remaining)
			break
		}
		reasoningParts = append(reasoningParts, remaining[:end])
		remaining = remaining[end+len("</think>"):]
	}

	cleaned = strings.TrimSpace(strings.Join(cleanedParts, ""))
	reasoning = strings.TrimSpace(strings.Join(reasoningParts, "\n"))
	return
}

// Generate 简单的文本生成（使用默认温度）
func (c *Client) Generate(ctx context.Context, prompt string) (string, error) {
	content, _, err := c.GenerateWithTemperature(ctx, prompt, 1.0)
	return content, err
}
