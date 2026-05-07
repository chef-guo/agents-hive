package trajectory

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type PGStore struct {
	pool *pgxpool.Pool
}

func NewPGStore(pool *pgxpool.Pool) *PGStore {
	return &PGStore{pool: pool}
}

func (s *PGStore) Save(ctx context.Context, snapshot Snapshot) error {
	if snapshot.SnapshotSeq <= 0 {
		err := s.pool.QueryRow(ctx,
			`SELECT COALESCE(MAX(snapshot_seq), 0) + 1 FROM hive_step_snapshots WHERE session_id = $1`,
			snapshot.SessionID,
		).Scan(&snapshot.SnapshotSeq)
		if err != nil {
			return err
		}
	}
	snapshot.Messages = normalizeJSON(snapshot.Messages, "[]")
	var sessionTodo any
	if len(snapshot.SessionTodo) > 0 {
		sessionTodo = json.RawMessage(snapshot.SessionTodo)
	}
	var memoryRefs any
	if len(snapshot.MemoryRefs) > 0 {
		memoryRefs = json.RawMessage(snapshot.MemoryRefs)
	}
	_, err := s.pool.Exec(ctx,
		`INSERT INTO hive_step_snapshots
			(session_id, snapshot_seq, trace_id, span_id, iteration, message_count, messages, sessiontodo, memory_refs)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
		ON CONFLICT (session_id, snapshot_seq) DO UPDATE SET
			trace_id = EXCLUDED.trace_id,
			span_id = EXCLUDED.span_id,
			iteration = EXCLUDED.iteration,
			message_count = EXCLUDED.message_count,
			messages = EXCLUDED.messages,
			sessiontodo = EXCLUDED.sessiontodo,
			memory_refs = EXCLUDED.memory_refs`,
		snapshot.SessionID, snapshot.SnapshotSeq, snapshot.TraceID, snapshot.SpanID,
		snapshot.Iteration, snapshot.MessageCount, snapshot.Messages, sessionTodo, memoryRefs,
	)
	return err
}

func (s *PGStore) Get(ctx context.Context, sessionID string, snapshotSeq int) (Snapshot, error) {
	var snapshot Snapshot
	err := s.pool.QueryRow(ctx,
		`SELECT session_id, snapshot_seq, trace_id, span_id, iteration, message_count,
			messages, sessiontodo, memory_refs, created_at
		FROM hive_step_snapshots
		WHERE session_id = $1 AND snapshot_seq = $2`,
		sessionID, snapshotSeq,
	).Scan(
		&snapshot.SessionID,
		&snapshot.SnapshotSeq,
		&snapshot.TraceID,
		&snapshot.SpanID,
		&snapshot.Iteration,
		&snapshot.MessageCount,
		&snapshot.Messages,
		&snapshot.SessionTodo,
		&snapshot.MemoryRefs,
		&snapshot.CreatedAt,
	)
	if err == pgx.ErrNoRows {
		return Snapshot{}, ErrNotFound
	}
	if err != nil {
		return Snapshot{}, err
	}
	snapshot.Messages = normalizeJSON(snapshot.Messages, "[]")
	return snapshot, nil
}
