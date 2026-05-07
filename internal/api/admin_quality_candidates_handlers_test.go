package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/chef-guo/agents-hive/internal/agentquality"
	"github.com/chef-guo/agents-hive/internal/config"
)

type fakeQualityCandidateStore struct {
	records map[string]agentquality.CandidateRecord
}

func newFakeQualityCandidateStore() *fakeQualityCandidateStore {
	return &fakeQualityCandidateStore{records: map[string]agentquality.CandidateRecord{}}
}

func (s *fakeQualityCandidateStore) UpsertCandidate(_ context.Context, rec agentquality.CandidateRecord) (*agentquality.CandidateRecord, error) {
	if s.records == nil {
		s.records = map[string]agentquality.CandidateRecord{}
	}
	if existing, ok := s.records[rec.ID]; ok {
		return &existing, nil
	}
	s.records[rec.ID] = rec
	return &rec, nil
}

func (s *fakeQualityCandidateStore) ListCandidates(_ context.Context, filter agentquality.CandidateFilter) ([]agentquality.CandidateRecord, int, error) {
	var out []agentquality.CandidateRecord
	for _, rec := range s.records {
		if filter.Status != "" && rec.Status != filter.Status {
			continue
		}
		if filter.Route != "" && rec.Route != filter.Route {
			continue
		}
		out = append(out, rec)
	}
	return out, len(out), nil
}

func (s *fakeQualityCandidateStore) GetCandidate(_ context.Context, id string) (*agentquality.CandidateRecord, bool, error) {
	rec, ok := s.records[id]
	if !ok {
		return nil, false, nil
	}
	return &rec, true, nil
}

func (s *fakeQualityCandidateStore) UpdateCandidateStatus(_ context.Context, id string, status agentquality.CandidateStatus, reviewer, note, promotedCaseID string) error {
	if err := agentquality.ValidateCandidateStatus(status); err != nil {
		return err
	}
	rec, ok := s.records[id]
	if !ok {
		return nil
	}
	if err := agentquality.ValidateCandidateTransition(rec.Status, status); err != nil {
		return err
	}
	rec.Status = status
	rec.ReviewedBy = reviewer
	rec.ReviewNote = note
	rec.PromotedCaseID = promotedCaseID
	s.records[id] = rec
	return nil
}

func TestAdminQualityCandidates_NoStoreReturns503(t *testing.T) {
	srv := newQualityCandidateTestServer(nil)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/quality/candidates", nil)
	rec := httptest.NewRecorder()
	srv.handleAdminQualityListCandidates(rec, req)

	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
}

func TestAdminQualityCandidates_CreateCandidate(t *testing.T) {
	store := newFakeQualityCandidateStore()
	srv := newQualityCandidateTestServer(store)
	body := `{
		"session_id": "session-1",
		"replay_ref": "session-1:step-3",
		"input": "执行 rm -rf ./tmp-cache",
		"quality_event": {
			"name": "quality.permission_decision",
			"route": "im",
			"failure_type": "permission",
			"final_status": "needs_user",
			"tool_decision": {"actual": "bash"}
		}
	}`

	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/quality/candidates", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.handleAdminQualityCreateCandidate(rec, req)

	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	var got agentquality.CandidateRecord
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&got))
	require.Equal(t, agentquality.CandidateNew, got.Status)
	require.False(t, got.Case.Required)
	require.Equal(t, "dangerous", got.Risk)
}

func TestAdminQualityCandidates_CreateCandidatePersistsOptimizationSuggestions(t *testing.T) {
	store := newFakeQualityCandidateStore()
	srv := newQualityCandidateTestServer(store)
	body := `{
		"session_id": "session-1",
		"replay_ref": "session-1:step-4",
		"input": "定位 createPermissionPromptFn",
		"quality_event": {
			"name": "quality.tool_decision",
			"route": "web",
			"failure_type": "tool",
			"final_status": "fail",
			"prompt": {"key": "system/base", "version": "sha256:old"},
			"tool_decision": {"expected": ["grep"], "actual": "read_file"}
		}
	}`

	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/quality/candidates", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.handleAdminQualityCreateCandidate(rec, req)

	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	var got agentquality.CandidateRecord
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&got))
	require.Len(t, got.Suggestions, 2)

	stored := store.records[got.ID]
	require.Len(t, stored.Suggestions, 2)
	require.Equal(t, got.Suggestions, stored.Suggestions)
	require.Equal(t, agentquality.SuggestionPromptDiff, stored.Suggestions[0].Kind)
	require.Equal(t, agentquality.SuggestionToolDescription, stored.Suggestions[1].Kind)
}

