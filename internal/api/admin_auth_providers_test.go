package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/chef-guo/agents-hive/internal/auth"
	"github.com/chef-guo/agents-hive/internal/security"
)

type adminAuthProviderStore struct {
	mu        sync.Mutex
	providers map[string]auth.ProviderConfig
}

func newAdminAuthProviderStore() *adminAuthProviderStore {
	return &adminAuthProviderStore{providers: make(map[string]auth.ProviderConfig)}
}

func (s *adminAuthProviderStore) ListEnabledProviders(_ context.Context) ([]auth.ProviderConfig, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]auth.ProviderConfig, 0, len(s.providers))
	for _, p := range s.providers {
		if p.Enabled {
			out = append(out, cloneProviderConfig(p))
		}
	}
	return out, nil
}

func (s *adminAuthProviderStore) ListAllProviders(_ context.Context) ([]auth.ProviderConfig, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]auth.ProviderConfig, 0, len(s.providers))
	for _, p := range s.providers {
		out = append(out, cloneProviderConfig(p))
	}
	return out, nil
}

func (s *adminAuthProviderStore) CreateProvider(_ context.Context, cfg auth.ProviderConfig) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.providers[cfg.Name] = cloneProviderConfig(cfg)
	return nil
}

func (s *adminAuthProviderStore) UpsertProvider(ctx context.Context, cfg auth.ProviderConfig) error {
	return s.CreateProvider(ctx, cfg)
}

func (s *adminAuthProviderStore) UpdateProvider(_ context.Context, name string, cfg auth.ProviderConfig) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cfg.Name = name
	s.providers[name] = cloneProviderConfig(cfg)
	return nil
}

func (s *adminAuthProviderStore) UpdateProviderFields(_ context.Context, name string, update auth.ProviderUpdate) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.providers[name]
	if update.ProviderType != nil && *update.ProviderType != "" {
		p.ProviderType = *update.ProviderType
	}
	if update.Enabled != nil {
		p.Enabled = *update.Enabled
	}
	if len(update.ConfigJSON) > 0 && string(update.ConfigJSON) != "{}" && string(update.ConfigJSON) != "null" {
		p.ConfigJSON = append(json.RawMessage(nil), update.ConfigJSON...)
	}
	s.providers[name] = p
	return nil
}

func (s *adminAuthProviderStore) DeleteProvider(_ context.Context, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.providers, name)
	return nil
}

func (s *adminAuthProviderStore) CountEnabledProviders(_ context.Context) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, p := range s.providers {
		if p.Enabled {
			n++
		}
	}
	return n, nil
}

func (s *adminAuthProviderStore) CountUsersByProvider(_ context.Context, _ string) (int64, error) {
	return 0, nil
}

func (s *adminAuthProviderStore) FindUserByExternalID(_ context.Context, _, _ string) (*auth.User, error) {
	return nil, nil
}

func (s *adminAuthProviderStore) FindUserByExternalIDAndProviderType(_ context.Context, _, _ string) (*auth.User, error) {
	return nil, nil
}

func (s *adminAuthProviderStore) GetUserByID(_ context.Context, _ string) (*auth.User, error) {
	return nil, nil
}

func (s *adminAuthProviderStore) CreateUser(_ context.Context, _ *auth.User) error { return nil }
func (s *adminAuthProviderStore) CountUsers(_ context.Context) (int64, error)      { return 0, nil }
func (s *adminAuthProviderStore) UpdateUserProfile(_ context.Context, _ string, _ *auth.UserInfo) error {
	return nil
}
func (s *adminAuthProviderStore) UpdateLoginInfo(_ context.Context, _, _ string) error { return nil }
func (s *adminAuthProviderStore) RecordLogin(_ context.Context, _ *auth.LoginRecord) error {
	return nil
}
func (s *adminAuthProviderStore) GetUserQuota(_ context.Context, _ string) (*auth.UserQuota, error) {
	return nil, nil
}
func (s *adminAuthProviderStore) UpsertUserQuota(_ context.Context, _ string, _ int64) error {
	return nil
}
func (s *adminAuthProviderStore) IncrementTokenUsage(_ context.Context, _ string, _ int64) error {
	return nil
}
func (s *adminAuthProviderStore) ResetQuotaIfExpired(_ context.Context, _ string, _ time.Time) (*auth.UserQuota, error) {
	return nil, nil
}
func (s *adminAuthProviderStore) ListUsers(_ context.Context, _ string, _, _ int) ([]*auth.UserWithQuota, int64, error) {
	return nil, 0, nil
}
func (s *adminAuthProviderStore) GetUserWithQuota(_ context.Context, _ string) (*auth.UserWithQuota, error) {
	return nil, nil
}
func (s *adminAuthProviderStore) UpdateUserRole(_ context.Context, _, _ string) error {
	return nil
}
func (s *adminAuthProviderStore) UpdateUserStatus(_ context.Context, _, _ string) error {
	return nil
}
func (s *adminAuthProviderStore) GetLoginHistory(_ context.Context, _ string, _ int) ([]*auth.LoginRecord, error) {
	return nil, nil
}
func (s *adminAuthProviderStore) DeleteUser(_ context.Context, _ string) error { return nil }
func (s *adminAuthProviderStore) CountActiveAdmins(_ context.Context) (int64, error) {
	return 0, nil
}
func (s *adminAuthProviderStore) FindLocalUserByLogin(_ context.Context, _ string) (*auth.User, string, error) {
	return nil, "", nil
}
func (s *adminAuthProviderStore) CreateUserWithPassword(_ context.Context, _ *auth.User, _ string) error {
	return nil
}
func (s *adminAuthProviderStore) RegisterUserWithInvite(_ context.Context, _ *auth.User, _ string, _ string) error {
	return nil
}
func (s *adminAuthProviderStore) CreateInviteCode(_ context.Context, _ *auth.InviteCode, _ []byte, _ string) error {
	return nil
}
func (s *adminAuthProviderStore) GetInviteCodeByID(_ context.Context, _ string) (*auth.InviteCode, error) {
	return nil, nil
}
func (s *adminAuthProviderStore) ListInviteCodes(_ context.Context) ([]*auth.InviteCode, error) {
	return nil, nil
}
func (s *adminAuthProviderStore) UpdateInviteCode(_ context.Context, _ string, _ *bool, _ *string, _ *time.Time) error {
	return nil
}
func (s *adminAuthProviderStore) DeleteInviteCode(_ context.Context, _ string) error { return nil }
func (s *adminAuthProviderStore) FindInviteByLookup(_ context.Context, _ []byte) (*auth.InviteCode, error) {
	return nil, nil
}

