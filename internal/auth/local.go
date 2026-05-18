package auth

import (
	"context"
	"fmt"
)

// LocalProvider 本地账密认证（密码存 users.password_hash）。
type LocalProvider struct {
	store Store
}

// NewLocalProvider 创建本地 provider。
func NewLocalProvider(store Store) *LocalProvider {
	return &LocalProvider{store: store}
}

func (p *LocalProvider) Type() string { return localProviderName }

func (p *LocalProvider) Authenticate(ctx context.Context, username, password string) (*UserInfo, error) {
	login := NormalizeLocalLogin(username)
	u, hash, err := p.store.FindLocalUserByLogin(ctx, login)
	if err != nil {
		return nil, err
	}
	if u == nil || !CheckPassword(hash, password) {
		return nil, fmt.Errorf("认证失败")
	}
	email := u.Email
	if email == "" {
		email = u.ExternalID
	}
	return &UserInfo{
		ExternalID:  u.ExternalID,
		DisplayName: u.DisplayName,
		Email:       email,
		AvatarURL:   u.AvatarURL,
		Department:  u.Department,
	}, nil
}
