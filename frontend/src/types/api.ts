// 会话相关
export interface Session {
  id: string;
  name: string;
  message_count: number;
  total_tokens: number;
  kb_domain_id?: string;
  last_accessed: string;
  created_at?: string;
  updated_at?: string;
  tags: string[];
  is_active: boolean;
  is_starred?: boolean;
}

export interface SessionDetail extends Session {
  created: string;
  updated: string;
}

export interface CreateSessionRequest {
  name: string;
  tags?: string[];
}

export interface UpdateSessionRequest {
  name?: string;
  kb_domain_id?: string;
  tags?: string[];
}

export interface FileAttachment {
  filename: string;
  mime_type: string;
  data?: string; // base64，仅发送当前 turn 时存在；历史消息走 asset_uri
  size: number; // bytes, for display
  asset_uri?: string;
  content_hash?: string;
}

export interface SendMessageRequest {
  content: string;
  attachments?: FileAttachment[];
  reasoning_effort?: string;
  kb_domain_id?: string;
}

export interface SendMessageResponse {
  content: string;
  completed: boolean;
}

// 消息相关
export interface ToolCall {
  id: string;
  name: string;
  arguments: string; // JSON 字符串
}

// 工具调用实时状态（由 tool_call WS 事件更新）
export interface ToolCallStatus {
  id: string;
  name: string;
  status: 'running' | 'success' | 'error';
  duration?: number; // 毫秒
  error?: string;
  recoverable?: boolean;
  terminal?: boolean;
  error_kind?: string;
  failure_type?: string;
  requires_user_approval?: boolean;
  suggested_action?: string;
}

// Token 用量
export interface MessageUsage {
  input_tokens: number;
  output_tokens: number;
}

export interface MessageArtifact {
  uri: string;
  title: string;
  type: 'markdown' | 'html' | 'code' | 'ppt';
  language?: string;
  mime_type: string;
  size: number;
  content_hash: string;
}

export interface MessageCitation {
  token?: string;
  Token?: string;
  namespace_id?: string;
  NamespaceID?: string;
  document_id?: string;
  DocumentID?: string;
  doc_id?: string;
  document_version?: string;
  DocumentVersion?: string;
  node_id?: string;
  NodeID?: string;
  node_path?: string;
  NodePath?: string;
  start_page?: number;
  StartPage?: number;
  end_page?: number;
  EndPage?: number;
  citation_text?: string;
  CitationText?: string;
  verified?: boolean;
  Verified?: boolean;
}

export interface Message {
  role: 'user' | 'assistant' | 'tool';
  content: string;
  reasoning_content?: string; // <think>...</think> 推理内容，可折叠展示
  tool_call_id?: string;
  tool_calls?: ToolCall[];
  tool_call_preview?: boolean; // WebSocket 工具调用预览帧，最终帧到达后按 tool_call_id 合并
  timestamp?: string;
  attachments?: FileAttachment[];
  artifacts?: MessageArtifact[];
  citations?: MessageCitation[];
  usage?: MessageUsage;       // token 用量（后端支持后填充）
  llm_duration?: number;      // LLM 请求耗时（毫秒）
  is_error?: boolean;         // 错误消息标记
  tool_name?: string;         // 工具名称（tool 消息使用）
  recoverable?: boolean;      // 可恢复工具错误，可由模型修复或重新触发审批
  terminal?: boolean;         // 终止错误，避免重复相同调用
  error_kind?: string;        // 结构化错误类型
}

export interface MessagesListResponse {
  session_id: string;
  messages: Message[];
  total: number;
}

// Session trace / observability replay
export interface TraceQualityReflection {
  trigger?: string;
  severity?: string;
  tool_name?: string;
  consecutive?: number;
  summary?: string;
  recommended?: string[];
  injected?: boolean;
}

export interface TraceQualityEvent {
  name?: string;
  case_id?: string;
  run_id?: string;
  trace_id?: string;
  span_id?: string;
  turn_id?: string;
  domain_id?: string;
  source_kind?: string;
  source_name?: string;
  owner_scope?: string;
  owner_id?: string;
  user_id?: string;
  route?: string;
  failure_type?: string;
  retry_reason?: string;
  final_status?: string;
  reflection?: TraceQualityReflection;
  tool_decision?: Record<string, unknown>;
  prompt?: Record<string, unknown>;
  context_build?: Record<string, unknown>;
  delegation?: Record<string, unknown>;
  attributes?: Record<string, unknown>;
}

export interface TraceTimelineItem {
  kind: 'span' | 'quality_event' | string;
  trace_id?: string;
  span_id?: string;
  parent_span_id?: string;
  operation: string;
  service?: string;
  status?: string;
  duration_ms?: number;
  attributes?: Record<string, unknown> & {
    quality_event?: TraceQualityEvent | string;
  };
  timestamp: string;
}

export interface AgentTraceNode {
  trace_id: string;
  agent_id?: string;
  type?: string;
  status?: string;
  children?: AgentTraceNode[];
}

export interface SessionTraceResponse {
  session_id: string;
  trace_id?: string;
  items: TraceTimelineItem[];
  agent_tree?: AgentTraceNode[];
}

// HITL 相关
export type InputRequestType = 'approval' | 'clarification' | 'confirmation' | 'choice' | 'permission';

export interface InputRequest {
  id: string;
  task_id: string;
  step_id?: string;
  session_id?: string;
  type: InputRequestType;
  prompt: string;
  options?: string[];
  default?: string;
  timeout?: number;
  tool_name?: string;
  data?: Record<string, unknown>;
  created_at: string;
}

export interface InputResponse {
  request_id: string;
  task_id: string;
  value: string;
  action: string; // "approve" | "reject" | "modify" | "proceed"
  remember?: boolean;
}

export interface UserCommand {
  type: 'pause' | 'resume' | 'cancel';
  task_id: string;
  payload?: unknown;
}

// Agent / Skill
export interface AgentInfo {
  id: string;
  name: string;
  description: string;
  skills?: string[];
  dynamic?: boolean;
}

