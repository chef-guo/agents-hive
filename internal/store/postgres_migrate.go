package store

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"

	"github.com/chef-guo/agents-hive/internal/errs"
)

// pgInitSQL PostgreSQL 建表 SQL（一次性初始化，开发阶段不做增量迁移）
//
// 建表策略说明：
//   - 本文件管理：sessions、messages、permission_grants、memories、usage_records（P2-4）、
//     hive_traces/hive_metrics（P2-6）等核心表
//   - 各包自管理（在各自 NewPGXxx 中建表）：
//     journal（internal/journal/pg_journal.go）、
//     taskboard（internal/taskboard/pg_taskboard.go）
const pgInitSQL = `
-- 会话表
CREATE TABLE IF NOT EXISTS sessions (
	id               TEXT PRIMARY KEY,
	name             TEXT NOT NULL DEFAULT '',
	created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
	updated_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
	last_accessed_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
	message_count    INTEGER NOT NULL DEFAULT 0,
	total_tokens     INTEGER NOT NULL DEFAULT 0,
	profile_name     TEXT NOT NULL DEFAULT '',
	deleted          INTEGER NOT NULL DEFAULT 0,
	tags             TEXT NOT NULL DEFAULT '[]',
	parent_id        TEXT NOT NULL DEFAULT '',
	fork_point       INTEGER NOT NULL DEFAULT 0,
	children         TEXT NOT NULL DEFAULT '[]'
);

CREATE INDEX IF NOT EXISTS idx_sessions_last_accessed ON sessions(last_accessed_at DESC);
CREATE INDEX IF NOT EXISTS idx_sessions_profile ON sessions(profile_name);
CREATE INDEX IF NOT EXISTS idx_sessions_deleted ON sessions(deleted);

-- 消息表
CREATE TABLE IF NOT EXISTS messages (
	id         BIGSERIAL PRIMARY KEY,
	session_id TEXT NOT NULL,
	role       TEXT NOT NULL,
	content    TEXT NOT NULL DEFAULT '',
	metadata   JSONB,
	tokens_in  INTEGER NOT NULL DEFAULT 0,
	tokens_out INTEGER NOT NULL DEFAULT 0,
	cost       DOUBLE PRECISION NOT NULL DEFAULT 0,
	created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
	FOREIGN KEY (session_id) REFERENCES sessions(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_messages_session ON messages(session_id, id);
CREATE INDEX IF NOT EXISTS idx_messages_role ON messages(session_id, role);

-- 权限授予记录表
CREATE TABLE IF NOT EXISTS permission_grants (
	id         BIGSERIAL PRIMARY KEY,
	tool       TEXT NOT NULL,
	pattern    TEXT NOT NULL DEFAULT '',
	action     TEXT NOT NULL DEFAULT 'allow',
	created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
	expires_at TIMESTAMPTZ DEFAULT NULL
);

CREATE INDEX IF NOT EXISTS idx_perm_grants_tool ON permission_grants(tool);

-- OAuth token 持久化表
CREATE TABLE IF NOT EXISTS oauth_tokens (
	id            BIGSERIAL PRIMARY KEY,
	server_url    TEXT NOT NULL UNIQUE,
	access_token  TEXT NOT NULL,
	refresh_token TEXT DEFAULT '',
	token_type    TEXT DEFAULT 'Bearer',
	scopes        TEXT DEFAULT '',
	expires_at    TIMESTAMPTZ DEFAULT NULL,
	created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
	updated_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_oauth_tokens_server ON oauth_tokens(server_url);

-- 配置键值表
CREATE TABLE IF NOT EXISTS configs (
	key        TEXT PRIMARY KEY,
	value      TEXT NOT NULL DEFAULT '',
	updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- IM 通道配置表
CREATE TABLE IF NOT EXISTS channel_configs (
	platform    TEXT PRIMARY KEY,
	enabled     BOOLEAN NOT NULL DEFAULT FALSE,
	config_json JSONB NOT NULL DEFAULT '{}',
	updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- 通道 push 定时任务表
CREATE TABLE IF NOT EXISTS scheduled_pushes (
	id           TEXT PRIMARY KEY,
	name         TEXT NOT NULL,
	platform     TEXT NOT NULL,
	prompt       TEXT NOT NULL,
	interval_sec INTEGER NOT NULL,
	enabled      BOOLEAN NOT NULL DEFAULT TRUE,
	created_by   TEXT NOT NULL DEFAULT '',
	last_run_at  TIMESTAMPTZ,
	next_run_at  TIMESTAMPTZ,
	last_error   TEXT NOT NULL DEFAULT '',
	created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
	updated_at   TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_scheduled_pushes_platform ON scheduled_pushes(platform, enabled, created_at);

-- MCP 服务端配置表
CREATE TABLE IF NOT EXISTS mcp_servers (
	name       TEXT PRIMARY KEY,
	transport  TEXT NOT NULL DEFAULT 'stdio',
	command    TEXT NOT NULL DEFAULT '',
	args       JSONB NOT NULL DEFAULT '[]',
	env        JSONB NOT NULL DEFAULT '{}',
	url        TEXT NOT NULL DEFAULT '',
	headers    JSONB NOT NULL DEFAULT '{}',
	timeout    TEXT NOT NULL DEFAULT '30s',
	enabled    BOOLEAN NOT NULL DEFAULT TRUE,
	updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- 外部资源配置表
CREATE TABLE IF NOT EXISTS external_resources (
	name        TEXT PRIMARY KEY,
	type        TEXT NOT NULL DEFAULT '',
	environment TEXT NOT NULL DEFAULT '',
	description TEXT NOT NULL DEFAULT '',
	connection  TEXT NOT NULL DEFAULT '',
	endpoint    TEXT NOT NULL DEFAULT '',
	credentials JSONB NOT NULL DEFAULT '{}',
	read_only   BOOLEAN NOT NULL DEFAULT FALSE,
	enabled     BOOLEAN NOT NULL DEFAULT TRUE,
	updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Worker 预留表
CREATE TABLE IF NOT EXISTS nodes (
	id             TEXT PRIMARY KEY,
	name           TEXT NOT NULL,
	role           TEXT NOT NULL DEFAULT 'worker',
	status         TEXT NOT NULL DEFAULT 'offline',
	address        TEXT NOT NULL DEFAULT '',
	capabilities   JSONB NOT NULL DEFAULT '[]',
	last_heartbeat TIMESTAMPTZ,
	created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
	updated_at     TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS task_queue (
	id          TEXT PRIMARY KEY,
	node_id     TEXT,
	session_id  TEXT,
	request     TEXT NOT NULL,
	status      TEXT NOT NULL DEFAULT 'pending',
	priority    INTEGER NOT NULL DEFAULT 0,
	result_json JSONB,
	error       TEXT,
	created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
	updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
	FOREIGN KEY (node_id) REFERENCES nodes(id)
);

CREATE INDEX IF NOT EXISTS idx_task_queue_status ON task_queue(status);
CREATE INDEX IF NOT EXISTS idx_task_queue_node ON task_queue(node_id);

-- LLM 提供商配置表
CREATE TABLE IF NOT EXISTS llm_providers (
	name            TEXT PRIMARY KEY,
	provider_type   TEXT NOT NULL DEFAULT 'openai',
	api_key         TEXT NOT NULL DEFAULT '',
	base_url        TEXT NOT NULL DEFAULT '',
	is_default      BOOLEAN NOT NULL DEFAULT FALSE,
	enabled         BOOLEAN NOT NULL DEFAULT TRUE,
	config_json     JSONB NOT NULL DEFAULT '{}',
	api_format      TEXT NOT NULL DEFAULT 'chat',
	service_type    TEXT NOT NULL DEFAULT 'llm',
	created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
	updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_llm_providers_default ON llm_providers(is_default) WHERE is_default = TRUE;

-- LLM 模型配置表
CREATE TABLE IF NOT EXISTS llm_models (
	name            TEXT PRIMARY KEY,
	provider_name   TEXT NOT NULL,
	model           TEXT NOT NULL,
	base_url        TEXT NOT NULL DEFAULT '',
	api_key         TEXT NOT NULL DEFAULT '',
	is_default      BOOLEAN NOT NULL DEFAULT FALSE,
	enabled         BOOLEAN NOT NULL DEFAULT TRUE,
	config_json     JSONB NOT NULL DEFAULT '{}',
	created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
	updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
	FOREIGN KEY (provider_name) REFERENCES llm_providers(name) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_llm_models_provider ON llm_models(provider_name);
CREATE INDEX IF NOT EXISTS idx_llm_models_default ON llm_models(is_default) WHERE is_default = TRUE;

-- 记忆表
CREATE TABLE IF NOT EXISTS memories (
	id           BIGSERIAL PRIMARY KEY,
	type         TEXT NOT NULL DEFAULT 'user',
	content      TEXT NOT NULL,
	tags         TEXT NOT NULL DEFAULT '[]',
	session_id   TEXT NOT NULL DEFAULT '',
	metadata     JSONB NOT NULL DEFAULT '{}',
	embedding    BYTEA,
	search_vector TSVECTOR,
	created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
	updated_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
	accessed_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
	access_count INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX IF NOT EXISTS idx_memories_type ON memories(type);
CREATE INDEX IF NOT EXISTS idx_memories_session ON memories(session_id);
CREATE INDEX IF NOT EXISTS idx_memories_accessed ON memories(accessed_at DESC);
CREATE INDEX IF NOT EXISTS idx_memories_created ON memories(created_at DESC);
CREATE INDEX IF NOT EXISTS idx_memories_search ON memories USING GIN(search_vector);
CREATE INDEX IF NOT EXISTS idx_memories_governance_expires
	ON memories (((metadata->'governance'->>'expires_at')));
CREATE INDEX IF NOT EXISTS idx_memories_governance_source
	ON memories (((metadata->'governance'->>'source')));

-- 自动更新 search_vector 的触发器
CREATE OR REPLACE FUNCTION memories_search_update() RETURNS trigger AS $$
BEGIN
	NEW.search_vector := to_tsvector('simple', COALESCE(NEW.content, '') || ' ' || COALESCE(NEW.tags, ''));
	RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS memories_search_trigger ON memories;
CREATE TRIGGER memories_search_trigger
	BEFORE INSERT OR UPDATE ON memories
	FOR EACH ROW EXECUTE FUNCTION memories_search_update();

-- 成本追踪表（P2-4）
CREATE TABLE IF NOT EXISTS usage_records (
	id               BIGSERIAL PRIMARY KEY,
	session_id       TEXT NOT NULL,
	user_id          TEXT NOT NULL DEFAULT '',
	model            TEXT NOT NULL,
	prompt_tokens    BIGINT NOT NULL DEFAULT 0,
	completion_tokens BIGINT NOT NULL DEFAULT 0,
	cost_usd         DOUBLE PRECISION NOT NULL DEFAULT 0,
	task_type        TEXT NOT NULL DEFAULT '',
	quality_case_id  TEXT NOT NULL DEFAULT '',
	prompt_version   TEXT NOT NULL DEFAULT '',
	failure_type     TEXT NOT NULL DEFAULT '',
	final_status     TEXT NOT NULL DEFAULT '',
	created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_usage_records_session ON usage_records(session_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_usage_records_model ON usage_records(model, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_usage_records_created ON usage_records(created_at DESC);

-- 可观测性：Traces 表（P2-6）
CREATE TABLE IF NOT EXISTS hive_traces (
	id            BIGSERIAL PRIMARY KEY,
	trace_id      TEXT NOT NULL,
	span_id       TEXT NOT NULL,
	parent_span_id TEXT,
	operation     TEXT NOT NULL,
	service       TEXT NOT NULL,
	session_id    TEXT,
	user_id       TEXT NOT NULL DEFAULT '',
	duration_ms   INTEGER NOT NULL DEFAULT 0,
	status        TEXT NOT NULL DEFAULT 'ok',
	attributes    JSONB,
	ts            TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_traces_trace_id   ON hive_traces(trace_id);
CREATE INDEX IF NOT EXISTS idx_traces_session_id ON hive_traces(session_id);
CREATE INDEX IF NOT EXISTS idx_traces_ts         ON hive_traces(ts DESC);
CREATE INDEX IF NOT EXISTS idx_traces_operation  ON hive_traces(operation);

-- 可观测性：Metrics 表（P2-6）
CREATE TABLE IF NOT EXISTS hive_metrics (
	id     BIGSERIAL PRIMARY KEY,
	name   TEXT NOT NULL,
	value  DOUBLE PRECISION NOT NULL,
	labels JSONB,
	ts     TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_metrics_name   ON hive_metrics(name);
CREATE INDEX IF NOT EXISTS idx_metrics_ts     ON hive_metrics(ts DESC);
CREATE INDEX IF NOT EXISTS idx_metrics_labels ON hive_metrics USING GIN(labels);

-- 可观测性：Logs 表（P2-6）
CREATE TABLE IF NOT EXISTS hive_logs (
    id         BIGSERIAL PRIMARY KEY,
    level      TEXT NOT NULL,
    message    TEXT NOT NULL,
    trace_id   TEXT,
    span_id    TEXT,
    session_id TEXT,
    attributes JSONB,
    ts         TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_logs_level      ON hive_logs(level);
CREATE INDEX IF NOT EXISTS idx_logs_trace_id   ON hive_logs(trace_id);
CREATE INDEX IF NOT EXISTS idx_logs_session_id ON hive_logs(session_id);
CREATE INDEX IF NOT EXISTS idx_logs_ts         ON hive_logs(ts DESC);

-- Agent Quality regression candidate 表
CREATE TABLE IF NOT EXISTS agentquality_candidates (
	id               TEXT PRIMARY KEY,
	status           TEXT NOT NULL DEFAULT 'new',
	route            TEXT NOT NULL DEFAULT '',
	session_id       TEXT NOT NULL DEFAULT '',
	replay_ref       TEXT NOT NULL DEFAULT '',
	input            TEXT NOT NULL DEFAULT '',
	case_json        JSONB NOT NULL DEFAULT '{}',
	failure_type     TEXT NOT NULL DEFAULT '',
	risk             TEXT NOT NULL DEFAULT 'safe',
	fingerprint      TEXT NOT NULL DEFAULT '',
	source_event     JSONB NOT NULL DEFAULT '{}',
	suggestions_json JSONB NOT NULL DEFAULT '[]',
	review_note      TEXT NOT NULL DEFAULT '',
	created_by       TEXT NOT NULL DEFAULT '',
	reviewed_by      TEXT NOT NULL DEFAULT '',
	promoted_case_id TEXT NOT NULL DEFAULT '',
	cluster_id       TEXT NOT NULL DEFAULT '',
	verify_result    JSONB NOT NULL DEFAULT '{}',
	created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
	updated_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
	reviewed_at      TIMESTAMPTZ,
	last_verified_at TIMESTAMPTZ
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_agentquality_candidates_fingerprint
	ON agentquality_candidates(fingerprint)
	WHERE status IN ('new', 'reviewing', 'approved');
CREATE INDEX IF NOT EXISTS idx_agentquality_candidates_status_created
	ON agentquality_candidates(status, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_agentquality_candidates_session
	ON agentquality_candidates(session_id, created_at DESC)
	WHERE session_id != '';

-- Agent Quality 自动优化建议表。建议只做人工审批记录，不自动改生产 prompt/tool/skill。
CREATE TABLE IF NOT EXISTS agentquality_optimization_suggestions (
	id                  TEXT PRIMARY KEY,
	status              TEXT NOT NULL DEFAULT 'pending',
	target              TEXT NOT NULL DEFAULT '',
	kind                TEXT NOT NULL DEFAULT '',
	title               TEXT NOT NULL DEFAULT '',
	rationale           TEXT NOT NULL DEFAULT '',
	current_value       TEXT NOT NULL DEFAULT '',
	proposed_value      TEXT NOT NULL DEFAULT '',
	diff_format         TEXT NOT NULL DEFAULT 'text',
	source_candidate_id TEXT NOT NULL DEFAULT '',
	source_eval_diff_id TEXT NOT NULL DEFAULT '',
	source_event        JSONB NOT NULL DEFAULT '{}',
	review_required     BOOLEAN NOT NULL DEFAULT TRUE,
	created_by          TEXT NOT NULL DEFAULT '',
	approved_by         TEXT NOT NULL DEFAULT '',
	approval_note       TEXT NOT NULL DEFAULT '',
	apply_status        TEXT NOT NULL DEFAULT 'unapplied',
	applied_by          TEXT NOT NULL DEFAULT '',
	apply_error         TEXT NOT NULL DEFAULT '',
	created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
	updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
	approved_at         TIMESTAMPTZ,
	applied_at          TIMESTAMPTZ,
	expires_at          TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS idx_agentquality_opt_suggestions_status_created
	ON agentquality_optimization_suggestions(status, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_agentquality_opt_suggestions_source_candidate
	ON agentquality_optimization_suggestions(source_candidate_id, created_at DESC)
	WHERE source_candidate_id != '';
CREATE INDEX IF NOT EXISTS idx_agentquality_opt_suggestions_target
	ON agentquality_optimization_suggestions(target, created_at DESC);

CREATE TABLE IF NOT EXISTS optimization_eval_diffs (
	id                       TEXT PRIMARY KEY,
	status                   TEXT NOT NULL DEFAULT '',
	baseline_run_id          TEXT NOT NULL DEFAULT '',
	treatment_run_id         TEXT NOT NULL DEFAULT '',
	success_rate_delta       DOUBLE PRECISION NOT NULL DEFAULT 0,
	average_cost_delta_usd   DOUBLE PRECISION NOT NULL DEFAULT 0,
	average_latency_delta_ms DOUBLE PRECISION NOT NULL DEFAULT 0,
	success_p_value          DOUBLE PRECISION NOT NULL DEFAULT 1,
	payload                  JSONB NOT NULL DEFAULT '{}',
	created_at               TIMESTAMPTZ NOT NULL DEFAULT NOW(),
	updated_at               TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_optimization_eval_diffs_treatment
	ON optimization_eval_diffs(treatment_run_id, updated_at DESC)
	WHERE treatment_run_id != '';
CREATE INDEX IF NOT EXISTS idx_optimization_eval_diffs_status
	ON optimization_eval_diffs(status, updated_at DESC);

CREATE TABLE IF NOT EXISTS optimization_approvals (
	id            TEXT PRIMARY KEY,
	subject_id    TEXT NOT NULL,
	subject_type  TEXT NOT NULL,
	action        TEXT NOT NULL,
	reviewer      TEXT NOT NULL DEFAULT '',
	reviewer_role TEXT NOT NULL DEFAULT '',
	note          TEXT NOT NULL DEFAULT '',
	created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_optimization_approvals_subject
	ON optimization_approvals(subject_id, created_at DESC);

CREATE TABLE IF NOT EXISTS optimization_rollback_alerts (
	id                       TEXT PRIMARY KEY,
	status                   TEXT NOT NULL DEFAULT 'open',
	eval_diff_id             TEXT NOT NULL DEFAULT '',
	treatment_run_id         TEXT NOT NULL DEFAULT '',
	reasons                  JSONB NOT NULL DEFAULT '[]',
	success_rate_delta       DOUBLE PRECISION NOT NULL DEFAULT 0,
	average_latency_delta_ms DOUBLE PRECISION NOT NULL DEFAULT 0,
	created_at               TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_optimization_rollback_alerts_status
	ON optimization_rollback_alerts(status, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_optimization_rollback_alerts_eval_diff
	ON optimization_rollback_alerts(eval_diff_id, created_at DESC)
	WHERE eval_diff_id != '';

CREATE TABLE IF NOT EXISTS optimization_rollbacks (
	id            TEXT PRIMARY KEY,
	suggestion_id TEXT NOT NULL,
	alert_id      TEXT NOT NULL DEFAULT '',
	trigger       TEXT NOT NULL DEFAULT '',
	triggered_by  TEXT NOT NULL DEFAULT '',
	rollout       JSONB NOT NULL DEFAULT '{}',
	created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_optimization_rollbacks_suggestion
	ON optimization_rollbacks(suggestion_id, created_at DESC);

CREATE TABLE IF NOT EXISTS embedding_backlog (
	id          BIGSERIAL PRIMARY KEY,
	memory_id   BIGINT NOT NULL,
	user_id     TEXT NOT NULL DEFAULT '',
	content     TEXT NOT NULL DEFAULT '',
	vector_space TEXT NOT NULL DEFAULT 'memory:default',
	status      TEXT NOT NULL DEFAULT 'pending',
	attempts    INTEGER NOT NULL DEFAULT 0,
	claimed_by TEXT NOT NULL DEFAULT '',
	claimed_at TIMESTAMPTZ,
	next_run_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
	last_error  TEXT NOT NULL DEFAULT '',
	created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
	updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_embedding_backlog_status_next
	ON embedding_backlog(status, next_run_at ASC, id ASC);
CREATE INDEX IF NOT EXISTS idx_embedding_backlog_memory
	ON embedding_backlog(memory_id);

-- 自动优化实际应用结果表：工具描述覆盖与 memory governance 策略。
CREATE TABLE IF NOT EXISTS optimization_tool_descriptions (
	tool_name   TEXT PRIMARY KEY,
	description TEXT NOT NULL DEFAULT '',
	updated_by  TEXT NOT NULL DEFAULT '',
	updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS memory_governance_policies (
	name        TEXT PRIMARY KEY,
	policy_json JSONB NOT NULL DEFAULT '{}',
	updated_by  TEXT NOT NULL DEFAULT '',
	updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS optimization_rollouts (
	id              TEXT PRIMARY KEY,
	suggestion_id   TEXT NOT NULL UNIQUE,
	target          TEXT NOT NULL DEFAULT '',
	target_key      TEXT NOT NULL DEFAULT '',
	previous_value  TEXT NOT NULL DEFAULT '',
	previous_exists BOOLEAN NOT NULL DEFAULT FALSE,
	applied_value   TEXT NOT NULL DEFAULT '',
	status          TEXT NOT NULL DEFAULT 'applied',
	applied_by      TEXT NOT NULL DEFAULT '',
	rolled_back_by  TEXT NOT NULL DEFAULT '',
	created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
	updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
	rolled_back_at  TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS idx_optimization_rollouts_status
	ON optimization_rollouts(status, updated_at DESC);

CREATE TABLE IF NOT EXISTS hive_step_snapshots (
	id BIGSERIAL PRIMARY KEY,
	session_id TEXT NOT NULL,
	snapshot_seq INTEGER NOT NULL,
	trace_id TEXT NOT NULL DEFAULT '',
	span_id TEXT NOT NULL DEFAULT '',
	iteration INTEGER NOT NULL DEFAULT 0,
	message_count INTEGER NOT NULL DEFAULT 0,
	messages JSONB NOT NULL DEFAULT '[]'::jsonb,
	sessiontodo JSONB,
	memory_refs JSONB,
	created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
	UNIQUE(session_id, snapshot_seq)
);
CREATE INDEX IF NOT EXISTS idx_step_snapshots_session_seq
	ON hive_step_snapshots(session_id, snapshot_seq);
CREATE INDEX IF NOT EXISTS idx_step_snapshots_trace
	ON hive_step_snapshots(trace_id) WHERE trace_id != '';

-- Quality Workbench 持久化表
CREATE TABLE IF NOT EXISTS agentquality_grouping_rules (
	id         TEXT PRIMARY KEY,
	name       TEXT NOT NULL DEFAULT '',
	priority   INTEGER NOT NULL DEFAULT 0,
	enabled    BOOLEAN NOT NULL DEFAULT TRUE,
	rule_json  JSONB NOT NULL DEFAULT '{}',
	created_by TEXT NOT NULL DEFAULT '',
	created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
	updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_agentquality_grouping_rules_priority
	ON agentquality_grouping_rules(enabled, priority ASC, id ASC);

CREATE TABLE IF NOT EXISTS qualityworkbench_replay_jobs (
	id          TEXT PRIMARY KEY DEFAULT ('replay_' || md5(random()::text || clock_timestamp()::text)),
	batch_id    TEXT NOT NULL,
	kind        TEXT NOT NULL,
	target_ids  JSONB NOT NULL DEFAULT '[]',
	status      TEXT NOT NULL DEFAULT 'queued',
	max_attempt INTEGER NOT NULL DEFAULT 1,
	attempt     INTEGER NOT NULL DEFAULT 0,
	result      JSONB NOT NULL DEFAULT '{}',
	error       TEXT NOT NULL DEFAULT '',
	created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
	updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_qualityworkbench_replay_jobs_batch
	ON qualityworkbench_replay_jobs(batch_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_qualityworkbench_replay_jobs_status
	ON qualityworkbench_replay_jobs(status, created_at DESC);

CREATE TABLE IF NOT EXISTS qualityworkbench_batch_eval_runs (
	id         TEXT PRIMARY KEY DEFAULT ('eval_' || md5(random()::text || clock_timestamp()::text)),
	batch_id   TEXT NOT NULL,
	kind       TEXT NOT NULL,
	status     TEXT NOT NULL,
	summary    JSONB NOT NULL DEFAULT '{}',
	diff       JSONB NOT NULL DEFAULT '{}',
	case_results JSONB NOT NULL DEFAULT '[]',
	created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
	updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_qualityworkbench_batch_eval_runs_batch
	ON qualityworkbench_batch_eval_runs(batch_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_qualityworkbench_batch_eval_runs_status
	ON qualityworkbench_batch_eval_runs(status, created_at DESC);

CREATE TABLE IF NOT EXISTS qualityworkbench_weekly_reports (
	id         TEXT PRIMARY KEY,
	week_start DATE NOT NULL,
	title      TEXT NOT NULL DEFAULT 'Quality Workbench Weekly Report',
	summary    JSONB NOT NULL DEFAULT '{}',
	markdown   TEXT NOT NULL DEFAULT '',
	created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
	updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_qualityworkbench_weekly_reports_week_start
	ON qualityworkbench_weekly_reports(week_start DESC);
-- 认证 Provider 配置表
CREATE TABLE IF NOT EXISTS auth_providers (
    name          TEXT PRIMARY KEY,
    provider_type TEXT NOT NULL DEFAULT 'feishu',
    enabled       BOOLEAN NOT NULL DEFAULT FALSE,
    config_json   JSONB NOT NULL DEFAULT '{}',
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- 用户表
CREATE TABLE IF NOT EXISTS users (
    id              TEXT PRIMARY KEY,
    external_id     TEXT NOT NULL DEFAULT '',
    auth_provider   TEXT NOT NULL DEFAULT '',
    display_name    TEXT NOT NULL DEFAULT '',
    email           TEXT NOT NULL DEFAULT '',
    avatar_url      TEXT NOT NULL DEFAULT '',
    department      TEXT NOT NULL DEFAULT '',
    role            TEXT NOT NULL DEFAULT 'user',
    status          TEXT NOT NULL DEFAULT 'active',
    last_login_at   TIMESTAMPTZ,
    last_login_ip   TEXT NOT NULL DEFAULT '',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_users_external_provider
    ON users(external_id, auth_provider) WHERE external_id != '';
CREATE INDEX IF NOT EXISTS idx_users_role ON users(role);
CREATE INDEX IF NOT EXISTS idx_users_status ON users(status);

-- 登录历史表
CREATE TABLE IF NOT EXISTS login_history (
    id            BIGSERIAL PRIMARY KEY,
    user_id       TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    auth_provider TEXT NOT NULL DEFAULT '',
    ip_address    TEXT NOT NULL DEFAULT '',
    user_agent    TEXT NOT NULL DEFAULT '',
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_login_history_user
    ON login_history(user_id, created_at DESC);

-- 用户配额表（Phase 5B）
CREATE TABLE IF NOT EXISTS user_quotas (
    user_id        TEXT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    token_quota    BIGINT NOT NULL DEFAULT 0,
    token_used     BIGINT NOT NULL DEFAULT 0,
    quota_reset_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- 飞书事件去重表(Phase 0 P0-#8 两阶段 claim + dedup)。
-- 来源:migrations/20260423000000_feishu_dedup.sql
CREATE TABLE IF NOT EXISTS feishu_event_dedup (
    event_id     VARCHAR(255) PRIMARY KEY,
    claimed_at   TIMESTAMP WITH TIME ZONE,
    processed    BOOLEAN NOT NULL DEFAULT FALSE,
    processed_at TIMESTAMP WITH TIME ZONE
);
CREATE INDEX IF NOT EXISTS idx_feishu_dedup_claimed_unprocessed
    ON feishu_event_dedup(claimed_at) WHERE processed = FALSE;

-- 飞书出站重试队列(Phase 0 P0-#7 handler 永返 nil + 失败入队;Phase 5 加 tenant_key 列)。
-- 来源:migrations/20260423000001 + 20260426000002
CREATE TABLE IF NOT EXISTS feishu_outbound_retry_queue (
    id            SERIAL PRIMARY KEY,
    message_id    VARCHAR(255) NOT NULL,
    platform      VARCHAR(50) NOT NULL,
    chat_id       VARCHAR(255) NOT NULL,
    sender_id     VARCHAR(255) NOT NULL,
    reason        VARCHAR(100) NOT NULL,
    error_msg     TEXT,
    payload       JSONB,
    created_at    TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP,
    next_retry_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP,
    retry_count   INT DEFAULT 0,
    tenant_key    TEXT NOT NULL DEFAULT 'default'
);
CREATE INDEX IF NOT EXISTS idx_feishu_retry_queue_next
    ON feishu_outbound_retry_queue(next_retry_at) WHERE retry_count < 5;
CREATE INDEX IF NOT EXISTS idx_feishu_retry_queue_tenant
    ON feishu_outbound_retry_queue(tenant_key);

-- 飞书 chat 状态表(Phase 2 M3 lifecycle + M10 治理 + Phase 7 model/agent override)。
-- 来源:migrations/20260424000002_feishu_chat_state.sql + chat_state_repo.go 后续扩字段
CREATE TABLE IF NOT EXISTS feishu_chat_state (
    platform                  VARCHAR(50) NOT NULL,
    tenant_key                VARCHAR(255) NOT NULL,
    chat_id                   VARCHAR(255) NOT NULL,
    session_id                VARCHAR(255) NOT NULL DEFAULT '',
    model_override            VARCHAR(255) NOT NULL DEFAULT '',
    agent_profile             VARCHAR(255) NOT NULL DEFAULT '',
    state                     VARCHAR(32) NOT NULL DEFAULT 'active',
    mute_until                TIMESTAMP WITH TIME ZONE,
    rollout_mode              VARCHAR(32) NOT NULL DEFAULT 'allow',
    suppress_outbound         BOOLEAN NOT NULL DEFAULT FALSE,
    last_lifecycle_event_id   VARCHAR(255) NOT NULL DEFAULT '',
    last_lifecycle_event_time BIGINT NOT NULL DEFAULT 0,
    updated_at                TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_by                VARCHAR(255) NOT NULL DEFAULT '',
    PRIMARY KEY (platform, tenant_key, chat_id),
    CONSTRAINT chk_feishu_chat_state_state CHECK (state IN ('active', 'evicted')),
    CONSTRAINT chk_feishu_chat_state_rollout_mode CHECK (rollout_mode IN ('allow', 'deny'))
);
-- 增量列(老 DB 已建表时 ADD COLUMN IF NOT EXISTS 幂等补)
ALTER TABLE feishu_chat_state
    ADD COLUMN IF NOT EXISTS model_override VARCHAR(255) NOT NULL DEFAULT '';
ALTER TABLE feishu_chat_state
    ADD COLUMN IF NOT EXISTS agent_profile VARCHAR(255) NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS idx_feishu_chat_state_session_id_nonempty
    ON feishu_chat_state (session_id) WHERE session_id <> '';
CREATE INDEX IF NOT EXISTS idx_feishu_chat_state_suppressed_lookup
    ON feishu_chat_state (platform, tenant_key, chat_id)
    WHERE suppress_outbound = TRUE OR mute_until IS NOT NULL;
`

