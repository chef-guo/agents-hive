package mcphost

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/chef-guo/agents-hive/internal/errs"
)

const defaultMCPProtocolVersion = "2024-11-05"

// HTTPTransportConfig StreamableHTTP 传输配置
type HTTPTransportConfig struct {
	URL          string                 // MCP 服务端 URL
	Headers      map[string]string      // 自定义 HTTP 头
	Timeout      time.Duration          // HTTP 请求超时，默认 30s
	AuthProvider func() (string, error) // 可选，返回 Authorization header 值
}

// HTTPTransport 使用标准 HTTP POST 请求与 MCP 服务端通信的传输层
// 支持 SSE 降级：如果服务端返回 text/event-stream content-type，自动按 SSE 解析响应
type HTTPTransport struct {
	cfg    HTTPTransportConfig
	logger *zap.Logger
	client *http.Client

	mu              sync.Mutex
	msgCh           chan json.RawMessage
	closed          bool
	sessionID       string
	protocolVersion string
	lastEventID     string
	listenCancel    context.CancelFunc
}

// NewHTTPTransport 创建 StreamableHTTP 传输实例
func NewHTTPTransport(cfg HTTPTransportConfig, logger *zap.Logger) *HTTPTransport {
	if cfg.Timeout <= 0 {
		cfg.Timeout = 30 * time.Second
	}
	return &HTTPTransport{
		cfg:    cfg,
		logger: logger,
		client: &http.Client{Timeout: cfg.Timeout},
		msgCh:  make(chan json.RawMessage, 64),
	}
}

// Connect 验证服务端可达性
func (t *HTTPTransport) Connect(ctx context.Context) error {
	return t.connect(ctx, true)
}

func (t *HTTPTransport) connect(ctx context.Context, enqueueInit bool) error {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return errs.New(errs.CodeMCPTransportClosed, "HTTP 传输已关闭")
	}
	t.mu.Unlock()

	// 发送 MCP initialize 请求（协议要求客户端先发起）。
	initMsg, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      0,
		"method":  "initialize",
		"params": map[string]any{
			"protocolVersion": defaultMCPProtocolVersion,
			"capabilities":    map[string]any{},
			"clientInfo": map[string]any{
				"name":    "agents-hive",
				"version": "1.0",
			},
		},
	})
	if err != nil {
		return errs.Wrap(errs.CodeMCPTransportFailed, "序列化 HTTP initialize 请求失败", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.cfg.URL, bytes.NewReader(initMsg))
	if err != nil {
		return errs.Wrap(errs.CodeMCPTransportFailed, "创建 HTTP 连接检测请求失败", err)
	}
	t.applyRequestHeaders(req, false)
	if err := t.applyAuth(req); err != nil {
		return err
	}
	t.logHTTPRequest("MCP HTTP 初始化请求", req)

	resp, err := t.client.Do(req)
	if err != nil {
		return errs.Wrap(errs.CodeMCPTransportFailed, "HTTP 连接检测失败", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		t.logger.Warn("MCP HTTP 初始化请求返回错误状态",
			zap.Int("status", resp.StatusCode),
			zap.String("url", safeURLForLog(t.cfg.URL)),
			zap.Strings("request_headers", safeHeaderKeys(req.Header)),
			zap.Bool("has_x_api_key", req.Header.Get("X-API-Key") != ""),
			zap.Bool("has_authorization", req.Header.Get("Authorization") != ""),
			zap.String("body", string(body)),
		)
		return errs.New(errs.CodeMCPTransportFailed, fmt.Sprintf("HTTP 连接检测返回 %d: %s", resp.StatusCode, string(body)))
	}

	if sessionID := resp.Header.Get("Mcp-Session-Id"); sessionID != "" {
		t.mu.Lock()
		t.sessionID = sessionID
		t.mu.Unlock()
		t.logger.Info("已保存 MCP HTTP 会话 ID", zap.String("url", safeURLForLog(t.cfg.URL)))
	}

	// 将初始化响应放入消息队列
	contentType := resp.Header.Get("Content-Type")
	if err := t.consumeResponse(ctx, resp.Body, contentType, enqueueInit); err != nil {
		return errs.Wrap(errs.CodeMCPTransportFailed, "读取初始化响应失败", err)
	}

	if enqueueInit {
		t.startListener(ctx)
	}

	t.logger.Info("HTTP 传输连接成功", zap.String("url", t.cfg.URL))
	return nil
}

// Send 通过 HTTP POST 发送 JSON-RPC 消息并读取响应
func (t *HTTPTransport) Send(ctx context.Context, msg json.RawMessage) error {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return errs.New(errs.CodeMCPTransportClosed, "HTTP 传输已关闭")
	}
	t.mu.Unlock()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.cfg.URL, bytes.NewReader(msg))
	if err != nil {
		return errs.Wrap(errs.CodeMCPTransportFailed, "创建 HTTP 请求失败", err)
	}
	t.applyRequestHeaders(req, true)
	if err := t.applyAuth(req); err != nil {
		return err
	}
	t.logHTTPRequest("MCP HTTP 请求", req)

	resp, err := t.client.Do(req)
	if err != nil {
		return errs.Wrap(errs.CodeMCPTransportFailed, "发送 HTTP 请求失败", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound && t.hasSession() {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		t.logger.Warn("MCP HTTP 会话失效，准备重新初始化",
			zap.String("url", safeURLForLog(t.cfg.URL)),
			zap.String("body", string(body)),
		)
		t.clearSession()
		if err := t.connect(ctx, false); err != nil {
			return errs.Wrap(errs.CodeMCPTransportFailed, "MCP HTTP 会话重新初始化失败", err)
		}
		return t.Send(ctx, msg)
	}

	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		t.logger.Warn("MCP HTTP 请求返回错误状态",
			zap.Int("status", resp.StatusCode),
			zap.String("url", safeURLForLog(t.cfg.URL)),
			zap.Strings("request_headers", safeHeaderKeys(req.Header)),
			zap.Bool("has_x_api_key", req.Header.Get("X-API-Key") != ""),
			zap.Bool("has_authorization", req.Header.Get("Authorization") != ""),
			zap.String("body", string(body)),
		)
		return errs.New(errs.CodeMCPTransportFailed, fmt.Sprintf("HTTP 请求返回 %d: %s", resp.StatusCode, string(body)))
	}

	contentType := resp.Header.Get("Content-Type")
	return t.consumeResponse(ctx, resp.Body, contentType, true)
}

