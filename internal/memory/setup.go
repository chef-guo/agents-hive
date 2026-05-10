package memory

import (
	"context"

	"go.uber.org/zap"
)

// EmbeddingSetupConfig embedding 初始化配置
type EmbeddingSetupConfig struct {
	BaseURL        string // LLM API 基础 URL
	APIKey         string // LLM API 密钥
	EmbeddingModel string // embedding 模型名称（空则自动选择）
	Provider       string // LLM 提供商名称（用于选择默认模型）
}

// SetupEmbedding 统一配置 embedding 组件并返回 HybridSearcher
// 避免在 CLI 和 Server 中重复初始化逻辑。
//
// vecStore 为可插拔向量索引：
//   - nil: 自动创建 InMemoryVecStore（VecIndex），并通过 vecLoader 加载历史数据
//   - PgVectorStore: 由调用方预先创建
//
// vecLoader 仅在 vecStore 为 nil 时使用，用于从数据库加载已有的向量到内存索引：
//   - PG: func(ctx, v) { return v.LoadFromPool(ctx, pool) }
func SetupEmbedding(
	ctx context.Context,
	memStore MemoryStore,
	vecStore VectorStore,
	vecLoader func(ctx context.Context, vecIdx *VecIndex) (int, error),
	cfg EmbeddingSetupConfig,
	logger *zap.Logger,
) (*HybridSearcher, EmbeddingProvider, VectorStore, error) {
	embedder := NewOpenAIEmbedder(cfg.BaseURL, cfg.APIKey, cfg.EmbeddingModel, cfg.Provider, logger)

	// 如果未提供外部 VectorStore，创建内存实现并加载历史数据
	if vecStore == nil {
		vecIdx := NewVecIndex(0, logger)
		if vecLoader != nil {
			if n, err := vecLoader(ctx, vecIdx); err != nil {
				logger.Warn("加载向量索引失败", zap.Error(err))
			} else if n > 0 {
				logger.Info("从数据库加载向量索引", zap.Int("loaded", n))
			}
		}
		vecStore = vecIdx
	}

	// 设置 embedding 组件：写入记忆时自动生成向量
	memStore.SetEmbedding(embedder, vecStore)

	// 创建混合搜索引擎：融合关键词搜索 + 向量语义搜索
	hybrid := NewHybridSearcher(memStore, vecStore, embedder, logger)
	if metricAware, ok := memStore.(MetricAwareStore); ok {
		metricAware.SetMetricsConfig(MetricsConfig{VectorSpace: DefaultVectorSpaceName})
	}
	hybrid.SetMetricsConfig(MetricsConfig{VectorSpace: DefaultVectorSpaceName})

	indexSize, _ := vecStore.Count(ctx)
	logger.Info("向量搜索已启用",
		zap.String("model", cfg.EmbeddingModel),
		zap.String("provider", cfg.Provider),
		zap.Int("index_size", indexSize),
	)

	return hybrid, embedder, vecStore, nil
}
