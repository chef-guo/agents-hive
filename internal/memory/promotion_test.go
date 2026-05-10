package memory

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestGenerateMemoryPromotionCandidatesFromFeedback(t *testing.T) {
	now := time.Date(2026, 5, 9, 10, 0, 0, 0, time.UTC)
	records := []MemoryRecord{
		{
			ID:        101,
			UserID:    "u1",
			Type:      MemoryTypeFeedback,
			Content:   "下次遇到 Go 测试失败，必须先运行最小包级 go test 并读取完整失败输出",
			Tags:      []string{"feedback"},
			SessionID: "s1",
			Metadata: EncodeGovernance(json.RawMessage(`{"target":{"target_scope":"user","visibility":"private","user_id":"u1"}}`), Governance{
				Source:       "reflection",
				Evidence:     "用户纠正了没有读取失败输出的问题",
				Confidence:   0.92,
				SourceUserID: "u1",
			}),
			UpdatedAt: now.Add(-time.Hour),
		},
		{
			ID:      102,
			UserID:  "u1",
			Type:    MemoryTypeFeedback,
			Content: "低置信度反馈",
			Metadata: EncodeGovernance(nil, Governance{
				Confidence: 0.2,
			}),
		},
	}

	candidates := GenerateMemoryPromotionCandidates(records, MemoryPromotionOptions{
		Now:           now,
		UserID:        "u1",
		MinConfidence: 0.75,
	})

	require.Len(t, candidates, 1)
	got := candidates[0]
	require.NotEmpty(t, got.SubjectID)
	require.Equal(t, MemoryPromotionTargetProcedural, got.TargetType)
	require.Equal(t, MemoryPromotionSourceFeedback, got.SourceKind)
	require.Equal(t, []int64{101}, got.SourceMemoryIDs)
	require.Equal(t, MemoryTypeProcedural, got.ProposedProceduralMemory.Type)
	require.Equal(t, "u1", got.ProposedProceduralMemory.UserID)
	require.Contains(t, got.ProposedProceduralMemory.Content, "Go 测试失败")
	require.Contains(t, got.ProposedProceduralMemory.Tags, "memory-promotion")
	require.Contains(t, got.Rationale, "source memory 101")
	require.Equal(t, 0.92, got.Confidence)

	meta := decodeMetadataMap(got.ProposedProceduralMemory.Metadata)
	require.Equal(t, "procedural", meta["kind"])
	require.Equal(t, "procedure", meta["subject_type"])
	require.Contains(t, meta, "target")
	gov := DecodeGovernance(got.ProposedProceduralMemory.Metadata)
	require.Equal(t, "memory_promotion_candidate", gov.Source)
	require.Equal(t, "u1", gov.SourceUserID)
}

func TestGenerateMemoryPromotionCandidatesFromToolAndFailureSignals(t *testing.T) {
	now := time.Date(2026, 5, 9, 11, 0, 0, 0, time.UTC)
	records := []MemoryRecord{
		{
			ID:      201,
			UserID:  "u1",
			Type:    MemoryTypeEpisodic,
			Content: "go test ./internal/memory -run TestPromotion 能最快定位 promotion 回归",
			Metadata: EncodeGovernance(json.RawMessage(`{"promotion_source":"tool_pattern"}`), Governance{
				Confidence: 0.8,
			}),
			UpdatedAt: now.Add(-time.Minute),
		},
		{
			ID:      202,
			UserID:  "u1",
			Type:    MemoryTypeProject,
			Content: "复盘：没有把审批 subject_id 记录下来导致闭环不可审计",
			Metadata: EncodeGovernance(json.RawMessage(`{"promotion_source":"failure_experience"}`), Governance{
				Confidence: 0.81,
			}),
			UpdatedAt: now,
		},
		{
			ID:      203,
			UserID:  "u2",
			Type:    MemoryTypeFeedback,
			Content: "下次跨用户记忆不能晋升",
			Metadata: EncodeGovernance(nil, Governance{
				Confidence: 0.99,
			}),
		},
	}

	candidates := GenerateMemoryPromotionCandidates(records, MemoryPromotionOptions{
		Now:    now,
		UserID: "u1",
		Limit:  5,
	})

	require.Len(t, candidates, 2)
	require.Equal(t, []int64{202}, candidates[0].SourceMemoryIDs)
	require.Equal(t, MemoryPromotionSourceFailure, candidates[0].SourceKind)
	require.Equal(t, []int64{201}, candidates[1].SourceMemoryIDs)
	require.Equal(t, MemoryPromotionSourceToolPattern, candidates[1].SourceKind)
	require.Contains(t, candidates[1].ProposedProceduralMemory.Content, "go test")
}

func TestMemoryPromotionSubjectIDStable(t *testing.T) {
	first := MemoryPromotionSubjectID(MemoryPromotionSourceFeedback, []int64{2, 1}, " Always run go test ")
	second := MemoryPromotionSubjectID(MemoryPromotionSourceFeedback, []int64{1, 2}, "always   run   go test")

	require.Equal(t, first, second)
	require.Contains(t, first, PromotionSubjectPrefix+"_")
}

func TestGenerateMemoryPromotionCandidatesSkipsAppliedProceduralRecords(t *testing.T) {
	now := time.Date(2026, 5, 9, 12, 0, 0, 0, time.UTC)
	source := MemoryRecord{
		ID:      301,
		UserID:  "u1",
		Type:    MemoryTypeFeedback,
		Content: "下次处理 schema migration 时必须先 dry-run",
		Metadata: EncodeGovernance(nil, Governance{
			Confidence: 0.91,
		}),
	}
	candidate := GenerateMemoryPromotionCandidates([]MemoryRecord{source}, MemoryPromotionOptions{
		Now: now,
	})
	require.Len(t, candidate, 1)
	applied := BuildAppliedMemoryPromotion(candidate[0], MemoryPromotionApplyOptions{
		Now:        now,
		ApprovalID: "approval-1",
		AppliedBy:  "lead",
	})

	candidates := GenerateMemoryPromotionCandidates([]MemoryRecord{applied, source}, MemoryPromotionOptions{
		Now: now,
	})
	require.Len(t, candidates, 1)
	require.Equal(t, candidate[0].SubjectID, candidates[0].SubjectID)
	require.Equal(t, candidate[0].SubjectID, MemoryPromotionSubjectIDFromRecord(applied))
	require.Equal(t, "approval-1", DecodeMemoryPromotionAudit(applied.Metadata).ApprovalID)
}
