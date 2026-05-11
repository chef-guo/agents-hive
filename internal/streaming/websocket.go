package streaming

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"go.uber.org/zap"

	"github.com/chef-guo/agents-hive/internal/auth"
	"github.com/chef-guo/agents-hive/internal/errs"
	"github.com/chef-guo/agents-hive/internal/imctx"
	"github.com/chef-guo/agents-hive/internal/master"
)

// WSMessage 是双向 WebSocket 消息信封。
type WSMessage struct {
	Type    string          `json:"type"` // "event","input_response","command","ping","pong","error"
	Payload json.RawMessage `json:"payload"`
}

// WSHandler 管理 WebSocket 连接的完整生命周期，包括实时双向通信。
type WSHandler struct {
	master         *master.Master
	logger         *zap.Logger
	insecureOrigin bool          // 为 true 时接受任意 Origin 的 WebSocket 连接
	allowedOrigins []string      // 允许的 Origin 列表（用于开发环境跨域）
	pingInterval   time.Duration // WebSocket ping 间隔，默认 30s

	// 认证配置
	token               string         // 静态认证 token（向后兼容）
	authEngine          *auth.Engine   // JWT 认证引擎（可选，nil 表示 auth 未启用）
	maxConnectionsPerIP int            // 单 IP 最大连接数
	ipConnections       map[string]int // IP 连接计数
	ipConnectionsMu     sync.Mutex     // IP 连接计数器锁
}

// NewWSHandler 创建新的 WSHandler。
func NewWSHandler(m *master.Master, logger *zap.Logger) *WSHandler {
	return &WSHandler{
		master:              m,
		logger:              logger,
		insecureOrigin:      false,
		maxConnectionsPerIP: 5,
		ipConnections:       make(map[string]int),
	}
}

// NewWSHandlerWithOptions 创建可配置的 WSHandler。
func NewWSHandlerWithOptions(m *master.Master, logger *zap.Logger, insecureOrigin bool, allowedOrigins ...string) *WSHandler {
	return &WSHandler{
		master:              m,
		logger:              logger,
		insecureOrigin:      insecureOrigin,
		allowedOrigins:      allowedOrigins,
		maxConnectionsPerIP: 5,
		ipConnections:       make(map[string]int),
	}
}

// SetAuthToken 设置 WebSocket 认证 token。
func (h *WSHandler) SetAuthToken(token string) {
	h.token = token
}

// SetAuthEngine 设置 JWT 认证引擎（auth 启用时调用）。
func (h *WSHandler) SetAuthEngine(engine *auth.Engine) {
	h.authEngine = engine
}

// SetMaxConnectionsPerIP 设置单 IP 最大连接数。
func (h *WSHandler) SetMaxConnectionsPerIP(max int) {
	if max > 0 {
		h.maxConnectionsPerIP = max
	}
}

// SetPingInterval 设置 WebSocket ping 间隔。
func (h *WSHandler) SetPingInterval(d time.Duration) {
	h.pingInterval = d
}

// buildAcceptOpts 构建 WebSocket Accept 选项（CORS/Origin 配置）。
func (h *WSHandler) buildAcceptOpts() *websocket.AcceptOptions {
	opts := &websocket.AcceptOptions{}
	if h.insecureOrigin {
		opts.InsecureSkipVerify = true
	} else if len(h.allowedOrigins) > 0 {
		opts.OriginPatterns = h.allowedOrigins
	}
	return opts
}