export interface SkillMetadata {
  name: string;
  description: string;
  user_invocable?: boolean;
  argument_hint?: string;
  model?: string;
  context?: string;
}

// Admin Skills（管理后台 overlay 视图）
export interface AdminSkillItem {
  name: string;
  description: string;
  path: string;
  origin: 'fs' | 'db';
  revision: number;
}

export interface AdminSkillDetail extends AdminSkillItem {
  content: string;
}

export type QualityCandidateStatus =
  | 'new'
  | 'reviewing'
  | 'approved'
  | 'rejected'
  | 'promoted'
  | 'promoted_verified'
  | 'promoted_regressed';

export interface QualityCandidateCase {
  id: string;
  name: string;
  route: string;
  input: string;
  expected_tools?: string[];
  allowed_tools?: string[];
  expected_skills?: string[];
  expected_agents?: string[];
  scenario?: string;
  expected_status: string;
  failure_type?: string;
  risk?: string;
  required: boolean;
  notes?: string;
}

export type QualityOptimizationSuggestionKind = 'prompt_diff_suggestion' | 'tool_description_suggestion' | 'skill_draft';

export interface QualityOptimizationSuggestion {
  kind: QualityOptimizationSuggestionKind;
  title: string;
  target?: string;
  rationale: string;
  proposed: string;
  review_required: boolean;
}

export interface QualityCandidateRecord {
  id: string;
  status: QualityCandidateStatus;
  route: string;
  session_id: string;
  replay_ref: string;
  input: string;
  case: QualityCandidateCase;
  failure_type: string;
  risk: string;
  fingerprint: string;
  source_event: Record<string, unknown>;
  review_note?: string;
  created_by?: string;
  reviewed_by?: string;
  promoted_case_id?: string;
  cluster_id?: string;
  verify_result?: string;
  optimization_suggestions?: QualityOptimizationSuggestion[];
  golden_case?: QualityCandidateCase;
  created_at: string;
  updated_at: string;
  reviewed_at?: string;
  last_verified_at?: string;
}

export interface QualityCandidateUpdateRequest {
  status: QualityCandidateStatus;
  review_note?: string;
  promoted_case_id?: string;
}

export interface QualityCandidateCreateRequest {
  session_id: string;
  replay_ref?: string;
  event_index?: number;
  input: string;
  quality_event: unknown;
}

export interface QualityCandidatesResponse {
  candidates: QualityCandidateRecord[];
  total: number;
  page: number;
  size: number;
}

export interface QualityWorkbenchCluster {
  id: string;
  key: string;
  rule_id: string;
  failure_type: string;
  tool?: string;
  skill?: string;
  prompt_key?: string;
  domain_id?: string;
  source_kind?: string;
  source_name?: string;
  error_digest: string;
  sample_message: string;
  first_seen: string;
  last_seen: string;
  size: number;
  open_count: number;
  candidate_ids: string[];
  domain_counts?: Record<string, number>;
  source_kind_counts?: Record<string, number>;
  source_name_counts?: Record<string, number>;
}

export interface QualityWorkbenchClustersResponse {
  clusters?: QualityWorkbenchCluster[];
  items?: QualityWorkbenchCluster[];
  total: number;
  page?: number;
  size?: number;
}

export interface QualityWorkbenchFilter {
  status?: QualityCandidateStatus | '';
  route?: string;
  page?: number;
  size?: number;
  domain_id?: string;
  source_kind?: string;
  source_name?: string;
  failure_type?: QualityFailureType | string | '';
}

export interface QualityWorkbenchDashboardFilter {
  since?: string;
  until?: string;
  domain_id?: string;
  source_kind?: string;
  source_name?: string;
  failure_type?: QualityFailureType | string | '';
}

export interface GroupingMatch {
  failure_type?: string;
  tool?: string;
  skill?: string;
  prompt_key?: string;
  domain_id?: string;
  source_kind?: string;
  source_name?: string;
  error_substring?: string;
}

export interface GroupingRule {
  id: string;
  name: string;
  priority: number;
  enabled: boolean;
  match: GroupingMatch;
  key_version?: 'v1' | 'v2' | string;
  key_fields: string[];
  digest_normalize: string[];
  notes?: string;
  created_by?: string;
  created_at?: string;
  updated_at?: string;
}

export interface GroupingRulePreview {
  clusters: QualityWorkbenchCluster[];
  rule_hits: Record<string, number>;
}

export interface GroupingRulesResponse {
  items: GroupingRule[];
  total: number;
}

export interface ReplayFanoutPlan {
  total: number;
  limit: number;
  selected_ids: string[];
  truncated: boolean;
  remaining: number;
  remaining_batches: string[][];
}

export interface CaseRunResult {
  case_id: string;
  passed: boolean;
  reason?: string;
  runner_info?: RunnerInfo;
  evidence_level?: RunnerEvidenceLevel | string;
  judge_verdict?: QualityEvaluationVerdict;
  gate_metrics?: GateMetrics;
  trace_ref?: string;
  replay_ref?: string;
  domain_id?: string;
  source_kind?: string;
  source_name?: string;
}

export interface VersionMatrixInput {
  baseline_run_id?: string;
  treatment_run_id?: string;
  baseline: CaseRunResult[];
  treatment: CaseRunResult[];
}

export interface CaseVersionDiff {
  case_id: string;
  baseline_present: boolean;
  treatment_present: boolean;
  baseline_passed: boolean;
  treatment_passed: boolean;
  baseline_reason?: string;
  treatment_reason?: string;
  regressed: boolean;
  recovered: boolean;
  new_failure: boolean;
}

export interface VersionDiff {
  baseline_run_id?: string;
  treatment_run_id?: string;
  cases: Record<string, CaseVersionDiff>;
  regressed_case_ids: string[];
  recovered_case_ids: string[];
  new_failure_case_ids: string[];
}

export type ReplayJobStatus = 'queued' | 'running' | 'succeeded' | 'failed' | 'cancelled';

export type RunnerEvidenceLevel =
  | 'static_schema'
  | 'replay_trace'
  | 'simulated_runner'
  | 'real_runner'
  | 'production_shadow'
  | 'human_verified'
  | (string & {});

