import type {
  Session,
  SessionDetail,
  UpdateSessionRequest,
  SendMessageResponse,
  InputResponse,
  AgentInfo,
  SkillMetadata,
  Health,
  SessionListResponse,
  Message,
  MessagesListResponse,
  SessionTraceResponse,
  WeChatConfigResponse,
  UpdateWeChatProtocolRequest,
  FileAttachment,
  RemoteAgentConfig,
  RemoteAgentHealth,
  ExternalResource,
  RPCResponse,
  ModelInfo,
  RuntimeConfig,
  ConfigUpdateRequest,
  AdminUsersResponse,
  UsageSummary,
  AdminProvidersResponse,
  AdminProvider,
  PromptRecord,
  LLMProviderRecord,
  LLMModelRecord,
  AdminSkillItem,
  AdminSkillDetail,
  QualityCandidateStatus,
  QualityCandidateCreateRequest,
  QualityCandidateUpdateRequest,
  QualityCandidatesResponse,
  QualityCandidateRecord,
  QualityWorkbenchClustersResponse,
  GroupingRule,
  GroupingRulePreview,
  GroupingRulesResponse,
  ReplayFanoutPlan,
  VersionMatrixInput,
  VersionDiff,
  ReplayJobsResponse,
  ReplayJob,
  BatchEvalRun,
  BatchEvalRunsResponse,
  EvalRun,
  EvalDiff,
  EvalDiffsResponse,
  ABReportResponse,
  QualityReport,
  QualityReportsResponse,
  QualityDashboardSnapshot,
  QualityDashboardSeriesPoint,
  MemoryGovernanceStats,
  MemoryPruneResponse,
  MemoryExportDocument,
  MemoryImportResponse,
  VectorSpaceMigrationResponse,
  EmbeddingBacklogStats,
  OptimizationReviewSuggestion,
  OptimizationSuggestionsResponse,
  OptimizationSuggestionStatus,
  OptimizationRollout,
  OptimizationApprovalAction,
  OptimizationApprovalRecord,
  OptimizationApprovalRole,
  OptimizationApprovalsResponse,
  OptimizationApprovalSubjectType,
  RollbackAlertResponse,
  RollbackAlertThresholds,
  RollbackAlertsResponse,
  RollbacksResponse,
  PromptSmokeEvalRequest,
  PromptSmokeEvalResponse,
  UsageQualityCost,
} from '../types/api';
import type { JournalResponse, JournalStatsResponse } from '../types/journal';
import type { TodoResumeResponse, TodoSnapshot } from '../store/todos';
import { apiClient, ApiClient } from './client';

