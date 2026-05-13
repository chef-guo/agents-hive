package master

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/chef-guo/agents-hive/internal/agentquality"
	"github.com/chef-guo/agents-hive/internal/llm"
	"github.com/chef-guo/agents-hive/internal/mcphost"
	"github.com/chef-guo/agents-hive/internal/skills"
	"github.com/chef-guo/agents-hive/internal/toolctx"
)

func actionGuardFingerprint(toolName string, args json.RawMessage) string {
	return strings.TrimSpace(strings.ToLower(toolName)) + ":" + hashToolArgs(args)
}

func (m *Master) guardToolExecution(ctx context.Context, session *SessionState, sessionID, userID, toolCallID, toolName string, args json.RawMessage, sessionTraceID, sessionSpanID string, approved map[string]bool) (toolResult, bool) {
	if m.config.SecurityPermissionMode == "strict" {
		if m.hitlBroker == nil || !m.hitlBroker.Enabled() {
			content := "[权限拒绝: strict 权限模式需要 HITL，但 HITL 未启用]"
			m.recordStrictPermissionBlocked(session, sessionID, userID, toolCallID, toolName, args, sessionTraceID, sessionSpanID, "strict_hitl_disabled")
			return m.actionGuardErrorResult(ctx, sessionID, toolCallID, toolName, args, sessionTraceID, content, "strict_hitl_disabled"), false
		}
		fingerprint := actionGuardFingerprint(toolName, args)
		if approved != nil && approved[fingerprint] {
			return toolResult{}, true
		}
		tr, ok := m.enforceToolExecutionGate(ctx, session, sessionID, toolCallID, toolName, args, sessionTraceID, sessionSpanID)
		if ok && approved != nil {
			approved[fingerprint] = true
		}
		return tr, ok
	}
	if !m.config.ActionGuardEnabled {
		return m.enforceToolExecutionGate(ctx, session, sessionID, toolCallID, toolName, args, sessionTraceID, sessionSpanID)
	}

	// ActionGuard 接管 Master 主路径权限判断；这里仍复用原 runtime allow-list，
	// 但跳过 legacy PermissionManager，避免 shell / 外发动作双审批。
	routeCtx := toolctx.WithSkipPermission(ctx)
	if tr, ok := m.enforceToolExecutionGate(routeCtx, session, sessionID, toolCallID, toolName, args, sessionTraceID, sessionSpanID); !ok {
		return tr, false
	}

	fingerprint := actionGuardFingerprint(toolName, args)
	if approved != nil && approved[fingerprint] {
		return toolResult{}, true
	}

	start := time.Now()
	decision := newDeterministicActionGuard().Decide(ctx, ActionGuardInput{
		SessionID:    sessionID,
		UserID:       userID,
		ToolCallID:   toolCallID,
		ToolName:     toolName,
		Arguments:    args,
		SafeExecutor: m.safeExecutor.Load(),
		ToolDef:      m.actionGuardToolDefinition(toolName),
	})
	latency := time.Since(start)

	switch decision.Action {
	case ActionGuardAllow:
		return toolResult{}, true
	case ActionGuardAsk:
		m.recordActionGuardDecision(session, sessionID, userID, toolCallID, toolName, args, sessionTraceID, sessionSpanID, decision, agentquality.StatusNeedsUser, latency, "")
		if m.hitlBroker == nil || !m.hitlBroker.Enabled() {
			content := fmt.Sprintf("[ActionGuard 拒绝: %s 需要人工确认，但 HITL 未启用]", decision.Reason)
			m.recordActionGuardDecision(session, sessionID, userID, toolCallID, toolName, args, sessionTraceID, sessionSpanID, decision, agentquality.StatusBlocked, latency, "action_guard_hitl_disabled")
			return m.actionGuardErrorResult(ctx, sessionID, toolCallID, toolName, args, sessionTraceID, content, "action_guard_hitl_disabled"), false
		}
		resp, err := m.requestHITLPermission(toolctx.WithSessionID(ctx, sessionID), skills.PermissionRequest{
			ToolName:    toolName,
			Description: actionGuardPermissionDescription(toolName, args, decision),
			Input:       args,
		}, sessionID)
		if err != nil {
			content := fmt.Sprintf("[ActionGuard 拒绝: 权限确认失败: %v]", err)
			m.recordActionGuardDecision(session, sessionID, userID, toolCallID, toolName, args, sessionTraceID, sessionSpanID, decision, agentquality.StatusBlocked, latency, err.Error())
			return m.actionGuardErrorResult(ctx, sessionID, toolCallID, toolName, args, sessionTraceID, content, err.Error()), false
		}
		if !resp.Granted {
			content := fmt.Sprintf("[ActionGuard 拒绝: 用户未批准 %s]", toolName)
			m.recordActionGuardDecision(session, sessionID, userID, toolCallID, toolName, args, sessionTraceID, sessionSpanID, decision, agentquality.StatusBlocked, latency, "user_denied")
			return m.actionGuardErrorResult(ctx, sessionID, toolCallID, toolName, args, sessionTraceID, content, "user_denied"), false
		}
		if approved != nil {
			approved[fingerprint] = true
		}
		m.recordActionGuardDecision(session, sessionID, userID, toolCallID, toolName, args, sessionTraceID, sessionSpanID, decision, agentquality.StatusPass, latency, "")
		return toolResult{}, true
	case ActionGuardDeny:
		content := fmt.Sprintf("[ActionGuard 拒绝: %s]", decision.Reason)
		m.recordActionGuardDecision(session, sessionID, userID, toolCallID, toolName, args, sessionTraceID, sessionSpanID, decision, agentquality.StatusBlocked, latency, "")
		return m.actionGuardErrorResult(ctx, sessionID, toolCallID, toolName, args, sessionTraceID, content, decision.Reason), false
	default:
		deny := ActionGuardDecision{Action: ActionGuardDeny, Reason: "unknown_action_guard_decision", Source: "action_guard"}
		content := "[ActionGuard 拒绝: unknown_action_guard_decision]"
		m.recordActionGuardDecision(session, sessionID, userID, toolCallID, toolName, args, sessionTraceID, sessionSpanID, deny, agentquality.StatusBlocked, latency, "")
		return m.actionGuardErrorResult(ctx, sessionID, toolCallID, toolName, args, sessionTraceID, content, deny.Reason), false
	}
}

