package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/chef-guo/agents-hive/internal/agentquality"
	"github.com/chef-guo/agents-hive/internal/auth"
	"github.com/chef-guo/agents-hive/internal/errs"
)

func (s *Server) handleAdminQualityCreateCandidate(w http.ResponseWriter, r *http.Request) {
	if s.qualityCandidateStore == nil {
		writeJSON(w, http.StatusServiceUnavailable, ErrorResponse{Error: "质量候选用例存储未启用", Code: errs.CodeInternal})
		return
	}

	var body struct {
		SessionID    string             `json:"session_id"`
		ReplayRef    string             `json:"replay_ref"`
		EventIndex   *int               `json:"event_index,omitempty"`
		Input        string             `json:"input"`
		QualityEvent agentquality.Event `json:"quality_event"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "无效的请求体", Code: errs.CodeBadRequest})
		return
	}
	if strings.TrimSpace(body.Input) == "" {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "input 不能为空", Code: errs.CodeInvalidInput})
		return
	}
	if !isRegressionCandidateEvent(body.QualityEvent) {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "只允许失败/阻塞/需人工介入的质量事件进入候选池", Code: errs.CodeInvalidInput})
		return
	}
	if strings.TrimSpace(body.ReplayRef) == "" {
		body.ReplayRef = strings.TrimSpace(body.QualityEvent.ReplayRef)
	}
	if strings.TrimSpace(body.ReplayRef) == "" && body.EventIndex != nil && *body.EventIndex >= 0 && strings.TrimSpace(body.SessionID) != "" {
		body.ReplayRef = strings.TrimSpace(body.SessionID) + ":step-" + strconv.Itoa(*body.EventIndex)
	}
	if body.QualityEvent.ReplayRef == "" {
		body.QualityEvent.ReplayRef = body.ReplayRef
	}

	rec := agentquality.CandidateFromFailure(body.SessionID, body.Input, body.ReplayRef, body.QualityEvent)
	if body.QualityEvent.Name == agentquality.EventReflection {
		rec = agentquality.CandidateFromReflection(body.SessionID, body.Input, body.ReplayRef, body.QualityEvent)
	}
	rec.CreatedBy = auth.UserIDFrom(r.Context())
	created, err := s.qualityCandidateStore.UpsertCandidate(r.Context(), rec)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: err.Error(), Code: errs.CodeInternal})
		return
	}
	writeJSON(w, http.StatusCreated, enrichQualityCandidate(*created))
}

func (s *Server) handleAdminQualityListCandidates(w http.ResponseWriter, r *http.Request) {
	if s.qualityCandidateStore == nil {
		writeJSON(w, http.StatusServiceUnavailable, ErrorResponse{Error: "质量候选用例存储未启用", Code: errs.CodeInternal})
		return
	}

	page, size := parsePagination(r)
	filter := agentquality.CandidateFilter{
		Status: agentquality.CandidateStatus(r.URL.Query().Get("status")),
		Route:  r.URL.Query().Get("route"),
		Limit:  size,
		Offset: (page - 1) * size,
	}
	if filter.Status != "" {
		if err := agentquality.ValidateCandidateStatus(filter.Status); err != nil {
			writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: err.Error(), Code: errs.CodeInvalidInput})
			return
		}
	}

	items, total, err := s.qualityCandidateStore.ListCandidates(r.Context(), filter)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: err.Error(), Code: errs.CodeInternal})
		return
	}
	enriched := make([]agentquality.CandidateRecord, len(items))
	for i := range items {
		enriched[i] = enrichQualityCandidate(items[i])
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"candidates": enriched,
		"total":      total,
		"page":       page,
		"size":       size,
	})
}

func (s *Server) handleAdminQualityUpdateCandidate(w http.ResponseWriter, r *http.Request) {
	if s.qualityCandidateStore == nil {
		writeJSON(w, http.StatusServiceUnavailable, ErrorResponse{Error: "质量候选用例存储未启用", Code: errs.CodeInternal})
		return
	}
	id := r.PathValue("id")
	if strings.TrimSpace(id) == "" {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "candidate id 不能为空", Code: errs.CodeBadRequest})
		return
	}

	var body struct {
		Status         agentquality.CandidateStatus `json:"status"`
		ReviewNote     string                       `json:"review_note"`
		PromotedCaseID string                       `json:"promoted_case_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "无效的请求体", Code: errs.CodeBadRequest})
		return
	}
	if err := agentquality.ValidateCandidateStatus(body.Status); err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: err.Error(), Code: errs.CodeInvalidInput})
		return
	}
	if body.Status == agentquality.CandidatePromoted && strings.TrimSpace(body.PromotedCaseID) == "" {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "promoted 必须提供 promoted_case_id", Code: errs.CodeInvalidInput})
		return
	}

	reviewer := auth.UserIDFrom(r.Context())
	err := s.qualityCandidateStore.UpdateCandidateStatus(r.Context(), id, body.Status, reviewer, body.ReviewNote, body.PromotedCaseID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, ErrorResponse{Error: "候选用例不存在", Code: errs.CodeNotFound})
			return
		}
		if strings.Contains(err.Error(), "candidate transition") {
			writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: err.Error(), Code: errs.CodeInvalidInput})
			return
		}
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: err.Error(), Code: errs.CodeInternal})
		return
	}

	got, ok, err := s.qualityCandidateStore.GetCandidate(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: err.Error(), Code: errs.CodeInternal})
		return
	}
	if !ok {
		writeJSON(w, http.StatusOK, map[string]string{"status": string(body.Status)})
		return
	}
	writeJSON(w, http.StatusOK, enrichQualityCandidate(*got))
}

func (s *Server) handleAdminQualityExportCandidate(w http.ResponseWriter, r *http.Request) {
	if s.qualityCandidateStore == nil {
		writeJSON(w, http.StatusServiceUnavailable, ErrorResponse{Error: "质量候选用例存储未启用", Code: errs.CodeInternal})
		return
	}
	id := r.PathValue("id")
	if strings.TrimSpace(id) == "" {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "candidate id 不能为空", Code: errs.CodeBadRequest})
		return
	}
	got, ok, err := s.qualityCandidateStore.GetCandidate(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: err.Error(), Code: errs.CodeInternal})
		return
	}
	if !ok {
		writeJSON(w, http.StatusNotFound, ErrorResponse{Error: "候选用例不存在", Code: errs.CodeNotFound})
		return
	}
	golden, err := agentquality.GoldenCaseFromPromotedCandidate(*got)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: err.Error(), Code: errs.CodeInvalidInput})
		return
	}
	writeJSON(w, http.StatusOK, golden)
}

func isRegressionCandidateEvent(ev agentquality.Event) bool {
	switch ev.FinalStatus {
	case agentquality.StatusFail, agentquality.StatusBlocked, agentquality.StatusNeedsUser:
		return true
	case agentquality.StatusPass:
		return false
	}
	return ev.FailureType != "" && ev.FailureType != agentquality.FailureNone
}

func enrichQualityCandidate(rec agentquality.CandidateRecord) agentquality.CandidateRecord {
	rec.Suggestions = agentquality.BuildOptimizationSuggestions(rec)
	if rec.Status == agentquality.CandidatePromoted {
		if golden, err := agentquality.GoldenCaseFromPromotedCandidate(rec); err == nil {
			rec.GoldenCase = &golden
		}
	}
	return rec
}