// NodeClient 接口 - Phase 2 扩展为 RemoteNodeClient
export interface NodeClient {
  nodeId: string;
  // 会话管理
  listSessions(): Promise<Session[]>;
  getSession(id: string): Promise<SessionDetail>;
  createSession(name: string, tags?: string[]): Promise<{ session_id: string; name: string }>;
  updateSession(id: string, req: UpdateSessionRequest): Promise<void>;
  deleteSession(id: string): Promise<void>;
  clearSession(id: string): Promise<void>;
  revertSession(sessionId: string, revertTo: number): Promise<void>;
  regenerateMessage(sessionId: string): Promise<void>;
  stopTask(sessionId: string): Promise<{ stopped: boolean }>;
  // 消息
  sendMessage(sessionId: string, content: string, options?: { attachments?: FileAttachment[]; deepThinking?: boolean }): Promise<SendMessageResponse>;
  getMessages(sessionId: string, limit?: number): Promise<Message[]>;
  getSessionTrace(sessionId: string, limit?: number): Promise<SessionTraceResponse>;
  getTodoSnapshot(sessionId: string): Promise<TodoSnapshot>;
  resumeTodos(sessionId: string, expectedPlanVersion: number, expectedRuntimeEpoch: string): Promise<TodoResumeResponse>;
  // HITL
  submitInput(taskId: string, resp: InputResponse): Promise<void>;
  getPendingInput(taskId: string): Promise<unknown[]>;
  // 元数据
  listAgents(): Promise<AgentInfo[]>;
  listSkills(): Promise<SkillMetadata[]>;
  health(): Promise<Health>;
  // 微信配置
  getWeChatConfig(): Promise<WeChatConfigResponse>;
  updateWeChatProtocol(protocol: string, req: UpdateWeChatProtocolRequest): Promise<{ success: boolean; message: string }>;
  saveConfig(): Promise<{ success: boolean; message: string; path: string }>;
  reloadWeChatProtocol(protocol: string): Promise<{ success: boolean; message: string; status: string }>;
  // Model
  listModels(): Promise<{ models: ModelInfo[]; active: string }>;
  switchModel(name: string): Promise<void>;
  // 远程 ACP Agent
  listRemoteAgents(): Promise<RemoteAgentConfig[]>;
  connectRemoteAgent(cfg: RemoteAgentConfig): Promise<{ name: string; status: string }>;
  disconnectRemoteAgent(name: string): Promise<{ name: string; status: string }>;
  healthCheckRemoteAgents(): Promise<Record<string, RemoteAgentHealth>>;
  // 运行时配置
  getRuntimeConfig(): Promise<RuntimeConfig>;
  updateRuntimeConfig(req: ConfigUpdateRequest): Promise<{ status: string }>;
  // 热重载
  reloadChannels(platform?: string): Promise<{ status: string; channels: string[] }>;
  reloadMCP(name?: string): Promise<{ status: string; servers: string[] }>;
  // 外部资源管理
  listExternalResources(): Promise<ExternalResource[]>;
  saveExternalResource(resource: Partial<ExternalResource> & { name: string }): Promise<{ status: string; name: string }>;
  deleteExternalResource(name: string): Promise<{ status: string; name: string }>;
  // WebSocket URL
  getWebSocketUrl(): string;
  // 工具直接调用（白名单，用于预览等无副作用操作）
  invokeTool(toolName: string, args: Record<string, unknown>): Promise<string>;
  // 收藏 & 标签
  starSession(id: string, starred: boolean): Promise<void>;
  updateSessionTags(id: string, tags: string[]): Promise<void>;
  // Admin 用户管理
  adminListUsers(query?: string, page?: number, size?: number): Promise<AdminUsersResponse>;
  adminUpdateUser(id: string, body: { role?: string; status?: string }): Promise<void>;
  adminUpdateQuota(id: string, tokenQuota: number): Promise<void>;
  adminGetUsageSummary(): Promise<UsageSummary>;
  adminGetUsageByModel(): Promise<{ by_model: Record<string, { tokens: number; cost_usd: number }> }>;
  adminGetUsageQuality(): Promise<UsageQualityCost>;
  adminListProviders(): Promise<AdminProvider[]>;
  adminCreateProvider(body: Partial<AdminProvider> & { name: string; provider_type: string }): Promise<void>;
  adminUpdateProvider(name: string, body: Partial<AdminProvider>): Promise<void>;
  adminDeleteProvider(name: string): Promise<void>;
  // Journal（回放剧场）
  getSessionJournal(sessionId: string, limit?: number): Promise<JournalResponse>;
  getJournalStats(sessionIds: string[]): Promise<JournalStatsResponse>;
  // Prompt 管理
  adminListPrompts(page?: number, size?: number): Promise<{ items: PromptRecord[]; total: number; page: number; size: number }>;
  adminGetPrompt(key: string, language?: string): Promise<{ key: string; language: string; content: string }>;
  adminPromptSmokeEval(req: PromptSmokeEvalRequest): Promise<PromptSmokeEvalResponse>;
  adminUpsertPrompt(key: string, language: string, content: string): Promise<void>;
  adminDeletePrompt(key: string, language: string): Promise<void>;
  // LLM Provider 管理
  adminListLLMProviders(): Promise<{ providers: LLMProviderRecord[] }>;
  adminCreateLLMProvider(body: Partial<LLMProviderRecord> & { name: string; provider_type: string }): Promise<void>;
  adminUpdateLLMProvider(name: string, body: Partial<LLMProviderRecord>): Promise<void>;
  adminDeleteLLMProvider(name: string): Promise<void>;
  // LLM Model 管理
  adminListLLMModels(): Promise<{ models: LLMModelRecord[] }>;
  adminCreateLLMModel(body: Partial<LLMModelRecord> & { name: string; model: string }): Promise<void>;
  adminUpdateLLMModel(name: string, body: Partial<LLMModelRecord>): Promise<void>;
  adminDeleteLLMModel(name: string): Promise<void>;
  // Admin Skill 管理（overlay: FS + DB）
  adminListSkills(): Promise<{ items: AdminSkillItem[]; total: number }>;
  adminGetSkill(name: string): Promise<AdminSkillDetail>;
  adminUpsertSkill(name: string, content: string, expectRevision?: number): Promise<void>;
  adminDeleteSkill(name: string): Promise<void>;
  adminListQualityCandidates(filter?: { status?: QualityCandidateStatus | ''; route?: string; page?: number; size?: number }): Promise<QualityCandidatesResponse>;
  adminCreateQualityCandidate(body: QualityCandidateCreateRequest): Promise<QualityCandidateRecord>;
  adminUpdateQualityCandidate(id: string, body: QualityCandidateUpdateRequest): Promise<QualityCandidateRecord>;
  adminExportQualityCandidate(id: string): Promise<QualityCandidateRecord['golden_case']>;
  adminListQualityWorkbenchClusters(filter?: { status?: QualityCandidateStatus | ''; route?: string; page?: number; size?: number }): Promise<QualityWorkbenchClustersResponse>;
  adminPreviewGroupingRules(): Promise<GroupingRulePreview>;
  adminListGroupingRules(): Promise<GroupingRulesResponse>;
  adminUpsertGroupingRule(id: string, rule: GroupingRule): Promise<GroupingRule>;
  adminDeleteGroupingRule(id: string): Promise<void>;
  adminPlanReplayFanout(body: { target_ids: string[]; limit?: number }): Promise<ReplayFanoutPlan>;
  adminCompareVersionMatrix(body: VersionMatrixInput): Promise<VersionDiff>;
  adminCreateReplayJobs(body: { kind: string; target_ids: string[]; max_attempt?: number }): Promise<{ batch_id: string; jobs: ReplayJob[] }>;
  adminListReplayJobs(filter?: { batch_id?: string; status?: string; page?: number; size?: number }): Promise<ReplayJobsResponse>;
  adminCancelReplayJob(id: string): Promise<ReplayJob>;
  adminRunReplayJob(id: string): Promise<ReplayJob>;
  adminCreateBatchEval(body: { mode: string; since?: string; baseline_run_id?: string; cases_dir?: string }): Promise<BatchEvalRun>;
  adminListBatchEvals(): Promise<BatchEvalRunsResponse>;
  adminListQualityReports(): Promise<QualityReportsResponse>;
  adminGenerateQualityReport(weekStart: string): Promise<QualityReport>;
  adminGetQualityDashboardSnapshot(filter?: { since?: string; until?: string }): Promise<QualityDashboardSnapshot>;
  adminGetQualityDashboardSeries(filter?: { since?: string; until?: string }): Promise<{ items: QualityDashboardSeriesPoint[] }>;
  adminGetMemoryGovernance(limit?: number): Promise<MemoryGovernanceStats>;
  adminPruneMemoryGovernance(options?: { dryRun?: boolean; minConfidence?: number; maxMemories?: number; limit?: number }): Promise<MemoryPruneResponse>;
  adminExportMemory(options?: { userId?: string; user_id?: string; limit?: number }): Promise<MemoryExportDocument>;
  adminImportMemory(body: { user_id?: string; reset_ids?: boolean; document: MemoryExportDocument | unknown }): Promise<MemoryImportResponse>;
  adminPlanVectorSpaceMigration(body: { target_space?: string; batch_size?: number; resume_token?: string; offset?: number; dry_run?: boolean; apply?: boolean; limit?: number; user_id?: string }): Promise<VectorSpaceMigrationResponse>;
  adminGetEmbeddingBacklogStats(): Promise<EmbeddingBacklogStats>;
  adminGenerateOptimizationSuggestions(candidateId: string): Promise<{ suggestions: OptimizationReviewSuggestion[] }>;
  adminListOptimizationSuggestions(filter?: { status?: OptimizationSuggestionStatus | ''; target?: string; sourceCandidateId?: string; page?: number; size?: number }): Promise<OptimizationSuggestionsResponse>;
  adminApproveOptimizationSuggestion(id: string, note?: string): Promise<OptimizationReviewSuggestion>;
  adminRejectOptimizationSuggestion(id: string, note?: string): Promise<OptimizationReviewSuggestion>;
  adminApplyOptimizationSuggestion(id: string): Promise<OptimizationReviewSuggestion>;
  adminRollbackOptimizationSuggestion(id: string): Promise<OptimizationRollout>;
  adminComputeEvalDiff(body: { baseline: EvalRun; treatment: EvalRun }): Promise<EvalDiff>;
  adminListEvalDiffs(): Promise<EvalDiffsResponse>;
  adminGetEvalDiff(id: string): Promise<EvalDiff>;
  adminGenerateEvalDiffSuggestions(evalDiff: EvalDiff): Promise<{ suggestions: OptimizationReviewSuggestion[] }>;
  adminGetABReport(evalDiffId: string): Promise<ABReportResponse>;
  adminListOptimizationApprovals(filter?: { subjectId?: string; subject_id?: string }): Promise<OptimizationApprovalsResponse>;
  adminCreateOptimizationApproval(body: { subject_id: string; subject_type: OptimizationApprovalSubjectType; action: OptimizationApprovalAction; reviewer_role: OptimizationApprovalRole; note?: string }): Promise<OptimizationApprovalRecord>;
  adminEvaluateRollbackAlert(body: { eval_diff: EvalDiff; thresholds: RollbackAlertThresholds }): Promise<RollbackAlertResponse>;
  adminListRollbackAlerts(): Promise<RollbackAlertsResponse>;
  adminListRollbacks(): Promise<RollbacksResponse>;
}

