package agentquality

import (
	"encoding/json"
	"fmt"
)

type LongRunCase struct {
	ID          string        `json:"id"`
	Name        string        `json:"name"`
	Route       string        `json:"route,omitempty"`
	Goal        string        `json:"goal"`
	Steps       []LongRunStep `json:"steps"`
	Constraints []string      `json:"constraints,omitempty"`
	Seed        int64         `json:"seed,omitempty"`
	Budget      int           `json:"budget,omitempty"`
}

type LongRunStep struct {
	Turn                   int                `json:"turn"`
	Kind                   string             `json:"kind"`
	Action                 string             `json:"action,omitempty"`
	Tool                   string             `json:"tool,omitempty"`
	Outcome                string             `json:"outcome,omitempty"`
	TokensBeforeCompaction int                `json:"tokens_before_compaction,omitempty"`
	TokensAfterCompaction  int                `json:"tokens_after_compaction,omitempty"`
	CompactionStageNames   []string           `json:"compaction_stage_names,omitempty"`
	CompactionElapsedMS    int                `json:"compaction_elapsed_ms,omitempty"`
	Compaction             bool               `json:"compaction,omitempty"`
	LazyCompactionSkip     bool               `json:"lazy_compaction_skip,omitempty"`
	TodoDelta              int                `json:"todo_delta,omitempty"`
	TodoLost               int                `json:"todo_lost,omitempty"`
	ConstraintLost         int                `json:"constraint_lost,omitempty"`
	DuplicateToolFailure   bool               `json:"duplicate_tool_failure,omitempty"`
	BudgetExitMode         string             `json:"budget_exit_mode,omitempty"`
	FinalStatus            string             `json:"final_status,omitempty"`
	FailureType            LongRunFailureType `json:"failure_type,omitempty"`
	TraceMissing           bool               `json:"trace_missing,omitempty"`
	ResumeFailed           bool               `json:"resume_failed,omitempty"`
	Notes                  []string           `json:"notes,omitempty"`
}

type LongRunReport struct {
	CaseID                    string           `json:"case_id"`
	Name                      string           `json:"name,omitempty"`
	TurnCount                 int              `json:"turn_count"`
	LLMCallCount              int              `json:"llm_call_count"`
	ToolCallCount             int              `json:"tool_call_count"`
	CompactionCount           int              `json:"compaction_count"`
	LazyCompactionSkipCount   int              `json:"lazy_compaction_skip_count"`
	TokensBeforeCompaction    int              `json:"tokens_before_compaction"`
	TokensAfterCompaction     int              `json:"tokens_after_compaction"`
	CompactionStageNames      []string         `json:"compaction_stage_names,omitempty"`
	CompactionElapsedMS       int              `json:"compaction_elapsed_ms,omitempty"`
	TodoLostCount             int              `json:"todo_lost_count"`
	ConstraintLostCount       int              `json:"constraint_lost_count"`
	DuplicateToolFailureCount int              `json:"duplicate_tool_failure_count"`
	BudgetExitMode            string           `json:"budget_exit_mode"`
	FinalStatus               string           `json:"final_status"`
	Failures                  []LongRunFailure `json:"failures,omitempty"`
}

type LongRunFailureType string

type LongRunFailure struct {
	CaseID string             `json:"case_id"`
	Turn   int                `json:"turn"`
	Type   LongRunFailureType `json:"failure_type"`
	Reason string             `json:"reason"`
}

const (
	LongRunFailureContextOverflow   LongRunFailureType = "context_overflow"
	LongRunFailureTodoLoss          LongRunFailureType = "todo_loss"
	LongRunFailureConstraintLoss    LongRunFailureType = "constraint_loss"
	LongRunFailureToolLoop          LongRunFailureType = "tool_loop"
	LongRunFailurePrematureComplete LongRunFailureType = "premature_completion"
	LongRunFailureBudgetHardStop    LongRunFailureType = "budget_hard_stop"
	LongRunFailureResumeFailed      LongRunFailureType = "resume_failed"
	LongRunFailureTraceMissing      LongRunFailureType = "trace_missing"
)

const (
	LongRunBudgetExitNone          = "none"
	LongRunBudgetExitHardStop      = "hard_stop"
	LongRunBudgetExitGracefulYield = "graceful_yield"
)

const (
	LongRunFinalStatusCompleted = "completed"
	LongRunFinalStatusPaused    = "paused"
	LongRunFinalStatusFailed    = "failed"
)

const (
	LongRunStepToolSuccess     = "tool_success"
	LongRunStepToolFailure     = "tool_failure"
	LongRunStepLargeToolOutput = "large_tool_output"
)

var validLongRunFailureTypes = map[LongRunFailureType]struct{}{
	LongRunFailureContextOverflow:   {},
	LongRunFailureTodoLoss:          {},
	LongRunFailureConstraintLoss:    {},
	LongRunFailureToolLoop:          {},
	LongRunFailurePrematureComplete: {},
	LongRunFailureBudgetHardStop:    {},
	LongRunFailureResumeFailed:      {},
	LongRunFailureTraceMissing:      {},
}

var requiredLongRunReportFields = []string{
	"case_id",
	"turn_count",
	"llm_call_count",
	"tool_call_count",
	"compaction_count",
	"lazy_compaction_skip_count",
	"tokens_before_compaction",
	"tokens_after_compaction",
	"todo_lost_count",
	"constraint_lost_count",
	"duplicate_tool_failure_count",
	"budget_exit_mode",
	"final_status",
	"failures",
}

func isValidLongRunFailureType(v LongRunFailureType) bool {
	_, ok := validLongRunFailureTypes[v]
	return ok
}

