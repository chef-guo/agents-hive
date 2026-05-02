package master

import (
	"context"
	"fmt"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/chef-guo/agents-hive/internal/observability"
	"github.com/chef-guo/agents-hive/internal/sessiontodo"
	"github.com/chef-guo/agents-hive/internal/toolctx"
	"github.com/chef-guo/agents-hive/internal/tools"
)

type PlanToolTraceContext struct {
	TraceID      string
	SpanID       string
	ParentSpanID string
	ToolCallID   string
}

type planToolTraceKey struct{}

func WithPlanToolTrace(ctx context.Context, trace PlanToolTraceContext) context.Context {
	return context.WithValue(ctx, planToolTraceKey{}, trace)
}

func PlanToolTraceFromContext(ctx context.Context) PlanToolTraceContext {
	if trace, ok := ctx.Value(planToolTraceKey{}).(PlanToolTraceContext); ok {
		return trace
	}
	return PlanToolTraceContext{}
}

type PlanToolGateDecision struct {
	Allowed    bool
	Reason     string
	CallerType toolctx.CallerType
	ToolName   string
}

// EvaluatePlanToolGate 是 master 侧唯一工具执行 gate。
//
// 直接 ReAct 工具和 executeToolsConcurrent 已在 executeTool 前调用本函数。
// tools 包内的 batch / parallel_dispatch 子工具入口不能只依赖模型可见性；
// 整合时应通过 callback 复用同一判定，例如：
//
//	type NestedToolGate func(ctx context.Context, toolName string) error
//
// 子工具真正执行前调用该 callback；被拒绝时返回 tool error，不再执行子工具。
// 这样避免 tools 包反向 import master，同时保持 plan mode 白名单只有这一处。
var planControlTools = map[string]bool{
	"todo_write":      true,
	"finish_plan":     true,
	"enter_plan_mode": true,
	"exit_plan_mode":  true,
}

var planModeAllowedTools = map[string]bool{
	"exit_plan_mode": true,
	"glob":           true,
	"grep":           true,
	"ls":             true,
	"memory":         true,
	"question":       true,
	"read_file":      true,
	"skill":          true,
	"todo_write":     true,
	"tool_search":    true,
	"webfetch":       true,
	"websearch":      true,
	"web_fetch":      true,
	"web_search":     true,
}

func EvaluatePlanToolGate(ctx context.Context, session *SessionState, toolName string) PlanToolGateDecision {
	caller := toolctx.GetToolContext(ctx).CallerType
	toolName = strings.TrimSpace(toolName)
	decision := PlanToolGateDecision{
		Allowed:    true,
		CallerType: caller,
		ToolName:   toolName,
	}

	if caller == toolctx.CallerSubAgent && planControlTools[toolName] {
		decision.Allowed = false
		decision.Reason = "subagent cannot call plan runtime control tools"
		return decision
	}

	if session == nil {
		return decision
	}
	session.mu.RLock()
	planMode := session.PlanMode
	planStatus := session.PlanStatus
	session.mu.RUnlock()

	if !planMode && planStatus != sessiontodo.PlanStatusPlanning && planStatus != sessiontodo.PlanStatusAwaitingApproval {
		return decision
	}
	if !planModeAllowedTools[toolName] {
		decision.Allowed = false
		decision.Reason = fmt.Sprintf("tool %q is not allowed in plan mode", toolName)
	}
	return decision
}

func (m *Master) evaluatePlanToolGate(ctx context.Context, session *SessionState, toolName string) PlanToolGateDecision {
	decision := EvaluatePlanToolGate(ctx, session, toolName)
	if decision.Allowed {
		return decision
	}
	sessionID := ""
	if session != nil {
		sessionID = session.ID
	}
	if m.logger != nil {
		m.logger.Warn("plan mode gate denied tool call",
			zap.String("session_id", sessionID),
			zap.String("tool_name", toolName),
			zap.String("caller_type", string(decision.CallerType)),
			zap.String("reason", decision.Reason),
		)
	}
	m.enqueueMetric(observability.Metric{
		Name:  "hive_plan_mode_gate_denied_total",
		Value: 1,
		Labels: map[string]any{
			"tool_name":   toolName,
			"caller_type": string(decision.CallerType),
		},
		Ts: time.Now(),
	})
	return decision
}

