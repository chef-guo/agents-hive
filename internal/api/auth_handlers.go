package api

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/chef-guo/agents-hive/internal/auth"
)

// handleAuthStatus 返回 auth 是否启用（公开端点，前端 AuthGuard 用）
func (s *Server) handleAuthStatus(w http.ResponseWriter, r *http.Request) {
	cfg := s.config.Auth
	resp := map[string]any{
		"enabled":                        s.authEngine != nil,
		"allow_public_registration":      cfg.AllowPublicRegistration,
		"invite_error_weak_distinction":  cfg.InviteErrorWeakDistinction,
		"has_local_register":             s.authEngine != nil,
	}
	writeJSON(w, http.StatusOK, resp)
}

// AuthErrorResponse 认证相关错误（含 error_code）。
type AuthErrorResponse struct {
	Error     string `json:"error"`
	ErrorCode string `json:"error_code,omitempty"`
	Code      int    `json:"code"`
}

// handleAuthRegister POST /api/v1/auth/register
func (s *Server) handleAuthRegister(w http.ResponseWriter, r *http.Request) {
	if s.authEngine == nil {
		http.Error(w, "auth not enabled", http.StatusNotFound)
		return
	}
	var req auth.RegisterRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, AuthErrorResponse{Error: "请求格式错误", ErrorCode: "invalid_request", Code: http.StatusBadRequest})
		return
	}
	policy := auth.RegisterPolicy{
		AllowPublicRegistration:    s.config.Auth.AllowPublicRegistration,
		InviteErrorWeakDistinction: s.config.Auth.InviteErrorWeakDistinction,
	}
	ip := realIP(r)
	token, user, err := s.authEngine.RegisterLocalUser(r.Context(), req, policy, ip)
	if err != nil {
		var re *auth.RegisterError
		if errors.As(err, &re) {
			writeJSON(w, re.HTTPStatus, AuthErrorResponse{Error: re.Message, ErrorCode: re.ErrorCode, Code: re.HTTPStatus})
			return
		}
		s.logger.Error("注册失败", zap.Error(err))
		writeJSON(w, http.StatusInternalServerError, AuthErrorResponse{Error: "注册失败", Code: http.StatusInternalServerError})
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"token": token, "user": user})
}

// handleListAuthProviders 返回已启用的 provider 列表（公开，登录页用）
func (s *Server) handleListAuthProviders(w http.ResponseWriter, r *http.Request) {
	if s.authEngine == nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{"providers": []interface{}{}})
		return
	}
	providers := s.authEngine.ListProviders()
	writeJSON(w, http.StatusOK, map[string]interface{}{"providers": providers})
}

// handleAuthLogin 生成 state 存 cookie，重定向到 provider.AuthCodeURL(state)
func (s *Server) handleAuthLogin(w http.ResponseWriter, r *http.Request) {
	if s.authEngine == nil {
		http.Error(w, "auth not enabled", http.StatusNotFound)
		return
	}
	providerName := r.URL.Query().Get("provider")
	if providerName == "" {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "缺少 provider 参数", Code: http.StatusBadRequest})
		return
	}
	p, ok := s.authEngine.GetOAuthProvider(providerName)
	if !ok {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "provider 不存在", Code: http.StatusNotFound})
		return
	}
	state := generateState()
	isProduction := r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"
	http.SetCookie(w, &http.Cookie{
		Name:     "oauth_state",
		Value:    state,
		Path:     "/api/v1/auth",
		MaxAge:   900, // 15 分钟
		HttpOnly: true,
		Secure:   isProduction,
		SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, p.AuthCodeURL(state), http.StatusFound)
}

// mapAuthError 将 OAuth 错误映射为前端可识别的错误码
func mapAuthError(err error) string {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "state"):
		return "state_mismatch"
	case strings.Contains(msg, "禁用"):
		return "user_disabled"
	case strings.Contains(msg, "频繁"):
		return "rate_limited"
	default:
		return "auth_failed"
	}
}

// handleAuthCallback 验证 state，调 provider.Exchange(code)，redirect 到前端 /auth/callback#token= 或 #error=
func (s *Server) handleAuthCallback(w http.ResponseWriter, r *http.Request) {
	if s.authEngine == nil {
		http.Error(w, "auth not enabled", http.StatusNotFound)
		return
	}
	providerName := r.URL.Query().Get("provider")
	code := r.URL.Query().Get("code")
	state := r.URL.Query().Get("state")

	frontendURL := s.config.Auth.FrontendURL // 空=相对路径（后端托管前端时）

	// 验证 state
	cookie, err := r.Cookie("oauth_state")
	if err != nil || cookie.Value != state || state == "" {
		s.logger.Warn("OAuth state 验证失败", zap.String("provider", providerName))
		http.Redirect(w, r, frontendURL+"/auth/callback#error=state_mismatch", http.StatusFound)
		return
	}
	// 清除 state cookie
	http.SetCookie(w, &http.Cookie{Name: "oauth_state", Value: "", MaxAge: -1, Path: "/api/v1/auth"})

	if code == "" {
		http.Redirect(w, r, frontendURL+"/auth/callback#error=auth_failed", http.StatusFound)
		return
	}

	ip := realIP(r)
	ua := r.UserAgent()
	token, user, err := s.authEngine.OAuthLogin(r.Context(), providerName, code, ip, ua)
	if err != nil {
		errCode := mapAuthError(err)
		s.logger.Warn("OAuth 登录失败",
			zap.String("provider", providerName),
			zap.String("error_code", errCode),
			zap.String("ip", ip),
			zap.Error(err),
		)
		http.Redirect(w, r, frontendURL+"/auth/callback#error="+errCode, http.StatusFound)
		return
	}
	s.logger.Info("OAuth 登录成功",
		zap.String("provider", providerName),
		zap.String("user_id", user.ID),
	)
	http.Redirect(w, r, frontendURL+"/auth/callback#token="+token, http.StatusFound)
}