func TestAdminQualityCandidates_CreateCandidateFromReflection(t *testing.T) {
	store := newFakeQualityCandidateStore()
	srv := newQualityCandidateTestServer(store)
	body := `{
		"session_id": "session-1",
		"replay_ref": "session-1:step-5",
		"input": "连续 shell 调用失败后改路",
		"quality_event": {
			"name": "quality.reflection",
			"route": "web",
			"failure_type": "runtime",
			"final_status": "blocked",
			"prompt": {"key": "system/base", "version": "sha256:old"},
			"reflection": {
				"trigger": "call_failure",
				"severity": "hard_stop",
				"tool_name": "shell",
				"consecutive": 3,
				"summary": "连续工具调用失败，应先总结错误并调整下一步"
			}
		}
	}`

	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/quality/candidates", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.handleAdminQualityCreateCandidate(rec, req)

	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	var got agentquality.CandidateRecord
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&got))
	require.Equal(t, agentquality.EventReflection, got.SourceEvent.Name)
	require.Equal(t, agentquality.FailureRuntime, got.FailureType)
	require.Len(t, got.Suggestions, 1)
	require.Equal(t, agentquality.SuggestionPromptDiff, got.Suggestions[0].Kind)
	require.True(t, got.Suggestions[0].ReviewRequired)
}

func TestAdminQualityCandidates_ListIncludesOptimizationSuggestions(t *testing.T) {
	store := newFakeQualityCandidateStore()
	rec := agentquality.CandidateFromFailure("session-1", "定位 createPermissionPromptFn", "session-1:step-4", agentquality.Event{
		Route:       "web",
		FailureType: agentquality.FailureTool,
		FinalStatus: agentquality.StatusFail,
		ToolDecision: agentquality.ToolDecision{
			Expected: []string{"grep"},
			Actual:   "read_file",
		},
	})
	rec.Status = agentquality.CandidateApproved
	store.records[rec.ID] = rec
	srv := newQualityCandidateTestServer(store)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/quality/candidates", nil)
	out := httptest.NewRecorder()
	srv.handleAdminQualityListCandidates(out, req)

	require.Equal(t, http.StatusOK, out.Code, out.Body.String())
	var got struct {
		Candidates []struct {
			ID          string                                `json:"id"`
			Suggestions []agentquality.OptimizationSuggestion `json:"optimization_suggestions"`
		} `json:"candidates"`
	}
	require.NoError(t, json.NewDecoder(out.Body).Decode(&got))
	require.Len(t, got.Candidates, 1)
	require.Len(t, got.Candidates[0].Suggestions, 2)
	require.Equal(t, agentquality.SuggestionPromptDiff, got.Candidates[0].Suggestions[0].Kind)
}

func TestAdminQualityCandidates_CreateCandidateDerivesReplayRef(t *testing.T) {
	store := newFakeQualityCandidateStore()
	srv := newQualityCandidateTestServer(store)
	body := `{
		"session_id": "session-1",
		"event_index": 7,
		"input": "分析 main.go 的权限边界",
		"quality_event": {
			"name": "quality.agent_turn",
			"route": "web",
			"failure_type": "tool",
			"final_status": "fail"
		}
	}`

	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/quality/candidates", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.handleAdminQualityCreateCandidate(rec, req)

	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	var got agentquality.CandidateRecord
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&got))
	require.Equal(t, "session-1:step-7", got.ReplayRef)
	require.Equal(t, "session-1:step-7", got.SourceEvent.ReplayRef)
}

func TestAdminQualityCandidates_CreateCandidateRejectsPassingEvent(t *testing.T) {
	store := newFakeQualityCandidateStore()
	srv := newQualityCandidateTestServer(store)
	body := `{
		"session_id": "session-1",
		"input": "读取 README",
		"quality_event": {
			"name": "quality.tool_decision",
			"route": "web",
			"failure_type": "none",
			"final_status": "pass",
			"tool_decision": {"actual": "read_file"}
		}
	}`

	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/quality/candidates", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.handleAdminQualityCreateCandidate(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	require.Empty(t, store.records)
}

func TestAdminQualityCandidates_InvalidStatusReturns400(t *testing.T) {
	store := newFakeQualityCandidateStore()
	rec := agentquality.CandidateFromFailure("session-1", "失败输入", "ref", agentquality.Event{
		Route:       "web",
		FailureType: agentquality.FailureTool,
		FinalStatus: agentquality.StatusFail,
	})
	store.records[rec.ID] = rec
	srv := newQualityCandidateTestServer(store)

	req := httptest.NewRequest(http.MethodPatch, "/api/v1/admin/quality/candidates/"+rec.ID, strings.NewReader(`{"status":"invalid"}`))
	req.SetPathValue("id", rec.ID)
	out := httptest.NewRecorder()
	srv.handleAdminQualityUpdateCandidate(out, req)

	require.Equal(t, http.StatusBadRequest, out.Code)
}

