package master

import (
	"time"

	"github.com/chef-guo/agents-hive/internal/observability"
	"github.com/chef-guo/agents-hive/internal/sessiontodo"
	"github.com/chef-guo/agents-hive/internal/toolctx"
)

type planModeAuditEvent struct {
	Action          string
	SessionID       string
	FromStatus      sessiontodo.PlanStatus
	ToStatus        sessiontodo.PlanStatus
	ToolName        string
	BlockedToolName string
	ToolCallID      string
	TurnID          string
	DecisionSource  string
	CallerType      toolctx.CallerType
	Reason          string
}

func (m *Master) recordPlanModeAudit(event planModeAuditEvent) {
	if m == nil {
		return
	}
	fromStatus := event.FromStatus
	if fromStatus == "" {
		fromStatus = sessiontodo.PlanStatusNone
	}
	toStatus := event.ToStatus
	if toStatus == "" {
		toStatus = sessiontodo.PlanStatusNone
	}
	attrs := map[string]any{
		"action":              event.Action,
		"turn_id":             event.TurnID,
		"from_status":         string(fromStatus),
		"to_status":           string(toStatus),
		"tool_name":           event.ToolName,
		"blocked_tool_name":   event.BlockedToolName,
		"source_tool_call_id": event.ToolCallID,
		"decision_source":     event.DecisionSource,
		"caller_type":         string(event.CallerType),
		"reason":              event.Reason,
	}
	m.enqueueLog(observability.LogEntry{
		Level:      "info",
		Message:    "plan_mode audit",
		SessionID:  event.SessionID,
		Attributes: attrs,
		Ts:         time.Now(),
	})
}

func planStatusForAudit(session *SessionState) sessiontodo.PlanStatus {
	if session == nil {
		return sessiontodo.PlanStatusNone
	}
	session.mu.RLock()
	status := session.PlanStatus
	session.mu.RUnlock()
	if status == "" {
		return sessiontodo.PlanStatusNone
	}
	return status
}
