package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/chef-guo/agents-hive/internal/mcphost"
	"github.com/chef-guo/agents-hive/internal/sessiontodo"
	"github.com/chef-guo/agents-hive/internal/toolctx"
)

const (
	todoWriteToolName = "todo_write"
	maxTodoWriteItems = 100
)

// TodoStatus 是 session 级临时 todo 状态。
type TodoStatus = sessiontodo.TodoStatus

const (
	TodoStatusPending    TodoStatus = sessiontodo.TodoStatusPending
	TodoStatusInProgress TodoStatus = sessiontodo.TodoStatusInProgress
	TodoStatusCompleted  TodoStatus = sessiontodo.TodoStatusCompleted
	TodoStatusCancelled  TodoStatus = sessiontodo.TodoStatusCancelled
)

// PlanStatus 是 session 级计划状态。
type PlanStatus = sessiontodo.PlanStatus

const (
	PlanStatusNone             PlanStatus = sessiontodo.PlanStatusNone
	PlanStatusPlanning         PlanStatus = sessiontodo.PlanStatusPlanning
	PlanStatusAwaitingApproval PlanStatus = sessiontodo.PlanStatusAwaitingApproval
	PlanStatusExecuting        PlanStatus = sessiontodo.PlanStatusExecuting
	PlanStatusPaused           PlanStatus = sessiontodo.PlanStatusPaused
	PlanStatusCompleted        PlanStatus = sessiontodo.PlanStatusCompleted
	PlanStatusFailed           PlanStatus = sessiontodo.PlanStatusFailed
)

// SessionTodoInput 是 todo_write 写入 Store 的单条 todo 输入。
type SessionTodoInput = sessiontodo.TodoInput

// SessionTodo 是 snapshot 中的单条 todo。
type SessionTodo = sessiontodo.Todo

// SessionTodoSource 标识本次写入来源，用于 trace 和广播排障。
type SessionTodoSource struct {
	Source           string `json:"source,omitempty"`
	TraceID          string `json:"trace_id,omitempty"`
	SpanID           string `json:"span_id,omitempty"`
	ParentSpanID     string `json:"parent_span_id,omitempty"`
	SourceToolCallID string `json:"source_tool_call_id,omitempty"`
}

// SessionTodoSnapshot 是工具层与 sessiontodo 存储层之间的窄 snapshot 契约。
type SessionTodoSnapshot = sessiontodo.Snapshot

// SessionTodoStore 是工具层依赖的最小 sessiontodo Store 契约。
type SessionTodoStore interface {
	Replace(ctx context.Context, sessionID string, expectedPlanVersion int64, todos []SessionTodoInput) (SessionTodoSnapshot, error)
	Snapshot(ctx context.Context, sessionID string) (SessionTodoSnapshot, error)
	SetPlanStatus(ctx context.Context, sessionID string, status PlanStatus) (SessionTodoSnapshot, error)
}

// TodoSnapshotBroadcaster 是 todo/plan 工具成功后触发实时广播的窄接口。
type TodoSnapshotBroadcaster interface {
	BroadcastTodoSnapshot(ctx context.Context, snapshot SessionTodoSnapshot) error
}

// PlanRuntimeObserver 由 Master 注入，用于把工具层事件转成 metrics / spans / logs。
type PlanRuntimeObserver interface {
	RecordTodoWrite(ctx context.Context, event TodoWriteObservation)
	RecordPlanTool(ctx context.Context, event PlanToolObservation)
}

type TodoWriteObservation struct {
	SessionID           string
	Source              string
	Status              string
	TraceID             string
	SpanID              string
	ParentSpanID        string
	ToolCallID          string
	ExpectedPlanVersion int64
	PlanVersion         int64
	TodoCount           int
	Error               string
	ConflictExpected    int64
	ConflictGot         int64
	StartedAt           time.Time
	Duration            time.Duration
}

type PlanToolObservation struct {
	ToolName      string
	Operation     string
	Source        SessionTodoSource
	SessionID     string
	Status        string
	PlanStatus    PlanStatus
	PlanVersion   int64
	TodoCount     int
	TraceID       string
	SpanID        string
	ParentSpanID  string
	ToolCallID    string
	Error         string
	OpenTodoCount int
	StartedAt     time.Time
	Duration      time.Duration
}

// PlanVersionConflictError 表示 expected_plan_version CAS 冲突。
type PlanVersionConflictError = sessiontodo.PlanVersionConflictError

type todoWriteInput struct {
	SessionID           *string               `json:"session_id,omitempty"`
	ExpectedPlanVersion *int64                `json:"expected_plan_version"`
	Todos               *[]todoWriteTodoInput `json:"todos"`
}

