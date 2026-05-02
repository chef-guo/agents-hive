package sessiontodo

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const pgSessionTodosInitSQL = `
CREATE TABLE IF NOT EXISTS hive_session_todos (
    session_id       TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    todo_id          TEXT NOT NULL,
    content          TEXT NOT NULL,
    status           TEXT NOT NULL,
    order_index      INTEGER NOT NULL,
    version          BIGINT NOT NULL DEFAULT 1,
    plan_version     BIGINT NOT NULL,
    plan_status      TEXT NOT NULL DEFAULT 'none',
    source           TEXT NOT NULL DEFAULT 'agent',
    trace_id         TEXT,
    span_id          TEXT,
    source_change_id TEXT,
    source_revision  BIGINT,
    source_tool_call_id TEXT,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (session_id, todo_id)
);

CREATE INDEX IF NOT EXISTS idx_hive_session_todos_session_plan
    ON hive_session_todos(session_id, plan_version);

CREATE INDEX IF NOT EXISTS idx_hive_session_todos_source_change
    ON hive_session_todos(source_change_id);

CREATE INDEX IF NOT EXISTS idx_hive_session_todos_trace
    ON hive_session_todos(trace_id);
`

var _ Store = (*PGStore)(nil)

// PGStore 是基于 PostgreSQL 的 session todo store。
type PGStore struct {
	pool *pgxpool.Pool
}

func NewPGStore(ctx context.Context, pool *pgxpool.Pool) (*PGStore, error) {
	if pool == nil {
		return nil, fmt.Errorf("%w: pg pool is nil", ErrInvalidInput)
	}
	if _, err := pool.Exec(ctx, pgSessionTodosInitSQL); err != nil {
		return nil, fmt.Errorf("sessiontodo pg migrate: %w", err)
	}
	return &PGStore{pool: pool}, nil
}

func (s *PGStore) Replace(ctx context.Context, sessionID string, expectedPlanVersion int64, inputs []TodoInput) (Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return Snapshot{}, err
	}
	if err := validateSessionID(sessionID); err != nil {
		return Snapshot{}, err
	}
	if expectedPlanVersion < 0 {
		return Snapshot{}, fmt.Errorf("%w: expectedPlanVersion must be >= 0", ErrInvalidInput)
	}
	if err := validateTodoInputs(inputs); err != nil {
		return Snapshot{}, err
	}

	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return Snapshot{}, err
	}
	defer func() {
		_ = tx.Rollback(ctx)
	}()

	if err := lockSessionTodos(ctx, tx, sessionID); err != nil {
		return Snapshot{}, err
	}

	currentVersion, currentStatus, err := loadPGSnapshotHeader(ctx, tx, sessionID)
	if err != nil {
		return Snapshot{}, err
	}
	if currentVersion != expectedPlanVersion {
		return Snapshot{}, &PlanVersionConflictError{Expected: expectedPlanVersion, Got: currentVersion}
	}
	if currentStatus == "" {
		currentStatus = PlanStatusNone
	}

	nextVersion := currentVersion + 1
	now := time.Now().UTC()
	if _, err := tx.Exec(ctx, `DELETE FROM hive_session_todos WHERE session_id = $1`, sessionID); err != nil {
		return Snapshot{}, fmt.Errorf("sessiontodo replace delete: %w", err)
	}

	todos := make([]Todo, 0, len(inputs))
	for i, input := range inputs {
		todo := Todo{
			ID:               todoIDForInput(input, i),
			SessionID:        sessionID,
			Content:          input.Content,
			Status:           input.Status,
			Order:            input.Order,
			Version:          nextVersion,
			Source:           sourceOrDefault(input.Source),
			TraceID:          input.TraceID,
			SpanID:           input.SpanID,
			SourceChangeID:   input.SourceChangeID,
			SourceRevision:   input.SourceRevision,
			SourceToolCallID: input.SourceToolCallID,
			CreatedAt:        now,
			UpdatedAt:        now,
		}
		todos = append(todos, todo)
	}

	source, traceID, spanID, sourceToolCallID, sourceChangeID, sourceRevision := snapshotSourceFromTodos(todos)
	if source == "" {
		source = "agent"
	}

	if err := insertPGTodoRow(ctx, tx, Todo{
		ID:               sessionTodosMetaID,
		SessionID:        sessionID,
		Content:          "",
		Status:           TodoStatusPending,
		Order:            -1,
		Version:          nextVersion,
		Source:           source,
		TraceID:          traceID,
		SpanID:           spanID,
		SourceChangeID:   sourceChangeID,
		SourceRevision:   sourceRevision,
		SourceToolCallID: sourceToolCallID,
		CreatedAt:        now,
		UpdatedAt:        now,
	}, nextVersion, currentStatus); err != nil {
		return Snapshot{}, err
	}

	for _, todo := range todos {
		if err := insertPGTodoRow(ctx, tx, todo, nextVersion, currentStatus); err != nil {
			return Snapshot{}, err
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return Snapshot{}, fmt.Errorf("sessiontodo replace commit: %w", err)
	}

	return Snapshot{
		SessionID:        sessionID,
		PlanStatus:       currentStatus,
		PlanVersion:      nextVersion,
		Todos:            todos,
		Source:           source,
		TraceID:          traceID,
		SpanID:           spanID,
		SourceToolCallID: sourceToolCallID,
		SourceChangeID:   sourceChangeID,
		SourceRevision:   sourceRevision,
		UpdatedAt:        now,
	}, nil
}

