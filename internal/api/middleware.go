package api

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/chef-guo/agents-hive/internal/auth"
	"github.com/chef-guo/agents-hive/internal/errs"
)

// requestIDKey 请求追踪 ID 的 context key
type requestIDKey struct{}

// generateRequestID 生成唯一的请求追踪 ID
func generateRequestID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		// 降级使用时间戳
		return fmt.Sprintf("req-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

// RequestIDFromContext 从 context 提取请求追踪 ID
func RequestIDFromContext(ctx context.Context) string {
	if id, ok := ctx.Value(requestIDKey{}).(string); ok {
		return id
	}
	return ""
}

func (s *Server) applyMiddleware(handler http.Handler) http.Handler {
	h := handler
	h = s.securityHeadersMiddleware(h)
	h = s.corsMiddleware(h)
	if s.authEngine != nil {
		h = auth.AuthMiddleware(s.authEngine)(h)
	}
	h = s.loggingMiddleware(h) // logging 在 auth 外层，能捕获 401/403
	h = s.recoveryMiddleware(h)
	h = s.tracingMiddleware(h) // 最外层，最先执行
	return h
}

func (s *Server) securityHeadersMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
		// CSP: 限制脚本来源为同源，阻止内联脚本（XSS 防护）
		// style-src 'unsafe-inline' 因为 tailwind 需要
		// script-src 'wasm-unsafe-eval' 因为 shiki 用 oniguruma WebAssembly 做语法高亮；该指令仅放通 WASM 编译，不开 JS eval
		w.Header().Set("Content-Security-Policy",
			"default-src 'self'; script-src 'self' 'wasm-unsafe-eval'; style-src 'self' 'unsafe-inline' https://fonts.googleapis.com https://fonts.cdnfonts.com; font-src 'self' https://fonts.gstatic.com https://fonts.cdnfonts.com data:; img-src 'self' data: https: http://localhost:* http://127.0.0.1:*; connect-src 'self'")
		next.ServeHTTP(w, r)
	})
}

// tracingMiddleware 为每个请求注入追踪 ID
func (s *Server) tracingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 优先使用客户端传入的 ID，否则生成新的
		requestID := r.Header.Get("X-Request-ID")
		if requestID == "" {
			requestID = generateRequestID()
		}

		// 设置响应头
		w.Header().Set("X-Request-ID", requestID)

		// 注入 context
		ctx := context.WithValue(r.Context(), requestIDKey{}, requestID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (s *Server) loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		wrapped := &responseWriter{ResponseWriter: w, statusCode: http.StatusOK}
		next.ServeHTTP(wrapped, r)
		s.logger.Info("请求",
			zap.String("request_id", RequestIDFromContext(r.Context())),
			zap.String("method", r.Method),
			zap.String("path", r.URL.Path),
			zap.Int("status", wrapped.statusCode),
			zap.Duration("duration", time.Since(start)),
		)
	})
}

func (s *Server) recoveryMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if err := recover(); err != nil {
				s.logger.Error("恢复 panic", zap.Any("error", err), zap.String("path", r.URL.Path))
				writeJSON(w, http.StatusInternalServerError, ErrorResponse{
					Error: "内部服务器错误",
					Code:  errs.CodeInternal,
				})
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// defaultDevPorts 未显式配置 CORS 时允许的开发端口白名单
var defaultDevPorts = []string{"3000", "5173", "18080"}

func (s *Server) corsMiddleware(next http.Handler) http.Handler {
	// 构建允许的来源集合
	allowedOrigins := make(map[string]bool)
	for _, o := range s.corsOrigins {
		allowedOrigins[strings.TrimRight(o, "/")] = true
	}
	// 如果未配置来源，仅允许特定的开发端口，而非任意 localhost 端口
	if len(allowedOrigins) == 0 {
		allowedOrigins["http://localhost:"+s.serverPort] = true
		allowedOrigins["http://127.0.0.1:"+s.serverPort] = true
		for _, port := range defaultDevPorts {
			allowedOrigins["http://localhost:"+port] = true
			allowedOrigins["http://127.0.0.1:"+port] = true
		}
		s.logger.Warn("CORS 未显式配置，仅允许本地开发端口，生产环境请通过 server.cors_origins 显式配置允许的来源")
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" && allowedOrigins[origin] {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Vary", "Origin")
		}
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PATCH, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		next.ServeHTTP(w, r)
	})
}

type responseWriter struct {
	http.ResponseWriter
	statusCode int
}

// Unwrap 返回底层 ResponseWriter，使 http.ResponseController 和 coder/websocket 能正确穿透中间件
func (rw *responseWriter) Unwrap() http.ResponseWriter {
	return rw.ResponseWriter
}

func (rw *responseWriter) WriteHeader(code int) {
	rw.statusCode = code
	rw.ResponseWriter.WriteHeader(code)
}

func (rw *responseWriter) Flush() {
	if f, ok := rw.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Hijack 实现 http.Hijacker 接口，WebSocket 升级需要此方法获取底层 TCP 连接
func (rw *responseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if h, ok := rw.ResponseWriter.(http.Hijacker); ok {
		return h.Hijack()
	}
	return nil, nil, errs.New(errs.CodeInvalidRequest, "底层 ResponseWriter 不支持 Hijack")
}