// pgFixTextToTimestamptz 修复旧版数据库中 TEXT 时间列 → TIMESTAMPTZ 的迁移 SQL
// 使用 information_schema 判断列类型，仅在列为 text 时执行 ALTER，确保幂等
const pgFixTextToTimestamptz = `
DO $$
DECLARE
    r RECORD;
BEGIN
    -- 需要从 TEXT 迁移到 TIMESTAMPTZ 的列清单
    FOR r IN
        SELECT * FROM (VALUES
            ('sessions',           'created_at'),
            ('sessions',           'updated_at'),
            ('sessions',           'last_accessed_at'),
            ('messages',           'created_at'),
            ('permission_grants',  'created_at'),
            ('permission_grants',  'expires_at'),
            ('oauth_tokens',       'expires_at'),
            ('oauth_tokens',       'created_at'),
            ('oauth_tokens',       'updated_at'),
            ('configs',            'updated_at'),
            ('channel_configs',    'updated_at'),
            ('mcp_servers',        'updated_at'),
            ('nodes',              'last_heartbeat'),
            ('nodes',              'created_at'),
            ('nodes',              'updated_at'),
            ('task_queue',         'created_at'),
            ('task_queue',         'updated_at'),
            ('memories',           'created_at'),
            ('memories',           'updated_at'),
            ('memories',           'accessed_at'),
            ('llm_providers',      'created_at'),
            ('llm_providers',      'updated_at'),
            ('llm_models',         'created_at'),
            ('llm_models',         'updated_at')
        ) AS t(table_name, column_name)
    LOOP
        -- 仅当表和列存在且类型为 text/character varying 时才执行 ALTER
        IF EXISTS (
            SELECT 1 FROM information_schema.columns
            WHERE table_schema = 'public'
              AND table_name   = r.table_name
              AND column_name  = r.column_name
              AND data_type    IN ('text', 'character varying')
        ) THEN
            EXECUTE format(
                'ALTER TABLE %I ALTER COLUMN %I TYPE TIMESTAMPTZ USING CASE WHEN %I = '''' THEN NULL ELSE %I::timestamptz END',
                r.table_name, r.column_name, r.column_name, r.column_name
            );
            RAISE NOTICE 'ALTER TABLE %.% TEXT → TIMESTAMPTZ', r.table_name, r.column_name;
        END IF;
    END LOOP;
END $$;
`

