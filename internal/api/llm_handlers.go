package api

import (
	"encoding/json"
	"net/http"
	"strings"

	"go.uber.org/zap"

	"github.com/chef-guo/agents-hive/internal/errs"
	"github.com/chef-guo/agents-hive/internal/store"
)

// ── LLM Provider 管理 ─────────────────────────────────────────────────────────

// requireStore 检查 s.store 是否初始化，未初始化时写入 503 并返回 false。
// 消除 8 处重复的 s.store == nil 检查。
func (s *Server) requireStore(w http.ResponseWriter) bool {
	if s.store == nil {
		writeJSON(w, http.StatusServiceUnavailable, ErrorResponse{Error: "存储未初始化", Code: errs.CodeInternal})
		return false
	}
	return true
}

// handleAdminListLLMProviders GET /api/v1/admin/llm/providers
func (s *Server) handleAdminListLLMProviders(w http.ResponseWriter, r *http.Request) {
	if !s.requireStore(w) {
		return
	}
	providers, err := s.store.ListLLMProviders(r.Context())
	if err != nil {
		s.logger.Error("查询 LLM Provider 列表失败", zap.Error(err))
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "查询失败", Code: errs.CodeStoreReadFailed})
		return
	}
	// 脱敏 api_key
	type providerItem struct {
		Name         string `json:"name"`
		ProviderType string `json:"provider_type"`
		BaseURL      string `json:"base_url"`
		APIKey       string `json:"api_key"` // masked
		IsDefault    bool   `json:"is_default"`
		Enabled      bool   `json:"enabled"`
		APIFormat    string `json:"api_format"`
		ServiceType  string `json:"service_type"`
		ConfigJSON   string `json:"config_json"`
		CreatedAt    string `json:"created_at"`
		UpdatedAt    string `json:"updated_at"`
	}
	items := make([]providerItem, 0, len(providers))
	for _, p := range providers {
		items = append(items, providerItem{
			Name:         p.Name,
			ProviderType: p.ProviderType,
			BaseURL:      p.BaseURL,
			APIKey:       maskAPIKey(p.APIKey),
			IsDefault:    p.IsDefault,
			Enabled:      p.Enabled,
			APIFormat:    p.APIFormat,
			ServiceType:  p.ServiceType,
			ConfigJSON:   p.ConfigJSON,
			CreatedAt:    p.CreatedAt,
			UpdatedAt:    p.UpdatedAt,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"providers": items})
}