// handleLDAPLogin 接收 {provider, username, password}，返回 JWT + user
func (s *Server) handleLDAPLogin(w http.ResponseWriter, r *http.Request) {
	if s.authEngine == nil {
		http.Error(w, "auth not enabled", http.StatusNotFound)
		return
	}
	var req struct {
		Provider string `json:"provider"`
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "请求格式错误", Code: http.StatusBadRequest})
		return
	}
	if req.Provider == "" {
		// 自动选第一个已注册的 LDAP credential provider，避免硬编码 provider 名称
		providers := s.authEngine.ListProviders()
		for _, p := range providers {
			if p.ProviderType == "ldap" {
				req.Provider = p.Name
				break
			}
		}
		if req.Provider == "" {
			req.Provider = "ldap" // 最终兜底（若没有任何 LDAP provider 注册则沿用旧行为）
		}
	}
	if req.Username == "" || req.Password == "" {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "用户名和密码不能为空", Code: http.StatusBadRequest})
		return
	}
	ip := realIP(r)
	ua := r.UserAgent()
	token, user, err := s.authEngine.CredentialLogin(r.Context(), req.Provider, req.Username, req.Password, ip, ua)
	if err != nil {
		s.logger.Warn("LDAP 登录失败", zap.String("provider", req.Provider), zap.Error(err))
		writeJSON(w, http.StatusUnauthorized, ErrorResponse{Error: "用户名或密码错误", Code: http.StatusUnauthorized})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"token": token,
		"user":  user,
	})
}

// handleGetCurrentUser 从 ctx 取 user（需认证），返回用户信息
func (s *Server) handleGetCurrentUser(w http.ResponseWriter, r *http.Request) {
	user := auth.UserFrom(r.Context())
	if user == nil {
		writeJSON(w, http.StatusUnauthorized, ErrorResponse{Error: "未授权", Code: http.StatusUnauthorized})
		return
	}
	writeJSON(w, http.StatusOK, user)
}

// handleRefreshToken 刷新 JWT（从 Authorization: Bearer <token> 读取）
func (s *Server) handleRefreshToken(w http.ResponseWriter, r *http.Request) {
	if s.authEngine == nil {
		http.Error(w, "auth not enabled", http.StatusNotFound)
		return
	}
	authHeader := r.Header.Get("Authorization")
	if !strings.HasPrefix(authHeader, "Bearer ") {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "缺少 token", Code: http.StatusBadRequest})
		return
	}
	tokenStr := strings.TrimPrefix(authHeader, "Bearer ")

	// 解析 token（跳过 exp 校验），提取 subject 用于检查用户状态
	// Refresh 流程允许已过期但未超过 maxTTL 的 token
	claims, err := s.authEngine.JWT().ParseSkipExpiry(tokenStr)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, ErrorResponse{Error: "token 无效，请重新登录", Code: http.StatusUnauthorized})
		return
	}
	// 检查用户当前状态：已被禁用则拒绝 refresh
	user, err := s.authEngine.GetUserByIDCached(r.Context(), claims.Subject)
	if err != nil || user == nil {
		writeJSON(w, http.StatusUnauthorized, ErrorResponse{Error: "token 无效或已过期，请重新登录", Code: http.StatusUnauthorized})
		return
	}
	if user.Status != "active" {
		writeJSON(w, http.StatusForbidden, ErrorResponse{Error: "用户已被禁用，无法刷新 token", Code: http.StatusForbidden})
		return
	}

	newToken, err := s.authEngine.JWT().Refresh(tokenStr)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, ErrorResponse{Error: "token 无效或已过期，请重新登录", Code: http.StatusUnauthorized})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"token": newToken})
}

// generateState 生成随机 OAuth state
func generateState() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return time.Now().Format("20060102150405")
	}
	return hex.EncodeToString(b)
}

// trustedProxyCIDRs 可信代理 CIDR 列表，从 TRUSTED_PROXY_CIDRS 环境变量读取。
// 未设置时默认信任 RFC1918 私有地址。
var trustedProxyCIDRs []*net.IPNet

func init() {
	cidrs := os.Getenv("TRUSTED_PROXY_CIDRS")
	if cidrs == "" {
		cidrs = "10.0.0.0/8,172.16.0.0/12,192.168.0.0/16,127.0.0.0/8"
	}
	for _, cidr := range strings.Split(cidrs, ",") {
		cidr = strings.TrimSpace(cidr)
		if cidr == "" {
			continue
		}
		_, ipNet, err := net.ParseCIDR(cidr)
		if err == nil {
			trustedProxyCIDRs = append(trustedProxyCIDRs, ipNet)
		}
	}
}

// realIP 返回真实客户端 IP。
// 只有当 RemoteAddr 属于可信代理范围时，才信任 X-Forwarded-For 的第一个 IP，
// 防止客户端伪造头绕过速率限制。
func realIP(r *http.Request) string {
	remoteHost, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		remoteHost = r.RemoteAddr
	}
	remoteIP := net.ParseIP(remoteHost)
	if remoteIP != nil {
		for _, cidr := range trustedProxyCIDRs {
			if cidr.Contains(remoteIP) {
				if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
					if idx := strings.Index(xff, ","); idx != -1 {
						xff = xff[:idx]
					}
					if ip := net.ParseIP(strings.TrimSpace(xff)); ip != nil {
						return ip.String()
					}
				}
				break
			}
		}
	}
	return remoteHost
}
