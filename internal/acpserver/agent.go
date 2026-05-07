// Package acpserver 实现 ACP (Agent Client Protocol) 协议服务器
// 允许 Zed/JetBrains/Neovim/VS Code 等 IDE 零配置接入 agents-hive
package acpserver

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"sync"

	acp "github.com/coder/acp-go-sdk"
	"go.uber.org/zap"

	"github.com/chef-guo/agents-hive/internal/agentquality"
	"github.com/chef-guo/agents-hive/internal/command"
	"github.com/chef-guo/agents-hive/internal/config"
	"github.com/chef-guo/agents-hive/internal/errs"
	"github.com/chef-guo/agents-hive/internal/master"
	"github.com/chef-guo/agents-hive/internal/mcphost"
	"github.com/chef-guo/agents-hive/internal/toolctx"
	"github.com/chef-guo/agents-hive/internal/tools"
)

// sessionEntry 保存单个 ACP 会话的内部状态
type sessionEntry struct {
	masterSessionID string                     // 对应的 Master 会话 ID
	cancel          context.CancelFunc         // 用于取消当前 Prompt 请求
	mcpClients      []*mcphost.RemoteMCPClient // 会话级 MCP 客户端（需在会话结束时关闭）
}

// ClawAgent 实现 acp.Agent 接口，桥接 ACP 协议与 Master
type ClawAgent struct {
	master      *master.Master
	cfg         config.ACPServerConfig
	conn        *acp.AgentSideConnection
	logger      *zap.Logger
	cmdRegistry *command.Registry // 命令注册表（可为 nil）
	mcpHost     *mcphost.Host     // MCP 工具宿主（用于会话级 MCP 连接）

	mu       sync.Mutex
	sessions map[string]*sessionEntry // ACP sessionId -> sessionEntry
}

// 编译期接口合规检查
var _ acp.Agent = (*ClawAgent)(nil)

// NewClawAgent 创建 ACP Agent 实例
func NewClawAgent(m *master.Master, cfg config.ACPServerConfig, logger *zap.Logger, cmdRegistry *command.Registry, host *mcphost.Host) *ClawAgent {
	return &ClawAgent{
		master:      m,
		cfg:         cfg,
		logger:      logger,
		cmdRegistry: cmdRegistry,
		mcpHost:     host,
		sessions:    make(map[string]*sessionEntry),
	}
}

// SetAgentConnection 实现 acp.AgentConnAware，在 AgentSideConnection 创建后注入
func (a *ClawAgent) SetAgentConnection(conn *acp.AgentSideConnection) {
	a.conn = conn
}

// Initialize 处理客户端初始化请求，返回 Agent 能力声明
func (a *ClawAgent) Initialize(_ context.Context, _ acp.InitializeRequest) (acp.InitializeResponse, error) {
	a.logger.Info("ACP 客户端初始化")
	return acp.InitializeResponse{
		ProtocolVersion: acp.ProtocolVersionNumber,
		AgentCapabilities: acp.AgentCapabilities{
			LoadSession: false,
		},
	}, nil
}

// Authenticate 处理认证请求
// 支持 token 认证：配置了 AuthToken 时校验，未配置时允许所有连接（开发模式）
func (a *ClawAgent) Authenticate(_ context.Context, req acp.AuthenticateRequest) (acp.AuthenticateResponse, error) {
	if a.cfg.AuthToken == "" {
		a.logger.Warn("ACP 认证未配置 token，当前允许所有连接（仅限开发环境）")
		return acp.AuthenticateResponse{}, nil
	}

	token := extractToken(req.Meta)
	if token == a.cfg.AuthToken {
		return acp.AuthenticateResponse{}, nil
	}

	a.logger.Warn("ACP 认证失败：token 不匹配")
	return acp.AuthenticateResponse{}, errs.New(errs.CodeACPServerAuthFailed, "认证失败：token 无效")
}