// 本地节点客户端 - 直接调用 /api/v1/*
export class LocalNodeClient implements NodeClient {
  nodeId = 'local';
  private client: ApiClient;

  constructor(client: ApiClient = apiClient) {
    this.client = client;
  }

  async listSessions(): Promise<Session[]> {
    const res = await this.client.get<SessionListResponse>('/api/v1/sessions');
    return res.sessions || [];
  }

  getSession(id: string): Promise<SessionDetail> {
    return this.client.get(`/api/v1/sessions/${id}`);
  }

  createSession(name: string, tags?: string[]) {
    return this.client.post<{ session_id: string; name: string }>('/api/v1/sessions', { name, tags });
  }

  updateSession(id: string, req: UpdateSessionRequest): Promise<void> {
    return this.client.patch(`/api/v1/sessions/${id}`, req);
  }

  deleteSession(id: string): Promise<void> {
    return this.client.delete(`/api/v1/sessions/${id}`);
  }

  clearSession(id: string): Promise<void> {
    return this.client.postLong(`/api/v1/sessions/${id}/clear`);
  }

  revertSession(sessionId: string, revertTo: number): Promise<void> {
    return this.client.post(`/api/v1/sessions/${sessionId}/revert`, { revert_to: revertTo });
  }

  regenerateMessage(sessionId: string): Promise<void> {
    return this.client.postLong(`/api/v1/sessions/${sessionId}/regenerate`);
  }

