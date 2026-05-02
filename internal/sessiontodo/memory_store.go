package sessiontodo

import (
	"context"
	"fmt"
	"sync"
	"time"
)

var _ Store = (*MemoryStore)(nil)

type memorySession struct {
	snapshot Snapshot
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
			SourceChangeID:   input.SourceChangeID,
			SourceRevision:   input.SourceRevision,
			SourceToolCallID: input.SourceToolCallID,
			CreatedAt:        now,
			UpdatedAt:        now,
		})
	}

	source, traceID, spanID, sourceToolCallID, sourceChangeID, sourceRevision := snapshotSourceFromTodos(todos)
	state.snapshot.SessionID = sessionID
	state.snapshot.PlanVersion = nextVersion
	if state.snapshot.PlanStatus == "" {
		state.snapshot.PlanStatus = PlanStatusNone
	}
	state.snapshot.Todos = todos
	state.snapshot.Source = source
	state.snapshot.TraceID = traceID
	state.snapshot.SpanID = spanID
	state.snapshot.SourceToolCallID = sourceToolCallID
	state.snapshot.SourceChangeID = sourceChangeID
	state.snapshot.SourceRevision = sourceRevision
	state.snapshot.UpdatedAt = now
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

	s.mu.RLock()
	defer s.mu.RUnlock()

	state, ok := s.sessions[sessionID]
	if !ok || state.snapshot.SessionID == "" {
		return emptySnapshot(sessionID), nil
	}
	return cloneSnapshot(state.snapshot), nil
}

func (s *MemoryStore) SetPlanStatus(ctx context.Context, sessionID string, status PlanStatus) (Snapshot, error) {
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
	nextVersion := state.snapshot.PlanVersion + 1
	state.snapshot.PlanStatus = status
	state.snapshot.PlanVersion = nextVersion
	for i := range state.snapshot.Todos {
		state.snapshot.Todos[i].Version = nextVersion
		state.snapshot.Todos[i].UpdatedAt = now
	}
	state.snapshot.UpdatedAt = now
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
