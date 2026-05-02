package master

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/chef-guo/agents-hive/internal/llm"
	"github.com/chef-guo/agents-hive/internal/mcphost"
	"github.com/chef-guo/agents-hive/internal/sessiontodo"
	"github.com/chef-guo/agents-hive/internal/toolctx"
	"github.com/chef-guo/agents-hive/internal/tools"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestPlanRuntime_EventCriticality(t *testing.T) {
	assert.False(t, isCriticalEvent(EventTypeTodoSnapshot), "todo snapshot 可从 API 恢复，不应走关键事件重试")
	assert.True(t, isCriticalEvent(EventTypePlanModeChanged), "plan mode 切换改变用户可见状态，必须可靠送达")
}

func TestTaskResponseStatusCompat(t *testing.T) {
	completed := NewTaskResponse("done", TaskStatusCompleted)
	assert.Equal(t, string(TaskStatusCompleted), completed.Status)
	assert.True(t, completed.Completed)

	paused := NewTaskResponse("wait", TaskStatusPaused)
	assert.Equal(t, string(TaskStatusPaused), paused.Status)
	assert.False(t, paused.Completed)

	legacy := NormalizeTaskResponse(TaskResponse{Content: "legacy", Completed: true})
	assert.Equal(t, string(TaskStatusCompleted), legacy.Status)
	assert.True(t, legacy.Completed)

	derived := NormalizeTaskResponse(TaskResponse{Status: string(TaskStatusPaused), Completed: true})
	assert.False(t, derived.Completed, "Status 是新权威字段，Completed 必须从 Status 派生")
}

func TestPlanRuntimeGuardDecideTurnCompletion(t *testing.T) {
	ctx := context.Background()
	store := sessiontodo.NewMemoryStore()
	m := &Master{obsCh: make(chan observabilityEntry, 8), eventBus: NewEventBus(zap.NewNop())}
	t.Cleanup(func() { m.eventBus.Close() })
	_, events := m.eventBus.Subscribe()
	guard := NewPlanRuntimeGuard(store, m)

	noneSession := &SessionState{ID: "no-plan"}
	decision, err := guard.DecideTurnCompletion(ctx, noneSession, "plain answer", "trace-1", "span-1")
	require.NoError(t, err)
	assert.Equal(t, TaskStatusCompleted, decision.Status)
	assert.True(t, decision.Completed)

	activeSnapshot, err := store.SetPlanStatus(ctx, "active", sessiontodo.PlanStatusExecuting)
	require.NoError(t, err)
	activeSnapshot, err = store.Replace(ctx, "active", activeSnapshot.PlanVersion, []sessiontodo.TodoInput{
		{ID: "read", Content: "read context", Status: sessiontodo.TodoStatusCompleted},
		{ID: "write", Content: "write code", Status: sessiontodo.TodoStatusPending},
	})
	require.NoError(t, err)
	activeSession := &SessionState{ID: "active", PlanMode: true, PlanStatus: sessiontodo.PlanStatusExecuting}

	decision, err = guard.DecideTurnCompletion(ctx, activeSession, "stopping here", "trace-2", "span-2")
	require.NoError(t, err)
	assert.Equal(t, TaskStatusPaused, decision.Status)
	assert.False(t, decision.Completed, "active plan 未完成时不能把自然语言终态回答标 completed")
	assert.Contains(t, decision.Message, "Send a message to continue")
	pausedSnapshot, err := store.Snapshot(ctx, "active")
	require.NoError(t, err)
	assert.Equal(t, sessiontodo.PlanStatusPaused, pausedSnapshot.PlanStatus)
	assert.Equal(t, activeSnapshot.PlanVersion+1, pausedSnapshot.PlanVersion)
	assertTodoSnapshotEvent(t, events, sessiontodo.PlanStatusPaused, pausedSnapshot.PlanVersion)

	completedInputSnapshot, err := store.Replace(ctx, "active", pausedSnapshot.PlanVersion, []sessiontodo.TodoInput{
		{ID: "read", Content: "read context", Status: sessiontodo.TodoStatusCompleted},
		{ID: "write", Content: "write code", Status: sessiontodo.TodoStatusCompleted},
	})
	require.NoError(t, err)
	decision, err = guard.DecideTurnCompletion(ctx, activeSession, "done", "trace-3", "span-3")
	require.NoError(t, err)
	assert.Equal(t, TaskStatusCompleted, decision.Status)
	assert.True(t, decision.Completed)
	completedSnapshot, err := store.Snapshot(ctx, "active")
	require.NoError(t, err)
	assert.Equal(t, sessiontodo.PlanStatusCompleted, completedSnapshot.PlanStatus)
	assert.Equal(t, completedInputSnapshot.PlanVersion+1, completedSnapshot.PlanVersion)
	assertTodoSnapshotEvent(t, events, sessiontodo.PlanStatusCompleted, completedSnapshot.PlanVersion)
	assertPlanRuntimeObs(t, m)
}

