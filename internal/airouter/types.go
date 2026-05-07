package airouter

import "github.com/chef-guo/agents-hive/internal/llm"

// ServiceType AI 服务类型（可扩展，新增能力只需在此添加常量）
type ServiceType string

const (
	ServiceLLM       ServiceType = "llm"       // 文本生成（Chat / Responses API）
	ServiceImageGen  ServiceType = "image_gen" // 图片生成（DALL-E / 即梦等）
	ServiceVideoGen  ServiceType = "video_gen" // 视频生成（Sora / 即梦等）
	ServiceTTS       ServiceType = "tts"       // 文字转语音
	ServiceSTT       ServiceType = "stt"       // 语音转文字
	ServiceEmbedding ServiceType = "embedding" // 向量化
)

// LLMTaskType LLM 内部任务分类（用于智能选模型）
type LLMTaskType string

const (
	TaskChat       LLMTaskType = "chat"        // 主对话（用户选定的模型）
	TaskTitle      LLMTaskType = "title"       // 生成标题（最便宜的）
	TaskSummary    LLMTaskType = "summary"     // 摘要/压缩（便宜的）
	TaskCodeReview LLMTaskType = "code_review" // 代码审查（最强推理）
	TaskVision     LLMTaskType = "vision"      // 图片理解（需 Vision 能力）
	TaskAgent      LLMTaskType = "agent"       // 子代理（需 ToolUse 能力）
	TaskPlanning   LLMTaskType = "planning"    // spec-driven planner（结构化 JSON，受 token budget 限制）
)

// CostTier 模型成本层级
type CostTier int

const (
	TierCheap     CostTier = 1 // mini/small 级别（gpt-4o-mini, deepseek-chat 等）
	TierMedium    CostTier = 2 // 标准级别（gpt-4o, claude-3-sonnet 等）
	TierExpensive CostTier = 3 // 旗舰级别（o1, o3, claude-3-opus 等）
)

// ModelScore 模型评分记录（用于智能选择）
type ModelScore struct {
	Name         string   // DB 中的模型名称
	Model        string   // 发送到 API 的模型 ID
	Provider     string   // 提供商名称
	BaseURL      string   // API 端点
	APIKey       string   // API 密钥
	APIFormat    string   // "chat" 或 "responses"
	CostTier     CostTier // 成本层级
	Capabilities []string // 支持的能力: "vision", "tools", "reasoning", "json" 等

	// 来自 config_json 的额外配置
	ReasoningEffort string
	DisableJSONMode bool
	StorePrivacy    bool
}

// HasCapability 检查模型是否具有指定能力
func (m ModelScore) HasCapability(cap string) bool {
	for _, c := range m.Capabilities {
		if c == cap {
			return true
		}
	}
	return false
}

// HasAllCapabilities 检查模型是否具有所有指定能力
func (m ModelScore) HasAllCapabilities(caps ...string) bool {
	for _, cap := range caps {
		if !m.HasCapability(cap) {
			return false
		}
	}
	return true
}

// SupportsAutoReasoningEffort 判断自动 reasoning_effort 是否可安全启用。
func (m ModelScore) SupportsAutoReasoningEffort() bool {
	if !m.HasCapability("reasoning") {
		return false
	}
	meta := llm.GetModelMeta(m.Model)
	return meta != nil && meta.Capabilities.Reasoning && len(meta.Capabilities.ReasoningEfforts) > 0
}

// ProviderConfig 非 LLM 服务的 provider 配置
type ProviderConfig struct {
	Name         string
	ServiceType  ServiceType
	ProviderType string
	APIKey       string
	BaseURL      string
	Model        string // 来自 llm_models.model，优先于 ConfigJSON["model"]
	ConfigJSON   map[string]any
	Enabled      bool
}

// CapabilityInfo 描述一个可用的 AI 能力
type CapabilityInfo struct {
	Type      ServiceType `json:"type"`
	Models    []string    `json:"models,omitempty"`
	Providers []string    `json:"providers,omitempty"`
	Active    string      `json:"active,omitempty"`
	Available bool        `json:"available"`
}

// Usage AI 服务调用消耗
type Usage struct {
	InputTokens  int64 `json:"input_tokens,omitempty"`
	OutputTokens int64 `json:"output_tokens,omitempty"`
	TotalTokens  int64 `json:"total_tokens,omitempty"`
}
