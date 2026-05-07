package trajectory

import (
	"context"
	"encoding/json"
	"testing"
)

func TestMemoryStoreAssignsMonotonicSnapshotSeqPerSession(t *testing.T) {
	ctx := context.Background()
	st := NewMemoryStore()

	first := Snapshot{SessionID: "s1", Messages: json.RawMessage(`[{"role":"user","content":"one"}]`)}
	if err := st.Save(ctx, first); err != nil {
		t.Fatalf("save first snapshot: %v", err)
	}
	second := Snapshot{SessionID: "s1", Messages: json.RawMessage(`[{"role":"user","content":"two"}]`)}
	if err := st.Save(ctx, second); err != nil {
		t.Fatalf("save second snapshot: %v", err)
	}
	other := Snapshot{SessionID: "s2", Messages: json.RawMessage(`[{"role":"user","content":"other"}]`)}
	if err := st.Save(ctx, other); err != nil {
		t.Fatalf("save other session snapshot: %v", err)
	}

	gotFirst, err := st.Get(ctx, "s1", 1)
	if err != nil {
		t.Fatalf("get first snapshot: %v", err)
	}
	gotSecond, err := st.Get(ctx, "s1", 2)
	if err != nil {
		t.Fatalf("get second snapshot: %v", err)
	}
	gotOther, err := st.Get(ctx, "s2", 1)
	if err != nil {
		t.Fatalf("get other session snapshot: %v", err)
	}

	if gotFirst.SnapshotSeq != 1 || gotSecond.SnapshotSeq != 2 || gotOther.SnapshotSeq != 1 {
		t.Fatalf("snapshot seqs = %d, %d, %d; want 1, 2, 1", gotFirst.SnapshotSeq, gotSecond.SnapshotSeq, gotOther.SnapshotSeq)
	}
}
