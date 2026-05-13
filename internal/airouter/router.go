package airouter

import (
	"context"
	"encoding/json"
	"sync"

	"go.uber.org/zap"

	"github.com/chef-guo/agents-hive/internal/llm"
	"github.com/chef-guo/agents-hive/internal/store"
)

// ServiceAdapter 服务适配器接口 — 每种 AI 能力实现一个
type ServiceAdapter interface {
	ServiceType() ServiceType
	Execute(ctx context.Context, req ServiceRequest) (*ServiceResponse, error)
}

// ServiceRequest 通用服务请求
type ServiceRequest struct {
	Type   ServiceType
	Params map[string]any
}

// ServiceResponse 通用服务响应
type ServiceResponse struct {
	Content  string         `json:"content,omitempty"`
	Data     []byte         `json:"data,omitempty"`
	MimeType string         `json:"mime_type,omitempty"`
	URL      string         `json:"url,omitempty"`
	Metadata map[string]any `json:"metadata,omitempty"`
	Usage    Usage          `json:"usage,omitempty"`
}

// RouterConfig 路由器初始化配置
type RouterConfig struct {
	Store  store.Store
	Logger *zap.Logger

	// 初始 LLM 配置（从 bootstrap 传入）
	DefaultModel     string
	DefaultProvider  string
	DefaultBaseURL   string
	DefaultAPIKey    string
	DefaultAPIFormat string
	DisableJSONMode  bool
	ReasoningEffort  string
	StorePrivacy     bool
	PromptCacheKey   bool
	ServiceTier      string
}

// Router AI 服务路由器 — 所有 AI 能力的单一入口
type Router struct {
	mu        sync.RWMutex
	adapters  map[ServiceType]ServiceAdapter
	llmPool   *llm.ClientPool
	models    []ModelScore     // 所有可用 LLM 模型（含评分）
	providers []ProviderConfig // 所有非 LLM provider 配置
	userModel string           // 用户选定的主对话模型名称

	// 默认配置（用于没有 DB 时的 fallback）
	defaultCfg RouterConfig

	store  store.Store
	logger *zap.Logger
}

// NewRouter 创建 AI 服务路由器
func NewRouter(cfg RouterConfig) *Router {
	r := &Router{
		adapters:   make(map[ServiceType]ServiceAdapter),
		llmPool:    llm.NewClientPool(cfg.Logger),
		userModel:  "",
		defaultCfg: cfg,
		store:      cfg.Store,
		logger:     cfg.Logger,
	}

	// 从 DB 加载模型和 provider 配置
	if cfg.Store != nil {
		if err := r.Reload(context.Background()); err != nil {
			cfg.Logger.Warn("初始加载 AI 服务配置失败，使用默认配置", zap.Error(err))
		}
	}

	// 仅在无 DB 场景下使用默认配置构建单个模型（有 DB 时配置完全由 DB 管理）
	if len(r.models) == 0 && cfg.Store == nil && cfg.DefaultAPIKey != "" {
		r.models = []ModelScore{{
			Name:            "default",
			Model:           cfg.DefaultModel,
			Provider:        cfg.DefaultProvider,
			BaseURL:         cfg.DefaultBaseURL,
			APIKey:          cfg.DefaultAPIKey,
			APIFormat:       cfg.DefaultAPIFormat,
			CostTier:        inferCostTier(cfg.DefaultModel),
			Capabilities:    inferCapabilities(cfg.DefaultModel, nil),
			ReasoningEffort: cfg.ReasoningEffort,
			DisableJSONMode: cfg.DisableJSONMode,
			StorePrivacy:    cfg.StorePrivacy,
			PromptCacheKey:  cfg.PromptCacheKey,
			ServiceTier:     cfg.ServiceTier,
		}}
		r.userModel = "default"
	}

	return r
}

// RegisterAdapter 注册服务适配器
func (r *Router) RegisterAdapter(a ServiceAdapter) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.adapters[a.ServiceType()] = a
	r.logger.Info("注册 AI 服务适配器", zap.String("type", string(a.ServiceType())))
}

