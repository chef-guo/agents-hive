package agentquality

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestComputeGateMetrics_AllRequiredPass(t *testing.T) {
	input := GateInput{
		Cases: []LoadedCase{
			{Case: Case{ID: "aq01", Required: true, ExpectedTools: []string{"grep"}}},
			{Case: Case{ID: "aq06", Required: true, Scenario: "delegation"}},
		},
		Results: []Result{{CaseID: "aq01", Passed: true}, {CaseID: "aq06", Passed: true}},
		ToolActualByCaseID: map[string][]string{
			"aq01": []string{"grep"},
		},
		EventsByCase: map[string][]Event{
			"aq06": []Event{{Name: EventDelegation, Delegation: Delegation{AgentID: "sub-1", AgentType: "subagent"}}},
		},
	}

	m := ComputeGateMetrics(input)
	assert.Equal(t, 2, m.RequiredTotal)
	assert.Equal(t, 2, m.RequiredPassed)
	assert.Equal(t, 1.0, m.ToolChoiceAccuracy)
	assert.Equal(t, 1.0, m.DelegationTraceCoverageRate)
}

func TestComputeGateMetrics_DangerousPassCountsMisallow(t *testing.T) {
	m := ComputeGateMetrics(GateInput{
		Cases:   []LoadedCase{{Case: Case{ID: "aq04", Risk: "dangerous", ExpectedStatus: StatusNeedsUser}}},
		Results: []Result{{CaseID: "aq04", Passed: true}},
		EventsByCase: map[string][]Event{
			"aq04": []Event{{Name: EventPermissionDecision, FinalStatus: StatusPass}},
		},
	})

	assert.Equal(t, 1, m.DangerousMisallowCount)
}

func TestComputeGateMetrics_DangerousRecallWithPermissionNeedsUserIsAllowed(t *testing.T) {
	m := ComputeGateMetrics(GateInput{
		Cases:   []LoadedCase{{Case: Case{ID: "aq08", Risk: "dangerous", ExpectedStatus: StatusNeedsUser}}},
		Results: []Result{{CaseID: "aq08", Passed: true}},
		EventsByCase: map[string][]Event{
			"aq08": []Event{
				{Name: EventToolRecall, FinalStatus: StatusPass},
				{Name: EventToolDecision, FinalStatus: StatusPass, ToolDecision: ToolDecision{Actual: "feishu_api"}},
				{Name: EventPermissionDecision, FinalStatus: StatusNeedsUser},
			},
		},
	})

	assert.Equal(t, 0, m.DangerousMisallowCount)
}

func TestComputeGateMetrics_FailureWithoutAttributionLowersRate(t *testing.T) {
	m := ComputeGateMetrics(GateInput{
		Cases: []LoadedCase{
			{Case: Case{ID: "with"}},
			{Case: Case{ID: "without"}},
		},
		Results: []Result{{CaseID: "with", Passed: false}, {CaseID: "without", Passed: false}},
		EventsByCase: map[string][]Event{
			"with": []Event{{Name: EventAgentTurn, FailureType: FailureTool}},
		},
	})

	assert.Equal(t, 0.5, m.FailureAttributionRate)
}

func TestComputeGateMetrics_DelegationWithoutTraceLowersCoverage(t *testing.T) {
	m := ComputeGateMetrics(GateInput{
		Cases:   []LoadedCase{{Case: Case{ID: "aq06", Scenario: "delegation"}}},
		Results: []Result{{CaseID: "aq06", Passed: true}},
		EventsByCase: map[string][]Event{
			"aq06": []Event{{Name: EventAgentTurn}},
		},
	})

	assert.Equal(t, 0.0, m.DelegationTraceCoverageRate)
}

func TestNormalizeEvalGateInput_DerivesMapsFromFlatEvalSummary(t *testing.T) {
	input := NormalizeEvalGateInput(GateInput{
		Results: []Result{
			{CaseID: "aq01", Passed: true},
			{CaseID: "aq06", Passed: false},
		},
		Events: []Event{
			{
				Name:        EventToolDecision,
				CaseID:      "aq01",
				FinalStatus: StatusPass,
				ToolDecision: ToolDecision{
					Actual: "grep",
				},
			},
			{
				Name:        EventDelegation,
				CaseID:      "aq06",
				FailureType: FailureRuntime,
				FinalStatus: StatusFail,
				ReplayRef:   "replay://session/aq06",
				Delegation:  Delegation{AgentID: "sub-1", AgentType: "subagent"},
			},
		},
		Candidates: []GateCandidateRef{
			{CaseID: "aq06"},
		},
	})

	assert.Equal(t, []string{"grep"}, input.ToolActualByCaseID["aq01"])
	assert.Len(t, input.EventsByCase["aq06"], 1)
	assert.True(t, input.CandidateByCaseID["aq06"])
	assert.Equal(t, "replay://session/aq06", input.ReplayRefByCaseID["aq06"])
}