func (f LongRunFailure) Validate() error {
	if f.CaseID == "" {
		return fmt.Errorf("case_id missing")
	}
	if f.Turn < 0 {
		return fmt.Errorf("turn must be non-negative")
	}
	if !isValidLongRunFailureType(f.Type) {
		return fmt.Errorf("invalid longrun failure type %q", f.Type)
	}
	if f.Reason == "" {
		return fmt.Errorf("reason missing")
	}
	return nil
}

func (f LongRunFailure) MarshalJSON() ([]byte, error) {
	type alias LongRunFailure
	if err := f.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(alias(f))
}

func (f *LongRunFailure) UnmarshalJSON(b []byte) error {
	type alias LongRunFailure
	var v alias
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	*f = LongRunFailure(v)
	return f.Validate()
}

func (c LongRunCase) Validate() error {
	if c.ID == "" {
		return fmt.Errorf("id missing")
	}
	if c.Name == "" {
		return fmt.Errorf("%s: name missing", c.ID)
	}
	if c.Goal == "" {
		return fmt.Errorf("%s: goal missing", c.ID)
	}
	if len(c.Steps) == 0 {
		return fmt.Errorf("%s: steps missing", c.ID)
	}
	prevTurn := 0
	for i, step := range c.Steps {
		if err := step.Validate(c.ID, i); err != nil {
			return err
		}
		if step.Turn <= prevTurn {
			return fmt.Errorf("%s step %d: turn must be greater than previous turn", c.ID, i)
		}
		prevTurn = step.Turn
	}
	return nil
}

func (s LongRunStep) Validate(caseID string, idx int) error {
	if s.Turn <= 0 {
		return fmt.Errorf("%s step %d: turn must be positive", caseID, idx)
	}
	if s.Kind == "" {
		return fmt.Errorf("%s step %d: kind missing", caseID, idx)
	}
	if s.FailureType != "" && !isValidLongRunFailureType(s.FailureType) {
		return fmt.Errorf("%s step %d: invalid failure_type %q", caseID, idx, s.FailureType)
	}
	if s.TokensBeforeCompaction < 0 {
		return fmt.Errorf("%s step %d: tokens_before_compaction must be non-negative", caseID, idx)
	}
	if s.TokensAfterCompaction < 0 {
		return fmt.Errorf("%s step %d: tokens_after_compaction must be non-negative", caseID, idx)
	}
	if s.CompactionElapsedMS < 0 {
		return fmt.Errorf("%s step %d: compaction_elapsed_ms must be non-negative", caseID, idx)
	}
	if s.TodoLost < 0 {
		return fmt.Errorf("%s step %d: todo_lost must be non-negative", caseID, idx)
	}
	if s.ConstraintLost < 0 {
		return fmt.Errorf("%s step %d: constraint_lost must be non-negative", caseID, idx)
	}
	switch s.BudgetExitMode {
	case "", LongRunBudgetExitNone, LongRunBudgetExitHardStop, LongRunBudgetExitGracefulYield:
	default:
		return fmt.Errorf("%s step %d: invalid budget_exit_mode %q", caseID, idx, s.BudgetExitMode)
	}
	if s.FinalStatus != "" {
		switch s.FinalStatus {
		case LongRunFinalStatusCompleted, LongRunFinalStatusPaused, LongRunFinalStatusFailed:
		default:
			return fmt.Errorf("%s step %d: invalid final_status %q", caseID, idx, s.FinalStatus)
		}
	}
	return nil
}

func (r LongRunReport) Validate() error {
	if r.CaseID == "" {
		return fmt.Errorf("case_id missing")
	}
	if r.TurnCount < 0 {
		return fmt.Errorf("turn_count must be non-negative")
	}
	if r.LLMCallCount < 0 || r.ToolCallCount < 0 || r.CompactionCount < 0 || r.LazyCompactionSkipCount < 0 {
		return fmt.Errorf("metric counts must be non-negative")
	}
	if r.TokensBeforeCompaction < 0 || r.TokensAfterCompaction < 0 || r.TodoLostCount < 0 || r.ConstraintLostCount < 0 || r.DuplicateToolFailureCount < 0 {
		return fmt.Errorf("longrun report detail counts must be non-negative")
	}
	if r.CompactionElapsedMS < 0 {
		return fmt.Errorf("compaction_elapsed_ms must be non-negative")
	}
	if r.BudgetExitMode != "" {
		switch r.BudgetExitMode {
		case LongRunBudgetExitNone, LongRunBudgetExitHardStop, LongRunBudgetExitGracefulYield:
		default:
			return fmt.Errorf("invalid budget_exit_mode %q", r.BudgetExitMode)
		}
	}
	if r.FinalStatus != "" {
		switch r.FinalStatus {
		case LongRunFinalStatusCompleted, LongRunFinalStatusPaused, LongRunFinalStatusFailed:
		default:
			return fmt.Errorf("invalid final_status %q", r.FinalStatus)
		}
	}
	for _, failure := range r.Failures {
		if err := failure.Validate(); err != nil {
			return err
		}
	}
	return nil
}

func (r LongRunReport) MarshalJSON() ([]byte, error) {
	type alias LongRunReport
	return json.Marshal(alias(r))
}

func (r *LongRunReport) UnmarshalJSON(b []byte) error {
	type alias LongRunReport
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(b, &fields); err != nil {
		return err
	}
	for _, field := range requiredLongRunReportFields {
		if _, ok := fields[field]; !ok {
			return fmt.Errorf("longrun report missing %s", field)
		}
	}
	var v alias
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	*r = LongRunReport(v)
	return r.Validate()
}
