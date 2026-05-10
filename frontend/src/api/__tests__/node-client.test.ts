import { describe, expect, it, vi } from 'vitest';
import { LocalNodeClient } from '../node-client';
import type { ApiClient } from '../client';
import type { EvalRun, GroupingRule, MemoryExportDocument, VectorSpaceMigrationResponse, VersionMatrixInput } from '../../types/api';

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
    delete: vi.fn(),
  } as unknown as ApiClient & {
    get: ReturnType<typeof vi.fn>;
    post: ReturnType<typeof vi.fn>;
    postLong: ReturnType<typeof vi.fn>;
    put: ReturnType<typeof vi.fn>;
    delete: ReturnType<typeof vi.fn>;
  };
}

describe('LocalNodeClient admin capability endpoints', () => {
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
});
