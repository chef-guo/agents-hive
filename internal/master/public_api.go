package master

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/chef-guo/agents-hive/internal/errs"
	"github.com/chef-guo/agents-hive/internal/imctx"
	"github.com/chef-guo/agents-hive/internal/mcphost"
	"github.com/chef-guo/agents-hive/internal/toolctx"
)

// MessageOption ProcessMessage 的可选参数
type MessageOption func(*SessionRequest)

// WithAttachments 设置附件
func WithAttachments(attachments []FileAttachment) MessageOption {
	return func(req *SessionRequest) {
		req.Attachments = attachments
	}
}

// WithReasoningEffort 设置推理努力级别
func WithReasoningEffort(effort string) MessageOption {
	return func(req *SessionRequest) {
		req.ReasoningEffort = effort
	}
}

// WithSkipUserMessage 跳过向 session 追加用户消息（regenerate 专用）
// regenerate 路径中用户消息已保留在 DB/内存，不需要重新写入
func WithSkipUserMessage() MessageOption {
	return func(req *SessionRequest) {
		req.SkipUserMessage = true
	}
}

// WithChannelMessageID 设置 IM 平台原消息 ID，供 input_received 事件透传。
// renderer 基于此 ID 在平台侧做 ack 表情（飞书 reactions / 钉钉 messageId）。
// 非 IM 通道（Web/CLI）不应设置；会作为空串广播，subscriber 端按空串跳过。
func WithChannelMessageID(id string) MessageOption {
	return func(req *SessionRequest) {
		req.ChannelMessageID = id
	}
}

func WithTurnID(id string) MessageOption {
	return func(req *SessionRequest) {
		req.TurnID = id
	}
}

func WithAckAlreadyEmitted() MessageOption {
	return func(req *SessionRequest) {
		req.AckAlreadyEmitted = true
	}
}

// WithIMContext 设置 IM 消息上下文（由 InboundContextResolver 解析得到）。
// nil 表示非飞书平台或 resolver degrade，不影响消息处理。
func WithIMContext(imCtx *imctx.IMMessageContext) MessageOption {
	return func(req *SessionRequest) {
		req.IMContext = imCtx
	}
}

func WithModelOverride(model string) MessageOption {
	return func(req *SessionRequest) {
		req.ModelOverride = model
	}
}

// ProcessMessage 向指定会话发送消息并等待响应（委托给 SessionManager）
// 实现 channel.MessageProcessor 接口
func (m *Master) ProcessMessage(ctx context.Context, sessionID string, input string) (TaskResponse, error) {
	return m.ProcessMessageWithOptions(ctx, sessionID, input)
}

// ProcessMessageFromIM 是 IM 通道专用入口，支持透传平台原消息 ID 和 IM 上下文。
// 实现 channel.IMMessageProcessor 接口——Router 从 InboundMessage.MessageID 取值传入，
// 由此经 SessionRequest.ChannelMessageID 写入 input_received 事件供 renderer 做 ack。
// channelMessageID == "" 时等价于 ProcessMessage（input_received 仍广播但 payload 中 ChannelMessageID 为空）。
// imCtx 为 nil 时表示非飞书平台或 resolver degrade，不影响消息处理。
func (m *Master) ProcessMessageFromIM(
	ctx context.Context,
	sessionID string,
	input string,
	channelMessageID string,
	modelOverride string,
	ackAlreadyEmitted bool,
	imCtx *imctx.IMMessageContext,
) (TaskResponse, error) {
	opts := []MessageOption{}
	if channelMessageID != "" {
		opts = append(opts, WithChannelMessageID(channelMessageID))
	}
	if ackAlreadyEmitted {
		opts = append(opts, WithAckAlreadyEmitted())
	}
	if modelOverride != "" {
		opts = append(opts, WithModelOverride(modelOverride))
	}
	if imCtx != nil {
		opts = append(opts, WithIMContext(imCtx))
	}
	return m.ProcessMessageWithOptions(ctx, sessionID, input, opts...)
}

// ProcessMessageWithOptions 向指定会话发送消息并等待响应（支持附件、推理努力级别等可选参数）
func (m *Master) ProcessMessageWithOptions(ctx context.Context, sessionID string, input string, opts ...MessageOption) (TaskResponse, error) {
	// Issue 3 fix: 先做权限检查
	if sessionID != "" {
		if session := m.sessionMgr.GetSession(sessionID); session != nil && session.IsTerminated() {
			return TaskResponse{}, errs.New(errs.CodeInvalidInput, "session terminated: "+sessionID)
		}
		if _, err := m.checkSessionAccess(ctx, sessionID); err != nil {
			return TaskResponse{}, err
		}
	}
	req := SessionRequest{
		Input:     input,
		SessionID: sessionID,
	}
	for _, opt := range opts {
		opt(&req)
	}

	// 权限检查通过后、进入 SessionManager 之前广播 input_received：
	// renderer 据此在 IM 侧做 ack 表情（飞书 GET/KEYBOARD 等），
	// 给用户"消息已受理"的即时反馈，覆盖 LLM 首 token 之前的静默窗口。
	// 仅当 sessionID 非空时广播（空 sessionID 的命令路径不经过此入口）。
	if req.SessionID != "" && !req.AckAlreadyEmitted {
		m.eventBus.BroadcastSessionMessage(req.SessionID, BroadcastMessage{
			Type:      EventTypeInputReceived,
			SessionID: req.SessionID,
			Payload: InputReceivedEvent{
				SessionID:        req.SessionID,
				ChannelMessageID: req.ChannelMessageID,
			},
		})
	}

	return m.sessionMgr.ProcessRequestWithResponse(ctx, req)
}