// handleAdminCreateLLMProvider POST /api/v1/admin/llm/providers
func (s *Server) handleAdminCreateLLMProvider(w http.ResponseWriter, r *http.Request) {
	if !s.requireStore(w) {
		return
	}
	var req struct {
		Name         string `json:"name"`
		ProviderType string `json:"provider_type"`
		APIKey       string `json:"api_key"`
		BaseURL      string `json:"base_url"`
		IsDefault    bool   `json:"is_default"`
		Enabled      bool   `json:"enabled"`
		APIFormat    string `json:"api_format"`
		ServiceType  string `json:"service_type"`
		ConfigJSON   string `json:"config_json"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "请求体解析失败", Code: errs.CodeBadRequest})
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	req.ProviderType = strings.TrimSpace(req.ProviderType)
	if req.Name == "" || req.ProviderType == "" {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "name 和 provider_type 不能为空", Code: errs.CodeBadRequest})
		return
	}
	if req.ConfigJSON == "" {
		req.ConfigJSON = "{}"
	}
	serviceType := req.ServiceType
	if serviceType == "" {
		serviceType = "llm"
	}

	ctx := r.Context()

	// 检查名称是否已存在，避免静默覆盖
	if _, err := s.store.GetLLMProvider(ctx, req.Name); err == nil {
		writeJSON(w, http.StatusConflict, ErrorResponse{Error: "Provider 已存在: " + req.Name, Code: errs.CodeInvalidInput})
		return
	}

	// 原子化设置默认（事务保证唯一性）
	if req.IsDefault {
		if err := s.store.SetDefaultLLMProvider(ctx, req.Name); err != nil {
			s.logger.Error("原子化设置默认 LLM Provider 失败", zap.Error(err))
			writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "设置默认 Provider 失败", Code: errs.CodeStoreWriteFailed})
			return
		}
	}

	rec := &store.LLMProviderRecord{
		Name:         req.Name,
		ProviderType: req.ProviderType,
		APIKey:       req.APIKey,
		BaseURL:      req.BaseURL,
		IsDefault:    req.IsDefault,
		Enabled:      req.Enabled,
		APIFormat:    req.APIFormat,
		ServiceType:  serviceType,
		ConfigJSON:   req.ConfigJSON,
	}
	if err := s.store.SaveLLMProvider(ctx, rec); err != nil {
		s.logger.Error("保存 LLM Provider 失败", zap.Error(err))
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "保存失败: " + err.Error(), Code: errs.CodeStoreWriteFailed})
		return
	}
	s.logger.Info("LLM Provider 已创建", zap.String("name", req.Name))
	s.reloadAIRouter(r)
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "name": req.Name})
}

// handleAdminUpdateLLMProvider PATCH /api/v1/admin/llm/providers/{name}
func (s *Server) handleAdminUpdateLLMProvider(w http.ResponseWriter, r *http.Request) {
	if !s.requireStore(w) {
		return
	}
	name := r.PathValue("name")
	ctx := r.Context()

	existing, err := s.store.GetLLMProvider(ctx, name)
	if err != nil {
		if errs.IsCode(err, errs.CodeNotFound) {
			writeJSON(w, http.StatusNotFound, ErrorResponse{Error: "Provider 不存在: " + name, Code: errs.CodeNotFound})
		} else {
			s.logger.Error("读取 LLM Provider 失败", zap.String("name", name), zap.Error(err))
			writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "读取失败", Code: errs.CodeStoreReadFailed})
		}
		return
	}

	var req struct {
		ProviderType *string `json:"provider_type"`
		APIKey       *string `json:"api_key"`
		BaseURL      *string `json:"base_url"`
		IsDefault    *bool   `json:"is_default"`
		Enabled      *bool   `json:"enabled"`
		APIFormat    *string `json:"api_format"`
		ServiceType  *string `json:"service_type"`
		ConfigJSON   *string `json:"config_json"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "请求体解析失败", Code: errs.CodeBadRequest})
		return
	}

	if req.ProviderType != nil {
		existing.ProviderType = *req.ProviderType
	}
	if req.APIKey != nil && *req.APIKey != "" && *req.APIKey != "****" {
		existing.APIKey = *req.APIKey
	}
	if req.BaseURL != nil {
		existing.BaseURL = *req.BaseURL
	}
	if req.IsDefault != nil {
		if *req.IsDefault {
			if err := s.store.SetDefaultLLMProvider(ctx, name); err != nil {
				s.logger.Error("原子化设置默认 LLM Provider 失败", zap.Error(err))
				writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "设置默认 Provider 失败", Code: errs.CodeStoreWriteFailed})
				return
			}
		}
		existing.IsDefault = *req.IsDefault
	}
	if req.Enabled != nil {
		existing.Enabled = *req.Enabled
	}
	if req.APIFormat != nil {
		existing.APIFormat = *req.APIFormat
	}
	if req.ServiceType != nil && *req.ServiceType != "" {
		existing.ServiceType = *req.ServiceType
	}
	if req.ConfigJSON != nil {
		existing.ConfigJSON = *req.ConfigJSON
	}

	if err := s.store.SaveLLMProvider(ctx, existing); err != nil {
		s.logger.Error("更新 LLM Provider 失败", zap.Error(err))
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "更新失败: " + err.Error(), Code: errs.CodeStoreWriteFailed})
		return
	}
	s.logger.Info("LLM Provider 已更新", zap.String("name", name))
	s.reloadAIRouter(r)
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "name": name})
}

// handleAdminDeleteLLMProvider DELETE /api/v1/admin/llm/providers/{name}
// 级联删除该 Provider 下的所有 Models（在 store 层事务内完成）。
func (s *Server) handleAdminDeleteLLMProvider(w http.ResponseWriter, r *http.Request) {
	if !s.requireStore(w) {
		return
	}
	name := r.PathValue("name")
	ctx := r.Context()

	if err := s.store.DeleteLLMProvider(ctx, name); err != nil {
		if errs.IsCode(err, errs.CodeNotFound) {
			writeJSON(w, http.StatusNotFound, ErrorResponse{Error: "Provider 不存在: " + name, Code: errs.CodeNotFound})
		} else {
			s.logger.Error("删除 LLM Provider 失败", zap.String("name", name), zap.Error(err))
			writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "删除失败: " + err.Error(), Code: errs.CodeStoreWriteFailed})
		}
		return
	}
	s.logger.Info("LLM Provider 已删除（关联 Models 已级联删除）", zap.String("name", name))
	s.reloadAIRouter(r)
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "name": name})
}

