package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/chef-guo/agents-hive/internal/auth"
	"github.com/chef-guo/agents-hive/internal/config"
)

// flowTestStore 是 auth.Store 的内存实现，供 API 层注册/邀请码/删用户测试。
type flowTestStore struct {
	users        map[string]*auth.User
	localLogins  map[string]*auth.User
	invites      map[string]*auth.InviteCode // key: string(lookup bytes)
	inviteByID   map[string]*auth.InviteCode
	activeAdmins int64
}

func newFlowTestStore() *flowTestStore {
	return &flowTestStore{
		users:       make(map[string]*auth.User),
		localLogins: make(map[string]*auth.User),
		invites:     make(map[string]*auth.InviteCode),
		inviteByID:  make(map[string]*auth.InviteCode),
		activeAdmins: 1,
	}
}

func (s *flowTestStore) seedAdmin(id string) {
	u := &auth.User{ID: id, Role: "admin", Status: "active", AuthProvider: "local", ExternalID: "admin@test", Email: "admin@test"}
	s.users[id] = u
	s.activeAdmins = 1
}

func (s *flowTestStore) seedSecondAdmin(id string) {
	u := &auth.User{ID: id, Role: "admin", Status: "active", AuthProvider: "local", ExternalID: "admin2@test", Email: "admin2@test"}
	s.users[id] = u
	s.activeAdmins = 2
}

func (s *flowTestStore) addInvite(plain string, inv *auth.InviteCode) error {
	hash, err := auth.HashPassword(plain)
	if err != nil {
		return err
	}
	inv.CodeHash = hash
	lookup := auth.InviteLookupKey(plain)
	s.invites[string(lookup)] = inv
	s.inviteByID[inv.ID] = inv
	return nil
}