// Execute 执行非 LLM 服务调用（图片生成、TTS 等）
func (r *Router) Execute(ctx context.Context, req ServiceRequest) (*ServiceResponse, error) {
	r.mu.RLock()
	adapter, ok := r.adapters[req.Type]
	r.mu.RUnlock()

	if !ok {
		return nil, &ErrNoAdapter{ServiceType: req.Type}
	}

	return adapter.Execute(ctx, req)
}

// GetLLMClient 根据任务类型获取最优 LLM 客户端
func (r *Router) GetLLMClient(task LLMTaskType) *llm.Client {
	model := r.selectBestModel(task)
	if model == nil {
		r.logger.Error("无可用 LLM 模型", zap.String("task", string(task)))
		return nil
	}

	provDef := llm.LookupProvider(model.Provider)
	provDef.APIFormat = model.APIFormat

	cfg := llm.ClientConfig{
		APIKey:          model.APIKey,
		BaseURL:         model.BaseURL,
		Model:           model.Model,
		Provider:        provDef,
		DisableJSONMode: model.DisableJSONMode,
		ReasoningEffort: model.ReasoningEffort,
		StorePrivacy:    model.StorePrivacy,
		PromptCacheKey:  model.PromptCacheKey,
		ServiceTier:     model.ServiceTier,
	}

	client := r.llmPool.Get(cfg)

	maskedKey := ""
	if len(model.APIKey) >= 8 {
		maskedKey = model.APIKey[:4] + "****" + model.APIKey[len(model.APIKey)-4:]
	} else if model.APIKey != "" {
		maskedKey = "****"
	}
	r.logger.Info("LLM 客户端路由",
		zap.String("task", string(task)),
		zap.String("model_name", model.Name),
		zap.String("model", model.Model),
		zap.String("provider", model.Provider),
		zap.String("base_url", model.BaseURL),
		zap.String("api_key", maskedKey),
		zap.String("api_format", model.APIFormat),
		zap.Int("cost_tier", int(model.CostTier)),
	)

	return client
}

// GetUserLLMClient 获取用户选定的主对话 LLM 客户端（等价于 GetLLMClient(TaskChat)）
func (r *Router) GetUserLLMClient() *llm.Client {
	return r.GetLLMClient(TaskChat)
}

func (r *Router) SupportsAutoReasoningEffort(task LLMTaskType) bool {
	model := r.selectBestModel(task)
	return model != nil && model.SupportsAutoReasoningEffort()
}

// SwitchUserModel 切换用户选定的主对话模型（由前端触发）
func (r *Router) SwitchUserModel(modelName, model, baseURL, provider, apiFormat string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.userModel = modelName

	// 更新模型列表中对应模型的配置
	for i := range r.models {
		if r.models[i].Name == modelName {
			if model != "" {
				r.models[i].Model = model
			}
			if baseURL != "" {
				r.models[i].BaseURL = baseURL
			}
			if provider != "" {
				r.models[i].Provider = provider
			}
			if apiFormat != "" {
				r.models[i].APIFormat = apiFormat
			}
			break
		}
	}

	r.logger.Info("用户切换主模型",
		zap.String("name", modelName),
		zap.String("model", model),
		zap.String("provider", provider),
	)
}

