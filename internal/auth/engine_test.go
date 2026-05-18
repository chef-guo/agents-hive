package auth

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// mockStore 实现 Store 接口，用于测试
type mockStore struct {
	users  map[string]*User // key: externalID+":"+provider
	byID   map[string]*User
	count  int64 // 仅用于幂等性断言
	logins []LoginRecord
}

func newMockStore() *mockStore {
	return &mockStore{
		users: make(map[string]*User),
		byID:  make(map[string]*User),
	}
}

func (m *mockStore) ListEnabledProviders(ctx context.Context) ([]ProviderConfig, error) {
	return nil, nil
}

func (m *mockStore) FindUserByExternalID(ctx context.Context, externalID, provider string) (*User, error) {
	key := externalID + ":" + provider
	return m.users[key], nil
}

func (m *mockStore) FindUserByExternalIDAndProviderType(ctx context.Context, externalID, providerType string) (*User, error) {
	// 测试中 provider name == provider type，直接复用 FindUserByExternalID 逻辑
	key := externalID + ":" + providerType
	return m.users[key], nil
}

func (m *mockStore) GetUserByID(ctx context.Context, userID string) (*User, error) {
	return m.byID[userID], nil
}

func (m *mockStore) CreateUser(ctx context.Context, user *User) error {
	key := user.ExternalID + ":" + user.AuthProvider
	m.users[key] = user
	m.byID[user.ID] = user
	m.count++
	return nil
}

func (m *mockStore) UpdateUserProfile(ctx context.Context, userID string, info *UserInfo) error {
	return nil
}

func (m *mockStore) UpdateLoginInfo(ctx context.Context, userID, ip string) error {
	return nil
}

func (m *mockStore) RecordLogin(ctx context.Context, record *LoginRecord) error {
	m.logins = append(m.logins, *record)
	return nil
}

func (m *mockStore) GetUserQuota(ctx context.Context, userID string) (*UserQuota, error) {
	return nil, nil
}

func (m *mockStore) UpsertUserQuota(ctx context.Context, userID string, tokenQuota int64) error {
	return nil
}

func (m *mockStore) IncrementTokenUsage(ctx context.Context, userID string, tokens int64) error {
	return nil
}

func (m *mockStore) ResetQuotaIfExpired(ctx context.Context, userID string, now time.Time) (*UserQuota, error) {
	return nil, nil
}

func (m *mockStore) ListUsers(_ context.Context, _ string, _, _ int) ([]*UserWithQuota, int64, error) {
	return nil, 0, nil
}
func (m *mockStore) GetUserWithQuota(_ context.Context, _ string) (*UserWithQuota, error) {
	return nil, nil
}
func (m *mockStore) UpdateUserRole(_ context.Context, _, _ string) error { return nil }
func (m *mockStore) UpdateUserStatus(_ context.Context, _, _ string) error { return nil }
func (m *mockStore) GetLoginHistory(_ context.Context, _ string, _ int) ([]*LoginRecord, error) {
	return nil, nil
}
func (m *mockStore) ListAllProviders(_ context.Context) ([]ProviderConfig, error) {
	return nil, nil
}
func (m *mockStore) CreateProvider(_ context.Context, _ ProviderConfig) error { return nil }
func (m *mockStore) UpsertProvider(_ context.Context, _ ProviderConfig) error { return nil }
func (m *mockStore) UpdateProvider(_ context.Context, _ string, _ ProviderConfig) error {
	return nil
}
func (m *mockStore) DeleteProvider(_ context.Context, _ string) error { return nil }
func (m *mockStore) CountEnabledProviders(_ context.Context) (int, error) { return 0, nil }
func (m *mockStore) UpdateProviderFields(_ context.Context, _ string, _ ProviderUpdate) error { return nil }
func (m *mockStore) CountUsersByProvider(_ context.Context, _ string) (int64, error) { return 0, nil }
func (m *mockStore) CountUsers(_ context.Context) (int64, error) {
	return int64(len(m.users)), nil
}

func (m *mockStore) DeleteUser(_ context.Context, userID string) error {
	delete(m.byID, userID)
	return nil
}
func (m *mockStore) CountActiveAdmins(_ context.Context) (int64, error) { return 1, nil }
func (m *mockStore) FindLocalUserByLogin(_ context.Context, login string) (*User, string, error) {
	return nil, "", nil
}
func (m *mockStore) CreateUserWithPassword(ctx context.Context, user *User, _ string) error {
	return m.CreateUser(ctx, user)
}
func (m *mockStore) RegisterUserWithInvite(ctx context.Context, user *User, _ string, _ string) error {
	return m.CreateUser(ctx, user)
}
func (m *mockStore) CreateInviteCode(_ context.Context, _ *InviteCode, _ []byte, _ string) error {
	return nil
}
func (m *mockStore) GetInviteCodeByID(_ context.Context, _ string) (*InviteCode, error) { return nil, nil }
func (m *mockStore) ListInviteCodes(_ context.Context) ([]*InviteCode, error) { return nil, nil }
func (m *mockStore) UpdateInviteCode(_ context.Context, _ string, _ *bool, _ *string, _ *time.Time) error {
	return nil
}
func (m *mockStore) DeleteInviteCode(_ context.Context, _ string) error { return nil }
func (m *mockStore) FindInviteByLookup(_ context.Context, _ []byte) (*InviteCode, error) {
	return nil, nil
}