func (s *PGStore) Snapshot(ctx context.Context, sessionID string) (Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return Snapshot{}, err
	}
	if err := validateSessionID(sessionID); err != nil {
		return Snapshot{}, err
	}

	rows, err := s.pool.Query(ctx, `
		SELECT todo_id, content, status, order_index, version, plan_version, plan_status,
		       source, COALESCE(trace_id, ''), COALESCE(span_id, ''),
		       COALESCE(source_change_id, ''), COALESCE(source_revision, 0),
		       COALESCE(source_tool_call_id, ''), created_at, updated_at
		FROM hive_session_todos
		WHERE session_id = $1
		ORDER BY CASE WHEN todo_id = $2 THEN 0 ELSE 1 END, order_index, todo_id
	`, sessionID, sessionTodosMetaID)
	if err != nil {
		return Snapshot{}, fmt.Errorf("sessiontodo snapshot query: %w", err)
	}
	defer rows.Close()

	snap := emptySnapshot(sessionID)
	var haveHeader bool
	for rows.Next() {
		var todo Todo
		var status string
		var planStatus string
		var planVersion int64
		if err := rows.Scan(
			&todo.ID,
			&todo.Content,
			&status,
			&todo.Order,
			&todo.Version,
			&planVersion,
			&planStatus,
			&todo.Source,
			&todo.TraceID,
			&todo.SpanID,
			&todo.SourceChangeID,
			&todo.SourceRevision,
			&todo.SourceToolCallID,
			&todo.CreatedAt,
			&todo.UpdatedAt,
		); err != nil {
			return Snapshot{}, fmt.Errorf("sessiontodo snapshot scan: %w", err)
		}
		if !haveHeader {
			snap.PlanVersion = planVersion
			snap.PlanStatus = PlanStatus(planStatus)
			snap.Source = todo.Source
			snap.TraceID = todo.TraceID
			snap.SpanID = todo.SpanID
			snap.SourceChangeID = todo.SourceChangeID
			snap.SourceRevision = todo.SourceRevision
			snap.SourceToolCallID = todo.SourceToolCallID
			snap.UpdatedAt = todo.UpdatedAt
			haveHeader = true
		}
		if todo.ID == sessionTodosMetaID {
			continue
		}
		todo.SessionID = sessionID
		todo.Status = TodoStatus(status)
		snap.Todos = append(snap.Todos, todo)
		if todo.UpdatedAt.After(snap.UpdatedAt) {
			snap.UpdatedAt = todo.UpdatedAt
		}
	}
	if err := rows.Err(); err != nil {
		return Snapshot{}, fmt.Errorf("sessiontodo snapshot rows: %w", err)
	}
	if !haveHeader {
		return emptySnapshot(sessionID), nil
	}
	if snap.Todos == nil {
		snap.Todos = []Todo{}
	}
	return snap, nil
}

