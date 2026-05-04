package sessiontodo

import (
	"context"
	"fmt"
	"sync"
	"time"
)

var _ Store = (*MemoryStore)(nil)

type memorySession struct {
	snapshot    Snapshot
	lastTouched time.Time
}

// MemoryStore 是并发安全的内存实现，主要用于测试和开发 fallback。
type MemoryStore struct {
	mu       sync.RWMutex
	sessions map[string]memorySession
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		sessions: make(map[string]memorySession),
	}
}

func (s *MemoryStore) Replace(ctx context.Context, sessionID string, expectedPlanVersion int64, inputs []TodoInput) (Snapshot, error) {
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
	now := time.Now().UTC()

	s.mu.Lock()
	defer s.mu.Unlock()

	state := s.sessions[sessionID]
	if state.snapshot.SessionID == "" {
		state.snapshot = emptySnapshot(sessionID)
	}
	currentVersion := state.snapshot.PlanVersion
	if currentVersion != expectedPlanVersion {
		return Snapshot{}, &PlanVersionConflictError{Expected: expectedPlanVersion, Got: currentVersion}
	}

	nextVersion := currentVersion + 1
	todos := make([]Todo, 0, len(inputs))
	for i, input := range inputs {
		todos = append(todos, Todo{
			ID:               todoIDForInput(input, i),
			SessionID:        sessionID,
			Content:          input.Content,
			Status:           input.Status,
			Order:            input.Order,
			Version:          nextVersion,
			Source:           sourceOrDefault(input.Source),
			TraceID:          input.TraceID,
			SpanID:           input.SpanID,
			TurnID:           input.TurnID,
			RuntimeEpoch:     input.RuntimeEpoch,
			SourceChangeID:   input.SourceChangeID,
			SourceRevision:   input.SourceRevision,
			SourceToolCallID: input.SourceToolCallID,
			CreatedAt:        now,
			UpdatedAt:        now,
		})
	}

	source, traceID, spanID, turnID, runtimeEpoch, sourceToolCallID, sourceChangeID, sourceRevision := snapshotSourceFromTodos(todos)
	state.snapshot.SessionID = sessionID
	state.snapshot.PlanVersion = nextVersion
	if state.snapshot.PlanStatus == "" {
		state.snapshot.PlanStatus = PlanStatusNone
	}
	state.snapshot.Todos = todos
	state.snapshot.Source = source
	state.snapshot.TraceID = traceID
	state.snapshot.SpanID = spanID
	state.snapshot.TurnID = turnID
	state.snapshot.RuntimeEpoch = runtimeEpoch
	state.snapshot.SourceToolCallID = sourceToolCallID
	state.snapshot.SourceChangeID = sourceChangeID
	state.snapshot.SourceRevision = sourceRevision
	state.snapshot.UpdatedAt = now
	state.lastTouched = now
	s.sessions[sessionID] = state

	return cloneSnapshot(state.snapshot), nil
}

func (s *MemoryStore) Snapshot(ctx context.Context, sessionID string) (Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return Snapshot{}, err
	}
	if err := validateSessionID(sessionID); err != nil {
		return Snapshot{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	state, ok := s.sessions[sessionID]
	if !ok || state.snapshot.SessionID == "" {
		return emptySnapshot(sessionID), nil
	}
	state.lastTouched = time.Now().UTC()
	s.sessions[sessionID] = state
	return cloneSnapshot(state.snapshot), nil
}

func (s *MemoryStore) SetPlanStatus(ctx context.Context, sessionID string, status PlanStatus) (Snapshot, error) {
	return s.SetPlanStatusWithMeta(ctx, sessionID, status, SnapshotMeta{})
}

func (s *MemoryStore) SetPlanStatusWithMeta(ctx context.Context, sessionID string, status PlanStatus, meta SnapshotMeta) (Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return Snapshot{}, err
	}
	if err := validateSessionID(sessionID); err != nil {
		return Snapshot{}, err
	}
	if err := validatePlanStatus(status); err != nil {
		return Snapshot{}, err
	}
	now := time.Now().UTC()

	s.mu.Lock()
	defer s.mu.Unlock()

	state := s.sessions[sessionID]
	if state.snapshot.SessionID == "" {
		state.snapshot = emptySnapshot(sessionID)
	}
	meta = mergeSnapshotMeta(state.snapshot, meta)
	nextVersion := state.snapshot.PlanVersion + 1
	state.snapshot.PlanStatus = status
	state.snapshot.PlanVersion = nextVersion
	state.snapshot.Source = sourceOrDefault(meta.Source)
	state.snapshot.TraceID = meta.TraceID
	state.snapshot.SpanID = meta.SpanID
	state.snapshot.TurnID = meta.TurnID
	state.snapshot.RuntimeEpoch = meta.RuntimeEpoch
	state.snapshot.SourceToolCallID = meta.SourceToolCallID
	state.snapshot.SourceChangeID = meta.SourceChangeID
	state.snapshot.SourceRevision = meta.SourceRevision
	for i := range state.snapshot.Todos {
		state.snapshot.Todos[i].Version = nextVersion
		state.snapshot.Todos[i].TurnID = meta.TurnID
		state.snapshot.Todos[i].RuntimeEpoch = meta.RuntimeEpoch
		state.snapshot.Todos[i].UpdatedAt = now
	}
	state.snapshot.UpdatedAt = now
	state.lastTouched = now
	s.sessions[sessionID] = state

	return cloneSnapshot(state.snapshot), nil
}