// consumeResponse 消费 HTTP 响应体，支持 JSON 和 SSE 两种格式
func (t *HTTPTransport) consumeResponse(ctx context.Context, body io.Reader, contentType string, enqueue bool) error {
	if isSSEContentType(contentType) {
		// SSE 降级：服务端返回事件流
		t.logger.Debug("检测到 SSE 响应，使用 SSE 模式解析")
		return t.consumeSSEResponse(ctx, body, enqueue)
	}

	// 标准 JSON 响应
	data, err := io.ReadAll(io.LimitReader(body, 10*1024*1024)) // 限制 10MB
	if err != nil {
		return errs.Wrap(errs.CodeMCPTransportFailed, "读取 HTTP 响应失败", err)
	}

	if len(data) == 0 {
		return nil
	}

	// 检查是否为合法 JSON
	if !json.Valid(data) {
		return errs.New(errs.CodeMCPResponseInvalid, "HTTP 响应不是有效的 JSON")
	}

	t.captureProtocolVersion(data)
	if enqueue {
		select {
		case t.msgCh <- json.RawMessage(data):
		default:
			t.logger.Warn("HTTP 消息队列已满，丢弃响应")
		}
	}

	return nil
}

// consumeSSEResponse 解析 SSE 格式的响应体
func (t *HTTPTransport) consumeSSEResponse(ctx context.Context, body io.Reader, enqueue bool) error {
	scanner := bufio.NewScanner(body)
	var dataBuf strings.Builder
	var eventID string

	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return errs.Wrap(errs.CodeMCPTransportFailed, "读取 SSE 响应被取消", ctx.Err())
		default:
		}

		line := scanner.Text()

		if line == "" {
			// 空行表示事件结束
			if dataBuf.Len() > 0 {
				data := dataBuf.String()
				dataBuf.Reset()

				if json.Valid([]byte(data)) {
					t.captureProtocolVersion([]byte(data))
					if eventID != "" {
						t.setLastEventID(eventID)
						eventID = ""
					}
					if enqueue {
						select {
						case t.msgCh <- json.RawMessage(data):
						default:
							t.logger.Warn("HTTP-SSE 消息队列已满，丢弃消息")
						}
					}
				}
			}
			continue
		}

		if strings.HasPrefix(line, "data:") {
			d := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if dataBuf.Len() > 0 {
				dataBuf.WriteString("\n")
			}
			dataBuf.WriteString(d)
		} else if strings.HasPrefix(line, "id:") {
			eventID = strings.TrimSpace(strings.TrimPrefix(line, "id:"))
		}
		// 忽略 event: / retry: / 注释行
	}

	// 处理流结尾可能遗留的数据
	if dataBuf.Len() > 0 {
		data := dataBuf.String()
		if json.Valid([]byte(data)) {
			t.captureProtocolVersion([]byte(data))
			if eventID != "" {
				t.setLastEventID(eventID)
			}
			if enqueue {
				select {
				case t.msgCh <- json.RawMessage(data):
				default:
				}
			}
		}
	}

	return scanner.Err()
}

