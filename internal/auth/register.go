package auth

import (
	"context"
	"fmt"
	"strings"
	"time"

	"go.uber.org/zap"
)

// RegisterPolicy 注册策略（来自配置）。
type RegisterPolicy struct {
	AllowPublicRegistration    bool
	InviteErrorWeakDistinction bool
}

// RegisterRequest 注册请求。
type RegisterRequest struct {
	Email       string `json:"email"`
	Username    string `json:"username"`
	Password    string `json:"password"`
	DisplayName string `json:"display_name"`
	Role        string `json:"role"`
	InviteCode  string `json:"invite_code"`
}

// RegisterError 注册业务错误。
type RegisterError struct {
	HTTPStatus int
	ErrorCode  string
	Message    string
}

func (e *RegisterError) Error() string { return e.Message }

func loginFromRegister(req RegisterRequest) string {
	if v := NormalizeLocalLogin(req.Email); v != "" {
		return v
	}
	return NormalizeLocalLogin(req.Username)
}

func (e *Engine) checkRegisterRateLimit(ip string) error {
	return e.checkLoginRateLimit(ip) // 复用同一 IP 窗口
}

func (e *Engine) recordRegisterFailure(ip string) {
	e.recordLoginFailure(ip)
}

// RegisterLocalUser 本地注册（公开/持码）。
func (e *Engine) RegisterLocalUser(ctx context.Context, req RegisterRequest, policy RegisterPolicy, ip string) (string, *User, error) {
	if err := e.checkRegisterRateLimit(ip); err != nil {
		return "", nil, &RegisterError{HTTPStatus: 429, ErrorCode: "rate_limited", Message: "请求过于频繁，请稍后再试"}
	}

	login := loginFromRegister(req)
	if login == "" || req.Password == "" {
		return "", nil, &RegisterError{HTTPStatus: 400, ErrorCode: "invalid_request", Message: "邮箱/用户名与密码不能为空"}
	}
	if err := ValidateLocalPassword(req.Password); err != nil {
		return "", nil, &RegisterError{HTTPStatus: 400, ErrorCode: "invalid_request", Message: err.Error()}
	}

	inviteRaw := strings.TrimSpace(req.InviteCode)
	hasInvite := inviteRaw != ""

	role := strings.TrimSpace(req.Role)
	if role == "" {
		role = "user"
	}
	if role != "user" && role != "admin" {
		return "", nil, &RegisterError{HTTPStatus: 400, ErrorCode: "invalid_request", Message: "role 只能是 user 或 admin"}
	}

	if !hasInvite {
		if !policy.AllowPublicRegistration {
			return "", nil, &RegisterError{
				HTTPStatus: 403,
				ErrorCode:  "registration_closed",
				Message:    "当前不允许自助注册，请联系管理员",
			}
		}
		if role == "admin" {
			count, err := e.store.CountUsers(ctx)
			if err != nil {
				return "", nil, err
			}
			if count > 0 {
				return "", nil, &RegisterError{
					HTTPStatus: 400,
					ErrorCode:  "admin_requires_invite",
					Message:    "创建管理员账号需要有效邀请码",
				}
			}
		}
	} else {
		role = "" // 以邀请码 role 为准
	}

	existing, _, err := e.store.FindLocalUserByLogin(ctx, login)
	if err != nil {
		return "", nil, err
	}
	if existing != nil {
		return "", nil, &RegisterError{
			HTTPStatus: 409,
			ErrorCode:  "email_already_registered",
			Message:    "该邮箱已注册，请直接登录或联系管理员",
		}
	}

	passwordHash, err := HashPassword(req.Password)
	if err != nil {
		return "", nil, err
	}

	displayName := strings.TrimSpace(req.DisplayName)
	if displayName == "" {
		displayName = login
	}

	newUser := &User{
		ID:           generateRandomSecret(16),
		ExternalID:   login,
		AuthProvider: localProviderName,
		DisplayName:  displayName,
		Email:        login,
		Status:       "active",
		Role:         "user",
	}

	if hasInvite {
		lookup := inviteLookupKey(inviteRaw)
		invite, err := e.store.FindInviteByLookup(ctx, lookup)
		if err != nil {
			return "", nil, err
		}
		if invite == nil || !CheckPassword(invite.CodeHash, inviteRaw) {
			e.recordRegisterFailure(ip)
			return "", nil, e.inviteRegisterError(invite, inviteRaw, policy)
		}
		if invite.Disabled || invite.UseCount >= invite.MaxUses || !invite.ExpiresAt.After(time.Now()) {
			e.recordRegisterFailure(ip)
			return "", nil, e.inviteRegisterError(invite, inviteRaw, policy)
		}
		newUser.Role = invite.Role
		if err := e.store.RegisterUserWithInvite(ctx, newUser, passwordHash, invite.ID); err != nil {
			if isUniqueViolation(err) {
				return "", nil, &RegisterError{
					HTTPStatus: 409,
					ErrorCode:  "email_already_registered",
					Message:    "该邮箱已注册，请直接登录或联系管理员",
				}
			}
			e.recordRegisterFailure(ip)
			return "", nil, err
		}
	} else {
		e.bootstrapMu.Lock()
		count, countErr := e.store.CountUsers(ctx)
		if countErr == nil && count == 0 && role == "admin" {
			newUser.Role = "admin"
		} else if role == "admin" {
			e.bootstrapMu.Unlock()
			return "", nil, &RegisterError{
				HTTPStatus: 400,
				ErrorCode:  "admin_requires_invite",
				Message:    "创建管理员账号需要有效邀请码",
			}
		} else {
			newUser.Role = "user"
		}
		createErr := e.store.CreateUserWithPassword(ctx, newUser, passwordHash)
		e.bootstrapMu.Unlock()
		if createErr != nil {
			if isUniqueViolation(createErr) {
				return "", nil, &RegisterError{
					HTTPStatus: 409,
					ErrorCode:  "email_already_registered",
					Message:    "该邮箱已注册，请直接登录或联系管理员",
				}
			}
			e.recordRegisterFailure(ip)
			return "", nil, createErr
		}
	}

	token, user, err := e.issueTokenAfterRegister(ctx, newUser, ip, "")
	if err != nil {
		return "", nil, err
	}
	e.resetLoginFailure(ip)
	return token, user, nil
}