// HandleConnection 将 HTTP 请求升级为 WebSocket 并管理连接完整生命周期。
//
// goroutine 生命周期说明：
//   - writeLoop 以独立 goroutine 运行，通过 WaitGroup 追踪。
//   - readLoop 在当前 goroutine 中阻塞执行，负责读取客户端消息。
//   - 任意一方出错时均调用 cancel()，通知对方退出。
//   - readLoop 退出前通过 wg.Wait() 等待 writeLoop 完全结束，
//     确保连接关闭时不存在 goroutine 泄漏。
func (h *WSHandler) HandleConnection(w http.ResponseWriter, r *http.Request) {
	// 1. Static token 认证（仅在无 authEngine 时强制执行）
	// 有 authEngine 时，static token 作为可选的 OR 路径（CLI/外部集成可用），
	// 浏览器 JWT 路径在步骤 2 处理，两者任一通过即可。
	staticTokenPassed := false
	if h.token != "" {
		var receivedToken string
		var tokenSource string

		// 优先从 Authorization header 读取
		if auth := r.Header.Get("Authorization"); auth != "" {
			receivedToken = strings.TrimPrefix(auth, "Bearer ")
			tokenSource = "Authorization header"
		} else if proto := r.Header.Get("Sec-WebSocket-Protocol"); proto != "" {
			// 其次从 Sec-WebSocket-Protocol header 读取
			receivedToken = proto
			tokenSource = "Sec-WebSocket-Protocol header"
		} else if q := r.URL.Query().Get("token"); q != "" {
			// 最后从 URL query 读取（向后兼容，但不安全）
			receivedToken = q
			tokenSource = "URL query"
			h.logger.Warn("WebSocket token 通过 URL 参数传递，存在安全风险，建议使用 Authorization 或 Sec-WebSocket-Protocol header",
				zap.String("remote_addr", r.RemoteAddr),
			)
		}

		if receivedToken == h.token {
			staticTokenPassed = true
		} else if h.authEngine == nil {
			// 无 JWT 引擎时，static token 是唯一认证方式，不匹配则拒绝
			h.logger.Warn("WebSocket 连接被拒绝：无效 token",
				zap.String("remote_addr", r.RemoteAddr),
				zap.String("token_source", tokenSource),
			)
			http.Error(w, "Unauthorized: invalid or missing token", http.StatusUnauthorized)
			return
		}
	}

	// 2. JWT 认证（当 authEngine 可用时，从 Sec-WebSocket-Protocol 解析 bearer-{jwt}）
	var authenticatedUser *auth.User
	var selectedProtocol string
	if h.authEngine != nil {
		protoHeader := r.Header.Get("Sec-WebSocket-Protocol")
		for _, p := range strings.Split(protoHeader, ",") {
			p = strings.TrimSpace(p)
			if strings.HasPrefix(p, "bearer-") {
				jwtToken := strings.TrimPrefix(p, "bearer-")
				claims, err := h.authEngine.JWT().Verify(jwtToken)
				if err != nil {
					// JWT 无效：若 static token 已通过则放行（CLI/外部集成场景），否则拒绝
					if staticTokenPassed {
						break
					}
					h.logger.Warn("WebSocket JWT 验证失败",
						zap.String("remote_addr", r.RemoteAddr),
						zap.Error(err),
					)
					conn, acceptErr := websocket.Accept(w, r, h.buildAcceptOpts())
					if acceptErr == nil {
						conn.Close(4401, "invalid token")
					}
					return
				}
				user, _ := h.authEngine.GetUserByIDCached(r.Context(), claims.Subject)
				if user == nil || user.Status != "active" {
					// 用户不存在或已被禁用 → 拒绝连接（与 HTTP middleware 行为一致）
					closeCode := websocket.StatusCode(4401)
					closeReason := "user not found or disabled"
					if user != nil {
						closeCode = 4403
						closeReason = "forbidden"
					}
					conn, acceptErr := websocket.Accept(w, r, h.buildAcceptOpts())
					if acceptErr == nil {
						conn.Close(closeCode, closeReason)
					}
					return
				}
				authenticatedUser = user
			} else if p == "v1" {
				selectedProtocol = "v1"
			}
		}

		// authEngine 启用但既无有效 JWT 也无 static token → 拒绝
		if authenticatedUser == nil && !staticTokenPassed {
			http.Error(w, "Unauthorized: missing or invalid credentials", http.StatusUnauthorized)
			return
		}
	}

	// 3. 获取客户端 IP（仅在可信代理环境下读取 X-Forwarded-For/X-Real-IP）
	clientIP, _, _ := net.SplitHostPort(r.RemoteAddr)
	if clientIP == "" {
		clientIP = r.RemoteAddr
	}
	// 只有本地回环地址才信任代理头，防止外部伪造
	if clientIP == "127.0.0.1" || clientIP == "::1" {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			if idx := strings.Index(xff, ","); idx != -1 {
				clientIP = strings.TrimSpace(xff[:idx])
			} else {
				clientIP = strings.TrimSpace(xff)
			}
		} else if xri := r.Header.Get("X-Real-IP"); xri != "" {
			clientIP = strings.TrimSpace(xri)
		}
	}

	// 3. 连接数限制检查
	h.ipConnectionsMu.Lock()
	currentConnections := h.ipConnections[clientIP]
	if currentConnections >= h.maxConnectionsPerIP {
		h.ipConnectionsMu.Unlock()
		h.logger.Warn("WebSocket 连接被拒绝：超过 IP 连接数限制",
			zap.String("ip", clientIP),
			zap.Int("current", currentConnections),
			zap.Int("max", h.maxConnectionsPerIP),
		)
		http.Error(w, "Too many connections from this IP", http.StatusTooManyRequests)
		return
	}
	h.ipConnections[clientIP]++
	h.ipConnectionsMu.Unlock()

	// 4. 连接关闭时减少计数
	defer func() {
		h.ipConnectionsMu.Lock()
		h.ipConnections[clientIP]--
		if h.ipConnections[clientIP] <= 0 {
			delete(h.ipConnections, clientIP)
		}
		h.ipConnectionsMu.Unlock()
	}()

	// 5. WebSocket 升级
	opts := &websocket.AcceptOptions{}
	if h.insecureOrigin {
		opts.InsecureSkipVerify = true
	} else if len(h.allowedOrigins) > 0 {
		// 配置了 CORS 域名时，使用 OriginPatterns 允许跨域 WebSocket
		opts.OriginPatterns = h.allowedOrigins
	}
	// 回显 v1 protocol（不回显 bearer-{jwt}，防止 JWT 泄露到响应头）
	if selectedProtocol != "" {
		opts.Subprotocols = []string{selectedProtocol}
	}
	// 清除 HTTP Server 的 ReadTimeout 对 WebSocket 长连接的影响（防御性编程）
	rc := http.NewResponseController(w)
	rc.SetReadDeadline(time.Time{})
	rc.SetWriteDeadline(time.Time{})

	conn, err := websocket.Accept(w, r, opts)
	if err != nil {
		h.logger.Error("WebSocket 接受连接失败", zap.Error(err))
		return
	}

	h.logger.Info("WebSocket 连接已建立",
		zap.String("ip", clientIP),
		zap.Int("active_connections", currentConnections+1),
	)

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	// 如果有 JWT 认证用户，注入 context
	if authenticatedUser != nil {
		ctx = auth.WithUser(ctx, authenticatedUser)
	}

	// 提取当前连接的用户 session ID，用于广播过滤。
	// 前端连接时应传入 ?session_id=xxx，auth 启用后用于隔离不同用户的广播。
	userSessionID := r.URL.Query().Get("session_id")
	if h.authEngine != nil && userSessionID != "" {
		if authenticatedUser != nil {
			if err := h.authorizeSessionSubscription(ctx, userSessionID, authenticatedUser.ID); err != nil {
				h.logger.Warn("WebSocket 会话订阅被拒绝：无权访问",
					zap.String("remote_addr", r.RemoteAddr),
					zap.String("user_id", authenticatedUser.ID),
					zap.String("session_id", userSessionID),
					zap.Error(err),
				)
				conn.Close(4403, "forbidden")
				return
			}
		} else if !staticTokenPassed {
			conn.Close(4401, "missing user")
			return
		}
	}
	defer conn.Close(websocket.StatusNormalClosure, "closing")

	// 订阅 WebSocket 广播
	subID, broadcastCh := h.master.SubscribeWSBroadcast()
	defer h.master.UnsubscribeWSBroadcast(subID)

	// 用 WaitGroup 追踪 writeLoop goroutine，防止泄漏。
	// readLoop 退出前必须等待 writeLoop 完全结束，确保连接关闭时
	// 不存在残留 goroutine 继续向已关闭的连接写数据。
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		h.writeLoop(ctx, cancel, conn, broadcastCh, userSessionID)
	}()

	// readLoop 在当前 goroutine 中阻塞，包含 panic recover 以防异常泄漏 writeLoop。
	h.readLoop(ctx, conn, cancel)

	// readLoop 退出后，取消 context（此处 cancel 可能已被调用，重复调用无害）
	// 并等待 writeLoop goroutine 完全退出，避免 goroutine 泄漏。
	cancel()
	wg.Wait()
	h.logger.Debug("WebSocket 所有 goroutine 已退出，连接清理完成",
		zap.String("ip", clientIP),
	)
}

