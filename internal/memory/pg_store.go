package memory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"

	"github.com/chef-guo/agents-hive/internal/auth"
	"github.com/chef-guo/agents-hive/internal/errs"
)

// 编译期接口合规检查
var _ MemoryStore = (*PostgresMemoryStore)(nil)
var _ GovernanceStore = (*PostgresMemoryStore)(nil)
var _ MemoryEmbeddingSyncer = (*PostgresMemoryStore)(nil)

// PostgresMemoryStore PostgreSQL 记忆存储实现
// 使用 tsvector + GIN 索引实现全文搜索，共享主 PostgresStore 的连接池
type PostgresMemoryStore struct {
	pool        *pgxpool.Pool
	logger      *zap.Logger
	vecStore    VectorStore
	embedder    EmbeddingProvider
	scopePolicy ScopePolicy
	metrics     MetricRecorder
	vectorSpace string
	// embedding goroutine 并发控制
	embeddingSem chan struct{}
	embeddingWg  sync.WaitGroup
}

// NewPostgresMemoryStore 创建 PostgreSQL 记忆存储
func NewPostgresMemoryStore(pool *pgxpool.Pool, logger *zap.Logger) (*PostgresMemoryStore, error) {
	if pool == nil {
		return nil, errs.New(errs.CodeInvalidArgument, "连接池不能为空")
	}
	return &PostgresMemoryStore{
		pool:         pool,
		logger:       logger,
		scopePolicy:  DefaultScopePolicy{},
		vectorSpace:  metricVectorSpace(""),
		embeddingSem: make(chan struct{}, 5),
	}, nil
}

// Save 保存新记忆，返回分配的 ID
func (s *PostgresMemoryStore) Save(ctx context.Context, record *MemoryRecord) (int64, error) {
	if record == nil {
		return 0, errs.New(errs.CodeInvalidInput, "记忆记录不能为空")
	}
	if record.Content == "" {
		return 0, errs.New(errs.CodeInvalidInput, "记忆内容不能为空")
	}
	if err := NormalizeMemoryRecord(record, RuntimeContextFromContext(ctx)); err != nil {
		return 0, errs.New(errs.CodeInvalidInput, err.Error())
	}

	tagsJSON, err := json.Marshal(record.Tags)
	if err != nil {
		tagsJSON = []byte("[]")
	}

	metadataJSON := record.Metadata
	if metadataJSON == nil {
		metadataJSON = json.RawMessage("{}")
	}
	columns := memoryFirstClassColumnValues(record.Metadata, record.Type, record.UserID, record.SessionID)

	var id int64
	err = s.pool.QueryRow(ctx,
		`INSERT INTO memories (type, content, tags, session_id, metadata, user_id, target_scope, target_id, visibility, memory_kind, subject_type)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		 RETURNING id`,
		string(record.Type), record.Content, string(tagsJSON), record.SessionID, string(metadataJSON), record.UserID,
		columns.TargetScope, columns.TargetID, columns.Visibility, columns.MemoryKind, columns.SubjectType,
	).Scan(&id)
	if isUndefinedMemoryFirstClassColumnError(err) {
		err = s.pool.QueryRow(ctx,
			`INSERT INTO memories (type, content, tags, session_id, metadata, user_id)
			 VALUES ($1, $2, $3, $4, $5, $6)
			 RETURNING id`,
			string(record.Type), record.Content, string(tagsJSON), record.SessionID, string(metadataJSON), record.UserID,
		).Scan(&id)
	}
	if err != nil {
		return 0, errs.Wrap(errs.CodeMemoryWriteFailed, "保存记忆失败", err)
	}

	s.logger.Debug("保存记忆成功", zap.Int64("id", id), zap.String("type", string(record.Type)))

	// 异步生成 embedding（不阻塞主流程，使用独立 ctx，不受 caller ctx 取消影响）
	if s.embedder != nil && s.vecStore != nil {
		rctx := RuntimeContextFromContext(ctx)
		s.embeddingWg.Add(1)
		go func() {
			defer s.embeddingWg.Done()
			// 用信号量限制并发数
			timer := time.NewTimer(5 * time.Minute)
			select {
			case s.embeddingSem <- struct{}{}:
				timer.Stop()
				defer func() { <-s.embeddingSem }()
			case <-timer.C:
				// 兜底：信号量满时最多等 5 分钟，避免 goroutine 泄漏
				recordEmbeddingDropped(context.Background(), s.metrics, EmbeddingDroppedReasonSemaphoreTimeout)
				return
			}
			embedCtx := WithRuntimeContext(context.Background(), rctx)
			s.generateEmbedding(embedCtx, id, record.Content)
		}()
	}

	return id, nil
}

// Get 按 ID 获取记忆（同时更新 accessed_at 和 access_count）
func (s *PostgresMemoryStore) Get(ctx context.Context, id int64) (*MemoryRecord, error) {
	userID := auth.UserIDFrom(ctx)
	var row pgx.Row
	if userID != "" {
		row = s.pool.QueryRow(ctx,
			`UPDATE memories
			 SET accessed_at = NOW(), access_count = access_count + 1
			 WHERE id = $1 AND user_id = $2
			 RETURNING id, user_id, type, content, tags, session_id, metadata,
			           created_at, updated_at, accessed_at, access_count`, id, userID)
	} else {
		row = s.pool.QueryRow(ctx,
			`UPDATE memories
			 SET accessed_at = NOW(), access_count = access_count + 1
			 WHERE id = $1
			 RETURNING id, user_id, type, content, tags, session_id, metadata,
			           created_at, updated_at, accessed_at, access_count`, id)
	}

	record, err := scanPgMemoryRecord(row)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, errs.New(errs.CodeMemoryNotFound, fmt.Sprintf("记忆 %d 未找到", id))
		}
		return nil, errs.Wrap(errs.CodeMemoryReadFailed, "读取记忆失败", err)
	}
	return record, nil
}

