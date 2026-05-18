package auth

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/chef-guo/agents-hive/internal/errs"
	"go.uber.org/zap"
)

// Engine 认证引擎
type Engine struct {
	mu             sync.RWMutex
	bootstrapMu    sync.Mutex // 防止并发注册时多个首用户都成为 admin
	oauthProviders map[string]OAuthProvider
	credProviders  map[string]CredentialProvider
	jwt            *JWTManager
	store          Store
	logger         *zap.Logger
	userCache      sync.Map // userID → *cachedUser，5 分钟 TTL
	loginAttempts  sync.Map // ip → *loginAttemptRecord，LDAP 暴力破解防护
}

// loginAttemptRecord 记录某 IP 的登录失败次数
type loginAttemptRecord struct {
	mu       sync.Mutex
	count    int
	resetAt  time.Time
	lastSeen time.Time // 最后活跃时间，用于清理过期条目
}

// NewEngine 创建认证引擎
func NewEngine(store Store, jwt *JWTManager, logger *zap.Logger) *Engine {
	e := &Engine{
		oauthProviders: make(map[string]OAuthProvider),
		credProviders:  make(map[string]CredentialProvider),
		jwt:            jwt,
		store:          store,
		logger:         logger,
	}
	// 内置 local 账密（种子 admin、注册账号）；LoadProvidersFromDB 会重建 cred map 后再次挂载。
	e.MountLocalProvider()
	// BUG-5: 后台定期清理超过 15 分钟未活跃的 loginAttempts 条目，防止内存泄漏
	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for range ticker.C {
			cutoff := time.Now().Add(-15 * time.Minute)
			e.loginAttempts.Range(func(key, value any) bool {
				rec := value.(*loginAttemptRecord)
				rec.mu.Lock()
				inactive := rec.lastSeen.Before(cutoff)
				if inactive {
					e.loginAttempts.Delete(key)
				}
				rec.mu.Unlock()
				return true
			})
		}
	}()
	return e
}

// RegisterOAuthProvider 注册 OAuth provider
func (e *Engine) RegisterOAuthProvider(name string, p OAuthProvider) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.oauthProviders[name] = p
}

// RegisterCredentialProvider 注册凭证 provider
func (e *Engine) RegisterCredentialProvider(name string, p CredentialProvider) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.credProviders[name] = p
}

// GetOAuthProvider 获取 OAuth provider。
// 若 provider 处于 degraded 状态（配置解析失败），返回 nil, false，让调用方走 404 路径。
func (e *Engine) GetOAuthProvider(name string) (OAuthProvider, bool) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	p, ok := e.oauthProviders[name]
	if !ok {
		return nil, false
	}
	if _, degraded := p.(*degradedOAuthProvider); degraded {
		return nil, false
	}
	return p, true
}

// GetCredentialProvider 获取凭证 provider
func (e *Engine) GetCredentialProvider(name string) (CredentialProvider, bool) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	p, ok := e.credProviders[name]
	return p, ok
}

// ListProviders 列出所有已注册的 provider 信息，过滤掉配置解析失败的 degraded provider。
func (e *Engine) ListProviders() []ProviderInfo {
	e.mu.RLock()
	defer e.mu.RUnlock()
	var list []ProviderInfo
	for name, p := range e.oauthProviders {
		if _, degraded := p.(*degradedOAuthProvider); degraded {
			continue // 不暴露解析失败的 provider
		}
		list = append(list, ProviderInfo{Name: name, ProviderType: p.Type()})
	}
	for name, p := range e.credProviders {
		if _, degraded := p.(*degradedCredProvider); degraded {
			continue
		}
		list = append(list, ProviderInfo{Name: name, ProviderType: p.Type()})
	}
	return list
}

