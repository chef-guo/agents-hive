package llm

import (
	"fmt"
	"sync"

	"go.uber.org/zap"
)

// ClientPool 管理多个 LLM Client 实例，避免重复创建
// 线程安全，支持并发访问
type ClientPool struct {
	mu      sync.RWMutex
	clients map[string]*Client // key: buildCacheKey(cfg)
	logger  *zap.Logger
	maxSize int // 最大缓存数量，避免内存泄漏
}

// NewClientPool 创建一个新的 LLM Client 池
func NewClientPool(logger *zap.Logger) *ClientPool {
	return &ClientPool{
		clients: make(map[string]*Client),
		logger:  logger,
		maxSize: 10, // 默认最多缓存 10 个 client
	}
}

// Get 获取或创建 LLM Client
// 如果缓存中存在相同配置的 client，直接返回
// 否则创建新 client 并缓存
func (p *ClientPool) Get(cfg ClientConfig) *Client {
	key := buildCacheKey(cfg)

	// 快速路径：读锁检查
	p.mu.RLock()
	if client, ok := p.clients[key]; ok {
		p.mu.RUnlock()
		p.logger.Debug("LLM Client 缓存命中",
			zap.String("key", key),
			zap.String("provider", cfg.Provider.Name),
			zap.String("model", cfg.Model),
		)
		return client
	}
	p.mu.RUnlock()

	// 慢路径：写锁创建
	p.mu.Lock()
	defer p.mu.Unlock()

	// 再次检查（防止并发创建）
	if client, ok := p.clients[key]; ok {
		p.logger.Debug("LLM Client 缓存命中（慢路径）",
			zap.String("key", key),
		)
		return client
	}

	// 限制池大小，采用简化策略：拒绝缓存新 client
	if len(p.clients) >= p.maxSize {
		p.logger.Warn("LLM Client Pool 已满，创建临时 client",
			zap.Int("pool_size", len(p.clients)),
			zap.Int("max_size", p.maxSize),
			zap.String("provider", cfg.Provider.Name),
			zap.String("model", cfg.Model),
		)
		// 创建临时 client，不缓存
		return NewClient(cfg, p.logger)
	}

	// 创建新 client 并缓存
	client := NewClient(cfg, p.logger)
	p.clients[key] = client

	p.logger.Info("创建并缓存 LLM Client",
		zap.String("key", key),
		zap.String("provider", cfg.Provider.Name),
		zap.String("model", cfg.Model),
		zap.Int("pool_size", len(p.clients)),
	)

	return client
}

// Clear 清空所有缓存的 client
func (p *ClientPool) Clear() {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.clients = make(map[string]*Client)
	p.logger.Info("LLM Client Pool 已清空")
}

// Size 返回当前缓存的 client 数量
func (p *ClientPool) Size() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return len(p.clients)
}

// buildCacheKey 为配置生成唯一的缓存键
// key 格式: provider:model:baseURL:apiFormat:cache:tier:privacy
func buildCacheKey(cfg ClientConfig) string {
	provider := cfg.Provider.Name
	if provider == "" {
		provider = "unknown"
	}

	model := cfg.Model
	if model == "" {
		model = "default"
	}

	baseURL := cfg.BaseURL
	if baseURL == "" {
		baseURL = cfg.Provider.BaseURL
	}

	apiFormat := cfg.Provider.APIFormat
	if apiFormat == "" {
		apiFormat = "chat"
	}

	return fmt.Sprintf("%s:%s:%s:%s:cache=%t:tier=%s:privacy=%t", provider, model, baseURL, apiFormat, cfg.PromptCacheKey, cfg.ServiceTier, cfg.StorePrivacy)
}
