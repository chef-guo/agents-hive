package subagent

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/chef-guo/agents-hive/internal/errs"
	"github.com/chef-guo/agents-hive/internal/skills"
)

// AgentStatus 表示 sub-agent 的当前状态
type AgentStatus int

const (
	StatusStopped AgentStatus = iota
	StatusRunning
	StatusError
)

func (s AgentStatus) String() string {
	switch s {
	case StatusStopped:
		return "stopped"
	case StatusRunning:
		return "running"
	case StatusError:
		return "error"
	default:
		return "unknown"
	}
}

// AgentCard 描述 sub-agent 的身份和能力
type AgentCard struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Skills      []string `json:"skills,omitempty"`
	// Dynamic 标记该 Agent 是否为动态创建（spawn_agent / fork）。
	// false = 固定 Agent（主路径），true = 动态补充 Agent。
	// 前端和事件系统可通过此字段区分 fixed vs dynamic。
	Dynamic bool `json:"dynamic,omitempty"`
}

// TaskRequest 是从 master 发送给 sub-agent 的任务
type TaskRequest struct {
	ID            string          `json:"id"`
	Type          string          `json:"type"`
	SessionID     string          `json:"session_id,omitempty"`      // 发起会话 ID（用于成本追踪等）
	UserID        string          `json:"user_id,omitempty"`         // 发起用户 ID（用于成本追踪等）
	TraceID       string          `json:"trace_id,omitempty"`        // 当前 child agent trace
	ParentSpanID  string          `json:"parent_span_id,omitempty"`  // 发起 delegation 的 tool span
	ParentTraceID string          `json:"parent_trace_id,omitempty"` // 发起 delegation 的 parent trace
	Payload       json.RawMessage `json:"payload"`
}

// DeriveChildTraceID 稳定派生子 agent trace，避免各委派入口拼接规则漂移。
func DeriveChildTraceID(parentTraceID, agentID string) string {
	if parentTraceID == "" {
		return agentID
	}
	if agentID == "" {
		return parentTraceID + ":unknown-agent"
	}
	return parentTraceID + ":" + agentID
}

// TaskResponse 是 sub-agent 返回给 master 的结果
type TaskResponse struct {
	RequestID string          `json:"request_id"`
	AgentID   string          `json:"agent_id"`
	Status    string          `json:"status"` // "completed", "failed", "partial"
	Result    json.RawMessage `json:"result,omitempty"`
	Error     string          `json:"error,omitempty"`
}

// HealthPing 用于健康检查
type HealthPing struct {
	Reply chan HealthStatus
}

// HealthStatus 是对健康检查的响应
type HealthStatus struct {
	AgentID string        `json:"agent_id"`
	Status  AgentStatus   `json:"status"`
	Uptime  time.Duration `json:"uptime"`
}

// HumanInputExchange 携带从 sub-agent 到 master 的问题以及
// master 返回给 sub-agent 的回复
type HumanInputExchange struct {
	Question InputQuestion
	Reply    chan string // buffered(1)
}

// InputQuestion 是 sub-agent 请求人工澄清的请求
type InputQuestion struct {
	AgentID string `json:"agent_id"`
	Prompt  string `json:"prompt"`
	Type    string `json:"type"` // "clarification", "approval"
}

// SubAgentMailbox 为 agent 通信提供类型化的 channel
type SubAgentMailbox struct {
	Request    chan TaskEnvelope // 使用 envelope 携带 per-request reply channel，支持并发安全
	Health     chan HealthPing
	Quit       chan struct{}
	HumanInput chan HumanInputExchange // 当 HITL 禁用时为 nil
}

// NewMailbox 创建一个带缓冲 channel 的新邮箱
// 如果 hitlEnabled 为 true，则也会创建 HumanInput channel
func NewMailbox(bufSize int, hitlEnabled ...bool) *SubAgentMailbox {
	m := &SubAgentMailbox{
		Request: make(chan TaskEnvelope, bufSize),
		Health:  make(chan HealthPing, 1),
		Quit:    make(chan struct{}, 1), // buffered(1)，确保 Stop 信号不因 agent 忙碌而丢失
	}
	if len(hitlEnabled) > 0 && hitlEnabled[0] {
		m.HumanInput = make(chan HumanInputExchange, 4)
	}
	return m
}

