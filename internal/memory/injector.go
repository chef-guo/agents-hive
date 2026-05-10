package memory

import (
	"context"
	"fmt"
	"strings"
	"time"

	"go.uber.org/zap"
)

// Injector 将相关记忆注入到 LLM 上下文
type Injector struct {
	store             MemoryStore
	hybrid            *HybridSearcher // 混合搜索引擎（可选）
	maxTokens         int             // 注入的最大 token 数
	topK              int             // 最大记忆条数
	minConfidence     float64         // 最低注入置信度
	minScore          float64         // 最低相关性分数，0 表示不按分数过滤
	feedbackTopK      int             // feedback 最大记忆条数
	memoryTopK        int             // 普通记忆最大条数
	feedbackMaxTokens int             // feedback 注入 token 上限
	memoryMaxTokens   int             // 普通记忆注入 token 上限
	scopePolicy       ScopePolicy
	logger            *zap.Logger
}

// NewInjector 创建记忆注入器
func NewInjector(store MemoryStore, maxTokens, topK int, logger *zap.Logger) *Injector {
	cfg := DefaultInjectionConfig()
	if maxTokens <= 0 {
		maxTokens = 2000
	}
	if topK <= 0 {
		topK = 10
	}
	cfg.MemoryMaxTokens = maxTokens
	cfg.MemoryTopK = topK
	cfg.FeedbackMaxTokens = minPositiveInt(cfg.FeedbackMaxTokens, maxTokens)
	cfg.FeedbackTopK = minPositiveInt(cfg.FeedbackTopK, topK)
	inj := NewInjectorWithConfig(store, cfg, logger)
	inj.maxTokens = maxTokens
	inj.topK = topK
	return inj
}

// InjectionConfig 控制 feedback 和普通记忆的独立注入预算。
type InjectionConfig struct {
	MinConfidence     float64
	MinScore          float64
	FeedbackTopK      int
	MemoryTopK        int
	FeedbackMaxTokens int
	MemoryMaxTokens   int
}

func DefaultInjectionConfig() InjectionConfig {
	return InjectionConfig{
		MinConfidence:     0.5,
		MinScore:          0.0,
		FeedbackTopK:      3,
		MemoryTopK:        8,
		FeedbackMaxTokens: 600,
		MemoryMaxTokens:   1800,
	}
}

func NormalizeInjectionConfig(cfg InjectionConfig) InjectionConfig {
	def := DefaultInjectionConfig()
	if cfg.MinConfidence <= 0 || cfg.MinConfidence > 1 {
		cfg.MinConfidence = def.MinConfidence
	}
	if cfg.MinScore < 0 {
		cfg.MinScore = 0
	}
	if cfg.FeedbackTopK <= 0 {
		cfg.FeedbackTopK = def.FeedbackTopK
	}
	if cfg.FeedbackTopK > 20 {
		cfg.FeedbackTopK = 20
	}
	if cfg.MemoryTopK <= 0 {
		cfg.MemoryTopK = def.MemoryTopK
	}
	if cfg.MemoryTopK > 50 {
		cfg.MemoryTopK = 50
	}
	if cfg.FeedbackMaxTokens <= 0 {
		cfg.FeedbackMaxTokens = def.FeedbackMaxTokens
	}
	if cfg.FeedbackMaxTokens > 4000 {
		cfg.FeedbackMaxTokens = 4000
	}
	if cfg.MemoryMaxTokens <= 0 {
		cfg.MemoryMaxTokens = def.MemoryMaxTokens
	}
	if cfg.MemoryMaxTokens > 12000 {
		cfg.MemoryMaxTokens = 12000
	}
	return cfg
}

// NewInjectorWithConfig 创建带独立 feedback/普通记忆预算的注入器。
func NewInjectorWithConfig(store MemoryStore, cfg InjectionConfig, logger *zap.Logger) *Injector {
	cfg = NormalizeInjectionConfig(cfg)
	return &Injector{
		store:             store,
		maxTokens:         cfg.FeedbackMaxTokens + cfg.MemoryMaxTokens,
		topK:              cfg.FeedbackTopK + cfg.MemoryTopK,
		minConfidence:     cfg.MinConfidence,
		minScore:          cfg.MinScore,
		feedbackTopK:      cfg.FeedbackTopK,
		memoryTopK:        cfg.MemoryTopK,
		feedbackMaxTokens: cfg.FeedbackMaxTokens,
		memoryMaxTokens:   cfg.MemoryMaxTokens,
		scopePolicy:       DefaultScopePolicy{},
		logger:            logger,
	}
}

