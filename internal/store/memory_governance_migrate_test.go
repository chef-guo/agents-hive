package store

import (
	"strings"
	"testing"
)

func TestPGInitSQLIncludesMemoryGovernanceIndexes(t *testing.T) {
	sql := strings.Join(strings.Fields(pgInitSQL), " ")
	required := []string{
		"target_scope TEXT NOT NULL DEFAULT 'user'",
		"target_id TEXT NOT NULL DEFAULT ''",
		"visibility TEXT NOT NULL DEFAULT 'private'",
		"memory_kind TEXT NOT NULL DEFAULT ''",
		"subject_type TEXT NOT NULL DEFAULT ''",
		"CREATE INDEX IF NOT EXISTS idx_memories_governance_expires ON memories (((metadata->'governance'->>'expires_at')))",
		"CREATE INDEX IF NOT EXISTS idx_memories_governance_source ON memories (((metadata->'governance'->>'source')))",
		"CREATE EXTENSION IF NOT EXISTS pg_trgm",
		"CREATE INDEX IF NOT EXISTS idx_memories_content_trgm ON memories USING GIN(content gin_trgm_ops)",
	}

	for _, needle := range required {
		if !strings.Contains(sql, needle) {
			t.Fatalf("pgInitSQL missing %q", needle)
		}
	}
}

func TestPGAddUserColumnsKeepsMemoryTrigramFallbackIndex(t *testing.T) {
	sql := strings.Join(strings.Fields(pgAddUserColumns), " ")
	required := []string{
		"ALTER TABLE memories ADD COLUMN IF NOT EXISTS target_scope TEXT NOT NULL DEFAULT 'user'",
		"ALTER TABLE memories ADD COLUMN IF NOT EXISTS target_id TEXT NOT NULL DEFAULT ''",
		"ALTER TABLE memories ADD COLUMN IF NOT EXISTS visibility TEXT NOT NULL DEFAULT 'private'",
		"ALTER TABLE memories ADD COLUMN IF NOT EXISTS memory_kind TEXT NOT NULL DEFAULT ''",
		"ALTER TABLE memories ADD COLUMN IF NOT EXISTS subject_type TEXT NOT NULL DEFAULT ''",
		"CREATE INDEX IF NOT EXISTS idx_memories_user_target ON memories(user_id, target_scope, target_id) WHERE user_id != ''",
		"CREATE INDEX IF NOT EXISTS idx_memories_kind_accessed ON memories(memory_kind, accessed_at DESC)",
		"CREATE INDEX IF NOT EXISTS idx_memories_visibility_accessed ON memories(visibility, accessed_at DESC)",
		"CREATE INDEX IF NOT EXISTS idx_memories_subject_type_accessed ON memories(subject_type, accessed_at DESC)",
		"CREATE EXTENSION IF NOT EXISTS pg_trgm",
		"CREATE INDEX IF NOT EXISTS idx_memories_content_trgm ON memories USING GIN(content gin_trgm_ops)",
	}

	for _, needle := range required {
		if !strings.Contains(sql, needle) {
			t.Fatalf("pgAddUserColumns missing %q", needle)
		}
	}
}

func TestPGBackfillMemoryColumnsBatchIsChunkedAndSkipLocked(t *testing.T) {
	sql := strings.Join(strings.Fields(pgBackfillMemoryColumnsBatch), " ")
	required := []string{
		"LIMIT $1 FOR UPDATE SKIP LOCKED",
		"UPDATE memories AS m SET target_scope = batch.desired_target_scope",
		"target_id = batch.desired_target_id",
		"visibility = batch.desired_visibility",
		"memory_kind = batch.desired_memory_kind",
		"subject_type = batch.desired_subject_type",
		"SELECT COUNT(*) FROM updated",
	}

	for _, needle := range required {
		if !strings.Contains(sql, needle) {
			t.Fatalf("pgBackfillMemoryColumnsBatch missing %q", needle)
		}
	}
}