func (s *flowTestStore) ListEnabledProviders(context.Context) ([]auth.ProviderConfig, error) {
	return nil, nil
}
func (s *flowTestStore) FindUserByExternalID(context.Context, string, string) (*auth.User, error) {
	return nil, nil
}
func (s *flowTestStore) FindUserByExternalIDAndProviderType(context.Context, string, string) (*auth.User, error) {
	return nil, nil
}
func (s *flowTestStore) GetUserByID(_ context.Context, id string) (*auth.User, error) {
	return s.users[id], nil
}
func (s *flowTestStore) CreateUser(ctx context.Context, u *auth.User) error {
	s.users[u.ID] = u
	return nil
}
func (s *flowTestStore) CountUsers(context.Context) (int64, error) {
	return int64(len(s.users)), nil
}
func (s *flowTestStore) UpdateUserProfile(context.Context, string, *auth.UserInfo) error {
	return nil
}
func (s *flowTestStore) UpdateLoginInfo(context.Context, string, string) error { return nil }
func (s *flowTestStore) RecordLogin(context.Context, *auth.LoginRecord) error  { return nil }
func (s *flowTestStore) GetUserQuota(context.Context, string) (*auth.UserQuota, error) {
	return nil, nil
}
func (s *flowTestStore) UpsertUserQuota(context.Context, string, int64) error { return nil }
func (s *flowTestStore) IncrementTokenUsage(context.Context, string, int64) error {
	return nil
}
func (s *flowTestStore) ResetQuotaIfExpired(context.Context, string, time.Time) (*auth.UserQuota, error) {
	return nil, nil
}
func (s *flowTestStore) ListUsers(context.Context, string, int, int) ([]*auth.UserWithQuota, int64, error) {
	return nil, 0, nil
}
func (s *flowTestStore) GetUserWithQuota(context.Context, string) (*auth.UserWithQuota, error) {
	return nil, nil
}
func (s *flowTestStore) UpdateUserRole(context.Context, string, string) error   { return nil }
func (s *flowTestStore) UpdateUserStatus(context.Context, string, string) error { return nil }
func (s *flowTestStore) DeleteUser(_ context.Context, id string) error {
	if u := s.users[id]; u != nil && u.Role == "admin" && u.Status == "active" {
		s.activeAdmins--
	}
	delete(s.users, id)
	return nil
}
func (s *flowTestStore) CountActiveAdmins(context.Context) (int64, error) {
	return s.activeAdmins, nil
}
func (s *flowTestStore) GetLoginHistory(context.Context, string, int) ([]*auth.LoginRecord, error) {
	return nil, nil
}
func (s *flowTestStore) FindLocalUserByLogin(_ context.Context, login string) (*auth.User, string, error) {
	if u := s.localLogins[login]; u != nil {
		return u, "", nil
	}
	return nil, "", nil
}
func (s *flowTestStore) CreateUserWithPassword(ctx context.Context, u *auth.User, _ string) error {
	s.users[u.ID] = u
	s.localLogins[u.Email] = u
	return nil
}
func (s *flowTestStore) RegisterUserWithInvite(ctx context.Context, u *auth.User, _ string, inviteID string) error {
	if inv := s.inviteByID[inviteID]; inv != nil {
		inv.UseCount++
	}
	s.users[u.ID] = u
	s.localLogins[u.Email] = u
	return nil
}
func (s *flowTestStore) CreateInviteCode(_ context.Context, inv *auth.InviteCode, lookup []byte, hash string) error {
	inv.CodeHash = hash
	s.invites[string(lookup)] = inv
	s.inviteByID[inv.ID] = inv
	return nil
}
func (s *flowTestStore) GetInviteCodeByID(_ context.Context, id string) (*auth.InviteCode, error) {
	return s.inviteByID[id], nil
}
func (s *flowTestStore) ListInviteCodes(_ context.Context) ([]*auth.InviteCode, error) {
	out := make([]*auth.InviteCode, 0, len(s.inviteByID))
	for _, inv := range s.inviteByID {
		out = append(out, inv)
	}
	return out, nil
}
func (s *flowTestStore) UpdateInviteCode(context.Context, string, *bool, *string, *time.Time) error {
	return nil
}
func (s *flowTestStore) DeleteInviteCode(_ context.Context, id string) error {
	delete(s.inviteByID, id)
	return nil
}
func (s *flowTestStore) FindInviteByLookup(_ context.Context, lookup []byte) (*auth.InviteCode, error) {
	return s.invites[string(lookup)], nil
}
func (s *flowTestStore) ListAllProviders(context.Context) ([]auth.ProviderConfig, error) {
	return nil, nil
}
func (s *flowTestStore) CreateProvider(context.Context, auth.ProviderConfig) error { return nil }
func (s *flowTestStore) UpsertProvider(context.Context, auth.ProviderConfig) error  { return nil }
func (s *flowTestStore) UpdateProvider(context.Context, string, auth.ProviderConfig) error {
	return nil
}
func (s *flowTestStore) UpdateProviderFields(context.Context, string, auth.ProviderUpdate) error {
	return nil
}
func (s *flowTestStore) DeleteProvider(context.Context, string) error { return nil }
func (s *flowTestStore) CountEnabledProviders(context.Context) (int, error) { return 0, nil }
func (s *flowTestStore) CountUsersByProvider(context.Context, string) (int64, error) {
	return 0, nil
}

func newFlowTestServer(t *testing.T, store *flowTestStore, authCfg config.AuthConfig) *Server {
	t.Helper()
	logger, _ := zap.NewDevelopment()
	jwt := auth.NewJWTManager("flow-test-secret", time.Hour, 24*time.Hour)
	engine := auth.NewEngine(store, jwt, logger)
	cfg := config.Default()
	cfg.Auth = authCfg
	return &Server{logger: logger, config: cfg, authEngine: engine}
}

func adminContext(userID string) context.Context {
	return auth.WithUser(
		auth.WithAuthEnabled(context.Background()),
		&auth.User{ID: userID, Role: "admin", Status: "active"},
	)
}