// pgSeedDefaultConfigs 种子默认运行时配置到 configs KV 表（幂等，不覆盖已有值）
const pgSeedDefaultConfigs = `
INSERT INTO configs (key, value) VALUES
  -- HITL
  ('hitl.enabled',                   'false'),
  ('hitl.step_confirmation',         'none'),
  ('hitl.input_timeout',             '30m'),
  ('hitl.websocket_enabled',         'false'),
  ('hitl.websocket_insecure_origin', 'false'),
  ('hitl.websocket_max_conn_per_ip', '5'),
  ('hitl.permission_rules',          '[{"tool_name":"read_file","action":"allow"},{"tool_name":"glob","action":"allow"},{"tool_name":"grep","action":"allow"},{"tool_name":"ls","action":"allow"},{"tool_name":"websearch","action":"allow"},{"tool_name":"webfetch","action":"allow"},{"tool_name":"browser_interact","action":"allow"},{"tool_name":"memory","action":"allow"},{"tool_name":"skill","action":"allow"},{"tool_name":"task","action":"allow"},{"tool_name":"question","action":"allow"},{"tool_name":"batch","action":"allow"},{"tool_name":"write_file","action":"ask"},{"tool_name":"edit","action":"ask"},{"tool_name":"bash","action":"ask"},{"tool_name":"multiedit","action":"ask"},{"tool_name":"apply_patch","action":"ask"},{"tool_name":"taskboard","action":"ask"},{"tool_name":"create_tool","action":"ask"},{"tool_name":"remove_tool","action":"ask"},{"tool_name":"spawn_agent","action":"ask"},{"tool_name":"parallel_dispatch","action":"ask"},{"tool_name":"send_im_message","action":"ask"},{"tool_name":"feishu_api","action":"ask"},{"tool_name":"wechat_send_rich_message","action":"ask"},{"tool_name":"wechat_contacts","action":"ask"},{"tool_name":"wechat_groups","action":"ask"},{"tool_name":"wechat_profile","action":"ask"},{"tool_name":"wechat_moments","action":"ask"},{"tool_name":"wechat_status","action":"ask"}]'),
  -- Agent
  ('agent.timeout',               '10m'),
  ('agent.max_concurrent_agents', '10'),
  ('agent.health_interval',       '10s'),
  ('agent.shell_timeout',         '10s'),
  ('agent.script_timeout',        '30s'),
  ('agent.ws_ping_interval',      '30s'),
  ('agent.sync_interval',         '5m'),
  ('agent.plan_runtime.enabled',  'true'),
  ('agent.plan_runtime.auto_continue', 'false'),
  ('agent.plan_runtime.max_auto_continue', '0'),
  -- Context Compression
  ('agent.context_compression.enabled',         'true'),
  ('agent.context_compression.strategy',        'llm_summary'),
  ('agent.context_compression.max_tokens',      '500000'),
  ('agent.context_compression.reserve_tokens',  '10000'),
  ('agent.context_compression.compact_timeout', '30s'),
  ('agent.context_compression.use_tiktoken',    'true'),
  ('agent.context_compression.lazy_mode',       'true'),
  ('agent.context_compression.lazy_threshold',  '500000'),
  -- Memory
  ('memory.enabled',           'true'),
  ('memory.max_memories',      '10000'),
  ('memory.retention_days',    '90'),
  ('memory.auto_extract',      'true'),
  ('memory.inject_max_tokens', '2000'),
  ('memory.inject_top_k',      '5'),
  ('memory.embedding_enabled', 'false'),
  ('memory.embedding_model',   ''),
  -- Misc
  ('prompt_language',             'en-US'),
  ('webui.enabled',               'true'),
  ('plugin.enabled',              'false'),
  ('plugin.auto_discover',        'false'),
  ('control_plane.enabled',       'false'),
  ('control_plane.max_sessions',  '100'),
  ('control_plane.rate_limit',    '10'),
  ('control_plane.rate_burst',    '20'),
  ('custom_tools_dir',            '.claw/tools'),
  ('sessions_dir',                '~/.claw/sessions'),
  ('channel.enabled',             'false'),
  -- MCP
  ('mcp.timeout',                 '30s'),
  -- Security
  ('security.enabled',            'true'),
  ('security.exec_rules',         '[{"pattern":"^ls\\s","policy":"allow","description":"允许 ls 命令"},{"pattern":"^cat\\s","policy":"allow","description":"允许 cat 命令"},{"pattern":"^echo\\s","policy":"allow","description":"允许 echo 命令"},{"pattern":"^grep\\s","policy":"allow","description":"允许 grep 命令"},{"pattern":"^find\\s","policy":"allow","description":"允许 find 命令"},{"pattern":"^go\\s+(build|test|vet|run)","policy":"allow","description":"允许 go 编译/测试"},{"pattern":"^git\\s+(status|log|diff|show|branch)","policy":"allow","description":"允许 git 只读操作"},{"pattern":"^git\\s","policy":"ask","description":"其他 git 命令需确认"},{"pattern":"^npm\\s+install","policy":"ask","description":"npm install 需确认"},{"pattern":"^curl\\s","policy":"ask","description":"curl 请求需确认"},{"pattern":"rm\\s+-rf","policy":"deny","description":"禁止 rm -rf"},{"pattern":"^sudo\\s","policy":"deny","description":"禁止 sudo"}]'),
  ('security.watch_env_vars',     '["PATH","HOME","OPENAI_API_KEY"]'),
  -- ACP Server
  ('acp_server.enabled',          'false'),
  ('acp_server.auth_method',      'none'),
  ('acp_server.max_sessions',     '50'),
  -- LSP
  ('lsp.enabled',                 'true'),
  ('lsp.timeout',                 '10s'),
  ('lsp.max_servers',             '5'),
  ('lsp.health_interval',         '30s'),
  ('lsp.languages',               '{"go":{"command":"gopls","args":["serve"],"extensions":[".go"]},"python":{"command":"pyright-langserver","args":["--stdio"],"extensions":[".py"]},"typescript":{"command":"typescript-language-server","args":["--stdio"],"extensions":[".ts",".tsx",".js",".jsx"]}}')
ON CONFLICT (key) DO NOTHING;
`

