package master

import (
	"context"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/chef-guo/agents-hive/internal/imctx"
	"github.com/chef-guo/agents-hive/internal/llm"
	"github.com/chef-guo/agents-hive/internal/memory"
	"github.com/chef-guo/agents-hive/internal/router"
	"github.com/chef-guo/agents-hive/internal/sessiontodo"
	"github.com/chef-guo/agents-hive/internal/specdriven"
)

// SessionState 表示一个持续的对话会话
type SessionState struct {
	// mu 保护所有字段的并发访问（多个 goroutine 可能并发读写）
	mu sync.RWMutex `json:"-"`

	// 基础信息
	ID           string    `json:"id"`
	Name         string    `json:"name"` // 用户友好名称
	Created      time.Time `json:"created"`
	LastAccessed time.Time `json:"last_accessed"` // 最后访问时间

	// 对话数据
	Messages []llm.MessageWithTools `json:"messages"` // 累积的对话历史

	// 元数据
	Metadata map[string]any `json:"metadata"`          // 额外元数据
	Tags     []string       `json:"tags,omitempty"`    // 标签
	UserID   string         `json:"user_id,omitempty"` // 会话所属用户（auth 启用时设置）

	// 统计信息
	Stats SessionStats `json:"stats"`

	// Spec-driven Phase 2 持久层：user ingress 路径累积的 change 索引
	// （对应 hive_spec_session_state 行）。
	// 写入者仅限 session_loop.go:processTask 入口 hook，持锁外写。
	// subagent / tool 路径触碰应走 runtime guard（specCtxGuard）。
	SpecState specdriven.SessionSpecState `json:"spec_state,omitzero"`

	// Plan Runtime 状态。PlanMode 控制 planning 阶段的工具 gate；
	// PlanStatus 表示当前 session plan 的完成判定状态。
	// 不在 SessionState 保存 per-session allowed tools，白名单由统一 gate 函数计算。
	PlanMode   bool                   `json:"plan_mode,omitempty"`
	PlanStatus sessiontodo.PlanStatus `json:"plan_status,omitempty"`

	// 内部状态（不持久化）
	lastSavedIndex int         `json:"-"` // 上次保存的消息索引
	persistFailed  bool        `json:"-"` // 增量持久化失败标记，阻止后续消息写入 DB（防止空洞）
	dirty          bool        `json:"-"` // 是否有未保存修改
	activeModel    string      `json:"-"` // 当前激活的模型（运行时）
	activeLLM      *llm.Client `json:"-"` // 当前 LLM client（可能不同于全局，运行时）

	// specCtx 是 spec-driven 运行时指针，发布后 IMMUTABLE（*specdriven.Context 内部字段禁改）。
	// 用 atomic.Pointer 而非 sync.Mutex 是 Codex Round 1 P0-6 红线：
	// react_processor.go 的 LLM 调用栈已经持会话锁，再引入 getSpecCtx()/setSpecCtx() 互斥会死锁。
	// 写：processTask ingress；读：tool 层、planner 回调、任何持锁位置都安全。
	specCtx atomic.Pointer[specdriven.Context] `json:"-"`

	// 临时字段（仅在当前请求处理期间有效，不持久化）
	pendingAttachments     []FileAttachment             `json:"-"`
	pendingReasoningEffort string                       `json:"-"`
	pendingModelOverride   string                       `json:"-"`
	pendingIMContext       *imctx.IMMessageContext      `json:"-"`
	pendingMemoryInjection memory.InjectionResult       `json:"-"`
	discoveredTools        map[string]bool              `json:"-"`
	allowedToolInputs      map[string]map[string]string `json:"-"`
	reflectionBlocks       []router.ReflectionBlock     `json:"-"`

	// 终止态：用于阻止已取消任务的陈旧写回。
	terminated         bool      `json:"-"`
	terminationReason  string    `json:"-"`
	terminatedAt       time.Time `json:"-"`
	terminationJournal bool      `json:"-"`
}

