package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/chef-guo/agents-hive/internal/agentquality"
	"github.com/chef-guo/agents-hive/internal/errs"
	"github.com/chef-guo/agents-hive/internal/memory"
	"github.com/chef-guo/agents-hive/internal/memoryobs"
)

func (s *Server) handleAdminMemoryGovernance(w http.ResponseWriter, r *http.Request) {
	if s.memoryStore == nil {
		writeJSON(w, http.StatusServiceUnavailable, ErrorResponse{Error: "memory store 未启用", Code: errs.CodeInternal})
		return
	}
	limit := parseIntDefault(r.URL.Query().Get("limit"), 1000)
	filter := adminMemoryFilterFromRequest(r)
	if err := filter.validate(); err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: err.Error(), Code: errs.CodeInvalidInput})
		return
	}
	policy := s.memoryGovernancePolicy(r)
	queryOpts := memory.GovernanceQueryOptions{
		Search:        filter.searchOptions(limit),
		Now:           nowFromQuery(r),
		MinConfidence: parseFloatDefault(r.URL.Query().Get("min_confidence"), policy.MinConfidence),
	}
	var stats memory.GovernanceStats
	var err error
	if filter.needsPostFilter() {
		result, listErr := s.memoryStore.List(r.Context(), queryOpts.Search)
		if listErr != nil {
			err = listErr
		} else {
			stats = memory.AnalyzeGovernance(filter.apply(result.Memories), queryOpts.Now, queryOpts.MinConfidence)
		}
	} else {
		stats, err = memory.GovernanceStatsForStore(r.Context(), s.memoryStore, queryOpts)
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: err.Error(), Code: errs.CodeInternal})
		return
	}
	writeJSON(w, http.StatusOK, withMemoryGovernancePolicy(stats, policy))
}

func (s *Server) handleAdminMemoryPrune(w http.ResponseWriter, r *http.Request) {
	if s.memoryStore == nil {
		writeJSON(w, http.StatusServiceUnavailable, ErrorResponse{Error: "memory store 未启用", Code: errs.CodeInternal})
		return
	}
	limit := parseIntDefault(r.URL.Query().Get("limit"), 1000)
	filter := adminMemoryFilterFromRequest(r)
	if err := filter.validate(); err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: err.Error(), Code: errs.CodeInvalidInput})
		return
	}
	policy := s.memoryGovernancePolicy(r)
	result, err := s.memoryStore.List(r.Context(), filter.searchOptions(limit))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: err.Error(), Code: errs.CodeInternal})
		return
	}
	result.Memories = filter.apply(result.Memories)
	plan := memory.PlanGovernancePrune(result.Memories, memory.GovernancePruneOptions{
		Now:           nowFromQuery(r),
		MinConfidence: parseFloatDefault(r.URL.Query().Get("min_confidence"), policy.MinConfidence),
		MaxMemories:   parseIntDefault(r.URL.Query().Get("max_memories"), policy.MaxMemories),
	})
	dryRun := r.URL.Query().Get("dry_run") != "false"
	pruned, err := memory.PruneGovernanceForStore(r.Context(), s.memoryStore, plan, dryRun)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: err.Error(), Code: errs.CodeInternal})
		return
	}
	writeJSON(w, http.StatusOK, pruned)
}

func (s *Server) handleAdminMemoryExport(w http.ResponseWriter, r *http.Request) {
	if s.memoryStore == nil {
		writeJSON(w, http.StatusServiceUnavailable, ErrorResponse{Error: "memory store 未启用", Code: errs.CodeInternal})
		return
	}
	userID := r.URL.Query().Get("user_id")
	limit := parseIntDefault(r.URL.Query().Get("limit"), 1000)
	filter := adminMemoryFilterFromRequest(r)
	filter.UserID = userID
	if err := filter.validate(); err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: err.Error(), Code: errs.CodeInvalidInput})
		return
	}
	result, err := s.memoryStore.List(r.Context(), filter.searchOptions(limit))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: err.Error(), Code: errs.CodeInternal})
		return
	}
	result.Memories = filter.apply(result.Memories)
	data, err := memory.ExportMemoriesJSON(result.Memories, memory.MemoryExportOptions{
		UserID:              userID,
		StrictUserIsolation: userID != "",
		Now:                 time.Now(),
	})
	if err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: err.Error(), Code: errs.CodeInvalidInput})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(data)
}

