package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"go.uber.org/zap"

	"github.com/chef-guo/agents-hive/internal/store"
)

// newTestServerForLLM 创建带 MemoryStore 的测试服务器，专门用于 LLM handler 测试。
// 不需要 master、authEngine 等组件。
func newTestServerForLLM(t *testing.T) (http.Handler, *store.MemoryStore) {
	t.Helper()
	logger, _ := zap.NewDevelopment()
	st := store.NewMemoryStore()

	srv := &Server{
		store:  st,
		logger: logger,
	}

	mux := http.NewServeMux()
	// 直接注册 LLM 路由（绕过 authEngine guard，专注 handler 逻辑）
	mux.HandleFunc("GET /api/v1/admin/llm/providers", srv.handleAdminListLLMProviders)
	mux.HandleFunc("POST /api/v1/admin/llm/providers", srv.handleAdminCreateLLMProvider)
	mux.HandleFunc("PATCH /api/v1/admin/llm/providers/{name}", srv.handleAdminUpdateLLMProvider)
	mux.HandleFunc("DELETE /api/v1/admin/llm/providers/{name}", srv.handleAdminDeleteLLMProvider)
	mux.HandleFunc("GET /api/v1/admin/llm/models", srv.handleAdminListLLMModels)
	mux.HandleFunc("POST /api/v1/admin/llm/models", srv.handleAdminCreateLLMModel)
	mux.HandleFunc("PATCH /api/v1/admin/llm/models/{name}", srv.handleAdminUpdateLLMModel)
	mux.HandleFunc("DELETE /api/v1/admin/llm/models/{name}", srv.handleAdminDeleteLLMModel)

	return mux, st
}

func doJSON(t *testing.T, handler http.Handler, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var reqBody *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		reqBody = bytes.NewReader(b)
	} else {
		reqBody = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, reqBody)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

// ── Test 1: List providers — api_key 脱敏验证 ──────────────────────────────

