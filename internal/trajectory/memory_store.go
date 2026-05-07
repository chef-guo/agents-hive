package trajectory

import (
	"context"
	"sync"
	"time"
)

type MemoryStore struct {
	mu        sync.RWMutex
	snapshots map[string]map[int]Snapshot
	nextSeq   map[string]int
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		snapshots: make(map[string]map[int]Snapshot),
		nextSeq:   make(map[string]int),
	}
}

func (s *MemoryStore) Save(_ context.Context, snapshot Snapshot) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if snapshot.SnapshotSeq <= 0 {
		snapshot.SnapshotSeq = s.nextSeq[snapshot.SessionID] + 1
	}
	if snapshot.SnapshotSeq > s.nextSeq[snapshot.SessionID] {
		s.nextSeq[snapshot.SessionID] = snapshot.SnapshotSeq
	}
	if snapshot.CreatedAt.IsZero() {
		snapshot.CreatedAt = time.Now()
	}
	snapshot.Messages = normalizeJSON(snapshot.Messages, "[]")
	if snapshot.SessionTodo != nil {
		snapshot.SessionTodo = normalizeJSON(snapshot.SessionTodo, "null")
	}
	if snapshot.MemoryRefs != nil {
		snapshot.MemoryRefs = normalizeJSON(snapshot.MemoryRefs, "null")
	}
	if _, ok := s.snapshots[snapshot.SessionID]; !ok {
		s.snapshots[snapshot.SessionID] = make(map[int]Snapshot)
	}
	s.snapshots[snapshot.SessionID][snapshot.SnapshotSeq] = cloneSnapshot(snapshot)
	return nil
}

func (s *MemoryStore) Get(_ context.Context, sessionID string, snapshotSeq int) (Snapshot, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	bySeq, ok := s.snapshots[sessionID]
	if !ok {
		return Snapshot{}, ErrNotFound
	}
	snapshot, ok := bySeq[snapshotSeq]
	if !ok {
		return Snapshot{}, ErrNotFound
	}
	return cloneSnapshot(snapshot), nil
}

func cloneSnapshot(snapshot Snapshot) Snapshot {
	snapshot.Messages = normalizeJSON(snapshot.Messages, "[]")
	if snapshot.SessionTodo != nil {
		snapshot.SessionTodo = normalizeJSON(snapshot.SessionTodo, "null")
	}
	if snapshot.MemoryRefs != nil {
		snapshot.MemoryRefs = normalizeJSON(snapshot.MemoryRefs, "null")
	}
	return snapshot
}
