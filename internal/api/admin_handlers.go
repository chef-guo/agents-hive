package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"go.uber.org/zap"

	"github.com/chef-guo/agents-hive/internal/accounting"
	"github.com/chef-guo/agents-hive/internal/auth"
	"github.com/chef-guo/agents-hive/internal/errs"
	"github.com/chef-guo/agents-hive/internal/security"
)

// ── 辅助函数 ──────────────────────────────────────────────────────────────────

// parsePagination 解析分页参数，page≥1，size∈[1,100]
func parsePagination(r *http.Request) (page, size int) {
	page, _ = strconv.Atoi(r.URL.Query().Get("page"))
	size, _ = strconv.Atoi(r.URL.Query().Get("size"))
	if page < 1 {
		page = 1
	}
	if size < 1 || size > 100 {
		size = 20
	}
	return
}

// maskSecrets 将 config_json 中的敏感字段替换为统一脱敏占位。
func maskSecrets(config map[string]any) map[string]any {
	if config == nil {
		return map[string]any{}
	}
	redacted, err := security.RedactSecrets(config)
	if err != nil {
		return config
	}
	masked, ok := redacted.(map[string]any)
	if !ok {
		return map[string]any{}
	}
	return masked
}

// ── 用户管理 ──────────────────────────────────────────────────────────────────

// handleListUsers GET /api/v1/admin/users
func (s *Server) handleListUsers(w http.ResponseWriter, r *http.Request) {
	page, size := parsePagination(r)
	query := r.URL.Query().Get("q")

	users, total, err := s.authEngine.Store().ListUsers(r.Context(), query, page, size)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: err.Error(), Code: errs.CodeInternal})
		return
	}

	type userItem struct {
		ID           string `json:"id"`
		DisplayName  string `json:"display_name"`
		Email        string `json:"email"`
		Role         string `json:"role"`
		Status       string `json:"status"`
		AuthProvider string `json:"auth_provider"`
		TokenQuota   int64  `json:"token_quota"`
		TokenUsed    int64  `json:"token_used"`
	}

	items := make([]userItem, 0, len(users))
	for _, u := range users {
		items = append(items, userItem{
			ID:           u.ID,
			DisplayName:  u.DisplayName,
			Email:        u.Email,
			Role:         u.Role,
			Status:       u.Status,
			AuthProvider: u.AuthProvider,
			TokenQuota:   u.TokenQuota,
			TokenUsed:    u.TokenUsed,
		})
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"users": items,
		"total": total,
		"page":  page,
		"size":  size,
	})
}

// handleGetUser GET /api/v1/admin/users/{id}
func (s *Server) handleGetUser(w http.ResponseWriter, r *http.Request) {
	userID := r.PathValue("id")
	uwq, err := s.authEngine.Store().GetUserWithQuota(r.Context(), userID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: err.Error(), Code: errs.CodeInternal})
		return
	}
	if uwq == nil {
		writeJSON(w, http.StatusNotFound, ErrorResponse{Error: "用户不存在", Code: errs.CodeNotFound})
		return
	}
	writeJSON(w, http.StatusOK, uwq)
}

// handleUpdateUser PATCH /api/v1/admin/users/{id}
func (s *Server) handleUpdateUser(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Role   *string `json:"role,omitempty"`
		Status *string `json:"status,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "无效的请求体", Code: errs.CodeBadRequest})
		return
	}

	if body.Role != nil && *body.Role != "user" && *body.Role != "admin" {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "role 只能是 user 或 admin", Code: errs.CodeInvalidInput})
		return
	}
	if body.Status != nil && *body.Status != "active" && *body.Status != "disabled" {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "status 只能是 active 或 disabled", Code: errs.CodeInvalidInput})
		return
	}

	currentUser := auth.UserFrom(r.Context())
	targetID := r.PathValue("id")
	if body.Role != nil && currentUser != nil && currentUser.ID == targetID {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "不能修改自己的角色", Code: errs.CodeInvalidInput})
		return
	}

	// 先验证目标用户存在
	target, err := s.authEngine.Store().GetUserByID(r.Context(), targetID)
	if err != nil || target == nil {
		writeJSON(w, http.StatusNotFound, ErrorResponse{Error: "用户不存在", Code: errs.CodeNotFound})
		return
	}

	if body.Role != nil {
		if err := s.authEngine.Store().UpdateUserRole(r.Context(), targetID, *body.Role); err != nil {
			writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: err.Error(), Code: errs.CodeInternal})
			return
		}
		s.authEngine.InvalidateUserCacheCluster(r.Context(), targetID)
	}
	if body.Status != nil {
		if err := s.authEngine.Store().UpdateUserStatus(r.Context(), targetID, *body.Status); err != nil {
			writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: err.Error(), Code: errs.CodeInternal})
			return
		}
		s.authEngine.InvalidateUserCacheCluster(r.Context(), targetID)
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleDeleteUser DELETE /api/v1/admin/users/{id}
func (s *Server) handleDeleteUser(w http.ResponseWriter, r *http.Request) {
	targetID := r.PathValue("id")
	currentUser := auth.UserFrom(r.Context())
	if currentUser != nil && currentUser.ID == targetID {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "不能删除当前登录账号", Code: errs.CodeInvalidInput})
		return
	}
	target, err := s.authEngine.Store().GetUserByID(r.Context(), targetID)
	if err != nil || target == nil {
		writeJSON(w, http.StatusNotFound, ErrorResponse{Error: "用户不存在", Code: errs.CodeNotFound})
		return
	}
	if target.Role == "admin" && target.Status == "active" {
		n, err := s.authEngine.Store().CountActiveAdmins(r.Context())
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: err.Error(), Code: errs.CodeInternal})
			return
		}
		if n <= 1 {
			writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "不能删除最后一个可用管理员", Code: errs.CodeInvalidInput})
			return
		}
	}
	if err := s.authEngine.Store().DeleteUser(r.Context(), targetID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, ErrorResponse{Error: "用户不存在", Code: errs.CodeNotFound})
			return
		}
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: err.Error(), Code: errs.CodeInternal})
		return
	}
	s.authEngine.InvalidateUserCacheCluster(r.Context(), targetID)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleUpdateQuota PATCH /api/v1/admin/users/{id}/quota
func (s *Server) handleUpdateQuota(w http.ResponseWriter, r *http.Request) {
	var body struct {
		TokenQuota int64 `json:"token_quota"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "无效的请求体", Code: errs.CodeBadRequest})
		return
	}
	if body.TokenQuota < 0 {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "token_quota 不能为负数", Code: errs.CodeInvalidInput})
		return
	}

	userID := r.PathValue("id")
	user, err := s.authEngine.Store().GetUserByID(r.Context(), userID)
	if err != nil || user == nil {
		writeJSON(w, http.StatusNotFound, ErrorResponse{Error: "用户不存在", Code: errs.CodeNotFound})
		return
	}

	if err := s.authEngine.Store().UpsertUserQuota(r.Context(), userID, body.TokenQuota); err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: err.Error(), Code: errs.CodeInternal})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleUserLogins GET /api/v1/admin/users/{id}/logins