// TaskEnvelope 将任务请求和 per-request reply channel 打包在一起，
// 解决多个 goroutine 并发调用 SendTask 时响应被错误方读走的问题。
type TaskEnvelope struct {
	Req     TaskRequest
	ReplyCh chan TaskResponse // buffered(1)，由 SendTask 创建
}

// NewTaskEnvelope 创建一个新的 TaskEnvelope
func NewTaskEnvelope(req TaskRequest, replyCh chan TaskResponse) TaskEnvelope {
	return TaskEnvelope{Req: req, ReplyCh: replyCh}
}

// TaskHandler 处理任务请求并返回响应
type TaskHandler func(ctx context.Context, req TaskRequest) TaskResponse

// BaseAgent 为 sub-agent 提供生命周期管理和 channel 通信
type BaseAgent struct {
	card      AgentCard
	mailbox   *SubAgentMailbox
	skills    *skills.Registry
	logger    *zap.Logger
	handler   TaskHandler
	status    AgentStatus
	startTime time.Time
	mu        sync.RWMutex
}

// NewBaseAgent 创建一个新的 BaseAgent
func NewBaseAgent(card AgentCard, handler TaskHandler, skillReg *skills.Registry, logger *zap.Logger) *BaseAgent {
	return &BaseAgent{
		card:    card,
		mailbox: NewMailbox(16),
		skills:  skillReg,
		logger:  logger.With(zap.String("agent", card.ID)),
		handler: handler,
		status:  StatusStopped,
	}
}

// ID 返回 agent 的 ID
func (a *BaseAgent) ID() string { return a.card.ID }

// Card 返回 agent 的卡片
func (a *BaseAgent) Card() AgentCard { return a.card }

// Mailbox 返回 agent 的邮箱
func (a *BaseAgent) Mailbox() *SubAgentMailbox { return a.mailbox }

// Status 返回当前 agent 状态
func (a *BaseAgent) Status() AgentStatus {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.status
}

func (a *BaseAgent) setStatus(s AgentStatus) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.status = s
}

// Run 在 goroutine 中启动 agent 的事件循环
func (a *BaseAgent) Run(ctx context.Context) {
	a.mu.Lock()
	a.status = StatusRunning
	a.startTime = time.Now()
	a.mu.Unlock()
	a.logger.Info("agent 已启动")

	defer func() {
		if r := recover(); r != nil {
			a.logger.Error("agent 发生 panic", zap.Any("panic", r))
			a.setStatus(StatusError)
		}
	}()

	for {
		select {
		case env := <-a.mailbox.Request:
			a.logger.Debug("正在处理任务", zap.String("task_id", env.Req.ID), zap.String("type", env.Req.Type))
			resp := a.handler(ctx, env.Req)
			resp.AgentID = a.card.ID
			resp.RequestID = env.Req.ID
			// 通过 per-request reply channel 回传响应，避免并发调用时响应被错误方读走
			select {
			case env.ReplyCh <- resp:
			case <-ctx.Done():
				a.logger.Warn("发送响应时 context 已取消，丢弃响应",
					zap.String("task_id", env.Req.ID))
				// 向 replyCh 发送 error 响应，避免 SendTask 调用方永远等待
				select {
				case env.ReplyCh <- TaskResponse{Status: "failed", Error: "context canceled", AgentID: a.card.ID, RequestID: env.Req.ID}:
				default:
				}
				a.setStatus(StatusStopped)
				return
			case <-a.mailbox.Quit:
				a.logger.Info("发送响应时收到退出信号")
				// 向 replyCh 发送 error 响应，避免 SendTask 调用方永远等待
				select {
				case env.ReplyCh <- TaskResponse{Status: "failed", Error: "agent quit", AgentID: a.card.ID, RequestID: env.Req.ID}:
				default:
				}
				a.setStatus(StatusStopped)
				return
			}

		case ping := <-a.mailbox.Health:
			ping.Reply <- a.healthStatus()

		case <-a.mailbox.Quit:
			a.logger.Info("agent 正在停止（收到退出信号）")
			a.setStatus(StatusStopped)
			return

		case <-ctx.Done():
			a.logger.Info("agent 正在停止（context 已取消）")
			a.setStatus(StatusStopped)
			return
		}
	}
}