export interface RunnerInfo {
  name?: string;
  version?: string;
  evidence_level?: RunnerEvidenceLevel | string;
}

export interface QualityEvaluationVerdict {
  score?: number;
  verdict?: string;
  failure_type?: QualityFailureType | string;
  feedback?: string[];
  should_optimize?: boolean;
}

export interface GateMetrics {
  required_total?: number;
  required_passed?: number;
  dangerous_misallow_count?: number;
  failure_attribution_rate?: number;
  tool_choice_accuracy?: number;
  replay_locatable_rate?: number;
  regression_candidate_rate?: number;
  required_zero_tool_regression?: number;
  delegation_trace_coverage_rate?: number;
  semantic_score?: number;
  judge_missing?: boolean;
  judge_required_domain?: string;
}

export interface ShadowEvalResult {
  case_id?: string;
  domain_id?: string;
  source_kind?: string;
  source_name?: string;
  passed?: boolean;
  judge_verdict?: QualityEvaluationVerdict;
  runner_info?: RunnerInfo;
  trace_ref?: string;
  replay_ref?: string;
  timestamp?: string;
  eval_duration_ms?: number;
}

export interface ShadowEvalMetrics {
  domain_id?: string;
  sample_count?: number;
  pass_rate?: number;
  avg_semantic_score?: number;
  safety_failures?: number;
  tool_misuses?: number;
  recent_alerts?: RollbackAlert[];
}

export interface DomainRegressionStatus {
  domain_id?: string;
  status?: 'pass' | 'fail' | 'unknown' | string;
  semantic_score?: number;
  safety_failures?: number;
  active_cases?: number;
  evidence_level?: RunnerEvidenceLevel | string;
}

export interface ReplayJobResult {
  total?: number;
  passed?: number;
  failed?: number;
  unknown?: number;
  case_ids?: string[];
  reasons?: string[];
  runner_info?: RunnerInfo;
  evidence_level?: RunnerEvidenceLevel | string;
}

export interface ReplayJob {
  id: string;
  batch_id: string;
  kind: string;
  target_ids: string[];
  domain_id?: string;
  source_kind?: string;
  source_name?: string;
  status: ReplayJobStatus;
  max_attempt: number;
  attempt: number;
  error?: string;
  result?: ReplayJobResult;
  created_at: string;
  updated_at: string;
}

export interface ReplayJobsResponse {
  items: ReplayJob[];
  total: number;
  page?: number;
  size?: number;
}

export type OptimizationRolloutStatus = 'applied' | 'rolled_back';

export interface OptimizationRollout {
  id: string;
  suggestion_id: string;
  target: OptimizationSuggestionTarget;
  target_key: string;
  previous_value: string;
  previous_exists: boolean;
  applied_value: string;
  status: OptimizationRolloutStatus;
  applied_by: string;
  rolled_back_by?: string;
  created_at: string;
  updated_at: string;
  rolled_back_at?: string;
  candidate?: QualityCandidateRecord | null;
}

export interface OptimizationRolloutMutationResponse {
  rollout: OptimizationRollout;
  candidate?: QualityCandidateRecord | null;
}

export interface BatchEvalRun {
  id: string;
  batch_id: string;
  kind: string;
  suite_type?: string;
  domain_id?: string;
  source_kind?: string;
  source_name?: string;
  runner_info?: RunnerInfo;
  evidence_level?: RunnerEvidenceLevel | string;
  status: string;
  summary?: {
    total: number;
    passed: number;
    failed: number;
    unknown: number;
    reasons?: string[];
    evidence_level?: RunnerEvidenceLevel | string;
    semantic_score?: number;
    judge_missing?: boolean;
    gate_metrics?: GateMetrics;
    judge_verdict?: QualityEvaluationVerdict;
    shadow_metrics?: ShadowEvalMetrics | ShadowEvalMetrics[];
    domain_regression?: DomainRegressionStatus | DomainRegressionStatus[];
    domain_regressions?: DomainRegressionStatus[];
  };
  diff?: {
    changed_candidate_ids?: string[];
    new_failures?: string[];
    recovered?: string[];
    domain_regressions?: DomainRegressionStatus[];
    shadow_metrics?: ShadowEvalMetrics | ShadowEvalMetrics[];
    [key: string]: unknown;
  };
  gate_metrics?: GateMetrics;
  judge_verdict?: QualityEvaluationVerdict;
  shadow_metrics?: ShadowEvalMetrics | ShadowEvalMetrics[];
  shadow_results?: ShadowEvalResult[];
  domain_regression?: DomainRegressionStatus | DomainRegressionStatus[];
  domain_regressions?: DomainRegressionStatus[];
  case_results?: CaseRunResult[];
  created_at?: string;
  updated_at?: string;
}

export interface BatchEvalRunsResponse {
  items: BatchEvalRun[];
  total: number;
}

export type QualityFailureType =
  | 'none'
  | 'prompt'
  | 'tool'
  | 'skill'
  | 'context'
  | 'model'
  | 'permission'
  | 'runtime'
  | 'user_input';

export interface PromptRef {
  key?: string;
  version?: string;
  source?: string;
  language?: string;
}

export type EvalDiffStatus = 'pending' | 'eval_diff_running' | 'eval_diff_done' | 'approved' | 'rejected';

export interface EvalResult {
  case_id: string;
  passed: boolean;
  cost_usd: number;
  latency_ms: number;
  failure_type?: QualityFailureType;
  prompt?: PromptRef;
  expected_tools?: string[];
  actual_tool?: string;
  expected_skills?: string[];
  reason?: string;
}

export interface EvalRun {
  id: string;
  runner_info?: RunnerInfo;
  results: EvalResult[];
  created_at?: string;
}

export interface EvalRunSummary {
  case_count: number;
  success_count: number;
  success_rate: number;
  average_cost_usd: number;
  average_latency_ms: number;
}

