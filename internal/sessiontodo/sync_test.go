package sessiontodo

import (
	"context"
	"testing"

	"github.com/chef-guo/agents-hive/internal/specdriven"
	"github.com/stretchr/testify/require"
)

func TestSyncFromSpecProjectsPlanStepsIntoTodos(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()

	snapshot, err := SyncFromSpec(ctx, store, SyncFromSpecInput{
		SessionID: "sess-spec",
		ChangeID:  "harden-spec",
		Revision:  2,
		Plan: specdriven.Plan{Steps: []specdriven.PlanStep{
			{TaskKey: "1.1", ToolName: "read_file"},
			{TaskKey: "1.2", ToolName: "edit"},
		}},
	})

	require.NoError(t, err)
	require.Equal(t, int64(1), snapshot.PlanVersion)
	require.Len(t, snapshot.Todos, 2)
	require.Equal(t, "spec:harden-spec:1.1", snapshot.Todos[0].ID)
	require.Equal(t, "1.1: read_file", snapshot.Todos[0].Content)
	require.Equal(t, TodoStatusPending, snapshot.Todos[0].Status)
	require.Equal(t, "spec_projected", snapshot.Todos[0].Source)
	require.Equal(t, "harden-spec", snapshot.Todos[0].SourceChangeID)
	require.Equal(t, int64(2), snapshot.Todos[0].SourceRevision)
}

func TestSyncFromSpecPreservesManualTodosAndExistingProjectedStatus(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	first, err := store.Replace(ctx, "sess-spec", 0, []TodoInput{
		{ID: "manual", Content: "人工补充事项", Status: TodoStatusInProgress},
		{ID: "spec:harden-spec:1.1", Content: "1.1: read_file", Status: TodoStatusCompleted, Source: "spec_projected", TurnID: "turn-old", RuntimeEpoch: "epoch-old", SourceChangeID: "harden-spec", SourceRevision: 1},
		{ID: "spec:harden-spec:old", Content: "old: grep", Status: TodoStatusPending, Source: "spec_projected", SourceChangeID: "harden-spec", SourceRevision: 1},
	})
	require.NoError(t, err)

	snapshot, err := SyncFromSpec(ctx, store, SyncFromSpecInput{
		SessionID: "sess-spec",
		ChangeID:  "harden-spec",
		Revision:  2,
		Plan: specdriven.Plan{Steps: []specdriven.PlanStep{
			{TaskKey: "1.1", ToolName: "read_file"},
			{TaskKey: "1.2", ToolName: "edit"},
		}},
	})

	require.NoError(t, err)
	require.Equal(t, first.PlanVersion+1, snapshot.PlanVersion)
	require.Len(t, snapshot.Todos, 4)
	require.Equal(t, "manual", snapshot.Todos[0].ID)
	require.Equal(t, TodoStatusInProgress, snapshot.Todos[0].Status)
	require.Equal(t, "spec:harden-spec:1.1", snapshot.Todos[1].ID)
	require.Equal(t, TodoStatusCompleted, snapshot.Todos[1].Status)
	require.Equal(t, "turn-old", snapshot.Todos[1].TurnID)
	require.Equal(t, "epoch-old", snapshot.Todos[1].RuntimeEpoch)
	require.Equal(t, int64(2), snapshot.Todos[1].SourceRevision)
	require.Equal(t, "spec:harden-spec:1.2", snapshot.Todos[2].ID)
	require.Equal(t, TodoStatusPending, snapshot.Todos[2].Status)
	require.Equal(t, "spec:harden-spec:old", snapshot.Todos[3].ID)
	require.Equal(t, TodoStatusCancelled, snapshot.Todos[3].Status)
}

func TestSyncFromSpecValidatesRequiredFields(t *testing.T) {
	_, err := SyncFromSpec(context.Background(), NewMemoryStore(), SyncFromSpecInput{})
	require.Error(t, err)
	require.ErrorIs(t, err, ErrInvalidInput)

	_, err = SyncFromSpec(context.Background(), NewMemoryStore(), SyncFromSpecInput{
		SessionID: "sess",
		ChangeID:  "change",
		Revision:  1,
		Plan:      specdriven.Plan{},
	})
	require.Error(t, err)
	require.ErrorIs(t, err, ErrInvalidInput)
}
