package memory

import "context"

// MemoryStore 记忆存储统一接口
// 当前支持 PostgreSQL（tsvector）实现
type MemoryStore interface {
	// Save 保存新记忆，返回分配的 ID
	Save(ctx context.Context, record *MemoryRecord) (int64, error)

	// Get 按 ID 获取记忆（同时更新 accessed_at 和 access_count）
	Get(ctx context.Context, id int64) (*MemoryRecord, error)

	// BatchGet 按 ID 批量获取记忆（异步合批更新访问计数，不改变 Get 语义）
	BatchGet(ctx context.Context, ids []int64) ([]MemoryRecord, error)

	// Update 更新已有记忆的内容、标签和元数据
	Update(ctx context.Context, record *MemoryRecord) error

	// Delete 删除记忆
	Delete(ctx context.Context, id int64) error

	// Search 全文搜索记忆（FTS5 MATCH + BM25 排序）
	Search(ctx context.Context, opts SearchOptions) (*SearchResult, error)

	// List 列出记忆（支持按类型/标签/会话过滤，按访问时间排序）
	List(ctx context.Context, opts SearchOptions) (*SearchResult, error)

	// Stats 返回记忆统计信息
	Stats(ctx context.Context) (*MemoryStats, error)

	// SetEmbedding 设置向量搜索组件（可选，启用 embedding 后调用）
	SetEmbedding(embedder EmbeddingProvider, vecStore VectorStore)

	// Close 释放存储资源
	Close() error
}

// SearchEngine 搜索引擎抽象接口（Phase 2 扩展点）
// Phase 1: FTS5Searcher 实现
// Phase 2: EmbeddingSearcher / HybridSearcher 实现
type SearchEngine interface {
	// Search 执行搜索，返回按相关性排序的记忆 ID 和得分
	Search(ctx context.Context, query string, limit int, userID string) ([]ScoredID, error)
}

// MemoryExtractor 记忆提取器接口（用于 compaction agent 注入）
// 避免 subagent/compaction 包直接依赖 memory 包
type MemoryExtractor interface {
	// ExtractFromSummary 从压缩摘要中提取并保存记忆
	ExtractFromSummary(ctx context.Context, summaryText string, sessionID string, userID string, opts ...ExtractorOption) error
}