// Receive 接收 JSON-RPC 响应消息（阻塞）
func (t *HTTPTransport) Receive(ctx context.Context) (json.RawMessage, error) {
	select {
	case <-ctx.Done():
		return nil, errs.Wrap(errs.CodeMCPTransportFailed, "接收消息被取消", ctx.Err())
	case msg := <-t.msgCh:
		return msg, nil
	}
}

// applyAuth 如果配置了 AuthProvider，则设置 Authorization 头
func (t *HTTPTransport) applyAuth(req *http.Request) error {
	if t.cfg.AuthProvider != nil {
		authHeader, err := t.cfg.AuthProvider()
		if err != nil {
			return errs.Wrap(errs.CodeMCPOAuthFailed, "获取认证信息失败", err)
		}
		if authHeader != "" {
			req.Header.Set("Authorization", authHeader)
		}
	}
	return nil
}

// Close 关闭 HTTP 传输
func (t *HTTPTransport) Close() error {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return nil
	}
	t.closed = true
	sessionID := t.sessionID
	cancel := t.listenCancel
	t.listenCancel = nil
	t.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	if sessionID != "" {
		ctx, cancel := context.WithTimeout(context.Background(), t.cfg.Timeout)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, http.MethodDelete, t.cfg.URL, nil)
		if err == nil {
			t.applyRequestHeaders(req, true)
			if err := t.applyAuth(req); err == nil {
				resp, err := t.client.Do(req)
				if err != nil {
					t.logger.Warn("关闭 MCP HTTP 会话失败", zap.String("url", safeURLForLog(t.cfg.URL)), zap.Error(err))
				} else {
					_ = resp.Body.Close()
					t.logger.Info("MCP HTTP 会话已关闭",
						zap.String("url", safeURLForLog(t.cfg.URL)),
						zap.Int("status", resp.StatusCode),
					)
				}
			}
		}
	}

	t.logger.Info("HTTP 传输已关闭")
	return nil
}

// isSSEContentType 判断 Content-Type 是否为 SSE 事件流
func isSSEContentType(ct string) bool {
	return strings.Contains(ct, "text/event-stream")
}

func (t *HTTPTransport) logHTTPRequest(msg string, req *http.Request) {
	t.logger.Info(msg,
		zap.String("method", req.Method),
		zap.String("url", safeURLForLog(req.URL.String())),
		zap.Strings("headers", safeHeaderKeys(req.Header)),
		zap.Bool("has_x_api_key", req.Header.Get("X-API-Key") != ""),
		zap.Bool("has_authorization", req.Header.Get("Authorization") != ""),
		zap.Bool("has_mcp_session_id", req.Header.Get("Mcp-Session-Id") != ""),
	)
}

func (t *HTTPTransport) getSessionID() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.sessionID
}

func (t *HTTPTransport) hasSession() bool {
	return t.getSessionID() != ""
}

func (t *HTTPTransport) clearSession() {
	t.mu.Lock()
	if t.listenCancel != nil {
		t.listenCancel()
		t.listenCancel = nil
	}
	t.sessionID = ""
	t.lastEventID = ""
	t.mu.Unlock()
}