func (s *Server) handleAdminMemoryImport(w http.ResponseWriter, r *http.Request) {
	if s.memoryStore == nil {
		writeJSON(w, http.StatusServiceUnavailable, ErrorResponse{Error: "memory store 未启用", Code: errs.CodeInternal})
		return
	}
	var body struct {
		UserID      string          `json:"user_id"`
		Target      string          `json:"target"`
		TargetScope string          `json:"target_scope"`
		Scope       string          `json:"scope"`
		Kind        string          `json:"kind"`
		MemoryKind  string          `json:"memory_kind"`
		ResetIDs    bool            `json:"reset_ids"`
		Document    json.RawMessage `json:"document"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "无效的请求体", Code: errs.CodeBadRequest})
		return
	}
	// 强制隔离:跨用户写入是注入路径。body.UserID 必填,空一律拒绝。
	// 一次 import 只能写入一个 user_id,且 strict isolation 永远开,
	// 文档里 record.UserID 与 body.UserID 不一致直接拒绝(不静默跳过)。
	if body.UserID == "" {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "user_id is required for memory import (cross-user import is not allowed)", Code: errs.CodeBadRequest})
		return
	}
	records, err := memory.ImportMemoriesJSON(body.Document, memory.MemoryImportOptions{
		UserID:              body.UserID,
		StrictUserIsolation: true,
		ResetIDs:            body.ResetIDs,
	})
	if err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: err.Error(), Code: errs.CodeInvalidInput})
		return
	}
	filter := adminMemoryFilter{
		UserID:      body.UserID,
		Kind:        firstNonEmpty(body.MemoryKind, body.Kind),
		Target:      body.Target,
		TargetScope: firstNonEmpty(body.TargetScope, body.Scope),
	}
	if err := filter.validate(); err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: err.Error(), Code: errs.CodeInvalidInput})
		return
	}
	for _, record := range records {
		if !filter.matches(record) {
			writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "memory import document contains records outside requested target/kind/scope filter", Code: errs.CodeInvalidInput})
			return
		}
	}
	var ids []int64
	for i := range records {
		id, err := s.memoryStore.Save(r.Context(), &records[i])
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: err.Error(), Code: errs.CodeInternal})
			return
		}
		ids = append(ids, id)
	}
	writeJSON(w, http.StatusCreated, map[string]any{"imported": len(ids), "ids": ids})
}

func (s *Server) handleAdminMemoryInjectionExplain(w http.ResponseWriter, r *http.Request) {
	limit := parseIntDefault(r.URL.Query().Get("limit"), 20)
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	if s.memoryInjectionExplainReader != nil {
		events, err := s.memoryInjectionExplainReader.RecentMemoryInjectionEvents(r.Context(), limit)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: err.Error(), Code: errs.CodeInternal})
			return
		}
		items := make([]memoryInjectionExplainItem, 0, len(events))
		for _, ev := range events {
			items = append(items, memoryInjectionExplainItemFromEvent(ev))
		}
		writeJSON(w, http.StatusOK, memoryInjectionExplainResponse{
			Items:  items,
			Total:  len(items),
			Limit:  limit,
			Source: "hive_logs",
		})
		return
	}
	writeJSON(w, http.StatusOK, memoryInjectionExplainResponse{
		Items:  []memoryInjectionExplainItem{},
		Total:  0,
		Limit:  limit,
		Source: "fallback_empty",
	})
}

func (s *Server) handleAdminMemoryPromotionCandidates(w http.ResponseWriter, r *http.Request) {
	if s.memoryStore == nil {
		writeJSON(w, http.StatusServiceUnavailable, ErrorResponse{Error: "memory store 未启用", Code: errs.CodeInternal})
		return
	}
	candidates, limit, err := s.memoryPromotionCandidatesFromRequest(r.Context(), r, memoryPromotionCandidateRequest{
		Limit:         parseIntDefault(r.URL.Query().Get("limit"), 20),
		MinConfidence: parseFloatDefault(r.URL.Query().Get("min_confidence"), 0.75),
	})
	if err != nil {
		writeMemoryPromotionError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items": candidates,
		"total": len(candidates),
		"limit": limit,
	})
}

func (s *Server) handleAdminMemoryPromotionApply(w http.ResponseWriter, r *http.Request) {
	if s.memoryStore == nil {
		writeJSON(w, http.StatusServiceUnavailable, ErrorResponse{Error: "memory store 未启用", Code: errs.CodeInternal})
		return
	}
	var body struct {
		SubjectID     string  `json:"subject_id"`
		UserID        string  `json:"user_id"`
		Target        string  `json:"target"`
		TargetScope   string  `json:"target_scope"`
		Scope         string  `json:"scope"`
		Kind          string  `json:"kind"`
		MemoryKind    string  `json:"memory_kind"`
		Limit         int     `json:"limit"`
		MinConfidence float64 `json:"min_confidence"`
		ApprovalID    string  `json:"approval_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "无效的请求体", Code: errs.CodeBadRequest})
		return
	}
	body.SubjectID = strings.TrimSpace(body.SubjectID)
	if body.SubjectID == "" {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "subject_id 不能为空", Code: errs.CodeInvalidInput})
		return
	}
	if existing, ok, err := s.findAppliedMemoryPromotion(r.Context(), body.SubjectID, body.UserID); err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "读取已应用 memory promotion 失败", Code: errs.CodeStoreReadFailed})
		return
	} else if ok {
		audit := memory.DecodeMemoryPromotionAudit(existing.Metadata)
		writeJSON(w, http.StatusOK, map[string]any{
			"applied":           true,
			"already_applied":   true,
			"memory_id":         existing.ID,
			"subject_id":        body.SubjectID,
			"source_memory_ids": audit.SourceMemoryIDs,
			"approval_id":       audit.ApprovalID,
		})
		return
	}

	req := memoryPromotionCandidateRequest{
		UserID:        body.UserID,
		Target:        body.Target,
		TargetScope:   firstNonEmpty(body.TargetScope, body.Scope),
		Kind:          firstNonEmpty(body.MemoryKind, body.Kind),
		Limit:         body.Limit,
		MinConfidence: body.MinConfidence,
	}
	candidates, _, err := s.memoryPromotionCandidatesFromRequest(r.Context(), r, req)
	if err != nil {
		writeMemoryPromotionError(w, err)
		return
	}
	candidate, ok := findMemoryPromotionCandidate(candidates, body.SubjectID)
	if !ok {
		writeJSON(w, http.StatusNotFound, ErrorResponse{Error: "memory promotion 候选不存在或已不满足筛选条件", Code: errs.CodeNotFound})
		return
	}
	approval, ok, err := s.findApprovedMemoryPromotion(r.Context(), body.SubjectID, body.ApprovalID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "读取 memory promotion 审批失败", Code: errs.CodeStoreReadFailed})
		return
	}
	if !ok {
		writeJSON(w, http.StatusPreconditionRequired, ErrorResponse{Error: "memory promotion 需要 lead/admin approve 后才能应用", Code: errs.CodeFailedPrecondition})
		return
	}

	record := memory.BuildAppliedMemoryPromotion(candidate, memory.MemoryPromotionApplyOptions{
		Now:        time.Now(),
		ApprovalID: approval.ID,
		AppliedBy:  approval.Reviewer,
	})
	id, err := s.memoryStore.Save(r.Context(), &record)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "应用 memory promotion 失败", Code: errs.CodeStoreWriteFailed})
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"applied":           true,
		"memory_id":         id,
		"subject_id":        body.SubjectID,
		"source_memory_ids": candidate.SourceMemoryIDs,
		"approval_id":       approval.ID,
	})
}