func (s *MemoryStore) ClaimResume(ctx context.Context, sessionID string, expectedPlanVersion int64, expectedRuntimeEpoch, runtimeEpoch, turnID string) (Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return Snapshot{}, err
	}
	if err := validateSessionID(sessionID); err != nil {
		return Snapshot{}, err
	}
	if expectedPlanVersion < 0 {
		return Snapshot{}, fmt.Errorf("%w: expectedPlanVersion must be >= 0", ErrInvalidInput)
	}
	now := time.Now().UTC()

	s.mu.Lock()
	defer s.mu.Unlock()

	state := s.sessions[sessionID]
	if state.snapshot.SessionID == "" {
		state.snapshot = emptySnapshot(sessionID)
	}
	if state.snapshot.PlanVersion != expectedPlanVersion {
		return Snapshot{}, &PlanVersionConflictError{Expected: expectedPlanVersion, Got: state.snapshot.PlanVersion}
	}
	if expectedRuntimeEpoch == "" {
		return Snapshot{}, fmt.Errorf("%w: expected runtime epoch is required", ErrInvalidInput)
	}
	if state.snapshot.RuntimeEpoch != expectedRuntimeEpoch {
		return Snapshot{}, &RuntimeEpochConflictError{Expected: expectedRuntimeEpoch, Got: state.snapshot.RuntimeEpoch}
	}
	if state.snapshot.PlanStatus != PlanStatusPaused {
		return Snapshot{}, fmt.Errorf("%w: plan status is not paused: %s", ErrResumeConflict, state.snapshot.PlanStatus)
	}

	nextVersion := state.snapshot.PlanVersion + 1
	state.snapshot.PlanStatus = PlanStatusExecuting
	state.snapshot.PlanVersion = nextVersion
	state.snapshot.TurnID = turnID
	state.snapshot.RuntimeEpoch = runtimeEpoch
	state.snapshot.UpdatedAt = now
	for i := range state.snapshot.Todos {
		state.snapshot.Todos[i].Version = nextVersion
		state.snapshot.Todos[i].TurnID = turnID
		state.snapshot.Todos[i].RuntimeEpoch = runtimeEpoch
		state.snapshot.Todos[i].UpdatedAt = now
	}
	state.lastTouched = now
	s.sessions[sessionID] = state
	return cloneSnapshot(state.snapshot), nil
}

func (s *MemoryStore) Clear(ctx context.Context, sessionID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateSessionID(sessionID); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, sessionID)
	return nil
}

func (s *MemoryStore) GCIdleSessions(ctx context.Context, maxIdle time.Duration) int {
	if err := ctx.Err(); err != nil {
		return 0
	}
	if maxIdle <= 0 {
		return 0
	}
	cutoff := time.Now().UTC().Add(-maxIdle)

	s.mu.Lock()
	defer s.mu.Unlock()

	removed := 0
	for sessionID, state := range s.sessions {
		lastTouched := state.lastTouched
		if lastTouched.IsZero() {
			lastTouched = state.snapshot.UpdatedAt
		}
		if lastTouched.IsZero() || lastTouched.After(cutoff) {
			continue
		}
		delete(s.sessions, sessionID)
		removed++
	}
	return removed
}