// LoadProvidersFromDB 从 DB 加载并注册 provider。
// 先在锁外构建新 map，再一次性替换，确保被删除/禁用的 provider 不残留内存。
func (e *Engine) LoadProvidersFromDB(ctx context.Context) error {
	providers, err := e.store.ListEnabledProviders(ctx)
	if err != nil {
		return fmt.Errorf("加载 provider 失败: %w", err)
	}

	newOAuth := make(map[string]OAuthProvider)
	newCred := make(map[string]CredentialProvider)

	for _, p := range providers {
		switch p.ProviderType {
		case "feishu":
			var cfg FeishuAuthConfig
			if err := unmarshalConfig(p.ConfigJSON, &cfg); err != nil {
				e.logger.Warn("解析飞书 provider 配置失败，保留占位以维持内存 count 一致", zap.String("name", p.Name), zap.Error(err))
				newOAuth[p.ProviderType] = &degradedOAuthProvider{providerType: p.ProviderType, name: p.Name}
				continue
			}
			newOAuth[p.ProviderType] = NewFeishuProvider(cfg)
		case "dingtalk":
			var cfg DingTalkAuthConfig
			if err := unmarshalConfig(p.ConfigJSON, &cfg); err != nil {
				e.logger.Warn("解析钉钉 provider 配置失败，保留占位以维持内存 count 一致", zap.String("name", p.Name), zap.Error(err))
				newOAuth[p.ProviderType] = &degradedOAuthProvider{providerType: p.ProviderType, name: p.Name}
				continue
			}
			newOAuth[p.ProviderType] = NewDingTalkProvider(cfg)
		case "ldap":
			var cfg LDAPAuthConfig
			if err := unmarshalConfig(p.ConfigJSON, &cfg); err != nil {
				e.logger.Warn("解析 LDAP provider 配置失败，保留占位以维持内存 count 一致", zap.String("name", p.Name), zap.Error(err))
				newCred[p.ProviderType] = &degradedCredProvider{providerType: p.ProviderType, name: p.Name}
				continue
			}
			ldapProvider := NewLDAPProvider(cfg)
			ldapProvider.SetLogger(e.logger)
			newCred[p.ProviderType] = ldapProvider
		default:
			e.logger.Warn("未知 provider 类型", zap.String("type", p.ProviderType))
		}
	}

	e.mu.Lock()
	e.oauthProviders = newOAuth
	e.credProviders = newCred
	e.mu.Unlock()
	e.MountLocalProvider()
	return nil
}

// MountLocalProvider 挂载内置 local 凭证 Provider（LoadProvidersFromDB 之后调用）。
func (e *Engine) MountLocalProvider() {
	e.RegisterCredentialProvider(localProviderName, NewLocalProvider(e.store))
}

// FindOrCreateUser 查找或创建用户
func (e *Engine) FindOrCreateUser(ctx context.Context, provider string, info *UserInfo) (*User, error) {
	existing, err := e.store.FindUserByExternalID(ctx, info.ExternalID, provider)
	if err != nil {
		return nil, fmt.Errorf("查找用户失败: %w", err)
	}
	if existing != nil {
		return existing, nil
	}

	newUser := &User{
		ID:           generateRandomSecret(16),
		ExternalID:   info.ExternalID,
		AuthProvider: provider,
		DisplayName:  info.DisplayName,
		Email:        info.Email,
		AvatarURL:    info.AvatarURL,
		Department:   info.Department,
		Role:         "user",
		Status:       "active",
	}

	// Bootstrap admin: 第一个注册的用户自动成为 admin
	// BUG-6: 将 CountUsers + CreateUser 都放在 bootstrapMu 锁内，
	// 防止两个并发请求都通过 count==0 检查后各自创建 admin 用户。
	e.bootstrapMu.Lock()
	count, countErr := e.store.CountUsers(ctx)
	if countErr == nil && count == 0 {
		newUser.Role = "admin"
		e.logger.Info("首个用户自动设为 admin", zap.String("external_id", info.ExternalID))
	}
	createErr := e.store.CreateUser(ctx, newUser)
	e.bootstrapMu.Unlock()

	if createErr != nil {
		// 并发首次登录时 DB 唯一索引可能报冲突，幂等返回已创建的用户
		if isUniqueViolation(createErr) {
			existing, findErr := e.store.FindUserByExternalID(ctx, info.ExternalID, provider)
			if findErr != nil {
				return nil, fmt.Errorf("创建用户失败: %w", createErr)
			}
			if existing != nil {
				return existing, nil
			}
		}
		return nil, fmt.Errorf("创建用户失败: %w", createErr)
	}
	return newUser, nil
}

