import { describe, expect, it, vi } from 'vitest';
import { LocalNodeClient } from '../node-client';
import type { ApiClient } from '../client';
import type { EvalRun, GroupingRule, MemoryExportDocument, OptimizationReviewSuggestion, QualityCandidateRecord, VectorSpaceMigrationResponse, VersionMatrixInput } from '../../types/api';

vi.mock('../../store/auth', () => ({
  useAuthStore: {
    getState: () => ({ clearAuth: vi.fn() }),
  },
  refreshToken: vi.fn(),
}));

function createApiClientMock() {
  return {
    get: vi.fn(),
    post: vi.fn(),
    postLong: vi.fn(),
    put: vi.fn(),
    patch: vi.fn(),
    delete: vi.fn(),
    postFormLong: vi.fn(),
  } as unknown as ApiClient & {
    get: ReturnType<typeof vi.fn>;
    post: ReturnType<typeof vi.fn>;
    postLong: ReturnType<typeof vi.fn>;
    put: ReturnType<typeof vi.fn>;
    patch: ReturnType<typeof vi.fn>;
    delete: ReturnType<typeof vi.fn>;
    postFormLong: ReturnType<typeof vi.fn>;
  };
}

describe('LocalNodeClient admin capability endpoints', () => {
  it('lists models globally when no session id is provided', async () => {
    const api = createApiClientMock();
    api.get.mockResolvedValue({ models: [], active: '' });
    const client = new LocalNodeClient(api);

    await client.listModels();

    expect(api.get).toHaveBeenCalledWith('/api/v1/models');
  });

  it('lists models with an encoded session id when provided', async () => {
    const api = createApiClientMock();
    api.get.mockResolvedValue({ models: [], active: '' });
    const client = new LocalNodeClient(api);

    await client.listModels('sess/with space?x=1');

    expect(api.get).toHaveBeenCalledWith('/api/v1/models?session_id=sess%2Fwith%20space%3Fx%3D1');
  });

  it('switches model with the session-scoped request body', async () => {
    const api = createApiClientMock();
    api.put.mockResolvedValue(undefined);
    const client = new LocalNodeClient(api);

    await client.switchModel('sess-1', 'gpt-5');

    expect(api.put).toHaveBeenCalledWith('/api/v1/model', {
      name: 'gpt-5',
      session_id: 'sess-1',
    });
  });

  it('calls MCP runtime tool catalog RPC', async () => {
    const api = createApiClientMock();
    api.post.mockResolvedValue({
      result: { total: 1, mcp_count: 1, local_count: 0, servers: [], tools: [] },
    });
    const client = new LocalNodeClient(api);

    await client.listMCPTools();

    expect(api.post).toHaveBeenCalledWith('/api/v1/rpc', {
      id: expect.any(String),
      method: 'mcp.tools.list',
      params: undefined,
    });
  });

  it('uses the explicit external resource upsert API over the resources.save wire method', async () => {
    const api = createApiClientMock();
    api.post.mockResolvedValue({ result: { status: 'ok', name: 'prod-db' } });
    const client = new LocalNodeClient(api);

    await client.upsertExternalResource({
      name: 'prod-db',
      type: 'postgres',
      credentials: '',
      enabled: true,
    });

    expect(api.post).toHaveBeenCalledWith('/api/v1/rpc', {
      id: expect.any(String),
      method: 'resources.save',
      params: {
        name: 'prod-db',
        type: 'postgres',
        credentials: '',
        enabled: true,
      },
    });
  });

  it('calls session trace endpoint with optional limit', async () => {
    const api = createApiClientMock();
    api.get.mockResolvedValue({ session_id: 'sess-1', items: [] });
    const client = new LocalNodeClient(api);

    await client.getSessionTrace('sess/1', 200);

    expect(api.get).toHaveBeenCalledWith('/api/v1/sessions/sess%2F1/trace?limit=200');
  });

  it('calls session todo resume endpoint with version and runtime epoch guards', async () => {
    const api = createApiClientMock();
    api.postLong.mockResolvedValue({});
    const client = new LocalNodeClient(api);

    await client.resumeTodos('sess-1', 7, 'epoch-1');

    expect(api.postLong).toHaveBeenCalledWith('/api/v1/sessions/sess-1/todos/resume', {
      execute: true,
      expected_plan_version: 7,
      expected_runtime_epoch: 'epoch-1',
    });
  });

  it('calls KB effective bindings endpoint with runtime context filters', async () => {
    const api = createApiClientMock();
    api.get.mockResolvedValue({ bindings: [] });
    const client = new LocalNodeClient(api);

    await client.getKBEffectiveBindings({
      domainId: 'support',
      agentId: 'master',
      sessionTemplateId: 'template-1',
      sessionId: 'sess/1',
      tenantId: 'tenant-a',
    });

    expect(api.get).toHaveBeenCalledWith('/api/v1/kb/effective-bindings?domain_id=support&agent_id=master&session_template_id=template-1&session_id=sess%2F1&tenant_id=tenant-a');
  });

  it('sends explicit KB domain with chat messages', async () => {
    const api = createApiClientMock();
    api.postLong.mockResolvedValue({ content: 'ok', completed: true });
    const client = new LocalNodeClient(api);

    await client.sendMessage('sess-1', 'hello', { kbDomainId: 'support' });

    expect(api.postLong).toHaveBeenCalledWith('/api/v1/sessions/sess-1/messages', {
      content: 'hello',
      attachments: undefined,
      reasoning_effort: undefined,
      kb_domain_id: 'support',
    });
  });

  it('persists explicit KB domain on session updates', async () => {
    const api = createApiClientMock();
    api.patch.mockResolvedValue(undefined);
    const client = new LocalNodeClient(api);

    await client.updateSession('sess-1', { kb_domain_id: 'support' });

    expect(api.patch).toHaveBeenCalledWith('/api/v1/sessions/sess-1', { kb_domain_id: 'support' });
  });

  it('returns regenerate response from an encoded session endpoint', async () => {
    const api = createApiClientMock();
    api.postLong.mockResolvedValue({ content: 'new answer', completed: true });
    const client = new LocalNodeClient(api);

    const resp = await client.regenerateMessage('sess/1');

    expect(api.postLong).toHaveBeenCalledWith('/api/v1/sessions/sess%2F1/regenerate');
    expect(resp).toEqual({ content: 'new answer', completed: true });
  });

  it('resolves asset URLs through the asset API with KB runtime context', async () => {
    const api = createApiClientMock();
    api.get.mockResolvedValue({ url: '/api/v1/assets/proxy?x=1', expires_in: 300, mime_type: 'image/png', size: 3 });
    const client = new LocalNodeClient(api);

    await client.resolveAsset('asset://kb/user/u1/ns/doc/hash.png', {
      purpose: 'kb_management',
      sessionId: 'sess/1',
      domainId: 'customer_service',
      tenantId: 'tenant-a',
      agentId: 'support-agent',
      sessionTemplateId: 'tmpl-1',
    });

    expect(api.get).toHaveBeenCalledWith('/api/v1/assets/resolve?uri=asset%3A%2F%2Fkb%2Fuser%2Fu1%2Fns%2Fdoc%2Fhash.png&purpose=kb_management&session_id=sess%2F1&domain_id=customer_service&tenant_id=tenant-a&agent_id=support-agent&session_template_id=tmpl-1');
  });

  it('uploads KB documents with multipart form data when provided', async () => {
    const api = createApiClientMock();
    api.postFormLong.mockResolvedValue({ document: { id: 'doc-1' } });
    const client = new LocalNodeClient(api);
    const body = new FormData();
    body.append('title', 'Doc');

    await client.ingestKBDocument('ns/1', body, 'support');

    expect(api.postFormLong).toHaveBeenCalledWith('/api/v1/kb/namespaces/ns%2F1/documents:ingest-markdown?domain_id=support', body);
    expect(api.postLong).not.toHaveBeenCalled();
  });

  it('calls quality workbench preview, replay fanout, and version diff endpoints', async () => {
    const api = createApiClientMock();
    api.post.mockResolvedValue({});
    api.post.mockResolvedValueOnce({});
    api.post.mockResolvedValueOnce({});
    api.post.mockResolvedValueOnce({ plan: { dry_run: true, scanned: 0, updates: [] }, applied: false, updated: 0 } satisfies VectorSpaceMigrationResponse);
    const client = new LocalNodeClient(api);
    const input: VersionMatrixInput = {
      baseline_run_id: 'base',
      treatment_run_id: 'treat',
      baseline: [{ case_id: 'case-1', passed: true }],
      treatment: [{ case_id: 'case-1', passed: false, reason: 'wrong tool' }],
    };

    await client.adminPreviewGroupingRules();
    const rule: GroupingRule = {
      id: 'tool_split',
      name: 'Tool Split',
      priority: 1,
      enabled: true,
      match: { failure_type: 'tool' },
      key_fields: ['failure_type', 'tool'],
      digest_normalize: ['path', 'num'],
    };
    await client.adminListGroupingRules();
    await client.adminUpsertGroupingRule(rule.id, rule);
    await client.adminDeleteGroupingRule(rule.id);
    await client.adminPlanReplayFanout({ target_ids: ['cl_1', 'cl_2'], limit: 1 });
    await client.adminCompareVersionMatrix(input);

    expect(api.post).toHaveBeenNthCalledWith(1, '/api/v1/admin/quality-workbench/grouping-rules/preview');
    expect(api.get).toHaveBeenNthCalledWith(1, '/api/v1/admin/quality-workbench/grouping-rules');
    expect(api.put).toHaveBeenNthCalledWith(1, '/api/v1/admin/quality-workbench/grouping-rules/tool_split', rule);
    expect(api.delete).toHaveBeenNthCalledWith(1, '/api/v1/admin/quality-workbench/grouping-rules/tool_split');
    expect(api.post).toHaveBeenNthCalledWith(2, '/api/v1/admin/quality-workbench/replays/fanout', { target_ids: ['cl_1', 'cl_2'], limit: 1 });
    expect(api.post).toHaveBeenNthCalledWith(3, '/api/v1/admin/quality-workbench/version-diff', input);
  });

  it('passes quality workbench attribution filters to cluster and dashboard endpoints', async () => {
    const api = createApiClientMock();
    api.get.mockResolvedValue({});
    const client = new LocalNodeClient(api);

    await client.adminListQualityWorkbenchClusters({
      page: 2,
      size: 25,
      status: 'new',
      route: 'web',
      domain_id: 'customer_service',
      source_kind: 'workflow',
      source_name: 'case_triage',
      failure_type: 'tool',
    });
    await client.adminGetQualityDashboardSnapshot({
      since: '2026-05-01',
      until: '2026-05-08',
      domain_id: 'customer_service',
      source_kind: 'workflow',
      source_name: 'case_triage',
      failure_type: 'tool',
    });
    await client.adminGetQualityDashboardSeries({
      domain_id: 'customer_service',
      failure_type: 'permission',
    });

    expect(api.get).toHaveBeenNthCalledWith(1, '/api/v1/admin/quality-workbench/clusters?page=2&size=25&status=new&route=web&domain_id=customer_service&source_kind=workflow&source_name=case_triage&failure_type=tool');
    expect(api.get).toHaveBeenNthCalledWith(2, '/api/v1/admin/quality-workbench/dashboard/snapshot?since=2026-05-01&until=2026-05-08&domain_id=customer_service&source_kind=workflow&source_name=case_triage&failure_type=tool');
    expect(api.get).toHaveBeenNthCalledWith(3, '/api/v1/admin/quality-workbench/dashboard/series?domain_id=customer_service&failure_type=permission');
  });

  it('passes quality workbench attribution through replay and batch eval APIs', async () => {
    const api = createApiClientMock();
    api.get.mockResolvedValue({});
    api.post.mockResolvedValue({});
    const client = new LocalNodeClient(api);

    await client.adminCreateReplayJobs({
      kind: 'cluster',
      target_ids: ['cl_1'],
      max_attempt: 1,
      domain_id: 'customer_service',
      source_kind: 'workflow',
      source_name: 'case_triage',
    });
    await client.adminListReplayJobs({
      page: 2,
      size: 10,
      status: 'queued',
      kind: 'cluster',
      domain_id: 'customer_service',
      source_kind: 'workflow',
      source_name: 'case_triage',
    });
    await client.adminCreateBatchEval({
      mode: 'manual',
      cases_dir: './cases',
      domain_id: 'customer_service',
      source_kind: 'workflow',
      source_name: 'case_triage',
    });
    await client.adminListBatchEvals({
      page: 1,
      size: 20,
      status: 'failed',
      domain_id: 'customer_service',
    });

    expect(api.post).toHaveBeenNthCalledWith(1, '/api/v1/admin/quality-workbench/replays', {
      kind: 'cluster',
      target_ids: ['cl_1'],
      max_attempt: 1,
      domain_id: 'customer_service',
      source_kind: 'workflow',
      source_name: 'case_triage',
    });
    expect(api.get).toHaveBeenNthCalledWith(1, '/api/v1/admin/quality-workbench/replays?page=2&size=10&kind=cluster&status=queued&domain_id=customer_service&source_kind=workflow&source_name=case_triage');
    expect(api.post).toHaveBeenNthCalledWith(2, '/api/v1/admin/quality-workbench/batch-evals', {
      mode: 'manual',
      cases_dir: './cases',
      domain_id: 'customer_service',
      source_kind: 'workflow',
      source_name: 'case_triage',
    });
    expect(api.get).toHaveBeenNthCalledWith(2, '/api/v1/admin/quality-workbench/batch-evals?page=1&size=20&status=failed&domain_id=customer_service');
  });

  it('calls memory export/import/vector-space/backlog endpoints with backend JSON keys', async () => {
    const api = createApiClientMock();
    api.get.mockResolvedValue({});
    api.post.mockResolvedValue({});
    const client = new LocalNodeClient(api);
    const document: MemoryExportDocument = { version: 1, user_id: 'u1', memories: [] };

    await client.adminExportMemory({ userId: 'u1', target: 'profile', target_scope: 'personal', kind: 'user', limit: 25 });
    await client.adminImportMemory({ user_id: 'u1', target: 'profile', target_scope: 'personal', memory_kind: 'user', reset_ids: true, document });
    await client.adminGetMemoryInjectionExplain({ limit: 12 });
    await client.adminPlanVectorSpaceMigration({ target_space: 'memory:v2', batch_size: 10, dry_run: true, limit: 50 });
    await client.adminGetEmbeddingBacklogStats();
    await client.adminGetMemoryProductionMetrics({ windowMinutes: 120, bucketMinutes: 30 });
    await client.adminListMemoryPromotionCandidates({ userId: 'u1', target: 'profile', target_scope: 'personal', kind: 'feedback', limit: 8, minConfidence: 0.75 });
    await client.adminApplyMemoryPromotion({ subject_id: 'subj-1', user_id: 'u1', target: 'profile', target_scope: 'personal', memory_kind: 'feedback', limit: 8, min_confidence: 0.75, approval_id: 'appr-1' });

    expect(api.get).toHaveBeenNthCalledWith(1, '/api/v1/admin/memory/export?user_id=u1&target=profile&target_scope=personal&memory_kind=user&limit=25');
    expect(api.post).toHaveBeenNthCalledWith(1, '/api/v1/admin/memory/import', { user_id: 'u1', target: 'profile', target_scope: 'personal', memory_kind: 'user', reset_ids: true, document });
    expect(api.post).toHaveBeenNthCalledWith(2, '/api/v1/admin/memory/vector-space/plan', { target_space: 'memory:v2', batch_size: 10, dry_run: true, limit: 50 });
    expect(api.get).toHaveBeenNthCalledWith(2, '/api/v1/admin/memory/injection/explain?limit=12');
    expect(api.get).toHaveBeenNthCalledWith(3, '/api/v1/admin/memory/backlog/stats');
    expect(api.get).toHaveBeenNthCalledWith(4, '/api/v1/admin/memory/metrics?window_minutes=120&bucket_minutes=30');
    expect(api.get).toHaveBeenNthCalledWith(5, '/api/v1/admin/memory/promotions/candidates?user_id=u1&target=profile&target_scope=personal&memory_kind=feedback&limit=8&min_confidence=0.75');
    expect(api.post).toHaveBeenNthCalledWith(3, '/api/v1/admin/memory/promotions/apply', { subject_id: 'subj-1', user_id: 'u1', target: 'profile', target_scope: 'personal', memory_kind: 'feedback', limit: 8, min_confidence: 0.75, approval_id: 'appr-1' });
  });

  it('calls optimization eval diff, approvals, and rollback endpoints', async () => {
    const api = createApiClientMock();
    api.get.mockResolvedValue({});
    api.post.mockResolvedValue({});
    const client = new LocalNodeClient(api);
    const baseline: EvalRun = {
      id: 'base',
      results: [{ case_id: 'case-1', passed: true, cost_usd: 0.01, latency_ms: 100 }],
    };
    const treatment: EvalRun = {
      id: 'treat',
      results: [{ case_id: 'case-1', passed: false, cost_usd: 0.02, latency_ms: 200, failure_type: 'prompt' }],
    };
    const evalDiff = {
      id: 'evaldiff_base_treat',
      status: 'eval_diff_done' as const,
      baseline_run_id: 'base',
      treatment_run_id: 'treat',
      baseline: { case_count: 1, success_count: 1, success_rate: 1, average_cost_usd: 0.01, average_latency_ms: 100 },
      treatment: { case_count: 1, success_count: 0, success_rate: 0, average_cost_usd: 0.02, average_latency_ms: 200 },
      success_rate_delta: -1,
      average_cost_delta_usd: 0.01,
      average_latency_delta_ms: 100,
      success_p_value: 0,
      case_diffs: [],
    };

    await client.adminComputeEvalDiff({ baseline, treatment });
    await client.adminGenerateEvalDiffSuggestions(evalDiff);
    await client.adminGetABReport(evalDiff.id);
    await client.adminListOptimizationApprovals({ subjectId: evalDiff.id });
    await client.adminCreateOptimizationApproval({ subject_id: evalDiff.id, subject_type: 'eval_diff', action: 'approve', reviewer_role: 'lead', note: 'ok' });
    await client.adminEvaluateRollbackAlert({ eval_diff: evalDiff, thresholds: { min_success_rate_delta: -0.05, max_latency_delta_ms: 500 } });
    await client.adminListRollbackAlerts();
    await client.adminListRollbacks();

    expect(api.post).toHaveBeenNthCalledWith(1, '/api/v1/admin/optimization/eval-diffs', { baseline, treatment });
    expect(api.post).toHaveBeenNthCalledWith(2, '/api/v1/admin/optimization/eval-diffs/suggestions', { eval_diff: evalDiff });
    expect(api.post).toHaveBeenNthCalledWith(3, '/api/v1/admin/optimization/eval-diffs/evaldiff_base_treat/report');
    expect(api.get).toHaveBeenNthCalledWith(1, '/api/v1/admin/optimization/approvals?subject_id=evaldiff_base_treat');
    expect(api.post).toHaveBeenNthCalledWith(4, '/api/v1/admin/optimization/approvals', { subject_id: evalDiff.id, subject_type: 'eval_diff', action: 'approve', reviewer_role: 'lead', note: 'ok' });
    expect(api.post).toHaveBeenNthCalledWith(5, '/api/v1/admin/optimization/rollback-alerts/evaluate', { eval_diff: evalDiff, thresholds: { min_success_rate_delta: -0.05, max_latency_delta_ms: 500 } });
    expect(api.get).toHaveBeenNthCalledWith(2, '/api/v1/admin/optimization/rollback-alerts');
    expect(api.get).toHaveBeenNthCalledWith(3, '/api/v1/admin/optimization/rollbacks');
  });

  it('unwraps optimization mutation wrappers while preserving candidate context', async () => {
    const api = createApiClientMock();
    const client = new LocalNodeClient(api);
    const candidate = createQualityCandidate('candidate-1');
    const suggestion = createOptimizationSuggestion('suggestion-1');
    const rollout = {
      id: 'rollout-1',
      suggestion_id: suggestion.id,
      target: 'prompt' as const,
      target_key: 'triage.prompt',
      previous_value: 'old',
      previous_exists: true,
      applied_value: 'new',
      status: 'rolled_back' as const,
      applied_by: 'admin',
      rolled_back_by: 'admin',
      created_at: '2026-05-16T00:00:00Z',
      updated_at: '2026-05-16T00:00:00Z',
      rolled_back_at: '2026-05-16T00:00:00Z',
    };
    const approval = {
      id: 'approval-1',
      subject_id: suggestion.id,
      subject_type: 'suggestion' as const,
      action: 'reject' as const,
      reviewer: 'admin',
      reviewer_role: 'lead' as const,
      note: 'bad case',
      created_at: '2026-05-16T00:00:00Z',
    };
    api.post
      .mockResolvedValueOnce({ suggestion, candidate })
      .mockResolvedValueOnce({ rollout, candidate })
      .mockResolvedValueOnce({ approval, candidate });

    const rejected = await client.adminRejectOptimizationSuggestion(suggestion.id, 'bad case');
    const rolledBack = await client.adminRollbackOptimizationSuggestion(suggestion.id);
    const createdApproval = await client.adminCreateOptimizationApproval({
      subject_id: suggestion.id,
      subject_type: 'suggestion',
      action: 'reject',
      reviewer_role: 'lead',
      note: 'bad case',
    });

    expect(rejected.id).toBe(suggestion.id);
    expect(rejected.status).toBe('pending');
    expect(rejected.candidate?.id).toBe(candidate.id);
    expect(rolledBack.id).toBe(rollout.id);
    expect(rolledBack.status).toBe('rolled_back');
    expect(rolledBack.candidate?.id).toBe(candidate.id);
    expect(createdApproval.id).toBe(approval.id);
    expect(createdApproval.candidate?.id).toBe(candidate.id);
  });

  it('keeps optimization mutation responses compatible when backend returns raw objects', async () => {
    const api = createApiClientMock();
    const client = new LocalNodeClient(api);
    const suggestion = createOptimizationSuggestion('suggestion-raw');
    api.post.mockResolvedValueOnce(suggestion);

    const approved = await client.adminApproveOptimizationSuggestion(suggestion.id, 'ok');

    expect(approved).toEqual(suggestion);
  });
});

function createQualityCandidate(id: string): QualityCandidateRecord {
  return {
    id,
    status: 'new',
    route: 'web',
    session_id: 'session-1',
    replay_ref: 'replay-1',
    input: 'failed input',
    case: {
      id: 'case-1',
      name: 'Failed input',
      route: 'web',
      input: 'failed input',
      expected_status: 'success',
      required: true,
    },
    failure_type: 'regression',
    risk: 'medium',
    fingerprint: 'fp-1',
    source_event: {},
    created_at: '2026-05-16T00:00:00Z',
    updated_at: '2026-05-16T00:00:00Z',
  };
}

function createOptimizationSuggestion(id: string): OptimizationReviewSuggestion {
  return {
    id,
    status: 'pending',
    target: 'prompt',
    kind: 'prompt_diff_suggestion',
    title: 'Improve prompt',
    rationale: 'Regression candidate created',
    current_value: 'old',
    proposed_value: 'new',
    diff_format: 'text',
    source_candidate_id: 'candidate-1',
    review_required: true,
    created_by: 'system',
    apply_status: 'unapplied',
    created_at: '2026-05-16T00:00:00Z',
    updated_at: '2026-05-16T00:00:00Z',
    expires_at: '2026-05-17T00:00:00Z',
  };
}
