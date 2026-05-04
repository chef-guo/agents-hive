package api

import (
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

func TestAdminQualityCases_ReturnsFixtures(t *testing.T) {
	srv := newQualityAdminTestServer()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/quality/cases", nil)
	rec := httptest.NewRecorder()

	srv.handleAdminQualityListCases(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var got struct {
		Cases    []agentquality.Case `json:"cases"`
		Total    int                 `json:"total"`
		Required int                 `json:"required"`
	}
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&got))
	require.NotEmpty(t, got.Cases)
	require.Equal(t, len(got.Cases), got.Total)
	require.Greater(t, got.Required, 0)
	for _, c := range got.Cases {
		require.NoError(t, agentquality.ValidateCase(c), c.ID)
	}
}

func TestAdminQualityPromptSmoke_EmptyContentReturns400(t *testing.T) {
	srv := newQualityAdminTestServer()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/quality/prompt-smoke", strings.NewReader(`{
		"key": "system/main",
		"language": "zh",
		"content": "   "
	}`))
	rec := httptest.NewRecorder()

	srv.handleAdminQualityPromptSmoke(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestAdminQualityPromptSmoke_NormalReturns200(t *testing.T) {
	srv := newQualityAdminTestServer()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/quality/prompt-smoke", strings.NewReader(`{
		"key": "system/main",
		"language": "zh",
		"content": "你可以使用工具完成任务。"
	}`))
	rec := httptest.NewRecorder()

	srv.handleAdminQualityPromptSmoke(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var got struct {
		OK           bool     `json:"ok"`
		CheckedCases int      `json:"checked_cases"`
		Warnings     []string `json:"warnings"`
	}
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&got))
	require.True(t, got.OK)
	require.Greater(t, got.CheckedCases, 0)
	require.Empty(t, got.Warnings)
}

func TestAdminQualityPromptSmoke_PlanRuntimeKeyIsKnown(t *testing.T) {
	srv := newQualityAdminTestServer()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/quality/prompt-smoke", strings.NewReader(`{
		"key": "system/plan_runtime",
		"language": "zh",
		"content": "复杂任务需要自动进入计划模式，并使用工具维护 todo 状态。"
	}`))
	rec := httptest.NewRecorder()

	srv.handleAdminQualityPromptSmoke(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var got struct {
		OK       bool     `json:"ok"`
		Warnings []string `json:"warnings"`
	}
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&got))
	require.True(t, got.OK)
	require.NotContains(t, strings.Join(got.Warnings, "\n"), "未知 prompt key")
}

func TestAdminQualityPromptSmoke_WarningReturns200(t *testing.T) {
	srv := newQualityAdminTestServer()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/quality/prompt-smoke", strings.NewReader(`{
		"key": "system/safety",
		"language": "zh",
		"content": "请在执行前确认目标。"
	}`))
	rec := httptest.NewRecorder()

	srv.handleAdminQualityPromptSmoke(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var got struct {
		OK           bool     `json:"ok"`
		CheckedCases int      `json:"checked_cases"`
		Warnings     []string `json:"warnings"`
	}
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&got))
	require.True(t, got.OK)
	require.Greater(t, got.CheckedCases, 0)
	require.NotEmpty(t, got.Warnings)
}

func TestAdminQualityPromptSmoke_UnknownKeyBlocksSave(t *testing.T) {
	srv := newQualityAdminTestServer()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/quality/prompt-smoke", strings.NewReader(`{
		"key": "unknown/prompt",
		"language": "zh",
		"content": "你可以使用工具完成任务。"
	}`))
	rec := httptest.NewRecorder()

	srv.handleAdminQualityPromptSmoke(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var got struct {
		OK       bool     `json:"ok"`
		Warnings []string `json:"warnings"`
	}
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&got))
	require.False(t, got.OK)
	require.NotEmpty(t, got.Warnings)
}

func TestAdminQualityPromptSmoke_OverlongContentBlocksSave(t *testing.T) {
	srv := newQualityAdminTestServer()
	body, err := json.Marshal(map[string]string{
		"key":      "system/base",
		"language": "zh",
		"content":  strings.Repeat("工具", 10001),
	})
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/quality/prompt-smoke", strings.NewReader(string(body)))
	rec := httptest.NewRecorder()

	srv.handleAdminQualityPromptSmoke(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var got struct {
		OK       bool     `json:"ok"`
		Warnings []string `json:"warnings"`
	}
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&got))
	require.False(t, got.OK)
	require.NotEmpty(t, got.Warnings)
}

func newQualityAdminTestServer() *Server {
	return &Server{
		logger: zap.NewNop(),
		config: config.Default(),
	}
}
