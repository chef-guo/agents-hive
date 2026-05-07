package agentquality

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type LongRunRunner struct{}

func (LongRunRunner) Run(c LongRunCase) (LongRunReport, error) {
	if err := c.Validate(); err != nil {
		return LongRunReport{}, err
	}
	report := LongRunReport{
		CaseID:         c.ID,
		Name:           c.Name,
		BudgetExitMode: LongRunBudgetExitNone,
		FinalStatus:    LongRunFinalStatusFailed,
		Failures:       []LongRunFailure{},
	}
	openTodos := 0
	for _, step := range c.Steps {
		if step.Turn > report.TurnCount {
			report.TurnCount = step.Turn
			report.LLMCallCount = step.Turn
		}
		if isLongRunToolStep(step) {
			report.ToolCallCount++
		}
		if step.Compaction {
			report.CompactionCount++
		}
		if step.LazyCompactionSkip {
			report.LazyCompactionSkipCount++
		}
		if step.TokensBeforeCompaction > 0 {
			report.TokensBeforeCompaction = step.TokensBeforeCompaction
		}
		if step.TokensAfterCompaction > 0 {
			report.TokensAfterCompaction = step.TokensAfterCompaction
		}
		if len(step.CompactionStageNames) > 0 {
			report.CompactionStageNames = append([]string(nil), step.CompactionStageNames...)
		}
		if step.CompactionElapsedMS > 0 {
			report.CompactionElapsedMS = step.CompactionElapsedMS
		}
		report.TodoLostCount += step.TodoLost
		report.ConstraintLostCount += step.ConstraintLost
		openTodos += step.TodoDelta
		if openTodos < 0 {
			openTodos = 0
		}
		if step.DuplicateToolFailure {
			report.DuplicateToolFailureCount++
		}
		if step.BudgetExitMode != "" {
			report.BudgetExitMode = step.BudgetExitMode
		}
		if step.FinalStatus != "" {
			report.FinalStatus = step.FinalStatus
		}
		report.Failures = append(report.Failures, longRunFailuresFromStep(c.ID, step)...)
	}
	if report.FinalStatus == LongRunFinalStatusCompleted && (openTodos > 0 || report.TodoLostCount > 0 || report.ConstraintLostCount > 0) {
		reason := "case completed after losing todos or constraints"
		if openTodos > 0 {
			reason = fmt.Sprintf("case completed with %d open todo item(s)", openTodos)
		}
		report.Failures = append(report.Failures, LongRunFailure{
			CaseID: c.ID,
			Turn:   report.TurnCount,
			Type:   LongRunFailurePrematureComplete,
			Reason: reason,
		})
	}
	if err := report.Validate(); err != nil {
		return LongRunReport{}, err
	}
	return report, nil
}

func RunLongRunCase(c LongRunCase) (LongRunReport, error) {
	return LongRunRunner{}.Run(c)
}

func LoadLongRunCases(dir string) ([]LongRunCase, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var cases []LongRunCase
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") || entry.Name() == "baseline-report.json" {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		b, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		var c LongRunCase
		if err := json.Unmarshal(b, &c); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		if err := c.Validate(); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		cases = append(cases, c)
	}
	sort.Slice(cases, func(i, j int) bool { return cases[i].ID < cases[j].ID })
	return cases, nil
}

func LoadLongRunBaseline(path string) ([]LongRunReport, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var reports []LongRunReport
	if err := json.Unmarshal(b, &reports); err != nil {
		return nil, err
	}
	for i := range reports {
		if err := reports[i].Validate(); err != nil {
			return nil, fmt.Errorf("%s report %d: %w", path, i, err)
		}
	}
	return reports, nil
}

func isLongRunToolStep(step LongRunStep) bool {
	if step.Tool != "" {
		return true
	}
	switch step.Kind {
	case LongRunStepToolSuccess, LongRunStepToolFailure, LongRunStepLargeToolOutput:
		return true
	default:
		return false
	}
}

func longRunFailuresFromStep(caseID string, step LongRunStep) []LongRunFailure {
	var out []LongRunFailure
	if step.FailureType != "" {
		out = append(out, LongRunFailure{
			CaseID: caseID,
			Turn:   step.Turn,
			Type:   step.FailureType,
			Reason: longRunFailureReason(step),
		})
	}
	if step.TodoLost > 0 {
		out = append(out, LongRunFailure{
			CaseID: caseID,
			Turn:   step.Turn,
			Type:   LongRunFailureTodoLoss,
			Reason: fmt.Sprintf("%d todo item(s) lost", step.TodoLost),
		})
	}
	if step.ConstraintLost > 0 {
		out = append(out, LongRunFailure{
			CaseID: caseID,
			Turn:   step.Turn,
			Type:   LongRunFailureConstraintLoss,
			Reason: fmt.Sprintf("%d constraint(s) lost", step.ConstraintLost),
		})
	}
	if step.DuplicateToolFailure {
		out = append(out, LongRunFailure{
			CaseID: caseID,
			Turn:   step.Turn,
			Type:   LongRunFailureToolLoop,
			Reason: "duplicate failed tool call",
		})
	}
	if step.BudgetExitMode == LongRunBudgetExitHardStop {
		out = append(out, LongRunFailure{
			CaseID: caseID,
			Turn:   step.Turn,
			Type:   LongRunFailureBudgetHardStop,
			Reason: "budget reached hard stop",
		})
	}
	if step.TraceMissing {
		out = append(out, LongRunFailure{
			CaseID: caseID,
			Turn:   step.Turn,
			Type:   LongRunFailureTraceMissing,
			Reason: "required longrun trace marker missing",
		})
	}
	if step.ResumeFailed {
		out = append(out, LongRunFailure{
			CaseID: caseID,
			Turn:   step.Turn,
			Type:   LongRunFailureResumeFailed,
			Reason: "resume snapshot could not continue",
		})
	}
	return out
}

func longRunFailureReason(step LongRunStep) string {
	if step.Outcome != "" {
		return step.Outcome
	}
	if step.Action != "" {
		return step.Action
	}
	return "scripted longrun failure"
}