func TestPlanRuntimeGuard_UsesSessionStateWithoutStore(t *testing.T) {
	m := &Master{obsCh: make(chan observabilityEntry, 4)}
	guard := NewPlanRuntimeGuard(nil, m)
	session := &SessionState{
		ID:         "session-only",
		PlanMode:   true,
		PlanStatus: sessiontodo.PlanStatusExecuting,
	}

	decision, err := guard.DecideTurnCompletion(context.Background(), session, "stopping here", "trace", "span")

	require.NoError(t, err)
	assert.Equal(t, TaskStatusPaused, decision.Status)
	assert.False(t, decision.Completed)
}

func TestExecuteTool_InjectsTraceContextBeforeExecution(t *testing.T) {
	m := newPhase6MasterWithMCPHost(t)

	var captured PlanToolTraceContext
	var capturedToolContext *toolctx.ToolContext
	m.mcpHost.RegisterTool(
		mcphost.ToolDefinition{Name: "capture_trace", Description: "test"},
		func(ctx context.Context, input json.RawMessage) (*mcphost.ToolResult, error) {
			captured = PlanToolTraceFromContext(ctx)
			capturedToolContext = toolctx.GetToolContext(ctx)
			return &mcphost.ToolResult{Content: jsonTestText("ok")}, nil
		},
	)

	session := newTestSession("trace-session")
	tc := llm.ToolCall{ID: "tool-call-1", Name: "capture_trace", Arguments: json.RawMessage(`{}`)}
	result := m.executeTool(context.Background(), session, "user-1", tc, "trace-root", "parent-span")

	require.False(t, result.IsError)
	assert.Equal(t, "trace-root", captured.TraceID)
	assert.NotEmpty(t, captured.SpanID)
	assert.Equal(t, "parent-span", captured.ParentSpanID)
	assert.Equal(t, "tool-call-1", captured.ToolCallID)
	require.NotNil(t, capturedToolContext)
	assert.Equal(t, captured.TraceID, capturedToolContext.TraceID)
	assert.Equal(t, captured.SpanID, capturedToolContext.SpanID)
	assert.Equal(t, captured.ParentSpanID, capturedToolContext.ParentSpanID)
	assert.Equal(t, captured.ToolCallID, capturedToolContext.ToolCallID)
}

func TestExecuteToolGate_BlocksPlanModeWriteTools(t *testing.T) {
	m := newPhase6MasterWithMCPHost(t)

	called := false
	m.mcpHost.RegisterTool(
		mcphost.ToolDefinition{Name: "write_file", Description: "test"},
		func(ctx context.Context, input json.RawMessage) (*mcphost.ToolResult, error) {
			called = true
			return &mcphost.ToolResult{Content: jsonTestText("wrote")}, nil
		},
	)

	session := newTestSession("plan-gate")
	session.PlanMode = true
	session.PlanStatus = sessiontodo.PlanStatusPlanning
	tc := llm.ToolCall{ID: "write-1", Name: "write_file", Arguments: json.RawMessage(`{}`)}

	result := m.executeTool(context.Background(), session, "", tc, "trace", "span")

	assert.True(t, result.IsError)
	assert.True(t, result.Terminal)
	assert.False(t, called, "gate 必须在工具执行前拒绝")
	assert.Contains(t, result.Content, "plan mode")
}