func (h *WSHandler) authorizeSessionSubscription(ctx context.Context, sessionID, userID string) error {
	if h.master == nil {
		return errs.New(errs.CodeInternal, "master 未初始化")
	}
	if sessionID == "" {
		return nil
	}
	if strings.HasPrefix(sessionID, imctx.SessionIDPrefix+"-") {
		return errs.New(errs.CodePermissionDenied, "IM 会话不开放给 WebSocket 订阅")
	}
	user := auth.UserFrom(ctx)
	if user == nil || user.ID != userID {
		return errs.New(errs.CodePermissionDenied, "未授权")
	}
	authCtx := auth.WithUser(auth.WithAuthEnabled(ctx), user)
	record, err := h.master.GetSessionByID(authCtx, sessionID)
	if err != nil {
		return err
	}
	if record == nil || record.UserID != userID {
		return errs.New(errs.CodePermissionDenied, "无权访问此会话")
	}
	return nil
}

// writeLoop 向 WebSocket 客户端发送 keepalive ping 和广播消息。
// 写入失败时调用 cancel() 通知 readLoop 退出。
// 包含 panic recover，确保异常不会导致 goroutine 静默消失而遗留资源。
func (h *WSHandler) writeLoop(ctx context.Context, cancel context.CancelFunc, conn *websocket.Conn, broadcastCh chan master.BroadcastMessage, userSessionID string) {
	// recover 防止 writeLoop 内部 panic 导致整个进程崩溃，
	// 同时确保 cancel() 被调用以通知 readLoop 退出。
	defer func() {
		if r := recover(); r != nil {
			h.logger.Error("writeLoop 发生 panic，正在恢复",
				zap.Any("panic", r),
			)
			cancel() // 通知 readLoop 退出
		}
	}()

	interval := h.pingInterval
	if interval == 0 {
		interval = 30 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			// 发送 ping keepalive
			msg := WSMessage{Type: "ping", Payload: json.RawMessage(`{}`)}
			data, err := json.Marshal(msg)
			if err != nil {
				h.logger.Error("ping 消息序列化失败", zap.Error(err))
				continue
			}
			if err := conn.Write(ctx, websocket.MessageText, data); err != nil {
				h.logger.Debug("WebSocket ping 失败，通知 readLoop 退出", zap.Error(err))
				cancel() // 写入失败，通知 readLoop 退出
				return
			}

		case broadcastMsg := <-broadcastCh:
			// 安全修复：按 session 隔离广播。
			// auth 启用时，若广播消息有 session 归属，必须与当前连接的 session 匹配才转发。
			// 注意：userSessionID 为空（未传 ?session_id）时也拒绝有归属的消息，
			// 防止客户端通过省略参数绕过过滤。
			//
			// 可见性补丁（im-streaming-reply 事后回归）：drop 时打 Debug 日志。
			// 历史教训：im-streaming-reply Sprint 12 把 react_processor 的广播
			// 从 BroadcastGenericMessage 迁到 BroadcastSessionMessage 后，未同步
			// 更新 AppShell.tsx 的 subscriber 契约（未传 ?session_id），导致前端
			// 流式渲染静默全量丢失。当时此处 continue 无日志，排障用了超过 17min。
			// 现要求：任何 session-mismatch drop 都可观测，下次类似盲区秒级暴露。
			if broadcastMsg.SessionID != "" {
				if userSessionID == "" || broadcastMsg.SessionID != userSessionID {
					h.logger.Debug("WebSocket session-mismatch drop",
						zap.String("broadcast_session", broadcastMsg.SessionID),
						zap.String("conn_session", userSessionID),
						zap.String("type", broadcastMsg.Type),
					)
					continue
				}
			}
			// 接收到广播消息，转发到 WebSocket 客户端
			payload, err := json.Marshal(broadcastMsg.Payload)
			if err != nil {
				h.logger.Error("广播消息序列化失败", zap.Error(err))
				continue
			}
			msg := WSMessage{Type: broadcastMsg.Type, Payload: payload}
			data, err := json.Marshal(msg)
			if err != nil {
				h.logger.Error("WebSocket 消息信封序列化失败", zap.Error(err))
				continue
			}
			if err := conn.Write(ctx, websocket.MessageText, data); err != nil {
				h.logger.Debug("WebSocket 广播消息发送失败，通知 readLoop 退出", zap.Error(err))
				cancel() // 写入失败，通知 readLoop 退出
				return
			}
			h.logger.Debug("WebSocket 广播消息已发送",
				zap.String("type", broadcastMsg.Type),
			)

		case <-ctx.Done():
			// context 已取消（readLoop 退出或连接关闭），正常退出
			return
		}
	}
}

