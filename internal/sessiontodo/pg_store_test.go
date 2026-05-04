package sessiontodo

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPGStoreReplaceSnapshotAndClear(t *testing.T) {
	ctx := context.Background()
	st, cleanup := setupPGStore(t, ctx)
	defer cleanup()

	sessionID := "sessiontodo-pg-" + time.Now().UTC().Format("20060102150405.000000000")
	createPGSession(t, ctx, st.pool, sessionID)

	first, err := st.Replace(ctx, sessionID, 0, []TodoInput{
		{
			ID:               "todo-a",
			SessionID:        "ignored",
			Content:          "PG 写入",
			Status:           TodoStatusPending,
			Order:            42,
			Source:           "tool",
			TraceID:          "trace-pg",
			SpanID:           "span-pg",
			SourceChangeID:   "change-pg",
			SourceRevision:   9,
			SourceToolCallID: "call-pg",
		},
	})
	if err != nil {
		t.Fatalf("initial replace: %v", err)
	}
	if first.PlanVersion != 1 || len(first.Todos) != 1 {
		t.Fatalf("first snapshot = %+v, want version 1 with one todo", first)
	}
	if first.Todos[0].SessionID != sessionID {
		t.Fatalf("todo SessionID = %q, want caller session", first.Todos[0].SessionID)
	}
	if first.Todos[0].Order != 42 || first.Todos[0].TraceID != "trace-pg" || first.Todos[0].SourceToolCallID != "call-pg" {
		t.Fatalf("metadata not preserved: %+v", first.Todos[0])
	}

	_, err = st.Replace(ctx, sessionID, 0, []TodoInput{
		{ID: "todo-b", Content: "旧版本", Status: TodoStatusPending},
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

	statusSnap, err := st.SetPlanStatus(ctx, sessionID, PlanStatusExecuting)
	if err != nil {
		t.Fatalf("set status: %v", err)
	}
	if statusSnap.PlanStatus != PlanStatusExecuting || statusSnap.PlanVersion != 2 {
		t.Fatalf("status snapshot = %+v, want executing at version 2", statusSnap)
	}

	second, err := st.Replace(ctx, sessionID, 2, []TodoInput{
		{ID: "todo-b", Content: "替换后", Status: TodoStatusCompleted},
	})
	if err != nil {
		t.Fatalf("second replace: %v", err)
	}
	if second.PlanVersion != 3 || second.PlanStatus != PlanStatusExecuting {
		t.Fatalf("second snapshot = %+v, want version 3 with preserved status", second)
	}
	if len(second.Todos) != 1 || second.Todos[0].ID != "todo-b" || second.Todos[0].Version != 3 {
		t.Fatalf("second todos = %+v, want replacement row at version 3", second.Todos)
	}

	if err := st.Clear(ctx, sessionID); err != nil {
		t.Fatalf("clear: %v", err)
	}
	cleared, err := st.Snapshot(ctx, sessionID)
	if err != nil {
		t.Fatalf("snapshot after clear: %v", err)
	}
	if cleared.PlanVersion != 0 || cleared.PlanStatus != PlanStatusNone || len(cleared.Todos) != 0 {
		t.Fatalf("cleared snapshot = %+v, want empty", cleared)
	}
}

func TestPGStorePlanVersionIsPerSession(t *testing.T) {
	ctx := context.Background()
	st, cleanup := setupPGStore(t, ctx)
	defer cleanup()

	suffix := time.Now().UTC().Format("20060102150405.000000000")
	sessionA := "sessiontodo-pg-a-" + suffix
	sessionB := "sessiontodo-pg-b-" + suffix
	createPGSession(t, ctx, st.pool, sessionA)
	createPGSession(t, ctx, st.pool, sessionB)

	if _, err := st.Replace(ctx, sessionA, 0, []TodoInput{{ID: "a1", Content: "A1", Status: TodoStatusPending}}); err != nil {
		t.Fatalf("session A first replace: %v", err)
	}
	a2, err := st.Replace(ctx, sessionA, 1, []TodoInput{{ID: "a2", Content: "A2", Status: TodoStatusPending}})
	if err != nil {
		t.Fatalf("session A second replace: %v", err)
	}
	b1, err := st.Replace(ctx, sessionB, 0, []TodoInput{{ID: "b1", Content: "B1", Status: TodoStatusPending}})
	if err != nil {
		t.Fatalf("session B first replace: %v", err)
	}

	if a2.PlanVersion != 2 {
		t.Fatalf("session A PlanVersion = %d, want 2", a2.PlanVersion)
	}
	if b1.PlanVersion != 1 {
		t.Fatalf("session B PlanVersion = %d, want independent 1", b1.PlanVersion)
	}
}

func TestPGStoreSetPlanStatusBeforeTodosCanRepeat(t *testing.T) {
	ctx := context.Background()
	st, cleanup := setupPGStore(t, ctx)
	defer cleanup()

	sessionID := "sessiontodo-pg-status-" + time.Now().UTC().Format("20060102150405.000000000")
	createPGSession(t, ctx, st.pool, sessionID)

	first, err := st.SetPlanStatus(ctx, sessionID, PlanStatusPlanning)
	if err != nil {
		t.Fatalf("first status: %v", err)
	}
	if first.PlanStatus != PlanStatusPlanning || first.PlanVersion != 1 || len(first.Todos) != 0 {
		t.Fatalf("first snapshot = %+v, want planning empty snapshot", first)
	}

	second, err := st.SetPlanStatus(ctx, sessionID, PlanStatusAwaitingApproval)
	if err != nil {
		t.Fatalf("second status: %v", err)
	}
	if second.PlanStatus != PlanStatusAwaitingApproval || second.PlanVersion != 2 || len(second.Todos) != 0 {
		t.Fatalf("second snapshot = %+v, want awaiting_approval empty snapshot", second)
	}
}

func TestPGStoreClaimResumeUsesMetaRowRuntimeEpoch(t *testing.T) {
	ctx := context.Background()
	st, cleanup := setupPGStore(t, ctx)
	defer cleanup()

	sessionID := "sessiontodo-pg-resume-" + time.Now().UTC().Format("20060102150405.000000000")
	createPGSession(t, ctx, st.pool, sessionID)

	first, err := st.Replace(ctx, sessionID, 0, []TodoInput{
		{ID: "todo-a", Content: "继续实现", Status: TodoStatusPending, RuntimeEpoch: "todo-epoch", TurnID: "todo-turn"},
	})
	if err != nil {
		t.Fatalf("replace: %v", err)
	}
	paused, err := st.SetPlanStatusWithMeta(ctx, sessionID, PlanStatusPaused, SnapshotMeta{RuntimeEpoch: "meta-epoch", TurnID: "meta-turn"})
	if err != nil {
		t.Fatalf("set paused: %v", err)
	}
	if paused.PlanVersion != first.PlanVersion+1 || paused.RuntimeEpoch != "meta-epoch" {
		t.Fatalf("paused snapshot = %+v", paused)
	}

	if _, err := st.ClaimResume(ctx, sessionID, paused.PlanVersion, "todo-epoch", "epoch-new", "turn-new"); !errors.Is(err, ErrRuntimeEpochConflict) {
		t.Fatalf("claim with todo row epoch error = %v, want ErrRuntimeEpochConflict", err)
	}
	claimed, err := st.ClaimResume(ctx, sessionID, paused.PlanVersion, "meta-epoch", "epoch-new", "turn-new")
	if err != nil {
		t.Fatalf("claim with meta row epoch: %v", err)
	}
	if claimed.PlanStatus != PlanStatusExecuting || claimed.RuntimeEpoch != "epoch-new" || claimed.TurnID != "turn-new" {
		t.Fatalf("claimed snapshot = %+v", claimed)
	}
}

func setupPGStore(t *testing.T, ctx context.Context) (*PGStore, func()) {
	t.Helper()

	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL 未设置，跳过 sessiontodo PG 集成测试")
	}

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	ensurePGSessionsTable(t, ctx, pool)
	st, err := NewPGStore(ctx, pool)
	if err != nil {
		pool.Close()
		t.Fatalf("new PG store: %v", err)
	}
	cleanup := func() {
		pool.Close()
	}
	return st, cleanup
}

func ensurePGSessionsTable(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()

	_, err := pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS sessions (
			id               TEXT PRIMARY KEY,
			name             TEXT NOT NULL DEFAULT '',
			created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			updated_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			last_accessed_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)
	`)
	if err != nil {
		t.Fatalf("ensure sessions table: %v", err)
	}
}

func createPGSession(t *testing.T, ctx context.Context, pool *pgxpool.Pool, sessionID string) {
	t.Helper()

	_, err := pool.Exec(ctx, `
		INSERT INTO sessions (id, name, created_at, updated_at, last_accessed_at)
		VALUES ($1, $2, NOW(), NOW(), NOW())
		ON CONFLICT (id) DO NOTHING
	`, sessionID, fmt.Sprintf("sessiontodo test %s", sessionID))
	if err != nil {
		t.Fatalf("create test session: %v", err)
	}
}