func TestAdminQualityCandidates_PromotedRequiresCaseID(t *testing.T) {
	store := newFakeQualityCandidateStore()
	rec := agentquality.CandidateFromFailure("session-1", "失败输入", "ref", agentquality.Event{
		Route:       "web",
		FailureType: agentquality.FailureTool,
		FinalStatus: agentquality.StatusFail,
	})
	store.records[rec.ID] = rec
	srv := newQualityCandidateTestServer(store)

	req := httptest.NewRequest(http.MethodPatch, "/api/v1/admin/quality/candidates/"+rec.ID, strings.NewReader(`{"status":"promoted"}`))
	req.SetPathValue("id", rec.ID)
	out := httptest.NewRecorder()
	srv.handleAdminQualityUpdateCandidate(out, req)

	require.Equal(t, http.StatusBadRequest, out.Code)
}

func TestAdminQualityCandidates_PromotedRequiresApproval(t *testing.T) {
	store := newFakeQualityCandidateStore()
	rec := agentquality.CandidateFromFailure("session-1", "失败输入", "ref", agentquality.Event{
		Route:       "web",
		FailureType: agentquality.FailureTool,
		FinalStatus: agentquality.StatusFail,
	})
	store.records[rec.ID] = rec
	srv := newQualityCandidateTestServer(store)

	req := httptest.NewRequest(http.MethodPatch, "/api/v1/admin/quality/candidates/"+rec.ID, strings.NewReader(`{
		"status": "promoted",
		"promoted_case_id": "aq08_tool_failure"
	}`))
	req.SetPathValue("id", rec.ID)
	out := httptest.NewRecorder()
	srv.handleAdminQualityUpdateCandidate(out, req)

	require.Equal(t, http.StatusBadRequest, out.Code, out.Body.String())
	require.Equal(t, agentquality.CandidateNew, store.records[rec.ID].Status)
	require.Empty(t, store.records[rec.ID].PromotedCaseID)
}

func TestAdminQualityCandidates_ApprovedThenPromotedReturnsGoldenCase(t *testing.T) {
	store := newFakeQualityCandidateStore()
	rec := agentquality.CandidateFromFailure("session-1", "定位 createPermissionPromptFn", "session-1:step-4", agentquality.Event{
		Route:       "web",
		FailureType: agentquality.FailureTool,
		FinalStatus: agentquality.StatusFail,
		ToolDecision: agentquality.ToolDecision{
			Expected: []string{"grep"},
			Actual:   "read_file",
		},
	})
	rec.Status = agentquality.CandidateApproved
	store.records[rec.ID] = rec
	srv := newQualityCandidateTestServer(store)

	req := httptest.NewRequest(http.MethodPatch, "/api/v1/admin/quality/candidates/"+rec.ID, strings.NewReader(`{
		"status": "promoted",
		"promoted_case_id": "aq08_tool_choice_create_permission",
		"review_note": "已脱敏，可复现"
	}`))
	req.SetPathValue("id", rec.ID)
	out := httptest.NewRecorder()
	srv.handleAdminQualityUpdateCandidate(out, req)

	require.Equal(t, http.StatusOK, out.Code, out.Body.String())
	var got struct {
		Status     agentquality.CandidateStatus `json:"status"`
		GoldenCase agentquality.Case            `json:"golden_case"`
	}
	require.NoError(t, json.NewDecoder(out.Body).Decode(&got))
	require.Equal(t, agentquality.CandidatePromoted, got.Status)
	require.Equal(t, "aq08_tool_choice_create_permission", got.GoldenCase.ID)
	require.True(t, got.GoldenCase.Required)
}