func (m *Master) CheckNestedToolAllowed(ctx context.Context, toolName string) error {
	if m == nil {
		return nil
	}
	sessionID := toolctx.GetSessionID(ctx)
	var session *SessionState
	if sessionID != "" && m.sessionMgr != nil {
		session = m.sessionMgr.GetSession(sessionID)
	}
	if decision := m.evaluatePlanToolGate(ctx, session, toolName); !decision.Allowed {
		return fmt.Errorf("plan mode gate denied: %s", decision.Reason)
	}
	return nil
}

func (m *Master) applyPlanToolStateAfterSuccess(session *SessionState, toolName, toolCallID string) {
	if session == nil {
		return
	}
	var (
		handled    = true
		planMode   bool
		planStatus sessiontodo.PlanStatus
	)
	switch toolName {
	case "enter_plan_mode":
		planMode = true
		planStatus = sessiontodo.PlanStatusPlanning
	case "exit_plan_mode":
		planMode = false
		planStatus = sessiontodo.PlanStatusExecuting
	case "finish_plan":
		planMode = false
		planStatus = sessiontodo.PlanStatusCompleted
	default:
		handled = false
	}
	if !handled {
		return
	}

	session.mu.Lock()
	session.PlanMode = planMode
	session.PlanStatus = planStatus
	session.mu.Unlock()

	if m.logger != nil {
		m.logger.Info("plan runtime state changed by tool",
			zap.String("session_id", session.ID),
			zap.String("tool_name", toolName),
			zap.String("tool_call_id", toolCallID),
			zap.Bool("plan_mode", planMode),
			zap.String("plan_status", string(planStatus)),
		)
	}
	m.recordPlanStatusTransition(session.ID, "", planStatus, "", "", toolCallID, "tool")
	if m.eventBus != nil {
		m.eventBus.BroadcastSessionMessage(session.ID, BroadcastMessage{
			Type: EventTypePlanModeChanged,
			Payload: map[string]any{
				"session_id":   session.ID,
				"plan_mode":    planMode,
				"plan_status":  string(planStatus),
				"tool_name":    toolName,
				"tool_call_id": toolCallID,
			},
		})
	}
}

func (m *Master) RecordTodoWrite(ctx context.Context, event tools.TodoWriteObservation) {
	if m == nil {
		return
	}
	if event.Source == "" {
		event.Source = "agent"
	}
	if event.StartedAt.IsZero() {
		event.StartedAt = time.Now()
	}
	status := event.Status
	if status == "" {
		status = "ok"
	}
	spanStatus := "ok"
	if status != "ok" {
		spanStatus = "error"
	}
	attrs := map[string]any{
		"source":                event.Source,
		"status":                status,
		"expected_plan_version": event.ExpectedPlanVersion,
		"plan_version":          event.PlanVersion,
		"todo_count":            event.TodoCount,
		"source_tool_call_id":   event.ToolCallID,
	}
	if event.Error != "" {
		attrs["error"] = event.Error
	}
	if status == "conflict" {
		attrs["conflict_expected"] = event.ConflictExpected
		attrs["conflict_got"] = event.ConflictGot
		m.enqueueMetric(observability.Metric{
			Name:  "hive_sessiontodo_version_conflicts_total",
			Value: 1,
			Labels: map[string]any{
				"source": event.Source,
			},
			Ts: time.Now(),
		})
	}
	m.enqueueMetric(observability.Metric{
		Name:  "hive_sessiontodo_writes_total",
		Value: 1,
		Labels: map[string]any{
			"source": event.Source,
			"status": status,
		},
		Ts: time.Now(),
	})
	m.enqueueSpan(observability.Span{
		TraceID:      event.TraceID,
		SpanID:       event.SpanID,
		ParentSpanID: event.ParentSpanID,
		Operation:    "todo_write.execute",
		Service:      "tools",
		SessionID:    event.SessionID,
		DurationMs:   int(event.Duration.Milliseconds()),
		Status:       spanStatus,
		Attributes:   attrs,
		Ts:           event.StartedAt,
	})
	m.enqueueSpan(observability.Span{
		TraceID:      event.TraceID,
		SpanID:       observability.NewSpanID(),
		ParentSpanID: event.SpanID,
		Operation:    "sessiontodo.replace",
		Service:      "sessiontodo",
		SessionID:    event.SessionID,
		DurationMs:   int(event.Duration.Milliseconds()),
		Status:       spanStatus,
		Attributes:   attrs,
		Ts:           event.StartedAt,
	})
	level := "info"
	message := "todo_write completed"
	if status == "conflict" {
		level = "warn"
		message = "todo_write plan_version conflict"
	} else if status != "ok" {
		level = "error"
		message = "todo_write failed"
	}
	m.enqueueLog(observability.LogEntry{
		Level:     level,
		Message:   message,
		TraceID:   event.TraceID,
		SpanID:    event.SpanID,
		SessionID: event.SessionID,
		Attributes: map[string]any{
			"source":                event.Source,
			"status":                status,
			"plan_version":          event.PlanVersion,
			"expected_plan_version": event.ExpectedPlanVersion,
			"source_tool_call_id":   event.ToolCallID,
			"error_code":            planRuntimeErrorCode(status),
			"error":                 event.Error,
		},
		Ts: time.Now(),
	})
	_ = ctx
}