// BatchGet 按 ID 批量获取记忆，并异步合批更新 accessed_at/access_count。
func (s *PostgresMemoryStore) BatchGet(ctx context.Context, ids []int64) ([]MemoryRecord, error) {
	ids = uniquePositiveIDs(ids)
	if len(ids) == 0 {
		return []MemoryRecord{}, nil
	}

	query := `SELECT id, user_id, type, content, tags, session_id, metadata,
		created_at, updated_at, accessed_at, access_count
	FROM memories WHERE id = ANY($1)`
	args := []any{ids}
	argIdx := 2

	userID := auth.UserIDFrom(ctx)
	if userID != "" {
		query += fmt.Sprintf(" AND user_id = $%d", argIdx)
		args = append(args, userID)
		argIdx++
	}

	query, args, argIdx = s.applyScopeFilter(ctx, query, args, argIdx)
	_ = argIdx

	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, errs.Wrap(errs.CodeMemoryReadFailed, "批量读取记忆失败", err)
	}
	defer rows.Close()

	memories, err := scanPgSearchRows(rows, false)
	if err != nil {
		return nil, err
	}
	if len(memories) > 0 {
		s.bumpAccessAsync(memoryRecordIDs(memories))
	}
	return memories, nil
}

// Update 更新已有记忆的内容、标签和元数据
func (s *PostgresMemoryStore) Update(ctx context.Context, record *MemoryRecord) error {
	if record == nil || record.ID == 0 {
		return errs.New(errs.CodeInvalidInput, "记忆记录或 ID 无效")
	}
	if record.Content == "" {
		return errs.New(errs.CodeInvalidInput, "记忆内容不能为空")
	}
	if err := NormalizeMemoryRecord(record, RuntimeContextFromContext(ctx)); err != nil {
		return errs.New(errs.CodeInvalidInput, err.Error())
	}

	tagsJSON, err := json.Marshal(record.Tags)
	if err != nil {
		tagsJSON = []byte("[]")
	}

	metadataJSON, err := s.metadataForUpdate(ctx, record)
	if err != nil {
		return err
	}
	columns := memoryFirstClassColumnValues(metadataJSON, record.Type, record.UserID, record.SessionID)

	var ct pgconn.CommandTag
	if record.UserID != "" {
		ct, err = s.pool.Exec(ctx,
			`UPDATE memories
			 SET content = $1, tags = $2, metadata = $3,
			     target_scope = $4, target_id = $5, visibility = $6, memory_kind = $7, subject_type = $8,
			     updated_at = NOW()
			 WHERE id = $9 AND user_id = $10`,
			record.Content, string(tagsJSON), string(metadataJSON),
			columns.TargetScope, columns.TargetID, columns.Visibility, columns.MemoryKind, columns.SubjectType,
			record.ID, record.UserID,
		)
		if isUndefinedMemoryFirstClassColumnError(err) {
			ct, err = s.pool.Exec(ctx,
				`UPDATE memories SET content = $1, tags = $2, metadata = $3, updated_at = NOW()
				 WHERE id = $4 AND user_id = $5`,
				record.Content, string(tagsJSON), string(metadataJSON), record.ID, record.UserID,
			)
		}
	} else {
		ct, err = s.pool.Exec(ctx,
			`UPDATE memories
			 SET content = $1, tags = $2, metadata = $3,
			     target_scope = $4, target_id = $5, visibility = $6, memory_kind = $7, subject_type = $8,
			     updated_at = NOW()
			 WHERE id = $9`,
			record.Content, string(tagsJSON), string(metadataJSON),
			columns.TargetScope, columns.TargetID, columns.Visibility, columns.MemoryKind, columns.SubjectType,
			record.ID,
		)
		if isUndefinedMemoryFirstClassColumnError(err) {
			ct, err = s.pool.Exec(ctx,
				`UPDATE memories SET content = $1, tags = $2, metadata = $3, updated_at = NOW()
				 WHERE id = $4`,
				record.Content, string(tagsJSON), string(metadataJSON), record.ID,
			)
		}
	}
	if err != nil {
		return errs.Wrap(errs.CodeMemoryWriteFailed, "更新记忆失败", err)
	}
	if ct.RowsAffected() == 0 {
		return errs.New(errs.CodeMemoryNotFound, fmt.Sprintf("记忆 %d 未找到", record.ID))
	}

	s.logger.Debug("更新记忆成功", zap.Int64("id", record.ID))

	// 异步重新生成 embedding（使用独立 ctx，不受 caller ctx 取消影响）
	if s.embedder != nil && s.vecStore != nil {
		rctx := RuntimeContextFromContext(ctx)
		s.embeddingWg.Add(1)
		go func() {
			defer s.embeddingWg.Done()
			// 用信号量限制并发数
			timer := time.NewTimer(5 * time.Minute)
			select {
			case s.embeddingSem <- struct{}{}:
				timer.Stop()
				defer func() { <-s.embeddingSem }()
			case <-timer.C:
				recordEmbeddingDropped(context.Background(), s.metrics, EmbeddingDroppedReasonSemaphoreTimeout)
				return
			}
			embedCtx := WithRuntimeContext(context.Background(), rctx)
			s.generateEmbedding(embedCtx, record.ID, record.Content)
		}()
	}

	return nil
}

func (s *PostgresMemoryStore) metadataForUpdate(ctx context.Context, record *MemoryRecord) (json.RawMessage, error) {
	var existing string
	var err error
	if record.UserID != "" {
		err = s.pool.QueryRow(ctx, `SELECT metadata FROM memories WHERE id = $1 AND user_id = $2`, record.ID, record.UserID).Scan(&existing)
	} else {
		err = s.pool.QueryRow(ctx, `SELECT metadata FROM memories WHERE id = $1`, record.ID).Scan(&existing)
	}
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, errs.New(errs.CodeMemoryNotFound, fmt.Sprintf("记忆 %d 未找到", record.ID))
		}
		return nil, errs.Wrap(errs.CodeMemoryReadFailed, "读取记忆元数据失败", err)
	}

	metadataJSON := PreserveGovernanceOnUpdate(json.RawMessage(existing), record.Metadata)
	if metadataJSON == nil {
		metadataJSON = json.RawMessage("{}")
	}
	metadataJSON, err = normalizeMemoryMetadata(metadataJSON, record.Type, record.UserID, RuntimeContextFromContext(ctx))
	if err != nil {
		return nil, errs.New(errs.CodeInvalidInput, err.Error())
	}
	return metadataJSON, nil
}