// ── LLM Model 管理 ────────────────────────────────────────────────────────────

// handleAdminListLLMModels GET /api/v1/admin/llm/models
// 注意：api_key 暂未脱敏（admin-only 接口，用户知悉）。如需脱敏请参照 handleAdminListLLMProviders。
func (s *Server) handleAdminListLLMModels(w http.ResponseWriter, r *http.Request) {
	if !s.requireStore(w) {
		return
	}
	models, err := s.store.ListLLMModels(r.Context())
	if err != nil {
		s.logger.Error("查询 LLM Model 列表失败", zap.Error(err))
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "查询失败", Code: errs.CodeStoreReadFailed})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"models": models})
}

// handleAdminCreateLLMModel POST /api/v1/admin/llm/models
func (s *Server) handleAdminCreateLLMModel(w http.ResponseWriter, r *http.Request) {
	if !s.requireStore(w) {
		return
	}
	var req struct {
		Name         string `json:"name"`
		ProviderName string `json:"provider_name"`
		Model        string `json:"model"`
		BaseURL      string `json:"base_url"`
		APIKey       string `json:"api_key"`
		IsDefault    bool   `json:"is_default"`
		Enabled      bool   `json:"enabled"`
		ConfigJSON   string `json:"config_json"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "请求体解析失败", Code: errs.CodeBadRequest})
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	req.Model = strings.TrimSpace(req.Model)
	if req.Name == "" || req.Model == "" {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "name 和 model 不能为空", Code: errs.CodeBadRequest})
		return
	}
	if req.ConfigJSON == "" {
		req.ConfigJSON = "{}"
	}

	ctx := r.Context()

	// 检查名称是否已存在
	if _, err := s.store.GetLLMModel(ctx, req.Name); err == nil {
		writeJSON(w, http.StatusConflict, ErrorResponse{Error: "Model 已存在: " + req.Name, Code: errs.CodeInvalidInput})
		return
	}

	// 原子化设置默认
	if req.IsDefault {
		if err := s.store.SetDefaultLLMModel(ctx, req.Name); err != nil {
			s.logger.Error("原子化设置默认 LLM Model 失败", zap.Error(err))
			writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "设置默认 Model 失败", Code: errs.CodeStoreWriteFailed})
			return
		}
	}

	rec := &store.LLMModelRecord{
		Name:         req.Name,
		ProviderName: req.ProviderName,
		Model:        req.Model,
		BaseURL:      req.BaseURL,
		APIKey:       req.APIKey,
		IsDefault:    req.IsDefault,
		Enabled:      req.Enabled,
		ConfigJSON:   req.ConfigJSON,
	}
	if err := s.store.SaveLLMModel(ctx, rec); err != nil {
		s.logger.Error("保存 LLM Model 失败", zap.Error(err))
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "保存失败: " + err.Error(), Code: errs.CodeStoreWriteFailed})
		return
	}
	s.logger.Info("LLM Model 已创建", zap.String("name", req.Name))
	s.reloadAIRouter(r)
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "name": req.Name})
}

// handleAdminUpdateLLMModel PATCH /api/v1/admin/llm/models/{name}
func (s *Server) handleAdminUpdateLLMModel(w http.ResponseWriter, r *http.Request) {
	if !s.requireStore(w) {
		return
	}
	name := r.PathValue("name")
	ctx := r.Context()

	existing, err := s.store.GetLLMModel(ctx, name)
	if err != nil {
		if errs.IsCode(err, errs.CodeNotFound) {
			writeJSON(w, http.StatusNotFound, ErrorResponse{Error: "Model 不存在: " + name, Code: errs.CodeNotFound})
		} else {
			s.logger.Error("读取 LLM Model 失败", zap.String("name", name), zap.Error(err))
			writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "读取失败", Code: errs.CodeStoreReadFailed})
		}
		return
	}

	var req struct {
		Name         *string `json:"name"`
		ProviderName *string `json:"provider_name"`
		Model        *string `json:"model"`
		BaseURL      *string `json:"base_url"`
		APIKey       *string `json:"api_key"`
		IsDefault    *bool   `json:"is_default"`
		Enabled      *bool   `json:"enabled"`
		ConfigJSON   *string `json:"config_json"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "请求体解析失败", Code: errs.CodeBadRequest})
		return
	}

	if req.Name != nil {
		newName := strings.TrimSpace(*req.Name)
		if newName == "" {
			writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "name 不能为空", Code: errs.CodeBadRequest})
			return
		}
		if newName != name {
			if _, err := s.store.GetLLMModel(ctx, newName); err == nil {
				writeJSON(w, http.StatusConflict, ErrorResponse{Error: "Model 已存在: " + newName, Code: errs.CodeInvalidInput})
				return
			} else if !errs.IsCode(err, errs.CodeNotFound) {
				s.logger.Error("检查 LLM Model 重名失败", zap.String("name", newName), zap.Error(err))
				writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "检查名称失败", Code: errs.CodeStoreReadFailed})
				return
			}
			existing.Name = newName
		}
	}
	if req.ProviderName != nil {
		existing.ProviderName = *req.ProviderName
	}
	if req.Model != nil && *req.Model != "" {
		existing.Model = strings.TrimSpace(*req.Model)
		if existing.Model == "" {
			writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "model 不能为空", Code: errs.CodeBadRequest})
			return
		}
	}
	if req.BaseURL != nil {
		existing.BaseURL = *req.BaseURL
	}
	if req.APIKey != nil && *req.APIKey != "" && *req.APIKey != "****" {
		existing.APIKey = *req.APIKey
	}
	if req.IsDefault != nil {
		existing.IsDefault = *req.IsDefault
	}
	if req.Enabled != nil {
		existing.Enabled = *req.Enabled
	}
	if req.ConfigJSON != nil {
		existing.ConfigJSON = *req.ConfigJSON
	}

	if err := s.store.UpdateLLMModel(ctx, name, existing); err != nil {
		s.logger.Error("更新 LLM Model 失败", zap.Error(err))
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "更新失败: " + err.Error(), Code: errs.CodeStoreWriteFailed})
		return
	}
	s.logger.Info("LLM Model 已更新", zap.String("old_name", name), zap.String("name", existing.Name))
	s.reloadAIRouter(r)
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "name": existing.Name})
}

