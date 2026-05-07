package agentquality

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEvaluationVerdict(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		verdict EvaluationVerdict
		wantErr string
	}{
		{
			name: "valid verdict",
			verdict: EvaluationVerdict{
				Score:          8,
				Verdict:        "输出基本正确，但仍有改进空间",
				FailureType:    FailureTool,
				Feedback:       []string{"补充失败原因", "说明下一步动作"},
				ShouldOptimize: true,
			},
		},
		{
			name: "score below range",
			verdict: EvaluationVerdict{
				Score:   -1,
				Verdict: "分数非法",
			},
			wantErr: "score",
		},
		{
			name: "score above range",
			verdict: EvaluationVerdict{
				Score:   11,
				Verdict: "分数非法",
			},
			wantErr: "score",
		},
		{
			name: "empty verdict",
			verdict: EvaluationVerdict{
				Score: 5,
			},
			wantErr: "verdict",
		},
		{
			name: "feedback too many",
			verdict: EvaluationVerdict{
				Score:   6,
				Verdict: "反馈过多",
				Feedback: []string{
					"1", "2", "3", "4", "5", "6",
				},
			},
			wantErr: "feedback",
		},
		{
			name: "invalid failure type",
			verdict: EvaluationVerdict{
				Score:       4,
				Verdict:     "失败类型非法",
				FailureType: FailureType("bad_type"),
			},
			wantErr: "failure_type",
		},
		{
			name: "empty failure type allowed",
			verdict: EvaluationVerdict{
				Score:   7,
				Verdict: "failure_type 允许为空",
			},
		},
		{
			name: "none failure type allowed",
			verdict: EvaluationVerdict{
				Score:       7,
				Verdict:     "failure_type 为 none 也合法",
				FailureType: FailureNone,
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := ValidateEvaluationVerdict(tt.verdict)
			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}

func TestHeuristicReflectionEvaluator(t *testing.T) {
	verdict, err := HeuristicReflectionEvaluator{}.Evaluate(context.Background(), EvaluationInput{Trigger: "test_failed"})
	if err != nil {
		t.Fatalf("Evaluate returned error: %v", err)
	}
	if verdict.Score >= 7 {
		t.Fatalf("Score = %d, want failed validation score below 7", verdict.Score)
	}
	if !verdict.ShouldOptimize {
		t.Fatal("ShouldOptimize = false, want true for failed validation")
	}
	if err := ValidateEvaluationVerdict(verdict); err != nil {
		t.Fatalf("invalid verdict: %v", err)
	}
}