// ProcessCommand 向 SessionLoop 发送会话命令并等待响应（委托给 SessionManager）
func (m *Master) ProcessCommand(ctx context.Context, req SessionRequest) (TaskResponse, error) {
	return m.sessionMgr.ProcessRequestWithResponse(ctx, req)
}

func (m *Master) RestorePausedAfterResumeFailure(ctx context.Context, sessionID string, claimedPlanVersion int64, claimedRuntimeEpoch, claimedTurnID, message string, cause error) {
	m.restorePausedAfterResumeFailure(ctx, sessionID, claimedPlanVersion, claimedRuntimeEpoch, claimedTurnID, message, cause)
}

// SubmitInput 是提交用户响应的外部入口点（委托给 HITLBroker）
func (m *Master) SubmitInput(resp InputResponse) error {
	return m.hitlBroker.SubmitInput(resp)
}

// SendCommand 是发送用户控制命令的外部入口点（委托给 HITLBroker）
func (m *Master) SendCommand(cmd UserCommand) error {
	return m.hitlBroker.SendCommand(cmd)
}

// TerminateSession 终止指定会话上的运行任务，并等待该会话执行通道释放。
// 空 sessionID 或不存在的 session 视为 no-op，返回 nil。
func (m *Master) TerminateSession(sessionID, reason string) error {
	return m.terminateSession(sessionID, reason)
}

// PendingInputs 返回给定任务 ID 的待处理输入请求（委托给 HITLBroker）
func (m *Master) PendingInputs(taskID string) []*InputRequest {
	return m.hitlBroker.PendingInputs(taskID)
}

// HITLEnabled 返回 HITL 是否启用（委托给 HITLBroker）
func (m *Master) HITLEnabled() bool {
	return m.hitlBroker.Enabled()
}

// SetHITLEnabled 动态开关 HITL，并同步更新 permMgr 的 promptFn
func (m *Master) SetHITLEnabled(enabled bool) {
	m.hitlBroker.SetEnabled(enabled)
	if enabled {
		m.permMgr.SetPromptFn(m.createPermissionPromptFn())
	} else {
		m.permMgr.SetPromptFn(nil)
	}
}

// AskQuestion 实现 QuestionBridge 接口，供 question 工具使用
func (m *Master) AskQuestion(ctx context.Context, question string, options []string, timeout time.Duration) (string, error) {
	// 优先从 ctx 取当前 agent 真正跑的 session_id（react_processor.executeTool 通过 toolctx.WithSessionID 注入）。
	//
	// 历史 bug：旧实现只用 sessionMgr.GetActiveSessionID() —— 这是单用户旧设计遗留的全局变量，
	// 启动恢复 web 会话时被设进去后就一直留着。多租户 / IM 渠道场景下，飞书消息进来跑 agent
	// 调 question 工具，AskQuestion 拿到的是 web 用户的 active session id，InputRequest 的
	// task_id 路由到错误 session，feishu renderer 永远收不到 → 用户在飞书看不到审批卡片。
	// 实测日志: web session 807d5d44-... 抢走了飞书 session im-feishu-...-oc_xxx 的提问事件。
	taskID := toolctx.GetSessionID(ctx)
	if taskID == "" {
		taskID = m.sessionMgr.GetActiveSessionID()
	}
	if taskID == "" {
		taskID = "unknown"
	}

	// 创建输入请求
	req := m.hitlBroker.RequestInput(taskID, "", InputClarification, question, options)

	// 如果提供了自定义超时，覆盖默认值
	if timeout > 0 {
		req.Timeout = timeout
	}

	// 等待用户回答
	resp, err := m.hitlBroker.WaitForInput(ctx, taskID, req)
	if err != nil {
		return "", err
	}

	// 返回用户的回答
	if resp.Value != "" {
		return resp.Value, nil
	}
	return resp.Action, nil
}

// InvokeTool 直接调用 MCP 工具并返回结果文本（供预览 API 等内部使用，不经过权限审批）
// 调用方应自行限制可调用的工具范围（白名单）
func (m *Master) InvokeTool(ctx context.Context, toolName string, args json.RawMessage) (string, error) {
	if m.mcpHost == nil {
		return "", errors.New("MCP host not initialized")
	}
	result, err := m.mcpHost.ExecuteTool(ctx, toolName, args)
	if err != nil {
		return "", err
	}
	if result == nil {
		return "", nil
	}
	return mcphost.DecodeToolContent(result.Content), nil
}