func TestLLM_ListProviders_MaskAPIKey(t *testing.T) {
	handler, st := newTestServerForLLM(t)
	ctx := t.Context()

	// 写入一个有长 api_key 的 provider
	_ = st.SaveLLMProvider(ctx, &store.LLMProviderRecord{
		Name: "openai-1", ProviderType: "openai", APIKey: "sk-1234567890abcdef",
		Enabled: true, APIFormat: "chat", ServiceType: "llm", ConfigJSON: "{}",
	})

	rec := doJSON(t, handler, "GET", "/api/v1/admin/llm/providers", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("期望 200，得到 %d: %s", rec.Code, rec.Body.String())
	}

	var resp struct {
		Providers []map[string]any `json:"providers"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if len(resp.Providers) != 1 {
		t.Fatalf("期望 1 个 provider，得到 %d", len(resp.Providers))
	}
	key := resp.Providers[0]["api_key"].(string)
	// 应该以 **** 包含脱敏
	if key == "sk-1234567890abcdef" {
		t.Errorf("api_key 未脱敏，原始值暴露: %s", key)
	}
	if len(key) < 8 || key[4:8] != "****" {
		t.Errorf("脱敏格式不符合预期（首4末4****），得到: %s", key)
	}
}

// ── Test 2: Create 重复名返回 409 ──────────────────────────────────────────

func TestLLM_CreateProvider_DuplicateReturns409(t *testing.T) {
	handler, _ := newTestServerForLLM(t)

	body := map[string]any{"name": "dup", "provider_type": "openai"}
	rec := doJSON(t, handler, "POST", "/api/v1/admin/llm/providers", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("第一次创建应成功，得到 %d", rec.Code)
	}

	rec2 := doJSON(t, handler, "POST", "/api/v1/admin/llm/providers", body)
	if rec2.Code != http.StatusConflict {
		t.Errorf("重复创建应返回 409，得到 %d: %s", rec2.Code, rec2.Body.String())
	}
}

// ── Test 3: Update api_key="****" 不覆盖现有 key ────────────────────────────

func TestLLM_UpdateProvider_MaskedKeyNotOverwritten(t *testing.T) {
	handler, st := newTestServerForLLM(t)
	ctx := t.Context()

	_ = st.SaveLLMProvider(ctx, &store.LLMProviderRecord{
		Name: "p1", ProviderType: "openai", APIKey: "original-secret-key",
		Enabled: true, APIFormat: "chat", ServiceType: "llm", ConfigJSON: "{}",
	})

	// 发送 api_key="****"（前端在 edit 模式下留空/掩码值）
	body := map[string]any{"api_key": "****", "enabled": false}
	rec := doJSON(t, handler, "PATCH", "/api/v1/admin/llm/providers/p1", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("更新应成功，得到 %d: %s", rec.Code, rec.Body.String())
	}

	// 验证原始 key 未被覆盖
	p, _ := st.GetLLMProvider(ctx, "p1")
	if p.APIKey != "original-secret-key" {
		t.Errorf("api_key 被 **** 覆盖，期望保留原值，得到: %s", p.APIKey)
	}
}

// ── Test 4: Update is_default 清除其他 provider 的默认标记 ─────────────────

func TestLLM_UpdateProvider_SetDefaultClearsOthers(t *testing.T) {
	handler, st := newTestServerForLLM(t)
	ctx := t.Context()

	_ = st.SaveLLMProvider(ctx, &store.LLMProviderRecord{
		Name: "p1", ProviderType: "openai", IsDefault: true,
		Enabled: true, APIFormat: "chat", ServiceType: "llm", ConfigJSON: "{}",
	})
	_ = st.SaveLLMProvider(ctx, &store.LLMProviderRecord{
		Name: "p2", ProviderType: "anthropic", IsDefault: false,
		Enabled: true, APIFormat: "chat", ServiceType: "llm", ConfigJSON: "{}",
	})

	// 将 p2 设为 default
	isDefault := true
	body := map[string]any{"is_default": isDefault}
	rec := doJSON(t, handler, "PATCH", "/api/v1/admin/llm/providers/p2", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("更新应成功，得到 %d: %s", rec.Code, rec.Body.String())
	}

	// p1 应不再是 default
	p1, _ := st.GetLLMProvider(ctx, "p1")
	if p1.IsDefault {
		t.Errorf("p1 仍然是默认，期望被清除")
	}
}

// ── Test 5: Delete provider 级联删除关联 models ─────────────────────────────

func TestLLM_DeleteProvider_CascadesModels(t *testing.T) {
	handler, st := newTestServerForLLM(t)
	ctx := t.Context()

	_ = st.SaveLLMProvider(ctx, &store.LLMProviderRecord{
		Name: "openai", ProviderType: "openai",
		Enabled: true, APIFormat: "chat", ServiceType: "llm", ConfigJSON: "{}",
	})
	_ = st.SaveLLMModel(ctx, &store.LLMModelRecord{
		Name: "gpt4", ProviderName: "openai", Model: "gpt-4o", ConfigJSON: "{}",
	})

	rec := doJSON(t, handler, "DELETE", "/api/v1/admin/llm/providers/openai", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("删除应成功，得到 %d: %s", rec.Code, rec.Body.String())
	}

	// 关联 model 应被级联删除
	models, _ := st.ListLLMModels(ctx)
	for _, m := range models {
		if m.Name == "gpt4" {
			t.Errorf("级联删除失败，gpt4 model 仍然存在")
		}
	}
}

// ── Test 6: Update 记录不存在返回 404（非 500）─────────────────────────────

func TestLLM_UpdateProvider_NotFoundReturns404(t *testing.T) {
	handler, _ := newTestServerForLLM(t)

	body := map[string]any{"enabled": true}
	rec := doJSON(t, handler, "PATCH", "/api/v1/admin/llm/providers/nonexistent", body)
	if rec.Code != http.StatusNotFound {
		t.Errorf("期望 404，得到 %d: %s", rec.Code, rec.Body.String())
	}
}

// ── Test 7: Create model 重复名返回 409 ────────────────────────────────────

func TestLLM_CreateModel_DuplicateReturns409(t *testing.T) {
	handler, _ := newTestServerForLLM(t)

	body := map[string]any{"name": "m1", "model": "gpt-4o"}
	rec := doJSON(t, handler, "POST", "/api/v1/admin/llm/models", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("第一次创建应成功，得到 %d", rec.Code)
	}

	rec2 := doJSON(t, handler, "POST", "/api/v1/admin/llm/models", body)
	if rec2.Code != http.StatusConflict {
		t.Errorf("重复创建应返回 409，得到 %d: %s", rec2.Code, rec2.Body.String())
	}
}

// ── Test 8: Update model api_key="****" 不覆盖现有 key ──────────────────────

func TestLLM_UpdateModel_MaskedKeyNotOverwritten(t *testing.T) {
	handler, st := newTestServerForLLM(t)
	ctx := t.Context()

	_ = st.SaveLLMModel(ctx, &store.LLMModelRecord{
		Name: "m1", ProviderName: "p1", Model: "gpt-4o",
		APIKey: "model-secret", ConfigJSON: "{}",
	})

	body := map[string]any{"api_key": "****"}
	rec := doJSON(t, handler, "PATCH", "/api/v1/admin/llm/models/m1", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("更新应成功，得到 %d: %s", rec.Code, rec.Body.String())
	}

	m, _ := st.GetLLMModel(ctx, "m1")
	if m.APIKey != "model-secret" {
		t.Errorf("api_key 被 **** 覆盖，期望保留原值，得到: %s", m.APIKey)
	}
}

func TestLLM_UpdateModel_RenamesModel(t *testing.T) {
	handler, st := newTestServerForLLM(t)
	ctx := t.Context()

	_ = st.SaveLLMModel(ctx, &store.LLMModelRecord{
		Name: "gpt-5.2", ProviderName: "openai", Model: "gpt-5.4",
		APIKey: "model-secret", ConfigJSON: "{}",
	})

	body := map[string]any{"name": "gpt-5.4", "model": "gpt-5.4"}
	rec := doJSON(t, handler, "PATCH", "/api/v1/admin/llm/models/gpt-5.2", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("更新应成功，得到 %d: %s", rec.Code, rec.Body.String())
	}

	if _, err := st.GetLLMModel(ctx, "gpt-5.2"); err == nil {
		t.Fatal("旧模型名称仍可读取，期望已被 rename")
	}
	m, err := st.GetLLMModel(ctx, "gpt-5.4")
	if err != nil {
		t.Fatalf("新模型名称读取失败: %v", err)
	}
	if m.Model != "gpt-5.4" {
		t.Fatalf("Model = %q, want gpt-5.4", m.Model)
	}
	models, err := st.ListLLMModels(ctx)
	if err != nil {
		t.Fatalf("ListLLMModels failed: %v", err)
	}
	if len(models) != 1 {
		t.Fatalf("models count = %d, want 1", len(models))
	}
}

func TestLLM_UpdateModel_RenameAndSetDefaultClearsOthers(t *testing.T) {
	handler, st := newTestServerForLLM(t)
	ctx := t.Context()

	_ = st.SaveLLMModel(ctx, &store.LLMModelRecord{
		Name: "old-default", ProviderName: "openai", Model: "gpt-4o",
		IsDefault: true, ConfigJSON: "{}",
	})
	_ = st.SaveLLMModel(ctx, &store.LLMModelRecord{
		Name: "gpt-5.2", ProviderName: "openai", Model: "gpt-5.4",
		IsDefault: false, ConfigJSON: "{}",
	})

	body := map[string]any{"name": "gpt-5.4", "model": "gpt-5.4", "is_default": true}
	rec := doJSON(t, handler, "PATCH", "/api/v1/admin/llm/models/gpt-5.2", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("更新应成功，得到 %d: %s", rec.Code, rec.Body.String())
	}

	oldDefault, err := st.GetLLMModel(ctx, "old-default")
	if err != nil {
		t.Fatalf("读取旧默认模型失败: %v", err)
	}
	if oldDefault.IsDefault {
		t.Fatal("旧默认模型仍为默认，期望被清除")
	}
	newDefault, err := st.GetLLMModel(ctx, "gpt-5.4")
	if err != nil {
		t.Fatalf("读取新默认模型失败: %v", err)
	}
	if !newDefault.IsDefault {
		t.Fatal("重命名后的模型未设为默认")
	}
}