export interface EvalCaseDiff {
  case_id: string;
  baseline_passed: boolean;
  treatment_passed: boolean;
  cost_delta_usd: number;
  latency_delta_ms: number;
  failure_type?: QualityFailureType;
  prompt?: PromptRef;
  expected_tools?: string[];
  actual_tool?: string;
  expected_skills?: string[];
  reason?: string;
}

export interface EvalDiff {
  id: string;
  status: EvalDiffStatus;
  baseline_run_id: string;
  treatment_run_id: string;
  baseline_runner_info?: RunnerInfo;
  treatment_runner_info?: RunnerInfo;
  baseline: EvalRunSummary;
  treatment: EvalRunSummary;
  success_rate_delta: number;
  average_cost_delta_usd: number;
  average_latency_delta_ms: number;
  success_p_value: number;
  case_diffs: EvalCaseDiff[];
  created_at?: string;
  updated_at?: string;
  approved_by?: string;
  rejected_by?: string;
}

export interface EvalDiffsResponse {
  items: EvalDiff[];
  total: number;
  page?: number;
  size?: number;
}

export interface ABReportResponse {
  eval_diff_id: string;
  markdown: string;
}

export interface QualityReport {
  id: string;
  week_start: string;
  title: string;
  summary?: Record<string, unknown>;
  markdown: string;
  created_at: string;
}

export interface QualityReportsResponse {
  items: QualityReport[];
  total: number;
}

export interface QualityDashboardSnapshot {
  since?: string;
  until?: string;
  open_clusters: number;
  open_candidates?: number;
  candidate_status_counts?: Record<string, number>;
  failure_type_counts?: Record<string, number>;
  verify_result_counts?: Record<string, number>;
  domain_counts?: Record<string, number>;
  source_kind_counts?: Record<string, number>;
  source_name_counts?: Record<string, number>;
  by_status?: Record<string, number>;
  by_failure_type?: Record<string, number>;
  by_verify_result?: Record<string, number>;
}

export interface QualityDashboardSeriesPoint {
  since?: string;
  until?: string;
  candidate_status_counts?: Record<string, number>;
  failure_type_counts?: Record<string, number>;
  verify_result_counts?: Record<string, number>;
  domain_counts?: Record<string, number>;
  source_kind_counts?: Record<string, number>;
  source_name_counts?: Record<string, number>;
}

export interface MemoryGovernanceStats {
  total: number;
  missing_governance: number;
  expired: number;
  low_confidence: number;
  cross_user_risk: number;
  policy?: MemoryGovernancePolicy;
  min_confidence?: number;
  max_memories?: number;
}

export interface MemoryGovernancePolicy {
  min_confidence?: number;
  max_memories?: number;
}

export interface MemoryPruneResponse {
  dry_run: boolean;
  matched?: number;
  deleted?: number;
  delete_ids?: number[] | null;
  reasons?: Record<string, string> | null;
}

export type MemoryType = 'user' | 'project' | 'feedback' | 'reference' | 'procedural' | 'episodic';

export interface MemoryRecord {
  id: number;
  user_id?: string;
  type: MemoryType;
  content: string;
  tags?: string[];
  session_id?: string;
  metadata?: Record<string, unknown>;
  score?: number;
  created_at: string;
  updated_at: string;
  accessed_at: string;
  access_count: number;
}

export interface MemoryExportDocument {
  version: number;
  user_id?: string;
  exported_at?: string;
  memories?: MemoryRecord[] | null;
}

export interface MemoryImportResponse {
  imported: number;
  ids: number[];
}

export interface MemoryAdminFilter {
  userId?: string;
  user_id?: string;
  target?: string;
  target_scope?: string;
  scope?: string;
  kind?: MemoryType | string;
  memory_kind?: MemoryType | string;
  limit?: number;
}

export interface MemoryInjectionExplainItem {
  timestamp?: string;
  session_id_hash?: string;
  route?: string;
  prompt_versions?: string[] | null;
  memory_ids?: number[] | null;
  skipped_memory_ids?: number[] | null;
  skip_counts?: Record<string, number> | null;
  estimated_tokens?: number;
  memory_injected: boolean;
  feedback_memory_count?: number;
  regular_memory_count?: number;
  memory_domain_id?: string;
  memory_source_kind?: string;
  memory_source_name?: string;
  memory_owner_scope?: string;
  memory_owner_id?: string;
  contamination_check?: string;
  additional_attributes?: Record<string, string>;
}

export interface MemoryInjectionExplainResponse {
  items?: MemoryInjectionExplainItem[] | null;
  total: number;
  limit: number;
  source?: string;
}

export type EmbeddingState = 'pending' | 'ready' | 'failed';

export interface VectorSpaceMetadata {
  name?: string;
  embedding_state?: EmbeddingState;
  migrated_at?: string;
}

export interface VectorSpaceMigrationUpdate {
  memory_id: number;
  record: MemoryRecord;
}

export interface VectorSpaceMigrationPlan {
  dry_run: boolean;
  scanned: number;
  updates?: VectorSpaceMigrationUpdate[] | null;
  resume_token?: string;
  next_offset?: number;
}

export interface VectorSpaceMigrationResponse {
  plan?: VectorSpaceMigrationPlan | null;
  applied: boolean;
  updated: number;
}

export type EmbeddingBacklogStatus = 'pending' | 'claimed' | 'done' | 'failed';

export interface EmbeddingBacklogStats {
  total: number;
  by_state?: Record<EmbeddingBacklogStatus | string, number> | null;
}

export interface MemoryPromotionCandidate {
  subject_id: string;
  target_type: 'procedural' | string;
  proposed_procedural_memory?: MemoryRecord | null;
  rationale?: string | null;
  source_memory_ids?: number[] | null;
  source_kind?: string | null;
  confidence: number;
  created_at: string;
}

export interface MemoryPromotionCandidatesResponse {
  items?: MemoryPromotionCandidate[] | null;
  total: number;
  limit: number;
}

export interface MemoryPromotionCandidateFilter extends MemoryAdminFilter {
  limit?: number;
  minConfidence?: number;
  min_confidence?: number;
}