type todoWriteTodoInput struct {
	ID      string     `json:"id,omitempty"`
	Content string     `json:"content"`
	Status  TodoStatus `json:"status"`
}

func registerTodoWrite(host *mcphost.Host, logger *zap.Logger, store SessionTodoStore, broadcaster TodoSnapshotBroadcaster, observers ...PlanRuntimeObserver) {
	observer := firstPlanRuntimeObserver(observers...)
	schema, _ := json.Marshal(map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"expected_plan_version": map[string]any{
				"type":        "integer",
				"description": "当前 snapshot 的 plan_version。初次写入传 0；冲突时重新读取最新 snapshot 后重试。",
			},
			"todos": map[string]any{
				"type":        "array",
				"description": "完整 todo snapshot。每次调用都必须传全部当前 todos，最多 100 条。",
				"maxItems":    maxTodoWriteItems,
				"items": map[string]any{
					"type":                 "object",
					"additionalProperties": false,
					"properties": map[string]any{
						"id":      map[string]any{"type": "string", "description": "稳定 todo ID；为空时由后端生成"},
						"content": map[string]any{"type": "string", "description": "todo 内容，不能为空"},
						"status": map[string]any{
							"type": "string",
							"enum": []string{
								string(TodoStatusPending),
								string(TodoStatusInProgress),
								string(TodoStatusCompleted),
								string(TodoStatusCancelled),
							},
						},
					},
					"required": []string{"content", "status"},
				},
			},
		},
		"required": []string{"expected_plan_version", "todos"},
	})

	host.RegisterTool(
		mcphost.ToolDefinition{
			Name:              todoWriteToolName,
			Description:       "写入当前 session 的完整 todo snapshot。仅 Master Agent 可调用；session_id 由运行时上下文提供，不能作为参数传入。",
			InputSchema:       schema,
			Core:              true,
			IsConcurrencySafe: false,
		},
		func(ctx context.Context, input json.RawMessage) (*mcphost.ToolResult, error) {
			start := time.Now()
			source := sourceFromToolContext(ctx)
			if err := requireMasterCaller(ctx, todoWriteToolName, logger); err != nil {
				recordTodoWriteObservation(ctx, observer, TodoWriteObservation{
					SessionID:    "",
					Source:       source.Source,
					Status:       "error",
					TraceID:      source.TraceID,
					SpanID:       source.SpanID,
					ParentSpanID: source.ParentSpanID,
					ToolCallID:   source.SourceToolCallID,
					Error:        err.Error(),
					StartedAt:    start,
					Duration:     time.Since(start),
				})
				return errorResult(err.Error()), nil
			}
			if store == nil {
				recordTodoWriteObservation(ctx, observer, TodoWriteObservation{Source: source.Source, Status: "error", TraceID: source.TraceID, SpanID: source.SpanID, ParentSpanID: source.ParentSpanID, ToolCallID: source.SourceToolCallID, Error: "session todo store 未配置", StartedAt: start, Duration: time.Since(start)})
				return errorResult("todo_write 未启用：session todo store 未配置"), nil
			}
			sessionID := toolctx.GetSessionID(ctx)
			if sessionID == "" {
				recordTodoWriteObservation(ctx, observer, TodoWriteObservation{Source: source.Source, Status: "error", TraceID: source.TraceID, SpanID: source.SpanID, ParentSpanID: source.ParentSpanID, ToolCallID: source.SourceToolCallID, Error: "missing sessionID", StartedAt: start, Duration: time.Since(start)})
				return errorResult("todo_write 缺少 sessionID：必须由运行时上下文提供"), nil
			}

			var params todoWriteInput
			if err := json.Unmarshal(input, &params); err != nil {
				recordTodoWriteObservation(ctx, observer, TodoWriteObservation{SessionID: sessionID, Source: source.Source, Status: "error", TraceID: source.TraceID, SpanID: source.SpanID, ParentSpanID: source.ParentSpanID, ToolCallID: source.SourceToolCallID, Error: err.Error(), StartedAt: start, Duration: time.Since(start)})
				return errorResult("输入无效: " + err.Error()), nil
			}
			if params.SessionID != nil {
				recordTodoWriteObservation(ctx, observer, TodoWriteObservation{SessionID: sessionID, Source: source.Source, Status: "error", TraceID: source.TraceID, SpanID: source.SpanID, ParentSpanID: source.ParentSpanID, ToolCallID: source.SourceToolCallID, Error: "session_id input rejected", StartedAt: start, Duration: time.Since(start)})
				return errorResult("todo_write 不允许输入 session_id；sessionID 必须来自运行时上下文"), nil
			}
			if params.ExpectedPlanVersion == nil {
				recordTodoWriteObservation(ctx, observer, TodoWriteObservation{SessionID: sessionID, Source: source.Source, Status: "error", TraceID: source.TraceID, SpanID: source.SpanID, ParentSpanID: source.ParentSpanID, ToolCallID: source.SourceToolCallID, Error: "missing expected_plan_version", StartedAt: start, Duration: time.Since(start)})
				return errorResult("expected_plan_version 为必填字段"), nil
			}
			if params.Todos == nil {
				recordTodoWriteObservation(ctx, observer, TodoWriteObservation{SessionID: sessionID, Source: source.Source, Status: "error", TraceID: source.TraceID, SpanID: source.SpanID, ParentSpanID: source.ParentSpanID, ToolCallID: source.SourceToolCallID, ExpectedPlanVersion: *params.ExpectedPlanVersion, Error: "missing todos", StartedAt: start, Duration: time.Since(start)})
				return errorResult("todos 为必填字段；请传入完整 todo snapshot"), nil
			}

			todos, err := normalizeTodoWriteInputs(*params.Todos, source)
			if err != nil {
				recordTodoWriteObservation(ctx, observer, TodoWriteObservation{SessionID: sessionID, Source: source.Source, Status: "error", TraceID: source.TraceID, SpanID: source.SpanID, ParentSpanID: source.ParentSpanID, ToolCallID: source.SourceToolCallID, ExpectedPlanVersion: *params.ExpectedPlanVersion, TodoCount: len(*params.Todos), Error: err.Error(), StartedAt: start, Duration: time.Since(start)})
				return errorResult(err.Error()), nil
			}
			snapshot, err := store.Replace(ctx, sessionID, *params.ExpectedPlanVersion, todos)
			if err != nil {
				if conflict := asPlanVersionConflict(err); conflict != nil {
					recordTodoWriteObservation(ctx, observer, TodoWriteObservation{
						SessionID:           sessionID,
						Source:              source.Source,
						Status:              "conflict",
						TraceID:             source.TraceID,
						SpanID:              source.SpanID,
						ParentSpanID:        source.ParentSpanID,
						ToolCallID:          source.SourceToolCallID,
						ExpectedPlanVersion: *params.ExpectedPlanVersion,
						TodoCount:           len(todos),
						Error:               err.Error(),
						ConflictExpected:    conflict.Expected,
						ConflictGot:         conflict.Got,
						StartedAt:           start,
						Duration:            time.Since(start),
					})
					return errorResult(formatPlanVersionConflict(conflict) + "；请基于最新 todo snapshot 重试"), nil
				}
				logger.Error("todo_write 写入失败",
					zap.String("session_id", sessionID),
					zap.Int64("expected_plan_version", *params.ExpectedPlanVersion),
					zap.Error(err),
				)
				recordTodoWriteObservation(ctx, observer, TodoWriteObservation{SessionID: sessionID, Source: source.Source, Status: "error", TraceID: source.TraceID, SpanID: source.SpanID, ParentSpanID: source.ParentSpanID, ToolCallID: source.SourceToolCallID, ExpectedPlanVersion: *params.ExpectedPlanVersion, TodoCount: len(todos), Error: err.Error(), StartedAt: start, Duration: time.Since(start)})
				return errorResult("todo_write 写入失败: " + err.Error()), nil
			}
			if err := broadcastTodoSnapshot(ctx, broadcaster, snapshot, logger); err != nil {
				recordTodoWriteObservation(ctx, observer, TodoWriteObservation{SessionID: sessionID, Source: source.Source, Status: "error", TraceID: source.TraceID, SpanID: source.SpanID, ParentSpanID: source.ParentSpanID, ToolCallID: source.SourceToolCallID, ExpectedPlanVersion: *params.ExpectedPlanVersion, PlanVersion: snapshot.PlanVersion, TodoCount: len(snapshot.Todos), Error: err.Error(), StartedAt: start, Duration: time.Since(start)})
				return errorResult("todo_write 广播失败: " + err.Error()), nil
			}
			recordTodoWriteObservation(ctx, observer, TodoWriteObservation{
				SessionID:           sessionID,
				Source:              source.Source,
				Status:              "ok",
				TraceID:             source.TraceID,
				SpanID:              source.SpanID,
				ParentSpanID:        source.ParentSpanID,
				ToolCallID:          source.SourceToolCallID,
				ExpectedPlanVersion: *params.ExpectedPlanVersion,
				PlanVersion:         snapshot.PlanVersion,
				TodoCount:           len(snapshot.Todos),
				StartedAt:           start,
				Duration:            time.Since(start),
			})
			return todoSnapshotResult(snapshot), nil
		},
	)
}

