package nightlyeval

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRunDeterministicEvalComputesSuccessDeltaAndROI(t *testing.T) {
	summary, err := Run(context.Background(), DefaultCases(), DeterministicEvaluator{})
	require.NoError(t, err)

	require.Equal(t, 3, summary.CaseCount)
	require.Equal(t, 3, summary.RequiredCount)
	require.Equal(t, 1.0, summary.WithMemorySuccessRate)
	require.Equal(t, 0.0, summary.WithoutMemorySuccessRate)
	require.Equal(t, 1.0, summary.SuccessRateDelta)
	require.Greater(t, summary.MemoryTokenROI, 0.0)
	require.Less(t, summary.MemoryTokenROI, 0.05)
	require.True(t, summary.Passed)
	require.Len(t, summary.Results, 3)
}

func TestRunFailsWhenRequiredWithMemoryCaseFails(t *testing.T) {
	cases := []Case{{
		ID:                 "bad",
		Task:               "task",
		Memory:             "irrelevant",
		ExpectedWithMemory: []string{"missing-token"},
		Required:           true,
	}}

	summary, err := Run(context.Background(), cases, DeterministicEvaluator{})
	require.NoError(t, err)
	require.False(t, summary.Passed)
	require.Equal(t, []string{"bad"}, summary.RequiredFailed)
}