// readLoop 读取 WebSocket 客户端消息并分发处理。
// 包含 panic recover，确保异常不会导致 writeLoop goroutine 泄漏。
func (h *WSHandler) readLoop(ctx context.Context, conn *websocket.Conn, cancel context.CancelFunc) {
	// recover 防止 readLoop 内部 panic 导致进程崩溃，
	// 并通过 cancel() 通知 writeLoop goroutine 安全退出。
	defer func() {
		if r := recover(); r != nil {
			h.logger.Error("readLoop 发生 panic，正在恢复",
				zap.Any("panic", r),
			)
		}
		cancel() // 无论正常/异常退出，均通知 writeLoop 退出
	}()

	// 限制单条消息最大 64KB，防止超大载荷导致 OOM
	conn.SetReadLimit(64 * 1024)

	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			if websocket.CloseStatus(err) == websocket.StatusNormalClosure ||
				websocket.CloseStatus(err) == websocket.StatusGoingAway {
				h.logger.Debug("WebSocket 正常关闭")
			} else {
				h.logger.Debug("WebSocket 读取错误", zap.Error(err))
			}
			return
		}

		var msg WSMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			h.sendError(ctx, conn, "invalid message format")
			continue
		}

		switch msg.Type {
		case "input_response":
			var resp master.InputResponse
			if err := json.Unmarshal(msg.Payload, &resp); err != nil {
				h.sendError(ctx, conn, "invalid input_response payload")
				continue
			}
			if err := h.master.SubmitInput(resp); err != nil {
				h.sendError(ctx, conn, err.Error())
			}

		case "command":
			var cmd master.UserCommand
			if err := json.Unmarshal(msg.Payload, &cmd); err != nil {
				h.sendError(ctx, conn, "invalid command payload")
				continue
			}
			if err := h.master.SendCommand(cmd); err != nil {
				h.sendError(ctx, conn, err.Error())
			}

		case "pong":
			// 客户端 pong 响应，无需处理

		default:
			h.sendError(ctx, conn, "unknown message type: "+msg.Type)
		}
	}
}

// sendError 向 WebSocket 客户端发送错误消息。
func (h *WSHandler) sendError(ctx context.Context, conn *websocket.Conn, message string) {
	payload, err := json.Marshal(map[string]string{"message": message})
	if err != nil {
		h.logger.Error("错误消息载荷序列化失败", zap.Error(err))
		return
	}
	msg := WSMessage{Type: "error", Payload: payload}
	data, err := json.Marshal(msg)
	if err != nil {
		h.logger.Error("错误消息信封序列化失败", zap.Error(err))
		return
	}
	if err := conn.Write(ctx, websocket.MessageText, data); err != nil {
		h.logger.Debug("WebSocket 发送错误消息失败", zap.Error(err))
	}
}