func (s *Server) handleUserLogins(w http.ResponseWriter, r *http.Request) {
	userID := r.PathValue("id")
	limit := 50
	if l, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && l > 0 && l <= 200 {
		limit = l
	}
	records, err := s.authEngine.Store().GetLoginHistory(r.Context(), userID, limit)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: err.Error(), Code: errs.CodeInternal})
		return
	}
	if records == nil {
		records = []*auth.LoginRecord{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"logins": records})
}

// ── 用量统计 ──────────────────────────────────────────────────────────────────

// handleUsageSummary GET /api/v1/admin/usage/summary
func (s *Server) handleUsageSummary(w http.ResponseWriter, r *http.Request) {
	if s.costTracker == nil {
		writeJSON(w, http.StatusServiceUnavailable, ErrorResponse{Error: "成本追踪未启用", Code: errs.CodeInternal})
		return
	}
	summary, err := s.costTracker.GetTotalCost(r.Context(), accounting.CostFilter{})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: err.Error(), Code: errs.CodeInternal})
		return
	}
	writeJSON(w, http.StatusOK, summary)
}

// handleUsageByUser GET /api/v1/admin/usage/by-user
func (s *Server) handleUsageByUser(w http.ResponseWriter, r *http.Request) {
	if s.costTracker == nil {
		writeJSON(w, http.StatusServiceUnavailable, ErrorResponse{Error: "成本追踪未启用", Code: errs.CodeInternal})
		return
	}
	byUser, err := s.costTracker.GetCostByUser(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: err.Error(), Code: errs.CodeInternal})
		return
	}
	if byUser == nil {
		byUser = []accounting.UserCost{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"by_user": byUser})
}

// handleUsageByModel GET /api/v1/admin/usage/by-model
func (s *Server) handleUsageByModel(w http.ResponseWriter, r *http.Request) {
	if s.costTracker == nil {
		writeJSON(w, http.StatusServiceUnavailable, ErrorResponse{Error: "成本追踪未启用", Code: errs.CodeInternal})
		return
	}
	summary, err := s.costTracker.GetTotalCost(r.Context(), accounting.CostFilter{})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: err.Error(), Code: errs.CodeInternal})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"by_model": summary.ByModel})
}

// handleUsageQuality GET /api/v1/admin/usage/quality
func (s *Server) handleUsageQuality(w http.ResponseWriter, r *http.Request) {
	if s.costTracker == nil {
		writeJSON(w, http.StatusServiceUnavailable, ErrorResponse{Error: "成本追踪未启用", Code: errs.CodeInternal})
		return
	}
	summary, err := s.costTracker.GetQualityCost(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: err.Error(), Code: errs.CodeInternal})
		return
	}
	writeJSON(w, http.StatusOK, summary)
}

// ── Provider 管理 ─────────────────────────────────────────────────────────────

