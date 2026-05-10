package agentquality

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestApprovalPolicyAllowsLeadAndAdminButRejectsEngineer(t *testing.T) {
	assert.NoError(t, AuthorizeApprovalRole(ApprovalRoleAdmin))
	assert.NoError(t, AuthorizeApprovalRole(ApprovalRoleLead))
	assert.Error(t, AuthorizeApprovalRole(ApprovalRoleEngineer))
	assert.Error(t, AuthorizeApprovalRole(ApprovalRole("guest")))
}

func TestInMemoryApprovalStoreRecordsIndependentApproval(t *testing.T) {
	ctx := context.Background()
	store := NewInMemoryApprovalStore()
	now := time.Date(2026, 4, 30, 10, 0, 0, 0, time.UTC)

	rec, err := store.RecordApproval(ctx, ApprovalRecord{
		ID:           "approval-1",
		SubjectID:    "memory_promotion_abc",
		SubjectType:  ApprovalSubjectMemoryPromotion,
		Action:       ApprovalActionApprove,
		Reviewer:     "lead-1",
		ReviewerRole: ApprovalRoleLead,
		Note:         "指标通过",
		CreatedAt:    now,
	})
	require.NoError(t, err)
	assert.Equal(t, "approval-1", rec.ID)
	assert.Equal(t, ApprovalSubjectMemoryPromotion, rec.SubjectType)
	assert.Equal(t, ApprovalActionApprove, rec.Action)

	records, err := store.ListApprovals(ctx, "memory_promotion_abc")
	require.NoError(t, err)
	require.Len(t, records, 1)
	assert.Equal(t, "lead-1", records[0].Reviewer)
	assert.Equal(t, now, records[0].CreatedAt)

	_, err = store.RecordApproval(ctx, ApprovalRecord{
		ID:           "approval-2",
		SubjectID:    "diff-2",
		SubjectType:  ApprovalSubjectEvalDiff,
		Action:       ApprovalActionReject,
		Reviewer:     "engineer-1",
		ReviewerRole: ApprovalRoleEngineer,
	})
	assert.Error(t, err)
}
