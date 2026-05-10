package memory

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"time"
)

type GovernanceStats struct {
	Total             int `json:"total"`
	MissingGovernance int `json:"missing_governance"`
	Expired           int `json:"expired"`
	LowConfidence     int `json:"low_confidence"`
	CrossUserRisk     int `json:"cross_user_risk"`
}

type GovernancePruneOptions struct {
	Now           time.Time
	MinConfidence float64
	MaxMemories   int
}

type GovernanceQueryOptions struct {
	Search        SearchOptions
	Now           time.Time
	MinConfidence float64
}

type GovernancePrunePlan struct {
	DeleteIDs []int64          `json:"delete_ids"`
	Reasons   map[int64]string `json:"reasons"`
}

type GovernancePruneResult struct {
	DryRun    bool             `json:"dry_run"`
	Matched   int              `json:"matched"`
	Deleted   int              `json:"deleted"`
	DeleteIDs []int64          `json:"delete_ids"`
	Reasons   map[int64]string `json:"reasons"`
}

type GovernanceStore interface {
	GovernanceStats(ctx context.Context, opts GovernanceQueryOptions) (GovernanceStats, error)
	PruneGovernance(ctx context.Context, plan GovernancePrunePlan) (GovernancePruneResult, error)
}

func PreserveGovernanceOnUpdate(existing json.RawMessage, incoming json.RawMessage) json.RawMessage {
	if len(incoming) == 0 {
		return cloneRawMessage(existing)
	}
	existingGovernance := DecodeGovernance(existing)
	if existingGovernance == (Governance{}) {
		return cloneRawMessage(incoming)
	}
	if DecodeGovernance(incoming) != (Governance{}) {
		return cloneRawMessage(incoming)
	}

	var merged map[string]any
	if len(existing) > 0 {
		_ = json.Unmarshal(existing, &merged)
	}
	if merged == nil {
		merged = map[string]any{}
	}
	var incomingMap map[string]any
	_ = json.Unmarshal(incoming, &incomingMap)
	for k, v := range incomingMap {
		merged[k] = v
	}
	return EncodeGovernance(mustMarshalRaw(merged), existingGovernance)
}

func GovernanceStatsForStore(ctx context.Context, store MemoryStore, opts GovernanceQueryOptions) (GovernanceStats, error) {
	if governanceStore, ok := store.(GovernanceStore); ok {
		return governanceStore.GovernanceStats(ctx, opts)
	}
	search := opts.Search
	if search.Limit <= 0 {
		search.Limit = 1000
	}
	result, err := store.List(ctx, search)
	if err != nil {
		return GovernanceStats{}, err
	}
	return AnalyzeGovernance(result.Memories, opts.Now, opts.MinConfidence), nil
}

func PruneGovernanceForStore(ctx context.Context, store MemoryStore, plan GovernancePrunePlan, dryRun bool) (GovernancePruneResult, error) {
	result := GovernancePruneResult{
		DryRun:    dryRun,
		Matched:   len(plan.DeleteIDs),
		DeleteIDs: append([]int64(nil), plan.DeleteIDs...),
		Reasons:   plan.Reasons,
	}
	if dryRun {
		return result, nil
	}
	if governanceStore, ok := store.(GovernanceStore); ok {
		return governanceStore.PruneGovernance(ctx, plan)
	}
	for _, id := range plan.DeleteIDs {
		if err := store.Delete(ctx, id); err != nil {
			return result, err
		}
		result.Deleted++
	}
	return result, nil
}

func AnalyzeGovernance(records []MemoryRecord, now time.Time, minConfidence float64) GovernanceStats {
	if now.IsZero() {
		now = time.Now()
	}
	stats := GovernanceStats{Total: len(records)}
	for _, rec := range records {
		g := DecodeGovernance(rec.Metadata)
		if g == (Governance{}) {
			stats.MissingGovernance++
			continue
		}
		if !g.ExpiresAt.IsZero() && now.After(g.ExpiresAt) {
			stats.Expired++
		}
		if minConfidence > 0 && g.Confidence > 0 && g.Confidence < minConfidence {
			stats.LowConfidence++
		}
		if rec.UserID != "" && g.SourceUserID != "" && rec.UserID != g.SourceUserID {
			stats.CrossUserRisk++
		}
	}
	return stats
}

func PlanGovernancePrune(records []MemoryRecord, opts GovernancePruneOptions) GovernancePrunePlan {
	if opts.Now.IsZero() {
		opts.Now = time.Now()
	}
	plan := GovernancePrunePlan{Reasons: map[int64]string{}}
	keep := make([]MemoryRecord, 0, len(records))
	seenDelete := map[int64]bool{}
	for _, rec := range records {
		g := DecodeGovernance(rec.Metadata)
		if isManualHighConfidence(g) {
			keep = append(keep, rec)
			continue
		}
		if !g.ExpiresAt.IsZero() && opts.Now.After(g.ExpiresAt) {
			addPrune(&plan, seenDelete, rec.ID, "expired")
			continue
		}
		if opts.MinConfidence > 0 && g.Confidence > 0 && g.Confidence < opts.MinConfidence {
			addPrune(&plan, seenDelete, rec.ID, "low_confidence")
			continue
		}
		if rec.UserID != "" && g.SourceUserID != "" && rec.UserID != g.SourceUserID {
			addPrune(&plan, seenDelete, rec.ID, "cross_user_risk")
			continue
		}
		keep = append(keep, rec)
	}
	if opts.MaxMemories > 0 && len(keep) > opts.MaxMemories {
		capacityCandidates := make([]MemoryRecord, 0, len(keep))
		manualProtected := 0
		for _, rec := range keep {
			if isManualHighConfidence(DecodeGovernance(rec.Metadata)) {
				manualProtected++
				continue
			}
			capacityCandidates = append(capacityCandidates, rec)
		}
		allowedCandidates := opts.MaxMemories - manualProtected
		if allowedCandidates < 0 {
			allowedCandidates = 0
		}
		if len(capacityCandidates) <= allowedCandidates {
			return plan
		}
		keep = capacityCandidates
		sort.SliceStable(keep, func(i, j int) bool {
			if keep[i].AccessCount != keep[j].AccessCount {
				return keep[i].AccessCount > keep[j].AccessCount
			}
			return keep[i].UpdatedAt.After(keep[j].UpdatedAt)
		})
		for _, rec := range keep[allowedCandidates:] {
			addPrune(&plan, seenDelete, rec.ID, "capacity")
		}
	}
	return plan
}

func isManualHighConfidence(g Governance) bool {
	if g.Confidence < 0.9 {
		return false
	}
	source := strings.ToLower(strings.TrimSpace(g.Source))
	return source == "manual" || source == "human" || source == "admin"
}

func addPrune(plan *GovernancePrunePlan, seen map[int64]bool, id int64, reason string) {
	if id == 0 || seen[id] {
		return
	}
	seen[id] = true
	plan.DeleteIDs = append(plan.DeleteIDs, id)
	plan.Reasons[id] = reason
}

func cloneRawMessage(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return nil
	}
	return append(json.RawMessage(nil), raw...)
}

func mustMarshalRaw(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}