  stopTask(sessionId: string): Promise<{ stopped: boolean }> {
    return this.client.post(`/api/v1/sessions/${sessionId}/stop`);
  }

  sendMessage(sessionId: string, content: string, options?: { attachments?: FileAttachment[]; deepThinking?: boolean }): Promise<SendMessageResponse> {
    if (options?.attachments?.length) {
      console.log('[DEBUG-UPLOAD] 发送附件:', options.attachments.map(a => ({
        filename: a.filename,
        mime_type: a.mime_type,
        data_len: a.data.length,
        size: a.size,
      })));
    }
    return this.client.postLong(`/api/v1/sessions/${sessionId}/messages`, {
      content,
      attachments: options?.attachments,
      reasoning_effort: options?.deepThinking ? 'high' : undefined,
    });
  }

  async getMessages(sessionId: string, limit?: number): Promise<Message[]> {
    const params = limit ? `?limit=${limit}` : '';
    const res = await this.client.get<MessagesListResponse>(`/api/v1/sessions/${sessionId}/messages${params}`);
    return res.messages || [];
  }

  getSessionTrace(sessionId: string, limit?: number): Promise<SessionTraceResponse> {
    const params = limit ? `?limit=${limit}` : '';
    return this.client.get(`/api/v1/sessions/${encodeURIComponent(sessionId)}/trace${params}`);
  }

  getTodoSnapshot(sessionId: string): Promise<TodoSnapshot> {
    return this.client.get(`/api/v1/sessions/${sessionId}/todos`);
  }

  resumeTodos(sessionId: string, expectedPlanVersion: number, expectedRuntimeEpoch: string): Promise<TodoResumeResponse> {
    return this.client.postLong(`/api/v1/sessions/${sessionId}/todos/resume`, {
      execute: true,
      expected_plan_version: expectedPlanVersion,
      expected_runtime_epoch: expectedRuntimeEpoch,
    });
  }

  submitInput(taskId: string, resp: InputResponse): Promise<void> {
    return this.client.post(`/api/v1/tasks/${taskId}/input`, resp);
  }

  getPendingInput(taskId: string): Promise<unknown[]> {
    return this.client.get(`/api/v1/tasks/${taskId}/pending-input`);
  }

  listAgents(): Promise<AgentInfo[]> {
    return this.client.get('/api/v1/agents');
  }

  listSkills(): Promise<SkillMetadata[]> {
    return this.client.get('/api/v1/skills');
  }

  health(): Promise<Health> {
    return this.client.get('/api/v1/health');
  }

  getWeChatConfig(): Promise<WeChatConfigResponse> {
    return this.client.get('/api/v1/config/channels/wechat');
  }

  updateWeChatProtocol(protocol: string, req: UpdateWeChatProtocolRequest): Promise<{ success: boolean; message: string }> {
    return this.client.patch(`/api/v1/config/channels/wechat/${protocol}`, req);
  }

  saveConfig(): Promise<{ success: boolean; message: string; path: string }> {
    return this.client.post('/api/v1/config/save');
  }

  reloadWeChatProtocol(protocol: string): Promise<{ success: boolean; message: string; status: string }> {
    return this.client.post(`/api/v1/config/channels/wechat/${protocol}/reload`);
  }

  // Model
  listModels(): Promise<{ models: ModelInfo[]; active: string }> {
    return this.client.get('/api/v1/models');
  }

  switchModel(name: string): Promise<void> {
    return this.client.put('/api/v1/model', { name });
  }

  // 远程 ACP Agent — 通过 Gateway RPC 调用
  private async rpc<T>(method: string, params?: unknown): Promise<T> {
    const res = await this.client.post<RPCResponse<T>>('/api/v1/rpc', {
      id: `rpc-${Date.now()}`,
      method,
      params,
    });
    if (res.error) throw new Error(res.error.message);
    return res.result as T;
  }

  listRemoteAgents(): Promise<RemoteAgentConfig[]> {
    return this.rpc<RemoteAgentConfig[]>('remote_agents.list');
  }