type memoryFirstClassColumns struct {
	TargetScope string
	TargetID    string
	Visibility  string
	MemoryKind  string
	SubjectType string
}

func memoryFirstClassColumnValues(metadata json.RawMessage, memType MemoryType, userID, sessionID string) memoryFirstClassColumns {
	target := DecodeMemoryTarget(metadata, memType, userID)
	if target.Scope == TargetScopeSession && target.ID == "" {
		target.ID = sessionID
	}
	kind := DecodeMemoryKind(metadata, memType)
	subjectType := defaultSubjectType(memType)
	meta := decodeMetadataMap(metadata)
	if value, _ := meta["subject_type"].(string); value != "" {
		subjectType = value
	}
	return memoryFirstClassColumns{
		TargetScope: string(target.Scope),
		TargetID:    target.ID,
		Visibility:  string(target.Visibility),
		MemoryKind:  string(kind),
		SubjectType: subjectType,
	}
}

func isUndefinedMemoryFirstClassColumnError(err error) bool {
	if err == nil {
		return false
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return false
	}
	if pgErr.Code != "42703" {
		return false
	}
	if pgErr.ColumnName == "" {
		return strings.Contains(pgErr.Message, "target_scope") ||
			strings.Contains(pgErr.Message, "target_id") ||
			strings.Contains(pgErr.Message, "visibility") ||
			strings.Contains(pgErr.Message, "memory_kind") ||
			strings.Contains(pgErr.Message, "subject_type")
	}
	return isMemoryFirstClassColumnName(pgErr.ColumnName)
}

func isMemoryFirstClassColumnName(column string) bool {
	switch column {
	case "target_scope", "target_id", "visibility", "memory_kind", "subject_type":
		return true
	default:
		return false
	}
}

// Delete 删除记忆
func (s *PostgresMemoryStore) Delete(ctx context.Context, id int64) error {
	userID := auth.UserIDFrom(ctx)
	var ct pgconn.CommandTag
	var err error
	if userID != "" {
		ct, err = s.pool.Exec(ctx, `DELETE FROM memories WHERE id = $1 AND user_id = $2`, id, userID)
	} else {
		ct, err = s.pool.Exec(ctx, `DELETE FROM memories WHERE id = $1`, id)
	}
	if err != nil {
		return errs.Wrap(errs.CodeMemoryWriteFailed, "删除记忆失败", err)
	}
	if ct.RowsAffected() == 0 {
		return errs.New(errs.CodeMemoryNotFound, fmt.Sprintf("记忆 %d 未找到", id))
	}

	// 同步删除向量索引
	if s.vecStore != nil {
		_ = s.vecStore.Remove(ctx, id)
	}

	s.logger.Debug("删除记忆成功", zap.Int64("id", id))
	return nil
}

// SetEmbedding 设置向量搜索组件（启用 embedding 后调用）
func (s *PostgresMemoryStore) SetEmbedding(embedder EmbeddingProvider, vecStore VectorStore) {
	s.embedder = embedder
	s.vecStore = vecStore
}

// generateEmbedding 生成并保存 embedding 向量（后台执行）
func (s *PostgresMemoryStore) generateEmbedding(ctx context.Context, id int64, content string) {
	start := time.Now()
	vectors, err := s.embedder.Embed(ctx, []string{content})
	if err != nil {
		recordEmbeddingDropped(ctx, s.metrics, EmbeddingDroppedReasonProviderError)
		recordEmbeddingLatency(ctx, s.metrics, "inline", "error", time.Since(start))
		s.logger.Warn("生成 embedding 失败", zap.Int64("id", id), zap.Error(err))
		return
	}
	if len(vectors) == 0 || len(vectors[0]) == 0 {
		recordEmbeddingDropped(ctx, s.metrics, EmbeddingDroppedReasonEmptyVector)
		recordEmbeddingLatency(ctx, s.metrics, "inline", "empty", time.Since(start))
		return
	}

	vec := vectors[0]
	blob := encodeFloat32s(vec)

	// 双写：BYTEA 列（向后兼容）
	_, err = s.pool.Exec(ctx,
		`UPDATE memories SET embedding = $1 WHERE id = $2`, blob, id)
	if err != nil {
		recordEmbeddingDropped(ctx, s.metrics, EmbeddingDroppedReasonStoreError)
		recordEmbeddingLatency(ctx, s.metrics, "inline", "error", time.Since(start))
		s.logger.Warn("保存 embedding 失败", zap.Int64("id", id), zap.Error(err))
		return
	}
	meta := EncodeVectorSpace(nil, VectorSpaceMetadata{
		Name:           metricVectorSpace(s.vectorSpace),
		EmbeddingState: EmbeddingStateReady,
		MigratedAt:     time.Now(),
	})
	_, err = s.pool.Exec(ctx,
		`UPDATE memories
		 SET metadata = COALESCE(metadata, '{}'::jsonb) || $1::jsonb,
		     target_scope = COALESCE(NULLIF(metadata->'target'->>'target_scope', ''), 'user'),
		     target_id = COALESCE(
		         NULLIF(metadata->'target'->>'target_id', ''),
		         CASE COALESCE(NULLIF(metadata->'target'->>'target_scope', ''), 'user')
		             WHEN 'user' THEN COALESCE(NULLIF(metadata->'target'->>'user_id', ''), user_id)
		             WHEN 'workspace' THEN metadata->'target'->>'workspace_id'
		             WHEN 'project' THEN metadata->'target'->>'project_id'
		             WHEN 'repo' THEN metadata->'target'->>'repo_id'
		             WHEN 'session' THEN COALESCE(NULLIF(metadata->'target'->>'session_id', ''), session_id)
		             WHEN 'agent' THEN metadata->'target'->>'agent_name'
		             WHEN 'skill' THEN metadata->'target'->>'skill_name'
		             ELSE ''
		         END,
		         ''
		     ),
		     visibility = COALESCE(
		         NULLIF(metadata->'target'->>'visibility', ''),
		         CASE WHEN COALESCE(NULLIF(metadata->'target'->>'target_scope', ''), 'user') = 'global'
		             THEN 'global'
		             ELSE 'private'
		         END
		     ),
		     memory_kind = COALESCE(
		         NULLIF(metadata->>'kind', ''),
		         CASE type
		             WHEN 'feedback' THEN 'feedback'
		             WHEN 'reference' THEN 'reference'
		             WHEN 'procedural' THEN 'procedural'
		             WHEN 'episodic' THEN 'episodic'
		             ELSE 'semantic'
		         END
		     ),
		     subject_type = COALESCE(
		         NULLIF(metadata->>'subject_type', ''),
		         CASE type
		             WHEN 'procedural' THEN 'procedure'
		             WHEN 'episodic' THEN 'episode'
		             ELSE type
		         END
		     )
		 WHERE id = $2`, string(meta), id)
	if isUndefinedMemoryFirstClassColumnError(err) {
		_, err = s.pool.Exec(ctx,
			`UPDATE memories
			    SET metadata = COALESCE(metadata, '{}'::jsonb) || $1::jsonb
			  WHERE id = $2`, string(meta), id)
	}
	if err != nil {
		recordEmbeddingDropped(ctx, s.metrics, EmbeddingDroppedReasonStoreError)
		recordEmbeddingLatency(ctx, s.metrics, "inline", "error", time.Since(start))
		s.logger.Warn("保存 embedding vector-space 元数据失败", zap.Int64("id", id), zap.Error(err))
		return
	}

	// 写入 VectorStore（InMemoryVecStore 或 PgVectorStore）
	if err := s.vecStore.Add(ctx, id, vec); err != nil {
		recordEmbeddingDropped(ctx, s.metrics, EmbeddingDroppedReasonVectorIndexError)
		recordEmbeddingLatency(ctx, s.metrics, "inline", "error", time.Since(start))
		if isVectorSpaceMismatchError(err) {
			recordMetric(ctx, s.metrics, MetricVectorSpaceMismatchTotal, 1, map[string]any{"operation": "index"})
		}
		s.logger.Warn("写入向量索引失败", zap.Int64("id", id), zap.Error(err))
		return
	}
	recordEmbeddingLatency(ctx, s.metrics, "inline", "ok", time.Since(start))
	s.logger.Debug("embedding 已生成并保存", zap.Int64("id", id), zap.Int("dim", len(vec)))
}