const maxSessionReflectionBlocks = 10

var structuralReflectionFailureKinds = map[string]struct{}{
	"schema_invalid":      {},
	"auth":                {},
	"4xx":                 {},
	"permission_denied":   {},
	"permission":          {},
	"invalid_credentials": {},
	"policy_denied":       {},
}

// LoadSpecCtx 返回当前 spec-driven 上下文指针（可能为 nil——表示非 spec-driven 会话）。
// 调用方禁止 mutate 返回值；要更新必须 new(Context) + StoreSpecCtx。
func (s *SessionState) LoadSpecCtx() *specdriven.Context {
	return s.specCtx.Load()
}

// StoreSpecCtx 原子替换 spec-driven 上下文。
// 仅限 user ingress 路径调用（session_loop.processTask entry），subagent 写入见 StoreSpecCtxGuarded。
func (s *SessionState) StoreSpecCtx(ctx *specdriven.Context) {
	s.specCtx.Store(ctx)
}

// SessionStats 表示会话的统计信息
type SessionStats struct {
	MessageCount  int `json:"message_count"`
	TaskCount     int `json:"task_count"`
	ToolCallCount int `json:"tool_call_count"`
	TotalTokens   int `json:"total_tokens"`
	ErrorCount    int `json:"error_count"`
}

// SessionCommand 表示会话级命令
type SessionCommand string

const (
	SessionCommandNone   SessionCommand = ""
	SessionCommandNew    SessionCommand = "new"
	SessionCommandSwitch SessionCommand = "switch"
	SessionCommandList   SessionCommand = "list"
	SessionCommandDelete SessionCommand = "delete"
	SessionCommandRename SessionCommand = "rename"
	SessionCommandInfo   SessionCommand = "info"
	SessionCommandExport SessionCommand = "export"
	SessionCommandFork   SessionCommand = "fork"   // 创建分支
	SessionCommandRevert SessionCommand = "revert" // 回滚到指定消息
)

// FileAttachment 文件附件
type FileAttachment struct {
	Filename string `json:"filename"`
	MimeType string `json:"mime_type"`
	Data     string `json:"data"` // base64
}

// SessionRequest 表示会话请求（替代原来的 string）
type SessionRequest struct {
	SessionID         string                  `json:"session_id,omitempty"`         // 目标会话 ID（空=当前活跃）
	Input             string                  `json:"input"`                        // 用户输入
	Command           SessionCommand          `json:"command,omitempty"`            // 会话命令
	Args              []string                `json:"args,omitempty"`               // 命令参数
	ResponseID        uint64                  `json:"-"`                            // per-request 响应标识（ProcessMessage 使用）
	Attachments       []FileAttachment        `json:"attachments,omitempty"`        // 文件附件
	ReasoningEffort   string                  `json:"reasoning_effort,omitempty"`   // 推理努力级别: "low"/"medium"/"high"
	ModelOverride     string                  `json:"model_override,omitempty"`     // 模型覆盖（由 skill/command 设置）
	ChannelMessageID  string                  `json:"channel_message_id,omitempty"` // IM 平台原消息 ID（供 input_received 事件透传，renderer 基于此做 ack 表情）
	TurnID            string                  `json:"-"`                            // 当前请求的稳定 turn_id；为空时由 session loop 生成
	AckAlreadyEmitted bool                    `json:"-"`                            // Router renderer 路径已提前广播 input_received，避免重复 ack
	IMContext         *imctx.IMMessageContext `json:"-"`                            // IM 消息上下文（transient，不持久化）
	SkipUserMessage   bool                    `json:"-"`                            // 跳过追加用户消息（regenerate 专用，避免重复写入）
	Ctx               context.Context         `json:"-"`                            // 请求上下文（由 ProcessRequestWithResponse 注入）
}