  connectRemoteAgent(cfg: RemoteAgentConfig): Promise<{ name: string; status: string }> {
    return this.rpc('remote_agents.connect', cfg);
  }

  disconnectRemoteAgent(name: string): Promise<{ name: string; status: string }> {
    return this.rpc('remote_agents.disconnect', { name });
  }

  healthCheckRemoteAgents(): Promise<Record<string, RemoteAgentHealth>> {
    return this.rpc('remote_agents.health');
  }

  // 运行时配置
  getRuntimeConfig(): Promise<RuntimeConfig> {
    return this.rpc<RuntimeConfig>('config.get');
  }

  updateRuntimeConfig(req: ConfigUpdateRequest): Promise<{ status: string }> {
    return this.rpc<{ status: string }>('config.update', req);
  }

  reloadChannels(platform?: string): Promise<{ status: string; channels: string[] }> {
    return this.rpc<{ status: string; channels: string[] }>('channel.reload', platform ? { platform } : {});
  }

  reloadMCP(name?: string): Promise<{ status: string; servers: string[] }> {
    return this.rpc<{ status: string; servers: string[] }>('mcp.reload', name ? { name } : {});
  }

  // 外部资源管理
  async listExternalResources(): Promise<ExternalResource[]> {
    const res = await this.rpc<{ resources: ExternalResource[] }>('resources.list');
    return res.resources || [];
  }

  saveExternalResource(resource: Partial<ExternalResource> & { name: string }): Promise<{ status: string; name: string }> {
    return this.rpc<{ status: string; name: string }>('resources.save', resource);
  }

  deleteExternalResource(name: string): Promise<{ status: string; name: string }> {
    return this.rpc<{ status: string; name: string }>('resources.delete', { name });
  }

  getWebSocketUrl(): string {
    const proto = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
    const host = import.meta.env.VITE_API_BASE
      ? new URL(import.meta.env.VITE_API_BASE).host
      : window.location.host;
    return `${proto}//${host}/api/v1/ws`;
  }

  async invokeTool(toolName: string, args: Record<string, unknown>): Promise<string> {
    const res = await this.client.post<{ result: string }>('/api/v1/tools/invoke', {
      tool_name: toolName,
      args,
    });
    return res.result;
  }

  starSession(id: string, starred: boolean): Promise<void> {
    return this.client.patch(`/api/v1/sessions/${id}/star`, { starred });
  }

  updateSessionTags(id: string, tags: string[]): Promise<void> {
    return this.client.patch(`/api/v1/sessions/${id}/tags`, { tags });
  }

  // Admin 用户管理
  async adminListUsers(query = '', page = 1, size = 20): Promise<AdminUsersResponse> {
    const params = new URLSearchParams({ page: String(page), size: String(size) });
    if (query) params.set('q', query);
    return this.client.get(`/api/v1/admin/users?${params}`);
  }

  adminUpdateUser(id: string, body: { role?: string; status?: string }): Promise<void> {
    return this.client.patch(`/api/v1/admin/users/${id}`, body);
  }

  adminUpdateQuota(id: string, tokenQuota: number): Promise<void> {
    return this.client.patch(`/api/v1/admin/users/${id}/quota`, { token_quota: tokenQuota });
  }

  adminGetUsageSummary(): Promise<UsageSummary> {
    return this.client.get('/api/v1/admin/usage/summary');
  }

  adminGetUsageByModel(): Promise<{ by_model: Record<string, { tokens: number; cost_usd: number }> }> {
    return this.client.get('/api/v1/admin/usage/by-model');
  }

  adminGetUsageQuality(): Promise<UsageQualityCost> {
    return this.client.get('/api/v1/admin/usage/quality');
  }

  async adminListProviders(): Promise<AdminProvider[]> {
    const res = await this.client.get<AdminProvidersResponse>('/api/v1/admin/auth/providers');
    return res.providers ?? [];
  }

  adminCreateProvider(body: Partial<AdminProvider> & { name: string; provider_type: string }): Promise<void> {
    return this.client.post('/api/v1/admin/auth/providers', body);
  }

  adminUpdateProvider(name: string, body: Partial<AdminProvider>): Promise<void> {
    return this.client.patch(`/api/v1/admin/auth/providers/${name}`, body);
  }

  adminDeleteProvider(name: string): Promise<void> {
    return this.client.delete(`/api/v1/admin/auth/providers/${name}`);
  }

  // Journal（回放剧场）
  async getSessionJournal(sessionId: string, limit?: number): Promise<JournalResponse> {
    const params = limit ? `?limit=${limit}` : '';
    return this.client.get(`/api/v1/sessions/${sessionId}/journal${params}`);
  }

  async getJournalStats(sessionIds: string[]): Promise<JournalStatsResponse> {
    return this.client.get(`/api/v1/journal/stats?session_ids=${sessionIds.join(',')}`);
  }

  async adminListPrompts(page = 1, size = 50): Promise<{ items: PromptRecord[]; total: number; page: number; size: number }> {
    return this.client.get(`/api/v1/admin/prompts?page=${page}&size=${size}`);
  }

