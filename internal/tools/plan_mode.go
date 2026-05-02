package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/chef-guo/agents-hive/internal/mcphost"
	"github.com/chef-guo/agents-hive/internal/toolctx"
)

const (
	enterPlanModeToolName = "enter_plan_mode"
	exitPlanModeToolName  = "exit_plan_mode"
	finishPlanToolName    = "finish_plan"
)

func registerPlanModeTools(host *mcphost.Host, logger *zap.Logger, store SessionTodoStore, broadcaster TodoSnapshotBroadcaster, observers ...PlanRuntimeObserver) {
	observer := firstPlanRuntimeObserver(observers...)
	registerPlanStatusTool(host, logger, store, broadcaster, enterPlanModeToolName, PlanStatusPlanning,
		"进入规划态，将当前 session 的 plan_status 设置为 planning。仅 Master Agent 可调用。", observer)
	registerPlanStatusTool(host, logger, store, broadcaster, exitPlanModeToolName, PlanStatusExecuting,
		"退出规划态，将当前 session 的 plan_status 设置为 executing。退出规划态不代表计划完成。仅 Master Agent 可调用。", observer)
	registerFinishPlan(host, logger, store, broadcaster, observer)
}

func registerPlanStatusTool(host *mcphost.Host, logger *zap.Logger, store SessionTodoStore, broadcaster TodoSnapshotBroadcaster, name string, status PlanStatus, description string, observer PlanRuntimeObserver) {
	schema, _ := json.Marshal(map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties":           map[string]any{},
	})

	host.RegisterTool(
		mcphost.ToolDefinition{
			Name:              name,
			Description:       description,
			InputSchema:       schema,
			Core:              true,
			IsConcurrencySafe: false,
		},
		func(ctx context.Context, input json.RawMessage) (*mcphost.ToolResult, error) {
			start := time.Now()
			source := sourceFromToolContext(ctx)
			operation := planToolOperation(name)
			if err := requireMasterCaller(ctx, name, logger); err != nil {
				recordPlanToolObservation(ctx, observer, PlanToolObservation{ToolName: name, Operation: operation, Source: source, Status: "error", Error: err.Error(), StartedAt: start, Duration: time.Since(start)})
				return errorResult(err.Error()), nil
			}
			if store == nil {
				recordPlanToolObservation(ctx, observer, PlanToolObservation{ToolName: name, Operation: operation, Source: source, Status: "error", Error: "session todo store 未配置", StartedAt: start, Duration: time.Since(start)})
				return errorResult(fmt.Sprintf("%s 未启用：session todo store 未配置", name)), nil
			}
			if sessionID := toolctx.GetSessionID(ctx); sessionID == "" {
				recordPlanToolObservation(ctx, observer, PlanToolObservation{ToolName: name, Operation: operation, Source: source, Status: "error", Error: "missing sessionID", StartedAt: start, Duration: time.Since(start)})
				return errorResult(fmt.Sprintf("%s 缺少 sessionID：必须由运行时上下文提供", name)), nil
			}
			if err := rejectNonEmptyObject(input, name); err != nil {
				recordPlanToolObservation(ctx, observer, PlanToolObservation{ToolName: name, Operation: operation, Source: source, SessionID: toolctx.GetSessionID(ctx), Status: "error", Error: err.Error(), StartedAt: start, Duration: time.Since(start)})
				return errorResult(err.Error()), nil
			}

			sessionID := toolctx.GetSessionID(ctx)
			snapshot, err := store.SetPlanStatus(ctx, sessionID, status)
			if err != nil {
				logger.Error("plan mode 状态写入失败",
					zap.String("tool_name", name),
					zap.String("session_id", sessionID),
					zap.String("plan_status", string(status)),
					zap.Error(err),
				)
				recordPlanToolObservation(ctx, observer, PlanToolObservation{ToolName: name, Operation: operation, Source: source, SessionID: sessionID, Status: "error", PlanStatus: status, Error: err.Error(), StartedAt: start, Duration: time.Since(start)})
				return errorResult(fmt.Sprintf("%s 写入失败: %v", name, err)), nil
			}
			if err := broadcastTodoSnapshot(ctx, broadcaster, snapshot, logger); err != nil {
				recordPlanToolObservation(ctx, observer, PlanToolObservation{ToolName: name, Operation: operation, Source: source, SessionID: sessionID, Status: "error", PlanStatus: snapshot.PlanStatus, PlanVersion: snapshot.PlanVersion, TodoCount: len(snapshot.Todos), Error: err.Error(), StartedAt: start, Duration: time.Since(start)})
				return errorResult(fmt.Sprintf("%s 广播失败: %v", name, err)), nil
			}
			recordPlanToolObservation(ctx, observer, PlanToolObservation{ToolName: name, Operation: operation, Source: source, SessionID: sessionID, Status: "ok", PlanStatus: snapshot.PlanStatus, PlanVersion: snapshot.PlanVersion, TodoCount: len(snapshot.Todos), StartedAt: start, Duration: time.Since(start)})
			return todoSnapshotResult(snapshot), nil
		},
	)
}