func (s *PGStore) SetPlanStatus(ctx context.Context, sessionID string, status PlanStatus) (Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return Snapshot{}, err
	}
	if err := validateSessionID(sessionID); err != nil {
		return Snapshot{}, err
	}
	if err := validatePlanStatus(status); err != nil {
		return Snapshot{}, err
	}

	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return Snapshot{}, err
	}
	defer func() {
		_ = tx.Rollback(ctx)
	}()

	if err := lockSessionTodos(ctx, tx, sessionID); err != nil {
		return Snapshot{}, err
	}

	currentVersion, _, err := loadPGSnapshotHeader(ctx, tx, sessionID)
	if err != nil {
		return Snapshot{}, err
	}
	nextVersion := currentVersion + 1
	now := time.Now().UTC()
	if currentVersion == 0 {
		if err := insertPGTodoRow(ctx, tx, Todo{
			ID:        sessionTodosMetaID,
			SessionID: sessionID,
			Content:   "",
			Status:    TodoStatusPending,
			Order:     -1,
			Version:   nextVersion,
			Source:    "runtime",
			CreatedAt: now,
			UpdatedAt: now,
		}, nextVersion, status); err != nil {
			return Snapshot{}, err
		}
	} else {
		if _, err := tx.Exec(ctx, `
			UPDATE hive_session_todos
			SET plan_status = $2, plan_version = $3, version = $3, updated_at = $4
			WHERE session_id = $1
		`, sessionID, string(status), nextVersion, now); err != nil {
			return Snapshot{}, fmt.Errorf("sessiontodo set status: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return Snapshot{}, fmt.Errorf("sessiontodo set status commit: %w", err)
	}
	return s.Snapshot(ctx, sessionID)
}

func (s *PGStore) Clear(ctx context.Context, sessionID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateSessionID(sessionID); err != nil {
		return err
	}

	_, err := s.pool.Exec(ctx, `DELETE FROM hive_session_todos WHERE session_id = $1`, sessionID)
	if err != nil {
		return fmt.Errorf("sessiontodo clear: %w", err)
	}
	return nil
}

func lockSessionTodos(ctx context.Context, tx pgx.Tx, sessionID string) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(739531, hashtext($1))`, sessionID)
	if err != nil {
		return fmt.Errorf("sessiontodo advisory lock: %w", err)
	}
	return nil
}

func loadPGSnapshotHeader(ctx context.Context, tx pgx.Tx, sessionID string) (int64, PlanStatus, error) {
	var planVersion int64
	var planStatus string
	err := tx.QueryRow(ctx, `
		SELECT plan_version, plan_status
		FROM hive_session_todos
		WHERE session_id = $1
		ORDER BY plan_version DESC, updated_at DESC
		LIMIT 1
	`, sessionID).Scan(&planVersion, &planStatus)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, PlanStatusNone, nil
	}
	if err != nil {
		return 0, "", fmt.Errorf("sessiontodo load header: %w", err)
	}
	return planVersion, PlanStatus(planStatus), nil
}

func insertPGTodoRow(ctx context.Context, tx pgx.Tx, todo Todo, planVersion int64, planStatus PlanStatus) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO hive_session_todos (
			session_id, todo_id, content, status, order_index, version,
			plan_version, plan_status, source, trace_id, span_id,
			source_change_id, source_revision, source_tool_call_id,
			created_at, updated_at
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9,
		        NULLIF($10, ''), NULLIF($11, ''), NULLIF($12, ''),
		        NULLIF($13, 0), NULLIF($14, ''), $15, $16)
		ON CONFLICT (session_id, todo_id) DO UPDATE SET
			content             = EXCLUDED.content,
			status              = EXCLUDED.status,
			order_index         = EXCLUDED.order_index,
			version             = EXCLUDED.version,
			plan_version        = EXCLUDED.plan_version,
			plan_status         = EXCLUDED.plan_status,
			source              = EXCLUDED.source,
			trace_id            = EXCLUDED.trace_id,
			span_id             = EXCLUDED.span_id,
			source_change_id    = EXCLUDED.source_change_id,
			source_revision     = EXCLUDED.source_revision,
			source_tool_call_id = EXCLUDED.source_tool_call_id,
			updated_at          = EXCLUDED.updated_at
	`, todo.SessionID, todo.ID, todo.Content, string(todo.Status), todo.Order, todo.Version,
		planVersion, string(planStatus), sourceOrDefault(todo.Source), todo.TraceID, todo.SpanID,
		todo.SourceChangeID, todo.SourceRevision, todo.SourceToolCallID, todo.CreatedAt, todo.UpdatedAt)
	if err != nil {
		return fmt.Errorf("sessiontodo insert row: %w", err)
	}
	return nil
}

func sourceOrDefault(source string) string {
	if source == "" {
		return "agent"
	}
	return source
}