// SetHybridSearcher 设置混合搜索引擎（启用 embedding 后调用）
func (inj *Injector) SetHybridSearcher(h *HybridSearcher) {
	inj.hybrid = h
}

// SetMinConfidence 设置 memory 注入最低置信度。
func (inj *Injector) SetMinConfidence(v float64) {
	if v <= 0 || v > 1 {
		return
	}
	inj.minConfidence = v
}

// SetMinScore 设置 memory 注入最低相关性分数；0 表示不过滤。
func (inj *Injector) SetMinScore(v float64) {
	if v < 0 {
		return
	}
	inj.minScore = v
}

// InjectContext 基于用户消息查询相关记忆，返回注入文本
// 返回空字符串表示无相关记忆
func (inj *Injector) InjectContext(ctx context.Context, userMessage string, sessionID string, userID string) (string, error) {
	result, err := inj.InjectContextDetailed(ctx, userMessage, sessionID, userID)
	return result.Text, err
}

// InjectContextDetailed 基于用户消息查询相关记忆，返回结构化注入结果。
func (inj *Injector) InjectContextDetailed(ctx context.Context, userMessage string, sessionID string, userID string) (InjectionResult, error) {
	var out InjectionResult
	if userMessage == "" {
		return out, nil
	}

	feedback, err := inj.searchMemories(ctx, userMessage, userID, MemoryTypeFeedback, inj.feedbackTopK)
	if err != nil {
		inj.logger.Warn("搜索 feedback 记忆失败", zap.Error(err))
		return out, err
	}
	feedback = filterOnlyMemoryType(feedback, MemoryTypeFeedback)
	regular, err := inj.searchMemories(ctx, userMessage, userID, "", inj.memoryTopK)
	if err != nil {
		inj.logger.Warn("搜索相关记忆失败", zap.Error(err))
		return out, err
	}
	regular = filterOutMemoryType(regular, MemoryTypeFeedback)

	if len(feedback) == 0 && len(regular) == 0 {
		inj.logger.Debug("无相关记忆", zap.String("query", userMessage))
		return out, nil
	}

	now := time.Now()
	var sections []string
	var totalTokens int
	if text, tokens := inj.buildSection(&out, "## 工作方式反馈", feedback, inj.feedbackMaxTokens, now, userID, true); text != "" {
		sections = append(sections, text)
		totalTokens += tokens
	}
	if text, tokens := inj.buildSection(&out, "## 相关记忆", regular, inj.memoryMaxTokens, now, userID, false); text != "" {
		sections = append(sections, text)
		totalTokens += tokens
	}
	if len(sections) == 0 {
		return out, nil
	}

	out.Text = strings.Join(sections, "\n")
	out.EstimatedTokens = totalTokens
	inj.logger.Debug("注入相关记忆",
		zap.Int("count", len(out.Memories)),
		zap.Int("feedback_count", out.FeedbackCount),
		zap.Int("regular_count", out.RegularCount),
		zap.Int("estimated_tokens", totalTokens),
	)
	return out, nil
}

func (inj *Injector) searchMemories(ctx context.Context, query, userID string, memType MemoryType, limit int) ([]MemoryRecord, error) {
	if limit <= 0 {
		return nil, nil
	}
	if inj.hybrid != nil && memType == "" {
		scoredIDs, err := inj.hybrid.Search(ctx, query, limit, userID)
		if err != nil {
			inj.logger.Warn("混合搜索失败，回退到 FTS5", zap.Error(err))
		} else if len(scoredIDs) > 0 {
			ids := make([]int64, 0, len(scoredIDs))
			scores := make(map[int64]float64, len(scoredIDs))
			for _, sid := range scoredIDs {
				if sid.ID <= 0 {
					continue
				}
				ids = append(ids, sid.ID)
				scores[sid.ID] = sid.Score
			}
			memories, err := inj.store.BatchGet(ctx, ids)
			if err != nil {
				inj.logger.Warn("批量读取混合搜索结果失败，回退到 FTS5", zap.Error(err))
			} else {
				byID := make(map[int64]MemoryRecord, len(memories))
				for _, mem := range memories {
					mem.Score = scores[mem.ID]
					byID[mem.ID] = mem
				}
				ordered := make([]MemoryRecord, 0, len(scoredIDs))
				for _, sid := range scoredIDs {
					if mem, ok := byID[sid.ID]; ok {
						ordered = append(ordered, mem)
					}
				}
				return ordered, nil
			}
		}
	}

	result, err := inj.store.Search(ctx, SearchOptions{
		Query:    query,
		Limit:    limit,
		UserID:   userID,
		Type:     memType,
		MinScore: inj.minScore,
	})
	if err != nil {
		return nil, err
	}
	if result == nil {
		return nil, nil
	}
	return result.Memories, nil
}

