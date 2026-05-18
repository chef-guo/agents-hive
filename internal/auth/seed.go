package auth

import "context"

// SeedDefaultAdmin 在不存在 local/admin 时幂等插入 admin/admin。返回是否新建了种子账号。
// 不再要求 users 表为空：库中已有 OAuth 等用户时仍会补种本地管理员。
func SeedDefaultAdmin(ctx context.Context, store Store, enabled bool) (bool, error) {
	if !enabled {
		return false, nil
	}
	existing, err := store.FindUserByExternalID(ctx, "admin", localProviderName)
	if err != nil {
		return false, err
	}
	if existing != nil {
		return false, nil
	}
	hash, err := HashPassword("admin")
	if err != nil {
		return false, err
	}
	user := &User{
		ID:           generateRandomSecret(16),
		ExternalID:   "admin",
		AuthProvider: localProviderName,
		DisplayName:  "admin",
		Email:        "admin",
		Role:         "admin",
		Status:       "active",
	}
	if err := store.CreateUserWithPassword(ctx, user, hash); err != nil {
		return false, err
	}
	return true, nil
}

// SeedDefaultAdminEnabled 解析配置：nil 或 true 则种子。
func SeedDefaultAdminEnabled(cfg *bool) bool {
	if cfg == nil {
		return true
	}
	return *cfg
}