  async adminGetPrompt(key: string, language = ''): Promise<{ key: string; language: string; content: string }> {
    return this.client.get(`/api/v1/admin/prompts/${key}?language=${encodeURIComponent(language)}`);
  }

  async adminPromptSmokeEval(req: PromptSmokeEvalRequest): Promise<PromptSmokeEvalResponse> {
    return this.client.post('/api/v1/admin/quality/prompt-smoke', req);
  }

  async adminUpsertPrompt(key: string, language: string, content: string): Promise<void> {
    return this.client.put(`/api/v1/admin/prompts/${key}`, { language, content });
  }

  async adminDeletePrompt(key: string, language: string): Promise<void> {
    return this.client.delete(`/api/v1/admin/prompts/${key}?language=${encodeURIComponent(language)}`);
  }

  adminListLLMProviders(): Promise<{ providers: LLMProviderRecord[] }> {
    return this.client.get('/api/v1/admin/llm/providers');
  }

  adminCreateLLMProvider(body: Partial<LLMProviderRecord> & { name: string; provider_type: string }): Promise<void> {
    return this.client.post('/api/v1/admin/llm/providers', body);
  }

  adminUpdateLLMProvider(name: string, body: Partial<LLMProviderRecord>): Promise<void> {
    return this.client.patch(`/api/v1/admin/llm/providers/${encodeURIComponent(name)}`, body);
  }

  adminDeleteLLMProvider(name: string): Promise<void> {
    return this.client.delete(`/api/v1/admin/llm/providers/${encodeURIComponent(name)}`);
  }

  adminListLLMModels(): Promise<{ models: LLMModelRecord[] }> {
    return this.client.get('/api/v1/admin/llm/models');
  }

  adminCreateLLMModel(body: Partial<LLMModelRecord> & { name: string; model: string }): Promise<void> {
    return this.client.post('/api/v1/admin/llm/models', body);
  }

  adminUpdateLLMModel(name: string, body: Partial<LLMModelRecord>): Promise<void> {
    return this.client.patch(`/api/v1/admin/llm/models/${encodeURIComponent(name)}`, body);
  }

  adminDeleteLLMModel(name: string): Promise<void> {
    return this.client.delete(`/api/v1/admin/llm/models/${encodeURIComponent(name)}`);
  }

  adminListSkills(): Promise<{ items: AdminSkillItem[]; total: number }> {
    return this.client.get('/api/v1/admin/skills');
  }

  adminGetSkill(name: string): Promise<AdminSkillDetail> {
    return this.client.get(`/api/v1/admin/skills/${encodeURIComponent(name)}`);
  }

  adminUpsertSkill(name: string, content: string, expectRevision = 0): Promise<void> {
    return this.client.put(`/api/v1/admin/skills/${encodeURIComponent(name)}`, {
      content,
      expect_revision: expectRevision,
    });
  }

  adminDeleteSkill(name: string): Promise<void> {
    return this.client.delete(`/api/v1/admin/skills/${encodeURIComponent(name)}`);
  }

  adminListQualityCandidates(filter: { status?: QualityCandidateStatus | ''; route?: string; page?: number; size?: number } = {}): Promise<QualityCandidatesResponse> {
    const params = new URLSearchParams({
      page: String(filter.page ?? 1),
      size: String(filter.size ?? 50),
    });
    if (filter.status) params.set('status', filter.status);
    if (filter.route) params.set('route', filter.route);
    return this.client.get(`/api/v1/admin/quality/candidates?${params}`);
  }

  adminCreateQualityCandidate(body: QualityCandidateCreateRequest): Promise<QualityCandidateRecord> {
    return this.client.post('/api/v1/admin/quality/candidates', body);
  }

  adminUpdateQualityCandidate(id: string, body: QualityCandidateUpdateRequest): Promise<QualityCandidateRecord> {
    return this.client.patch(`/api/v1/admin/quality/candidates/${encodeURIComponent(id)}`, body);
  }

  adminExportQualityCandidate(id: string): Promise<QualityCandidateRecord['golden_case']> {
    return this.client.get(`/api/v1/admin/quality/candidates/${encodeURIComponent(id)}/golden-case`);
  }

  adminListQualityWorkbenchClusters(filter: { status?: QualityCandidateStatus | ''; route?: string; page?: number; size?: number } = {}): Promise<QualityWorkbenchClustersResponse> {
    const params = new URLSearchParams({
      page: String(filter.page ?? 1),
      size: String(filter.size ?? 50),
    });
    if (filter.status) params.set('status', filter.status);
    if (filter.route) params.set('route', filter.route);
    return this.client.get(`/api/v1/admin/quality-workbench/clusters?${params}`);
  }

  adminPreviewGroupingRules(): Promise<GroupingRulePreview> {
    return this.client.post('/api/v1/admin/quality-workbench/grouping-rules/preview');
  }

