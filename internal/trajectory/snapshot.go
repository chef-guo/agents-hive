package trajectory

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

var ErrNotFound = errors.New("trajectory snapshot not found")

type Snapshot struct {
	SessionID    string          `json:"session_id"`
	SnapshotSeq  int             `json:"snapshot_seq"`
	TraceID      string          `json:"trace_id,omitempty"`
	SpanID       string          `json:"span_id,omitempty"`
	Iteration    int             `json:"iteration"`
	MessageCount int             `json:"message_count"`
	Messages     json.RawMessage `json:"messages"`
	SessionTodo  json.RawMessage `json:"sessiontodo,omitempty"`
	MemoryRefs   json.RawMessage `json:"memory_refs,omitempty"`
	CreatedAt    time.Time       `json:"created_at,omitempty"`
}

type Store interface {
	Save(ctx context.Context, snapshot Snapshot) error
	Get(ctx context.Context, sessionID string, snapshotSeq int) (Snapshot, error)
}

func normalizeJSON(raw json.RawMessage, fallback string) json.RawMessage {
	if len(raw) == 0 {
		return json.RawMessage(fallback)
	}
	cp := make(json.RawMessage, len(raw))
	copy(cp, raw)
	return cp
}
