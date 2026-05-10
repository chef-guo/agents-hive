package memory

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestAnalyzeGovernanceCountsRiskBuckets(t *testing.T) {
	now := time.Now()
	records := []MemoryRecord{
		{ID: 1, UserID: "u1", Metadata: EncodeGovernance(nil, Governance{Confidence: 0.9, ExpiresAt: now.Add(time.Hour), SourceUserID: "u1"})},
		{ID: 2},
		{ID: 3, Metadata: EncodeGovernance(nil, Governance{Confidence: 0.2, ExpiresAt: now.Add(time.Hour)})},
		{ID: 4, Metadata: EncodeGovernance(nil, Governance{Confidence: 0.9, ExpiresAt: now.Add(-time.Hour)})},
		{ID: 5, UserID: "u1", Metadata: EncodeGovernance(nil, Governance{Confidence: 0.9, SourceUserID: "u2"})},
	}

	stats := AnalyzeGovernance(records, now, 0.5)

	assert.Equal(t, 5, stats.Total)
	assert.Equal(t, 1, stats.MissingGovernance)
	assert.Equal(t, 1, stats.LowConfidence)
	assert.Equal(t, 1, stats.Expired)
	assert.Equal(t, 1, stats.CrossUserRisk)
}

func TestPlanPruneGovernanceDryRun(t *testing.T) {
	now := time.Now()
	records := []MemoryRecord{
		{ID: 1, UpdatedAt: now.Add(-time.Hour), AccessCount: 10, Metadata: EncodeGovernance(nil, Governance{Confidence: 0.9, ExpiresAt: now.Add(time.Hour)})},
		{ID: 2, UpdatedAt: now.Add(-2 * time.Hour), AccessCount: 1, Metadata: EncodeGovernance(nil, Governance{Confidence: 0.9, ExpiresAt: now.Add(-time.Hour)})},
		{ID: 3, UpdatedAt: now.Add(-3 * time.Hour), AccessCount: 0, Metadata: EncodeGovernance(nil, Governance{Confidence: 0.9, ExpiresAt: now.Add(time.Hour)})},
	}

	plan := PlanGovernancePrune(records, GovernancePruneOptions{Now: now, MaxMemories: 1})

	assert.ElementsMatch(t, []int64{2, 3}, plan.DeleteIDs)
	assert.Contains(t, plan.Reasons[2], "expired")
	assert.Contains(t, plan.Reasons[3], "capacity")
}

func TestPlanPruneGovernanceHandlesCrossUserAndPreservesManualHighConfidence(t *testing.T) {
	now := time.Now()
	records := []MemoryRecord{
		{ID: 1, UserID: "u1", Metadata: EncodeGovernance(nil, Governance{SourceUserID: "u2", Confidence: 0.9})},
		{ID: 2, UpdatedAt: now.Add(-10 * time.Hour), AccessCount: 0, Metadata: EncodeGovernance(nil, Governance{Source: "manual", Confidence: 0.95})},
		{ID: 3, UpdatedAt: now.Add(-9 * time.Hour), AccessCount: 0, Metadata: EncodeGovernance(nil, Governance{Source: "summary", Confidence: 0.9})},
	}

	plan := PlanGovernancePrune(records, GovernancePruneOptions{Now: now, MaxMemories: 1})

	assert.Contains(t, plan.DeleteIDs, int64(1))
	assert.Equal(t, "cross_user_risk", plan.Reasons[1])
	assert.NotContains(t, plan.DeleteIDs, int64(2))
	assert.Contains(t, plan.DeleteIDs, int64(3))
}

func TestPreserveGovernanceOnUpdateKeepsExistingGovernance(t *testing.T) {
	existing := EncodeGovernance(json.RawMessage(`{"origin":"extractor"}`), Governance{
		Source:     "summary",
		Confidence: 0.9,
	})

	got := PreserveGovernanceOnUpdate(existing, json.RawMessage(`{"operator":"human"}`))

	assert.Equal(t, "summary", DecodeGovernance(got).Source)
	assert.JSONEq(t, `{"origin":"extractor","operator":"human","governance":{"source":"summary","confidence":0.9}}`, string(got))
}