func (s *PostgresMemoryStore) SyncMemoryEmbedding(ctx context.Context, memoryID int64, vector []float32, status MemoryEmbeddingStatus) error {
	if memoryID == 0 {
		return errs.New(errs.CodeInvalidInput, "记忆 ID 不能为空")
	}
	if len(vector) == 0 {
		return errs.New(errs.CodeInvalidInput, "embedding vector 不能为空")
	}
	if status.VectorSpace == "" {
		status.VectorSpace = DefaultVectorSpaceName
	}
	if status.EmbeddingState == "" {
		status.EmbeddingState = EmbeddingStateReady
	}
	if status.UpdatedAt.IsZero() {
		status.UpdatedAt = time.Now()
	}
	blob := encodeFloat32s(vector)
	var existing, memType, userID, sessionID string
	if err := s.pool.QueryRow(ctx, `SELECT metadata, type, user_id, session_id FROM memories WHERE id=$1`, memoryID).Scan(&existing, &memType, &userID, &sessionID); err != nil {
		if err == pgx.ErrNoRows {
			return errs.New(errs.CodeMemoryNotFound, fmt.Sprintf("记忆 %d 未找到", memoryID))
		}
		return errs.Wrap(errs.CodeMemoryReadFailed, "读取记忆元数据失败", err)
	}
	meta := EncodeVectorSpace(json.RawMessage(existing), VectorSpaceMetadata{
		Name:           status.VectorSpace,
		EmbeddingState: status.EmbeddingState,
		MigratedAt:     status.UpdatedAt,
	})
	columns := memoryFirstClassColumnValues(meta, MemoryType(memType), userID, sessionID)
	ct, err := s.pool.Exec(ctx,
		`UPDATE memories
		 SET embedding=$1, metadata=$2,
		     target_scope=$3, target_id=$4, visibility=$5, memory_kind=$6, subject_type=$7,
		     updated_at=NOW()
		 WHERE id=$8`,
		blob, string(meta), columns.TargetScope, columns.TargetID, columns.Visibility, columns.MemoryKind, columns.SubjectType, memoryID)
	if isUndefinedMemoryFirstClassColumnError(err) {
		ct, err = s.pool.Exec(ctx, `UPDATE memories SET embedding=$1, metadata=$2, updated_at=NOW() WHERE id=$3`, blob, string(meta), memoryID)
	}
	if err != nil {
		return errs.Wrap(errs.CodeMemoryWriteFailed, "同步 embedding 失败", err)
	}
	if ct.RowsAffected() == 0 {
		return errs.New(errs.CodeMemoryNotFound, fmt.Sprintf("记忆 %d 未找到", memoryID))
	}
	if s.vecStore != nil {
		if err := s.vecStore.Add(ctx, memoryID, vector); err != nil {
			return err
		}
	}
	return nil
}

// Search 全文搜索记忆（tsvector + ts_rank 排序，中文回退 ILIKE）
func (s *PostgresMemoryStore) Search(ctx context.Context, opts SearchOptions) (*SearchResult, error) {
	if opts.Query == "" {
		return &SearchResult{}, nil
	}

	limit := opts.Limit
	if limit <= 0 {
		limit = 10
	}

	// 第一步：尝试 tsvector 全文搜索
	memories, err := s.searchTSVector(ctx, opts, limit)
	if err != nil {
		s.logger.Debug("tsvector 搜索失败，回退到 ILIKE", zap.Error(err))
	}

	// 第二步：如果 tsvector 无结果，回退到 ILIKE 模糊搜索（支持中文）
	if len(memories) == 0 {
		memories, err = s.searchILike(ctx, opts, limit)
		if err != nil {
			return nil, errs.Wrap(errs.CodeMemorySearchFailed, "搜索记忆失败", err)
		}
	}

	// 第三步：自然语言长句常混入大量上下文词。严格 AND 搜索 0 命中时，
	// 用高信号词做 relaxed OR 召回，避免已有记忆因“怎么/请按/建议”等词被整体过滤。
	if len(memories) == 0 {
		memories, err = s.searchRelaxed(ctx, opts, limit)
		if err != nil {
			return nil, errs.Wrap(errs.CodeMemorySearchFailed, "宽松搜索记忆失败", err)
		}
	}

	return &SearchResult{
		Memories: memories,
		Total:    len(memories),
	}, nil
}