// Reload 从 DB 重新加载所有 AI 服务配置
func (r *Router) Reload(ctx context.Context) error {
	if r.store == nil {
		return nil
	}

	// 1. 加载所有 provider
	providers, err := r.store.ListLLMProviders(ctx)
	if err != nil {
		return err
	}

	// 2. 加载所有模型
	models, err := r.store.ListLLMModels(ctx)
	if err != nil {
		return err
	}

	// 3. 构建 provider 索引（按 name）
	providerMap := make(map[string]*store.LLMProviderRecord, len(providers))
	for _, p := range providers {
		if p.Enabled {
			providerMap[p.Name] = p
		}
	}

	// 4. 构建模型评分列表（LLM）和非 LLM provider 配置
	// model 的有效 service_type = model.ServiceType（若非空）> provider.ServiceType > "llm"
	var scoredModels []ModelScore
	var userModel string

	// 非 LLM provider 配置：先从 llm_providers 建基础配置，再由 llm_models 填充 Model 字段
	nonLLMMap := make(map[string]*ProviderConfig) // key: provider name
	for _, p := range providers {
		if !p.Enabled {
			continue
		}
		st := ServiceType(p.ServiceType)
		if st == "" || st == ServiceLLM {
			continue
		}
		var cfgMap map[string]any
		if p.ConfigJSON != "" {
			_ = json.Unmarshal([]byte(p.ConfigJSON), &cfgMap)
		}
		nonLLMMap[p.Name] = &ProviderConfig{
			Name:         p.Name,
			ServiceType:  st,
			ProviderType: p.ProviderType,
			APIKey:       p.APIKey,
			BaseURL:      p.BaseURL,
			ConfigJSON:   cfgMap,
			Enabled:      p.Enabled,
		}
	}

	for _, m := range models {
		if !m.Enabled {
			continue
		}

		p := providerMap[m.ProviderName]
		if p == nil {
			continue // provider 不存在或未启用
		}

		// 确定此 model 的有效 service_type
		effectiveST := m.ServiceType
		if effectiveST == "" {
			effectiveST = p.ServiceType
		}
		if effectiveST == "" {
			effectiveST = string(ServiceLLM)
		}

		// 确定 API key 和 base URL（model 级覆盖 > provider 级）
		apiKey := p.APIKey
		if m.APIKey != "" {
			apiKey = m.APIKey
		}
		baseURL := p.BaseURL
		if m.BaseURL != "" {
			baseURL = m.BaseURL
		}

		if effectiveST != string(ServiceLLM) {
			// 非 LLM model：填充到对应 provider 的 ProviderConfig.Model（取第一个 enabled 的）
			if pc, ok := nonLLMMap[m.ProviderName]; ok && pc.Model == "" {
				pc.Model = m.Model
				// model 级覆盖 api_key / base_url
				if m.APIKey != "" {
					pc.APIKey = apiKey
				}
				if m.BaseURL != "" {
					pc.BaseURL = baseURL
				}
			}
			continue
		}

		// LLM model：走原有评分路由
		provDef := llm.LookupProvider(p.ProviderType)
		provCaps := make(map[string]bool)
		if provDef.HasCapability(llm.CapVision) {
			provCaps["vision"] = true
		}
		if provDef.HasCapability(llm.CapTools) {
			provCaps["tools"] = true
		}
		if provDef.HasCapability(llm.CapJSONMode) {
			provCaps["json"] = true
		}

		// 解析 model config_json
		var reasoningEffort string
		var disableJSONMode bool
		var storePrivacy bool
		promptCacheKey := r.defaultCfg.PromptCacheKey
		promptCacheKeySet := false
		serviceTier := r.defaultCfg.ServiceTier
		serviceTierSet := false
		var costTierOverride CostTier

		if m.ConfigJSON != "" {
			var cfg map[string]any
			if err := json.Unmarshal([]byte(m.ConfigJSON), &cfg); err == nil {
				if v, ok := cfg["reasoning_effort"].(string); ok {
					reasoningEffort = v
				}
				if v, ok := cfg["disable_json_mode"].(bool); ok {
					disableJSONMode = v
				}
				if v, ok := cfg["store_privacy"].(bool); ok {
					storePrivacy = v
				}
				if v, ok := cfg["prompt_cache_key_enabled"].(bool); ok {
					promptCacheKey = v
					promptCacheKeySet = true
				}
				if v, ok := cfg["interactive_service_tier"].(string); ok {
					serviceTier = v
					serviceTierSet = true
				}
				if v, ok := cfg["cost_tier"].(float64); ok {
					costTierOverride = CostTier(int(v))
				}
			}
		}

		// provider 级 config_json 作为 fallback
		if p.ConfigJSON != "" {
			var pcfg map[string]any
			if err := json.Unmarshal([]byte(p.ConfigJSON), &pcfg); err == nil {
				if v, ok := pcfg["reasoning_effort"].(string); ok && reasoningEffort == "" {
					reasoningEffort = v
				}
				if v, ok := pcfg["disable_json_mode"].(bool); ok && !disableJSONMode {
					disableJSONMode = v
				}
				if v, ok := pcfg["store_privacy"].(bool); ok && !storePrivacy {
					storePrivacy = v
				}
				if v, ok := pcfg["prompt_cache_key_enabled"].(bool); ok && !promptCacheKeySet {
					promptCacheKey = v
				}
				if v, ok := pcfg["interactive_service_tier"].(string); ok && !serviceTierSet {
					serviceTier = v
				}
			}
		}

		costTier := inferCostTier(m.Model)
		if costTierOverride > 0 {
			costTier = costTierOverride
		}

		score := ModelScore{
			Name:            m.Name,
			Model:           m.Model,
			Provider:        p.ProviderType,
			BaseURL:         baseURL,
			APIKey:          apiKey,
			APIFormat:       p.APIFormat,
			CostTier:        costTier,
			Capabilities:    inferCapabilities(m.Model, provCaps),
			ReasoningEffort: reasoningEffort,
			DisableJSONMode: disableJSONMode,
			StorePrivacy:    storePrivacy,
			PromptCacheKey:  promptCacheKey,
			ServiceTier:     serviceTier,
		}

		scoredModels = append(scoredModels, score)

		if m.IsDefault {
			userModel = m.Name
		}
	}

	// 5. 收集非 LLM provider 配置列表
	var nonLLMProviders []ProviderConfig
	for _, pc := range nonLLMMap {
		nonLLMProviders = append(nonLLMProviders, *pc)
	}

	// （已删除原来的非 LLM provider 单独构建逻辑，统一由上方 llm_models 填充）
	// 6. 更新路由器状态
	r.mu.Lock()
	r.models = scoredModels
	r.providers = nonLLMProviders
	if userModel != "" {
		r.userModel = userModel
	}
	r.mu.Unlock()

	r.logger.Info("AI 服务配置已重载",
		zap.Int("llm_models", len(scoredModels)),
		zap.Int("non_llm_providers", len(nonLLMProviders)),
		zap.String("user_model", userModel),
	)

	return nil
}

