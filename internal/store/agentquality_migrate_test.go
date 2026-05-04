package store

import (
	"strings"
	"testing"
)

func TestPGInitSQLIncludesAgentQualityCandidates(t *testing.T) {
	sql := strings.Join(strings.Fields(pgInitSQL), " ")
	required := []string{
		"CREATE TABLE IF NOT EXISTS agentquality_candidates",
		"id TEXT PRIMARY KEY",
		"case_json JSONB NOT NULL DEFAULT '{}'",
		"source_event JSONB NOT NULL DEFAULT '{}'",
		"suggestions_json JSONB NOT NULL DEFAULT '[]'",
		"cluster_id TEXT NOT NULL DEFAULT ''",
		"verify_result JSONB NOT NULL DEFAULT '{}'",
		"last_verified_at TIMESTAMPTZ",
		"CREATE UNIQUE INDEX IF NOT EXISTS idx_agentquality_candidates_fingerprint",
		"WHERE status IN ('new', 'reviewing', 'approved')",
		"CREATE TABLE IF NOT EXISTS agentquality_optimization_suggestions",
		"source_candidate_id TEXT NOT NULL DEFAULT ''",
		"source_eval_diff_id TEXT NOT NULL DEFAULT ''",
		"proposed_value TEXT NOT NULL DEFAULT ''",
		"CREATE INDEX IF NOT EXISTS idx_agentquality_opt_suggestions_status_created",
		"CREATE INDEX IF NOT EXISTS idx_agentquality_opt_suggestions_source_candidate",
		"CREATE INDEX IF NOT EXISTS idx_agentquality_opt_suggestions_target",
		"CREATE TABLE IF NOT EXISTS optimization_eval_diffs",
		"CREATE TABLE IF NOT EXISTS optimization_approvals",
		"CREATE TABLE IF NOT EXISTS optimization_rollback_alerts",
		"CREATE TABLE IF NOT EXISTS optimization_rollbacks",
		"CREATE TABLE IF NOT EXISTS embedding_backlog",
		"vector_space TEXT NOT NULL DEFAULT 'memory:default'",
		"CREATE TABLE IF NOT EXISTS agentquality_grouping_rules",
		"CREATE INDEX IF NOT EXISTS idx_agentquality_grouping_rules_priority",
		"CREATE TABLE IF NOT EXISTS optimization_tool_descriptions",
		"CREATE TABLE IF NOT EXISTS memory_governance_policies",
		"CREATE TABLE IF NOT EXISTS optimization_rollouts",
		"previous_exists BOOLEAN NOT NULL DEFAULT FALSE",
		"CREATE INDEX IF NOT EXISTS idx_optimization_rollouts_status",
		"CREATE INDEX IF NOT EXISTS idx_agentquality_candidates_status_created",
		"CREATE INDEX IF NOT EXISTS idx_agentquality_candidates_session",
	}

	for _, needle := range required {
		if !strings.Contains(sql, needle) {
			t.Fatalf("pgInitSQL missing %q", needle)
		}
	}
}

func TestPGInitSQLDoesNotCreateIndexesBeforeCompatColumns(t *testing.T) {
	sql := strings.Join(strings.Fields(pgInitSQL), " ")
	forbidden := []string{
		"CREATE INDEX IF NOT EXISTS idx_usage_records_quality_case",
		"CREATE INDEX IF NOT EXISTS idx_agentquality_candidates_cluster",
		"CREATE INDEX IF NOT EXISTS idx_agentquality_opt_suggestions_source_eval_diff",
	}

	for _, needle := range forbidden {
		if strings.Contains(sql, needle) {
			t.Fatalf("pgInitSQL must not create %q before pgAddUserColumns has added compatibility columns", needle)
		}
	}
}

func TestPGAddUserColumnsCreatesIndexesAfterCompatColumns(t *testing.T) {
	sql := strings.Join(strings.Fields(pgAddUserColumns), " ")
	required := []string{
		"ALTER TABLE usage_records ADD COLUMN IF NOT EXISTS quality_case_id TEXT NOT NULL DEFAULT ''",
		"CREATE INDEX IF NOT EXISTS idx_usage_records_quality_case",
		"ALTER TABLE agentquality_candidates ADD COLUMN IF NOT EXISTS cluster_id TEXT NOT NULL DEFAULT ''",
		"CREATE INDEX IF NOT EXISTS idx_agentquality_candidates_cluster",
		"ALTER TABLE agentquality_optimization_suggestions ADD COLUMN IF NOT EXISTS source_eval_diff_id TEXT NOT NULL DEFAULT ''",
		"CREATE INDEX IF NOT EXISTS idx_agentquality_opt_suggestions_source_eval_diff",
	}

	for _, needle := range required {
		if !strings.Contains(sql, needle) {
			t.Fatalf("pgAddUserColumns missing %q", needle)
		}
	}
}