export interface MemoryPromotionApplyRequest {
  subject_id: string;
  user_id?: string;
  target?: string;
  target_scope?: string;
  scope?: string;
  kind?: string;
  memory_kind?: string;
  limit?: number;
  min_confidence?: number;
  approval_id?: string;
}

export interface MemoryPromotionApplyResponse {
  applied: true;
  memory_id: number;
  subject_id: string;
  source_memory_ids: number[];
  already_applied?: boolean;
  approval_id?: string;
}

export interface MemoryProductionMetricsSnapshot {
  embedding_dropped_total: number;
  hybrid_search_fallback_total: number;
  vector_space_mismatch_total: number;
  embedding_latency_count: number;
  embedding_latency_avg_seconds: number;
  embedding_latency_p95_seconds: number;
  backlog_depth_total: number;
  backlog_depth_by_status?: Record<string, number> | null;
  drop_reasons?: Record<string, number> | null;
  fallback_reasons?: Record<string, number> | null;
  mismatch_operations?: Record<string, number> | null;
}

export interface MemoryProductionMetricsSeriesPoint {
  since: string;
  until: string;
  embedding_dropped_total: number;
  hybrid_search_fallback_total: number;
  vector_space_mismatch_total: number;
  embedding_latency_avg_seconds: number;
  backlog_depth_total: number;
}

export interface MemoryProductionMetricAlert {
  level: 'info' | 'warning' | 'critical' | string;
  code: string;
  message: string;
  value: number;
}

export interface MemoryProductionMetrics {
  source?: string | null;
  since?: string | null;
  until?: string | null;
  window_minutes?: number | null;
  snapshot?: MemoryProductionMetricsSnapshot | null;
  series?: MemoryProductionMetricsSeriesPoint[] | null;
  alerts?: MemoryProductionMetricAlert[] | null;
}

export type OptimizationSuggestionStatus = 'pending' | 'approved' | 'rejected' | 'expired';
export type OptimizationSuggestionTarget = 'prompt' | 'tool_description' | 'skill_content' | 'memory_governance';
export type OptimizationSuggestionApplyStatus = 'unapplied' | 'applied' | 'apply_error' | 'not_applicable';

export interface OptimizationReviewSuggestion {
  id: string;
  status: OptimizationSuggestionStatus;
  target: OptimizationSuggestionTarget;
  kind: QualityOptimizationSuggestionKind | string;
  title: string;
  rationale: string;
  current_value?: string;
  proposed_value: string;
  diff_format: string;
  source_candidate_id: string;
  source_eval_diff_id?: string;
  source_event?: Record<string, unknown>;
  runner_info?: RunnerInfo;
  review_required: boolean;
  created_by: string;
  approved_by?: string;
  approval_note?: string;
  apply_status: OptimizationSuggestionApplyStatus;
  applied_by?: string;
  apply_error?: string;
  created_at: string;
  updated_at: string;
  approved_at?: string;
  applied_at?: string;
  expires_at: string;
  candidate?: QualityCandidateRecord | null;
}

export interface OptimizationSuggestionMutationResponse {
  suggestion: OptimizationReviewSuggestion;
  candidate?: QualityCandidateRecord | null;
}

export interface OptimizationSuggestionsResponse {
  suggestions?: OptimizationReviewSuggestion[];
  items?: OptimizationReviewSuggestion[];
  total: number;
  page?: number;
  size?: number;
}

export type OptimizationApprovalRole = 'admin' | 'engineer' | 'lead';
export type OptimizationApprovalAction = 'approve' | 'reject';
export type OptimizationApprovalSubjectType = 'eval_diff' | 'suggestion' | 'memory_promotion';

export interface OptimizationApprovalRecord {
  id: string;
  subject_id: string;
  subject_type: OptimizationApprovalSubjectType;
  action: OptimizationApprovalAction;
  reviewer: string;
  reviewer_role: OptimizationApprovalRole;
  note?: string;
  created_at: string;
  candidate?: QualityCandidateRecord | null;
}

export interface OptimizationApprovalMutationResponse {
  approval: OptimizationApprovalRecord;
  candidate?: QualityCandidateRecord | null;
}

export interface OptimizationApprovalsResponse {
  items: OptimizationApprovalRecord[];
  total: number;
}

export type RollbackAlertStatus = 'open' | 'acknowledged';

export interface RollbackAlert {
  id: string;
  status: RollbackAlertStatus;
  eval_diff_id: string;
  treatment_run_id: string;
  reasons: string[];
  success_rate_delta: number;
  average_latency_delta_ms: number;
  created_at: string;
}

export interface RollbackAlertThresholds {
  min_success_rate_delta: number;
  max_latency_delta_ms: number;
}

export interface RollbackAlertResponse {
  alert: RollbackAlert;
  triggered: boolean;
}

export interface RollbackAlertsResponse {
  items: RollbackAlert[];
  total: number;
}

export type RollbackTrigger = 'manual' | 'alert_ack';

export interface RollbackRecord {
  id: string;
  suggestion_id: string;
  alert_id?: string;
  trigger: RollbackTrigger;
  triggered_by: string;
  created_at: string;
  rollout: OptimizationRollout;
}

export interface RollbacksResponse {
  items: RollbackRecord[];
  total: number;
}

// 健康检查
export interface Health {
  status: string;
  version?: string;
  uptime?: number;
  active_sessions?: number;
}

// WebSocket 消息
export interface WSMessage {
  type: string;
  payload: unknown;
}

// API 错误
export interface ApiError {
  error: string;
  code: number;
}

// 通用列表响应
export interface SessionListResponse {
  sessions: Session[];
}

export type ScheduledTaskTargetType = 'im_push' | 'session';
export type ScheduledTaskRunStatus = 'running' | 'succeeded' | 'failed' | 'timeout' | 'skipped';

export interface ScheduledTask {
  id: string;
  name: string;
  description?: string;
  target_type: ScheduledTaskTargetType;
  target_config: Record<string, unknown>;
  platform?: string;
  prompt: string;
  cron_expr?: string;
  interval_sec?: number;
  timezone: string;
  enabled: boolean;
  created_by: string;
  last_run_at?: string;
  next_run_at?: string;
  last_error?: string;
  active_run_id?: string;
  lease_expires_at?: string;
  created_at: string;
  updated_at: string;
}

