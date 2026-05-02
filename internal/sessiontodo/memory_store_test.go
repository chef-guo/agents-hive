package sessiontodo

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
)

func TestMemoryStoreReplaceSnapshotAndCAS(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	st := NewMemoryStore()

	first, err := st.Replace(ctx, "session-a", 0, []TodoInput{
		{
			ID:               "read-context",
			SessionID:        "untrusted-session",
			Content:          "阅读上下文",
			Status:           TodoStatusCompleted,
			Order:            10,
			Source:           "agent",
			TraceID:          "trace-1",
			SpanID:           "span-1",
			SourceChangeID:   "change-1",
			SourceRevision:   7,
			SourceToolCallID: "call-1",
		},
		{
			ID:      "implement-store",
			Content: "实现存储",
			Status:  TodoStatusInProgress,
			Order:   20,
			Source:  "agent",
		},
	})
	if err != nil {
		t.Fatalf("initial replace: %v", err)
	}

	if first.SessionID != "session-a" {
		t.Fatalf("SessionID = %q, want caller session", first.SessionID)
	}
	if first.PlanVersion != 1 {
		t.Fatalf("PlanVersion = %d, want 1", first.PlanVersion)
	}
	if first.PlanStatus != PlanStatusNone {
		t.Fatalf("PlanStatus = %q, want %q", first.PlanStatus, PlanStatusNone)
	}
	if len(first.Todos) != 2 {
		t.Fatalf("len(Todos) = %d, want 2", len(first.Todos))
	}
	if first.Todos[0].SessionID != "session-a" {
		t.Fatalf("todo SessionID = %q, want caller session", first.Todos[0].SessionID)
	}
	if first.Todos[0].ID != "read-context" || first.Todos[0].Content != "阅读上下文" {
		t.Fatalf("unexpected first todo: %+v", first.Todos[0])
	}
	if first.Todos[0].Order != 10 {
		t.Fatalf("Order = %d, want input order 10", first.Todos[0].Order)
	}
	if first.Todos[0].Version != 1 {
		t.Fatalf("Version = %d, want 1", first.Todos[0].Version)
	}
	if first.Todos[0].TraceID != "trace-1" || first.Todos[0].SpanID != "span-1" || first.Todos[0].SourceToolCallID != "call-1" {
		t.Fatalf("source metadata not preserved: %+v", first.Todos[0])
	}

	got, err := st.Snapshot(ctx, "session-a")
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if got.PlanVersion != first.PlanVersion || len(got.Todos) != len(first.Todos) {
		t.Fatalf("snapshot mismatch: got %+v want %+v", got, first)
	}

	_, err = st.Replace(ctx, "session-a", 0, []TodoInput{
		{ID: "stale", Content: "旧版本写入", Status: TodoStatusPending},
	})
	if err == nil {
		t.Fatal("stale replace succeeded, want conflict")
	}
	if !errors.Is(err, ErrPlanVersionConflict) {
		t.Fatalf("error = %v, want ErrPlanVersionConflict", err)
	}
	var conflict *PlanVersionConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("error type = %T, want *PlanVersionConflictError", err)
	}
	if conflict.Expected != 0 || conflict.Got != 1 {
		t.Fatalf("conflict Expected/Got = %d/%d, want 0/1", conflict.Expected, conflict.Got)
	}

	second, err := st.Replace(ctx, "session-a", first.PlanVersion, []TodoInput{
		{ID: "implement-store", Content: "实现存储", Status: TodoStatusCompleted},
	})
	if err != nil {
		t.Fatalf("second replace: %v", err)
	}
	if second.PlanVersion != 2 {
		t.Fatalf("PlanVersion = %d, want 2", second.PlanVersion)
	}
	if len(second.Todos) != 1 || second.Todos[0].Version != 2 {
		t.Fatalf("second todos = %+v, want replacement at todo version 2", second.Todos)
	}

	other, err := st.Replace(ctx, "session-b", 0, []TodoInput{
		{ID: "other", Content: "另一个 session", Status: TodoStatusPending},
	})
	if err != nil {
		t.Fatalf("other session replace: %v", err)
	}
	if other.PlanVersion != 1 {
		t.Fatalf("other PlanVersion = %d, want per-session version 1", other.PlanVersion)
	}
}

