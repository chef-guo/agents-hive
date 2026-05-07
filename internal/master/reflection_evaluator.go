package master

import (
	"context"

	"github.com/chef-guo/agents-hive/internal/agentquality"
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
