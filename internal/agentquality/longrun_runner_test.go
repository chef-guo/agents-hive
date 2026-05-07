package agentquality

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLongRunRunner_DeterministicB1(t *testing.T) {
	cases, err := LoadLongRunCases(filepath.Join("testdata", "longrun"))
	require.NoError(t, err)

	c := findLongRunCase(t, cases, "b1_30_turn")
	report, err := RunLongRunCase(c)
	require.NoError(t, err)

	assert.Equal(t, "b1_30_turn", report.CaseID)
	assert.Equal(t, 30, report.TurnCount)
	assert.Equal(t, 30, report.LLMCallCount)
	assert.Equal(t, 12, report.ToolCallCount)
	assert.Equal(t, 0, report.TodoLostCount)
	assert.Equal(t, 0, report.ConstraintLostCount)
	assert.Equal(t, "none", report.BudgetExitMode)
	assert.Equal(t, "completed", report.FinalStatus)
	assert.Empty(t, report.Failures)
}

func TestLongRunRunner_ReportsFailureTurnAndType(t *testing.T) {
	report, err := RunLongRunCase(LongRunCase{
		ID:   "failure_case",
		Name: "failure case",
		Goal: "prove failure attribution",
		Steps: []LongRunStep{
			{Turn: 1, Kind: "tool_failure", Tool: "bash", Outcome: "same command failed"},
			{Turn: 2, Kind: "tool_failure", Tool: "bash", DuplicateToolFailure: true, Outcome: "same command failed again"},
		},
	})
	require.NoError(t, err)

	require.Len(t, report.Failures, 1)
	assert.Equal(t, "failure_case", report.Failures[0].CaseID)
	assert.Equal(t, 2, report.Failures[0].Turn)
	assert.Equal(t, LongRunFailureToolLoop, report.Failures[0].Type)
	assert.Equal(t, "duplicate failed tool call", report.Failures[0].Reason)
	assert.Equal(t, 1, report.DuplicateToolFailureCount)
}

func TestLongRunTestdata_LoadsB1B2B3B4AndBaseline(t *testing.T) {
	dir := filepath.Join("testdata", "longrun")
	cases, err := LoadLongRunCases(dir)
	require.NoError(t, err)
	require.Len(t, cases, 4)

	wantIDs := []string{"b1_30_turn", "b2_80_turn", "b3_150_turn", "b4_resume_restart"}
	for _, id := range wantIDs {
		t.Run(id, func(t *testing.T) {
			c := findLongRunCase(t, cases, id)
			require.NotEmpty(t, c.Steps)
			report, err := RunLongRunCase(c)
			require.NoError(t, err)
			require.NoError(t, report.Validate())
			assert.Equal(t, id, report.CaseID)
			assert.Greater(t, report.TurnCount, 0)
			assert.Equal(t, report.TurnCount, report.LLMCallCount)
			assert.NotEmpty(t, report.BudgetExitMode)
			assert.NotEmpty(t, report.FinalStatus)
			assert.Empty(t, report.Failures)
		})
	}

	baseline, err := LoadLongRunBaseline(filepath.Join(dir, "baseline-report.json"))
	require.NoError(t, err)
	require.Len(t, baseline, 4)
	for _, id := range wantIDs {
		report := findLongRunReport(t, baseline, id)
		require.NoError(t, report.Validate())
		assert.Greater(t, report.TurnCount, 0)
		assert.Equal(t, report.TurnCount, report.LLMCallCount)
	}
}

func TestLongRunReportBaseline_MatchesRunnerOutput(t *testing.T) {
	dir := filepath.Join("testdata", "longrun")
	cases, err := LoadLongRunCases(dir)
	require.NoError(t, err)
	baseline, err := LoadLongRunBaseline(filepath.Join(dir, "baseline-report.json"))
	require.NoError(t, err)

	for _, c := range cases {
		t.Run(c.ID, func(t *testing.T) {
			got, err := RunLongRunCase(c)
			require.NoError(t, err)
			want := findLongRunReport(t, baseline, c.ID)
			assert.Equal(t, want, got)
		})
	}
}

