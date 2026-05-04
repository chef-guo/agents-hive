package sessiontodo

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBuildSpecProgressPatchUsesLatestCompletedProjectedTodo(t *testing.T) {
	patch, ok := BuildSpecProgressPatch(Snapshot{
		SessionID:   "sess-spec",
		PlanVersion: 4,
		Todos: []Todo{
			{ID: "manual", Content: "人工事项", Status: TodoStatusCompleted},
			{ID: "spec:harden-spec:1.1", Status: TodoStatusCompleted, Source: SourceSpecProjected, SourceChangeID: "harden-spec", SourceRevision: 2},
			{ID: "spec:harden-spec:1.2", Status: TodoStatusInProgress, Source: SourceSpecProjected, SourceChangeID: "harden-spec", SourceRevision: 2},
			{ID: "spec:harden-spec:1.3", Status: TodoStatusPending, Source: SourceSpecProjected, SourceChangeID: "harden-spec", SourceRevision: 2},
		},
	})

	require.True(t, ok)
	require.Equal(t, "sess-spec", patch.SessionID)
	require.Equal(t, "harden-spec", patch.ChangeID)
	require.Equal(t, "1.1", patch.LastTaskKey)
	require.Equal(t, int64(2), patch.SourceRevision)
	require.Equal(t, int64(4), patch.PlanVersion)
}

func TestBuildSpecProgressPatchPicksHighestTaskKey(t *testing.T) {
	patch, ok := BuildSpecProgressPatch(Snapshot{
		SessionID: "sess-spec",
		Todos: []Todo{
			{ID: "spec:harden-spec:1.10", Status: TodoStatusCompleted, Source: SourceSpecProjected, SourceChangeID: "harden-spec", SourceRevision: 2},
			{ID: "spec:harden-spec:1.2", Status: TodoStatusCompleted, Source: SourceSpecProjected, SourceChangeID: "harden-spec", SourceRevision: 2},
		},
	})

	require.True(t, ok)
	require.Equal(t, "1.10", patch.LastTaskKey)
}

func TestBuildSpecProgressPatchIgnoresMixedChangesAndManualTodos(t *testing.T) {
	_, ok := BuildSpecProgressPatch(Snapshot{
		SessionID: "sess-spec",
		Todos: []Todo{
			{ID: "manual", Status: TodoStatusCompleted},
			{ID: "spec:a:1.1", Status: TodoStatusCompleted, Source: SourceSpecProjected, SourceChangeID: "a", SourceRevision: 1},
			{ID: "spec:b:1.2", Status: TodoStatusCompleted, Source: SourceSpecProjected, SourceChangeID: "b", SourceRevision: 1},
		},
	})

	require.False(t, ok, "多个 change 混在一个 snapshot 时不能自动反写 spec")
}