// NewSession 创建新的 ACP 会话，同时在 Master 中建立对应会话
func (a *ClawAgent) NewSession(_ context.Context, params acp.NewSessionRequest) (acp.NewSessionResponse, error) {
	// 检查会话上限
	if a.cfg.MaxSessions > 0 {
		a.mu.Lock()
		count := len(a.sessions)
		a.mu.Unlock()
		if count >= a.cfg.MaxSessions {
			return acp.NewSessionResponse{}, errs.New(errs.CodeACPServerSessionLimit, "ACP 会话数已达上限")
		}
	}

	acpSID := generateSessionID()

	a.master.GetOrCreateSession(acpSID)

	a.mu.Lock()
	a.sessions[acpSID] = &sessionEntry{
		masterSessionID: acpSID,
	}
	a.mu.Unlock()

	// 连接会话级 MCP 服务端（如果客户端在 NewSessionRequest 中指定了）
	if len(params.McpServers) > 0 && a.mcpHost != nil {
		serverCfgs := convertACPMCPServers(params.McpServers)
		clients := connectSessionMCPServers(context.Background(), a.mcpHost, serverCfgs, a.logger)
		if len(clients) > 0 {
			a.mu.Lock()
			a.sessions[acpSID].mcpClients = clients
			a.mu.Unlock()
			a.logger.Info("会话级 MCP 服务端已连接",
				zap.String("session_id", acpSID),
				zap.Int("数量", len(clients)))
		}
	}

	// 将 ACP 权限桥接函数注入 Master（会话级，避免多会话权限路由到错误连接）
	if a.conn != nil {
		permFn := createACPPermissionFn(a.conn, acpSID, a.logger, a.master)
		a.master.SetSessionPermissionFn(acpSID, permFn)
		a.logger.Debug("ACP 权限桥接函数已注入 Master（会话级）",
			zap.String("session_id", acpSID))
	}

	a.logger.Info("ACP 新会话已创建",
		zap.String("acp_session_id", acpSID))
	a.master.RecordDelegation(context.Background(), tools.DelegationEvent{
		SessionID: acpSID,
		AgentType: "acp",
		Status:    "started",
	})

	resp := acp.NewSessionResponse{SessionId: acp.SessionId(acpSID)}

	// 向客户端发送可用命令列表
	if a.conn != nil {
		cmds := buildSlashCommands(a.cmdRegistry)
		if len(cmds) > 0 {
			_ = a.conn.SessionUpdate(context.Background(), acp.SessionNotification{
				SessionId: acp.SessionId(acpSID),
				Update: acp.SessionUpdate{
					AvailableCommandsUpdate: &acp.SessionAvailableCommandsUpdate{
						AvailableCommands: cmds,
					},
				},
			})
			a.logger.Debug("ACP 可用命令已发送",
				zap.String("session_id", acpSID),
				zap.Int("命令数量", len(cmds)))
		}
	}

	return resp, nil
}

// SetSessionMode 处理模式切换请求（Profile 系统已移除，当前为空操作）
func (a *ClawAgent) SetSessionMode(_ context.Context, params acp.SetSessionModeRequest) (acp.SetSessionModeResponse, error) {
	a.logger.Warn("ACP 模式切换已禁用（Profile 系统已移除）",
		zap.String("session_id", string(params.SessionId)),
		zap.String("mode_id", string(params.ModeId)))
	return acp.SetSessionModeResponse{}, nil
}

// Cancel 取消指定会话的当前请求
func (a *ClawAgent) Cancel(_ context.Context, params acp.CancelNotification) error {
	sid := string(params.SessionId)
	a.mu.Lock()
	entry, ok := a.sessions[sid]
	a.mu.Unlock()

	if ok && entry != nil && entry.cancel != nil {
		entry.cancel()
		a.logger.Info("ACP 会话请求已取消", zap.String("session_id", sid))
	}
	a.recordDelegation(context.Background(), sid, "failed", string(agentquality.FailureRuntime), string(acp.StopReasonCancelled), "")
	return nil
}