func TestExecuteTool_SyncsPlanModeStateAfterPlanToolSuccess(t *testing.T) {
	m := newPhase6MasterWithMCPHost(t)
	m.eventBus = NewEventBus(zap.NewNop())
	t.Cleanup(func() { m.eventBus.Close() })
	_, events := m.eventBus.Subscribe()

	m.mcpHost.RegisterTool(
		mcphost.ToolDefinition{Name: "enter_plan_mode", Description: "test"},
		func(ctx context.Context, input json.RawMessage) (*mcphost.ToolResult, error) {
			return &mcphost.ToolResult{Content: jsonTestText("ok")}, nil
		},
	)

	session := newTestSession("plan-sync")
	result := m.executeTool(context.Background(), session, "", llm.ToolCall{
		ID:        "plan-call-1",
		Name:      "enter_plan_mode",
		Arguments: json.RawMessage(`{}`),
	}, "trace", "span")

	require.False(t, result.IsError)
	assert.True(t, session.PlanMode)
	assert.Equal(t, sessiontodo.PlanStatusPlanning, session.PlanStatus)

	select {
	case msg := <-events:
		assert.Equal(t, EventTypeToolCall, msg.Type)
	case <-time.After(100 * time.Millisecond):
		t.Fatal("expected tool start event before plan mode changed")
	}
	foundPlanEvent := false
	deadline := time.After(200 * time.Millisecond)
	for !foundPlanEvent {
		select {
		case msg := <-events:
			if msg.Type == EventTypePlanModeChanged {
				foundPlanEvent = true
			}
		case <-deadline:
			t.Fatal("expected plan_mode_changed event")
		}
	}
}

func TestPlanToolGate_SubAgentCannotCallPlanControlTools(t *testing.T) {
	session := &SessionState{ID: "s1"}
	ctx := toolctx.WithToolContext(context.Background(), &toolctx.ToolContext{CallerType: toolctx.CallerSubAgent, CallerName: "worker"})

	for _, name := range []string{"todo_write", "finish_plan", "enter_plan_mode", "exit_plan_mode"} {
		decision := EvaluatePlanToolGate(ctx, session, name)
		assert.False(t, decision.Allowed, "subagent must not call %s", name)
		assert.Contains(t, decision.Reason, "subagent")
	}
}

func TestPlanRuntimeObserver_EmitsTodoWriteMetricsAndSpans(t *testing.T) {
	m := &Master{obsCh: make(chan observabilityEntry, 16)}

	m.RecordTodoWrite(context.Background(), tools.TodoWriteObservation{
		SessionID:           "sess-obs",
		Source:              "agent",
		Status:              "conflict",
		TraceID:             "trace-obs",
		SpanID:              "span-obs",
		ParentSpanID:        "parent-obs",
		ToolCallID:          "call-obs",
		ExpectedPlanVersion: 2,
		PlanVersion:         3,
		TodoCount:           1,
		Error:               "plan_version conflict",
		ConflictExpected:    2,
		ConflictGot:         3,
		StartedAt:           time.Now(),
		Duration:            time.Millisecond,
	})

	assertObsMetric(t, m, "hive_sessiontodo_version_conflicts_total", map[string]any{"source": "agent"})
	assertObsMetric(t, m, "hive_sessiontodo_writes_total", map[string]any{"source": "agent", "status": "conflict"})
	assertObsSpan(t, m, "todo_write.execute")
	assertObsSpan(t, m, "sessiontodo.replace")
	assertObsLog(t, m, "todo_write plan_version conflict")
}

