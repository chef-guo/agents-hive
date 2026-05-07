package agentquality

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestQualityEvent_JSONStable(t *testing.T) {
	ev := Event{
		Name:          EventAgentTurn,
		CaseID:        "aq01",
		SessionIDHash: "sha256:abc",
		Route:         "web",
		Prompt: PromptRef{
			Key:     "system/base",
			Version: "sha256:1111",
			Source:  "db",
		},
		FailureType: FailureTool,
		FinalStatus: StatusFail,
		Attributes:  map[string]any{"trace_id": "trace-1"},
	}

	b, err := json.Marshal(ev)
	require.NoError(t, err)
	assert.Contains(t, string(b), `"name":"quality.agent_turn"`)
	assert.Contains(t, string(b), `"failure_type":"tool"`)
	assert.Contains(t, string(b), `"final_status":"fail"`)
}

func TestToolRecall_JSONStable(t *testing.T) {
	ev := Event{
		Name: EventToolRecall,
		ToolRecall: ToolRecall{
			Mode:                  "inject",
			TurnID:                "turn-1",
			TraceID:               "trace-1",
			QueryPreview:          "发送给飞书用户郭松",
			CandidateCount:        2,
			CandidateNames:        []string{"feishu_api", "send_im_message"},
			CandidateScores:       map[string]float64{"feishu_api": 0.91},
			VisibleBeforeCount:    6,
			VisibleAfterCount:     7,
			SelectedTool:          "feishu_api",
			ModelUsedRecalledTool: true,
		},
	}

	b, err := json.Marshal(ev)
	require.NoError(t, err)
	assert.Contains(t, string(b), `"name":"quality.tool_recall"`)
	assert.Contains(t, string(b), `"mode":"inject"`)
	assert.Contains(t, string(b), `"selected_tool":"feishu_api"`)
}

func TestMetricLabels_AreLowCardinality(t *testing.T) {
	labels := MetricLabels(Event{
		Name:         EventToolDecision,
		Route:        "im",
		ToolDecision: ToolDecision{Actual: "grep", Decision: DecisionExpected},
		FailureType:  FailureNone,
		FinalStatus:  StatusPass,
	})

	assert.Equal(t, "im", labels["route"])
	assert.Equal(t, "grep", labels["tool_name"])
	assert.Equal(t, DecisionExpected, labels["decision"])
	assert.NotContains(t, labels, "session_id")
	assert.NotContains(t, labels, "user_id")
	assert.NotContains(t, labels, "trace_id")
}

func TestReflectionMetricLabelsPreserveStatus(t *testing.T) {
	labels := MetricLabels(Event{
		Name:        EventReflection,
		FinalStatus: StatusNeedsUser,
		Reflection: Reflection{
			Trigger:  "batch_loop",
			Severity: "warn",
		},
	})

	assert.Equal(t, "batch_loop", labels["reflection_trigger"])
	assert.Equal(t, "warn", labels["severity"])
	assert.Equal(t, "needs_user", labels["status"])
}