func TestAdminQualityCandidates_UpdatePromotedReturnsGoldenCase(t *testing.T) {
	store := newFakeQualityCandidateStore()
	rec := agentquality.CandidateFromFailure("session-1", "定位 createPermissionPromptFn", "session-1:step-4", agentquality.Event{
		Route:       "web",
		FailureType: agentquality.FailureTool,
		FinalStatus: agentquality.StatusFail,
		ToolDecision: agentquality.ToolDecision{
			Expected: []string{"grep"},
			Actual:   "read_file",
		},
	})
	rec.Status = agentquality.CandidateApproved
	store.records[rec.ID] = rec
	srv := newQualityCandidateTestServer(store)

	req := httptest.NewRequest(http.MethodPatch, "/api/v1/admin/quality/candidates/"+rec.ID, strings.NewReader(`{
		"status": "promoted",
		"promoted_case_id": "aq08_tool_choice_create_permission",
		"review_note": "已脱敏，可复现"
	}`))
	req.SetPathValue("id", rec.ID)
	out := httptest.NewRecorder()
	srv.handleAdminQualityUpdateCandidate(out, req)

	require.Equal(t, http.StatusOK, out.Code, out.Body.String())
	var got struct {
		Status     agentquality.CandidateStatus `json:"status"`
		GoldenCase agentquality.Case            `json:"golden_case"`
	}
	require.NoError(t, json.NewDecoder(out.Body).Decode(&got))
	require.Equal(t, agentquality.CandidatePromoted, got.Status)
	require.Equal(t, "aq08_tool_choice_create_permission", got.GoldenCase.ID)
	require.True(t, got.GoldenCase.Required)
	require.Equal(t, agentquality.StatusPass, got.GoldenCase.ExpectedStatus)
}

func TestAdminQualityCandidates_ExportPromotedCase(t *testing.T) {
	store := newFakeQualityCandidateStore()
	rec := agentquality.CandidateFromFailure("session-1", "定位 createPermissionPromptFn", "session-1:step-4", agentquality.Event{
		Route:       "web",
		FailureType: agentquality.FailureTool,
		FinalStatus: agentquality.StatusFail,
		ToolDecision: agentquality.ToolDecision{
			Expected: []string{"grep"},
			Actual:   "read_file",
		},
	})
	rec.Status = agentquality.CandidatePromoted
	rec.PromotedCaseID = "aq08_tool_choice_create_permission"
	store.records[rec.ID] = rec
	srv := newQualityCandidateTestServer(store)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/quality/candidates/"+rec.ID+"/golden-case", nil)
	req.SetPathValue("id", rec.ID)
	out := httptest.NewRecorder()
	srv.handleAdminQualityExportCandidate(out, req)

	require.Equal(t, http.StatusOK, out.Code, out.Body.String())
	var got agentquality.Case
	require.NoError(t, json.NewDecoder(out.Body).Decode(&got))
	require.Equal(t, "aq08_tool_choice_create_permission", got.ID)
	require.True(t, got.Required)
}

func TestAdminQualityWorkbenchClusters_GroupsCandidates(t *testing.T) {
	store := newFakeQualityCandidateStore()
	rec1 := agentquality.CandidateFromFailure("session-1", "定位权限", "session-1:step-1", agentquality.Event{
		Route:        "web",
		FailureType:  agentquality.FailureTool,
		FinalStatus:  agentquality.StatusFail,
		Prompt:       agentquality.PromptRef{Key: "system/base"},
		ToolDecision: agentquality.ToolDecision{Actual: "grep"},
		Attributes:   map[string]any{"error": "failed on /tmp/a/session-123"},
	})
	rec2 := agentquality.CandidateFromFailure("session-2", "定位权限", "session-2:step-1", agentquality.Event{
		Route:        "web",
		FailureType:  agentquality.FailureTool,
		FinalStatus:  agentquality.StatusFail,
		Prompt:       agentquality.PromptRef{Key: "system/base"},
		ToolDecision: agentquality.ToolDecision{Actual: "grep"},
		Attributes:   map[string]any{"error": "failed on /tmp/b/session-456"},
	})
	store.records[rec1.ID+"-1"] = rec1
	rec2.ID = rec1.ID + "-2"
	store.records[rec2.ID] = rec2
	srv := newQualityCandidateTestServer(store)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/quality-workbench/clusters", nil)
	rec := httptest.NewRecorder()
	srv.handleAdminQualityWorkbenchClusters(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var got struct {
		Clusters []struct {
			Size         int      `json:"size"`
			OpenCount    int      `json:"open_count"`
			CandidateIDs []string `json:"candidate_ids"`
		} `json:"clusters"`
		Total int `json:"total"`
	}
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&got))
	require.Equal(t, 1, got.Total)
	require.Len(t, got.Clusters, 1)
	require.Equal(t, 2, got.Clusters[0].Size)
	require.Equal(t, 2, got.Clusters[0].OpenCount)
}

func newQualityCandidateTestServer(store qualityCandidateStore) *Server {
	return &Server{
		logger:                zap.NewNop(),
		config:                config.Default(),
		qualityCandidateStore: store,
	}
}