func (e *Engine) inviteRegisterError(invite *InviteCode, raw string, policy RegisterPolicy) *RegisterError {
	if policy.InviteErrorWeakDistinction && invite != nil && !invite.ExpiresAt.After(time.Now()) {
		return &RegisterError{
			HTTPStatus: 403,
			ErrorCode:  "invite_expired",
			Message:    "邀请码已过期，请联系管理员",
		}
	}
	_ = raw
	return &RegisterError{
		HTTPStatus: 403,
		ErrorCode:  "invite_invalid",
		Message:    "邀请码无效或已失效，请联系管理员",
	}
}

func (e *Engine) issueTokenAfterRegister(ctx context.Context, u *User, ip, ua string) (string, *User, error) {
	if err := e.store.RecordLogin(ctx, &LoginRecord{
		UserID: u.ID, AuthProvider: localProviderName, IPAddress: ip, UserAgent: ua,
	}); err != nil {
		e.logger.Warn("记录注册登录历史失败", zap.Error(err))
	}
	_ = e.store.UpdateLoginInfo(ctx, u.ID, ip)
	token, err := e.jwt.Issue(u.ID, u.Role, localProviderName)
	if err != nil {
		return "", nil, fmt.Errorf("签发 JWT 失败: %w", err)
	}
	fresh, err := e.store.GetUserByID(ctx, u.ID)
	if err != nil || fresh == nil {
		return token, u, nil
	}
	return token, fresh, nil
}