// handleAdminDeleteLLMModel DELETE /api/v1/admin/llm/models/{name}
func (s *Server) handleAdminDeleteLLMModel(w http.ResponseWriter, r *http.Request) {
	if !s.requireStore(w) {
		return
	}
	name := r.PathValue("name")
	ctx := r.Context()

	if err := s.store.DeleteLLMModel(ctx, name); err != nil {
		if errs.IsCode(err, errs.CodeNotFound) {
			writeJSON(w, http.StatusNotFound, ErrorResponse{Error: "Model 不存在: " + name, Code: errs.CodeNotFound})
		} else {
			s.logger.Error("删除 LLM Model 失败", zap.String("name", name), zap.Error(err))
			writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "删除失败: " + err.Error(), Code: errs.CodeStoreWriteFailed})
		}
		return
	}
	s.logger.Info("LLM Model 已删除", zap.String("name", name))
	s.reloadAIRouter(r)
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "name": name})
}

// reloadAIRouter 在 LLM Provider/Model CRUD 成功后触发 AIRouter 热重载。
// Reload 失败时仅 Warn，不回滚已完成的 DB 写入——存储层成功即视为操作成功。
func (s *Server) reloadAIRouter(r *http.Request) {
	if s.aiRouter == nil {
		return
	}
	if err := s.aiRouter.Reload(r.Context()); err != nil {
		s.logger.Warn("AIRouter 热重载失败（DB 已更新，下次启动或刷新后生效）", zap.Error(err))
	}
}

// maskAPIKey 脱敏 API Key：保留首4末4，中间替换为 ****
func maskAPIKey(key string) string {
	if len(key) >= 8 {
		return key[:4] + "****" + key[len(key)-4:]
	}
	if key != "" {
		return "****"
	}
	return ""
}