func registerFinishPlan(host *mcphost.Host, logger *zap.Logger, store SessionTodoStore, broadcaster TodoSnapshotBroadcaster, observer PlanRuntimeObserver) {
	schema, _ := json.Marshal(map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties":           map[string]any{},
	})

	host.RegisterTool(
		mcphost.ToolDefinition{
			Name:              finishPlanToolName,
			Description:       "完成当前 active plan。若仍有 pending 或 in_progress todo，会返回工具错误且不改变 plan_status。仅 Master Agent 可调用。",
			InputSchema:       schema,
			Core:              true,
			IsConcurrencySafe: false,
		},
		func(ctx context.Context, input json.RawMessage) (*mcphost.ToolResult, error) {
			start := time.Now()
			source := sourceFromToolContext(ctx)
			if err := requireMasterCaller(ctx, finishPlanToolName, logger); err != nil {
				recordPlanToolObservation(ctx, observer, PlanToolObservation{ToolName: finishPlanToolName, Operation: "finish_plan.execute", Source: source, Status: "error", Error: err.Error(), StartedAt: start, Duration: time.Since(start)})
				return errorResult(err.Error()), nil
			}
			if store == nil {
				recordPlanToolObservation(ctx, observer, PlanToolObservation{ToolName: finishPlanToolName, Operation: "finish_plan.execute", Source: source, Status: "error", Error: "session todo store 未配置", StartedAt: start, Duration: time.Since(start)})
				return errorResult("finish_plan 未启用：session todo store 未配置"), nil
			}
			sessionID := toolctx.GetSessionID(ctx)
			if sessionID == "" {
				recordPlanToolObservation(ctx, observer, PlanToolObservation{ToolName: finishPlanToolName, Operation: "finish_plan.execute", Source: source, Status: "error", Error: "missing sessionID", StartedAt: start, Duration: time.Since(start)})
				return errorResult("finish_plan 缺少 sessionID：必须由运行时上下文提供"), nil
			}
			if err := rejectNonEmptyObject(input, finishPlanToolName); err != nil {
				recordPlanToolObservation(ctx, observer, PlanToolObservation{ToolName: finishPlanToolName, Operation: "finish_plan.execute", Source: source, SessionID: sessionID, Status: "error", Error: err.Error(), StartedAt: start, Duration: time.Since(start)})
				return errorResult(err.Error()), nil
			}

			current, err := store.Snapshot(ctx, sessionID)
			if err != nil {
				logger.Error("finish_plan 读取 snapshot 失败",
					zap.String("session_id", sessionID),
					zap.Error(err),
				)
				recordPlanToolObservation(ctx, observer, PlanToolObservation{ToolName: finishPlanToolName, Operation: "finish_plan.execute", Source: source, SessionID: sessionID, Status: "error", Error: err.Error(), StartedAt: start, Duration: time.Since(start)})
				return errorResult("finish_plan 读取 snapshot 失败: " + err.Error()), nil
			}
			openTodos := pendingOrInProgressTodos(current.Todos)
			if len(openTodos) > 0 {
				recordPlanToolObservation(ctx, observer, PlanToolObservation{ToolName: finishPlanToolName, Operation: "finish_plan.execute", Source: source, SessionID: sessionID, Status: "error", PlanStatus: current.PlanStatus, PlanVersion: current.PlanVersion, TodoCount: len(current.Todos), OpenTodoCount: len(openTodos), Error: "open todos remain", StartedAt: start, Duration: time.Since(start)})
				return errorResult(fmt.Sprintf("finish_plan 失败：仍有未完成 todo: %s", strings.Join(openTodos, ", "))), nil
			}

			snapshot, err := store.SetPlanStatus(ctx, sessionID, PlanStatusCompleted)
			if err != nil {
				logger.Error("finish_plan 写入 completed 失败",
					zap.String("session_id", sessionID),
					zap.Error(err),
				)
				recordPlanToolObservation(ctx, observer, PlanToolObservation{ToolName: finishPlanToolName, Operation: "finish_plan.execute", Source: source, SessionID: sessionID, Status: "error", PlanStatus: PlanStatusCompleted, Error: err.Error(), StartedAt: start, Duration: time.Since(start)})
				return errorResult("finish_plan 写入失败: " + err.Error()), nil
			}
			if err := broadcastTodoSnapshot(ctx, broadcaster, snapshot, logger); err != nil {
				recordPlanToolObservation(ctx, observer, PlanToolObservation{ToolName: finishPlanToolName, Operation: "finish_plan.execute", Source: source, SessionID: sessionID, Status: "error", PlanStatus: snapshot.PlanStatus, PlanVersion: snapshot.PlanVersion, TodoCount: len(snapshot.Todos), Error: err.Error(), StartedAt: start, Duration: time.Since(start)})
				return errorResult("finish_plan 广播失败: " + err.Error()), nil
			}
			recordPlanToolObservation(ctx, observer, PlanToolObservation{ToolName: finishPlanToolName, Operation: "finish_plan.execute", Source: source, SessionID: sessionID, Status: "ok", PlanStatus: snapshot.PlanStatus, PlanVersion: snapshot.PlanVersion, TodoCount: len(snapshot.Todos), StartedAt: start, Duration: time.Since(start)})
			return todoSnapshotResult(snapshot), nil
		},
	)
}

