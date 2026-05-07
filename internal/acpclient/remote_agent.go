package acpclient

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	acp "github.com/coder/acp-go-sdk"
	"go.uber.org/zap"

	"github.com/chef-guo/agents-hive/internal/errs"
	"github.com/chef-guo/agents-hive/internal/subagent"
	"github.com/chef-guo/agents-hive/internal/tools"
)

type acpPromptClient interface {
	Prompt(context.Context, acp.PromptRequest) (acp.PromptResponse, error)
}

// RemoteACPAgent 将远程 ACP Agent 包装为本地 subagent.Agent
type RemoteACPAgent struct {
	cfg       RemoteAgentConfig
	conn      acpPromptClient
	done      <-chan struct{}
	transport *Transport
	sessionID acp.SessionId
	mailbox   *subagent.SubAgentMailbox
	logger    *zap.Logger
	observer  tools.DelegationObserver
	status    subagent.AgentStatus
	startTime time.Time
	outputs   map[acp.SessionId][]string
	mu        sync.RWMutex
}

// NewRemoteACPAgent 创建远程 ACP Agent 包装器
// 调用者需要先建立传输连接并完成 Initialize + NewSession
func NewRemoteACPAgent(
	cfg RemoteAgentConfig,
	conn *acp.ClientSideConnection,
	transport *Transport,
	sessionID acp.SessionId,
	logger *zap.Logger,
) *RemoteACPAgent {
	var done <-chan struct{}
	if conn != nil {
		done = conn.Done()
	}
	return &RemoteACPAgent{
		cfg:       cfg,
		conn:      conn,
		done:      done,
		transport: transport,
		sessionID: sessionID,
		mailbox:   subagent.NewMailbox(16),
		logger:    logger.With(zap.String("remote_agent", cfg.Name)),
		status:    subagent.StatusStopped,
		outputs:   make(map[acp.SessionId][]string),
	}
}

func NewRemoteACPAgentWithPromptClient(
	cfg RemoteAgentConfig,
	client acpPromptClient,
	sessionID acp.SessionId,
	logger *zap.Logger,
	observer tools.DelegationObserver,
) *RemoteACPAgent {
	a := NewRemoteACPAgent(cfg, nil, nil, sessionID, logger)
	a.conn = client
	a.observer = observer
	return a
}

// ID 返回 agent 的唯一标识
func (a *RemoteACPAgent) ID() string { return a.cfg.Name }

// Card 返回 agent 的描述信息
func (a *RemoteACPAgent) Card() subagent.AgentCard {
	return subagent.AgentCard{
		ID:          a.cfg.Name,
		Name:        a.cfg.Name,
		Description: a.cfg.Description,
		Skills:      a.cfg.Skills,
	}
}

// Mailbox 返回 agent 的通信邮箱
func (a *RemoteACPAgent) Mailbox() *subagent.SubAgentMailbox { return a.mailbox }

// Status 返回当前状态
func (a *RemoteACPAgent) Status() subagent.AgentStatus {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.status
}

func (a *RemoteACPAgent) setStatus(s subagent.AgentStatus) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.status = s
}

// Run 启动事件循环，监听 Mailbox 中的任务请求
func (a *RemoteACPAgent) Run(ctx context.Context) {
	a.mu.Lock()
	a.status = subagent.StatusRunning
	a.startTime = time.Now()
	a.mu.Unlock()
	a.logger.Info("远程 ACP Agent 已启动")

	defer func() {
		if r := recover(); r != nil {
			a.logger.Error("远程 ACP Agent 发生 panic", zap.Any("panic", r))
			a.setStatus(subagent.StatusError)
		}
	}()

	for {
		select {
		case env := <-a.mailbox.Request:
			a.logger.Debug("正在处理远程任务", zap.String("task_id", env.Req.ID))
			resp := a.handleTask(ctx, env.Req)
			resp.AgentID = a.cfg.Name
			resp.RequestID = env.Req.ID
			select {
			case env.ReplyCh <- resp:
			case <-ctx.Done():
				a.logger.Warn("发送响应时 context 已取消，丢弃响应", zap.String("task_id", env.Req.ID))
				a.setStatus(subagent.StatusStopped)
				return
			}

		case ping := <-a.mailbox.Health:
			ping.Reply <- a.healthStatus()

		case <-a.mailbox.Quit:
			a.logger.Info("远程 ACP Agent 正在停止（收到退出信号）")
			a.setStatus(subagent.StatusStopped)
			return

		case <-ctx.Done():
			a.logger.Info("远程 ACP Agent 正在停止（context 已取消）")
			a.setStatus(subagent.StatusStopped)
			return

		case <-a.done:
			a.logger.Warn("远程 ACP Agent 连接已断开")
			a.setStatus(subagent.StatusError)
			return
		}
	}
}