// searchTSVector 使用 tsvector + ts_rank 搜索
func (s *PostgresMemoryStore) searchTSVector(ctx context.Context, opts SearchOptions, limit int) ([]MemoryRecord, error) {
	// 构建 tsquery：将用户输入按空格拆分为 & 连接的词
	tsQuery := buildTSQuery(opts.Query)

	query := `SELECT id, user_id, type, content, tags, session_id, metadata,
		created_at, updated_at, accessed_at, access_count,
		ts_rank(search_vector, to_tsquery('simple', $1)) AS score
	FROM memories
	WHERE search_vector @@ to_tsquery('simple', $1)`

	args := []any{tsQuery}
	argIdx := 2

	// 用户隔离过滤
	if opts.UserID != "" {
		query += fmt.Sprintf(" AND user_id = $%d", argIdx)
		args = append(args, opts.UserID)
		argIdx++
	}
	// 可选过滤
	if opts.Type != "" {
		query += fmt.Sprintf(" AND type = $%d", argIdx)
		args = append(args, string(opts.Type))
		argIdx++
	}
	if opts.SessionID != "" {
		query += fmt.Sprintf(" AND session_id = $%d", argIdx)
		args = append(args, opts.SessionID)
		argIdx++
	}
	if len(opts.Tags) > 0 {
		for _, tag := range opts.Tags {
			query += fmt.Sprintf(" AND tags ILIKE $%d", argIdx)
			args = append(args, "%"+tag+"%")
			argIdx++
		}
	}
	if opts.MinScore > 0 {
		query += fmt.Sprintf(" AND ts_rank(search_vector, to_tsquery('simple', $1)) >= $%d", argIdx)
		args = append(args, opts.MinScore)
		argIdx++
	}
	query, args, argIdx = s.applyScopeFilterForSearch(ctx, opts, query, args, argIdx)

	query += fmt.Sprintf(" ORDER BY score DESC LIMIT $%d", argIdx)
	args = append(args, limit)

	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	memories, err := scanPgSearchRows(rows, true)
	if err != nil {
		return nil, err
	}
	return filterByMinScore(memories, opts.MinScore), nil
}

// searchILike 使用 ILIKE 模糊搜索（中文回退方案）
func (s *PostgresMemoryStore) searchILike(ctx context.Context, opts SearchOptions, limit int) ([]MemoryRecord, error) {
	query := `SELECT id, user_id, type, content, tags, session_id, metadata,
		created_at, updated_at, accessed_at, access_count
	FROM memories WHERE TRUE`

	var args []any
	argIdx := 1

	// 用户隔离过滤
	if opts.UserID != "" {
		query += fmt.Sprintf(" AND user_id = $%d", argIdx)
		args = append(args, opts.UserID)
		argIdx++
	}
	// 将查询拆分为关键词，每个关键词都必须匹配（AND 逻辑）
	keywords := strings.Fields(opts.Query)
	for _, kw := range keywords {
		query += fmt.Sprintf(" AND content ILIKE $%d", argIdx)
		args = append(args, "%"+kw+"%")
		argIdx++
	}

	if opts.Type != "" {
		query += fmt.Sprintf(" AND type = $%d", argIdx)
		args = append(args, string(opts.Type))
		argIdx++
	}
	if opts.SessionID != "" {
		query += fmt.Sprintf(" AND session_id = $%d", argIdx)
		args = append(args, opts.SessionID)
		argIdx++
	}
	if len(opts.Tags) > 0 {
		for _, tag := range opts.Tags {
			query += fmt.Sprintf(" AND tags ILIKE $%d", argIdx)
			args = append(args, "%"+tag+"%")
			argIdx++
		}
	}
	query, args, argIdx = s.applyScopeFilterForSearch(ctx, opts, query, args, argIdx)

	query += fmt.Sprintf(" ORDER BY accessed_at DESC LIMIT $%d", argIdx)
	args = append(args, limit)

	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	memories, err := scanPgSearchRows(rows, false)
	if err != nil {
		return nil, err
	}
	return filterByMinScore(memories, opts.MinScore), nil
}