func cloneProviderConfig(p auth.ProviderConfig) auth.ProviderConfig {
	p.ConfigJSON = append(json.RawMessage(nil), p.ConfigJSON...)
	return p
}

func newAdminAuthProviderTestHandler(store *adminAuthProviderStore) http.Handler {
	engine := auth.NewEngine(store, auth.NewJWTManager("test-secret", time.Hour, 24*time.Hour), zap.NewNop())
	srv := &Server{authEngine: engine, logger: zap.NewNop()}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/admin/auth/providers", srv.handleAdminListProviders)
	mux.HandleFunc("PATCH /api/v1/admin/auth/providers/{name}", srv.handleUpdateProvider)
	return mux
}

func TestAdminAuthProviders_RedactsNestedConfigJSON(t *testing.T) {
	st := newAdminAuthProviderStore()
	st.providers["ldap"] = auth.ProviderConfig{
		Name:         "ldap",
		ProviderType: "ldap",
		Enabled:      true,
		ConfigJSON:   json.RawMessage(`{"host":"ldap.example.com","bind_password":"raw-pass","nested":{"api_key":"raw-key"},"message":"client_secret=raw-inline"}`),
	}
	handler := newAdminAuthProviderTestHandler(st)

	rec := doJSON(t, handler, "GET", "/api/v1/admin/auth/providers", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("期望 200，得到 %d: %s", rec.Code, rec.Body.String())
	}

	body := rec.Body.String()
	for _, leaked := range []string{"raw-pass", "raw-key", "raw-inline"} {
		if strings.Contains(body, leaked) {
			t.Fatalf("auth provider 配置泄露敏感值 %q: %s", leaked, body)
		}
	}
	if !strings.Contains(body, security.RedactedValue) {
		t.Fatalf("auth provider 配置缺少统一脱敏占位: %s", body)
	}
}

func TestAdminAuthProviderUpdate_PreservesMaskedConfigSecrets(t *testing.T) {
	st := newAdminAuthProviderStore()
	st.providers["feishu"] = auth.ProviderConfig{
		Name:         "feishu",
		ProviderType: "feishu",
		Enabled:      true,
		ConfigJSON:   json.RawMessage(`{"app_id":"old","app_secret":"real-secret","nested":{"client_secret":"real-nested"}}`),
	}
	handler := newAdminAuthProviderTestHandler(st)

	rec := doJSON(t, handler, "PATCH", "/api/v1/admin/auth/providers/feishu", map[string]any{
		"config_json": map[string]any{
			"app_id":     "new",
			"app_secret": security.RedactedValue,
			"nested": map[string]any{
				"client_secret": "****",
			},
		},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("更新应成功，得到 %d: %s", rec.Code, rec.Body.String())
	}

	got := st.providers["feishu"].ConfigJSON
	if strings.Contains(string(got), security.RedactedValue) || strings.Contains(string(got), "****") {
		t.Fatalf("脱敏占位被写入数据库: %s", got)
	}
	if !strings.Contains(string(got), `"app_secret":"real-secret"`) ||
		!strings.Contains(string(got), `"client_secret":"real-nested"`) ||
		!strings.Contains(string(got), `"app_id":"new"`) {
		t.Fatalf("config_json 未正确保留真实 secret 并更新普通字段: %s", got)
	}
}

func TestAdminAuthProviderUpdate_PreservesInlineRedactedConfigValues(t *testing.T) {
	st := newAdminAuthProviderStore()
	st.providers["feishu"] = auth.ProviderConfig{
		Name:         "feishu",
		ProviderType: "feishu",
		Enabled:      true,
		ConfigJSON:   json.RawMessage(`{"callback_url":"https://callback.example.com/hook?token=real-token","app_id":"old"}`),
	}
	handler := newAdminAuthProviderTestHandler(st)

	rec := doJSON(t, handler, "PATCH", "/api/v1/admin/auth/providers/feishu", map[string]any{
		"config_json": map[string]any{
			"callback_url": "https://callback.example.com/hook?token=[REDACTED]",
			"app_id":       "new",
		},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("更新应成功，得到 %d: %s", rec.Code, rec.Body.String())
	}

	got := st.providers["feishu"].ConfigJSON
	if strings.Contains(string(got), security.RedactedValue) {
		t.Fatalf("内联脱敏占位被写入数据库: %s", got)
	}
	if !strings.Contains(string(got), `"callback_url":"https://callback.example.com/hook?token=real-token"`) ||
		!strings.Contains(string(got), `"app_id":"new"`) {
		t.Fatalf("config_json 未正确保留内联脱敏真实值: %s", got)
	}
}
