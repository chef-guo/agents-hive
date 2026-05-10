package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/chef-guo/agents-hive/internal/agentquality"
	"github.com/chef-guo/agents-hive/internal/config"
	"github.com/chef-guo/agents-hive/internal/memory"
	"github.com/chef-guo/agents-hive/internal/memoryobs"
)

func TestAdminMemoryGovernance_ReturnsStats(t *testing.T) {
	now := time.Now()
	store := &fakeAdminMemoryStore{records: []memory.MemoryRecord{
		{ID: 1, Metadata: memory.EncodeGovernance(nil, memory.Governance{Confidence: 0.9, ExpiresAt: now.Add(time.Hour)})},
		{ID: 2},
	}}
	srv := newAdminMemoryTestServer(store)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/memory/governance", nil)
	rec := httptest.NewRecorder()
	srv.handleAdminMemoryGovernance(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var got memory.GovernanceStats
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&got))
	require.Equal(t, 2, got.Total)
	require.Equal(t, 1, got.MissingGovernance)
}

func TestAdminMemoryPruneDryRunDoesNotDelete(t *testing.T) {
	now := time.Now()
	store := &fakeAdminMemoryStore{records: []memory.MemoryRecord{
		{ID: 1, Metadata: memory.EncodeGovernance(nil, memory.Governance{Confidence: 0.9, ExpiresAt: now.Add(-time.Hour)})},
	}}
	srv := newAdminMemoryTestServer(store)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/memory/prune?dry_run=true", nil)
	rec := httptest.NewRecorder()
	srv.handleAdminMemoryPrune(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Empty(t, store.deleted)
	var got struct {
		DryRun    bool    `json:"dry_run"`
		DeleteIDs []int64 `json:"delete_ids"`
	}
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&got))
	require.True(t, got.DryRun)
	require.Equal(t, []int64{1}, got.DeleteIDs)
}

func TestAdminMemoryPruneDeletesWhenExplicit(t *testing.T) {
	now := time.Now()
	store := &fakeAdminMemoryStore{records: []memory.MemoryRecord{
		{ID: 1, Metadata: memory.EncodeGovernance(nil, memory.Governance{Confidence: 0.9, ExpiresAt: now.Add(-time.Hour)})},
	}}
	srv := newAdminMemoryTestServer(store)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/memory/prune?dry_run=false", nil)
	rec := httptest.NewRecorder()
	srv.handleAdminMemoryPrune(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, []int64{1}, store.deleted)
}

func TestAdminMemoryGovernanceUsesPersistedPolicyDefaults(t *testing.T) {
	store := &fakeAdminMemoryStore{records: []memory.MemoryRecord{
		{ID: 1, Metadata: memory.EncodeGovernance(nil, memory.Governance{Confidence: 0.4, ExpiresAt: time.Now().Add(time.Hour)})},
	}}
	policies := newFakeOptimizationWritableStore()
	require.NoError(t, policies.UpsertMemoryGovernancePolicy(context.Background(), "default", `{"min_confidence":0.5,"max_memories":10}`, "test"))
	srv := newAdminMemoryTestServer(store)
	srv.memoryGovernancePolicyStore = policies

	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/memory/governance", nil)
	rec := httptest.NewRecorder()
	srv.handleAdminMemoryGovernance(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var got struct {
		memory.GovernanceStats
		Policy struct {
			MinConfidence float64 `json:"min_confidence"`
			MaxMemories   int     `json:"max_memories"`
		} `json:"policy"`
	}
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&got))
	require.Equal(t, 1, got.LowConfidence)
	require.Equal(t, 0.5, got.Policy.MinConfidence)
	require.Equal(t, 10, got.Policy.MaxMemories)
}