// GetUserByExternalIDAndProviderType 按外部 ID 和 provider type 查找用户
// IM 路径用此方法关联用户：platformToProvider 返回 type（feishu/dingtalk），
// 而 users.auth_provider 存的是 provider name，需要 JOIN auth_providers 表匹配。
// 不自动创建用户（用户必须先通过 Web 端登录注册）。
func (e *Engine) GetUserByExternalIDAndProviderType(ctx context.Context, externalID, providerType string) (*User, error) {
	return e.store.FindUserByExternalIDAndProviderType(ctx, externalID, providerType)
}

// GetUserByID 按 ID 获取用户
func (e *Engine) GetUserByID(ctx context.Context, userID string) (*User, error) {
	return e.store.GetUserByID(ctx, userID)
}

// GetUserByIDCached 优先查缓存，miss 时查 DB 并缓存 5 分钟
func (e *Engine) GetUserByIDCached(ctx context.Context, userID string) (*User, error) {
	if v, ok := e.userCache.Load(userID); ok {
		if cu := v.(*cachedUser); time.Now().Before(cu.expiresAt) {
			return cu.user, nil
		}
	}
	user, err := e.store.GetUserByID(ctx, userID)
	if err != nil {
		return nil, err
	}
	if user != nil {
		e.userCache.Store(userID, &cachedUser{user: user, expiresAt: time.Now().Add(5 * time.Minute)})
	}
	return user, nil
}

// InvalidateUserCache 主动清除用户缓存（admin disable/降级用户时调用）。
// handleUpdateUser (PATCH /api/v1/admin/users/:id) 修改 role/status 后会调用此方法。
func (e *Engine) InvalidateUserCache(userID string) {
	e.userCache.Delete(userID)
}

// OAuthLogin OAuth 登录流程
func (e *Engine) OAuthLogin(ctx context.Context, provider, code, ip, ua string) (string, *User, error) {
	p, ok := e.GetOAuthProvider(provider)
	if !ok {
		return "", nil, fmt.Errorf("provider %q 不存在", provider)
	}
	info, err := p.Exchange(ctx, code)
	if err != nil {
		return "", nil, fmt.Errorf("OAuth 换取用户信息失败: %w", err)
	}
	user, err := e.FindOrCreateUser(ctx, provider, info)
	if err != nil {
		return "", nil, err
	}
	if user.Status != "active" {
		return "", nil, fmt.Errorf("用户已被禁用")
	}
	if err := e.store.UpdateUserProfile(ctx, user.ID, info); err != nil {
		e.logger.Warn("更新用户资料失败", zap.Error(err))
	}
	if err := e.store.RecordLogin(ctx, &LoginRecord{
		UserID:       user.ID,
		AuthProvider: provider,
		IPAddress:    ip,
		UserAgent:    ua,
	}); err != nil {
		e.logger.Warn("记录登录历史失败", zap.Error(err))
	}
	if err := e.store.UpdateLoginInfo(ctx, user.ID, ip); err != nil {
		e.logger.Warn("更新登录信息失败", zap.Error(err))
	}
	token, err := e.jwt.Issue(user.ID, user.Role, provider)
	if err != nil {
		return "", nil, fmt.Errorf("签发 JWT 失败: %w", err)
	}
	return token, user, nil
}

// CredentialLogin 凭证登录流程（LDAP）
func (e *Engine) CredentialLogin(ctx context.Context, provider, username, password, ip, ua string) (string, *User, error) {
	// 暴力破解防护：15 分钟内同一 IP 失败超过 10 次则拒绝
	if err := e.checkLoginRateLimit(ip); err != nil {
		return "", nil, err
	}
	p, ok := e.GetCredentialProvider(provider)
	if !ok {
		return "", nil, fmt.Errorf("provider %q 不存在", provider)
	}
	info, err := p.Authenticate(ctx, username, password)
	if err != nil {
		e.recordLoginFailure(ip)
		return "", nil, fmt.Errorf("认证失败: %w", err)
	}
	user, err := e.FindOrCreateUser(ctx, provider, info)
	if err != nil {
		return "", nil, err
	}
	if user.Status != "active" {
		return "", nil, fmt.Errorf("用户已被禁用")
	}
	if err := e.store.UpdateUserProfile(ctx, user.ID, info); err != nil {
		e.logger.Warn("更新用户资料失败", zap.Error(err))
	}
	if err := e.store.RecordLogin(ctx, &LoginRecord{
		UserID:       user.ID,
		AuthProvider: provider,
		IPAddress:    ip,
		UserAgent:    ua,
	}); err != nil {
		e.logger.Warn("记录登录历史失败", zap.Error(err))
	}
	if err := e.store.UpdateLoginInfo(ctx, user.ID, ip); err != nil {
		e.logger.Warn("更新登录信息失败", zap.Error(err))
	}
	token, err := e.jwt.Issue(user.ID, user.Role, provider)
	if err != nil {
		return "", nil, fmt.Errorf("签发 JWT 失败: %w", err)
	}
	// 登录成功，清除失败计数
	e.resetLoginFailure(ip)
	return token, user, nil
}