// pgAddAPIFormat 为已有数据库的 llm_providers 表添加 api_format 列
const pgAddAPIFormat = `
ALTER TABLE llm_providers ADD COLUMN IF NOT EXISTS api_format TEXT NOT NULL DEFAULT 'chat';
`

// pgAddServiceType 为已有数据库的 llm_providers 表添加 service_type 列
const pgAddServiceType = `
ALTER TABLE llm_providers ADD COLUMN IF NOT EXISTS service_type TEXT NOT NULL DEFAULT 'llm';
`

// pgAddMCPServerEnv 为已有数据库的 mcp_servers 表添加 env 列
const pgAddMCPServerEnv = `
ALTER TABLE mcp_servers ADD COLUMN IF NOT EXISTS env JSONB NOT NULL DEFAULT '{}';
`

// pgFixSecurityExecRules 将旧的 glob 格式 security.exec_rules 替换为正则格式。
// MatchPattern 已从 glob 改为正则，旧规则（^ls\s 等）本身已是正则，但
// 若数据库中存的是旧 glob 格式（ls*、rm -rf * 等），需要覆盖更新。
// 使用 DO NOTHING 以外的 UPDATE 确保已有行被修正。
const pgFixSecurityExecRules = `
UPDATE configs SET value = '[{"pattern":"^ls\\b","policy":"allow","description":"允许 ls 命令"},{"pattern":"^cat\\s","policy":"allow","description":"允许 cat 命令"},{"pattern":"^echo\\s","policy":"allow","description":"允许 echo 命令"},{"pattern":"^grep\\s","policy":"allow","description":"允许 grep 命令"},{"pattern":"^find\\s","policy":"allow","description":"允许 find 命令"},{"pattern":"^go\\s+(build|test|vet|run|mod|get|list|clean)","policy":"allow","description":"允许 go 工具链"},{"pattern":"^git\\s+(status|log|diff|show|branch|fetch|pull)","policy":"allow","description":"允许 git 只读操作"},{"pattern":"^git\\s+","policy":"ask","description":"其他 git 命令需确认"},{"pattern":"^npm\\s+install","policy":"ask","description":"npm install 需确认"},{"pattern":"^curl\\s","policy":"ask","description":"curl 请求需确认"},{"pattern":"^rm\\s+-rf","policy":"deny","description":"禁止 rm -rf"},{"pattern":"^sudo\\s","policy":"deny","description":"禁止 sudo"}]'
WHERE key = 'security.exec_rules'
  AND (value LIKE '%"ls*"%' OR value LIKE '%"rm -rf *"%' OR value LIKE '%^ls\\s%');
`

