package master

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"

	"github.com/chef-guo/agents-hive/internal/agentquality"
	"github.com/chef-guo/agents-hive/internal/journal"
	"github.com/chef-guo/agents-hive/internal/llm"
	"github.com/chef-guo/agents-hive/internal/observability"
	"github.com/chef-guo/agents-hive/internal/router"
	"github.com/chef-guo/agents-hive/internal/skills"
	"github.com/chef-guo/agents-hive/internal/toolctx"
	"github.com/chef-guo/agents-hive/internal/tools"
)

func qualitySessionHash(sessionID string) string {
	if sessionID == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(sessionID))
	return "sha256:" + hex.EncodeToString(sum[:8])
}

func routeFromSession(session *SessionState) string {
	if session == nil {
		return "unknown"
	}
	return routeFromSessionID(session.ID)
}

func routeFromSessionID(sessionID string) string {
	if sessionID == "" {
		return "unknown"
	}
	if strings.HasPrefix(sessionID, "im-") {
		return "im"
	}
	if strings.HasPrefix(sessionID, "acp-") || strings.HasPrefix(sessionID, "acp_") {
		return "acp"
	}
	return "web"
}

func hashToolArgs(args json.RawMessage) string {
	raw := []byte(args)
	var normalized any
	if json.Unmarshal(args, &normalized) == nil {
		if b, err := json.Marshal(normalized); err == nil {
			raw = b
		}
	}
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:8])
}

func (m *Master) emitQualityEvent(traceID, spanID, sessionID string, ev agentquality.Event) {
	if ev.Ts.IsZero() {
		ev.Ts = time.Now()
	}
	if ev.SessionIDHash == "" {
		ev.SessionIDHash = qualitySessionHash(sessionID)
	}
	raw, _ := json.Marshal(ev)
	labels := agentquality.MetricLabels(ev)
	if ev.Name == agentquality.EventDelegation && ev.Delegation.StopReason != "" {
		labels["stop_reason"] = ev.Delegation.StopReason
	}
	m.enqueueMetric(observability.Metric{
		Name:   string(ev.Name),
		Value:  1,
		Labels: labels,
		Ts:     ev.Ts,
	})
	m.enqueueLog(observability.LogEntry{
		Level:     "info",
		Message:   string(ev.Name),
		TraceID:   traceID,
		SpanID:    spanID,
		SessionID: sessionID,
		Attributes: map[string]any{
			"quality_event": json.RawMessage(raw),
		},
		Ts: ev.Ts,
	})
	m.enqueueQualityJournalDecision(sessionID, ev, raw)
}

func (m *Master) recordToolRecall(traceID, spanID string, session *SessionState, recall agentquality.ToolRecall) {
	if m == nil || recall.Mode == "" || recall.Mode == "off" || recall.QueryPreview == "" {
		return
	}
	sessionID := ""
	if session != nil {
		sessionID = session.ID
	}
	m.emitQualityEvent(traceID, spanID, sessionID, agentquality.Event{
		Name:        agentquality.EventToolRecall,
		Route:       routeFromSession(session),
		FailureType: agentquality.FailureNone,
		FinalStatus: agentquality.StatusPass,
		ToolRecall:  recall,
	})
}

func (m *Master) recordRouteDecision(traceID, spanID string, session *SessionState, ev agentquality.RouteDecisionEvent) {
	if m == nil || ev.IntentKind == "" {
		return
	}
	sessionID := ""
	if session != nil {
		sessionID = session.ID
	}
	m.emitQualityEvent(traceID, spanID, sessionID, agentquality.Event{
		Name:          agentquality.EventRouteDecision,
		Route:         routeFromSession(session),
		FailureType:   agentquality.FailureNone,
		FinalStatus:   agentquality.StatusPass,
		RouteDecision: ev,
	})
}

func (m *Master) recordRouteDecisionSpan(traceID, spanID string, session *SessionState, span router.DecisionSpan) {
	if m == nil || span.Intent.Kind == "" {
		return
	}
	sessionID := ""
	if session != nil {
		sessionID = session.ID
	}
	m.enqueueLog(observability.LogEntry{
		Level:     "info",
		Message:   "quality.route_decision.span",
		TraceID:   traceID,
		SpanID:    spanID,
		SessionID: sessionID,
		Attributes: map[string]any{
			"route_decision_span": span,
		},
		Ts: time.Now(),
	})
}