// GetProviders 获取指定服务类型的 provider 配置
func (r *Router) GetProviders(st ServiceType) []ProviderConfig {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var result []ProviderConfig
	for _, p := range r.providers {
		if p.ServiceType == st {
			result = append(result, p)
		}
	}
	return result
}

// ActiveModel 返回当前活跃的模型 ID（用于日志/API 返回）
func (r *Router) ActiveModel() string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	m := r.userSelectedModel()
	if m != nil {
		return m.Model
	}
	return ""
}

// ActiveModelName 返回当前活跃的模型名称
func (r *Router) ActiveModelName() string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.userModel
}

// ListCapabilities 返回当前可用的 AI 能力列表
func (r *Router) ListCapabilities() []CapabilityInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()

	caps := make([]CapabilityInfo, 0)

	// LLM capability (always present if models exist)
	if len(r.models) > 0 {
		modelNames := make([]string, 0, len(r.models))
		for _, m := range r.models {
			modelNames = append(modelNames, m.Name)
		}
		caps = append(caps, CapabilityInfo{
			Type:      ServiceLLM,
			Models:    modelNames,
			Active:    r.userModel,
			Available: true,
		})
	}

	// Non-LLM capabilities from registered adapters
	for st := range r.adapters {
		if st == ServiceLLM {
			continue
		}
		providerNames := make([]string, 0)
		for _, p := range r.providers {
			if p.ServiceType == st {
				providerNames = append(providerNames, p.Name)
			}
		}
		caps = append(caps, CapabilityInfo{
			Type:      st,
			Providers: providerNames,
			Available: len(providerNames) > 0,
		})
	}

	return caps
}

// ErrNoAdapter 无可用适配器错误
type ErrNoAdapter struct {
	ServiceType ServiceType
}

func (e *ErrNoAdapter) Error() string {
	return "no adapter registered for service type: " + string(e.ServiceType)
}