func firstPlanRuntimeObserver(observers ...PlanRuntimeObserver) PlanRuntimeObserver {
	for _, observer := range observers {
		if observer != nil {
			return observer
		}
	}
	return nil
}

func recordTodoWriteObservation(ctx context.Context, observer PlanRuntimeObserver, event TodoWriteObservation) {
	if observer == nil {
		return
	}
	if event.Source == "" {
		event.Source = "agent"
	}
	observer.RecordTodoWrite(ctx, event)
}

func normalizeTodoWriteInputs(inputs []todoWriteTodoInput, source SessionTodoSource) ([]SessionTodoInput, error) {
	if len(inputs) > maxTodoWriteItems {
		return nil, fmt.Errorf("todos 单次最多 %d 条", maxTodoWriteItems)
	}
	todos := make([]SessionTodoInput, 0, len(inputs))
	for i, input := range inputs {
		content := strings.TrimSpace(input.Content)
		if content == "" {
			return nil, fmt.Errorf("todos[%d].content 不能为空", i)
		}
		if !isValidTodoStatus(input.Status) {
			return nil, fmt.Errorf("todos[%d].status 非法：仅允许 pending/in_progress/completed/cancelled", i)
		}
		todos = append(todos, SessionTodoInput{
			ID:      strings.TrimSpace(input.ID),
			Content: content,
			Status:  input.Status,
			Order:   i,
			Source:  source.Source,
			TraceID: source.TraceID,
			SpanID:  source.SpanID,

			SourceToolCallID: source.SourceToolCallID,
		})
	}
	return todos, nil
}

