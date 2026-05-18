package auth

import "context"

// SeedDefaultAdmin 在 users 为空时幂等插入 admin/admin。
func SeedDefaultAdmin(ctx context.Context, store Store, enabled bool) error {
	if !enabled {
		return nil
	}
	count, err := store.CountUsers(ctx)
	if err != nil || count > 0 {
		return err
	}
	hash, err := HashPassword("admin")
	if err != nil {
		return err
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
	return store.CreateUserWithPassword(ctx, user, hash)
}

// SeedDefaultAdminEnabled 解析配置：nil 或 true 则种子。
func SeedDefaultAdminEnabled(cfg *bool) bool {
	if cfg == nil {
		return true
	}
	return *cfg
}
