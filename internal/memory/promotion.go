package memory

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

const PromotionSubjectPrefix = "memory_promotion"

type MemoryPromotionTargetType string

const (
	MemoryPromotionTargetProcedural MemoryPromotionTargetType = "procedural"
)

type MemoryPromotionSourceKind string

const (
	MemoryPromotionSourceFeedback    MemoryPromotionSourceKind = "feedback"
	MemoryPromotionSourceToolPattern MemoryPromotionSourceKind = "tool_pattern"
	MemoryPromotionSourceFailure     MemoryPromotionSourceKind = "failure_experience"
)

type MemoryPromotionCandidate struct {
	SubjectID                string                    `json:"subject_id"`
	TargetType               MemoryPromotionTargetType `json:"target_type"`
	ProposedProceduralMemory MemoryRecord              `json:"proposed_procedural_memory"`
	Rationale                string                    `json:"rationale"`
	SourceMemoryIDs          []int64                   `json:"source_memory_ids"`
	SourceKind               MemoryPromotionSourceKind `json:"source_kind"`
	Confidence               float64                   `json:"confidence,omitempty"`
	CreatedAt                time.Time                 `json:"created_at"`
}

type MemoryPromotionApplyOptions struct {
	Now        time.Time
	ApprovalID string
	AppliedBy  string
}

type MemoryPromotionAudit struct {
	SubjectID       string                    `json:"subject_id,omitempty"`
	ApprovalID      string                    `json:"approval_id,omitempty"`
	AppliedBy       string                    `json:"applied_by,omitempty"`
	AppliedAt       time.Time                 `json:"applied_at,omitempty"`
	SourceMemoryIDs []int64                   `json:"source_memory_ids,omitempty"`
	SourceKind      MemoryPromotionSourceKind `json:"source_kind,omitempty"`
}

type MemoryPromotionOptions struct {
	Now           time.Time
	UserID        string
	Limit         int
	MinConfidence float64
}

func GenerateMemoryPromotionCandidates(records []MemoryRecord, opts MemoryPromotionOptions) []MemoryPromotionCandidate {
	if opts.Now.IsZero() {
		opts.Now = time.Now()
	}
	if opts.Limit <= 0 {
		opts.Limit = 20
	}
	if opts.MinConfidence <= 0 {
		opts.MinConfidence = 0.75
	}

	candidates := make([]MemoryPromotionCandidate, 0)
	seen := map[string]bool{}
	for _, record := range records {
		if opts.UserID != "" && record.UserID != "" && record.UserID != opts.UserID {
			continue
		}
		if record.Type == MemoryTypeProcedural {
			continue
		}
		content := strings.TrimSpace(record.Content)
		if content == "" {
			continue
		}
		gov := DecodeGovernance(record.Metadata)
		if gov.Confidence > 0 && gov.Confidence < opts.MinConfidence {
			continue
		}
		sourceKind, ok := classifyPromotionSource(record)
		if !ok {
			continue
		}
		if MemoryPromotionSubjectIDFromRecord(record) != "" {
			continue
		}
		proposed := buildProceduralMemory(record, sourceKind, gov, opts.Now)
		normalized := normalizePromotionContent(proposed.Content)
		if normalized == "" || seen[normalized] {
			continue
		}
		seen[normalized] = true
		confidence := gov.Confidence
		if confidence == 0 {
			confidence = defaultPromotionConfidence(sourceKind)
		}
		subjectID := MemoryPromotionSubjectID(sourceKind, []int64{record.ID}, proposed.Content)
		candidates = append(candidates, MemoryPromotionCandidate{
			SubjectID:                subjectID,
			TargetType:               MemoryPromotionTargetProcedural,
			ProposedProceduralMemory: proposed,
			Rationale:                promotionRationale(record, sourceKind, gov),
			SourceMemoryIDs:          []int64{record.ID},
			SourceKind:               sourceKind,
			Confidence:               confidence,
			CreatedAt:                opts.Now,
		})
	}

	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].Confidence != candidates[j].Confidence {
			return candidates[i].Confidence > candidates[j].Confidence
		}
		left := candidates[i].ProposedProceduralMemory.UpdatedAt
		right := candidates[j].ProposedProceduralMemory.UpdatedAt
		if !left.Equal(right) {
			return left.After(right)
		}
		return candidates[i].SubjectID < candidates[j].SubjectID
	})
	if len(candidates) > opts.Limit {
		candidates = candidates[:opts.Limit]
	}
	return candidates
}