func planToolOperation(name string) string {
	switch name {
	case enterPlanModeToolName:
		return "plan_mode.enter"
	case exitPlanModeToolName:
		return "plan_mode.exit"
	default:
		return name + ".execute"
	}
}

func recordPlanToolObservation(ctx context.Context, observer PlanRuntimeObserver, event PlanToolObservation) {
	if observer == nil {
		return
	}
	if event.Source.Source == "" {
		event.Source.Source = "agent"
	}
	if event.TraceID == "" {
		event.TraceID = event.Source.TraceID
	}
	if event.SpanID == "" {
		event.SpanID = event.Source.SpanID
	}
	if event.ParentSpanID == "" {
		event.ParentSpanID = event.Source.ParentSpanID
	}
	if event.ToolCallID == "" {
		event.ToolCallID = event.Source.SourceToolCallID
	}
	observer.RecordPlanTool(ctx, event)
}

func pendingOrInProgressTodos(todos []SessionTodo) []string {
	open := make([]string, 0)
	for _, todo := range todos {
		if todo.Status != TodoStatusPending && todo.Status != TodoStatusInProgress {
			continue
		}
		label := strings.TrimSpace(todo.ID)
		if label == "" {
			label = strings.TrimSpace(todo.Content)
		}
		if label == "" {
			label = "<unnamed>"
		}
		open = append(open, label)
	}
	return open
}

func rejectNonEmptyObject(input json.RawMessage, toolName string) error {
	if len(strings.TrimSpace(string(input))) == 0 {
		return nil
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(input, &obj); err != nil {
		return fmt.Errorf("输入无效: %w", err)
	}
	if len(obj) > 0 {
		return fmt.Errorf("%s 不接受任何输入参数", toolName)
	}
	return nil
}