func isValidTodoStatus(status TodoStatus) bool {
	switch status {
	case TodoStatusPending, TodoStatusInProgress, TodoStatusCompleted, TodoStatusCancelled:
		return true
	default:
		return false
	}
}

func requireMasterCaller(ctx context.Context, toolName string, logger *zap.Logger) error {
	toolCtx := toolctx.GetToolContext(ctx)
	if toolCtx.CallerType == toolctx.CallerMaster {
		return nil
	}
	logger.Warn("plan/todo 工具调用被拒绝：非 Master 调用",
		zap.String("tool_name", toolName),
		zap.String("caller_type", string(toolCtx.CallerType)),
		zap.String("caller_name", toolCtx.CallerName),
	)
	return fmt.Errorf("错误：%s 工具仅允许 Master Agent 调用", toolName)
}

func sourceFromToolContext(ctx context.Context) SessionTodoSource {
	traceID, spanID, parentSpanID, toolCallID := toolctx.GetToolContext(ctx).TraceFields()
	return SessionTodoSource{
		Source:           "agent",
		TraceID:          traceID,
		SpanID:           spanID,
		ParentSpanID:     parentSpanID,
		SourceToolCallID: toolCallID,
	}
}

func broadcastTodoSnapshot(ctx context.Context, broadcaster TodoSnapshotBroadcaster, snapshot SessionTodoSnapshot, logger *zap.Logger) error {
	if broadcaster == nil {
		return nil
	}
	if err := broadcaster.BroadcastTodoSnapshot(ctx, snapshot); err != nil {
		logger.Error("todo snapshot 广播失败",
			zap.String("session_id", snapshot.SessionID),
			zap.Int64("plan_version", snapshot.PlanVersion),
			zap.Error(err),
		)
		return err
	}
	return nil
}

func todoSnapshotResult(snapshot SessionTodoSnapshot) *mcphost.ToolResult {
	data, err := json.Marshal(snapshot)
	if err != nil {
		return errorResult("序列化 todo snapshot 失败: " + err.Error())
	}
	return textResult(string(data))
}

func asPlanVersionConflict(err error) *PlanVersionConflictError {
	var conflict *PlanVersionConflictError
	if errors.As(err, &conflict) {
		return conflict
	}
	if errors.Is(err, sessiontodo.ErrPlanVersionConflict) {
		return &PlanVersionConflictError{}
	}
	return nil
}

func formatPlanVersionConflict(conflict *PlanVersionConflictError) string {
	if conflict == nil {
		return "plan_version conflict"
	}
	return fmt.Sprintf("plan_version conflict: expected=%d got=%d", conflict.Expected, conflict.Got)
}
