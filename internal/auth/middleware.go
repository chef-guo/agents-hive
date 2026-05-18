package auth

import (
	"context"
	"net/http"
	"path"
	"strings"
)

type contextKey int

const (
	contextKeyUser        contextKey = iota
	contextKeyAuthEnabled contextKey = iota
	contextKeyClaims      contextKey = iota
)

// publicPaths 公开路径白名单
// 带尾斜杠的条目用前缀匹配，不带尾斜杠的用精确匹配
var publicPaths = []string{
	"/api/v1/auth/providers",
	"/api/v1/auth/status",
	"/api/v1/auth/login",
	"/api/v1/auth/callback",
	"/api/v1/auth/refresh",
	"/api/v1/auth/register",
	"/api/v1/health",
	"/api/v1/assets/proxy",
	"/api/v1/channel/",
	"/api/v1/ws", // WebSocket: 浏览器无法设置 Authorization header，WS handler 自己验证 token
	"/assets/",
	"/favicon.ico",
	"/api/images/", // 生成图片的临时文件服务：<img> 标签无法携带 Authorization header
}

// publicExactPaths 需要精确匹配的根路径（SPA 入口）
var publicExactPaths = map[string]bool{
	"/":           true,
	"/index.html": true,
}

// AuthMiddleware 认证中间件
func AuthMiddleware(engine *Engine) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// 规范化路径，防路径穿越绕过白名单
			cleanPath := path.Clean(r.URL.Path)

			// 认证引擎未挂载（无 PG）：/api/v1 除健康/状态等外拒绝，避免业务 API 裸奔
			if engine == nil {
				if strings.HasPrefix(cleanPath, "/api/v1/") && !isAPIPublicWithoutEngine(cleanPath) {
					http.Error(w, `{"error":"认证服务未就绪，请检查数据库配置","code":"service_unavailable"}`, http.StatusServiceUnavailable)
					return
				}
				next.ServeHTTP(w, r)
				return
			}

			// auth 已启用：先标记，后续所有分支（含公开路径）都携带此标记
			ctx := WithAuthEnabled(r.Context())

			// Gateway RPC 需要同时支持 WebUI JWT 和 gateway.tokens 机器令牌。
			// 这里不直接拒绝无效/缺失 JWT，而是尽力注入 WebUI 用户后交给 Gateway 自己做 scope 判定；
			// 否则生产开启 auth 后，机器令牌会在到达 Gateway 前被误判为无效 JWT。
			if isGatewayRPCPath(cleanPath) {
				if user, claims, ok := authenticateBearerUser(r, engine); ok {
					ctx = WithUser(ctx, user)
					ctx = WithClaims(ctx, claims)
				}
				next.ServeHTTP(w, r.WithContext(ctx))
				return
			}

			// 公开路径放行：带尾斜杠用前缀匹配，不带尾斜杠用精确匹配
			if publicExactPaths[cleanPath] {
				next.ServeHTTP(w, r.WithContext(ctx))
				return
			}
			for _, p := range publicPaths {
				if strings.HasSuffix(p, "/") {
					if strings.HasPrefix(cleanPath, p) || cleanPath == strings.TrimSuffix(p, "/") {
						next.ServeHTTP(w, r.WithContext(ctx))
						return
					}
				} else {
					if cleanPath == p {
						next.ServeHTTP(w, r.WithContext(ctx))
						return
					}
				}
			}

			// 非 API 路径（SPA 路由）直接放行，由前端处理认证
			if !strings.HasPrefix(cleanPath, "/api/") {
				next.ServeHTTP(w, r.WithContext(ctx))
				return
			}

			// 提取 Bearer token
			user, claims, ok := authenticateBearerUser(r, engine)
			if !ok {
				http.Error(w, `{"error":"未授权","code":"unauthorized"}`, http.StatusUnauthorized)
				return
			}
			if user.Status != "active" {
				http.Error(w, `{"error":"用户已被禁用","code":"forbidden"}`, http.StatusForbidden)
				return
			}

			ctx = WithUser(ctx, user)
			ctx = WithClaims(ctx, claims)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func isGatewayRPCPath(cleanPath string) bool {
	return cleanPath == "/api/v1/rpc" || cleanPath == "/api/v1/rpc/ws"
}

func isAPIPublicWithoutEngine(cleanPath string) bool {
	switch cleanPath {
	case "/api/v1/health", "/api/v1/auth/status":
		return true
	default:
		return false
	}
}

func authenticateBearerUser(r *http.Request, engine *Engine) (*User, *Claims, bool) {
	authHeader := r.Header.Get("Authorization")
	if !strings.HasPrefix(authHeader, "Bearer ") {
		return nil, nil, false
	}
	tokenStr := strings.TrimPrefix(authHeader, "Bearer ")

	claims, err := engine.JWT().Verify(tokenStr)
	if err != nil {
		return nil, nil, false
	}

	user, err := engine.GetUserByIDCached(r.Context(), claims.Subject)
	if err != nil || user == nil {
		return nil, nil, false
	}
	return user, claims, true
}

// AdminOnly 仅允许 admin 角色访问
func AdminOnly(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user := UserFrom(r.Context())
		if user == nil || user.Role != "admin" {
			http.Error(w, `{"error":"需要管理员权限","code":"forbidden"}`, http.StatusForbidden)
			return
		}
		next(w, r)
	}
}

// WithUser 将用户注入 context
func WithUser(ctx context.Context, user *User) context.Context {
	return context.WithValue(ctx, contextKeyUser, user)
}

// UserFrom 从 context 提取用户
func UserFrom(ctx context.Context) *User {
	u, _ := ctx.Value(contextKeyUser).(*User)
	return u
}

// WithClaims 将 JWT claims 注入 context。
func WithClaims(ctx context.Context, claims *Claims) context.Context {
	return context.WithValue(ctx, contextKeyClaims, claims)
}

// ClaimsFrom 从 context 提取 JWT claims。
func ClaimsFrom(ctx context.Context) *Claims {
	claims, _ := ctx.Value(contextKeyClaims).(*Claims)
	return claims
}

// WithAuthEnabled 标记 auth 已启用
func WithAuthEnabled(ctx context.Context) context.Context {
	return context.WithValue(ctx, contextKeyAuthEnabled, true)
}

// IsAuthEnabled 检查 auth 是否已启用
func IsAuthEnabled(ctx context.Context) bool {
	v, _ := ctx.Value(contextKeyAuthEnabled).(bool)
	return v
}