func (a *BaseAgent) healthStatus() HealthStatus {
	return HealthStatus{
		AgentID: a.card.ID,
		Status:  a.Status(),
		Uptime:  time.Since(a.startTime),
	}
}

// Stop 向 agent 发送停止信号
func (a *BaseAgent) Stop() {
	select {
	case a.mailbox.Quit <- struct{}{}:
	default:
	}
}

// SendTask 向 agent 发送任务并等待响应
func (a *BaseAgent) SendTask(ctx context.Context, req TaskRequest) (TaskResponse, error) {
	if a.Status() != StatusRunning {
		return TaskResponse{}, errs.New(errs.CodeAgentUnavailable, fmt.Sprintf("agent %s is not running", a.card.ID))
	}

	replyCh := make(chan TaskResponse, 1)
	env := TaskEnvelope{Req: req, ReplyCh: replyCh}

	select {
	case a.mailbox.Request <- env:
	case <-ctx.Done():
		return TaskResponse{}, errs.Wrap(errs.CodeAgentTimeout, "send task timed out", ctx.Err())
	}

	select {
	case resp := <-replyCh:
		return resp, nil
	case <-ctx.Done():
		return TaskResponse{}, errs.Wrap(errs.CodeAgentTimeout, "wait response timed out", ctx.Err())
	}
}

// Ping 检查 agent 是否健康
func (a *BaseAgent) Ping(ctx context.Context) (HealthStatus, error) {
	reply := make(chan HealthStatus, 1)
	select {
	case a.mailbox.Health <- HealthPing{Reply: reply}:
	case <-ctx.Done():
		return HealthStatus{}, errs.Wrap(errs.CodeAgentTimeout, "health ping timed out", ctx.Err())
	}

	select {
	case status := <-reply:
		return status, nil
	case <-ctx.Done():
		return HealthStatus{}, errs.Wrap(errs.CodeAgentTimeout, "health reply timed out", ctx.Err())
	}
}

// Skills 返回 agent 的 skill 注册表
func (a *BaseAgent) Skills() *skills.Registry {
	return a.skills
}

// InvokeSkill 根据名称调用 skill 并返回渲染的内容
func (a *BaseAgent) InvokeSkill(name, args string) (string, error) {
	if a.skills == nil {
		return "", errs.New(errs.CodeSkillNotFound, "no skill registry configured")
	}
	rctx := skills.RenderContext{
		Arguments: args,
	}
	return a.skills.Invoke(name, rctx)
}

// ExtractPayload 从 TaskRequest 中提取实际的任务负载
// 请求负载可能包裹在 A2A Message 信封中
// 此方法解包并返回内部数据，以及如果存在的 skill_context
func ExtractPayload(req TaskRequest) (payload json.RawMessage, skillContext string) {
	// 尝试解包 A2A Message 信封: {"role":"user","parts":[{"type":"data","content":...}]}
	var msg struct {
		Parts []struct {
			Type    string          `json:"type"`
			Content json.RawMessage `json:"content"`
		} `json:"parts"`
	}
	if err := json.Unmarshal(req.Payload, &msg); err == nil && len(msg.Parts) > 0 {
		for _, part := range msg.Parts {
			if part.Type == "data" {
				// Content 是双重编码的：原始数据被 json.Marshal 到了 Part.Content 中
				// 尝试解包一层
				var inner json.RawMessage
				if err := json.Unmarshal(part.Content, &inner); err == nil {
					// 检查 inner 是否包含 skill_context
					var enriched struct {
						SkillContext string `json:"skill_context"`
					}
					if err := json.Unmarshal(inner, &enriched); err == nil && enriched.SkillContext != "" {
						skillContext = enriched.SkillContext
					}
					payload = inner
					return
				}
				payload = part.Content
				return
			}
		}
	}

	// 不是 A2A Message，直接使用原始 payload
	payload = req.Payload

	// 仍然检查 skill_context
	var enriched struct {
		SkillContext string `json:"skill_context"`
	}
	if err := json.Unmarshal(req.Payload, &enriched); err == nil && enriched.SkillContext != "" {
		skillContext = enriched.SkillContext
	}
	return
}