// Prompt 处理用户 Prompt 请求，转发给 Master 执行并流式返回结果
func (a *ClawAgent) Prompt(ctx context.Context, params acp.PromptRequest) (acp.PromptResponse, error) {
	sid := string(params.SessionId)

	// 取消上一轮请求
	a.mu.Lock()
	entry, ok := a.sessions[sid]
	if !ok {
		a.mu.Unlock()
		a.recordDelegation(ctx, sid, "failed", string(agentquality.FailureRuntime), "session_not_found", fmt.Sprintf("会话 %s 不存在", sid))
		return acp.PromptResponse{}, fmt.Errorf("会话 %s 不存在", sid)
	}
	if entry.cancel != nil {
		entry.cancel()
	}
	reqCtx, cancel := context.WithCancel(ctx)
	entry.cancel = cancel
	a.mu.Unlock()

	defer func() {
		a.mu.Lock()
		if e, ok := a.sessions[sid]; ok {
			e.cancel = nil
		}
		a.mu.Unlock()
		cancel()
	}()

	// 提取用户消息文本
	userText := extractPromptText(params)
	if userText == "" {
		a.recordDelegation(reqCtx, sid, "completed", "", string(acp.StopReasonEndTurn), "")
		return acp.PromptResponse{StopReason: acp.StopReasonEndTurn}, nil
	}

	// 检查是否为 slash 命令
	if handled, response := handleSlashCommand(userText, a.cmdRegistry); handled {
		if response != "" && a.conn != nil {
			_ = a.conn.SessionUpdate(reqCtx, acp.SessionNotification{
				SessionId: acp.SessionId(sid),
				Update:    acp.UpdateAgentMessageText(response),
			})
		}
		a.recordDelegation(reqCtx, sid, "completed", "", string(acp.StopReasonEndTurn), "")
		return acp.PromptResponse{StopReason: acp.StopReasonEndTurn}, nil
	}

	// 通知客户端：Agent 开始处理
	if a.conn != nil {
		if err := a.conn.SessionUpdate(reqCtx, acp.SessionNotification{
			SessionId: acp.SessionId(sid),
			Update:    acp.UpdateAgentMessageText(""),
		}); err != nil {
			if reqCtx.Err() != nil {
				a.recordDelegation(reqCtx, sid, "failed", string(agentquality.FailureRuntime), string(acp.StopReasonCancelled), err.Error())
				return acp.PromptResponse{StopReason: acp.StopReasonCancelled}, nil
			}
		}
	}

	// 启动实时流式推送 goroutine：订阅 EventBus 事件并转发给 ACP 客户端
	streamCtx, streamCancel := context.WithCancel(reqCtx)
	streamDone := make(chan struct{})
	if eb := a.master.GetEventBus(); eb != nil && a.conn != nil {
		go func() {
			defer close(streamDone)
			streamSessionUpdates(streamCtx, a.conn, eb, sid, a.logger)
		}()
	} else {
		close(streamDone)
	}

	// 委托给 Master 执行
	resp, err := a.master.ProcessMessage(reqCtx, sid, userText)

	// 停止流式推送并等待 goroutine 退出
	streamCancel()
	<-streamDone
	if reqCtx.Err() != nil {
		a.recordDelegation(reqCtx, sid, "failed", string(agentquality.FailureRuntime), string(acp.StopReasonCancelled), "")
		return acp.PromptResponse{StopReason: acp.StopReasonCancelled}, nil
	}
	if err != nil {
		a.logger.Error("Master 执行失败", zap.String("session_id", sid), zap.Error(err))
		a.recordDelegation(reqCtx, sid, "failed", string(agentquality.FailureRuntime), "error", err.Error())
		return acp.PromptResponse{}, err
	}

	// 将 Master 响应发送给客户端
	output := resp.Message
	if output == "" {
		output = resp.Content
	}
	if output == "" && resp.Error != "" {
		output = "错误: " + resp.Error
	}

	if output != "" {
		if a.conn != nil {
			if err := a.conn.SessionUpdate(reqCtx, acp.SessionNotification{
				SessionId: acp.SessionId(sid),
				Update:    acp.UpdateAgentMessageText(output),
			}); err != nil && reqCtx.Err() != nil {
				a.recordDelegation(reqCtx, sid, "failed", string(agentquality.FailureRuntime), string(acp.StopReasonCancelled), err.Error())
				return acp.PromptResponse{StopReason: acp.StopReasonCancelled}, nil
			}
		}
	}

	a.recordDelegation(reqCtx, sid, "completed", "", string(acp.StopReasonEndTurn), "")
	return acp.PromptResponse{StopReason: acp.StopReasonEndTurn}, nil
}

func (a *ClawAgent) recordDelegation(ctx context.Context, sessionID string, status string, failureType string, stopReason string, errText string) {
	if a.master == nil {
		return
	}
	tc := toolctx.GetToolContext(ctx)
	a.master.RecordDelegation(ctx, tools.DelegationEvent{
		SessionID:     sessionID,
		ParentTraceID: tc.TraceID,
		ChildTraceID:  tools.DeriveChildTraceID(tc.TraceID, sessionID),
		AgentType:     "acp",
		Status:        status,
		FailureType:   failureType,
		StopReason:    stopReason,
		Error:         errText,
	})
}