func (s *PostgresMemoryStore) searchRelaxed(ctx context.Context, opts SearchOptions, limit int) ([]MemoryRecord, error) {
	terms := relaxedSearchTerms(opts.Query)
	if len(terms) == 0 {
		return nil, nil
	}
	maxScore := 0.0
	for _, term := range terms {
		maxScore += relaxedSearchTermWeight(term)
	}
	if maxScore <= 0 {
		return nil, nil
	}

	query := `SELECT id, user_id, type, content, tags, session_id, metadata,
		created_at, updated_at, accessed_at, access_count,
		((`
	var args []any
	scoreParts := make([]string, 0, len(terms))
	argIdx := 1
	for _, term := range terms {
		scoreParts = append(scoreParts, fmt.Sprintf(
			`CASE WHEN search_vector @@ plainto_tsquery('simple', $%[1]d) OR content ILIKE $%[2]d OR tags ILIKE $%[2]d THEN $%[3]d ELSE 0 END`,
			argIdx, argIdx+1, argIdx+2,
		))
		args = append(args, term, "%"+term+"%", relaxedSearchTermWeight(term))
		argIdx += 3
	}
	query += strings.Join(scoreParts, " + ")
	query += fmt.Sprintf(`) / $%d::float8) AS score
	FROM memories WHERE (`, argIdx)
	args = append(args, maxScore)
	argIdx++

	predicates := make([]string, 0, len(terms))
	for i := 0; i < len(terms); i++ {
		base := 1 + i*3
		predicates = append(predicates, fmt.Sprintf(
			`search_vector @@ plainto_tsquery('simple', $%[1]d) OR content ILIKE $%[2]d OR tags ILIKE $%[2]d`,
			base, base+1,
		))
	}
	query += strings.Join(predicates, " OR ")
	query += `)`

	if opts.UserID != "" {
		query += fmt.Sprintf(" AND user_id = $%d", argIdx)
		args = append(args, opts.UserID)
		argIdx++
	}
	if opts.Type != "" {
		query += fmt.Sprintf(" AND type = $%d", argIdx)
		args = append(args, string(opts.Type))
		argIdx++
	}
	if opts.SessionID != "" {
		query += fmt.Sprintf(" AND session_id = $%d", argIdx)
		args = append(args, opts.SessionID)
		argIdx++
	}
	if len(opts.Tags) > 0 {
		for _, tag := range opts.Tags {
			query += fmt.Sprintf(" AND tags ILIKE $%d", argIdx)
			args = append(args, "%"+tag+"%")
			argIdx++
		}
	}
	query, args, argIdx = s.applyScopeFilterForSearch(ctx, opts, query, args, argIdx)

	query += fmt.Sprintf(" ORDER BY score DESC, accessed_at DESC LIMIT $%d", argIdx)
	args = append(args, limit)

	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	memories, err := scanPgSearchRows(rows, true)
	if err != nil {
		return nil, err
	}
	return filterByMinScore(memories, opts.MinScore), nil
}

func filterByMinScore(memories []MemoryRecord, minScore float64) []MemoryRecord {
	if minScore <= 0 {
		return memories
	}
	out := memories[:0]
	for _, mem := range memories {
		if mem.Score >= minScore {
			out = append(out, mem)
		}
	}
	return out
}

// List 列出记忆
func (s *PostgresMemoryStore) List(ctx context.Context, opts SearchOptions) (*SearchResult, error) {
	query := `SELECT id, user_id, type, content, tags, session_id, metadata,
		created_at, updated_at, accessed_at, access_count
	FROM memories WHERE TRUE`

	var args []any
	argIdx := 1

	// 用户隔离过滤
	if opts.UserID != "" {
		query += fmt.Sprintf(" AND user_id = $%d", argIdx)
		args = append(args, opts.UserID)
		argIdx++
	}
	if opts.Type != "" {
		query += fmt.Sprintf(" AND type = $%d", argIdx)
		args = append(args, string(opts.Type))
		argIdx++
	}
	if opts.SessionID != "" {
		query += fmt.Sprintf(" AND session_id = $%d", argIdx)
		args = append(args, opts.SessionID)
		argIdx++
	}
	if len(opts.Tags) > 0 {
		for _, tag := range opts.Tags {
			query += fmt.Sprintf(" AND tags ILIKE $%d", argIdx)
			args = append(args, "%"+tag+"%")
			argIdx++
		}
	}
	query, args, argIdx = s.applyScopeFilterForSearch(ctx, opts, query, args, argIdx)

	query += " ORDER BY accessed_at DESC"

	limit := opts.Limit
	if limit <= 0 {
		limit = 10
	}
	query += fmt.Sprintf(" LIMIT $%d", argIdx)
	args = append(args, limit)

	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, errs.Wrap(errs.CodeMemoryReadFailed, "列出记忆失败", err)
	}
	defer rows.Close()

	memories, err := scanPgSearchRows(rows, false)
	if err != nil {
		return nil, err
	}

	// 获取总数（同步 user_id 过滤，避免泄露跨用户计数）
	var total int
	countQuery := `SELECT COUNT(*) FROM memories WHERE TRUE`
	var countArgs []any
	countArgIdx := 1
	if opts.UserID != "" {
		countQuery += fmt.Sprintf(" AND user_id = $%d", countArgIdx)
		countArgs = append(countArgs, opts.UserID)
		countArgIdx++
	}
	if opts.Type != "" {
		countQuery += fmt.Sprintf(" AND type = $%d", countArgIdx)
		countArgs = append(countArgs, string(opts.Type))
		countArgIdx++
	}
	if opts.SessionID != "" {
		countQuery += fmt.Sprintf(" AND session_id = $%d", countArgIdx)
		countArgs = append(countArgs, opts.SessionID)
		countArgIdx++
	}
	countQuery, countArgs, countArgIdx = s.applyScopeFilterForSearch(ctx, opts, countQuery, countArgs, countArgIdx)
	_ = countArgIdx
	_ = s.pool.QueryRow(ctx, countQuery, countArgs...).Scan(&total)

	return &SearchResult{
		Memories: memories,
		Total:    total,
	}, nil
}

func (s *PostgresMemoryStore) applyScopeFilter(ctx context.Context, query string, args []any, argIdx int) (string, []any, int) {
	if s.scopePolicy == nil {
		s.scopePolicy = DefaultScopePolicy{}
	}
	return appendScopeSQL(query, args, argIdx, s.scopePolicy.SQLFilter(RuntimeContextFromContext(ctx)))
}

func (s *PostgresMemoryStore) applyScopeFilterForSearch(ctx context.Context, opts SearchOptions, query string, args []any, argIdx int) (string, []any, int) {
	rctx := RuntimeContextFromContext(ctx)
	if rctx.UserID == "" {
		rctx.UserID = opts.UserID
	}
	if s.scopePolicy == nil {
		s.scopePolicy = DefaultScopePolicy{}
	}
	return appendScopeSQL(query, args, argIdx, s.scopePolicy.SQLFilter(rctx))
}

func (s *PostgresMemoryStore) bumpAccessAsync(ids []int64) {
	ids = uniquePositiveIDs(ids)
	if len(ids) == 0 {
		return
	}
	s.embeddingWg.Add(1)
	go func() {
		defer s.embeddingWg.Done()
		_, err := s.pool.Exec(context.Background(),
			`UPDATE memories SET accessed_at = NOW(), access_count = access_count + 1 WHERE id = ANY($1)`, ids)
		if err != nil {
			s.logger.Debug("批量更新记忆访问计数失败", zap.Error(err))
		}
	}()
}