// handleTask 将 TaskRequest 转为 ACP Prompt 调用
func (a *RemoteACPAgent) handleTask(ctx context.Context, req subagent.TaskRequest) subagent.TaskResponse {
	a.resetSessionOutput(a.sessionID)

	// 提取 payload 中的指令
	payload, _ := subagent.ExtractPayload(req)

	var taskReq struct {
		Instruction string `json:"instruction"`
	}
	if err := json.Unmarshal(payload, &taskReq); err != nil {
		// payload 可能直接就是文本指令
		taskReq.Instruction = string(payload)
	}
	if taskReq.Instruction == "" {
		taskReq.Instruction = string(payload)
	}

	// 构建 ACP Prompt 请求
	promptReq := acp.PromptRequest{
		SessionId: a.sessionID,
		Prompt:    []acp.ContentBlock{acp.TextBlock(taskReq.Instruction)},
	}

	promptResp, err := a.conn.Prompt(ctx, promptReq)
	if err != nil {
		stopReason := "transport_error"
		if ctx.Err() != nil {
			stopReason = "timeout"
		}
		a.recordDelegation(ctx, req, "failed", "runtime", stopReason, err.Error())
		return subagent.TaskResponse{
			Status: "failed",
			Error:  fmt.Sprintf("ACP Prompt 调用失败: %v", err),
		}
	}

	content := a.sessionOutput(a.sessionID)
	if content == "" {
		content = promptResponseContent(promptResp)
	}

	// 将 PromptResponse 转为 TaskResponse
	result := map[string]interface{}{
		"stop_reason": string(promptResp.StopReason),
	}
	if content != "" {
		result["content"] = content
	}
	resultJSON, _ := json.Marshal(result)

	status := "completed"
	if promptResp.StopReason == acp.StopReasonCancelled || promptResp.StopReason == acp.StopReasonRefusal {
		status = "failed"
	}
	failureType := ""
	if status == "failed" {
		failureType = "runtime"
	}
	a.recordDelegation(ctx, req, status, failureType, string(promptResp.StopReason), "")

	return subagent.TaskResponse{
		Status: status,
		Result: resultJSON,
	}
}

func (a *RemoteACPAgent) handleSessionUpdate(notification acp.SessionNotification) {
	if notification.SessionId != a.sessionID {
		return
	}
	text := sessionUpdateText(notification.Update)
	if text == "" {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.outputs[notification.SessionId] = append(a.outputs[notification.SessionId], text)
}

func (a *RemoteACPAgent) resetSessionOutput(sessionID acp.SessionId) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.outputs, sessionID)
}

func (a *RemoteACPAgent) sessionOutput(sessionID acp.SessionId) string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return strings.Join(a.outputs[sessionID], "")
}

func sessionUpdateText(update acp.SessionUpdate) string {
	if update.AgentMessageChunk == nil || update.AgentMessageChunk.Content.Text == nil {
		return ""
	}
	return update.AgentMessageChunk.Content.Text.Text
}

func promptResponseContent(resp acp.PromptResponse) string {
	meta, ok := resp.Meta.(map[string]any)
	if !ok {
		return ""
	}
	for _, key := range []string{"content", "text", "assistant", "assistant_result"} {
		if value, ok := meta[key].(string); ok && value != "" {
			return value
		}
	}
	if result, ok := meta["result"].(map[string]any); ok {
		for _, key := range []string{"content", "text", "assistant", "assistant_result"} {
			if value, ok := result[key].(string); ok && value != "" {
				return value
			}
		}
	}
	return ""
}

func (a *RemoteACPAgent) recordDelegation(ctx context.Context, req subagent.TaskRequest, status string, failureType string, stopReason string, errText string) {
	if a.observer == nil {
		return
	}
	childTraceID := req.TraceID
	if childTraceID == "" {
		childTraceID = tools.DeriveChildTraceID(req.ParentTraceID, a.ID())
	}
	a.observer.RecordDelegation(ctx, tools.DelegationEvent{
		SessionID:     req.SessionID,
		ParentTraceID: req.ParentTraceID,
		ChildTraceID:  childTraceID,
		AgentID:       a.cfg.Name,
		AgentType:     "acp",
		Status:        status,
		FailureType:   failureType,
		StopReason:    stopReason,
		Error:         errText,
	})
}

