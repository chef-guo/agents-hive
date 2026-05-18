package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func newTestMiddlewareEngine() (*Engine, *mockStore) {
	store := newMockStore()
	mgr := NewJWTManager("test-secret", time.Hour, 7*24*time.Hour)
	engine := NewEngine(store, mgr, zap.NewNop())
	return engine, store
}

func callMiddleware(engine *Engine, path, authHeader string) *httptest.ResponseRecorder {
	handler := AuthMiddleware(engine)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func TestMiddlewarePublicPathAllowed(t *testing.T) {
	engine, _ := newTestMiddlewareEngine()
	paths := []string{
		"/api/v1/auth/providers",
		"/api/v1/health",
		"/api/v1/auth/login",
		"/api/v1/auth/callback",
		"/api/v1/auth/refresh",
		"/api/v1/auth/register",
		"/api/v1/channel/feishu/webhook",
	}
	for _, p := range paths {
		rec := callMiddleware(engine, p, "")
		assert.Equal(t, http.StatusOK, rec.Code, "公开路径应放行: %s", p)
	}
}

func TestMiddlewarePublicPathExactMatch(t *testing.T) {
	engine, _ := newTestMiddlewareEngine()
	// /api/v1/auth/providers/evil 不应被放行（精确匹配修复）
	rec := callMiddleware(engine, "/api/v1/auth/providers/evil", "")
	assert.Equal(t, http.StatusUnauthorized, rec.Code, "/api/v1/auth/providers/evil 不应放行")
}

func TestMiddlewareAuthMeRequiresAuth(t *testing.T) {
	engine, _ := newTestMiddlewareEngine()
	rec := callMiddleware(engine, "/api/v1/auth/me", "")
	assert.Equal(t, http.StatusUnauthorized, rec.Code, "/api/v1/auth/me 需要认证")
}

func TestMiddlewareNoToken(t *testing.T) {
	engine, _ := newTestMiddlewareEngine()
	rec := callMiddleware(engine, "/api/v1/sessions", "")
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestMiddlewareInvalidToken(t *testing.T) {
	engine, _ := newTestMiddlewareEngine()
	rec := callMiddleware(engine, "/api/v1/sessions", "Bearer invalid.token.here")
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestMiddlewareGatewayRPCAllowsGatewayTokenToReachGateway(t *testing.T) {
	engine, _ := newTestMiddlewareEngine()
	rec := callMiddleware(engine, "/api/v1/rpc", "Bearer machine-token")
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestMiddlewareGatewayRPCInjectsValidWebUser(t *testing.T) {
	engine, store := newTestMiddlewareEngine()

	user := &User{ID: "user-admin", ExternalID: "ext-admin", AuthProvider: "feishu", Role: "admin", Status: "active"}
	store.byID[user.ID] = user

	token, err := engine.JWT().Issue(user.ID, user.Role, "feishu")
	require.NoError(t, err)

	var capturedUser *User
	handler := AuthMiddleware(engine)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedUser = UserFrom(r.Context())
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodPost, "/api/v1/rpc", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	require.NotNil(t, capturedUser)
	assert.Equal(t, "user-admin", capturedUser.ID)
}

func TestMiddlewareDisabledUser(t *testing.T) {
	engine, store := newTestMiddlewareEngine()

	// 创建 disabled 用户
	user := &User{ID: "user-disabled", ExternalID: "ext-1", AuthProvider: "feishu", Role: "user", Status: "disabled"}
	store.byID[user.ID] = user

	token, err := engine.JWT().Issue(user.ID, user.Role, "feishu")
	require.NoError(t, err)

	rec := callMiddleware(engine, "/api/v1/sessions", "Bearer "+token)
	assert.Equal(t, http.StatusForbidden, rec.Code)
}

func TestMiddlewareValidToken(t *testing.T) {
	engine, store := newTestMiddlewareEngine()

	user := &User{ID: "user-active", ExternalID: "ext-2", AuthProvider: "feishu", Role: "user", Status: "active"}
	store.byID[user.ID] = user

	token, err := engine.JWT().Issue(user.ID, user.Role, "feishu")
	require.NoError(t, err)

	var capturedUser *User
	handler := AuthMiddleware(engine)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedUser = UserFrom(r.Context())
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	require.NotNil(t, capturedUser)
	assert.Equal(t, "user-active", capturedUser.ID)
}
