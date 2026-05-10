package master

import (
	"context"

	"go.uber.org/zap"

	"github.com/chef-guo/agents-hive/internal/agentquality"
	"github.com/chef-guo/agents-hive/internal/auth"
	"github.com/chef-guo/agents-hive/internal/memory"
)

type ReflectionEvaluator interface {
	Evaluate(ctx context.Context, input agentquality.EvaluationInput) (agentquality.EvaluationVerdict, error)
}

func (m *Master) recordReflectionEvaluation(ctx context.Context, sessionID, traceID, spanID string, input agentquality.EvaluationInput, evaluator ReflectionEvaluator) {
	if m == nil || !m.config.Reflection.EvaluatorShadow.Enabled || evaluator == nil {
		return
	}
	verdict, err := evaluator.Evaluate(ctx, input)
	if err != nil {
		m.emitQualityEvent(traceID, spanID, sessionID, agentquality.Event{
			Name:        agentquality.EventReflection,
			Route:       routeFromSessionID(sessionID),
			FailureType: agentquality.FailureRuntime,
			FinalStatus: agentquality.StatusNeedsUser,
			Reflection: agentquality.Reflection{
				Trigger:  "evaluator_shadow",
				Severity: "warn",
				Summary:  err.Error(),
			},
		})
		return
	}
	if err := agentquality.ValidateEvaluationVerdict(verdict); err != nil {
		m.emitQualityEvent(traceID, spanID, sessionID, agentquality.Event{
			Name:        agentquality.EventReflection,
			Route:       routeFromSessionID(sessionID),
			FailureType: agentquality.FailureRuntime,
			FinalStatus: agentquality.StatusNeedsUser,
			Reflection: agentquality.Reflection{
				Trigger:  "evaluator_shadow",
				Severity: "warn",
				Summary:  err.Error(),
			},
		})
		return
	}
	severity := "info"
	status := agentquality.StatusPass
	if verdict.ShouldOptimize || verdict.Score < 7 {
		severity = "warn"
		status = agentquality.StatusNeedsUser
	}
	m.emitQualityEvent(traceID, spanID, sessionID, agentquality.Event{
		Name:        agentquality.EventReflection,
		Route:       routeFromSessionID(sessionID),
		FailureType: verdict.FailureType,
		FinalStatus: status,
		Reflection: agentquality.Reflection{
			Trigger:     "evaluator_shadow",
			Severity:    severity,
			Summary:     verdict.Verdict,
			Recommended: verdict.Feedback,
		},
		Attributes: map[string]any{
			"score":           verdict.Score,
			"should_optimize": verdict.ShouldOptimize,
		},
	})
	m.recordFeedbackMemory(ctx, sessionID, traceID, spanID, verdict)
}

func (m *Master) recordReflectionEvaluationShadow(ctx context.Context, sessionID, traceID, spanID string, input agentquality.EvaluationInput) {
	if m == nil || !m.config.Reflection.EvaluatorShadow.Enabled {
		return
	}
	if input.SessionID == "" {
		input.SessionID = sessionID
	}
	if input.TraceID == "" {
		input.TraceID = traceID
	}
	evaluator := m.reflectionEval
	if evaluator == nil {
		evaluator = agentquality.HeuristicReflectionEvaluator{}
	}
	m.recordReflectionEvaluation(ctx, sessionID, traceID, spanID, input, evaluator)
}

func (m *Master) recordFeedbackMemory(ctx context.Context, sessionID, traceID, spanID string, verdict agentquality.EvaluationVerdict) {
	if m == nil || m.feedbackExtractor == nil || len(verdict.Feedback) == 0 {
		return
	}
	confidence := 0.75
	if verdict.ShouldOptimize || verdict.Score < 7 {
		confidence = 0.85
	}
	_, err := m.feedbackExtractor.ExtractFeedback(ctx, memory.FeedbackInput{
		Text:          verdict.Verdict,
		Feedback:      verdict.Feedback,
		SessionID:     sessionID,
		UserID:        auth.UserIDFrom(ctx),
		Source:        "reflection_evaluator",
		RunID:         firstNonEmptyString(traceID, sessionID),
		SourceMessage: firstNonEmptyString(spanID, traceID),
		Confidence:    confidence,
		SkillName:     memory.RuntimeContextFrom(ctx).SkillName,
		TaskType:      memory.RuntimeContextFrom(ctx).TaskType,
		AgentName:     memory.RuntimeContextFrom(ctx).AgentName,
	})
	if err != nil && m.logger != nil {
		m.logger.Warn("写入 feedback 记忆失败", zap.Error(err))
	}
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
