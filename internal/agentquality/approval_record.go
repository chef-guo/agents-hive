package agentquality

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

type ApprovalRole string

const (
	ApprovalRoleAdmin    ApprovalRole = "admin"
	ApprovalRoleEngineer ApprovalRole = "engineer"
	ApprovalRoleLead     ApprovalRole = "lead"
)

type ApprovalAction string

const (
	ApprovalActionApprove ApprovalAction = "approve"
	ApprovalActionReject  ApprovalAction = "reject"
)

type ApprovalSubjectType string

const (
	ApprovalSubjectEvalDiff        ApprovalSubjectType = "eval_diff"
	ApprovalSubjectSuggestion      ApprovalSubjectType = "suggestion"
	ApprovalSubjectMemoryPromotion ApprovalSubjectType = "memory_promotion"
)

type ApprovalRecord struct {
	ID           string              `json:"id"`
	SubjectID    string              `json:"subject_id"`
	SubjectType  ApprovalSubjectType `json:"subject_type"`
	Action       ApprovalAction      `json:"action"`
	Reviewer     string              `json:"reviewer"`
	ReviewerRole ApprovalRole        `json:"reviewer_role"`
	Note         string              `json:"note,omitempty"`
	CreatedAt    time.Time           `json:"created_at"`
}

type ApprovalStore interface {
	RecordApproval(ctx context.Context, rec ApprovalRecord) (*ApprovalRecord, error)
	ListApprovals(ctx context.Context, subjectID string) ([]ApprovalRecord, error)
}

type InMemoryApprovalStore struct {
	mu   sync.RWMutex
	rows map[string]ApprovalRecord
}

func NewInMemoryApprovalStore() *InMemoryApprovalStore {
	return &InMemoryApprovalStore{rows: map[string]ApprovalRecord{}}
}

func AuthorizeApprovalRole(role ApprovalRole) error {
	switch role {
	case ApprovalRoleAdmin, ApprovalRoleLead:
		return nil
	case ApprovalRoleEngineer:
		return fmt.Errorf("role %s cannot approve optimization changes", role)
	default:
		return fmt.Errorf("unknown approval role %q", role)
	}
}

func (s *InMemoryApprovalStore) RecordApproval(ctx context.Context, rec ApprovalRecord) (*ApprovalRecord, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s == nil {
		return nil, fmt.Errorf("approval store not configured")
	}
	if err := normalizeApprovalRecord(&rec); err != nil {
		return nil, err
	}
	if err := AuthorizeApprovalRole(rec.ReviewerRole); err != nil {
		return nil, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.rows == nil {
		s.rows = map[string]ApprovalRecord{}
	}
	s.rows[rec.ID] = rec
	out := rec
	return &out, nil
}

func (s *InMemoryApprovalStore) ListApprovals(ctx context.Context, subjectID string) ([]ApprovalRecord, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s == nil {
		return nil, fmt.Errorf("approval store not configured")
	}
	subjectID = strings.TrimSpace(subjectID)
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]ApprovalRecord, 0)
	for _, row := range s.rows {
		if subjectID != "" && row.SubjectID != subjectID {
			continue
		}
		out = append(out, row)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].ID < out[j].ID
		}
		return out[i].CreatedAt.Before(out[j].CreatedAt)
	})
	return out, nil
}

func normalizeApprovalRecord(rec *ApprovalRecord) error {
	rec.ID = strings.TrimSpace(rec.ID)
	rec.SubjectID = strings.TrimSpace(rec.SubjectID)
	rec.Reviewer = strings.TrimSpace(rec.Reviewer)
	rec.Note = strings.TrimSpace(rec.Note)
	if rec.ID == "" {
		return fmt.Errorf("approval id is required")
	}
	if rec.SubjectID == "" {
		return fmt.Errorf("approval subject id is required")
	}
	switch rec.SubjectType {
	case ApprovalSubjectEvalDiff, ApprovalSubjectSuggestion, ApprovalSubjectMemoryPromotion:
	default:
		return fmt.Errorf("invalid approval subject type %q", rec.SubjectType)
	}
	switch rec.Action {
	case ApprovalActionApprove, ApprovalActionReject:
	default:
		return fmt.Errorf("invalid approval action %q", rec.Action)
	}
	if rec.Reviewer == "" {
		return fmt.Errorf("approval reviewer is required")
	}
	if rec.CreatedAt.IsZero() {
		rec.CreatedAt = time.Now()
	}
	return nil
}