// JWT 返回 JWTManager
func (e *Engine) JWT() *JWTManager {
	return e.jwt
}

// Store 返回底层 Store 接口（Admin API 使用）
func (e *Engine) Store() Store {
	return e.store
}

// isUniqueViolation 检查是否为 PostgreSQL 唯一约束冲突（错误码 23505）
func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "23505") || strings.Contains(err.Error(), "unique constraint")
}

// checkLoginRateLimit 检查 IP 登录失败次数，超过阈值返回 error
// 窗口：15 分钟内失败 10 次则锁定
func (e *Engine) checkLoginRateLimit(ip string) error {
	const maxAttempts = 10
	const window = 15 * time.Minute

	now := time.Now()
	v, _ := e.loginAttempts.LoadOrStore(ip, &loginAttemptRecord{resetAt: now.Add(window), lastSeen: now})
	rec := v.(*loginAttemptRecord)
	rec.mu.Lock()
	defer rec.mu.Unlock()
	rec.lastSeen = now
	if now.After(rec.resetAt) {
		rec.count = 0
		rec.resetAt = now.Add(window)
	}
	if rec.count >= maxAttempts {
		return fmt.Errorf("登录尝试过于频繁，请 15 分钟后再试")
	}
	return nil
}

// recordLoginFailure 记录登录失败
func (e *Engine) recordLoginFailure(ip string) {
	now := time.Now()
	v, _ := e.loginAttempts.LoadOrStore(ip, &loginAttemptRecord{resetAt: now.Add(15 * time.Minute), lastSeen: now})
	rec := v.(*loginAttemptRecord)
	rec.mu.Lock()
	rec.count++
	rec.lastSeen = now
	rec.mu.Unlock()
}

// resetLoginFailure 登录成功后清除失败计数
func (e *Engine) resetLoginFailure(ip string) {
	e.loginAttempts.Delete(ip)
}

// CheckQuota 检查用户配额，超限返回错误
func (e *Engine) CheckQuota(ctx context.Context, userID string) error {
	quota, err := e.store.ResetQuotaIfExpired(ctx, userID, time.Now())
	if err != nil {
		return err
	}
	if quota == nil {
		return nil // 无记录 = 无限制
	}
	if quota.TokenQuota > 0 && quota.TokenUsed >= quota.TokenQuota {
		return errs.New(errs.CodeQuotaExceeded, "本月 token 配额已用完，请联系管理员")
	}
	return nil
}

// IncrementTokenUsage 累加用户 token 使用量
func (e *Engine) IncrementTokenUsage(ctx context.Context, userID string, tokens int64) error {
	return e.store.IncrementTokenUsage(ctx, userID, tokens)
}

// degradedOAuthProvider NEW-3: 配置解析失败时的占位 provider，
// 保证内存 count 与 DB count 一致，避免 handleDeleteProvider 保护逻辑误判。
// 实际调用 Exchange/AuthCodeURL 时返回明确错误。
type degradedOAuthProvider struct {
	providerType string
	name         string
}

func (d *degradedOAuthProvider) Type() string { return d.providerType }
func (d *degradedOAuthProvider) AuthCodeURL(_ string) string {
	return ""
}
func (d *degradedOAuthProvider) Exchange(_ context.Context, _ string) (*UserInfo, error) {
	return nil, fmt.Errorf("provider %q 配置损坏，无法登录", d.name)
}

// degradedCredProvider NEW-3: 配置解析失败时的占位 credential provider。
type degradedCredProvider struct {
	providerType string
	name         string
}

func (d *degradedCredProvider) Type() string { return d.providerType }
func (d *degradedCredProvider) Authenticate(_ context.Context, _, _ string) (*UserInfo, error) {
	return nil, fmt.Errorf("provider %q 配置损坏，无法登录", d.name)
}