// TaskResponse 表示任务处理的响应
type TaskResponse struct {
	Content   string `json:"content"`           // 响应内容
	Status    string `json:"status,omitempty"`  // completed/paused/failed，新代码权威状态
	Completed bool   `json:"completed"`         // 任务是否完成
	Error     string `json:"error,omitempty"`   // 错误信息
	Exit      bool   `json:"exit"`              // 指示会话应退出
	Message   string `json:"message,omitempty"` // 系统消息(如"已清空")
}

type TaskStatus string

const (
	TaskStatusCompleted TaskStatus = "completed"
	TaskStatusPaused    TaskStatus = "paused"
	TaskStatusFailed    TaskStatus = "failed"
)

func NewTaskResponse(content string, status TaskStatus) TaskResponse {
	return NormalizeTaskResponse(TaskResponse{
		Content: content,
		Status:  string(status),
	})
}

func NormalizeTaskResponse(resp TaskResponse) TaskResponse {
	if resp.Status == "" {
		if resp.Error != "" {
			resp.Status = string(TaskStatusFailed)
			resp.Completed = false
		} else if resp.Completed {
			resp.Status = string(TaskStatusCompleted)
		}
		return resp
	}
	resp.Completed = resp.Status == string(TaskStatusCompleted)
	return resp
}

// SessionInfo 表示会话的简要信息（用于列表显示）
type SessionInfo struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	MessageCount int       `json:"message_count"`
	LastAccessed time.Time `json:"last_accessed"`
	Tags         []string  `json:"tags,omitempty"`
	IsActive     bool      `json:"is_active"`
}

// SetPendingData 设置临时附件、推理努力级别、模型覆盖和 IM 上下文
func (s *SessionState) SetPendingData(
	attachments []FileAttachment,
	effort string,
	modelOverride string,
	imCtx *imctx.IMMessageContext,
) {
	s.mu.Lock()
	s.pendingAttachments = attachments
	s.pendingReasoningEffort = effort
	s.pendingModelOverride = modelOverride
	s.pendingIMContext = imCtx
	s.mu.Unlock()
}

// GetPendingData 获取临时附件、推理努力级别和模型覆盖（不包含 IMContext，使用 ConsumePendingIMContext）
func (s *SessionState) GetPendingData() ([]FileAttachment, string, string) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.pendingAttachments, s.pendingReasoningEffort, s.pendingModelOverride
}

// ClearPendingData 清理临时附件、推理努力级别和模型覆盖
func (s *SessionState) ClearPendingData() {
	s.mu.Lock()
	s.pendingAttachments = nil
	s.pendingReasoningEffort = ""
	s.pendingModelOverride = ""
	s.pendingIMContext = nil
	s.pendingMemoryInjection = memory.InjectionResult{}
	s.mu.Unlock()
}

// SetQualityMemoryInjection 记录当前 turn 实际注入的 memory 构成，供质量事件读取。
func (s *SessionState) SetQualityMemoryInjection(result memory.InjectionResult) {
	s.mu.Lock()
	s.pendingMemoryInjection = result
	s.mu.Unlock()
}

// ConsumeQualityMemoryInjection 一次性消费当前 turn 的 memory 注入构成，避免跨 turn 污染。
func (s *SessionState) ConsumeQualityMemoryInjection() memory.InjectionResult {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := s.pendingMemoryInjection
	s.pendingMemoryInjection = memory.InjectionResult{}
	return result
}

// RecordDiscoveredTools 记录本会话通过 tool_search 显式发现的扩展工具。
func (s *SessionState) RecordDiscoveredTools(names []string) {
	if s == nil || len(names) == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.discoveredTools == nil {
		s.discoveredTools = make(map[string]bool, len(names))
	}
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		s.discoveredTools[name] = true
	}
}