func MemoryPromotionSubjectID(kind MemoryPromotionSourceKind, sourceIDs []int64, content string) string {
	ids := append([]int64(nil), sourceIDs...)
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	parts := make([]string, 0, len(ids)+2)
	parts = append(parts, string(kind))
	for _, id := range ids {
		parts = append(parts, fmt.Sprintf("%d", id))
	}
	parts = append(parts, normalizePromotionContent(content))
	sum := sha1.Sum([]byte(strings.Join(parts, "|")))
	return PromotionSubjectPrefix + "_" + hex.EncodeToString(sum[:])[:16]
}

func BuildAppliedMemoryPromotion(candidate MemoryPromotionCandidate, opts MemoryPromotionApplyOptions) MemoryRecord {
	if opts.Now.IsZero() {
		opts.Now = time.Now()
	}
	record := candidate.ProposedProceduralMemory
	record.ID = 0
	record.Type = MemoryTypeProcedural
	record.CreatedAt = opts.Now
	record.UpdatedAt = opts.Now
	record.AccessedAt = time.Time{}
	record.AccessCount = 0

	gov := DecodeGovernance(record.Metadata)
	gov.Source = "memory_promotion_applied"
	gov.Evidence = firstNonEmpty(gov.Evidence, candidate.Rationale)
	record.Metadata = EncodeGovernance(record.Metadata, gov)

	meta := decodeMetadataMap(record.Metadata)
	meta["promotion"] = MemoryPromotionAudit{
		SubjectID:       candidate.SubjectID,
		ApprovalID:      opts.ApprovalID,
		AppliedBy:       strings.TrimSpace(opts.AppliedBy),
		AppliedAt:       opts.Now,
		SourceMemoryIDs: append([]int64(nil), candidate.SourceMemoryIDs...),
		SourceKind:      candidate.SourceKind,
	}
	record.Metadata = mustMarshalRaw(meta)
	return record
}

func DecodeMemoryPromotionAudit(raw json.RawMessage) MemoryPromotionAudit {
	meta := decodeMetadataMap(raw)
	v, ok := meta["promotion"]
	if !ok {
		return MemoryPromotionAudit{}
	}
	b, _ := json.Marshal(v)
	var audit MemoryPromotionAudit
	_ = json.Unmarshal(b, &audit)
	return audit
}

func MemoryPromotionSubjectIDFromRecord(record MemoryRecord) string {
	return strings.TrimSpace(DecodeMemoryPromotionAudit(record.Metadata).SubjectID)
}

func classifyPromotionSource(record MemoryRecord) (MemoryPromotionSourceKind, bool) {
	meta := decodeMetadataMap(record.Metadata)
	sourceType := strings.ToLower(metadataString(meta, "promotion_source"))
	if sourceType == "" {
		sourceType = strings.ToLower(metadataString(meta, "source_kind"))
	}
	switch sourceType {
	case string(MemoryPromotionSourceToolPattern), "tool":
		return MemoryPromotionSourceToolPattern, true
	case string(MemoryPromotionSourceFailure), "failure":
		return MemoryPromotionSourceFailure, true
	case string(MemoryPromotionSourceFeedback):
		return MemoryPromotionSourceFeedback, true
	}

	if record.Type == MemoryTypeFeedback {
		return MemoryPromotionSourceFeedback, true
	}
	lower := strings.ToLower(record.Content)
	switch {
	case containsAnyKeyword(lower, []string{"tool", "工具", "rg ", "grep", "fd ", "find ", "sed ", "go test", "npm run", "docker", "kubectl"}):
		return MemoryPromotionSourceToolPattern, true
	case containsAnyKeyword(lower, []string{"失败", "回归", "复盘", "blocked", "failure", "regression", "postmortem"}):
		return MemoryPromotionSourceFailure, true
	default:
		return "", false
	}
}

