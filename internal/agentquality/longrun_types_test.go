package agentquality

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLongRunReport_JSONRoundTrip(t *testing.T) {
	report := LongRunReport{
		CaseID:                    "b1_30_turn",
		Name:                      "B1 30 turn file workflow",
		TurnCount:                 30,
		LLMCallCount:              30,
		ToolCallCount:             12,
		CompactionCount:           0,
		LazyCompactionSkipCount:   1,
		TokensBeforeCompaction:    0,
		TokensAfterCompaction:     0,
		CompactionStageNames:      []string{"tool_budget", "session_memory", "truncate"},
		CompactionElapsedMS:       42,
		TodoLostCount:             0,
		ConstraintLostCount:       0,
		DuplicateToolFailureCount: 0,
		BudgetExitMode:            LongRunBudgetExitNone,
		FinalStatus:               LongRunFinalStatusCompleted,
		Failures: []LongRunFailure{{
			CaseID: "b1_30_turn",
			Turn:   12,
			Type:   LongRunFailureTraceMissing,
			Reason: "trace marker absent",
		}},
	}

	b, err := json.Marshal(report)
	require.NoError(t, err)
	assert.Contains(t, string(b), `"turn_count":30`)
	assert.Contains(t, string(b), `"llm_call_count":30`)
	assert.Contains(t, string(b), `"tool_call_count":12`)
	assert.Contains(t, string(b), `"compaction_count":0`)
	assert.Contains(t, string(b), `"lazy_compaction_skip_count":1`)
	assert.Contains(t, string(b), `"tokens_before_compaction":0`)
	assert.Contains(t, string(b), `"tokens_after_compaction":0`)
	assert.Contains(t, string(b), `"compaction_stage_names":["`)
	assert.Contains(t, string(b), `"compaction_elapsed_ms":42`)
	assert.Contains(t, string(b), `"todo_lost_count":0`)
	assert.Contains(t, string(b), `"constraint_lost_count":0`)
	assert.Contains(t, string(b), `"duplicate_tool_failure_count":0`)
	assert.Contains(t, string(b), `"budget_exit_mode":"none"`)
	assert.Contains(t, string(b), `"final_status":"completed"`)
	assert.Contains(t, string(b), `"failure_type":"trace_missing"`)

	var got LongRunReport
	require.NoError(t, json.Unmarshal(b, &got))
	assert.Equal(t, report, got)
}

func TestLongRunFailure_InvalidTypeReturnsError(t *testing.T) {
	err := json.Unmarshal([]byte(`{
		"case_id": "b1_30_turn",
		"turn": 1,
		"failure_type": "unknown",
		"reason": "bad type"
	}`), &LongRunFailure{})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid longrun failure type")
}

func TestLongRunCase_InvalidStepFailureTypeReturnsError(t *testing.T) {
	err := LongRunCase{
		ID:   "bad",
		Name: "bad",
		Goal: "exercise validation",
		Steps: []LongRunStep{{
			Turn:        1,
			Kind:        "llm",
			FailureType: "bad_failure",
		}},
	}.Validate()

	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid failure_type")
}

func TestLongRunStep_NegativeCountersReturnError(t *testing.T) {
	err := LongRunCase{
		ID:   "bad-counters",
		Name: "bad counters",
		Goal: "exercise validation",
		Steps: []LongRunStep{{
			Turn:                   1,
			Kind:                   LongRunStepToolSuccess,
			TokensBeforeCompaction: -1,
		}},
	}.Validate()

	require.Error(t, err)
	assert.Contains(t, err.Error(), "tokens_before_compaction must be non-negative")
}

func TestLongRunReport_MissingMetricFieldReturnsError(t *testing.T) {
	err := json.Unmarshal([]byte(`{
		"case_id": "b1_30_turn",
		"turn_count": 30,
		"llm_call_count": 30,
		"tool_call_count": 12,
		"compaction_count": 0,
		"lazy_compaction_skip_count": 0,
		"tokens_after_compaction": 0,
		"todo_lost_count": 0,
		"constraint_lost_count": 0,
		"duplicate_tool_failure_count": 0,
		"budget_exit_mode": "none",
		"final_status": "completed"
	}`), &LongRunReport{})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "longrun report missing tokens_before_compaction")
}

func TestLongRunFailureConstants_AreStable(t *testing.T) {
	assert.Equal(t, "context_overflow", string(LongRunFailureContextOverflow))
	assert.Equal(t, "todo_loss", string(LongRunFailureTodoLoss))
	assert.Equal(t, "constraint_loss", string(LongRunFailureConstraintLoss))
	assert.Equal(t, "tool_loop", string(LongRunFailureToolLoop))
	assert.Equal(t, "premature_completion", string(LongRunFailurePrematureComplete))
	assert.Equal(t, "budget_hard_stop", string(LongRunFailureBudgetHardStop))
	assert.Equal(t, "resume_failed", string(LongRunFailureResumeFailed))
	assert.Equal(t, "trace_missing", string(LongRunFailureTraceMissing))
}