  adminListGroupingRules(): Promise<GroupingRulesResponse> {
    return this.client.get('/api/v1/admin/quality-workbench/grouping-rules');
  }

  adminUpsertGroupingRule(id: string, rule: GroupingRule): Promise<GroupingRule> {
    return this.client.put(`/api/v1/admin/quality-workbench/grouping-rules/${encodeURIComponent(id)}`, rule);
  }

  adminDeleteGroupingRule(id: string): Promise<void> {
    return this.client.delete(`/api/v1/admin/quality-workbench/grouping-rules/${encodeURIComponent(id)}`);
  }

  adminPlanReplayFanout(body: { target_ids: string[]; limit?: number }): Promise<ReplayFanoutPlan> {
    return this.client.post('/api/v1/admin/quality-workbench/replays/fanout', body);
  }

  adminCompareVersionMatrix(body: VersionMatrixInput): Promise<VersionDiff> {
    return this.client.post('/api/v1/admin/quality-workbench/version-diff', body);
  }

  adminCreateReplayJobs(body: { kind: string; target_ids: string[]; max_attempt?: number }): Promise<{ batch_id: string; jobs: ReplayJob[] }> {
    return this.client.post('/api/v1/admin/quality-workbench/replays', body);
  }

  adminListReplayJobs(filter: { batch_id?: string; status?: string; page?: number; size?: number } = {}): Promise<ReplayJobsResponse> {
    const params = new URLSearchParams({
      page: String(filter.page ?? 1),
      size: String(filter.size ?? 50),
    });
    if (filter.batch_id) params.set('batch_id', filter.batch_id);
    if (filter.status) params.set('status', filter.status);
    return this.client.get(`/api/v1/admin/quality-workbench/replays?${params}`);
  }

  adminCancelReplayJob(id: string): Promise<ReplayJob> {
    return this.client.post(`/api/v1/admin/quality-workbench/replays/${encodeURIComponent(id)}/cancel`);
  }

  adminRunReplayJob(id: string): Promise<ReplayJob> {
    return this.client.post(`/api/v1/admin/quality-workbench/replays/${encodeURIComponent(id)}/run`);
  }

  adminCreateBatchEval(body: { mode: string; since?: string; baseline_run_id?: string; cases_dir?: string }): Promise<BatchEvalRun> {
    return this.client.post('/api/v1/admin/quality-workbench/batch-evals', body);
  }

  adminListBatchEvals(): Promise<BatchEvalRunsResponse> {
    return this.client.get('/api/v1/admin/quality-workbench/batch-evals');
  }

  adminListQualityReports(): Promise<QualityReportsResponse> {
    return this.client.get('/api/v1/admin/quality-workbench/reports');
  }

  adminGenerateQualityReport(weekStart: string): Promise<QualityReport> {
    return this.client.post('/api/v1/admin/quality-workbench/reports/generate', { week_start: weekStart });
  }

  adminGetQualityDashboardSnapshot(filter: { since?: string; until?: string } = {}): Promise<QualityDashboardSnapshot> {
    const params = new URLSearchParams();
    if (filter.since) params.set('since', filter.since);
    if (filter.until) params.set('until', filter.until);
    return this.client.get(`/api/v1/admin/quality-workbench/dashboard/snapshot?${params}`);
  }

  adminGetQualityDashboardSeries(filter: { since?: string; until?: string } = {}): Promise<{ items: QualityDashboardSeriesPoint[] }> {
    const params = new URLSearchParams();
    if (filter.since) params.set('since', filter.since);
    if (filter.until) params.set('until', filter.until);
    return this.client.get(`/api/v1/admin/quality-workbench/dashboard/series?${params}`);
  }

  adminGetMemoryGovernance(limit = 1000): Promise<MemoryGovernanceStats> {
    return this.client.get(`/api/v1/admin/memory/governance?limit=${limit}`);
  }

  adminPruneMemoryGovernance(options: { dryRun?: boolean; minConfidence?: number; maxMemories?: number; limit?: number } = {}): Promise<MemoryPruneResponse> {
    const params = new URLSearchParams({
      dry_run: String(options.dryRun !== false),
      limit: String(options.limit ?? 1000),
    });
    if (options.minConfidence != null) params.set('min_confidence', String(options.minConfidence));
    if (options.maxMemories != null) params.set('max_memories', String(options.maxMemories));
    return this.client.post(`/api/v1/admin/memory/prune?${params}`);
  }

  adminExportMemory(options: { userId?: string; user_id?: string; limit?: number } = {}): Promise<MemoryExportDocument> {
    const params = new URLSearchParams();
    const userID = options.user_id ?? options.userId;
    if (userID) params.set('user_id', userID);
    if (options.limit != null) params.set('limit', String(options.limit));
    const query = params.toString();
    return this.client.get(`/api/v1/admin/memory/export${query ? `?${query}` : ''}`);
  }