func TestLongRunRunner_DeterministicCaseMetrics(t *testing.T) {
	cases, err := LoadLongRunCases(filepath.Join("testdata", "longrun"))
	require.NoError(t, err)

	tests := map[string]struct {
		turns              int
		tools              int
		compactions        int
		lazySkips          int
		budgetExitMode     string
		finalStatus        string
		tokensBefore       int
		tokensAfter        int
		stageNames         []string
		elapsedMS          int
		minConstraintCount int
	}{
		"b1_30_turn": {
			turns:              30,
			tools:              12,
			budgetExitMode:     LongRunBudgetExitNone,
			finalStatus:        LongRunFinalStatusCompleted,
			minConstraintCount: 2,
		},
		"b2_80_turn": {
			turns:              80,
			tools:              3,
			compactions:        1,
			lazySkips:          1,
			budgetExitMode:     LongRunBudgetExitNone,
			finalStatus:        LongRunFinalStatusCompleted,
			tokensBefore:       92000,
			tokensAfter:        41000,
			stageNames:         []string{"tool_budget", "session_memory", "truncate"},
			elapsedMS:          180,
			minConstraintCount: 3,
		},
		"b3_150_turn": {
			turns:              150,
			tools:              3,
			compactions:        2,
			budgetExitMode:     LongRunBudgetExitNone,
			finalStatus:        LongRunFinalStatusCompleted,
			tokensBefore:       142000,
			tokensAfter:        57000,
			stageNames:         []string{"tool_budget", "session_memory", "truncate"},
			elapsedMS:          260,
			minConstraintCount: 3,
		},
		"b4_resume_restart": {
			turns:              60,
			tools:              2,
			budgetExitMode:     LongRunBudgetExitGracefulYield,
			finalStatus:        LongRunFinalStatusCompleted,
			minConstraintCount: 3,
		},
	}

	for id, tt := range tests {
		t.Run(id, func(t *testing.T) {
			c := findLongRunCase(t, cases, id)
			assert.GreaterOrEqual(t, len(c.Constraints), tt.minConstraintCount)

			report, err := RunLongRunCase(c)
			require.NoError(t, err)
			assert.Equal(t, tt.turns, report.TurnCount)
			assert.Equal(t, tt.turns, report.LLMCallCount)
			assert.Equal(t, tt.tools, report.ToolCallCount)
			assert.Equal(t, tt.compactions, report.CompactionCount)
			assert.Equal(t, tt.lazySkips, report.LazyCompactionSkipCount)
			assert.Equal(t, tt.budgetExitMode, report.BudgetExitMode)
			assert.Equal(t, tt.finalStatus, report.FinalStatus)
			assert.Equal(t, tt.tokensBefore, report.TokensBeforeCompaction)
			assert.Equal(t, tt.tokensAfter, report.TokensAfterCompaction)
			assert.Equal(t, tt.stageNames, report.CompactionStageNames)
			assert.Equal(t, tt.elapsedMS, report.CompactionElapsedMS)
			assert.Zero(t, report.TodoLostCount)
			assert.Zero(t, report.ConstraintLostCount)
			assert.Zero(t, report.DuplicateToolFailureCount)
			assert.Empty(t, report.Failures)
		})
	}
}

func TestLongRunRunner_PrematureCompletionWithOpenTodos(t *testing.T) {
	report, err := RunLongRunCase(LongRunCase{
		ID:   "premature",
		Name: "premature completion",
		Goal: "prove open todos block completion",
		Steps: []LongRunStep{
			{Turn: 1, Kind: LongRunStepToolSuccess, Tool: "read_file", TodoDelta: 2},
			{Turn: 2, Kind: "final", FinalStatus: LongRunFinalStatusCompleted},
		},
	})
	require.NoError(t, err)

	require.Len(t, report.Failures, 1)
	assert.Equal(t, LongRunFailurePrematureComplete, report.Failures[0].Type)
	assert.Equal(t, 2, report.Failures[0].Turn)
	assert.Contains(t, report.Failures[0].Reason, "open todo")
}

func TestLongRunRunner_ResumeFixturePausesBeforeCompleting(t *testing.T) {
	cases, err := LoadLongRunCases(filepath.Join("testdata", "longrun"))
	require.NoError(t, err)
	c := findLongRunCase(t, cases, "b4_resume_restart")

	var sawPause bool
	var sawResume bool
	for _, step := range c.Steps {
		if step.FinalStatus == LongRunFinalStatusPaused && step.BudgetExitMode == LongRunBudgetExitGracefulYield {
			sawPause = true
		}
		if step.Kind == "resume" {
			sawResume = true
		}
	}

	assert.True(t, sawPause)
	assert.True(t, sawResume)
}

func findLongRunCase(t *testing.T, cases []LongRunCase, id string) LongRunCase {
	t.Helper()
	for _, c := range cases {
		if c.ID == id {
			return c
		}
	}
	t.Fatalf("longrun case %s not found", id)
	return LongRunCase{}
}

func findLongRunReport(t *testing.T, reports []LongRunReport, id string) LongRunReport {
	t.Helper()
	for _, report := range reports {
		if report.CaseID == id {
			return report
		}
	}
	t.Fatalf("longrun report %s not found", id)
	return LongRunReport{}
}