func (m *Master) RecordPlanTool(ctx context.Context, event tools.PlanToolObservation) {
	if m == nil {
		return
	}
	if event.StartedAt.IsZero() {
		event.StartedAt = time.Now()
	}
	if event.Operation == "" {
		event.Operation = event.ToolName + ".execute"
	}
	status := event.Status
	if status == "" {
		status = "ok"
	}
	spanStatus := "ok"
	if status != "ok" {
		spanStatus = "error"
	}
	attrs := map[string]any{
		"tool_name":           event.ToolName,
		"plan_status":         string(event.PlanStatus),
		"plan_version":        event.PlanVersion,
		"todo_count":          event.TodoCount,
		"open_todo_count":     event.OpenTodoCount,
		"source_tool_call_id": event.ToolCallID,
	}
	if event.Error != "" {
		attrs["error"] = event.Error
	}
	m.enqueueSpan(observability.Span{
		TraceID:      event.TraceID,
		SpanID:       event.SpanID,
		ParentSpanID: event.ParentSpanID,
		Operation:    event.Operation,
		Service:      "tools",
		SessionID:    event.SessionID,
		DurationMs:   int(event.Duration.Milliseconds()),
		Status:       spanStatus,
		Attributes:   attrs,
		Ts:           event.StartedAt,
	})
	m.enqueueLog(observability.LogEntry{
		Level:     planRuntimeLogLevel(status),
		Message:   event.Operation + " " + status,
		TraceID:   event.TraceID,
		SpanID:    event.SpanID,
		SessionID: event.SessionID,
		Attributes: map[string]any{
			"tool_name":           event.ToolName,
			"plan_status":         string(event.PlanStatus),
			"plan_version":        event.PlanVersion,
			"source_tool_call_id": event.ToolCallID,
			"error_code":          planRuntimeErrorCode(status),
			"error":               event.Error,
		},
		Ts: time.Now(),
	})
	_ = ctx
}

func (m *Master) recordPlanStatusTransition(sessionID string, from, to sessiontodo.PlanStatus, traceID, spanID, toolCallID, source string) {
	if m == nil || to == "" {
		return
	}
	if source == "" {
		source = "runtime"
	}
	m.enqueueMetric(observability.Metric{
		Name:  "hive_sessiontodo_plan_status_transitions_total",
		Value: 1,
		Labels: map[string]any{
			"from": string(from),
			"to":   string(to),
		},
		Ts: time.Now(),
	})
	m.enqueueLog(observability.LogEntry{
		Level:     "info",
		Message:   "session todo plan status changed",
		TraceID:   traceID,
		SpanID:    spanID,
		SessionID: sessionID,
		Attributes: map[string]any{
			"plan_status_from":    string(from),
			"plan_status_to":      string(to),
			"source":              source,
			"source_tool_call_id": toolCallID,
		},
		Ts: time.Now(),
	})
}

func planRuntimeLogLevel(status string) string {
	if status == "ok" || status == "" {
		return "info"
	}
	if status == "conflict" {
		return "warn"
	}
	return "error"
}

func planRuntimeErrorCode(status string) string {
	switch status {
	case "conflict":
		return "plan_version_conflict"
	case "ok", "":
		return ""
	default:
		return status
	}
}

type CompletionDecision struct {
	Status    TaskStatus
	Completed bool
	Message   string
}

func (d CompletionDecision) TaskResponse(content string) TaskResponse {
	resp := NewTaskResponse(content, d.Status)
	if d.Message != "" {
		resp.Message = d.Message
	}
	return resp
}

type PlanRuntimeGuard struct {
	store sessiontodo.Store
	m     *Master
}