export interface ScheduledTaskRun {
  scheduled_at: string;
  id: string;
  task_id: string;
  started_at: string;
  finished_at?: string;
  status: ScheduledTaskRunStatus;
  attempt_count: number;
  output: string;
  error: string;
  session_id?: string;
  claimed_by?: string;
  claim_expires_at?: string;
}

export interface ScheduledTaskUpsertRequest {
  name: string;
  description?: string;
  target_type: ScheduledTaskTargetType;
  target_config?: Record<string, unknown>;
  platform?: string;
  prompt: string;
  interval_sec?: number;
  cron_expr?: string;
  timezone?: string;
  enabled: boolean;
}

// Model
export interface ModelInfo {
  name: string;
  model: string;
  provider?: string;
  service_type?: 'llm' | 'image_gen' | 'video_gen' | 'tts' | 'stt' | 'embedding';
  is_active: boolean;
}

// 远程 ACP Agent
export interface RemoteAgentConfig {
  name: string;
  description: string;
  transport: 'stdio' | 'http';
  command?: string;
  args?: string[];
  url?: string;
  headers?: Record<string, string>;
  skills?: string[];
  enabled: boolean;
}

export interface RemoteAgentHealth {
  agent_id: string;
  status: number | string; // 后端 AgentStatus: 0=stopped, 1=running, 2=error
  uptime: number;          // time.Duration 纳秒
}

export interface ExecRule {
  pattern: string;
  policy: 'allow' | 'ask' | 'deny';
  description?: string;
}

// 运行时配置（config.get 返回的脱敏配置）
export interface RuntimeConfig {
  hitl: {
    enabled: boolean;
    permission_rules: PermissionRule[];
  };
  agent: {
    timeout: number;
    shell_timeout: number;
  };
  mcp: {
    timeout: number;
    servers: Record<string, MCPServerConfig>;
  };
  channel: {
    enabled: boolean;
    dingtalk: DingTalkConfig;
    feishu: FeishuConfig;
    wecom: WeComConfig;
    wechatbot?: WeChatBotConfig;
  };
  security?: {
    default_policy?: 'allow' | 'ask' | 'deny';
    permission_mode?: 'minimal' | 'strict';
    exec_rules: ExecRule[];
  };
}

export interface PermissionRule {
  tool_name: string;
  action: 'allow' | 'ask' | 'deny';
  pattern?: string;
}

export interface MCPServerConfig {
  command?: string;
  args?: string[];
  env?: Record<string, string>;
  transport?: string; // "stdio" | "sse" | "http"
  url?: string;
  headers?: Record<string, string>;
  timeout?: string;
}

export interface MCPToolSummary {
  name: string;
  description?: string;
  server?: string;
  core?: boolean;
  is_concurrency_safe?: boolean;
  trusted?: boolean;
  risk?: string;
  read_only?: boolean;
  requires_approval?: boolean;
  may_require_approval?: boolean;
  route_status?: string;
  callable_now?: boolean;
  block_reason?: string;
}

export interface MCPToolsByServer {
  name: string;
  count: number;
  tools: MCPToolSummary[];
  resources?: number;
  prompts?: number;
}

export interface MCPToolsListResponse {
  total: number;
  mcp_count: number;
  local_count: number;
  servers: MCPToolsByServer[];
  tools: MCPToolSummary[];
}

// IM 通道配置
export interface DingTalkConfig {
  enabled: boolean;
  app_key: string;
  app_secret: string;
  token: string;
  aes_key: string;
  agent_id: number;
}

export interface FeishuRendererConfig {
  /** 回滚开关（反向语义）：true = 禁用流式卡片，走 legacy Plugin.Send。默认 false = 启用 EventRenderer。 */
  disabled?: boolean;
  /** 卡片 PATCH 最小间隔（毫秒）。默认 300；<= 0 后端 Normalize 回退到 300。 */
  throttle_ms?: number;
  /** 卡片中展示 "Agent 思考中" 等中间状态文案。默认 false。 */
  show_agent_progress?: boolean;
}

/** 飞书事件入口模式。CEO 决议:webhook XOR longconn,严禁同进程并存。 */
export type FeishuIngressMode = '' | 'webhook' | 'longconn';

export interface FeishuReliabilityConfig {
  /** longconn 入口主开关。Phase 2B+ 推荐用这个,旧的 longconn_enabled 顶层字段会回退读取。 */
  longconn_enabled?: boolean;
  /** longconn 重连后是否补偿断线期间消息(gap fetch),默认 false。 */
  longconn_gap_fetch_enabled?: boolean;
}

export interface FeishuConfig {
  enabled: boolean;
  app_id: string;
  app_secret: string;
  verification_token: string;
  encrypt_key: string;
  /** 收到消息的 ack 表情:"Get"(默认)/ "Typing" / "none"(禁用);飞书 reactions API emoji_type(CamelCase)。
   *  老值 "GET"/"KEYBOARD" 后端 Normalize 会静默迁移到 "Get"/"Typing";其他非法值 warn 回退到 "Get"。 */
  ack_emoji?: string;
  /** EventRenderer(流式卡片)行为参数,未配置等同于"全部默认"。 */
  renderer?: FeishuRendererConfig;
  /** 事件入口模式。空 = 默认走 longconn_enabled 推断,否则默认 webhook。 */
  ingress_mode?: FeishuIngressMode;
  /** webhook URL 声明。dual-ingress fatal guard:webhook_url 非空 + longconn=true → 启动 panic。 */
  webhook_url?: string;
  /** 飞书地区:""/"cn" 默认连 open.feishu.cn;"intl"/"lark"/"international" 切 open.larksuite.com。 */
  region?: string;
  /** 可靠性 / 长连接配置。 */
  reliability?: FeishuReliabilityConfig;
}

export interface WeComConfig {
  enabled: boolean;
  corp_id: string;
  agent_id: number;
  secret: string;
  token: string;
  encoding_aes_key: string;
}