func filterOutMemoryType(memories []MemoryRecord, memType MemoryType) []MemoryRecord {
	if memType == "" || len(memories) == 0 {
		return memories
	}
	out := memories[:0]
	for _, mem := range memories {
		if mem.Type != memType {
			out = append(out, mem)
		}
	}
	return out
}

func filterOnlyMemoryType(memories []MemoryRecord, memType MemoryType) []MemoryRecord {
	if memType == "" || len(memories) == 0 {
		return memories
	}
	out := memories[:0]
	for _, mem := range memories {
		if mem.Type == memType {
			out = append(out, mem)
		}
	}
	return out
}

func (inj *Injector) buildSection(out *InjectionResult, title string, memories []MemoryRecord, maxTokens int, now time.Time, userID string, feedback bool) (string, int) {
	var sb strings.Builder
	sb.WriteString(title)
	sb.WriteString("\n\n")
	headerTokens := estimateTokens(sb.String())
	totalTokens := headerTokens
	rctx := RuntimeContext{UserID: userID}
	if inj.scopePolicy == nil {
		inj.scopePolicy = DefaultScopePolicy{}
	}

	for _, mem := range memories {
		g := DecodeGovernance(mem.Metadata)
		if allowed, reason := inj.scopePolicy.Allow(mem, rctx, now); !allowed {
			if reason == "cross_user" {
				out.recordSkipped(mem.ID, "cross_user", feedback)
			} else {
				out.recordSkipped(mem.ID, "scope", feedback)
			}
			continue
		}
		if userID != "" && mem.UserID != "" && mem.UserID != userID {
			out.recordSkipped(mem.ID, "cross_user", feedback)
			continue
		}
		if !g.ExpiresAt.IsZero() && now.After(g.ExpiresAt) {
			out.recordSkipped(mem.ID, "expired", feedback)
			continue
		}
		if g.Confidence > 0 && g.Confidence < inj.minConfidence {
			out.recordSkipped(mem.ID, "low_trust", feedback)
			continue
		}
		if inj.minScore > 0 && mem.Score > 0 && mem.Score < inj.minScore {
			out.recordSkipped(mem.ID, "low_score", feedback)
			continue
		}

		line := fmt.Sprintf("- [%s] %s\n", mem.Type, mem.Content)
		lineTokens := estimateTokens(line)
		if totalTokens+lineTokens > maxTokens {
			inj.logger.Debug("记忆注入达到 token 上限",
				zap.Int("current_tokens", totalTokens),
				zap.Int("max_tokens", maxTokens),
			)
			out.recordSkipped(mem.ID, "token_budget", feedback)
			continue
		}

		sb.WriteString(line)
		totalTokens += lineTokens
		out.Memories = append(out.Memories, InjectedMemory{
			ID:         mem.ID,
			Type:       mem.Type,
			Score:      mem.Score,
			Confidence: g.Confidence,
			Source:     g.Source,
		})
		if feedback {
			out.FeedbackCount++
		} else {
			out.RegularCount++
		}
	}

	if totalTokens <= headerTokens {
		return "", 0
	}
	return sb.String(), totalTokens
}

func (r *InjectionResult) recordSkipped(id int64, reason string, feedback bool) {
	switch reason {
	case "expired":
		r.SkippedExpired++
	case "low_trust":
		r.SkippedLowTrust++
	case "cross_user":
		r.SkippedCrossUser++
	case "scope":
		r.SkippedScope++
	case "low_score":
		r.SkippedLowScore++
	case "token_budget":
		r.SkippedTokenBudget++
		if feedback {
			r.SkippedFeedbackBudget++
		} else {
			r.SkippedRegularBudget++
		}
	}
	r.SkippedMemoryIDs = append(r.SkippedMemoryIDs, id)
}

func minPositiveInt(a, b int) int {
	if a <= 0 {
		return b
	}
	if b <= 0 || a < b {
		return a
	}
	return b
}

// estimateTokens 粗略估算文本的 token 数（约 4 个字符 = 1 token）
func estimateTokens(text string) int {
	n := len(text) / 4
	if n == 0 && len(text) > 0 {
		n = 1
	}
	return n
}