func TestAdminMemoryExportImportAndVectorSpaceApply(t *testing.T) {
	store := &fakeAdminMemoryStore{records: []memory.MemoryRecord{
		{ID: 1, UserID: "u1", Type: memory.MemoryTypeUser, Content: "alpha", Tags: []string{"a"}, Metadata: json.RawMessage(`{}`)},
	}}
	srv := newAdminMemoryTestServer(store)

	exportReq := httptest.NewRequest(http.MethodGet, "/api/v1/admin/memory/export?user_id=u1", nil)
	exportOut := httptest.NewRecorder()
	srv.handleAdminMemoryExport(exportOut, exportReq)
	require.Equal(t, http.StatusOK, exportOut.Code, exportOut.Body.String())
	require.Contains(t, exportOut.Body.String(), `"memories"`)

	importBody := `{"user_id":"u1","reset_ids":true,"document":` + exportOut.Body.String() + `}`
	importReq := httptest.NewRequest(http.MethodPost, "/api/v1/admin/memory/import", strings.NewReader(importBody))
	importOut := httptest.NewRecorder()
	srv.handleAdminMemoryImport(importOut, importReq)
	require.Equal(t, http.StatusCreated, importOut.Code, importOut.Body.String())
	var imported struct {
		Imported int     `json:"imported"`
		IDs      []int64 `json:"ids"`
	}
	require.NoError(t, json.NewDecoder(importOut.Body).Decode(&imported))
	require.Equal(t, 1, imported.Imported)
	require.NotEmpty(t, imported.IDs)

	dryRunReq := httptest.NewRequest(http.MethodPost, "/api/v1/admin/memory/vector-space/plan", strings.NewReader(`{"target_space":"memory:v2","batch_size":10}`))
	dryRunOut := httptest.NewRecorder()
	srv.handleAdminMemoryVectorSpacePlan(dryRunOut, dryRunReq)
	require.Equal(t, http.StatusOK, dryRunOut.Code, dryRunOut.Body.String())
	require.Empty(t, store.updated)

	applyReq := httptest.NewRequest(http.MethodPost, "/api/v1/admin/memory/vector-space/plan", strings.NewReader(`{"target_space":"memory:v2","apply":true,"batch_size":10}`))
	applyOut := httptest.NewRecorder()
	srv.handleAdminMemoryVectorSpacePlan(applyOut, applyReq)
	require.Equal(t, http.StatusOK, applyOut.Code, applyOut.Body.String())
	require.NotEmpty(t, store.updated)
	require.Equal(t, "memory:v2", memory.DecodeVectorSpace(store.updated[0].Metadata).Name)

	statsReq := httptest.NewRequest(http.MethodGet, "/api/v1/admin/memory/backlog/stats", nil)
	statsOut := httptest.NewRecorder()
	srv.handleAdminMemoryBacklogStats(statsOut, statsReq)
	require.Equal(t, http.StatusOK, statsOut.Code, statsOut.Body.String())
	var stats memory.EmbeddingBacklogStats
	require.NoError(t, json.NewDecoder(statsOut.Body).Decode(&stats))
	require.GreaterOrEqual(t, stats.Total, 1)
	job, ok := srv.memoryEmbeddingBacklog.(*memory.InMemoryEmbeddingBacklog).Get(1)
	require.True(t, ok)
	require.Equal(t, "memory:v2", job.VectorSpace)
}

func TestAdminMemoryExportFiltersUserKindAndTargetScope(t *testing.T) {
	store := &fakeAdminMemoryStore{records: []memory.MemoryRecord{
		{ID: 1, UserID: "u1", Type: memory.MemoryTypeUser, Content: "alpha", Metadata: json.RawMessage(`{"target":"profile","target_scope":"personal"}`)},
		{ID: 2, UserID: "u1", Type: memory.MemoryTypeProject, Content: "beta", Metadata: json.RawMessage(`{"target":"profile","target_scope":"personal"}`)},
		{ID: 3, UserID: "u1", Type: memory.MemoryTypeUser, Content: "gamma", Metadata: json.RawMessage(`{"target":"workspace","target_scope":"personal"}`)},
		{ID: 4, UserID: "u2", Type: memory.MemoryTypeUser, Content: "delta", Metadata: json.RawMessage(`{"target":"profile","target_scope":"personal"}`)},
	}}
	srv := newAdminMemoryTestServer(store)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/memory/export?user_id=u1&kind=user&target=profile&target_scope=personal", nil)
	out := httptest.NewRecorder()
	srv.handleAdminMemoryExport(out, req)

	require.Equal(t, http.StatusOK, out.Code, out.Body.String())
	var doc memory.MemoryExportDocument
	require.NoError(t, json.NewDecoder(out.Body).Decode(&doc))
	require.Equal(t, "u1", doc.UserID)
	require.Len(t, doc.Memories, 1)
	require.Equal(t, int64(1), doc.Memories[0].ID)
}

