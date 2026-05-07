package master

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"go.uber.org/zap"

	"github.com/chef-guo/agents-hive/internal/agentquality"
	"github.com/chef-guo/agents-hive/internal/config"
)

type fakeReflectionEvaluator struct {
	verdict agentquality.EvaluationVerdict
	err     error
}

func (e fakeReflectionEvaluator) Evaluate(context.Context, agentquality.EvaluationInput) (agentquality.EvaluationVerdict, error) {
	return e.verdict, e.err
}

func TestReflectionEvaluatorShadowNoopsWhenDisabled(t *testing.T) {
	m := &Master{
		config: Config{
			Reflection: config.ReflectionConfig{EvaluatorShadow: config.ReflectionShadowConfig{Enabled: false}},
		},
		obsCh:  make(chan observabilityEntry, 2),
		logger: zap.NewNop(),
	}

	m.recordReflectionEvaluation(context.Background(), "session-1", "trace-1", "span-1", agentquality.EvaluationInput{Trigger: "validation_failure"}, fakeReflectionEvaluator{
		verdict: agentquality.EvaluationVerdict{Score: 1, Verdict: "bad", ShouldOptimize: true},
	})

	if len(m.obsCh) != 0 {
		t.Fatalf("obs events = %d, want 0", len(m.obsCh))
	}
}

func TestReflectionEvaluatorShadowRecordsVerdict(t *testing.T) {
	m := &Master{
		config: Config{
			Reflection: config.ReflectionConfig{EvaluatorShadow: config.ReflectionShadowConfig{Enabled: true}},
		},
		obsCh:  make(chan observabilityEntry, 4),
		logger: zap.NewNop(),
	}

	m.recordReflectionEvaluation(context.Background(), "session-1", "trace-1", "span-1", agentquality.EvaluationInput{Trigger: "validation_failure"}, fakeReflectionEvaluator{
		verdict: agentquality.EvaluationVerdict{
			Score:          5,
			Verdict:        "需要优化 prompt",
			FailureType:    agentquality.FailurePrompt,
			Feedback:       []string{"补充证据要求"},
			ShouldOptimize: true,
		},
	})

	if len(m.obsCh) != 2 {
		t.Fatalf("obs events = %d, want metric+log", len(m.obsCh))
	}
	<-m.obsCh
	logEntry := <-m.obsCh
	ev, ok := logEntry.log.Attributes["quality_event"].(json.RawMessage)
	if !ok || len(ev) == 0 {
		t.Fatalf("quality event not recorded: %+v", logEntry.log.Attributes)
	}
}

func TestReflectionEvaluatorShadowRecordsEvaluatorError(t *testing.T) {
	m := &Master{
		config: Config{
			Reflection: config.ReflectionConfig{EvaluatorShadow: config.ReflectionShadowConfig{Enabled: true}},
		},
		obsCh:  make(chan observabilityEntry, 4),
		logger: zap.NewNop(),
	}

	m.recordReflectionEvaluation(context.Background(), "session-1", "trace-1", "span-1", agentquality.EvaluationInput{Trigger: "validation_failure"}, fakeReflectionEvaluator{err: errors.New("llm unavailable")})

	if len(m.obsCh) != 2 {
		t.Fatalf("obs events = %d, want metric+log", len(m.obsCh))
	}
}

func TestReflectionEvaluationShadowUsesDefaultEvaluatorWhenEnabled(t *testing.T) {
	m := &Master{
		config: Config{
			Reflection: config.ReflectionConfig{EvaluatorShadow: config.ReflectionShadowConfig{Enabled: true}},
		},
		obsCh:  make(chan observabilityEntry, 4),
		logger: zap.NewNop(),
	}

	m.recordReflectionEvaluationShadow(context.Background(), "session-1", "trace-1", "span-1", agentquality.EvaluationInput{Trigger: "test_failed"})

	if len(m.obsCh) != 2 {
		t.Fatalf("obs events = %d, want metric+log from default evaluator", len(m.obsCh))
	}
}

func TestRecordReflectionTriggersLoopEvaluatorShadow(t *testing.T) {
	m := &Master{
		config: Config{
			Reflection: config.ReflectionConfig{EvaluatorShadow: config.ReflectionShadowConfig{Enabled: true}},
		},
		reflectionEval: fakeReflectionEvaluator{verdict: agentquality.EvaluationVerdict{
			Score:          5,
			Verdict:        "循环路径需要优化",
			FailureType:    agentquality.FailureTool,
			Feedback:       []string{"换策略"},
			ShouldOptimize: true,
		}},
		obsCh:  make(chan observabilityEntry, 8),
		logger: zap.NewNop(),
	}
	session := &SessionState{ID: "session-1"}

	m.recordReflection("trace-1", "span-1", session, reflectionNoteInput{
		Trigger:     "batch_loop",
		Severity:    "warn",
		Consecutive: 3,
	})

	if len(m.obsCh) != 4 {
		t.Fatalf("obs events = %d, want reflection metric/log plus evaluator metric/log", len(m.obsCh))
	}
}
