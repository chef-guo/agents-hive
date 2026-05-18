package auth

import (
	"context"
	"encoding/json"
	"time"
)

// Store auth 模块的数据访问接口
type Store interface {
	// Provider
	ListEnabledProviders(ctx context.Context) ([]ProviderConfig, error)

	// User CRUD
	FindUserByExternalID(ctx context.Context, externalID, provider string) (*User, error)
	// FindUserByExternalIDAndProviderType 按 external_id + provider_type 查找用户
	// IM 路径使用此方法：platformToProvider 返回 type（feishu/dingtalk），
	// 而 users.auth_provider 存的是 provider name，需要 JOIN auth_providers 表匹配
	FindUserByExternalIDAndProviderType(ctx context.Context, externalID, providerType string) (*User, error)
	GetUserByID(ctx context.Context, userID string) (*User, error)
	CreateUser(ctx context.Context, user *User) error
	CountUsers(ctx context.Context) (int64, error) // Bootstrap admin: 判断是否为首个用户
	UpdateUserProfile(ctx context.Context, userID string, info *UserInfo) error
	UpdateLoginInfo(ctx context.Context, userID, ip string) error

	// Login history
	RecordLogin(ctx context.Context, record *LoginRecord) error

	// Quota（Phase 5B 新增）
	GetUserQuota(ctx context.Context, userID string) (*UserQuota, error)
	UpsertUserQuota(ctx context.Context, userID string, tokenQuota int64) error
	IncrementTokenUsage(ctx context.Context, userID string, tokens int64) error
	ResetQuotaIfExpired(ctx context.Context, userID string, now time.Time) (*UserQuota, error)

	// Admin 用户管理（Phase 5C 新增）
	ListUsers(ctx context.Context, query string, page, size int) ([]*UserWithQuota, int64, error)
	GetUserWithQuota(ctx context.Context, userID string) (*UserWithQuota, error)
	UpdateUserRole(ctx context.Context, userID, role string) error
	UpdateUserStatus(ctx context.Context, userID, status string) error
	DeleteUser(ctx context.Context, userID string) error
	CountActiveAdmins(ctx context.Context) (int64, error)
	GetLoginHistory(ctx context.Context, userID string, limit int) ([]*LoginRecord, error)

	// 本地账号
	FindLocalUserByLogin(ctx context.Context, login string) (*User, string, error)
	CreateUserWithPassword(ctx context.Context, user *User, passwordHash string) error
	RegisterUserWithInvite(ctx context.Context, user *User, passwordHash string, inviteID string) error

	// 邀请码
	CreateInviteCode(ctx context.Context, invite *InviteCode, lookup []byte, hash string) error
	GetInviteCodeByID(ctx context.Context, id string) (*InviteCode, error)
	ListInviteCodes(ctx context.Context) ([]*InviteCode, error)
	UpdateInviteCode(ctx context.Context, id string, disabled *bool, note *string, expiresAt *time.Time) error
	DeleteInviteCode(ctx context.Context, id string) error
	FindInviteByLookup(ctx context.Context, lookup []byte) (*InviteCode, error)

	// Admin Provider 管理（Phase 5C 新增）
	ListAllProviders(ctx context.Context) ([]ProviderConfig, error)
	CreateProvider(ctx context.Context, cfg ProviderConfig) error
	UpsertProvider(ctx context.Context, cfg ProviderConfig) error
	UpdateProvider(ctx context.Context, name string, cfg ProviderConfig) error
	// UpdateProviderFields partial-updates only the non-nil fields in update.
	// Use this instead of UpdateProvider to avoid the bool zero-value trap.
	UpdateProviderFields(ctx context.Context, name string, update ProviderUpdate) error
	// DeleteProvider deletes the named provider.
	// Returns pgx.ErrNoRows if the provider does not exist.
	// Returns an error if the provider is the last enabled one.
	DeleteProvider(ctx context.Context, name string) error
	CountEnabledProviders(ctx context.Context) (int, error)
	// CountUsersByProvider 统计关联到指定 provider name 的用户数量
	CountUsersByProvider(ctx context.Context, providerName string) (int64, error)
}

// UserQuota 用户配额信息
type UserQuota struct {
	UserID       string    `json:"user_id"`
	TokenQuota   int64     `json:"token_quota"`
	TokenUsed    int64     `json:"token_used"`
	QuotaResetAt time.Time `json:"quota_reset_at"`
}

// UserWithQuota 用户信息含配额（Admin 详情接口用）
type UserWithQuota struct {
	*User
	TokenQuota   int64     `json:"token_quota"`
	TokenUsed    int64     `json:"token_used"`
	QuotaResetAt time.Time `json:"quota_reset_at"`
}

// ProviderUpdate Partial update for a provider — nil fields are left unchanged.
// This avoids the Go bool zero-value trap where PATCH omitting "enabled" defaults to false.
type ProviderUpdate struct {
	ProviderType *string         `json:"provider_type,omitempty"` // nil = no change
	Enabled      *bool           `json:"enabled,omitempty"`      // nil = no change; true/false = set value
	ConfigJSON   json.RawMessage `json:"config_json,omitempty"`   // empty = no change
}

// ProviderConfig DB 中的 provider 配置行
type ProviderConfig struct {
	Name         string          `json:"name"`
	ProviderType string          `json:"provider_type"`
	Enabled      bool            `json:"enabled"`
	ConfigJSON   json.RawMessage `json:"config_json"`
}