func TestAdminMemoryImportRejectsRecordsOutsideRequestedFilter(t *testing.T) {
	store := &fakeAdminMemoryStore{}
	srv := newAdminMemoryTestServer(store)
	doc := `{
		"version": 1,
		"user_id": "u1",
		"memories": [
			{"id":1,"user_id":"u1","type":"project","content":"wrong kind","metadata":{"target":"profile","target_scope":"personal"}}
		]
	}`
	body := `{"user_id":"u1","kind":"user","target":"profile","target_scope":"personal","reset_ids":true,"document":` + doc + `}`

	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/memory/import", strings.NewReader(body))
	out := httptest.NewRecorder()
	srv.handleAdminMemoryImport(out, req)

	require.Equal(t, http.StatusBadRequest, out.Code, out.Body.String())
	require.Empty(t, store.records)
}

func TestAdminMemoryInjectionExplainFallbackEmpty(t *testing.T) {
	srv := newAdminMemoryTestServer(&fakeAdminMemoryStore{})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/memory/injection/explain?limit=5", nil)
	out := httptest.NewRecorder()
	srv.handleAdminMemoryInjectionExplain(out, req)

	require.Equal(t, http.StatusOK, out.Code, out.Body.String())
	var got memoryInjectionExplainResponse
	require.NoError(t, json.NewDecoder(out.Body).Decode(&got))
	require.Equal(t, 5, got.Limit)
	require.Equal(t, 0, got.Total)
	require.Equal(t, "fallback_empty", got.Source)
	require.Empty(t, got.Items)
}

