package agentquality

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCandidateFromFailure_DoesNotMakeRequiredCase(t *testing.T) {
	rec := CandidateFromFailure("session-1", "执行 rm -rf ./tmp-cache", "session-1:step-3", Event{
		Route:        "im",
		FailureType:  FailurePermission,
		FinalStatus:  StatusNeedsUser,
		ToolDecision: ToolDecision{Actual: "bash"},
	})

	assert.Equal(t, CandidateNew, rec.Status)
	assert.False(t, rec.Case.Required)
	assert.Equal(t, "dangerous", rec.Risk)
	assert.Equal(t, StatusNeedsUser, rec.Case.ExpectedStatus)
	assert.Equal(t, []string{"bash"}, rec.Case.AllowedTools)
	assert.NotEmpty(t, rec.Fingerprint)
	assert.Equal(t, "session-1:step-3", rec.ReplayRef)
}

func TestCandidateFromFailure_PersistsOptimizationSuggestions(t *testing.T) {
	rec := CandidateFromFailure("session-1", "定位 createPermissionPromptFn", "session-1:step-4", Event{
		Route:       "web",
		FailureType: FailureTool,
		FinalStatus: StatusFail,
		Prompt:      PromptRef{Key: "system/base", Version: "sha256:old"},
		ToolDecision: ToolDecision{
			Expected: []string{"grep"},
			Actual:   "read_file",
		},
	})

	assertSuggestionKinds(t, rec.Suggestions, SuggestionPromptDiff, SuggestionToolDescription)
	assert.Equal(t, "system/base@sha256:old", rec.Suggestions[0].Target)
	assert.Equal(t, "grep", rec.Suggestions[1].Target)
}

func TestCandidateFromReflection_PreservesReflectionSource(t *testing.T) {
	rec := CandidateFromReflection("session-1", "连续调用失败后修复流程", "session-1:step-5", Event{
		Name:        EventReflection,
		Route:       "web",
		FailureType: FailureRuntime,
		FinalStatus: StatusBlocked,
		Reflection: Reflection{
			Trigger:     "call_failure",
			Severity:    "hard_stop",
			ToolName:    "shell",
			Consecutive: 3,
			Summary:     "连续工具调用失败，需要先检查错误模式再继续",
		},
	})

	assert.Equal(t, CandidateNew, rec.Status)
	assert.Equal(t, EventReflection, rec.SourceEvent.Name)
	assert.Equal(t, FailureRuntime, rec.FailureType)
	assert.Equal(t, FailureRuntime, rec.Case.FailureType)
	assert.Equal(t, StatusBlocked, rec.Case.ExpectedStatus)
	assert.Contains(t, rec.Case.Notes, "reflection")
	assert.NotEmpty(t, rec.Suggestions)
}

func TestValidateCandidateStatus(t *testing.T) {
	assert.NoError(t, ValidateCandidateStatus(CandidateApproved))
	assert.Error(t, ValidateCandidateStatus("invalid"))
}

func TestValidateCandidateTransition_BlocksDirectPromotion(t *testing.T) {
	assert.NoError(t, ValidateCandidateTransition(CandidateNew, CandidateReviewing))
	assert.NoError(t, ValidateCandidateTransition(CandidateReviewing, CandidateApproved))
	assert.NoError(t, ValidateCandidateTransition(CandidateApproved, CandidatePromoted))
	assert.Error(t, ValidateCandidateTransition(CandidateNew, CandidatePromoted))
}

func TestValidateCandidateTransition_AllowsPromotedVerificationResults(t *testing.T) {
	assert.NoError(t, ValidateCandidateStatus(CandidatePromotedVerified))
	assert.NoError(t, ValidateCandidateStatus(CandidatePromotedRegressed))
	assert.NoError(t, ValidateCandidateTransition(CandidatePromoted, CandidatePromotedVerified))
	assert.NoError(t, ValidateCandidateTransition(CandidatePromoted, CandidatePromotedRegressed))
	assert.Error(t, ValidateCandidateTransition(CandidatePromotedVerified, CandidatePromoted))
	assert.Error(t, ValidateCandidateTransition(CandidatePromotedRegressed, CandidatePromoted))
}