func (m *Master) actionGuardToolDefinition(toolName string) *mcphost.ToolDefinition {
	def, ok := m.lookupToolDefinition(toolName)
	if !ok {
		return nil
	}
	return &def
}

func actionGuardPermissionDescription(toolName string, args json.RawMessage, decision ActionGuardDecision) string {
	preview := strings.TrimSpace(string(args))
	if len(preview) > 600 {
		preview = preview[:600] + "..."
	}
	return fmt.Sprintf("ActionGuard 请求确认: tool=%s reason=%s pattern=%s args=%s", toolName, decision.Reason, decision.Pattern, preview)
}

func (m *Master) actionGuardErrorResult(ctx context.Context, sessionID, toolCallID, toolName string, args json.RawMessage, turnID, content, errText string) toolResult {
	m.logger.Info("ActionGuard 拒绝工具执行",
		zap.String("tool", toolName),
		zap.String("reason", errText),
	)
	m.emitToolCallEvent(sessionID, ToolCallEvent{
		ToolCallID: toolCallID,
		ToolName:   toolName,
		TurnID:     turnID,
		Status:     "error",
		Error:      errText,
		SessionID:  sessionID,
	})
	m.logToolCall(ctx, sessionID, llm.ToolCall{ID: toolCallID, Name: toolName, Arguments: args}, string(args), content, true, 0)
	return toolResult{Content: content, IsError: true, Terminal: true}
}

func (m *Master) recordActionGuardDecision(session *SessionState, sessionID, userID, toolCallID, toolName string, args json.RawMessage, traceID, spanID string, decision ActionGuardDecision, status agentquality.FinalStatus, latency time.Duration, errText string) {
	toolDecision := agentquality.DecisionAllowed
	if decision.Action == ActionGuardDeny || status == agentquality.StatusBlocked {
		toolDecision = agentquality.DecisionRejected
	}
	attrs := map[string]any{
		"tool_name":    toolName,
		"tool_call_id": toolCallID,
		"action":       decision.Action,
		"reason":       decision.Reason,
		"source":       decision.Source,
		"latency_ms":   latency.Milliseconds(),
	}
	if decision.Pattern != "" {
		attrs["pattern"] = decision.Pattern
	}
	if userID != "" {
		attrs["user_id"] = userID
	}
	if errText != "" {
		attrs["error"] = errText
	}
	m.emitQualityEvent(traceID, spanID, sessionID, agentquality.Event{
		Name:        agentquality.EventPermissionDecision,
		Route:       routeFromSession(session),
		FailureType: agentquality.FailurePermission,
		FinalStatus: status,
		ToolDecision: agentquality.ToolDecision{
			Actual:   toolName,
			Decision: toolDecision,
			ArgsHash: hashToolArgs(args),
		},
		Attributes: attrs,
	})
}

func (m *Master) recordStrictPermissionBlocked(session *SessionState, sessionID, userID, toolCallID, toolName string, args json.RawMessage, traceID, spanID, reason string) {
	attrs := map[string]any{
		"tool_name":    toolName,
		"tool_call_id": toolCallID,
		"reason":       reason,
		"mode":         "strict",
	}
	if userID != "" {
		attrs["user_id"] = userID
	}
	m.emitQualityEvent(traceID, spanID, sessionID, agentquality.Event{
		Name:        agentquality.EventPermissionDecision,
		Route:       routeFromSession(session),
		FailureType: agentquality.FailurePermission,
		FinalStatus: agentquality.StatusBlocked,
		ToolDecision: agentquality.ToolDecision{
			Actual:   toolName,
			Decision: agentquality.DecisionRejected,
			ArgsHash: hashToolArgs(args),
		},
		Attributes: attrs,
	})
}