export interface WeChatBotConfig {
  enabled: boolean;
  base_url?: string;
  cred_root?: string;
  log_level?: string;
}

export interface MCPServerUpdateRequest {
  command?: string;
  args?: string[];
  env?: Record<string, string>;
  transport?: string;
  url?: string;
  headers?: Record<string, string>;
  timeout?: string;
}

export interface DingTalkConfigPatch {
  enabled?: boolean;
  app_key?: string;
  app_secret?: string;
  token?: string;
  aes_key?: string;
  agent_id?: number;
}

export interface FeishuConfigPatch {
  enabled?: boolean;
  app_id?: string;
  app_secret?: string;
  verification_token?: string;
  encrypt_key?: string;
  ack_emoji?: string;
  renderer?: FeishuRendererConfig;
  ingress_mode?: FeishuIngressMode;
  webhook_url?: string;
  region?: string;
  reliability?: FeishuReliabilityConfig;
}

export interface WeComConfigPatch {
  enabled?: boolean;
  corp_id?: string;
  agent_id?: number;
  secret?: string;
  token?: string;
  encoding_aes_key?: string;
}

export interface WeChatBotConfigPatch {
  enabled?: boolean;
  base_url?: string;
  cred_root?: string;
  log_level?: string;
}

export interface ConfigUpdateRequest {
  hitl?: {
    enabled?: boolean;
    permission_rules?: PermissionRule[];
  };
  agent?: {
    timeout?: string;
    shell_timeout?: string;
  };
  mcp?: {
    timeout?: string;
    servers?: Record<string, MCPServerUpdateRequest | null>;
  };
  channel?: {
    enabled?: boolean;
    dingtalk?: DingTalkConfigPatch;
    feishu?: FeishuConfigPatch;
    wecom?: WeComConfigPatch;
    wechatbot?: WeChatBotConfigPatch;
  };
  security?: {
    default_policy?: 'allow' | 'ask' | 'deny';
    permission_mode?: 'minimal' | 'strict';
    exec_rules?: ExecRule[];
  };
}

// 外部资源
export interface ExternalResource {
  name: string;
  type: string;
  environment: string;
  description: string;
  connection: string;
  endpoint: string;
  credentials: string;
  read_only: boolean;
  enabled: boolean;
  updated_at: string;
}

export interface ExternalResourceSaveRequest {
  name: string;
  type?: string;
  environment?: string;
  description?: string;
  connection?: string;
  endpoint?: string;
  credentials?: string;
  read_only?: boolean;
  enabled?: boolean;
}

// Gateway RPC 响应格式
export interface RPCResponse<T = unknown> {
  id: string;
  result?: T;
  error?: { code: number; message: string };
}

// ── Admin 用户管理 ──────────────────────────────────────────────────────────

export interface AdminUser {
  id: string;
  display_name: string;
  email: string;
  role: 'user' | 'admin';
  status: 'active' | 'disabled';
  auth_provider: string;
  token_quota: number;
  token_used: number;
}

export interface AdminUsersResponse {
  users: AdminUser[];
  total: number;
  page: number;
  size: number;
}

export interface UsageSummary {
  total_cost_usd: number;
  total_tokens: number;
  by_model: Record<string, { tokens: number; cost_usd: number }>;
}

export interface UsageModelCost {
  cost_usd: number;
  prompt_tokens?: number;
  completion_tokens?: number;
  tokens: number;
  request_count?: number;
}

export interface UsageQualityCost {
  by_task_type: Record<string, UsageModelCost>;
  by_quality_case: Record<string, UsageModelCost>;
  by_prompt_version: Record<string, UsageModelCost>;
  by_failure_type?: Record<string, UsageModelCost>;
  by_final_status?: Record<string, UsageModelCost>;
  top_quality_cases: Array<{
    quality_case_id: string;
    tokens: number;
    cost_usd: number;
    request_count: number;
  }>;
}

export interface AdminProvider {
  name: string;
  provider_type: string;
  enabled: boolean;
  config_json: Record<string, unknown>;
}

export interface AdminProviderCreateRequest {
  name: string;
  provider_type: string;
  enabled?: boolean;
  config_json?: Record<string, unknown>;
}

export interface AdminProviderUpdateRequest {
  provider_type?: string;
  enabled?: boolean;
  config_json?: Record<string, unknown>;
}

export interface AdminProvidersResponse {
  providers: AdminProvider[];
}

export interface AdminInviteCode {
  id: string;
  code_hint?: string;
  role: 'user' | 'admin';
  max_uses: number;
  use_count: number;
  expires_at: string;
  disabled: boolean;
  note?: string;
  created_by?: string;
  created_at: string;
  updated_at: string;
}

export interface AdminInviteCodesResponse {
  invite_codes: AdminInviteCode[];
}

export interface AdminInviteCodeCreateRequest {
  role?: 'user' | 'admin';
  max_uses?: number;
  expires_at: string;
  note?: string;
}

export interface AdminInviteCodeCreateResponse {
  invite: AdminInviteCode;
  code: string;
}

export interface AdminInviteCodeUpdateRequest {
  disabled?: boolean;
  note?: string;
  expires_at?: string;
}

export interface PromptRecord {
  key: string;
  language: string;
  content: string;
  updated_at: string;
  updated_by: string;
}

export interface PromptSmokeEvalRequest {
  key: string;
  language: string;
  content: string;
}

export interface PromptSmokeEvalResponse {
  ok: boolean;
  checked_cases: number;
  warnings: string[];
}

// LLM Provider 管理
export interface LLMProviderRecord {
  name: string;
  provider_type: string;
  base_url: string;
  api_key: string; // 脱敏后
  is_default: boolean;
  enabled: boolean;
  api_format: string;
  service_type: string;
  config_json: string;
  created_at: string;
  updated_at: string;
}

export interface LLMProviderCreateRequest {
  name: string;
  provider_type: string;
  base_url?: string;
  api_key?: string;
  is_default?: boolean;
  enabled?: boolean;
  api_format?: string;
  service_type?: string;
  config_json?: string;
}