// pgAddPgvectorColumn 检测 pgvector 扩展可用性，可用时添加 embedding_vec 列
// 不可用时跳过，fallback 到 InMemoryVecStore
// 注意：HNSW 索引单独创建（CONCURRENTLY 不能在事务/DO 块内使用）
const pgAddPgvectorColumn = `
DO $$
BEGIN
    -- 尝试启用 pgvector 扩展（需要 superuser 或已预装）
    BEGIN
        CREATE EXTENSION IF NOT EXISTS vector;
    EXCEPTION WHEN OTHERS THEN
        RAISE NOTICE 'pgvector 扩展不可用，跳过 embedding_vec 列创建';
        RETURN;
    END;

    -- 添加 vector 类型列（幂等）
    IF NOT EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = 'public'
          AND table_name   = 'memories'
          AND column_name  = 'embedding_vec'
    ) THEN
        ALTER TABLE memories ADD COLUMN embedding_vec vector;
    END IF;
END $$;
`

// pgAddModelServiceType 为 llm_models 表添加 service_type 列
// 空字符串表示继承 provider 的 service_type，向后兼容
const pgAddModelServiceType = `
ALTER TABLE llm_models ADD COLUMN IF NOT EXISTS service_type TEXT NOT NULL DEFAULT '';
`