func (t *HTTPTransport) setLastEventID(id string) {
	t.mu.Lock()
	t.lastEventID = id
	t.mu.Unlock()
}

func (t *HTTPTransport) applyRequestHeaders(req *http.Request, includeSession bool) {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	for k, v := range t.cfg.Headers {
		req.Header.Set(k, v)
	}
	t.mu.Lock()
	protocolVersion := t.protocolVersion
	if protocolVersion == "" {
		protocolVersion = defaultMCPProtocolVersion
	}
	sessionID := t.sessionID
	lastEventID := t.lastEventID
	t.mu.Unlock()
	if includeSession {
		req.Header.Set("MCP-Protocol-Version", protocolVersion)
		if sessionID != "" {
			req.Header.Set("Mcp-Session-Id", sessionID)
		}
	}
	if lastEventID != "" && req.Method == http.MethodGet {
		req.Header.Set("Last-Event-ID", lastEventID)
	}
}

func (t *HTTPTransport) captureProtocolVersion(data []byte) {
	var resp struct {
		Result struct {
			ProtocolVersion string `json:"protocolVersion"`
		} `json:"result"`
	}
	if err := json.Unmarshal(data, &resp); err != nil || resp.Result.ProtocolVersion == "" {
		return
	}
	t.mu.Lock()
	t.protocolVersion = resp.Result.ProtocolVersion
	t.mu.Unlock()
}

func (t *HTTPTransport) startListener(ctx context.Context) {
	if t.getSessionID() == "" {
		return
	}
	t.mu.Lock()
	if t.listenCancel != nil {
		t.mu.Unlock()
		return
	}
	listenCtx, cancel := context.WithCancel(ctx)
	t.listenCancel = cancel
	t.mu.Unlock()

	go t.listenSSE(listenCtx)
}

func (t *HTTPTransport) listenSSE(ctx context.Context) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, t.cfg.URL, nil)
	if err != nil {
		t.logger.Warn("创建 MCP HTTP SSE 监听请求失败", zap.String("url", safeURLForLog(t.cfg.URL)), zap.Error(err))
		return
	}
	t.applyRequestHeaders(req, true)
	req.Header.Set("Accept", "text/event-stream")
	if err := t.applyAuth(req); err != nil {
		t.logger.Warn("应用 MCP HTTP SSE 监听认证失败", zap.String("url", safeURLForLog(t.cfg.URL)), zap.Error(err))
		return
	}
	t.logHTTPRequest("MCP HTTP SSE 监听请求", req)

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() == nil {
			t.logger.Debug("MCP HTTP SSE 监听请求失败", zap.String("url", safeURLForLog(t.cfg.URL)), zap.Error(err))
		}
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusMethodNotAllowed || resp.StatusCode == http.StatusNotFound {
		t.logger.Debug("MCP HTTP 服务端未启用 SSE 监听",
			zap.String("url", safeURLForLog(t.cfg.URL)),
			zap.Int("status", resp.StatusCode),
		)
		return
	}
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		t.logger.Warn("MCP HTTP SSE 监听返回错误状态",
			zap.String("url", safeURLForLog(t.cfg.URL)),
			zap.Int("status", resp.StatusCode),
			zap.String("body", string(body)),
		)
		return
	}
	if !isSSEContentType(resp.Header.Get("Content-Type")) {
		t.logger.Debug("MCP HTTP SSE 监听返回非事件流响应",
			zap.String("url", safeURLForLog(t.cfg.URL)),
			zap.String("content_type", resp.Header.Get("Content-Type")),
		)
		return
	}
	if err := t.consumeSSEResponse(ctx, resp.Body, true); err != nil && ctx.Err() == nil {
		t.logger.Warn("MCP HTTP SSE 监听读取失败", zap.String("url", safeURLForLog(t.cfg.URL)), zap.Error(err))
	}
}

func safeURLForLog(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "<invalid-url>"
	}
	u.RawQuery = ""
	u.Fragment = ""
	return u.String()
}

func safeHeaderKeys(h http.Header) []string {
	keys := make([]string, 0, len(h))
	for k := range h {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
