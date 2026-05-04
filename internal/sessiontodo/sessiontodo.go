package sessiontodo

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// TodoStatus 表示 session 级 todo 的执行状态。
type TodoStatus string

const (
	TodoStatusPending    TodoStatus = "pending"
	TodoStatusInProgress TodoStatus = "in_progress"
	TodoStatusCompleted  TodoStatus = "completed"
	TodoStatusCancelled  TodoStatus = "cancelled"
)

// PlanStatus 表示当前 session plan runtime 的阶段。
type PlanStatus string

const (
	PlanStatusNone             PlanStatus = "none"
	PlanStatusPlanning         PlanStatus = "planning"
	PlanStatusAwaitingApproval PlanStatus = "awaiting_approval"
	PlanStatusExecuting        PlanStatus = "executing"
	PlanStatusPaused           PlanStatus = "paused"
	PlanStatusCompleted        PlanStatus = "completed"
	PlanStatusFailed           PlanStatus = "failed"
)

// Todo 是持久化后的 session-scoped todo。
type Todo struct {
	ID               string     `json:"id"`
	SessionID        string     `json:"session_id"`
	Content          string     `json:"content"`
	Status           TodoStatus `json:"status"`
	Order            int        `json:"order"`
	Version          int64      `json:"version"`
	Source           string     `json:"source,omitempty"`
	TraceID          string     `json:"trace_id,omitempty"`
	SpanID           string     `json:"span_id,omitempty"`
	TurnID           string     `json:"turn_id,omitempty"`
	RuntimeEpoch     string     `json:"runtime_epoch,omitempty"`
	SourceChangeID   string     `json:"source_change_id,omitempty"`
	SourceRevision   int64      `json:"source_revision,omitempty"`
	SourceToolCallID string     `json:"source_tool_call_id,omitempty"`
	CreatedAt        time.Time  `json:"created_at"`
	UpdatedAt        time.Time  `json:"updated_at"`
}

// TodoInput 是 Replace 接收的单条 todo 输入。
//
// SessionID 仅用于兼容上游结构化输入；Store 必须忽略该字段，使用方法参数里的 sessionID。
type TodoInput struct {
	ID               string     `json:"id"`
	SessionID        string     `json:"session_id,omitempty"`
	Content          string     `json:"content"`
	Status           TodoStatus `json:"status"`
	Order            int        `json:"order"`
	Source           string     `json:"source,omitempty"`
	TraceID          string     `json:"trace_id,omitempty"`
	SpanID           string     `json:"span_id,omitempty"`
	TurnID           string     `json:"turn_id,omitempty"`
	RuntimeEpoch     string     `json:"runtime_epoch,omitempty"`
	SourceChangeID   string     `json:"source_change_id,omitempty"`
	SourceRevision   int64      `json:"source_revision,omitempty"`
	SourceToolCallID string     `json:"source_tool_call_id,omitempty"`
}

// Snapshot 是某个 session 当前 plan/todos 的完整快照。
type Snapshot struct {
	SessionID        string     `json:"session_id"`
	PlanStatus       PlanStatus `json:"plan_status"`
	PlanVersion      int64      `json:"plan_version"`
	Todos            []Todo     `json:"todos"`
	Source           string     `json:"source,omitempty"`
	TraceID          string     `json:"trace_id,omitempty"`
	SpanID           string     `json:"span_id,omitempty"`
	TurnID           string     `json:"turn_id,omitempty"`
	RuntimeEpoch     string     `json:"runtime_epoch,omitempty"`
	SourceToolCallID string     `json:"source_tool_call_id,omitempty"`
	SourceChangeID   string     `json:"source_change_id,omitempty"`
	SourceRevision   int64      `json:"source_revision,omitempty"`
	UpdatedAt        time.Time  `json:"updated_at"`
}

// SnapshotMeta 是状态迁移时附加到 snapshot header 的运行元数据。
type SnapshotMeta struct {
	Source           string
	TraceID          string
	SpanID           string
	TurnID           string
	RuntimeEpoch     string
	SourceToolCallID string
	SourceChangeID   string
	SourceRevision   int64
}

// Store 定义 session todo snapshot 存储接口。
type Store interface {
	Replace(ctx context.Context, sessionID string, expectedPlanVersion int64, todos []TodoInput) (Snapshot, error)
	Snapshot(ctx context.Context, sessionID string) (Snapshot, error)
	SetPlanStatus(ctx context.Context, sessionID string, status PlanStatus) (Snapshot, error)
	SetPlanStatusWithMeta(ctx context.Context, sessionID string, status PlanStatus, meta SnapshotMeta) (Snapshot, error)
	ClaimResume(ctx context.Context, sessionID string, expectedPlanVersion int64, expectedRuntimeEpoch, runtimeEpoch, turnID string) (Snapshot, error)
	Clear(ctx context.Context, sessionID string) error
}

const sessionTodosMetaID = "__snapshot__"

var (
	// ErrPlanVersionConflict 用于 errors.Is 识别 Replace CAS 冲突。
	ErrPlanVersionConflict = errors.New("sessiontodo plan version conflict")

	// ErrInvalidInput 表示 Store 输入不合法。
	ErrInvalidInput = errors.New("sessiontodo invalid input")

	// ErrRuntimeEpochConflict 用于识别恢复世代冲突。
	ErrRuntimeEpochConflict = errors.New("sessiontodo runtime epoch conflict")

	// ErrResumeConflict 表示当前 snapshot 不允许 resume claim。
	ErrResumeConflict = errors.New("sessiontodo resume conflict")
)