func newTestEngine(store Store) *Engine {
	mgr := NewJWTManager("test-secret", time.Hour, 7*24*time.Hour)
	return NewEngine(store, mgr, zap.NewNop())
}

func TestFindOrCreateUserIdempotent(t *testing.T) {
	store := newMockStore()
	engine := newTestEngine(store)

	info := &UserInfo{ExternalID: "open-123", DisplayName: "张三", Email: "z@example.com"}

	user1, err := engine.FindOrCreateUser(context.Background(), "feishu", info)
	require.NoError(t, err)
	require.NotNil(t, user1)

	user2, err := engine.FindOrCreateUser(context.Background(), "feishu", info)
	require.NoError(t, err)
	assert.Equal(t, user1.ID, user2.ID, "同一用户两次调用应返回同一 ID")
	assert.Equal(t, int64(1), store.count, "只应创建一次")
}

func TestBootstrapAdmin_FirstUserIsAdmin(t *testing.T) {
	store := newMockStore()
	engine := newTestEngine(store)

	info := &UserInfo{ExternalID: "open-001", DisplayName: "First User"}
	user, err := engine.FindOrCreateUser(context.Background(), "feishu", info)
	require.NoError(t, err)
	assert.Equal(t, "admin", user.Role, "首个用户应自动成为 admin")
}

func TestBootstrapAdmin_SecondUserIsUser(t *testing.T) {
	store := newMockStore()
	engine := newTestEngine(store)

	// 第一个用户
	info1 := &UserInfo{ExternalID: "open-001", DisplayName: "First"}
	_, err := engine.FindOrCreateUser(context.Background(), "feishu", info1)
	require.NoError(t, err)

	// 第二个用户应为普通 user
	info2 := &UserInfo{ExternalID: "open-002", DisplayName: "Second"}
	user2, err := engine.FindOrCreateUser(context.Background(), "feishu", info2)
	require.NoError(t, err)
	assert.Equal(t, "user", user2.Role, "第二个用户角色应为 user")
}

func TestOAuthLoginDisabledUser(t *testing.T) {
	store := newMockStore()
	engine := newTestEngine(store)

	// 先创建用户
	info := &UserInfo{ExternalID: "open-disabled", DisplayName: "Disabled"}
	user, err := engine.FindOrCreateUser(context.Background(), "feishu", info)
	require.NoError(t, err)

	// 手动设置为 disabled
	user.Status = "disabled"
	store.users["open-disabled:feishu"] = user
	store.byID[user.ID] = user

	// 注册一个 mock OAuth provider
	engine.RegisterOAuthProvider("feishu", &mockOAuthProvider{
		userInfo: info,
	})

	_, _, err = engine.OAuthLogin(context.Background(), "feishu", "code-123", "127.0.0.1", "test")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "禁用")
}

// mockOAuthProvider 用于测试
type mockOAuthProvider struct {
	userInfo *UserInfo
}

func (m *mockOAuthProvider) Type() string                { return "feishu" }
func (m *mockOAuthProvider) AuthCodeURL(state string) string { return "https://example.com/auth?state=" + state }
func (m *mockOAuthProvider) Exchange(ctx context.Context, code string) (*UserInfo, error) {
	return m.userInfo, nil
}

// mockStoreWithQuota 支持配额控制的 mockStore
type mockStoreWithQuota struct {
	mockStore
	quota *UserQuota
}

func (m *mockStoreWithQuota) GetUserQuota(ctx context.Context, userID string) (*UserQuota, error) {
	return m.quota, nil
}

func (m *mockStoreWithQuota) ResetQuotaIfExpired(ctx context.Context, userID string, now time.Time) (*UserQuota, error) {
	return m.quota, nil
}

func TestCheckQuota_NoRecord(t *testing.T) {
	store := &mockStoreWithQuota{mockStore: *newMockStore(), quota: nil}
	engine := newTestEngine(store)
	err := engine.CheckQuota(context.Background(), "user-1")
	assert.NoError(t, err, "无配额记录应通过（无限制）")
}

func TestCheckQuota_UnderLimit(t *testing.T) {
	store := &mockStoreWithQuota{
		mockStore: *newMockStore(),
		quota:     &UserQuota{UserID: "user-1", TokenQuota: 1000, TokenUsed: 500},
	}
	engine := newTestEngine(store)
	err := engine.CheckQuota(context.Background(), "user-1")
	assert.NoError(t, err, "未超限应通过")
}

func TestCheckQuota_Exceeded(t *testing.T) {
	store := &mockStoreWithQuota{
		mockStore: *newMockStore(),
		quota:     &UserQuota{UserID: "user-1", TokenQuota: 1000, TokenUsed: 1000},
	}
	engine := newTestEngine(store)
	err := engine.CheckQuota(context.Background(), "user-1")
	assert.Error(t, err, "已超限应返回错误")
	assert.Contains(t, err.Error(), "配额")
}

func TestCheckQuota_ZeroQuota_Unlimited(t *testing.T) {
	store := &mockStoreWithQuota{
		mockStore: *newMockStore(),
		quota:     &UserQuota{UserID: "user-1", TokenQuota: 0, TokenUsed: 9999},
	}
	engine := newTestEngine(store)
	err := engine.CheckQuota(context.Background(), "user-1")
	assert.NoError(t, err, "token_quota=0 表示无限制，应通过")
}