// pgAddPgvectorHNSWIndex 创建 HNSW 索引（CONCURRENTLY，不阻塞写入）
// 单独执行，不能放在 DO $$ 块或事务内
const pgAddPgvectorHNSWIndex = `
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_memories_embedding_vec_hnsw
    ON memories USING hnsw (embedding_vec vector_cosine_ops)
    WITH (m = 16, ef_construction = 64);
`

// IsPgvectorAvailable 检测数据库中 pgvector 扩展和 embedding_vec 列是否可用
func IsPgvectorAvailable(ctx context.Context, pool *pgxpool.Pool) bool {
	var exists bool
	err := pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM information_schema.columns
			WHERE table_schema = 'public'
			  AND table_name   = 'memories'
			  AND column_name  = 'embedding_vec'
		)
	`).Scan(&exists)
	return err == nil && exists
}

// IsBackfillComplete 检测是否所有 BYTEA 向量都已迁移到 embedding_vec 列
// 当 BYTEA 列无数据（全新部署）或所有 BYTEA 行都已有对应 embedding_vec 时返回 true
func IsBackfillComplete(ctx context.Context, pool *pgxpool.Pool) bool {
	var total, migrated int
	if err := pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM memories WHERE embedding IS NOT NULL`).Scan(&total); err != nil {
		// 查询失败时保守返回 false，避免误切 pgvector
		return false
	}
	if total == 0 {
		// 全新部署，无历史 BYTEA 数据，视为回填完成
		return true
	}
	if err := pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM memories WHERE embedding IS NOT NULL AND embedding_vec IS NOT NULL`).Scan(&migrated); err != nil {
		return false
	}
	return migrated >= total
}

// pgAddSkillsUserID 为 hive_skills 添加 user_id 列，升级主键为 (name, user_id)，
// 并将 pg_notify payload 从 raw name 改为 JSON {name, user_id, op}。
// 幂等：重复执行不报错（IF NOT EXISTS / DROP IF EXISTS / OR REPLACE）。
const pgAddSkillsUserID = `
ALTER TABLE hive_skills ADD COLUMN IF NOT EXISTS user_id TEXT NOT NULL DEFAULT '';

-- 主键升级：(name) → (name, user_id)。老库 hive_skills_pkey 仅含 name 列时才替换，
-- 已是复合主键（幂等二次执行）时走 ELSE 分支不动。
DO $$
DECLARE
    pk_cols INT;
BEGIN
    SELECT COUNT(*) INTO pk_cols
    FROM information_schema.key_column_usage
    WHERE table_schema = 'public'
      AND table_name   = 'hive_skills'
      AND constraint_name = 'hive_skills_pkey';

    IF pk_cols = 1 THEN
        ALTER TABLE hive_skills DROP CONSTRAINT hive_skills_pkey;
        ALTER TABLE hive_skills ADD CONSTRAINT hive_skills_pkey PRIMARY KEY (name, user_id);
    END IF;
END $$;

CREATE INDEX IF NOT EXISTS idx_hive_skills_user
    ON hive_skills(user_id) WHERE user_id != '';

-- 替换 notify function：payload 改 JSON {name, user_id, op}（pg_notify 8KB 限制够用）。
CREATE OR REPLACE FUNCTION hive_skills_notify() RETURNS trigger AS $$
BEGIN
    PERFORM pg_notify(
        'hive_skill_changed',
        json_build_object(
            'name',    COALESCE(NEW.name, OLD.name),
            'user_id', COALESCE(NEW.user_id, OLD.user_id, ''),
            'op',      TG_OP
        )::text
    );
    RETURN COALESCE(NEW, OLD);
END;
$$ LANGUAGE plpgsql;
`

// pgAddUserColumns 为已有表添加 user_id 列（幂等迁移）
const pgAddUserColumns = `
ALTER TABLE sessions ADD COLUMN IF NOT EXISTS user_id TEXT NOT NULL DEFAULT '';
ALTER TABLE sessions ADD COLUMN IF NOT EXISTS is_starred BOOLEAN NOT NULL DEFAULT FALSE;
CREATE INDEX IF NOT EXISTS idx_sessions_user ON sessions(user_id) WHERE user_id != '';
CREATE INDEX IF NOT EXISTS idx_sessions_starred ON sessions(user_id, is_starred) WHERE is_starred = TRUE;

ALTER TABLE usage_records ADD COLUMN IF NOT EXISTS user_id TEXT NOT NULL DEFAULT '';
ALTER TABLE usage_records ADD COLUMN IF NOT EXISTS task_type TEXT NOT NULL DEFAULT '';
ALTER TABLE usage_records ADD COLUMN IF NOT EXISTS quality_case_id TEXT NOT NULL DEFAULT '';
ALTER TABLE usage_records ADD COLUMN IF NOT EXISTS prompt_version TEXT NOT NULL DEFAULT '';
ALTER TABLE usage_records ADD COLUMN IF NOT EXISTS failure_type TEXT NOT NULL DEFAULT '';
ALTER TABLE usage_records ADD COLUMN IF NOT EXISTS final_status TEXT NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS idx_usage_records_user ON usage_records(user_id, created_at DESC) WHERE user_id != '';
CREATE INDEX IF NOT EXISTS idx_usage_records_quality_case ON usage_records(quality_case_id, created_at DESC) WHERE quality_case_id != '';