func TestPreserveGovernanceOnUpdateKeepsIncomingVectorSpace(t *testing.T) {
	existing := EncodeGovernance(json.RawMessage(`{"origin":"extractor"}`), Governance{
		Source:     "summary",
		Confidence: 0.9,
	})
	incoming := EncodeVectorSpace(json.RawMessage(`{"operator":"human"}`), VectorSpaceMetadata{
		Name:           "memory:v2",
		EmbeddingState: EmbeddingStatePending,
	})

	got := PreserveGovernanceOnUpdate(existing, incoming)

	assert.Equal(t, "summary", DecodeGovernance(got).Source)
	assert.Equal(t, "memory:v2", DecodeVectorSpace(got).Name)
	assert.Equal(t, EmbeddingStatePending, DecodeVectorSpace(got).EmbeddingState)
}

func TestPreserveGovernanceOnUpdateUsesExistingMetadataWhenIncomingMissing(t *testing.T) {
	existing := EncodeGovernance(json.RawMessage(`{"origin":"extractor"}`), Governance{
		Source:     "summary",
		Confidence: 0.9,
	})

	got := PreserveGovernanceOnUpdate(existing, nil)

	assert.JSONEq(t, string(existing), string(got))
}

func TestGovernanceStatsForStoreFallbackCountsRiskBuckets(t *testing.T) {
	now := time.Now()
	store := &governanceFallbackStore{records: []MemoryRecord{
		{ID: 1, Metadata: EncodeGovernance(nil, Governance{Confidence: 0.9, ExpiresAt: now.Add(time.Hour)})},
		{ID: 2},
		{ID: 3, Metadata: EncodeGovernance(nil, Governance{Confidence: 0.2, ExpiresAt: now.Add(time.Hour)})},
		{ID: 4, Metadata: EncodeGovernance(nil, Governance{Confidence: 0.9, ExpiresAt: now.Add(-time.Hour)})},
	}}

	stats, err := GovernanceStatsForStore(context.Background(), store, GovernanceQueryOptions{
		Now:           now,
		MinConfidence: 0.5,
		Search:        SearchOptions{Limit: 100},
	})

	assert.NoError(t, err)
	assert.Equal(t, GovernanceStats{Total: 4, MissingGovernance: 1, Expired: 1, LowConfidence: 1}, stats)
	assert.Equal(t, 100, store.lastListOpts.Limit)
}

func TestPruneGovernanceForStoreFallbackDryRunAndExecute(t *testing.T) {
	store := &governanceFallbackStore{records: []MemoryRecord{
		{ID: 1},
		{ID: 2},
		{ID: 3},
	}}
	plan := GovernancePrunePlan{
		DeleteIDs: []int64{2, 3},
		Reasons:   map[int64]string{2: "expired", 3: "low_confidence"},
	}

	dryRun, err := PruneGovernanceForStore(context.Background(), store, plan, true)

	assert.NoError(t, err)
	assert.False(t, store.deleted[2])
	assert.Equal(t, GovernancePruneResult{DryRun: true, Matched: 2, Deleted: 0, DeleteIDs: []int64{2, 3}, Reasons: plan.Reasons}, dryRun)

	executed, err := PruneGovernanceForStore(context.Background(), store, plan, false)

	assert.NoError(t, err)
	assert.True(t, store.deleted[2])
	assert.True(t, store.deleted[3])
	assert.Equal(t, 2, executed.Deleted)
	assert.False(t, executed.DryRun)
}

type governanceFallbackStore struct {
	records      []MemoryRecord
	lastListOpts SearchOptions
	deleted      map[int64]bool
}

func (s *governanceFallbackStore) Save(context.Context, *MemoryRecord) (int64, error) { return 0, nil }
func (s *governanceFallbackStore) Get(context.Context, int64) (*MemoryRecord, error)  { return nil, nil }
func (s *governanceFallbackStore) Update(context.Context, *MemoryRecord) error        { return nil }
func (s *governanceFallbackStore) Search(context.Context, SearchOptions) (*SearchResult, error) {
	return nil, nil
}
func (s *governanceFallbackStore) List(_ context.Context, opts SearchOptions) (*SearchResult, error) {
	s.lastListOpts = opts
	return &SearchResult{Memories: s.records, Total: len(s.records)}, nil
}
func (s *governanceFallbackStore) Stats(context.Context) (*MemoryStats, error) { return nil, nil }
func (s *governanceFallbackStore) SetEmbedding(EmbeddingProvider, VectorStore) {}
func (s *governanceFallbackStore) Close() error                                { return nil }
func (s *governanceFallbackStore) Delete(_ context.Context, id int64) error {
	if s.deleted == nil {
		s.deleted = map[int64]bool{}
	}
	s.deleted[id] = true
	return nil
}