// handleAdminListProviders GET /api/v1/admin/auth/providers
func (s *Server) handleAdminListProviders(w http.ResponseWriter, r *http.Request) {
	providers, err := s.authEngine.Store().ListAllProviders(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: err.Error(), Code: errs.CodeInternal})
		return
	}

	type providerItem struct {
		Name         string         `json:"name"`
		ProviderType string         `json:"provider_type"`
		Enabled      bool           `json:"enabled"`
		ConfigJSON   map[string]any `json:"config_json"`
	}

	items := make([]providerItem, 0, len(providers))
	for _, p := range providers {
		var cfg map[string]any
		_ = json.Unmarshal(p.ConfigJSON, &cfg)
		items = append(items, providerItem{
			Name:         p.Name,
			ProviderType: p.ProviderType,
			Enabled:      p.Enabled,
			ConfigJSON:   maskSecrets(cfg),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"providers": items})
}

// handleCreateProvider POST /api/v1/admin/auth/providers
func (s *Server) handleCreateProvider(w http.ResponseWriter, r *http.Request) {
	var body auth.ProviderConfig
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "无效的请求体", Code: errs.CodeBadRequest})
		return
	}
	if body.Name == "" || body.ProviderType == "" {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "name 和 provider_type 不能为空", Code: errs.CodeBadRequest})
		return
	}
	if len(body.ConfigJSON) > 0 {
		if !json.Valid(body.ConfigJSON) {
			writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "config_json 不是合法的 JSON", Code: errs.CodeBadRequest})
			return
		}
	}
	if err := s.authEngine.Store().CreateProvider(r.Context(), body); err != nil {
		if strings.Contains(err.Error(), "23505") {
			writeJSON(w, http.StatusConflict, ErrorResponse{Error: "Provider 名称已存在", Code: errs.CodeInvalidInput})
			return
		}
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: err.Error(), Code: errs.CodeInternal})
		return
	}
	if err := s.authEngine.LoadProvidersFromDB(r.Context()); err != nil {
		s.logger.Warn("Provider 热加载失败", zap.Error(err))
	}
	writeJSON(w, http.StatusCreated, map[string]string{"status": "ok"})
}

// handleUpdateProvider PATCH /api/v1/admin/auth/providers/{name}
// 使用 ProviderUpdate（指针字段）解码，避免 bool 默认值覆盖问题。
// JSON 中省略 enabled 则保持不变，设为 false 则禁用，设为 true 则启用。
func (s *Server) handleUpdateProvider(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	var update auth.ProviderUpdate
	if err := json.NewDecoder(r.Body).Decode(&update); err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "无效的请求体", Code: errs.CodeBadRequest})
		return
	}
	if len(update.ConfigJSON) > 0 && string(update.ConfigJSON) != "{}" && string(update.ConfigJSON) != "null" {
		merged, err := mergeAuthProviderConfigJSON(r.Context(), s.authEngine.Store(), name, update.ConfigJSON)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: err.Error(), Code: errs.CodeBadRequest})
			return
		}
		update.ConfigJSON = merged
	}
	if err := s.authEngine.Store().UpdateProviderFields(r.Context(), name, update); err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: err.Error(), Code: errs.CodeInternal})
		return
	}
	if err := s.authEngine.LoadProvidersFromDB(r.Context()); err != nil {
		s.logger.Warn("Provider 热加载失败", zap.Error(err))
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func mergeAuthProviderConfigJSON(ctx context.Context, st auth.Store, name string, incoming json.RawMessage) (json.RawMessage, error) {
	if !json.Valid(incoming) {
		return nil, errors.New("config_json 不是合法的 JSON")
	}

	var incomingValue any
	if err := json.Unmarshal(incoming, &incomingValue); err != nil {
		return nil, err
	}

	existingRaw := json.RawMessage(`{}`)
	if st != nil {
		providers, err := st.ListAllProviders(ctx)
		if err != nil {
			return nil, err
		}
		for _, p := range providers {
			if p.Name == name {
				existingRaw = p.ConfigJSON
				break
			}
		}
	}

	var existingValue any
	if len(existingRaw) > 0 && json.Valid(existingRaw) {
		_ = json.Unmarshal(existingRaw, &existingValue)
	}

	merged := security.PreserveRedactedValues(incomingValue, existingValue)
	out, err := json.Marshal(merged)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(out), nil
}

// handleDeleteProvider DELETE /api/v1/admin/auth/providers/{name}
func (s *Server) handleDeleteProvider(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")

	userCount, err := s.authEngine.Store().CountUsersByProvider(r.Context(), name)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: err.Error(), Code: errs.CodeInternal})
		return
	}
	if userCount > 0 {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{
			Error: fmt.Sprintf("该 Provider 下还有 %d 个用户，请先迁移或删除这些用户", userCount),
			Code:  errs.CodeInvalidInput,
		})
		return
	}

	if err := s.authEngine.Store().DeleteProvider(r.Context(), name); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, ErrorResponse{Error: "Provider 不存在", Code: errs.CodeNotFound})
			return
		}
		if strings.Contains(err.Error(), "不能删除") {
			writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: err.Error(), Code: errs.CodeInvalidInput})
			return
		}
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: err.Error(), Code: errs.CodeInternal})
		return
	}
	if err := s.authEngine.LoadProvidersFromDB(r.Context()); err != nil {
		s.logger.Warn("Provider 热加载失败", zap.Error(err))
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