func (s *Server) handleAdminMemoryVectorSpacePlan(w http.ResponseWriter, r *http.Request) {
	if s.memoryStore == nil {
		writeJSON(w, http.StatusServiceUnavailable, ErrorResponse{Error: "memory store 未启用", Code: errs.CodeInternal})
		return
	}
	var body struct {
		TargetSpace string `json:"target_space"`
		BatchSize   int    `json:"batch_size"`
		ResumeToken string `json:"resume_token"`
		Offset      int    `json:"offset"`
		DryRun      *bool  `json:"dry_run"`
		Apply       bool   `json:"apply"`
		Limit       int    `json:"limit"`
		UserID      string `json:"user_id"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if body.Limit <= 0 {
		body.Limit = 1000
	}
	result, err := s.memoryStore.List(r.Context(), memory.SearchOptions{UserID: body.UserID, Limit: body.Limit})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: err.Error(), Code: errs.CodeInternal})
		return
	}
	apply := body.Apply || (body.DryRun != nil && !*body.DryRun)
	plan := memory.PlanVectorSpaceMigration(result.Memories, memory.VectorSpaceMigrationOptions{
		TargetSpace: body.TargetSpace,
		BatchSize:   body.BatchSize,
		ResumeToken: body.ResumeToken,
		Offset:      body.Offset,
		DryRun:      !apply,
		Now:         time.Now(),
	})
	if apply {
		if s.memoryEmbeddingBacklog == nil {
			s.memoryEmbeddingBacklog = memory.NewInMemoryEmbeddingBacklog()
		}
		for _, update := range plan.Updates {
			if err := s.memoryStore.Update(r.Context(), &update.Record); err != nil {
				writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: err.Error(), Code: errs.CodeInternal})
				return
			}
			_, _ = s.memoryEmbeddingBacklog.Enqueue(r.Context(), memory.EmbeddingBacklogJob{
				MemoryID:    update.Record.ID,
				UserID:      update.Record.UserID,
				Content:     update.Record.Content,
				VectorSpace: memory.DecodeVectorSpace(update.Record.Metadata).Name,
				CreatedAt:   time.Now(),
				UpdatedAt:   time.Now(),
			})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"plan":    plan,
		"applied": apply,
		"updated": len(plan.Updates),
	})
}

func (s *Server) handleAdminMemoryBacklogStats(w http.ResponseWriter, r *http.Request) {
	if s.memoryEmbeddingBacklog == nil {
		s.memoryEmbeddingBacklog = memory.NewInMemoryEmbeddingBacklog()
	}
	stats, err := s.memoryEmbeddingBacklog.Stats(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: err.Error(), Code: errs.CodeInternal})
		return
	}
	writeJSON(w, http.StatusOK, stats)
}

func (s *Server) handleAdminMemoryProductionMetrics(w http.ResponseWriter, r *http.Request) {
	until := time.Now()
	if raw := r.URL.Query().Get("until"); raw != "" {
		parsed, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "until 必须是 RFC3339 时间", Code: errs.CodeBadRequest})
			return
		}
		until = parsed
	}
	window := parseMemoryMetricsWindow(r, 24*time.Hour)
	since := until.Add(-window)
	if raw := r.URL.Query().Get("since"); raw != "" {
		parsed, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "since 必须是 RFC3339 时间", Code: errs.CodeBadRequest})
			return
		}
		since = parsed
		window = until.Sub(since)
		if window <= 0 {
			writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "since 必须早于 until", Code: errs.CodeBadRequest})
			return
		}
	}
	bucketSize := parseMemoryMetricsBucket(r, time.Hour)
	if s.memoryProductionMetricsReader == nil {
		writeJSON(w, http.StatusOK, memoryobs.BuildProductionMetrics(nil, since, until, bucketSize, "fallback_empty"))
		return
	}
	metrics, err := s.memoryProductionMetricsReader.LoadProductionMetrics(r.Context(), since, until, bucketSize)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "读取 memory 生产指标失败", Code: errs.CodeStoreReadFailed})
		return
	}
	writeJSON(w, http.StatusOK, metrics)
}

type adminMemoryFilter struct {
	UserID      string
	Kind        string
	Target      string
	TargetScope string
}

type memoryPromotionCandidateRequest struct {
	UserID        string
	Target        string
	TargetScope   string
	Kind          string
	Limit         int
	MinConfidence float64
}

type memoryPromotionError struct {
	Status int
	Err    error
}

func (e memoryPromotionError) Error() string {
	if e.Err == nil {
		return ""
	}
	return e.Err.Error()
}

func (s *Server) memoryPromotionCandidatesFromRequest(ctx context.Context, r *http.Request, req memoryPromotionCandidateRequest) ([]memory.MemoryPromotionCandidate, int, error) {
	limit := req.Limit
	if limit <= 0 {
		limit = 20
	}
	if limit > 200 {
		limit = 200
	}
	filter := adminMemoryFilterFromRequest(r)
	if req.UserID != "" {
		filter.UserID = strings.TrimSpace(req.UserID)
	}
	if req.Target != "" {
		filter.Target = strings.TrimSpace(req.Target)
	}
	if req.TargetScope != "" {
		filter.TargetScope = strings.TrimSpace(req.TargetScope)
	}
	if req.Kind != "" {
		filter.Kind = strings.TrimSpace(req.Kind)
	}
	if err := filter.validate(); err != nil {
		return nil, limit, memoryPromotionError{Status: http.StatusBadRequest, Err: err}
	}
	search := filter.searchOptions(limit * 5)
	if search.Limit < 100 {
		search.Limit = 100
	}
	result, err := s.memoryStore.List(ctx, search)
	if err != nil {
		return nil, limit, memoryPromotionError{Status: http.StatusInternalServerError, Err: errs.New(errs.CodeStoreReadFailed, "读取 memory promotion 候选失败")}
	}
	records := result.Memories
	if filter.needsPostFilter() {
		records = filter.apply(records)
	}
	candidates := memory.GenerateMemoryPromotionCandidates(records, memory.MemoryPromotionOptions{
		Now:           nowFromQuery(r),
		UserID:        filter.UserID,
		Limit:         limit,
		MinConfidence: firstPositiveFloat(req.MinConfidence, 0.75),
	})
	applied, err := s.appliedMemoryPromotionSubjects(ctx, filter.UserID)
	if err != nil {
		return nil, limit, memoryPromotionError{Status: http.StatusInternalServerError, Err: errs.New(errs.CodeStoreReadFailed, "读取已应用 memory promotion 失败")}
	}
	if len(applied) > 0 {
		out := candidates[:0]
		for _, candidate := range candidates {
			if !applied[candidate.SubjectID] {
				out = append(out, candidate)
			}
		}
		candidates = out
	}
	return candidates, limit, nil
}

func writeMemoryPromotionError(w http.ResponseWriter, err error) {
	if typed, ok := err.(memoryPromotionError); ok {
		status := typed.Status
		if status == 0 {
			status = http.StatusInternalServerError
		}
		code := errs.CodeInternal
		if status == http.StatusBadRequest {
			code = errs.CodeInvalidInput
		}
		writeJSON(w, status, ErrorResponse{Error: typed.Error(), Code: code})
		return
	}
	writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: err.Error(), Code: errs.CodeInternal})
}

func findMemoryPromotionCandidate(candidates []memory.MemoryPromotionCandidate, subjectID string) (memory.MemoryPromotionCandidate, bool) {
	for _, candidate := range candidates {
		if candidate.SubjectID == subjectID {
			return candidate, true
		}
	}
	return memory.MemoryPromotionCandidate{}, false
}

func (s *Server) findApprovedMemoryPromotion(ctx context.Context, subjectID, approvalID string) (agentquality.ApprovalRecord, bool, error) {
	approvals, err := s.optimizationApprovalStoreOrDefault().ListApprovals(ctx, subjectID)
	if err != nil {
		return agentquality.ApprovalRecord{}, false, err
	}
	approvalID = strings.TrimSpace(approvalID)
	for i := len(approvals) - 1; i >= 0; i-- {
		approval := approvals[i]
		if approvalID != "" && approval.ID != approvalID {
			continue
		}
		if approval.SubjectType == agentquality.ApprovalSubjectMemoryPromotion && approval.Action == agentquality.ApprovalActionApprove {
			return approval, true, nil
		}
	}
	return agentquality.ApprovalRecord{}, false, nil
}

func (s *Server) findAppliedMemoryPromotion(ctx context.Context, subjectID, userID string) (memory.MemoryRecord, bool, error) {
	subjectID = strings.TrimSpace(subjectID)
	if subjectID == "" {
		return memory.MemoryRecord{}, false, nil
	}
	result, err := s.memoryStore.List(ctx, memory.SearchOptions{
		UserID: strings.TrimSpace(userID),
		Type:   memory.MemoryTypeProcedural,
		Limit:  5000,
	})
	if err != nil {
		return memory.MemoryRecord{}, false, err
	}
	for _, record := range result.Memories {
		if memory.MemoryPromotionSubjectIDFromRecord(record) == subjectID {
			return record, true, nil
		}
	}
	return memory.MemoryRecord{}, false, nil
}

func (s *Server) appliedMemoryPromotionSubjects(ctx context.Context, userID string) (map[string]bool, error) {
	result, err := s.memoryStore.List(ctx, memory.SearchOptions{
		UserID: strings.TrimSpace(userID),
		Type:   memory.MemoryTypeProcedural,
		Limit:  5000,
	})
	if err != nil {
		return nil, err
	}
	applied := map[string]bool{}
	for _, record := range result.Memories {
		subjectID := memory.MemoryPromotionSubjectIDFromRecord(record)
		if subjectID != "" {
			applied[subjectID] = true
		}
	}
	return applied, nil
}

func adminMemoryFilterFromRequest(r *http.Request) adminMemoryFilter {
	q := r.URL.Query()
	return adminMemoryFilter{
		UserID:      strings.TrimSpace(q.Get("user_id")),
		Kind:        firstNonEmpty(q.Get("memory_kind"), q.Get("kind")),
		Target:      q.Get("target"),
		TargetScope: firstNonEmpty(q.Get("target_scope"), q.Get("scope")),
	}
}

func (f adminMemoryFilter) searchOptions(limit int) memory.SearchOptions {
	return memory.SearchOptions{
		UserID: f.UserID,
		Type:   memory.MemoryType(strings.TrimSpace(f.Kind)),
		Limit:  limit,
	}
}

func (f adminMemoryFilter) validate() error {
	kind := strings.TrimSpace(f.Kind)
	if kind == "" {
		return nil
	}
	if !memory.ValidMemoryTypes[memory.MemoryType(kind)] {
		return errs.New(errs.CodeInvalidInput, "invalid memory kind: "+kind)
	}
	return nil
}

func (f adminMemoryFilter) apply(records []memory.MemoryRecord) []memory.MemoryRecord {
	if len(records) == 0 {
		return records
	}
	out := records[:0]
	for _, record := range records {
		if f.matches(record) {
			out = append(out, record)
		}
	}
	return out
}

func (f adminMemoryFilter) needsPostFilter() bool {
	return strings.TrimSpace(f.Target) != "" || strings.TrimSpace(f.TargetScope) != ""
}

func (f adminMemoryFilter) matches(record memory.MemoryRecord) bool {
	if f.UserID != "" && record.UserID != f.UserID {
		return false
	}
	if kind := strings.TrimSpace(f.Kind); kind != "" && string(record.Type) != kind {
		return false
	}
	if target := strings.TrimSpace(f.Target); target != "" && !metadataStringEquals(record.Metadata, target, "target", "target_id", "target_user_id", "user_id") {
		return false
	}
	if scope := strings.TrimSpace(f.TargetScope); scope != "" && !metadataStringEquals(record.Metadata, scope, "target_scope", "scope") {
		return false
	}
	return true
}

func metadataStringEquals(raw json.RawMessage, want string, keys ...string) bool {
	if len(raw) == 0 || want == "" {
		return false
	}
	var meta map[string]any
	if err := json.Unmarshal(raw, &meta); err != nil {
		return false
	}
	for _, key := range keys {
		if metadataValueEquals(meta[key], want) {
			return true
		}
	}
	return false
}

func metadataValueEquals(v any, want string) bool {
	switch typed := v.(type) {
	case string:
		return typed == want
	case map[string]any:
		for _, nested := range typed {
			if metadataValueEquals(nested, want) {
				return true
			}
		}
	case []any:
		for _, nested := range typed {
			if metadataValueEquals(nested, want) {
				return true
			}
		}
	}
	return false
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func firstPositiveFloat(values ...float64) float64 {
	for _, value := range values {
		if value > 0 {
			return value
		}
	}
	return 0
}

type memoryInjectionExplainResponse struct {
	Items  []memoryInjectionExplainItem `json:"items"`
	Total  int                          `json:"total"`
	Limit  int                          `json:"limit"`
	Source string                       `json:"source"`
}

type memoryInjectionExplainItem struct {
	Timestamp            time.Time         `json:"timestamp,omitempty"`
	SessionIDHash        string            `json:"session_id_hash,omitempty"`
	Route                string            `json:"route,omitempty"`
	PromptVersions       []string          `json:"prompt_versions,omitempty"`
	MemoryIDs            []int64           `json:"memory_ids,omitempty"`
	SkippedMemoryIDs     []int64           `json:"skipped_memory_ids,omitempty"`
	SkipCounts           map[string]int    `json:"skip_counts"`
	EstimatedTokens      int               `json:"estimated_tokens,omitempty"`
	MemoryInjected       bool              `json:"memory_injected"`
	FeedbackMemoryCount  int               `json:"feedback_memory_count,omitempty"`
	RegularMemoryCount   int               `json:"regular_memory_count,omitempty"`
	ContaminationCheck   string            `json:"contamination_check,omitempty"`
	AdditionalAttributes map[string]string `json:"additional_attributes,omitempty"`
}

func memoryInjectionExplainItemFromEvent(ev agentquality.Event) memoryInjectionExplainItem {
	ctx := ev.ContextBuild
	return memoryInjectionExplainItem{
		Timestamp:           ev.Ts,
		SessionIDHash:       ev.SessionIDHash,
		Route:               ev.Route,
		PromptVersions:      append([]string(nil), ctx.PromptVersions...),
		MemoryIDs:           append([]int64(nil), ctx.MemoryIDs...),
		SkippedMemoryIDs:    append([]int64(nil), ctx.SkippedMemoryIDs...),
		SkipCounts:          memoryInjectionSkipCounts(ctx),
		EstimatedTokens:     ctx.EstimatedTokens,
		MemoryInjected:      ctx.MemoryInjected,
		FeedbackMemoryCount: ctx.FeedbackMemoryCount,
		RegularMemoryCount:  ctx.RegularMemoryCount,
		ContaminationCheck:  ctx.ContaminationCheck,
	}
}

func memoryInjectionSkipCounts(ctx agentquality.ContextBuild) map[string]int {
	counts := map[string]int{}
	addCount := func(key string, value int) {
		if value > 0 {
			counts[key] = value
		}
	}
	addCount("expired", ctx.SkippedExpired)
	addCount("low_trust", ctx.SkippedLowTrust)
	addCount("cross_user", ctx.SkippedCrossUser)
	addCount("scope", ctx.SkippedScope)
	addCount("low_score", ctx.SkippedLowScore)
	addCount("token_budget", ctx.SkippedTokenBudget)
	addCount("feedback_budget", ctx.SkippedFeedbackBudget)
	addCount("regular_budget", ctx.SkippedRegularBudget)
	if ctx.SkippedMemoryTotal > 0 {
		counts["total"] = ctx.SkippedMemoryTotal
	}
	return counts
}

type pgMemoryInjectionExplainReader struct {
	pool *pgxpool.Pool
}

func (r *pgMemoryInjectionExplainReader) RecentMemoryInjectionEvents(ctx context.Context, limit int) ([]agentquality.Event, error) {
	if r == nil || r.pool == nil {
		return nil, nil
	}
	if limit <= 0 {
		limit = 20
	}
	rows, err := r.pool.Query(ctx, `
		SELECT attributes->'quality_event'
		  FROM hive_logs
		 WHERE message = $1
		   AND attributes ? 'quality_event'
		 ORDER BY ts DESC
		 LIMIT $2`, string(agentquality.EventContextBuild), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	events := make([]agentquality.Event, 0, limit)
	for rows.Next() {
		var raw json.RawMessage
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var ev agentquality.Event
		if err := json.Unmarshal(raw, &ev); err != nil || ev.Name != agentquality.EventContextBuild {
			continue
		}
		events = append(events, ev)
	}
	return events, rows.Err()
}

type memoryGovernancePolicyDTO struct {
	MinConfidence float64 `json:"min_confidence"`
	MaxMemories   int     `json:"max_memories"`
}

type memoryGovernanceResponse struct {
	memory.GovernanceStats
	Policy memoryGovernancePolicyDTO `json:"policy"`
}

func withMemoryGovernancePolicy(stats memory.GovernanceStats, policy memoryGovernancePolicyDTO) memoryGovernanceResponse {
	return memoryGovernanceResponse{GovernanceStats: stats, Policy: policy}
}

func (s *Server) memoryGovernancePolicy(r *http.Request) memoryGovernancePolicyDTO {
	policy := memoryGovernancePolicyDTO{MinConfidence: 0.5}
	if s.memoryGovernancePolicyStore == nil {
		return policy
	}
	raw, ok, err := s.memoryGovernancePolicyStore.GetMemoryGovernancePolicy(r.Context(), "default")
	if err != nil || !ok {
		return policy
	}
	_ = json.Unmarshal([]byte(raw), &policy)
	if policy.MinConfidence == 0 {
		policy.MinConfidence = 0.5
	}
	return policy
}

func parseIntDefault(raw string, def int) int {
	if raw == "" {
		return def
	}
	v, err := strconv.Atoi(raw)
	if err != nil || v < 0 {
		return def
	}
	return v
}

func parseFloatDefault(raw string, def float64) float64 {
	if raw == "" {
		return def
	}
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return def
	}
	return v
}

func parseMemoryMetricsWindow(r *http.Request, fallback time.Duration) time.Duration {
	if raw := r.URL.Query().Get("window_minutes"); raw != "" {
		minutes, err := strconv.Atoi(raw)
		if err == nil && minutes > 0 && minutes <= 7*24*60 {
			return time.Duration(minutes) * time.Minute
		}
	}
	return fallback
}

func parseMemoryMetricsBucket(r *http.Request, fallback time.Duration) time.Duration {
	if raw := r.URL.Query().Get("bucket_minutes"); raw != "" {
		minutes, err := strconv.Atoi(raw)
		if err == nil && minutes > 0 && minutes <= 24*60 {
			return time.Duration(minutes) * time.Minute
		}
	}
	return fallback
}

func nowFromQuery(r *http.Request) time.Time {
	if raw := r.URL.Query().Get("now"); raw != "" {
		if parsed, err := time.Parse(time.RFC3339, raw); err == nil {
			return parsed
		}
	}
	return time.Now()
}