// PlanVersionConflictError 携带 CAS 冲突的期望版本和当前版本。
type PlanVersionConflictError struct {
	Expected int64
	Got      int64
}

func (e *PlanVersionConflictError) Error() string {
	return fmt.Sprintf("%v: expected %d, got %d", ErrPlanVersionConflict, e.Expected, e.Got)
}

func (e *PlanVersionConflictError) Unwrap() error {
	return ErrPlanVersionConflict
}

// RuntimeEpochConflictError 携带 runtime epoch 冲突的新旧值。
type RuntimeEpochConflictError struct {
	Expected string
	Got      string
}

func (e *RuntimeEpochConflictError) Error() string {
	return fmt.Sprintf("%v: expected %q, got %q", ErrRuntimeEpochConflict, e.Expected, e.Got)
}

func (e *RuntimeEpochConflictError) Unwrap() error {
	return ErrRuntimeEpochConflict
}

func validateTodoStatus(status TodoStatus) error {
	switch status {
	case TodoStatusPending, TodoStatusInProgress, TodoStatusCompleted, TodoStatusCancelled:
		return nil
	default:
		return fmt.Errorf("%w: invalid todo status %q", ErrInvalidInput, status)
	}
}

func validatePlanStatus(status PlanStatus) error {
	switch status {
	case PlanStatusNone, PlanStatusPlanning, PlanStatusAwaitingApproval, PlanStatusExecuting,
		PlanStatusPaused, PlanStatusCompleted, PlanStatusFailed:
		return nil
	default:
		return fmt.Errorf("%w: invalid plan status %q", ErrInvalidInput, status)
	}
}

func validateSessionID(sessionID string) error {
	if strings.TrimSpace(sessionID) == "" {
		return fmt.Errorf("%w: sessionID is required", ErrInvalidInput)
	}
	return nil
}

func validateTodoInputs(todos []TodoInput) error {
	seen := make(map[string]struct{}, len(todos))
	for i, input := range todos {
		if strings.TrimSpace(input.Content) == "" {
			return fmt.Errorf("%w: todo content is required at index %d", ErrInvalidInput, i)
		}
		if err := validateTodoStatus(input.Status); err != nil {
			return err
		}
		id := todoIDForInput(input, i)
		if id == sessionTodosMetaID {
			return fmt.Errorf("%w: reserved todo id %q", ErrInvalidInput, id)
		}
		if _, ok := seen[id]; ok {
			return fmt.Errorf("%w: duplicate todo id %q", ErrInvalidInput, id)
		}
		seen[id] = struct{}{}
	}
	return nil
}

func cloneSnapshot(in Snapshot) Snapshot {
	out := in
	if in.Todos == nil {
		out.Todos = nil
		return out
	}
	out.Todos = make([]Todo, len(in.Todos))
	copy(out.Todos, in.Todos)
	return out
}

func emptySnapshot(sessionID string) Snapshot {
	return Snapshot{
		SessionID:   sessionID,
		PlanStatus:  PlanStatusNone,
		PlanVersion: 0,
		Todos:       []Todo{},
	}
}

func generatedTodoID(index int) string {
	return fmt.Sprintf("todo-%d", index+1)
}

func todoIDForInput(input TodoInput, index int) string {
	if input.ID != "" {
		return input.ID
	}
	return generatedTodoID(index)
}

func snapshotSourceFromTodos(todos []Todo) (source, traceID, spanID, turnID, runtimeEpoch, sourceToolCallID, sourceChangeID string, sourceRevision int64) {
	if len(todos) == 0 {
		return "", "", "", "", "", "", "", 0
	}
	for i := len(todos) - 1; i >= 0; i-- {
		todo := todos[i]
		if source == "" {
			source = todo.Source
		}
		if traceID == "" {
			traceID = todo.TraceID
		}
		if spanID == "" {
			spanID = todo.SpanID
		}
		if turnID == "" {
			turnID = todo.TurnID
		}
		if runtimeEpoch == "" {
			runtimeEpoch = todo.RuntimeEpoch
		}
		if sourceToolCallID == "" {
			sourceToolCallID = todo.SourceToolCallID
		}
		if sourceChangeID == "" {
			sourceChangeID = todo.SourceChangeID
		}
		if sourceRevision == 0 {
			sourceRevision = todo.SourceRevision
		}
	}
	return source, traceID, spanID, turnID, runtimeEpoch, sourceToolCallID, sourceChangeID, sourceRevision
}

func mergeSnapshotMeta(current Snapshot, meta SnapshotMeta) SnapshotMeta {
	if meta.Source == "" {
		meta.Source = current.Source
	}
	if meta.TraceID == "" {
		meta.TraceID = current.TraceID
	}
	if meta.SpanID == "" {
		meta.SpanID = current.SpanID
	}
	if meta.TurnID == "" {
		meta.TurnID = current.TurnID
	}
	if meta.RuntimeEpoch == "" {
		meta.RuntimeEpoch = current.RuntimeEpoch
	}
	if meta.SourceToolCallID == "" {
		meta.SourceToolCallID = current.SourceToolCallID
	}
	if meta.SourceChangeID == "" {
		meta.SourceChangeID = current.SourceChangeID
	}
	if meta.SourceRevision == 0 {
		meta.SourceRevision = current.SourceRevision
	}
	return meta
}