func TestAdminMemoryInjectionExplainReadsQualityEvents(t *testing.T) {
	srv := newAdminMemoryTestServer(&fakeAdminMemoryStore{})
	srv.memoryInjectionExplainReader = fakeMemoryInjectionExplainReader{events: []agentquality.Event{{
		Name:          agentquality.EventContextBuild,
		SessionIDHash: "sha256:abc",
		Route:         "web",
		Ts:            time.Date(2026, 5, 9, 10, 0, 0, 0, time.UTC),
		ContextBuild: agentquality.ContextBuild{
			MemoryInjected:      true,
			MemoryIDs:           []int64{1, 2},
			SkippedMemoryIDs:    []int64{3},
			SkippedExpired:      1,
			SkippedMemoryTotal:  1,
			EstimatedTokens:     42,
			FeedbackMemoryCount: 1,
			RegularMemoryCount:  1,
			PromptVersions:      []string{"p:v1"},
		},
	}},
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/memory/injection/explain?limit=5", nil)
	out := httptest.NewRecorder()
	srv.handleAdminMemoryInjectionExplain(out, req)

	require.Equal(t, http.StatusOK, out.Code, out.Body.String())
	var got memoryInjectionExplainResponse
	require.NoError(t, json.NewDecoder(out.Body).Decode(&got))
	require.Equal(t, "hive_logs", got.Source)
	require.Len(t, got.Items, 1)
	require.Equal(t, []int64{1, 2}, got.Items[0].MemoryIDs)
	require.Equal(t, 1, got.Items[0].SkipCounts["expired"])
	require.Equal(t, 1, got.Items[0].SkipCounts["total"])
	require.Equal(t, 42, got.Items[0].EstimatedTokens)
}

func TestAdminMemoryPromotionCandidatesAndApprovalRecord(t *testing.T) {
	store := &fakeAdminMemoryStore{records: []memory.MemoryRecord{
		{
			ID:      77,
			UserID:  "u1",
			Type:    memory.MemoryTypeFeedback,
			Content: "下次处理 memory promotion 时必须先生成 subject_id，再走 lead approval",
			Metadata: memory.EncodeGovernance(nil, memory.Governance{
				Source:       "feedback",
				Confidence:   0.91,
				SourceUserID: "u1",
			}),
		},
	}}
	srv := newAdminMemoryTestServer(store)
	srv.optimizationApprovalStore = agentquality.NewInMemoryApprovalStore()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/memory/promotions/candidates?user_id=u1&limit=5&now=2026-05-09T10:00:00Z", nil)
	out := httptest.NewRecorder()
	srv.handleAdminMemoryPromotionCandidates(out, req)

	require.Equal(t, http.StatusOK, out.Code, out.Body.String())
	var got struct {
		Items []memory.MemoryPromotionCandidate `json:"items"`
		Total int                               `json:"total"`
	}
	require.NoError(t, json.NewDecoder(out.Body).Decode(&got))
	require.Equal(t, 1, got.Total)
	require.Len(t, got.Items, 1)
	require.Equal(t, memory.MemoryPromotionTargetProcedural, got.Items[0].TargetType)
	require.Equal(t, []int64{77}, got.Items[0].SourceMemoryIDs)
	require.Equal(t, memory.MemoryTypeProcedural, got.Items[0].ProposedProceduralMemory.Type)

	approvalReq := httptest.NewRequest(http.MethodPost, "/api/v1/admin/optimization/approvals", strings.NewReader(`{"subject_id":"`+got.Items[0].SubjectID+`","subject_type":"memory_promotion","action":"approve","reviewer_role":"lead","note":"approved for procedural memory"}`))
	approvalOut := httptest.NewRecorder()
	srv.handleAdminOptimizationCreateApproval(approvalOut, approvalReq)
	require.Equal(t, http.StatusCreated, approvalOut.Code, approvalOut.Body.String())

	approvalsReq := httptest.NewRequest(http.MethodGet, "/api/v1/admin/optimization/approvals?subject_id="+got.Items[0].SubjectID, nil)
	approvalsOut := httptest.NewRecorder()
	srv.handleAdminOptimizationListApprovals(approvalsOut, approvalsReq)
	require.Equal(t, http.StatusOK, approvalsOut.Code, approvalsOut.Body.String())
	var approvals struct {
		Items []agentquality.ApprovalRecord `json:"items"`
		Total int                           `json:"total"`
	}
	require.NoError(t, json.NewDecoder(approvalsOut.Body).Decode(&approvals))
	require.Equal(t, 1, approvals.Total)
	require.Equal(t, agentquality.ApprovalSubjectMemoryPromotion, approvals.Items[0].SubjectType)
}

func TestAdminMemoryPromotionApplyRequiresApproval(t *testing.T) {
	store := &fakeAdminMemoryStore{records: []memory.MemoryRecord{memoryPromotionSourceRecord()}}
	srv := newAdminMemoryTestServer(store)
	srv.optimizationApprovalStore = agentquality.NewInMemoryApprovalStore()

	candidate := firstMemoryPromotionCandidate(t, srv)
	applyReq := httptest.NewRequest(http.MethodPost, "/api/v1/admin/memory/promotions/apply", strings.NewReader(`{"subject_id":"`+candidate.SubjectID+`","user_id":"u1"}`))
	applyOut := httptest.NewRecorder()
	srv.handleAdminMemoryPromotionApply(applyOut, applyReq)

	require.Equal(t, http.StatusPreconditionRequired, applyOut.Code, applyOut.Body.String())
	require.Len(t, store.records, 1)
}

func TestAdminMemoryPromotionApplyWritesProceduralMemory(t *testing.T) {
	store := &fakeAdminMemoryStore{records: []memory.MemoryRecord{memoryPromotionSourceRecord()}}
	srv := newAdminMemoryTestServer(store)
	srv.optimizationApprovalStore = agentquality.NewInMemoryApprovalStore()
	candidate := firstMemoryPromotionCandidate(t, srv)

	approvalReq := httptest.NewRequest(http.MethodPost, "/api/v1/admin/optimization/approvals", strings.NewReader(`{"subject_id":"`+candidate.SubjectID+`","subject_type":"memory_promotion","action":"approve","reviewer_role":"lead","note":"promote"}`))
	approvalOut := httptest.NewRecorder()
	srv.handleAdminOptimizationCreateApproval(approvalOut, approvalReq)
	require.Equal(t, http.StatusCreated, approvalOut.Code, approvalOut.Body.String())
	var approval agentquality.ApprovalRecord
	require.NoError(t, json.NewDecoder(approvalOut.Body).Decode(&approval))

	applyReq := httptest.NewRequest(http.MethodPost, "/api/v1/admin/memory/promotions/apply", strings.NewReader(`{"subject_id":"`+candidate.SubjectID+`","user_id":"u1","approval_id":"`+approval.ID+`"}`))
	applyOut := httptest.NewRecorder()
	srv.handleAdminMemoryPromotionApply(applyOut, applyReq)
	require.Equal(t, http.StatusCreated, applyOut.Code, applyOut.Body.String())
	var applied struct {
		Applied         bool    `json:"applied"`
		MemoryID        int64   `json:"memory_id"`
		SubjectID       string  `json:"subject_id"`
		SourceMemoryIDs []int64 `json:"source_memory_ids"`
		ApprovalID      string  `json:"approval_id"`
	}
	require.NoError(t, json.NewDecoder(applyOut.Body).Decode(&applied))
	require.True(t, applied.Applied)
	require.Equal(t, candidate.SubjectID, applied.SubjectID)
	require.Equal(t, approval.ID, applied.ApprovalID)
	require.Equal(t, []int64{77}, applied.SourceMemoryIDs)

	require.Len(t, store.records, 2)
	saved := store.records[1]
	require.Equal(t, memory.MemoryTypeProcedural, saved.Type)
	require.NotZero(t, saved.ID)
	require.Contains(t, saved.Content, "memory promotion")
	require.Equal(t, "memory_promotion_applied", memory.DecodeGovernance(saved.Metadata).Source)
	audit := memory.DecodeMemoryPromotionAudit(saved.Metadata)
	require.Equal(t, candidate.SubjectID, audit.SubjectID)
	require.Equal(t, approval.ID, audit.ApprovalID)
	require.Equal(t, []int64{77}, audit.SourceMemoryIDs)
}

func TestAdminMemoryPromotionApplyIsIdempotent(t *testing.T) {
	store := &fakeAdminMemoryStore{records: []memory.MemoryRecord{memoryPromotionSourceRecord()}}
	srv := newAdminMemoryTestServer(store)
	srv.optimizationApprovalStore = agentquality.NewInMemoryApprovalStore()
	candidate := firstMemoryPromotionCandidate(t, srv)

	approvalReq := httptest.NewRequest(http.MethodPost, "/api/v1/admin/optimization/approvals", strings.NewReader(`{"subject_id":"`+candidate.SubjectID+`","subject_type":"memory_promotion","action":"approve","reviewer_role":"lead","note":"promote"}`))
	approvalOut := httptest.NewRecorder()
	srv.handleAdminOptimizationCreateApproval(approvalOut, approvalReq)
	require.Equal(t, http.StatusCreated, approvalOut.Code, approvalOut.Body.String())

	body := `{"subject_id":"` + candidate.SubjectID + `","user_id":"u1"}`
	firstReq := httptest.NewRequest(http.MethodPost, "/api/v1/admin/memory/promotions/apply", strings.NewReader(body))
	firstOut := httptest.NewRecorder()
	srv.handleAdminMemoryPromotionApply(firstOut, firstReq)
	require.Equal(t, http.StatusCreated, firstOut.Code, firstOut.Body.String())
	var first struct {
		MemoryID int64 `json:"memory_id"`
	}
	require.NoError(t, json.NewDecoder(firstOut.Body).Decode(&first))

	secondReq := httptest.NewRequest(http.MethodPost, "/api/v1/admin/memory/promotions/apply", strings.NewReader(body))
	secondOut := httptest.NewRecorder()
	srv.handleAdminMemoryPromotionApply(secondOut, secondReq)
	require.Equal(t, http.StatusOK, secondOut.Code, secondOut.Body.String())
	var second struct {
		MemoryID       int64 `json:"memory_id"`
		AlreadyApplied bool  `json:"already_applied"`
	}
	require.NoError(t, json.NewDecoder(secondOut.Body).Decode(&second))
	require.True(t, second.AlreadyApplied)
	require.Equal(t, first.MemoryID, second.MemoryID)
	require.Len(t, store.records, 2)

	candidatesReq := httptest.NewRequest(http.MethodGet, "/api/v1/admin/memory/promotions/candidates?user_id=u1&limit=5", nil)
	candidatesOut := httptest.NewRecorder()
	srv.handleAdminMemoryPromotionCandidates(candidatesOut, candidatesReq)
	require.Equal(t, http.StatusOK, candidatesOut.Code, candidatesOut.Body.String())
	var candidates struct {
		Items []memory.MemoryPromotionCandidate `json:"items"`
		Total int                               `json:"total"`
	}
	require.NoError(t, json.NewDecoder(candidatesOut.Body).Decode(&candidates))
	require.Equal(t, 0, candidates.Total)
	require.Empty(t, candidates.Items)
}

func TestAdminMemoryProductionMetricsFallbackEmpty(t *testing.T) {
	srv := newAdminMemoryTestServer(&fakeAdminMemoryStore{})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/memory/metrics?window_minutes=120&bucket_minutes=60", nil)
	out := httptest.NewRecorder()
	srv.handleAdminMemoryProductionMetrics(out, req)

	require.Equal(t, http.StatusOK, out.Code, out.Body.String())
	var got memoryobs.ProductionMetrics
	require.NoError(t, json.NewDecoder(out.Body).Decode(&got))
	require.Equal(t, "fallback_empty", got.Source)
	require.Empty(t, got.Alerts)
	require.Len(t, got.Series, 2)
	require.Equal(t, float64(0), got.Series[0].EmbeddingDroppedTotal)
}

func TestAdminMemoryProductionMetricsReadsReader(t *testing.T) {
	srv := newAdminMemoryTestServer(&fakeAdminMemoryStore{})
	srv.memoryProductionMetricsReader = fakeMemoryProductionMetricsReader{
		metrics: memoryobs.ProductionMetrics{
			Source:   "hive_metrics",
			Snapshot: memoryobs.ProductionMetricsSnapshot{EmbeddingDroppedTotal: 7},
			Series:   []memoryobs.ProductionMetricsSeriesPoint{{EmbeddingDroppedTotal: 7}},
			Alerts:   []memoryobs.ProductionMetricAlert{{Code: "embedding_dropped"}},
		},
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/memory/metrics?window_minutes=120&bucket_minutes=60", nil)
	out := httptest.NewRecorder()
	srv.handleAdminMemoryProductionMetrics(out, req)

	require.Equal(t, http.StatusOK, out.Code, out.Body.String())
	var got memoryobs.ProductionMetrics
	require.NoError(t, json.NewDecoder(out.Body).Decode(&got))
	require.Equal(t, "hive_metrics", got.Source)
	require.Equal(t, float64(7), got.Snapshot.EmbeddingDroppedTotal)
	require.Len(t, got.Series, 1)
	require.Len(t, got.Alerts, 1)
}

func TestAdminMemoryImportRejectsCrossUserInjection(t *testing.T) {
	store := &fakeAdminMemoryStore{}
	srv := newAdminMemoryTestServer(store)

	// 攻击向量:admin 不传 body.user_id,JSONL 文档里手工写多个 user_id。
	// 期望:handler 必须 400 拒绝,不写任何 record。
	docWithCrossUser := `{
        "version": 1,
        "memories": [
            {"id":1,"user_id":"victim-a","type":"user","content":"injected"},
            {"id":2,"user_id":"victim-b","type":"user","content":"injected2"}
        ]
    }`
	body := `{"reset_ids":true,"document":` + docWithCrossUser + `}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/memory/import", strings.NewReader(body))
	out := httptest.NewRecorder()
	srv.handleAdminMemoryImport(out, req)

	require.Equal(t, http.StatusBadRequest, out.Code, out.Body.String())
	require.Contains(t, out.Body.String(), "user_id is required")
	require.Empty(t, store.records, "no record may be saved when user_id missing")

	// admin 显式传 user_id,但文档里某条 record.user_id 不一致 → strict isolation 拒绝整批,不静默跳过。
	bodyMismatch := `{"user_id":"target","reset_ids":true,"document":` + docWithCrossUser + `}`
	req2 := httptest.NewRequest(http.MethodPost, "/api/v1/admin/memory/import", strings.NewReader(bodyMismatch))
	out2 := httptest.NewRecorder()
	srv.handleAdminMemoryImport(out2, req2)
	require.Equal(t, http.StatusBadRequest, out2.Code, out2.Body.String())
	require.Empty(t, store.records, "no record may be saved when document contains mismatched user_id")
}

type fakeAdminMemoryStore struct {
	records []memory.MemoryRecord
	deleted []int64
	updated []memory.MemoryRecord
	nextID  int64
}

func (s *fakeAdminMemoryStore) Save(_ context.Context, rec *memory.MemoryRecord) (int64, error) {
	s.nextID++
	record := *rec
	record.ID = s.nextID
	s.records = append(s.records, record)
	return s.nextID, nil
}
func (s *fakeAdminMemoryStore) Get(context.Context, int64) (*memory.MemoryRecord, error) {
	return nil, nil
}
func (s *fakeAdminMemoryStore) BatchGet(_ context.Context, ids []int64) ([]memory.MemoryRecord, error) {
	want := map[int64]bool{}
	for _, id := range ids {
		want[id] = true
	}
	records := make([]memory.MemoryRecord, 0, len(ids))
	for _, record := range s.records {
		if want[record.ID] {
			records = append(records, record)
		}
	}
	return records, nil
}
func (s *fakeAdminMemoryStore) Update(_ context.Context, rec *memory.MemoryRecord) error {
	s.updated = append(s.updated, *rec)
	for i := range s.records {
		if s.records[i].ID == rec.ID {
			s.records[i] = *rec
			return nil
		}
	}
	s.records = append(s.records, *rec)
	return nil
}
func (s *fakeAdminMemoryStore) Delete(_ context.Context, id int64) error {
	s.deleted = append(s.deleted, id)
	return nil
}
func (s *fakeAdminMemoryStore) Search(context.Context, memory.SearchOptions) (*memory.SearchResult, error) {
	return &memory.SearchResult{}, nil
}
func (s *fakeAdminMemoryStore) List(_ context.Context, opts memory.SearchOptions) (*memory.SearchResult, error) {
	records := make([]memory.MemoryRecord, 0, len(s.records))
	for _, record := range s.records {
		if opts.UserID != "" && record.UserID != opts.UserID {
			continue
		}
		if opts.Type != "" && record.Type != opts.Type {
			continue
		}
		records = append(records, record)
	}
	return &memory.SearchResult{Memories: records, Total: len(records)}, nil
}
func (s *fakeAdminMemoryStore) Stats(context.Context) (*memory.MemoryStats, error) {
	return &memory.MemoryStats{}, nil
}
func (s *fakeAdminMemoryStore) SetEmbedding(memory.EmbeddingProvider, memory.VectorStore) {}
func (s *fakeAdminMemoryStore) Close() error                                              { return nil }

func newAdminMemoryTestServer(store memory.MemoryStore) *Server {
	return &Server{logger: zap.NewNop(), config: config.Default(), memoryStore: store}
}

func memoryPromotionSourceRecord() memory.MemoryRecord {
	return memory.MemoryRecord{
		ID:      77,
		UserID:  "u1",
		Type:    memory.MemoryTypeFeedback,
		Content: "下次处理 memory promotion 时必须先生成 subject_id，再走 lead approval",
		Metadata: memory.EncodeGovernance(nil, memory.Governance{
			Source:       "feedback",
			Confidence:   0.91,
			SourceUserID: "u1",
		}),
	}
}

func firstMemoryPromotionCandidate(t *testing.T, srv *Server) memory.MemoryPromotionCandidate {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/memory/promotions/candidates?user_id=u1&limit=5", nil)
	out := httptest.NewRecorder()
	srv.handleAdminMemoryPromotionCandidates(out, req)
	require.Equal(t, http.StatusOK, out.Code, out.Body.String())
	var got struct {
		Items []memory.MemoryPromotionCandidate `json:"items"`
	}
	require.NoError(t, json.NewDecoder(out.Body).Decode(&got))
	require.NotEmpty(t, got.Items)
	return got.Items[0]
}

type fakeMemoryInjectionExplainReader struct {
	events []agentquality.Event
	err    error
	limit  int
}

func (f fakeMemoryInjectionExplainReader) RecentMemoryInjectionEvents(_ context.Context, limit int) ([]agentquality.Event, error) {
	if f.err != nil {
		return nil, f.err
	}
	if limit > 0 && limit < len(f.events) {
		return append([]agentquality.Event(nil), f.events[:limit]...), nil
	}
	return append([]agentquality.Event(nil), f.events...), nil
}

type fakeMemoryProductionMetricsReader struct {
	metrics memoryobs.ProductionMetrics
	err     error
	since   time.Time
	until   time.Time
	bucket  time.Duration
}

func (f fakeMemoryProductionMetricsReader) LoadProductionMetrics(_ context.Context, since, until time.Time, bucketSize time.Duration) (memoryobs.ProductionMetrics, error) {
	if f.err != nil {
		return memoryobs.ProductionMetrics{}, f.err
	}
	f.since = since
	f.until = until
	f.bucket = bucketSize
	return f.metrics, nil
}