func NewPlanRuntimeGuard(store sessiontodo.Store, m *Master) *PlanRuntimeGuard {
	return &PlanRuntimeGuard{store: store, m: m}
}

func (g *PlanRuntimeGuard) DecideTurnCompletion(ctx context.Context, session *SessionState, llmContent, traceID, parentSpanID string) (CompletionDecision, error) {
	if session == nil {
		return CompletionDecision{Status: TaskStatusCompleted, Completed: true}, nil
	}
	start := time.Now()
	spanID := observability.NewSpanID()
	sessionID := session.ID

	snapshot := sessiontodo.Snapshot{SessionID: sessionID, PlanStatus: sessiontodo.PlanStatusNone}
	var err error
	if g != nil && g.store != nil {
		snapshot, err = g.store.Snapshot(ctx, sessionID)
		if err != nil {
			g.emitDecisionObs(traceID, spanID, parentSpanID, sessionID, snapshot.PlanStatus, TaskStatusFailed, start, "error", err)
			return CompletionDecision{}, err
		}
	}

	status := snapshot.PlanStatus
	session.mu.RLock()
	if status == "" || status == sessiontodo.PlanStatusNone {
		status = session.PlanStatus
	}
	planMode := session.PlanMode
	session.mu.RUnlock()
	if status == "" {
		status = sessiontodo.PlanStatusNone
	}
	if g == nil || g.store == nil {
		snapshot.PlanStatus = status
	}

	decision := decideCompletionFromSnapshot(status, planMode, snapshot)
	var nextPlanStatus sessiontodo.PlanStatus
	session.mu.Lock()
	switch decision.Status {
	case TaskStatusPaused:
		nextPlanStatus = sessiontodo.PlanStatusPaused
		session.PlanStatus = nextPlanStatus
	case TaskStatusCompleted:
		if status != sessiontodo.PlanStatusNone || planMode {
			nextPlanStatus = sessiontodo.PlanStatusCompleted
			session.PlanStatus = nextPlanStatus
			session.PlanMode = false
		}
	case TaskStatusFailed:
		nextPlanStatus = sessiontodo.PlanStatusFailed
		session.PlanStatus = nextPlanStatus
	}
	session.mu.Unlock()
	if nextPlanStatus != "" && g != nil && g.store != nil && status != nextPlanStatus {
		updated, setErr := g.store.SetPlanStatus(ctx, sessionID, nextPlanStatus)
		if setErr != nil {
			g.emitDecisionObs(traceID, spanID, parentSpanID, sessionID, status, TaskStatusFailed, start, "error", setErr)
			return CompletionDecision{}, setErr
		}
		if g.m != nil {
			g.m.recordPlanStatusTransition(sessionID, status, nextPlanStatus, traceID, spanID, "", "plan_runtime")
		}
		snapshot = updated
		status = updated.PlanStatus
		if err := g.broadcastSnapshot(ctx, updated); err != nil {
			g.emitDecisionObs(traceID, spanID, parentSpanID, sessionID, status, TaskStatusFailed, start, "error", err)
			return CompletionDecision{}, err
		}
	}
	g.emitDecisionObs(traceID, spanID, parentSpanID, sessionID, status, decision.Status, start, "ok", nil)
	if g != nil && g.m != nil && g.m.logger != nil {
		g.m.logger.Info("plan runtime guard decided turn completion",
			zap.String("session_id", sessionID),
			zap.String("plan_status", string(status)),
			zap.String("decision", string(decision.Status)),
			zap.Int("todos", len(snapshot.Todos)),
			zap.Int("content_len", len(llmContent)),
		)
	}
	return decision, nil
}

func decideCompletionFromSnapshot(status sessiontodo.PlanStatus, planMode bool, snapshot sessiontodo.Snapshot) CompletionDecision {
	switch status {
	case sessiontodo.PlanStatusFailed:
		return CompletionDecision{Status: TaskStatusFailed, Completed: false}
	case sessiontodo.PlanStatusPlanning, sessiontodo.PlanStatusAwaitingApproval:
		return pausedDecision()
	case sessiontodo.PlanStatusExecuting, sessiontodo.PlanStatusPaused:
		if todosComplete(snapshot.Todos) {
			return CompletionDecision{Status: TaskStatusCompleted, Completed: true}
		}
		return pausedDecision()
	case sessiontodo.PlanStatusCompleted:
		return CompletionDecision{Status: TaskStatusCompleted, Completed: true}
	case sessiontodo.PlanStatusNone:
		if planMode {
			return pausedDecision()
		}
		return CompletionDecision{Status: TaskStatusCompleted, Completed: true}
	default:
		return CompletionDecision{Status: TaskStatusCompleted, Completed: true}
	}
}