func uniquePositiveIDs(ids []int64) []int64 {
	seen := make(map[int64]bool, len(ids))
	out := make([]int64, 0, len(ids))
	for _, id := range ids {
		if id <= 0 || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}

func memoryRecordIDs(memories []MemoryRecord) []int64 {
	ids := make([]int64, 0, len(memories))
	for _, memory := range memories {
		ids = append(ids, memory.ID)
	}
	return ids
}

// Stats 返回记忆统计信息
func (s *PostgresMemoryStore) Stats(ctx context.Context) (*MemoryStats, error) {
	stats := &MemoryStats{
		ByType: make(map[string]int),
	}

	userID := auth.UserIDFrom(ctx)
	var rows pgx.Rows
	var err error
	if userID != "" {
		rows, err = s.pool.Query(ctx, `SELECT type, COUNT(*) FROM memories WHERE user_id = $1 GROUP BY type`, userID)
	} else {
		rows, err = s.pool.Query(ctx, `SELECT type, COUNT(*) FROM memories GROUP BY type`)
	}
	if err != nil {
		return nil, errs.Wrap(errs.CodeMemoryReadFailed, "统计记忆失败", err)
	}
	defer rows.Close()

	for rows.Next() {
		var memType string
		var count int
		if err := rows.Scan(&memType, &count); err != nil {
			continue
		}
		stats.ByType[memType] = count
		stats.Total += count
	}

	if userID != "" {
		_ = s.pool.QueryRow(ctx, `SELECT MIN(created_at)::text FROM memories WHERE user_id = $1`, userID).Scan(&stats.OldestAt)
		_ = s.pool.QueryRow(ctx, `SELECT MAX(created_at)::text FROM memories WHERE user_id = $1`, userID).Scan(&stats.NewestAt)
	} else {
		_ = s.pool.QueryRow(ctx, `SELECT MIN(created_at)::text FROM memories`).Scan(&stats.OldestAt)
		_ = s.pool.QueryRow(ctx, `SELECT MAX(created_at)::text FROM memories`).Scan(&stats.NewestAt)
	}

	return stats, nil
}

func (s *PostgresMemoryStore) GovernanceStats(ctx context.Context, opts GovernanceQueryOptions) (GovernanceStats, error) {
	search := opts.Search
	if search.Limit <= 0 {
		search.Limit = 1000
	}
	result, err := s.List(ctx, search)
	if err != nil {
		return GovernanceStats{}, err
	}
	return AnalyzeGovernance(result.Memories, opts.Now, opts.MinConfidence), nil
}

func (s *PostgresMemoryStore) PruneGovernance(ctx context.Context, plan GovernancePrunePlan) (GovernancePruneResult, error) {
	result := GovernancePruneResult{
		Matched:   len(plan.DeleteIDs),
		DeleteIDs: append([]int64(nil), plan.DeleteIDs...),
		Reasons:   plan.Reasons,
	}
	for _, id := range plan.DeleteIDs {
		if err := s.Delete(ctx, id); err != nil {
			return result, err
		}
		result.Deleted++
	}
	return result, nil
}

// Close 释放存储资源（不关闭共享连接池）
func (s *PostgresMemoryStore) Close() error {
	// 等待所有 embedding goroutine 完成
	s.embeddingWg.Wait()
	return nil
}

// BackfillEmbeddingVec 将历史 BYTEA 向量迁移到 embedding_vec 列
// 分批处理，避免大表锁。返回迁移的记录数。
func (s *PostgresMemoryStore) BackfillEmbeddingVec(ctx context.Context, batchSize int) (int, error) {
	if batchSize <= 0 {
		batchSize = 100
	}
	var total int
	for {
		rows, err := s.pool.Query(ctx,
			`SELECT id, embedding FROM memories
			 WHERE embedding IS NOT NULL AND embedding_vec IS NULL
			 LIMIT $1`, batchSize)
		if err != nil {
			return total, err
		}

		batch := 0
		for rows.Next() {
			var id int64
			var blob []byte
			if err := rows.Scan(&id, &blob); err != nil {
				continue
			}
			vec := decodeFloat32s(blob)
			if len(vec) == 0 {
				continue
			}
			vecStr := float32sToVecLiteral(vec)
			if _, err := s.pool.Exec(ctx,
				`UPDATE memories SET embedding_vec = $1::vector WHERE id = $2`,
				vecStr, id); err != nil {
				s.logger.Warn("回填向量失败", zap.Int64("id", id), zap.Error(err))
				continue
			}
			batch++
		}
		rows.Close()

		total += batch
		if batch == 0 {
			break
		}
		s.logger.Info("回填进度", zap.Int("batch", batch), zap.Int("total", total))
	}
	return total, nil
}

// buildTSQuery 将用户输入转换为 tsquery 格式
// "hello world" -> "hello & world"
func buildTSQuery(input string) string {
	words := strings.Fields(input)
	if len(words) == 0 {
		return ""
	}

	// 转义特殊字符，用 & 连接
	escaped := make([]string, 0, len(words))
	for _, w := range words {
		// 去除 tsquery 特殊字符
		w = strings.NewReplacer(
			"&", "", "|", "", "!", "", "(", "", ")", "",
			"<", "", ">", "", "'", "", ":", "",
		).Replace(w)
		w = strings.TrimSpace(w)
		if w != "" {
			escaped = append(escaped, "'"+w+"'")
		}
	}
	return strings.Join(escaped, " & ")
}

func relaxedSearchTerms(input string) []string {
	seen := map[string]bool{}
	weighted := make([]relaxedSearchTerm, 0, 8)
	for _, raw := range extractRelaxedSearchTerms(input) {
		term := normalizeRelaxedSearchTerm(raw)
		if term == "" || seen[term] || isRelaxedSearchStopTerm(term) {
			continue
		}
		seen[term] = true
		weighted = append(weighted, relaxedSearchTerm{Term: term, Weight: relaxedSearchTermWeight(term)})
	}
	sort.SliceStable(weighted, func(i, j int) bool {
		if weighted[i].Weight == weighted[j].Weight {
			return weighted[i].Term < weighted[j].Term
		}
		return weighted[i].Weight > weighted[j].Weight
	})
	if len(weighted) > 8 {
		weighted = weighted[:8]
	}
	out := make([]string, 0, len(weighted))
	for _, term := range weighted {
		out = append(out, term.Term)
	}
	return out
}

type relaxedSearchTerm struct {
	Term   string
	Weight float64
}

func extractRelaxedSearchTerms(input string) []string {
	var terms []string
	var ascii strings.Builder
	var cjk strings.Builder
	flushASCII := func() {
		if ascii.Len() > 0 {
			terms = append(terms, ascii.String())
			ascii.Reset()
		}
	}
	flushCJK := func() {
		if cjk.Len() > 0 {
			terms = append(terms, cjk.String())
			cjk.Reset()
		}
	}

	for _, r := range strings.ToLower(input) {
		switch {
		case isRelaxedASCIIWordRune(r):
			flushCJK()
			ascii.WriteRune(r)
		case unicode.Is(unicode.Han, r):
			flushASCII()
			cjk.WriteRune(r)
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			flushASCII()
			flushCJK()
			terms = append(terms, string(r))
		default:
			flushASCII()
			flushCJK()
		}
	}
	flushASCII()
	flushCJK()
	return terms
}

func isRelaxedASCIIWordRune(r rune) bool {
	if r > unicode.MaxASCII {
		return false
	}
	return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '-' || r == '/' || r == '.'
}

func normalizeRelaxedSearchTerm(input string) string {
	input = strings.ToLower(strings.TrimSpace(input))
	input = strings.Trim(input, "`'\"“”‘’()[]{}<>，。！？；：、,.!?;:")
	if isAllHan(input) {
		input = normalizeRelaxedCJKTerm(input)
	}
	var b strings.Builder
	for _, r := range input {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '-' || r == '/' || r == '.' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func isAllHan(input string) bool {
	if input == "" {
		return false
	}
	for _, r := range input {
		if !unicode.Is(unicode.Han, r) {
			return false
		}
	}
	return true
}

func normalizeRelaxedCJKTerm(input string) string {
	replacer := strings.NewReplacer(
		"请按", "",
		"按照", "",
		"应该", "",
		"怎么", "",
		"如何", "",
		"我的", "",
		"你的", "",
		"给我", "",
		"历史", "",
		"工作", "",
		"方式", "",
		"建议", "",
		"一下", "",
		"这个", "",
		"那个", "",
		"请", "",
		"按", "",
		"我", "",
		"你", "",
	)
	return strings.TrimSpace(replacer.Replace(input))
}

func relaxedSearchTermWeight(term string) float64 {
	if term == "" {
		return 0
	}
	hasASCII := false
	for _, r := range term {
		if r <= unicode.MaxASCII && (unicode.IsLetter(r) || unicode.IsDigit(r)) {
			hasASCII = true
			break
		}
	}
	switch {
	case strings.ContainsAny(term, "./-_"):
		return 3.0
	case hasASCII && len(term) >= 2:
		return 2.5
	case len([]rune(term)) >= 3:
		return 1.2
	default:
		return 0.8
	}
}

func isRelaxedSearchStopTerm(term string) bool {
	switch term {
	case "我", "你", "他", "她", "它", "我们", "你们", "他们",
		"应该", "怎么", "如何", "请", "按", "按照", "我的", "你的",
		"历史", "工作", "方式", "建议", "一下", "这个", "那个",
		"the", "a", "an", "to", "of", "and", "or", "in", "on", "for", "with":
		return true
	default:
		return false
	}
}

// scanPgMemoryRecord 从单行查询结果扫描记忆记录
func scanPgMemoryRecord(row pgx.Row) (*MemoryRecord, error) {
	var (
		id          int64
		userID      string
		memType     string
		content     string
		tags        string
		sessionID   string
		metadata    string
		createdAt   time.Time
		updatedAt   time.Time
		accessedAt  time.Time
		accessCount int
	)

	err := row.Scan(&id, &userID, &memType, &content, &tags, &sessionID, &metadata,
		&createdAt, &updatedAt, &accessedAt, &accessCount)
	if err != nil {
		return nil, err
	}

	record := &MemoryRecord{
		ID:          id,
		UserID:      userID,
		Type:        MemoryType(memType),
		Content:     content,
		SessionID:   sessionID,
		AccessCount: accessCount,
		CreatedAt:   createdAt,
		UpdatedAt:   updatedAt,
		AccessedAt:  accessedAt,
	}
	_ = json.Unmarshal([]byte(tags), &record.Tags)
	if metadata != "" {
		record.Metadata = json.RawMessage(metadata)
	}
	return record, nil
}

// scanPgSearchRows 扫描搜索结果行
func scanPgSearchRows(rows pgx.Rows, hasScore bool) ([]MemoryRecord, error) {
	var memories []MemoryRecord
	for rows.Next() {
		var (
			id          int64
			userID      string
			memType     string
			content     string
			tags        string
			sessionID   string
			metadata    string
			createdAt   time.Time
			updatedAt   time.Time
			accessedAt  time.Time
			accessCount int
			score       float64
		)

		var err error
		if hasScore {
			err = rows.Scan(&id, &userID, &memType, &content, &tags, &sessionID, &metadata,
				&createdAt, &updatedAt, &accessedAt, &accessCount, &score)
		} else {
			err = rows.Scan(&id, &userID, &memType, &content, &tags, &sessionID, &metadata,
				&createdAt, &updatedAt, &accessedAt, &accessCount)
		}
		if err != nil {
			continue
		}

		record := MemoryRecord{
			ID:          id,
			UserID:      userID,
			Type:        MemoryType(memType),
			Content:     content,
			SessionID:   sessionID,
			Score:       score,
			AccessCount: accessCount,
			CreatedAt:   createdAt,
			UpdatedAt:   updatedAt,
			AccessedAt:  accessedAt,
		}
		_ = json.Unmarshal([]byte(tags), &record.Tags)
		if metadata != "" {
			record.Metadata = json.RawMessage(metadata)
		}
		memories = append(memories, record)
	}
	return memories, nil
}