// SendTask 向远程 Agent 发送任务并等待响应
func (a *RemoteACPAgent) SendTask(ctx context.Context, req subagent.TaskRequest) (subagent.TaskResponse, error) {
	if a.Status() != subagent.StatusRunning {
		return subagent.TaskResponse{}, errs.New(errs.CodeAgentUnavailable,
			fmt.Sprintf("远程 agent %s 未运行", a.cfg.Name))
	}

	replyCh := make(chan subagent.TaskResponse, 1)
	env := subagent.NewTaskEnvelope(req, replyCh)

	select {
	case a.mailbox.Request <- env:
	case <-ctx.Done():
		return subagent.TaskResponse{}, errs.Wrap(errs.CodeAgentTimeout, "发送任务超时", ctx.Err())
	}

	select {
	case resp := <-replyCh:
		return resp, nil
	case <-ctx.Done():
		return subagent.TaskResponse{}, errs.Wrap(errs.CodeAgentTimeout, "等待响应超时", ctx.Err())
	}
}

// Ping 健康检查
func (a *RemoteACPAgent) Ping(ctx context.Context) (subagent.HealthStatus, error) {
	reply := make(chan subagent.HealthStatus, 1)
	select {
	case a.mailbox.Health <- subagent.HealthPing{Reply: reply}:
	case <-ctx.Done():
		return subagent.HealthStatus{}, errs.Wrap(errs.CodeAgentTimeout, "健康检查发送超时", ctx.Err())
	}

	select {
	case status := <-reply:
		return status, nil
	case <-ctx.Done():
		return subagent.HealthStatus{}, errs.Wrap(errs.CodeAgentTimeout, "健康检查响应超时", ctx.Err())
	}
}

func (a *RemoteACPAgent) healthStatus() subagent.HealthStatus {
	return subagent.HealthStatus{
		AgentID: a.cfg.Name,
		Status:  a.Status(),
		Uptime:  time.Since(a.startTime),
	}
}

// Stop 停止远程 Agent 并关闭传输连接
func (a *RemoteACPAgent) Stop() {
	select {
	case a.mailbox.Quit <- struct{}{}:
	default:
	}
	if a.transport != nil {
		a.transport.Close()
	}
}

// ConnectAndInit 连接远程 ACP Agent：建立传输、初始化协议、创建会话
func ConnectAndInit(ctx context.Context, cfg RemoteAgentConfig, logger *zap.Logger) (*RemoteACPAgent, error) {
	// 1. 建立传输连接
	transport, err := NewTransport(cfg)
	if err != nil {
		return nil, err
	}

	// 2. 创建 ACP Client 实现
	clientImpl := newACPClientImpl(cfg.Name, logger)

	// 3. 建立 ACP 客户端连接
	conn := acp.NewClientSideConnection(clientImpl, transport.Writer, transport.Reader)

	// 4. 初始化协议
	initCtx, initCancel := context.WithTimeout(ctx, 30*time.Second)
	defer initCancel()

	_, err = conn.Initialize(initCtx, acp.InitializeRequest{
		ProtocolVersion:    acp.ProtocolVersion(acp.ProtocolVersionNumber),
		ClientCapabilities: hiveACPClientCapabilities(),
		ClientInfo: &acp.Implementation{
			Name:    "agents-hive",
			Version: "1.0.0",
		},
	})
	if err != nil {
		transport.Close()
		return nil, errs.Wrap(errs.CodeACPClientConnFailed,
			fmt.Sprintf("初始化远程 ACP Agent %q 失败", cfg.Name), err)
	}

	// 5. 创建会话
	cwd, _ := os.Getwd()
	sessionResp, err := conn.NewSession(initCtx, acp.NewSessionRequest{
		Cwd:        cwd,
		McpServers: []acp.McpServer{},
	})
	if err != nil {
		transport.Close()
		return nil, errs.Wrap(errs.CodeACPClientConnFailed,
			fmt.Sprintf("创建远程 ACP Agent %q 会话失败", cfg.Name), err)
	}

	agent := NewRemoteACPAgent(cfg, conn, transport, sessionResp.SessionId, logger)
	clientImpl.onUpdate = agent.handleSessionUpdate

	logger.Info("远程 ACP Agent 连接成功",
		zap.String("name", cfg.Name),
		zap.String("transport", cfg.Transport),
		zap.String("session_id", string(sessionResp.SessionId)))

	return agent, nil
}

func hiveACPClientCapabilities() acp.ClientCapabilities {
	return acp.ClientCapabilities{
		Fs: acp.FileSystemCapability{
			ReadTextFile:  true,
			WriteTextFile: true,
		},
		Terminal: true,
	}
}