func TestPlanRuntimeObserver_EmitsPlanToolSpan(t *testing.T) {
	m := &Master{obsCh: make(chan observabilityEntry, 8)}

	m.RecordPlanTool(context.Background(), tools.PlanToolObservation{
		ToolName:     "finish_plan",
		Operation:    "finish_plan.execute",
		SessionID:    "sess-obs",
		Status:       "ok",
		PlanStatus:   sessiontodo.PlanStatusCompleted,
		PlanVersion:  7,
		TodoCount:    2,
		TraceID:      "trace-obs",
		SpanID:       "span-obs",
		ParentSpanID: "parent-obs",
		ToolCallID:   "call-obs",
		StartedAt:    time.Now(),
		Duration:     time.Millisecond,
	})

	assertObsSpan(t, m, "finish_plan.execute")
	assertObsLog(t, m, "finish_plan.execute ok")
}

func TestBroadcastTodoSnapshot_EmitsMetricAndLog(t *testing.T) {
	m := &Master{obsCh: make(chan observabilityEntry, 8), eventBus: NewEventBus(zap.NewNop())}
	t.Cleanup(func() { m.eventBus.Close() })

	err := m.BroadcastTodoSnapshot(context.Background(), sessiontodo.Snapshot{
		SessionID:   "sess-obs",
		PlanStatus:  sessiontodo.PlanStatusExecuting,
		PlanVersion: 5,
		Todos:       []sessiontodo.Todo{{ID: "t1", Status: sessiontodo.TodoStatusCompleted}},
		TraceID:     "trace-obs",
		SpanID:      "span-obs",
	})

	require.NoError(t, err)
	assertObsMetric(t, m, "hive_todo_snapshot_broadcast_total", map[string]any{"status": "ok"})
	assertObsLog(t, m, "todo_snapshot broadcasted")
}

func assertPlanRuntimeObs(t *testing.T, m *Master) {
	t.Helper()

	foundSpan := false
	foundMetric := false
	deadline := time.After(200 * time.Millisecond)
	for !foundSpan || !foundMetric {
		select {
		case e := <-m.obsCh:
			if e.span != nil && e.span.Operation == "plan_runtime.decide" {
				foundSpan = true
			}
			if e.metric != nil && e.metric.Name == "hive_plan_runtime_decisions_total" {
				foundMetric = true
			}
		case <-deadline:
			t.Fatalf("plan runtime guard did not emit expected obs entries: span=%v metric=%v", foundSpan, foundMetric)
		}
	}
}

func assertObsMetric(t *testing.T, m *Master, name string, labels map[string]any) {
	t.Helper()
	deadline := time.After(200 * time.Millisecond)
	for {
		select {
		case e := <-m.obsCh:
			if e.metric == nil || e.metric.Name != name {
				continue
			}
			for key, want := range labels {
				require.Equal(t, want, e.metric.Labels[key])
			}
			return
		case <-deadline:
			t.Fatalf("expected metric %s", name)
		}
	}
}

func assertObsSpan(t *testing.T, m *Master, operation string) {
	t.Helper()
	deadline := time.After(200 * time.Millisecond)
	for {
		select {
		case e := <-m.obsCh:
			if e.span != nil && e.span.Operation == operation {
				return
			}
		case <-deadline:
			t.Fatalf("expected span %s", operation)
		}
	}
}

func assertObsLog(t *testing.T, m *Master, message string) {
	t.Helper()
	deadline := time.After(200 * time.Millisecond)
	for {
		select {
		case e := <-m.obsCh:
			if e.log != nil && e.log.Message == message {
				return
			}
		case <-deadline:
			t.Fatalf("expected log %q", message)
		}
	}
}

func assertTodoSnapshotEvent(t *testing.T, events <-chan BroadcastMessage, status sessiontodo.PlanStatus, version int64) {
	t.Helper()

	deadline := time.After(200 * time.Millisecond)
	for {
		select {
		case msg := <-events:
			if msg.Type != EventTypeTodoSnapshot {
				continue
			}
			snapshot, ok := msg.Payload.(sessiontodo.Snapshot)
			require.True(t, ok, "todo_snapshot payload type = %T", msg.Payload)
			if snapshot.PlanStatus == status && snapshot.PlanVersion == version {
				return
			}
		case <-deadline:
			t.Fatalf("expected todo_snapshot status=%s version=%d", status, version)
		}
	}
}