func TestHandleAuthRegister_RegistrationClosed(t *testing.T) {
	store := newFlowTestStore()
	srv := newFlowTestServer(t, store, config.AuthConfig{AllowPublicRegistration: false})
	body := `{"email":"u@example.com","password":"password123"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/register", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.handleAuthRegister(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "registration_closed") {
		t.Fatalf("expected registration_closed, got %s", rec.Body.String())
	}
}

func TestHandleAuthRegister_PublicSuccess(t *testing.T) {
	store := newFlowTestStore()
	srv := newFlowTestServer(t, store, config.AuthConfig{AllowPublicRegistration: true})
	body := `{"email":"new@example.com","password":"password123"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/register", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.handleAuthRegister(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"token"`) {
		t.Fatalf("expected token in body: %s", rec.Body.String())
	}
}

func TestHandleAuthRegister_InviteInvalid(t *testing.T) {
	store := newFlowTestStore()
	srv := newFlowTestServer(t, store, config.AuthConfig{AllowPublicRegistration: false})
	body := `{"email":"u@example.com","password":"password123","invite_code":"BADCODE"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/register", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.handleAuthRegister(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "invite_invalid") {
		t.Fatalf("expected invite_invalid, got %s", rec.Body.String())
	}
}

func TestHandleAuthRegister_InviteSuccess(t *testing.T) {
	store := newFlowTestStore()
	plain := "TESTINVITECODE"
	inv := &auth.InviteCode{
		ID:        "inv-1",
		Role:      "user",
		MaxUses:   5,
		UseCount:  0,
		ExpiresAt: time.Now().Add(24 * time.Hour),
	}
	if err := store.addInvite(plain, inv); err != nil {
		t.Fatal(err)
	}
	srv := newFlowTestServer(t, store, config.AuthConfig{AllowPublicRegistration: false})
	body, _ := json.Marshal(map[string]string{
		"email":       "invited@example.com",
		"password":    "password123",
		"invite_code": plain,
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/register", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.handleAuthRegister(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandleAuthStatus_ExtendedFields(t *testing.T) {
	store := newFlowTestStore()
	srv := newFlowTestServer(t, store, config.AuthConfig{
		AllowPublicRegistration:     true,
		InviteErrorWeakDistinction: true,
	})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/status", nil)
	rec := httptest.NewRecorder()
	srv.handleAuthStatus(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{
		`"enabled":true`,
		`"allow_public_registration":true`,
		`"invite_error_weak_distinction":true`,
		`"has_local_register":true`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("body missing %s: %s", want, body)
		}
	}
}

func TestHandleDeleteUser_LastActiveAdmin(t *testing.T) {
	store := newFlowTestStore()
	store.seedAdmin("admin-only")
	srv := newFlowTestServer(t, store, config.AuthConfig{})
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/admin/users/admin-only", nil)
	req.SetPathValue("id", "admin-only")
	req = req.WithContext(adminContext("other-admin"))
	rec := httptest.NewRecorder()
	srv.handleDeleteUser(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandleDeleteUser_Success(t *testing.T) {
	store := newFlowTestStore()
	store.seedAdmin("admin-1")
	store.seedSecondAdmin("admin-2")
	store.users["user-del"] = &auth.User{ID: "user-del", Role: "user", Status: "active"}
	srv := newFlowTestServer(t, store, config.AuthConfig{})
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/admin/users/user-del", nil)
	req.SetPathValue("id", "user-del")
	req = req.WithContext(adminContext("admin-1"))
	rec := httptest.NewRecorder()
	srv.handleDeleteUser(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if _, ok := store.users["user-del"]; ok {
		t.Fatal("user should be deleted")
	}
}

func TestHandleListInviteCodes_Admin(t *testing.T) {
	store := newFlowTestStore()
	_ = store.addInvite("CODE1", &auth.InviteCode{
		ID: "i1", Role: "user", MaxUses: 1, ExpiresAt: time.Now().Add(time.Hour),
	})
	srv := newFlowTestServer(t, store, config.AuthConfig{})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/auth/invite-codes", nil)
	req = req.WithContext(adminContext("admin-1"))
	rec := httptest.NewRecorder()
	srv.handleListInviteCodes(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "invite_codes") {
		t.Fatalf("expected invite_codes array: %s", rec.Body.String())
	}
}

func TestAuthMiddleware_NilEngineProtectedPath503(t *testing.T) {
	handler := auth.AuthMiddleware(nil)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 when authEngine nil, got %d", rec.Code)
	}
}
