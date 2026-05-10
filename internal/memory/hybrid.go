package memory

import (
	"context"
	"strings"
	"time"

	"go.uber.org/zap"
)

// 编译期接口合规检查
var _ SearchEngine = (*HybridSearcher)(nil)

// HybridSearcher 混合搜索引擎：融合关键词搜索和向量语义搜索
// 基于 PostgreSQL（tsvector）后端
// 使用 RRF（Reciprocal Rank Fusion）算法融合两路结果
type HybridSearcher struct {
	store       MemoryStore       // 关键词搜索（FTS5 或 tsvector）
	vec         VectorStore       // 向量语义搜索（可插拔：VecIndex 或 PgVectorStore）
	embed       EmbeddingProvider // Embedding 提供者
	logger      *zap.Logger
	metrics     MetricRecorder
	vectorSpace string
}

// NewHybridSearcher 创建混合搜索引擎
func NewHybridSearcher(store MemoryStore, vec VectorStore, embed EmbeddingProvider, logger *zap.Logger) *HybridSearcher {
	return &HybridSearcher{
		store:       store,
		vec:         vec,
		embed:       embed,
		logger:      logger,
		vectorSpace: DefaultVectorSpaceName,
	}
}

// rrfK RRF 融合参数，值越大越平滑
const rrfK = 60

// Search 执行混合搜索，返回按相关性排序的记忆 ID 和得分
func (h *HybridSearcher) Search(ctx context.Context, query string, limit int, userID string) ([]ScoredID, error) {
	searchStart := time.Now()
	if limit <= 0 {
		limit = 10
	}

	// 两路搜索候选数量（取更多候选以保证融合质量）
	candidateLimit := limit * 3

	// 第一路：FTS5 关键词搜索
	ftsStart := time.Now()
	ftsResults, ftsErr := h.store.Search(ctx, SearchOptions{
		Query:  query,
		Limit:  candidateLimit,
		UserID: userID,
	})
	ftsDuration := time.Since(ftsStart)
	if ftsErr != nil {
		recordMetric(ctx, h.metrics, MetricHybridSearchFallbackTotal, 1, map[string]any{"reason": HybridFallbackReasonFTSError})
		h.logger.Debug("FTS5 搜索失败", zap.Error(ftsErr), zap.Duration("duration", ftsDuration))
	}

	// 第二路：向量语义搜索
	var vecResults []VecSearchResult
	var vecDuration time.Duration
	if h.vec != nil && h.embed != nil {
		vecStart := time.Now()
		queryVec, err := h.embed.Embed(ctx, []string{query})
		if err != nil {
			recordMetric(ctx, h.metrics, MetricHybridSearchFallbackTotal, 1, map[string]any{"reason": HybridFallbackReasonEmbedError})
			h.logger.Debug("查询向量化失败", zap.Error(err))
		} else if len(queryVec) > 0 && len(queryVec[0]) > 0 {
			var vecErr error
			vecResults, vecErr = h.vec.Search(ctx, queryVec[0], candidateLimit, userID)
			if vecErr != nil {
				// 降级为纯 FTS，但不静默吞错误
				recordMetric(ctx, h.metrics, MetricHybridSearchFallbackTotal, 1, map[string]any{"reason": HybridFallbackReasonVectorError})
				if isVectorSpaceMismatchError(vecErr) {
					recordMetric(ctx, h.metrics, MetricVectorSpaceMismatchTotal, 1, map[string]any{"operation": "search"})
					h.logger.Warn("memory vector-space mismatch",
						zap.String("expected_space", metricVectorSpace(h.vectorSpace)),
						zap.Int("query_dim", len(queryVec[0])),
						zap.Error(vecErr),
					)
				}
				h.logger.Warn("向量搜索失败，降级为纯 FTS", zap.Error(vecErr))
			}
		} else {
			recordMetric(ctx, h.metrics, MetricHybridSearchFallbackTotal, 1, map[string]any{"reason": HybridFallbackReasonEmptyVector})
		}
		vecDuration = time.Since(vecStart)
	} else {
		recordMetric(ctx, h.metrics, MetricHybridSearchFallbackTotal, 1, map[string]any{"reason": HybridFallbackReasonUnavailable})
	}

	// 融合结果
	results := h.fuse(ftsResults, vecResults, limit)

	h.logger.Info("混合搜索完成",
		zap.Duration("total", time.Since(searchStart)),
		zap.Duration("fts", ftsDuration),
		zap.Duration("vec", vecDuration),
		zap.Int("results", len(results)),
		zap.Int("query_len", len(query)),
	)

	return results, nil
}

func isVectorSpaceMismatchError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "维度不匹配") || strings.Contains(msg, "dimension mismatch")
}

// fuse 使用 RRF 算法融合两路搜索结果
func (h *HybridSearcher) fuse(ftsResult *SearchResult, vecResults []VecSearchResult, limit int) []ScoredID {
	scores := make(map[int64]float64)

	// FTS5 结果贡献 RRF 分数
	if ftsResult != nil {
		for rank, mem := range ftsResult.Memories {
			scores[mem.ID] += 1.0 / float64(rrfK+rank+1)
		}
	}

	// 向量搜索结果贡献 RRF 分数
	for rank, vr := range vecResults {
		// 只取余弦相似度 > 0 的结果
		if vr.Score > 0 {
			scores[vr.ID] += 1.0 / float64(rrfK+rank+1)
		}
	}

	if len(scores) == 0 {
		return nil
	}

	// RRF 原始分值很小（约 0.016 起），在返回给注入器前归一化到 0..1，
	// 让 SearchOptions.MinScore 和普通 store search 使用一致的“越大越相关”语义。
	maxScore := 0.0
	for _, score := range scores {
		if score > maxScore {
			maxScore = score
		}
	}

	// 转换为排序切片
	results := make([]ScoredID, 0, len(scores))
	for id, score := range scores {
		if maxScore > 0 {
			score = score / maxScore
		}
		results = append(results, ScoredID{ID: id, Score: score})
	}

	// 按 RRF 分数降序排序
	sortScoredIDs(results)

	if len(results) > limit {
		results = results[:limit]
	}

	return results
}

// sortScoredIDs 按 Score 降序排序
func sortScoredIDs(ids []ScoredID) {
	for i := 1; i < len(ids); i++ {
		for j := i; j > 0 && ids[j].Score > ids[j-1].Score; j-- {
			ids[j], ids[j-1] = ids[j-1], ids[j]
		}
	}
}
