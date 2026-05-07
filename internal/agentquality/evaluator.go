package agentquality

import (
	"context"
	"fmt"
	"strings"
)

type EvaluationInput struct {
	SessionID        string `json:"session_id,omitempty"`
	TraceID          string `json:"trace_id,omitempty"`
	Trigger          string `json:"trigger"`
	UserInput        string `json:"user_input,omitempty"`
	AssistantOutput  string `json:"assistant_output,omitempty"`
	ToolName         string `json:"tool_name,omitempty"`
	ToolError        string `json:"tool_error,omitempty"`
	ValidationOutput string `json:"validation_output,omitempty"`
}

type EvaluationVerdict struct {
	Score          int         `json:"score"`
	Verdict        string      `json:"verdict"`
	FailureType    FailureType `json:"failure_type,omitempty"`
	Feedback       []string    `json:"feedback"`
	ShouldOptimize bool        `json:"should_optimize"`
}

func ValidateEvaluationVerdict(verdict EvaluationVerdict) error {
	if verdict.Score < 0 || verdict.Score > 10 {
		return fmt.Errorf("score must be between 0 and 10")
	}
	if strings.TrimSpace(verdict.Verdict) == "" {
		return fmt.Errorf("verdict must not be empty")
	}
	if len(verdict.Feedback) > 5 {
		return fmt.Errorf("feedback must contain at most 5 items")
	}
	if verdict.FailureType != "" && !isValidFailureType(verdict.FailureType) {
		return fmt.Errorf("failure_type must be a known failure type")
	}
	return nil
}

func isValidFailureType(ft FailureType) bool {
	switch ft {
	case FailureNone,
		FailurePrompt,
		FailureTool,
		FailureSkill,
		FailureContext,
		FailureModel,
		FailurePermission,
		FailureRuntime,
		FailureUserInput:
		return true
	default:
		return false
	}
}

// HeuristicReflectionEvaluator is the default shadow evaluator used when no LLM-backed evaluator is injected.
type HeuristicReflectionEvaluator struct{}

func (HeuristicReflectionEvaluator) Evaluate(_ context.Context, input EvaluationInput) (EvaluationVerdict, error) {
	switch input.Trigger {
	case "test_failed":
		return EvaluationVerdict{
			Score:          4,
			Verdict:        "验证失败，需要修正后重新运行最小验证",
			FailureType:    FailureRuntime,
			Feedback:       []string{"read_validation_output", "fix_root_cause", "rerun_validation"},
			ShouldOptimize: true,
		}, nil
	case "loop_warn":
		return EvaluationVerdict{
			Score:          5,
			Verdict:        "检测到重复执行路径，需要改变工具策略或明确阻塞点",
			FailureType:    FailureTool,
			Feedback:       []string{"change_strategy", "avoid_repeated_tool_args"},
			ShouldOptimize: true,
		}, nil
	case "long_artifact":
		return EvaluationVerdict{
			Score:          7,
			Verdict:        "长产物已进入 shadow 抽查队列",
			FailureType:    FailureNone,
			Feedback:       []string{"review_structure", "verify_claims"},
			ShouldOptimize: false,
		}, nil
	default:
		return EvaluationVerdict{
			Score:          6,
			Verdict:        "反思事件需要人工复核",
			FailureType:    FailureRuntime,
			Feedback:       []string{"review_trace"},
			ShouldOptimize: true,
		}, nil
	}
}
