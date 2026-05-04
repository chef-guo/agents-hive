package sessiontodo

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPlanResumeActionAllowsPendingTodos(t *testing.T) {
	action := PlanResumeAction(Snapshot{
		SessionID:  "sess-resume",
		PlanStatus: PlanStatusPaused,
		Todos: []Todo{
			{ID: "done", Status: TodoStatusCompleted},
			{ID: "next", Content: "继续实现", Status: TodoStatusPending},
		},
	}, ResumeOptions{Mode: ResumeModeManual, BudgetOK: true})

	require.True(t, action.Allowed)
	require.Equal(t, ResumeModeManual, action.Mode)
	require.Contains(t, action.Prompt, "继续实现")
	require.Equal(t, []string{"next"}, action.PendingTodoIDs)
}

func TestPlanResumeActionRejectsInvalidState(t *testing.T) {
	action := PlanResumeAction(Snapshot{
		SessionID:  "sess-resume",
		PlanStatus: PlanStatusCompleted,
		Todos:      []Todo{{ID: "done", Status: TodoStatusCompleted}},
	}, ResumeOptions{Mode: ResumeModeManual, BudgetOK: true})

	require.False(t, action.Allowed)
	require.Contains(t, action.Reason, "not paused")

	action = PlanResumeAction(Snapshot{
		SessionID:  "sess-resume",
		PlanStatus: PlanStatusPaused,
		Todos:      []Todo{{ID: "done", Status: TodoStatusCompleted}},
	}, ResumeOptions{Mode: ResumeModeManual, BudgetOK: true})
	require.False(t, action.Allowed)
	require.Contains(t, action.Reason, "no pending")
}

func TestPlanResumeActionRejectsAutoContinueWhenBudgetInsufficient(t *testing.T) {
	action := PlanResumeAction(Snapshot{
		SessionID:  "sess-resume",
		PlanStatus: PlanStatusPaused,
		Todos:      []Todo{{ID: "next", Content: "继续实现", Status: TodoStatusPending}},
	}, ResumeOptions{Mode: ResumeModeAuto, BudgetOK: false})

	require.False(t, action.Allowed)
	require.Contains(t, action.Reason, "budget")
}

func TestPlanResumeActionRejectsRuntimeEpochMismatch(t *testing.T) {
	action := PlanResumeAction(Snapshot{
		SessionID:    "sess-resume",
		PlanStatus:   PlanStatusPaused,
		RuntimeEpoch: "epoch-new",
		Todos:        []Todo{{ID: "next", Content: "继续实现", Status: TodoStatusPending}},
	}, ResumeOptions{Mode: ResumeModeManual, BudgetOK: true, RuntimeEpoch: "epoch-new", ExpectedRuntimeEpoch: "epoch-old"})

	require.False(t, action.Allowed)
	require.Contains(t, action.Reason, "epoch")
}

func TestPlanResumeActionRejectsExecuteWithoutExpectedRuntimeEpoch(t *testing.T) {
	action := PlanResumeAction(Snapshot{
		SessionID:    "sess-resume",
		PlanStatus:   PlanStatusPaused,
		RuntimeEpoch: "epoch-current",
		PlanVersion:  3,
		Todos:        []Todo{{ID: "next", Content: "继续实现", Status: TodoStatusPending}},
	}, ResumeOptions{Mode: ResumeModeManual, BudgetOK: true, RuntimeEpoch: "epoch-current", Execute: true})

	require.False(t, action.Allowed)
	require.Contains(t, action.Reason, "expected runtime epoch")
}

func TestPlanResumeActionIncludesSnapshotVersionAndEpoch(t *testing.T) {
	action := PlanResumeAction(Snapshot{
		SessionID:    "sess-resume",
		PlanStatus:   PlanStatusPaused,
		RuntimeEpoch: "epoch-current",
		PlanVersion:  3,
		Todos:        []Todo{{ID: "next", Content: "继续实现", Status: TodoStatusPending}},
	}, ResumeOptions{Mode: ResumeModeManual, BudgetOK: true, RuntimeEpoch: "epoch-current", ExpectedRuntimeEpoch: "epoch-current", Execute: true})

	require.True(t, action.Allowed)
	require.Equal(t, int64(3), action.PlanVersion)
	require.Equal(t, "epoch-current", action.RuntimeEpoch)
}