ALTER TABLE hive_traces ADD COLUMN IF NOT EXISTS user_id TEXT NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS idx_traces_user ON hive_traces(user_id) WHERE user_id != '';

ALTER TABLE hive_logs ADD COLUMN IF NOT EXISTS user_id TEXT NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS idx_logs_user ON hive_logs(user_id) WHERE user_id != '';

ALTER TABLE memories ADD COLUMN IF NOT EXISTS user_id TEXT NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS idx_memories_user ON memories(user_id) WHERE user_id != '';

ALTER TABLE agentquality_candidates ADD COLUMN IF NOT EXISTS suggestions_json JSONB NOT NULL DEFAULT '[]';
ALTER TABLE agentquality_candidates ADD COLUMN IF NOT EXISTS cluster_id TEXT NOT NULL DEFAULT '';
ALTER TABLE agentquality_candidates ADD COLUMN IF NOT EXISTS verify_result JSONB NOT NULL DEFAULT '{}';
ALTER TABLE agentquality_candidates ADD COLUMN IF NOT EXISTS last_verified_at TIMESTAMPTZ;
CREATE INDEX IF NOT EXISTS idx_agentquality_candidates_cluster
	ON agentquality_candidates(cluster_id, created_at DESC)
	WHERE cluster_id != '';

ALTER TABLE agentquality_optimization_suggestions ADD COLUMN IF NOT EXISTS apply_status TEXT NOT NULL DEFAULT 'unapplied';
ALTER TABLE agentquality_optimization_suggestions ADD COLUMN IF NOT EXISTS applied_by TEXT NOT NULL DEFAULT '';
ALTER TABLE agentquality_optimization_suggestions ADD COLUMN IF NOT EXISTS apply_error TEXT NOT NULL DEFAULT '';
ALTER TABLE agentquality_optimization_suggestions ADD COLUMN IF NOT EXISTS applied_at TIMESTAMPTZ;
ALTER TABLE agentquality_optimization_suggestions ADD COLUMN IF NOT EXISTS source_eval_diff_id TEXT NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS idx_agentquality_opt_suggestions_source_eval_diff
	ON agentquality_optimization_suggestions(source_eval_diff_id, created_at DESC)
	WHERE source_eval_diff_id != '';

ALTER TABLE embedding_backlog ADD COLUMN IF NOT EXISTS vector_space TEXT NOT NULL DEFAULT 'memory:default';

ALTER TABLE qualityworkbench_replay_jobs ADD COLUMN IF NOT EXISTS result JSONB NOT NULL DEFAULT '{}';
ALTER TABLE qualityworkbench_replay_jobs ADD COLUMN IF NOT EXISTS error TEXT NOT NULL DEFAULT '';
`

// pgAddSessionTodos 创建 session-scoped todos 独立表。
const pgAddSessionTodos = `
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