func pausedDecision() CompletionDecision {
	return CompletionDecision{
		Status:    TaskStatusPaused,
		Completed: false,
		Message:   "Paused · Send a message to continue",
	}
}

func todosComplete(todos []sessiontodo.Todo) bool {
	if len(todos) == 0 {
		return false
	}
	for _, todo := range todos {
		if todo.Status != sessiontodo.TodoStatusCompleted && todo.Status != sessiontodo.TodoStatusCancelled {
			return false
		}
	}
	return true
}

func (g *PlanRuntimeGuard) emitDecisionObs(traceID, spanID, parentSpanID, sessionID string, planStatus sessiontodo.PlanStatus, decision TaskStatus, start time.Time, spanStatus string, err error) {
	if g == nil || g.m == nil {
		return
	}
	attrs := map[string]any{
		"plan_status": string(planStatus),
		"decision":    string(decision),
	}
	if err != nil {
		attrs["error"] = err.Error()
	}
	g.m.enqueueSpan(observability.Span{
		TraceID:      traceID,
		SpanID:       spanID,
		ParentSpanID: parentSpanID,
		Operation:    "plan_runtime.decide",
		Service:      "master",
		SessionID:    sessionID,
		DurationMs:   int(time.Since(start).Milliseconds()),
		Status:       spanStatus,
		Attributes:   attrs,
		Ts:           start,
	})
	g.m.enqueueMetric(observability.Metric{
		Name:  "hive_plan_runtime_decisions_total",
		Value: 1,
		Labels: map[string]any{
			"plan_status": string(planStatus),
			"decision":    string(decision),
		},
		Ts: time.Now(),
	})
}

func (g *PlanRuntimeGuard) broadcastSnapshot(ctx context.Context, snapshot sessiontodo.Snapshot) error {
	if g == nil || g.m == nil {
		return nil
	}
	return g.m.BroadcastTodoSnapshot(ctx, snapshot)
}

func (m *Master) planRuntimeGuard() *PlanRuntimeGuard {
	if m == nil {
		return nil
	}
	return NewPlanRuntimeGuard(m.sessionTodoStore, m)
}

func (m *Master) SetSessionTodoStore(store sessiontodo.Store) {
	m.sessionTodoStore = store
}

func (m *Master) BroadcastTodoSnapshot(ctx context.Context, snapshot sessiontodo.Snapshot) error {
	if m == nil || m.eventBus == nil {
		return nil
	}
	if err := ctx.Err(); err != nil {
		m.enqueueMetric(observability.Metric{
			Name:   "hive_todo_snapshot_broadcast_total",
			Value:  1,
			Labels: map[string]any{"status": "error"},
			Ts:     time.Now(),
		})
		m.enqueueLog(observability.LogEntry{
			Level:     "error",
			Message:   "todo_snapshot broadcast failed",
			TraceID:   snapshot.TraceID,
			SpanID:    snapshot.SpanID,
			SessionID: snapshot.SessionID,
			Attributes: map[string]any{
				"plan_status":  string(snapshot.PlanStatus),
				"plan_version": snapshot.PlanVersion,
				"error":        err.Error(),
			},
			Ts: time.Now(),
		})
		return err
	}
	m.eventBus.BroadcastSessionMessage(snapshot.SessionID, BroadcastMessage{
		Type:    EventTypeTodoSnapshot,
		Payload: snapshot,
	})
	m.enqueueMetric(observability.Metric{
		Name:   "hive_todo_snapshot_broadcast_total",
		Value:  1,
		Labels: map[string]any{"status": "ok"},
		Ts:     time.Now(),
	})
	m.enqueueLog(observability.LogEntry{
		Level:     "info",
		Message:   "todo_snapshot broadcasted",
		TraceID:   snapshot.TraceID,
		SpanID:    snapshot.SpanID,
		SessionID: snapshot.SessionID,
		Attributes: map[string]any{
			"plan_status":  string(snapshot.PlanStatus),
			"plan_version": snapshot.PlanVersion,
			"todo_count":   len(snapshot.Todos),
		},
		Ts: time.Now(),
	})
	return nil
}