// extractPromptText 从 PromptRequest 中提取纯文本内容
func extractPromptText(params acp.PromptRequest) string {
	for _, block := range params.Prompt {
		if block.Text != nil && block.Text.Text != "" {
			return block.Text.Text
		}
	}
	return ""
}

// generateSessionID 生成随机会话 ID
func generateSessionID() string {
	var b [12]byte
	if _, err := io.ReadFull(rand.Reader, b[:]); err != nil {
		return fmt.Sprintf("acp_%d", randFallback())
	}
	return "acp_" + hex.EncodeToString(b[:])
}

// randFallback 当 crypto/rand 失败时的备用随机数源
func randFallback() int64 {
	var b [8]byte
	_, _ = io.ReadFull(rand.Reader, b[:])
	var n int64
	for i, v := range b {
		n |= int64(v) << (8 * i)
	}
	return n
}

// CloseSession 关闭指定 ACP 会话，释放会话级 MCP 客户端等资源
func (a *ClawAgent) CloseSession(sessionID string) {
	a.mu.Lock()
	entry, ok := a.sessions[sessionID]
	if ok {
		delete(a.sessions, sessionID)
	}
	a.mu.Unlock()

	if !ok || entry == nil {
		return
	}

	// 关闭会话级 MCP 客户端
	if len(entry.mcpClients) > 0 {
		closeSessionMCPClients(entry.mcpClients)
		a.logger.Info("会话级 MCP 客户端已关闭",
			zap.String("session_id", sessionID),
			zap.Int("数量", len(entry.mcpClients)))
	}

	// 清除会话级权限函数，防止后续请求路由到已关闭的连接
	a.master.ClearSessionPermissionFn(sessionID)
}

// CloseAllSessions 关闭所有 ACP 会话，释放全部资源
// 应在 ACP 服务器关闭时调用
func (a *ClawAgent) CloseAllSessions() {
	a.mu.Lock()
	sids := make([]string, 0, len(a.sessions))
	for sid := range a.sessions {
		sids = append(sids, sid)
	}
	a.mu.Unlock()

	for _, sid := range sids {
		a.CloseSession(sid)
	}
}

// convertACPMCPServers 将 ACP 协议的 McpServer 列表转换为内部 MCPServerConfig 映射
func convertACPMCPServers(servers []acp.McpServer) map[string]config.MCPServerConfig {
	result := make(map[string]config.MCPServerConfig, len(servers))
	for i, s := range servers {
		var cfg config.MCPServerConfig
		name := fmt.Sprintf("acp_mcp_%d", i)

		switch {
		case s.Stdio != nil:
			cfg.Command = s.Stdio.Command
			cfg.Args = s.Stdio.Args
			cfg.Transport = "stdio"
			if s.Stdio.Name != "" {
				name = s.Stdio.Name
			}
		case s.Sse != nil:
			cfg.URL = s.Sse.Url
			cfg.Transport = "sse"
			cfg.Headers = convertHTTPHeaders(s.Sse.Headers)
			if s.Sse.Name != "" {
				name = s.Sse.Name
			}
		case s.Http != nil:
			cfg.URL = s.Http.Url
			cfg.Transport = "http"
			cfg.Headers = convertHTTPHeaders(s.Http.Headers)
			if s.Http.Name != "" {
				name = s.Http.Name
			}
		default:
			continue
		}

		result[name] = cfg
	}
	return result
}

// convertHTTPHeaders 将 ACP HttpHeader 切片转换为 map
func convertHTTPHeaders(headers []acp.HttpHeader) map[string]string {
	if len(headers) == 0 {
		return nil
	}
	m := make(map[string]string, len(headers))
	for _, h := range headers {
		m[h.Name] = h.Value
	}
	return m
}

// extractToken 从 ACP Meta 字段中提取 token 字符串
// 支持 map[string]any 和 json.RawMessage 两种格式
func extractToken(meta any) string {
	// 常见格式：map[string]any{"token": "..."}
	if m, ok := meta.(map[string]any); ok {
		if v, exists := m["token"]; exists {
			return fmt.Sprintf("%v", v)
		}
		return ""
	}
	// 某些传输层会将 Meta 序列化为 json.RawMessage / []byte
	var raw []byte
	switch v := meta.(type) {
	case []byte:
		raw = v
	case json.RawMessage:
		raw = v
	case string:
		raw = []byte(v)
	default:
		return ""
	}
	var parsed map[string]any
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return ""
	}
	if v, exists := parsed["token"]; exists {
		return fmt.Sprintf("%v", v)
	}
	return ""
}