// pgMigrate 初始化 PostgreSQL 数据库表结构
func pgMigrate(ctx context.Context, pool *pgxpool.Pool, logger *zap.Logger) error {
	logger.Info("初始化 PostgreSQL 数据库表结构")

	if _, err := pool.Exec(ctx, pgInitSQL); err != nil {
		return errs.Wrap(errs.CodeStoreError, "PostgreSQL 建表失败", err)
	}

	// 修复旧版数据库中 TEXT 时间列 → TIMESTAMPTZ
	if _, err := pool.Exec(ctx, pgFixTextToTimestamptz); err != nil {
		return errs.Wrap(errs.CodeStoreError, "PostgreSQL 时间列迁移失败", err)
	}

	// 为已有数据库添加 api_format 列
	if _, err := pool.Exec(ctx, pgAddAPIFormat); err != nil {
		return errs.Wrap(errs.CodeStoreError, "PostgreSQL api_format 列迁移失败", err)
	}

	// 为已有数据库添加 service_type 列
	if _, err := pool.Exec(ctx, pgAddServiceType); err != nil {
		return errs.Wrap(errs.CodeStoreError, "PostgreSQL service_type 列迁移失败", err)
	}

	// 为已有数据库的 mcp_servers 表添加 env 列
	if _, err := pool.Exec(ctx, pgAddMCPServerEnv); err != nil {
		return errs.Wrap(errs.CodeStoreError, "PostgreSQL mcp_servers.env 列迁移失败", err)
	}

	// 为 llm_models 添加 service_type 列
	if _, err := pool.Exec(ctx, pgAddModelServiceType); err != nil {
		return errs.Wrap(errs.CodeStoreError, "PostgreSQL llm_models.service_type 列迁移失败", err)
	}

	// 添加 pgvector embedding_vec 列（pgvector 不可用时自动跳过）
	if _, err := pool.Exec(ctx, pgAddPgvectorColumn); err != nil {
		logger.Warn("pgvector 列迁移失败（将使用内存向量索引）", zap.Error(err))
	}

	// 创建 HNSW 索引（CONCURRENTLY，不阻塞写入；列不存在时自动跳过）
	if IsPgvectorAvailable(ctx, pool) {
		if _, err := pool.Exec(ctx, pgAddPgvectorHNSWIndex); err != nil {
			logger.Warn("pgvector HNSW 索引创建失败（不影响功能，顺序扫描仍可用）", zap.Error(err))
		}
	}

	// 种子默认运行时配置
	if _, err := pool.Exec(ctx, pgSeedDefaultConfigs); err != nil {
		return errs.Wrap(errs.CodeStoreError, "PostgreSQL 默认配置种子失败", err)
	}

	// 迁移 security.exec_rules：将旧的 glob 格式 pattern 替换为正则格式
	if _, err := pool.Exec(ctx, pgFixSecurityExecRules); err != nil {
		logger.Warn("security.exec_rules 迁移失败（不影响功能，可手动在 Web UI 更新规则）", zap.Error(err))
	}

	// 为已有表添加 user_id 列
	if _, err := pool.Exec(ctx, pgAddUserColumns); err != nil {
		return errs.Wrap(errs.CodeStoreError, "PostgreSQL user_id 列迁移失败", err)
	}

	// Agent Plan Runtime：session-scoped todo snapshot 表
	if _, err := pool.Exec(ctx, pgAddSessionTodos); err != nil {
		return errs.Wrap(errs.CodeStoreError, "PostgreSQL session todo 表迁移失败", err)
	}

	// Phase 6: user_session_prefs 表（per-user 收藏偏好）
	if _, err := pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS user_session_prefs (
			user_id    TEXT NOT NULL,
			session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
			is_starred BOOLEAN NOT NULL DEFAULT false,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			PRIMARY KEY (user_id, session_id)
		);
		CREATE INDEX IF NOT EXISTS idx_user_session_prefs_starred
			ON user_session_prefs (user_id, is_starred DESC);
	`); err != nil {
		return fmt.Errorf("创建 user_session_prefs 表失败: %w", err)
	}

	// Prompt 外部化：hive_prompts 表 + PG NOTIFY trigger
	if _, err := pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS hive_prompts (
			key         TEXT NOT NULL,
			language    TEXT NOT NULL DEFAULT '',
			content     TEXT NOT NULL,
			updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			updated_by  TEXT NOT NULL DEFAULT '',
			PRIMARY KEY (key, language)
		);
		CREATE INDEX IF NOT EXISTS idx_hive_prompts_key ON hive_prompts(key);

		-- PG NOTIFY trigger：任何 INSERT/UPDATE/DELETE 都通知所有监听者
		CREATE OR REPLACE FUNCTION hive_prompts_notify() RETURNS trigger AS $$
		BEGIN
			PERFORM pg_notify('hive_prompt_changed', COALESCE(NEW.key, OLD.key));
			RETURN COALESCE(NEW, OLD);
		END;
		$$ LANGUAGE plpgsql;

		DROP TRIGGER IF EXISTS hive_prompts_notify_trigger ON hive_prompts;
		CREATE TRIGGER hive_prompts_notify_trigger
			AFTER INSERT OR UPDATE OR DELETE ON hive_prompts
			FOR EACH ROW EXECUTE FUNCTION hive_prompts_notify();
	`); err != nil {
		return fmt.Errorf("创建 hive_prompts 表失败: %w", err)
	}

	// Skill 外部化：hive_skills 表 + PG NOTIFY trigger
	if _, err := pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS hive_skills (
			name        TEXT PRIMARY KEY,
			content     TEXT NOT NULL,
			level       TEXT NOT NULL DEFAULT 'user',
			path        TEXT,
			revision    INTEGER NOT NULL DEFAULT 1,
			updated_by  TEXT NOT NULL DEFAULT '',
			created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
		);
		CREATE INDEX IF NOT EXISTS idx_hive_skills_level ON hive_skills(level);

		CREATE OR REPLACE FUNCTION hive_skills_notify() RETURNS trigger AS $$
		BEGIN
			PERFORM pg_notify('hive_skill_changed', COALESCE(NEW.name, OLD.name));
			RETURN COALESCE(NEW, OLD);
		END;
		$$ LANGUAGE plpgsql;

		DROP TRIGGER IF EXISTS hive_skills_notify_trigger ON hive_skills;
		CREATE TRIGGER hive_skills_notify_trigger
			AFTER INSERT OR UPDATE OR DELETE ON hive_skills
			FOR EACH ROW EXECUTE FUNCTION hive_skills_notify();
	`); err != nil {
		return fmt.Errorf("创建 hive_skills 表失败: %w", err)
	}

	// hive-skill-on-demand MAJOR 2：为 hive_skills 添加 user_id 列 + 复合主键 + JSON payload。
	// 原 PRIMARY KEY (name) 导致两个用户推同名 personal skill 时最后一条 NOTIFY 覆盖前者；
	// 改为 PRIMARY KEY (name, user_id) 后每层独立，user_id="" = public。
	if _, err := pool.Exec(ctx, pgAddSkillsUserID); err != nil {
		return fmt.Errorf("hive_skills user_id 迁移失败: %w", err)
	}

	// Spec-driven cognition Phase 2：3 张表 + 触发器的建表块抽成 MigrateSpecTables，
	// 为 Sprint 3.2 TestMigration_DownReverts 提供独立可调 + down 反向的入口。
	// 此处的 pgMigrate 继续作为 canonical 建表 orchestrator，单一事实源。
	if err := MigrateSpecTables(ctx, pool); err != nil {
		return err
	}

	logger.Info("PostgreSQL 数据库初始化完成")
	return nil
}

// MigrateSpecTables 创建 spec-driven Phase 2 所需的 3 张表：
//   - hive_spec_changes            （Guard 2 CAS 一致性基座）
//   - hive_spec_change_events      （Guard 2 事件序列，CASCADE 依赖 changes）
//   - hive_spec_session_state      （Guard 1 focus_mru + changes JSONB cache）
//
// 包含配套索引 + `hive_spec_changes_notify` trigger/function。
// `pgMigrate` 在 init 阶段调用；`spec_migrate_test.go` 也直接调——
// 单一事实源，避免 test 里复刻 DDL 与 prod 漂移（spec_store_test.go 旧坑）。
//
// 幂等：全部 `CREATE ... IF NOT EXISTS`，重复调用不报错。
func MigrateSpecTables(ctx context.Context, pool *pgxpool.Pool) error {
	// hive_spec_changes + hive_spec_change_events + trigger（一起建避免 FK 竞态）。
	// CAS 走单事务 UPDATE ... WHERE revision = $expected，rows_affected=0 即冲突；
	// 事件表 sequence per change_id 单调，(change_id, sequence DESC) 支持尾查。
	if _, err := pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS hive_spec_changes (
			id                TEXT PRIMARY KEY,
			status            TEXT NOT NULL DEFAULT 'draft',
			title             TEXT NOT NULL DEFAULT '',
			current_task_key  TEXT NOT NULL DEFAULT '',
			revision          INTEGER NOT NULL DEFAULT 1,
			updated_by        TEXT NOT NULL DEFAULT '',
			parent_id         TEXT,
			created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			updated_at        TIMESTAMPTZ NOT NULL DEFAULT NOW()
		);
		CREATE INDEX IF NOT EXISTS idx_hive_spec_changes_updated_at
			ON hive_spec_changes(updated_at DESC);
		CREATE INDEX IF NOT EXISTS idx_hive_spec_changes_updated_by
			ON hive_spec_changes(updated_by, updated_at DESC);

		CREATE TABLE IF NOT EXISTS hive_spec_change_events (
			change_id     TEXT NOT NULL REFERENCES hive_spec_changes(id) ON DELETE CASCADE,
			sequence      INTEGER NOT NULL,
			event_type    TEXT NOT NULL,
			prev_task_key TEXT NOT NULL DEFAULT '',
			new_task_key  TEXT NOT NULL DEFAULT '',
			prev_status   TEXT NOT NULL DEFAULT '',
			new_status    TEXT NOT NULL DEFAULT '',
			actor_id      TEXT NOT NULL DEFAULT '',
			payload       JSONB NOT NULL DEFAULT '{}'::jsonb,
			created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			PRIMARY KEY (change_id, sequence)
		);
		CREATE INDEX IF NOT EXISTS idx_hive_spec_events_change_seq
			ON hive_spec_change_events(change_id, sequence DESC);

		CREATE OR REPLACE FUNCTION hive_spec_changes_notify() RETURNS trigger AS $$
		BEGIN
			PERFORM pg_notify('hive_spec_change_updated', COALESCE(NEW.id, OLD.id));
			RETURN COALESCE(NEW, OLD);
		END;
		$$ LANGUAGE plpgsql;

		DROP TRIGGER IF EXISTS hive_spec_changes_notify_trigger ON hive_spec_changes;
		CREATE TRIGGER hive_spec_changes_notify_trigger
			AFTER INSERT OR UPDATE OR DELETE ON hive_spec_changes
			FOR EACH ROW EXECUTE FUNCTION hive_spec_changes_notify();
	`); err != nil {
		return fmt.Errorf("创建 hive_spec_changes/events 表失败: %w", err)
	}

	// hive_spec_session_state — 每 session 一行，JSONB 存 focus_mru / changes map；
	// 此层是 cache，canonical 在 hive_spec_changes。同 session 无并发（session_loop
	// 串行），updated_at 仅 debug 用。
	if _, err := pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS hive_spec_session_state (
			session_id        TEXT PRIMARY KEY,
			active_change_id  TEXT NOT NULL DEFAULT '',
			focus_mru         JSONB NOT NULL DEFAULT '[]'::jsonb,
			changes           JSONB NOT NULL DEFAULT '{}'::jsonb,
			updated_at        TIMESTAMPTZ NOT NULL DEFAULT NOW()
		);
		CREATE INDEX IF NOT EXISTS idx_hive_spec_session_state_updated_at
			ON hive_spec_session_state(updated_at DESC);
	`); err != nil {
		return fmt.Errorf("创建 hive_spec_session_state 表失败: %w", err)
	}

	return nil
}

// DropSpecTables 反转 MigrateSpecTables，用于 Sprint 3.2 TestMigration_DownReverts
// 验证 up→down→up 循环的 schema 一致性 + sequence 起点复位（Codex R5-2 要求）。
//
// 运维纪律（runbook §4）：生产环境回退路径是 mode=legacy 短路（2μs 内返回，表休眠
// 零开销），**不**调本函数——drop 真实表会丢审计证据。本函数只对 test / drill。
//
// Drop 顺序：trigger → function → events（FK 依赖 changes）→ session_state（独立）
// → changes（最后）。CASCADE 做 belt+suspenders，防未来新增 FK 时忘调顺序。
func DropSpecTables(ctx context.Context, pool *pgxpool.Pool) error {
	if _, err := pool.Exec(ctx, `
		-- trigger 先 drop（依赖 changes 表），function 独立 drop（CASCADE 不会级联 function）
		DROP TRIGGER IF EXISTS hive_spec_changes_notify_trigger ON hive_spec_changes;
		DROP FUNCTION IF EXISTS hive_spec_changes_notify();
		DROP TABLE IF EXISTS hive_spec_change_events CASCADE;
		DROP TABLE IF EXISTS hive_spec_session_state CASCADE;
		DROP TABLE IF EXISTS hive_spec_changes CASCADE;
	`); err != nil {
		return fmt.Errorf("drop spec tables 失败: %w", err)
	}
	return nil
}