func (m *Master) recordReflection(traceID, spanID string, session *SessionState, in reflectionNoteInput) {
	sessionID := ""
	if session != nil {
		sessionID = session.ID
		if in.ToolName != "" && in.FailureKind != "" {
			session.AddReflectionBlock(router.ReflectionBlock{
				ToolName:    in.ToolName,
				Mode:        "exec",
				Reason:      firstNonEmptyString(in.Detail, in.Trigger),
				FailureKind: in.FailureKind,
				CreatedAt:   time.Now(),
			})
		}
	}
	note := buildReflectionSystemNote(in)
	if session != nil {
		m.appendSessionMessage(session, llm.MessageWithTools{
			Role:      "system",
			Content:   llm.NewTextContent(note),
			CreatedAt: time.Now().Format(time.RFC3339),
			Metadata: map[string]string{
				"agent_id":            "master",
				"reflection_trigger":  in.Trigger,
				"reflection_severity": in.Severity,
			},
		})
	}
	m.emitQualityEvent(traceID, spanID, sessionID, agentquality.Event{
		Name:        agentquality.EventReflection,
		Route:       routeFromSession(session),
		FailureType: reflectionFailureType(in.Trigger),
		FinalStatus: reflectionFinalStatus(in.Severity),
		Reflection: agentquality.Reflection{
			Trigger:     in.Trigger,
			Severity:    in.Severity,
			ToolName:    in.ToolName,
			Consecutive: in.Consecutive,
			Summary:     reflectionSummary(in.Trigger),
			Injected:    session != nil,
		},
	})
	if in.Trigger == "batch_loop" && in.Severity == "warn" {
		m.recordReflectionEvaluationShadow(context.Background(), sessionID, traceID, spanID, agentquality.EvaluationInput{
			Trigger:          "loop_warn",
			ToolName:         in.ToolName,
			ValidationOutput: in.Detail,
		})
	}
}

func reflectionFailureType(trigger string) agentquality.FailureType {
	switch trigger {
	case "batch_loop", "call_failure":
		return agentquality.FailureTool
	case "guard_failure":
		return agentquality.FailurePrompt
	case "validation_failure":
		return agentquality.FailureModel
	default:
		return agentquality.FailureRuntime
	}
}

func reflectionFinalStatus(severity string) agentquality.FinalStatus {
	if severity == "hard_stop" {
		return agentquality.StatusFail
	}
	return agentquality.StatusPass
}

func reflectionSummary(trigger string) string {
	switch trigger {
	case "batch_loop":
		return "repeated tool batch detected"
	case "call_failure":
		return "repeated tool call failure detected"
	case "guard_failure":
		return "quality guard blocked model output"
	case "validation_failure":
		return "post-validation blocked model output"
	default:
		return "execution path needs strategy change"
	}
}

func (m *Master) enqueueQualityJournalDecision(sessionID string, ev agentquality.Event, raw []byte) {
	if m.journal == nil || m.journalCh == nil || sessionID == "" {
		return
	}
	entry := journal.DecisionEntry{
		SessionID: sessionID,
		Decision:  string(ev.Name),
		Reason:    string(raw),
		AgentID:   "quality",
		Timestamp: ev.Ts,
	}
	select {
	case m.journalCh <- journalEntry{decision: &entry}:
	default:
	}
}

func (m *Master) RecordDelegation(ctx context.Context, ev tools.DelegationEvent) {
	sessionID := toolctx.GetSessionID(ctx)
	if ev.SessionID != "" {
		sessionID = ev.SessionID
	}

	status := agentquality.StatusPass
	failureType := agentquality.FailureNone
	if ev.Status == "failed" {
		status = agentquality.StatusFail
		failureType = agentquality.FailureRuntime
	}
	if ev.FailureType != "" {
		failureType = agentquality.FailureType(ev.FailureType)
	}

	attrs := map[string]any{}
	if ev.Error != "" {
		attrs["error"] = ev.Error
	}
	if ev.StopReason != "" {
		attrs["stop_reason"] = ev.StopReason
	}

	m.emitQualityEvent("", "", sessionID, agentquality.Event{
		Name:        agentquality.EventDelegation,
		Route:       routeFromSessionID(sessionID),
		FailureType: failureType,
		FinalStatus: status,
		Delegation: agentquality.Delegation{
			ParentTraceID: ev.ParentTraceID,
			ChildTraceID:  ev.ChildTraceID,
			AgentID:       ev.AgentID,
			AgentType:     ev.AgentType,
			GroupID:       ev.GroupID,
			ToolWhitelist: append([]string(nil), ev.ToolWhitelist...),
			SpawnDepth:    ev.SpawnDepth,
			MaxTurns:      ev.MaxTurns,
			StopReason:    ev.StopReason,
		},
		Attributes: attrs,
	})
}

func (m *Master) RecordACPPermissionDecision(ctx context.Context, sessionID string, req skills.PermissionRequest, decision string, granted bool, remember bool, errText string) {
	status := agentquality.StatusBlocked
	if granted {
		status = agentquality.StatusPass
	} else if decision == "cancelled" {
		status = agentquality.StatusNeedsUser
	}
	attrs := map[string]any{
		"tool_name": req.ToolName,
		"decision":  decision,
		"remember":  remember,
		"bridge":    "acp",
	}
	if errText != "" {
		attrs["error"] = errText
	}
	m.emitQualityEvent("", "", sessionID, agentquality.Event{
		Name:        agentquality.EventPermissionDecision,
		Route:       routeFromSessionID(sessionID),
		FailureType: agentquality.FailurePermission,
		FinalStatus: status,
		ToolDecision: agentquality.ToolDecision{
			Actual:   req.ToolName,
			Decision: agentquality.Decision(decision),
		},
		Attributes: attrs,
	})
}