func buildProceduralMemory(record MemoryRecord, sourceKind MemoryPromotionSourceKind, gov Governance, now time.Time) MemoryRecord {
	content := strings.TrimSpace(record.Content)
	if !hasProcedurePrefix(content) {
		content = "当遇到相同模式时，" + content
	}
	tags := promotionTags(record.Tags, sourceKind)
	meta := EncodeGovernance(nil, Governance{
		Source:        "memory_promotion_candidate",
		Evidence:      firstNonEmpty(gov.Evidence, record.Content),
		Confidence:    firstNonZero(gov.Confidence, defaultPromotionConfidence(sourceKind)),
		ExtractedBy:   "memory_promotion_v1",
		SourceMessage: gov.SourceMessage,
		SourceUserID:  firstNonEmpty(gov.SourceUserID, record.UserID),
		RunID:         gov.RunID,
	})
	meta = copyPromotionMetadata(meta, record.Metadata)
	proposed := MemoryRecord{
		UserID:    record.UserID,
		Type:      MemoryTypeProcedural,
		Content:   content,
		Tags:      tags,
		SessionID: record.SessionID,
		Metadata:  meta,
		CreatedAt: now,
		UpdatedAt: now,
	}
	return proposed
}

func copyPromotionMetadata(dst json.RawMessage, src json.RawMessage) json.RawMessage {
	meta := decodeMetadataMap(dst)
	sourceMeta := decodeMetadataMap(src)
	if target, ok := sourceMeta["target"].(map[string]any); ok {
		meta["target"] = target
	}
	if runtime, ok := sourceMeta["runtime"]; ok {
		meta["runtime"] = runtime
	}
	meta["kind"] = string(MemoryKind("procedural"))
	meta["subject_type"] = "procedure"
	encoded, _ := json.Marshal(meta)
	return encoded
}

func promotionTags(existing []string, sourceKind MemoryPromotionSourceKind) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(existing)+3)
	for _, tag := range existing {
		tag = strings.TrimSpace(tag)
		if tag == "" || seen[tag] {
			continue
		}
		seen[tag] = true
		out = append(out, tag)
	}
	for _, tag := range []string{"memory-promotion", "procedural-candidate", string(sourceKind)} {
		if !seen[tag] {
			seen[tag] = true
			out = append(out, tag)
		}
	}
	return out
}

func promotionRationale(record MemoryRecord, sourceKind MemoryPromotionSourceKind, gov Governance) string {
	source := firstNonEmpty(gov.Source, string(sourceKind))
	return fmt.Sprintf("source memory %d (%s) is reusable as procedural guidance; source=%s", record.ID, record.Type, source)
}

func defaultPromotionConfidence(kind MemoryPromotionSourceKind) float64 {
	switch kind {
	case MemoryPromotionSourceFeedback:
		return 0.85
	case MemoryPromotionSourceToolPattern:
		return 0.8
	case MemoryPromotionSourceFailure:
		return 0.78
	default:
		return 0.75
	}
}

func hasProcedurePrefix(content string) bool {
	lower := strings.ToLower(strings.TrimSpace(content))
	return strings.HasPrefix(lower, "当") ||
		strings.HasPrefix(lower, "遇到") ||
		strings.HasPrefix(lower, "before ") ||
		strings.HasPrefix(lower, "when ") ||
		strings.HasPrefix(lower, "always ") ||
		strings.HasPrefix(lower, "先") ||
		strings.HasPrefix(lower, "必须")
}

func normalizePromotionContent(content string) string {
	return strings.Join(strings.Fields(strings.ToLower(strings.TrimSpace(content))), " ")
}

func metadataString(meta map[string]any, key string) string {
	v, _ := meta[key].(string)
	return strings.TrimSpace(v)
}

func firstNonZero(values ...float64) float64 {
	for _, value := range values {
		if value != 0 {
			return value
		}
	}
	return 0
}
