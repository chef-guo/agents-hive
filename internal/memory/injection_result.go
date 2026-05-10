package memory

// InjectedMemory 是一次上下文注入中实际进入 prompt 的记忆摘要。
type InjectedMemory struct {
	ID         int64      `json:"id"`
	Type       MemoryType `json:"type"`
	Score      float64    `json:"score,omitempty"`
	Confidence float64    `json:"confidence,omitempty"`
	Source     string     `json:"source,omitempty"`
}

// InjectionResult 让质量系统能解释 memory/context 的构成与过滤原因。
type InjectionResult struct {
	Text                  string           `json:"text"`
	Memories              []InjectedMemory `json:"memories"`
	EstimatedTokens       int              `json:"estimated_tokens"`
	FeedbackCount         int              `json:"feedback_count"`
	RegularCount          int              `json:"regular_count"`
	SkippedExpired        int              `json:"skipped_expired"`
	SkippedLowTrust       int              `json:"skipped_low_trust"`
	SkippedCrossUser      int              `json:"skipped_cross_user"`
	SkippedScope          int              `json:"skipped_scope"`
	SkippedLowScore       int              `json:"skipped_low_score"`
	SkippedTokenBudget    int              `json:"skipped_token_budget"`
	SkippedFeedbackBudget int              `json:"skipped_feedback_budget"`
	SkippedRegularBudget  int              `json:"skipped_regular_budget"`
	SkippedMemoryIDs      []int64          `json:"skipped_memory_ids,omitempty"`
}

// MemoryIDs 返回实际注入的 memory id 列表。
func (r InjectionResult) MemoryIDs() []int64 {
	if len(r.Memories) == 0 {
		return nil
	}
	ids := make([]int64, 0, len(r.Memories))
	for _, mem := range r.Memories {
		if mem.ID != 0 {
			ids = append(ids, mem.ID)
		}
	}
	return ids
}

// SkippedTotal 返回因治理策略未进入 prompt 的 memory 总数。
func (r InjectionResult) SkippedTotal() int {
	return r.SkippedExpired + r.SkippedLowTrust + r.SkippedCrossUser + r.SkippedScope + r.SkippedLowScore + r.SkippedTokenBudget
}

// HasSignal 判断本次检索是否产生了可观测信息，包括注入或治理过滤。
func (r InjectionResult) HasSignal() bool {
	return len(r.Memories) > 0 || r.SkippedTotal() > 0
}