func TestMemoryStoreSetPlanStatusAndClear(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	st := NewMemoryStore()

	snap, err := st.SetPlanStatus(ctx, "session-status", PlanStatusPlanning)
	if err != nil {
		t.Fatalf("set status before todos: %v", err)
	}
	if snap.PlanStatus != PlanStatusPlanning {
		t.Fatalf("PlanStatus = %q, want planning", snap.PlanStatus)
	}
	if snap.PlanVersion != 1 {
		t.Fatalf("PlanVersion = %d, want 1 when status changes", snap.PlanVersion)
	}

	snap, err = st.Replace(ctx, "session-status", snap.PlanVersion, []TodoInput{
		{ID: "todo-1", Content: "写入 todo", Status: TodoStatusPending},
	})
	if err != nil {
		t.Fatalf("replace: %v", err)
	}
	if snap.PlanStatus != PlanStatusPlanning {
		t.Fatalf("PlanStatus after replace = %q, want preserved planning", snap.PlanStatus)
	}

	snap, err = st.SetPlanStatus(ctx, "session-status", PlanStatusExecuting)
	if err != nil {
		t.Fatalf("set executing: %v", err)
	}
	if snap.PlanStatus != PlanStatusExecuting {
		t.Fatalf("PlanStatus = %q, want executing", snap.PlanStatus)
	}
	if snap.PlanVersion != 3 || len(snap.Todos) != 1 {
		t.Fatalf("status update should not rewrite todos/version: %+v", snap)
	}
	if snap.Todos[0].Version != 3 {
		t.Fatalf("todo Version = %d, want status snapshot version 3", snap.Todos[0].Version)
	}

	if err := st.Clear(ctx, "session-status"); err != nil {
		t.Fatalf("clear: %v", err)
	}
	cleared, err := st.Snapshot(ctx, "session-status")
	if err != nil {
		t.Fatalf("snapshot after clear: %v", err)
	}
	if cleared.PlanVersion != 0 || cleared.PlanStatus != PlanStatusNone || len(cleared.Todos) != 0 {
		t.Fatalf("cleared snapshot = %+v, want empty", cleared)
	}
}

func TestMemoryStoreRejectsInvalidInput(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	st := NewMemoryStore()

	if _, err := st.Replace(ctx, "", 0, []TodoInput{{ID: "x", Content: "x", Status: TodoStatusPending}}); err == nil {
		t.Fatal("empty sessionID accepted")
	}
	if _, err := st.Replace(ctx, "session-invalid", 0, []TodoInput{{ID: "x", Status: TodoStatusPending}}); err == nil {
		t.Fatal("empty content accepted")
	}
	if _, err := st.Replace(ctx, "session-invalid", 0, []TodoInput{{ID: "x", Content: "x", Status: TodoStatus("bad")}}); err == nil {
		t.Fatal("invalid todo status accepted")
	}
	if _, err := st.SetPlanStatus(ctx, "session-invalid", PlanStatus("bad")); err == nil {
		t.Fatal("invalid plan status accepted")
	}
	if _, err := st.Replace(ctx, "session-invalid", 0, []TodoInput{
		{Content: "自动生成 id", Status: TodoStatusPending},
		{ID: "todo-1", Content: "显式冲突 id", Status: TodoStatusPending},
	}); err == nil {
		t.Fatal("generated duplicate todo id accepted")
	}
}

func TestMemoryStoreConcurrentReplaceUsesCAS(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	st := NewMemoryStore()

	const writers = 32
	var wg sync.WaitGroup
	results := make(chan error, writers)

	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := st.Replace(ctx, "session-concurrent", 0, []TodoInput{
				{
					ID:      fmt.Sprintf("todo-%02d", i),
					Content: fmt.Sprintf("并发写入 %02d", i),
					Status:  TodoStatusPending,
				},
			})
			results <- err
		}(i)
	}

	wg.Wait()
	close(results)

	var successes, conflicts int
	for err := range results {
		if err == nil {
			successes++
			continue
		}
		if errors.Is(err, ErrPlanVersionConflict) {
			conflicts++
			continue
		}
		t.Fatalf("unexpected error: %v", err)
	}

	if successes != 1 {
		t.Fatalf("successes = %d, want exactly 1", successes)
	}
	if conflicts != writers-1 {
		t.Fatalf("conflicts = %d, want %d", conflicts, writers-1)
	}

	snap, err := st.Snapshot(ctx, "session-concurrent")
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if snap.PlanVersion != 1 || len(snap.Todos) != 1 {
		t.Fatalf("snapshot = %+v, want one successful replacement", snap)
	}
}