export interface LLMProviderUpdateRequest {
  provider_type?: string;
  base_url?: string;
  api_key?: string;
  is_default?: boolean;
  enabled?: boolean;
  api_format?: string;
  service_type?: string;
  config_json?: string;
}

// LLM Model 管理
export interface LLMModelRecord {
  name: string;
  provider_name: string;
  model: string;
  base_url: string;
  api_key: string;
  is_default: boolean;
  enabled: boolean;
  config_json: string;
  created_at: string;
  updated_at: string;
}

export interface LLMModelCreateRequest {
  name: string;
  provider_name?: string;
  model: string;
  base_url?: string;
  api_key?: string;
  is_default?: boolean;
  enabled?: boolean;
  config_json?: string;
}

export interface LLMModelUpdateRequest {
  name?: string;
  provider_name?: string;
  model?: string;
  base_url?: string;
  api_key?: string;
  is_default?: boolean;
  enabled?: boolean;
  config_json?: string;
}

export type KBOwnerScope = 'user' | 'tenant' | 'system';
export type KBDocumentStatus = 'draft' | 'active' | 'archived' | 'revoked';
export type KBBindingType = 'agent' | 'domain' | 'session_template' | 'session' | 'tenant' | 'user' | 'system';

export interface KBNamespace {
  id: string;
  name: string;
  domain_id: string;
  owner_scope: KBOwnerScope;
  owner_id: string;
  index_strategy: string;
  thinning_enabled: boolean;
  thinning_token_threshold: number;
  summary_token_threshold: number;
  summary_model?: string;
  created_at: string;
  updated_at: string;
}

export interface KBCreateNamespaceRequest {
  name: string;
  domain_id?: string;
  index_strategy?: string;
  thinning_enabled?: boolean;
  thinning_token_threshold?: number;
  summary_token_threshold?: number;
  summary_model?: string;
}

export interface KBDocument {
  id: string;
  namespace_id: string;
  title: string;
  version: string;
  status: KBDocumentStatus;
  description?: string;
  source_uri?: string;
  effective_at: string;
  expires_at?: string;
}

export interface KBAssetRef {
  asset_uri?: string;
  uri?: string;
  node_id?: string;
  line?: number;
  page?: number;
  mime_type: string;
  filename?: string;
  alt_text?: string;
  caption?: string;
  content_hash: string;
  source: 'local_file' | 'data_uri' | 'remote_url' | 'pdf_extract' | 'docx_extract' | string;
  signed_url?: string;
}

export interface KBMarkdownPreviewAsset {
  path: string;
  filename?: string;
  mime_type?: string;
  alt_text?: string;
  caption?: string;
  size: number;
  data_url?: string;
}

export interface KBMarkdownPreviewResponse {
  title?: string;
  markdown: string;
  assets?: KBMarkdownPreviewAsset[];
  quality?: string;
  provider?: string;
  warnings?: string[];
}

export interface KBSectionTextNode {
  node_id: string;
  node_path?: string;
  title?: string;
  text?: string;
  evidence_token?: string;
  asset_refs?: KBAssetRef[];
}

export interface KBSectionTextSection {
  node_id: string;
  node_path?: string;
  title?: string;
  text?: string;
  evidence_token?: string;
  start_line?: number;
  end_line?: number;
  start_page?: number;
  end_page?: number;
}

export interface KBSectionTextResult {
  sections?: KBSectionTextSection[];
  nodes?: KBSectionTextNode[];
  asset_refs?: KBAssetRef[];
}

export interface KBStructureNode {
  id: string;
  parent_node_id?: string;
  node_path: string;
  title: string;
  level: number;
  token_count: number;
  summary: string;
  prefix_summary: string;
  start_line: number;
  end_line: number;
  start_page?: number;
  end_page?: number;
  children?: KBStructureNode[];
}

export interface KBDocumentTreeResponse {
  doc_id: string;
  namespace_id: string;
  nodes: KBStructureNode[];
}

export interface KBDocumentNodeResponse {
  doc_id: string;
  namespace_id: string;
  node: unknown;
}

export interface KBBinding {
  id: string;
  namespace_id: string;
  domain_id: string;
  binding_type: KBBindingType;
  binding_target: string;
  enabled: boolean;
  effective_at: string;
  expires_at?: string;
}

export interface KBEffectiveBindingsFilter {
  agentId?: string;
  domainId?: string;
  sessionTemplateId?: string;
  sessionId?: string;
  tenantId?: string;
}

export interface KBEffectiveBindingsResponse {
  bindings: KBBinding[];
}

export interface KBIngestStageEvent {
  name: string;
  status: string;
  duration_ms: number;
  attributes?: Record<string, unknown>;
}

export interface KBAssetBindingReport {
  asset_uri: string;
  node_id?: string;
  line?: number;
  page?: number;
  node_path?: string;
  node_title?: string;
  alt_text?: string;
  caption?: string;
  content_hash?: string;
  mime_type?: string;
  bound: boolean;
}

export interface KBIngestReport {
  ingest_id: string;
  namespace_id: string;
  document_id?: string;
  title?: string;
  version?: string;
  source_filename?: string;
  content_bytes: number;
  markdown_lines: number;
  converted: boolean;
  provider?: string;
  quality?: string;
  uploaded_assets: number;
  converted_assets: number;
  image_refs: number;
  tree_nodes: number;
  bound_assets: number;
  unbound_assets: number;
  duration_ms: number;
  warnings?: string[];
  stages: KBIngestStageEvent[];
  asset_bindings?: KBAssetBindingReport[];
}

export interface KBIngestResponse {
  document: KBDocument;
  warnings?: string[];
  report?: KBIngestReport;
}

export interface KBCreateBindingRequest {
  namespace_id: string;
  domain_id?: string;
  binding_type: KBBindingType;
  binding_target: string;
  effective_at?: string;
  expires_at?: string;
}

export interface KBUpdateBindingRequest {
  enabled?: boolean;
  binding_target?: string;
  effective_at?: string;
  expires_at?: string;
}