// IsToolDiscovered 返回工具是否已在当前会话中被 tool_search 发现。
func (s *SessionState) IsToolDiscovered(name string) bool {
	if s == nil || name == "" {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.discoveredTools[name]
}

// DiscoveredTools 返回当前会话已发现工具名，按名称排序便于测试和调试。
func (s *SessionState) DiscoveredTools() []string {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]string, 0, len(s.discoveredTools))
	for name := range s.discoveredTools {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func (s *SessionState) SetAllowedToolInputs(inputs map[string]map[string]string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(inputs) == 0 {
		s.allowedToolInputs = nil
		return
	}
	s.allowedToolInputs = cloneAllowedToolInputs(inputs)
}

func (s *SessionState) AllowedToolInput(toolName, key string) (string, bool) {
	if s == nil || toolName == "" || key == "" {
		return "", false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	values := s.allowedToolInputs[toolName]
	if len(values) == 0 {
		return "", false
	}
	value, ok := values[key]
	return value, ok
}

func (s *SessionState) AllowedToolInputsSnapshot() map[string]map[string]string {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return cloneAllowedToolInputs(s.allowedToolInputs)
}

// AddReflectionBlock 记录结构性失败产出的会话级工具阻断；瞬态失败会被忽略。
func (s *SessionState) AddReflectionBlock(block router.ReflectionBlock) bool {
	if s == nil || strings.TrimSpace(block.ToolName) == "" || !isStructuralReflectionFailureKind(block.FailureKind) {
		return false
	}
	block.ToolName = strings.TrimSpace(block.ToolName)
	block.Mode = strings.TrimSpace(block.Mode)
	block.Reason = strings.TrimSpace(block.Reason)
	block.FailureKind = strings.TrimSpace(block.FailureKind)
	if block.CreatedAt.IsZero() {
		block.CreatedAt = time.Now()
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.reflectionBlocks = append(s.reflectionBlocks, block)
	if overflow := len(s.reflectionBlocks) - maxSessionReflectionBlocks; overflow > 0 {
		s.reflectionBlocks = append([]router.ReflectionBlock(nil), s.reflectionBlocks[overflow:]...)
	}
	return true
}

// ListReflectionBlocks 返回当前会话反思阻断快照，调用方可按 mode 传给 RouteDecision。
func (s *SessionState) ListReflectionBlocks() []router.ReflectionBlock {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]router.ReflectionBlock(nil), s.reflectionBlocks...)
}

func (s *SessionState) ClearReflectionBlocks() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.reflectionBlocks = nil
	s.mu.Unlock()
}

func isStructuralReflectionFailureKind(kind string) bool {
	kind = strings.TrimSpace(strings.ToLower(kind))
	if kind == "" {
		return false
	}
	if _, ok := structuralReflectionFailureKinds[kind]; ok {
		return true
	}
	return strings.HasPrefix(kind, "4") && strings.HasSuffix(kind, "xx")
}

func cloneAllowedToolInputs(in map[string]map[string]string) map[string]map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]map[string]string, len(in))
	for tool, values := range in {
		if len(values) == 0 {
			continue
		}
		copied := make(map[string]string, len(values))
		for key, value := range values {
			copied[key] = value
		}
		out[tool] = copied
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// ConsumePendingIMContext 一次性消费 pendingIMContext（第二次调用返回 nil）
func (s *SessionState) ConsumePendingIMContext() *imctx.IMMessageContext {
	s.mu.Lock()
	defer s.mu.Unlock()
	ctx := s.pendingIMContext
	s.pendingIMContext = nil
	return ctx
}

// MarkTerminated 标记会话为已终止；首次调用返回 true，后续幂等返回 false。
func (s *SessionState) MarkTerminated(reason string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.terminated {
		return false
	}
	s.terminated = true
	s.terminationReason = reason
	s.terminatedAt = time.Now()
	return true
}

// IsTerminated 返回会话是否已被终止。
func (s *SessionState) IsTerminated() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.terminated
}

// TerminationReason 返回终止原因。
func (s *SessionState) TerminationReason() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.terminationReason
}

// MarkTerminationJournalEnded 标记终止 journal 已处理；首次返回 true。
func (s *SessionState) MarkTerminationJournalEnded() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.terminationJournal {
		return false
	}
	s.terminationJournal = true
	return true
}