  adminImportMemory(body: { user_id?: string; reset_ids?: boolean; document: MemoryExportDocument | unknown }): Promise<MemoryImportResponse> {
    return this.client.post('/api/v1/admin/memory/import', body);
  }

  adminPlanVectorSpaceMigration(body: { target_space?: string; batch_size?: number; resume_token?: string; offset?: number; dry_run?: boolean; apply?: boolean; limit?: number; user_id?: string }): Promise<VectorSpaceMigrationResponse> {
    return this.client.post('/api/v1/admin/memory/vector-space/plan', body);
  }

  adminGetEmbeddingBacklogStats(): Promise<EmbeddingBacklogStats> {
    return this.client.get('/api/v1/admin/memory/backlog/stats');
  }

  adminGenerateOptimizationSuggestions(candidateId: string): Promise<{ suggestions: OptimizationReviewSuggestion[] }> {
    return this.client.post('/api/v1/admin/optimization/suggestions', { candidate_id: candidateId });
  }

  adminListOptimizationSuggestions(filter: { status?: OptimizationSuggestionStatus | ''; target?: string; sourceCandidateId?: string; page?: number; size?: number } = {}): Promise<OptimizationSuggestionsResponse> {
    const params = new URLSearchParams({
      page: String(filter.page ?? 1),
      size: String(filter.size ?? 50),
    });
    if (filter.status) params.set('status', filter.status);
    if (filter.target) params.set('target', filter.target);
    if (filter.sourceCandidateId) params.set('source_candidate_id', filter.sourceCandidateId);
    return this.client.get(`/api/v1/admin/optimization/suggestions?${params}`);
  }

  adminApproveOptimizationSuggestion(id: string, note = ''): Promise<OptimizationReviewSuggestion> {
    return this.client.post(`/api/v1/admin/optimization/suggestions/${encodeURIComponent(id)}/approve`, { note });
  }

  adminRejectOptimizationSuggestion(id: string, note = ''): Promise<OptimizationReviewSuggestion> {
    return this.client.post(`/api/v1/admin/optimization/suggestions/${encodeURIComponent(id)}/reject`, { note });
  }

  adminApplyOptimizationSuggestion(id: string): Promise<OptimizationReviewSuggestion> {
    return this.client.post(`/api/v1/admin/optimization/suggestions/${encodeURIComponent(id)}/apply`);
  }

  adminRollbackOptimizationSuggestion(id: string): Promise<OptimizationRollout> {
    return this.client.post(`/api/v1/admin/optimization/suggestions/${encodeURIComponent(id)}/rollback`);
  }

  adminComputeEvalDiff(body: { baseline: EvalRun; treatment: EvalRun }): Promise<EvalDiff> {
    return this.client.post('/api/v1/admin/optimization/eval-diffs', body);
  }

  adminListEvalDiffs(): Promise<EvalDiffsResponse> {
    return this.client.get('/api/v1/admin/optimization/eval-diffs');
  }

  adminGetEvalDiff(id: string): Promise<EvalDiff> {
    return this.client.get(`/api/v1/admin/optimization/eval-diffs/${encodeURIComponent(id)}`);
  }

  adminGenerateEvalDiffSuggestions(evalDiff: EvalDiff): Promise<{ suggestions: OptimizationReviewSuggestion[] }> {
    return this.client.post('/api/v1/admin/optimization/eval-diffs/suggestions', { eval_diff: evalDiff });
  }

  adminGetABReport(evalDiffId: string): Promise<ABReportResponse> {
    return this.client.post(`/api/v1/admin/optimization/eval-diffs/${encodeURIComponent(evalDiffId)}/report`);
  }

  adminListOptimizationApprovals(filter: { subjectId?: string; subject_id?: string } = {}): Promise<OptimizationApprovalsResponse> {
    const params = new URLSearchParams();
    const subjectID = filter.subject_id ?? filter.subjectId;
    if (subjectID) params.set('subject_id', subjectID);
    const query = params.toString();
    return this.client.get(`/api/v1/admin/optimization/approvals${query ? `?${query}` : ''}`);
  }

  adminCreateOptimizationApproval(body: { subject_id: string; subject_type: OptimizationApprovalSubjectType; action: OptimizationApprovalAction; reviewer_role: OptimizationApprovalRole; note?: string }): Promise<OptimizationApprovalRecord> {
    return this.client.post('/api/v1/admin/optimization/approvals', body);
  }

  adminEvaluateRollbackAlert(body: { eval_diff: EvalDiff; thresholds: RollbackAlertThresholds }): Promise<RollbackAlertResponse> {
    return this.client.post('/api/v1/admin/optimization/rollback-alerts/evaluate', body);
  }

  adminListRollbackAlerts(): Promise<RollbackAlertsResponse> {
    return this.client.get('/api/v1/admin/optimization/rollback-alerts');
  }

  adminListRollbacks(): Promise<RollbacksResponse> {
    return this.client.get('/api/v1/admin/optimization/rollbacks');
  }
}
