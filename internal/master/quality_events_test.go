package master

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/chef-guo/agents-hive/internal/agentquality"
	"github.com/chef-guo/agents-hive/internal/journal"
	"github.com/chef-guo/agents-hive/internal/llm"
	"github.com/chef-guo/agents-hive/internal/observability"
	"github.com/chef-guo/agents-hive/internal/router"
)

func TestMaster_EnqueueLog_NilSafe(t *testing.T) {
	m := &Master{}
	assert.NotPanics(t, func() {
		m.enqueueLog(observability.LogEntry{Level: "info", Message: "x"})
	})
}

func TestMaster_SetObservabilityWriters_DoNotResetQueue(t *testing.T) {
	m := &Master{}
	m.SetLogWriter(&observability.NoopLogWriter{})
	ch := m.obsCh
	m.SetTracer(&observability.NoopTracer{})
	m.SetMetricsWriter(&observability.NoopMetricsWriter{})
	assert.True(t, ch == m.obsCh)
}

func TestEmitQualityEvent_EnqueuesMetricAndLog(t *testing.T) {
	m := &Master{obsCh: make(chan observabilityEntry, 4)}
	m.emitQualityEvent("trace", "span", "session-1", agentquality.Event{
		Name:        agentquality.EventToolDecision,
		Route:       "web",
		FailureType: agentquality.FailureNone,
		FinalStatus: agentquality.StatusPass,
		ToolDecision: agentquality.ToolDecision{
			Actual:   "grep",
			Decision: agentquality.DecisionExpected,
		},
	})

	first := <-m.obsCh
	second := <-m.obsCh
	require.NotNil(t, first.metric)
	require.NotNil(t, second.log)
	assert.Equal(t, "quality.tool_decision", first.metric.Name)
	assert.NotContains(t, first.metric.Labels, "session_id")
	assert.Equal(t, "session-1", second.log.SessionID)
}

func TestRecordToolRecall_EmitsQualityEvent(t *testing.T) {
	m := &Master{obsCh: make(chan observabilityEntry, 4)}
	session := &SessionState{ID: "session-1"}

	m.recordToolRecall("trace", "span", session, agentquality.ToolRecall{
		Mode:                     "inject",
		TurnID:                   "turn-1",
		TraceID:                  "trace",
		QueryPreview:             "发送给飞书用户郭松",
		CandidateCount:           1,
		CandidateNames:           []string{"feishu_api"},
		CandidateScores:          map[string]float64{"feishu_api": 0.91},
		VisibleBeforeCount:       6,
		VisibleAfterCount:        7,
		SelectedTool:             "feishu_api",
		ModelUsedRecalledTool:    true,
		BlockedByPlanGate:        false,
		SideEffectCandidateCount: 1,
	})

	first := <-m.obsCh
	second := <-m.obsCh
	require.NotNil(t, first.metric)
	require.NotNil(t, second.log)
	assert.Equal(t, "quality.tool_recall", first.metric.Name)
	raw, ok := second.log.Attributes["quality_event"].(json.RawMessage)
	require.True(t, ok)
	assert.Contains(t, string(raw), `"name":"quality.tool_recall"`)
	assert.Contains(t, string(raw), `"selected_tool":"feishu_api"`)
	assert.Contains(t, string(raw), `"model_used_recalled_tool":true`)
	assert.Contains(t, string(raw), `"side_effect_candidate_count":1`)
}

func TestRecordReflectionAddsStructuralReflectionBlock(t *testing.T) {
	m := &Master{obsCh: make(chan observabilityEntry, 4)}
	session := &SessionState{ID: "session-1"}

	m.recordReflection("trace", "span", session, reflectionNoteInput{
		Trigger:     "call_failure",
		Severity:    "warn",
		ToolName:    "feishu_api",
		Detail:      "permission denied",
		FailureKind: "permission_denied",
	})

	blocks := session.ListReflectionBlocks()
	require.Len(t, blocks, 1)
	assert.Equal(t, "feishu_api", blocks[0].ToolName)
	assert.Equal(t, "exec", blocks[0].Mode)
	assert.Equal(t, "permission_denied", blocks[0].FailureKind)
}

func TestRecordReflectionIgnoresTransientReflectionBlock(t *testing.T) {
	m := &Master{obsCh: make(chan observabilityEntry, 4)}
	session := &SessionState{ID: "session-1"}

	m.recordReflection("trace", "span", session, reflectionNoteInput{
		Trigger:     "call_failure",
		Severity:    "warn",
		ToolName:    "web_fetch",
		Detail:      "timeout",
		FailureKind: "timeout",
	})

	assert.Empty(t, session.ListReflectionBlocks())
}

func TestReflectionFailureKindFromToolError(t *testing.T) {
	tests := map[string]string{
		"permission denied":             "permission_denied",
		"403 forbidden":                 "4xx",
		"invalid credentials from host": "auth",
		"schema validation failed":      "schema_invalid",
		"timeout":                       "",
	}
	for input, want := range tests {
		if got := reflectionFailureKindFromToolError(input); got != want {
			t.Fatalf("reflectionFailureKindFromToolError(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestRecordRouteDecisionSpan_EnqueuesReplayLog(t *testing.T) {
	m := &Master{obsCh: make(chan observabilityEntry, 2)}
	session := &SessionState{ID: "session-1"}

	m.recordRouteDecisionSpan("trace", "span", session, router.DecisionSpan{
		TraceID:       "trace",
		SessionIDHash: "sha256:test",
		Intent:        router.DecisionSpanIntent{Kind: router.IntentCreateSkill, Source: "rule"},
		Allowed:       []string{"skill"},
		Mode:          router.DecisionModeAllow,
	})

	got := <-m.obsCh
	require.NotNil(t, got.log)
	assert.Equal(t, "quality.route_decision.span", got.log.Message)
	assert.Equal(t, "trace", got.log.TraceID)
	_, ok := got.log.Attributes["route_decision_span"].(router.DecisionSpan)
	assert.True(t, ok)
}

func TestHashToolArgs_StableForJSONKeyOrder(t *testing.T) {
	a := json.RawMessage(`{"b":2,"a":1}`)
	b := json.RawMessage(`{"a":1,"b":2}`)
	assert.Equal(t, hashToolArgs(a), hashToolArgs(b))
	assert.NotEmpty(t, hashToolArgs(a))
}

func TestAgentTurnAttributes_ContainLongRunFields(t *testing.T) {
	attrs := agentTurnAttributes("turn-1", 3, 3, 2, 12, 8, true)

	assert.Equal(t, "turn-1", attrs["turn_id"])
	assert.Equal(t, 3, attrs["turn_index"])
	assert.Equal(t, 3, attrs["llm_call_count"])
	assert.Equal(t, 2, attrs["tool_call_count"])
	assert.Equal(t, 12, attrs["prepared_message_count"])
	assert.Equal(t, 8, attrs["visible_tool_count"])
	assert.Equal(t, true, attrs["compaction_triggered"])
}

func TestEmitQualityEvent_EnqueuesJournalDecision(t *testing.T) {
	m := &Master{
		journal:   journal.NoopJournal{},
		journalCh: make(chan journalEntry, 1),
	}
	m.emitQualityEvent("trace", "span", "session-1", agentquality.Event{
		Name:        agentquality.EventAgentTurn,
		Route:       "web",
		FailureType: agentquality.FailureTool,
		FinalStatus: agentquality.StatusFail,
	})

	got := <-m.journalCh
	require.NotNil(t, got.decision)
	assert.Equal(t, "quality.agent_turn", got.decision.Decision)
	assert.Contains(t, got.decision.Reason, `"failure_type":"tool"`)
}

func TestRecordReflection_AppendsNoteAndEmitsEvent(t *testing.T) {
	m := &Master{obsCh: make(chan observabilityEntry, 4)}
	session := &SessionState{ID: "session-1"}

	m.recordReflection("trace", "span", session, reflectionNoteInput{
		Trigger:     "batch_loop",
		Severity:    "warn",
		Consecutive: 3,
	})

	require.Len(t, session.Messages, 1)
	msg := session.Messages[0]
	assert.Equal(t, "system", msg.Role)
	assert.Contains(t, msg.Content.Text(), "连续出现 3 次")
	assert.Equal(t, "batch_loop", msg.Metadata["reflection_trigger"])

	first := <-m.obsCh
	second := <-m.obsCh
	require.NotNil(t, first.metric)
	require.NotNil(t, second.log)
	assert.Equal(t, "quality.reflection", first.metric.Name)
	assert.Equal(t, "batch_loop", first.metric.Labels["reflection_trigger"])
	assert.Equal(t, "warn", first.metric.Labels["severity"])

	raw, ok := second.log.Attributes["quality_event"].(json.RawMessage)
	require.True(t, ok)
	assert.Contains(t, string(raw), `"trigger":"batch_loop"`)
	assert.Contains(t, string(raw), `"injected":true`)
}

func TestRecordReflection_DoesNotLeakDetail(t *testing.T) {
	m := &Master{obsCh: make(chan observabilityEntry, 4)}
	session := &SessionState{
		ID: "session-1",
		Messages: []llm.MessageWithTools{{
			Role:    "user",
			Content: llm.NewTextContent("hello"),
		}},
	}
	secret := strings.Repeat("secret stack trace ", 10)

	m.recordReflection("trace", "span", session, reflectionNoteInput{
		Trigger:  "guard_failure",
		Severity: "hard_stop",
		Detail:   secret,
	})

	require.Len(t, session.Messages, 2)
	note := session.Messages[1].Content.Text()
	assert.NotContains(t, note, secret)
	assert.NotContains(t, note, "secret stack trace")

	<-m.obsCh
	logEntry := <-m.obsCh
	raw, ok := logEntry.log.Attributes["quality_event"].(json.RawMessage)
	require.True(t, ok)
	assert.NotContains(t, string(raw), secret)
	assert.NotContains(t, string(raw), "secret stack trace")
}

func TestRecordReflection_TriggersMapToLowCardinalityEvents(t *testing.T) {
	tests := []struct {
		name        string
		input       reflectionNoteInput
		status      agentquality.FinalStatus
		failureType agentquality.FailureType
	}{
		{
			name: "loop hard stop",
			input: reflectionNoteInput{
				Trigger:     "batch_loop",
				Severity:    "hard_stop",
				Consecutive: 5,
			},
			status:      agentquality.StatusFail,
			failureType: agentquality.FailureTool,
		},
		{
			name: "call failure",
			input: reflectionNoteInput{
				Trigger:     "call_failure",
				Severity:    "warn",
				ToolName:    "read_file",
				Consecutive: 2,
			},
			status:      agentquality.StatusPass,
			failureType: agentquality.FailureTool,
		},
		{
			name:        "guard failure",
			input:       reflectionNoteInput{Trigger: "guard_failure", Severity: "warn"},
			status:      agentquality.StatusPass,
			failureType: agentquality.FailurePrompt,
		},
		{
			name:        "validation failure",
			input:       reflectionNoteInput{Trigger: "validation_failure", Severity: "hard_stop"},
			status:      agentquality.StatusFail,
			failureType: agentquality.FailureModel,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := &Master{obsCh: make(chan observabilityEntry, 4)}
			session := &SessionState{ID: "session-1"}

			m.recordReflection("trace", "span", session, tt.input)

			<-m.obsCh
			logEntry := <-m.obsCh
			raw, ok := logEntry.log.Attributes["quality_event"].(json.RawMessage)
			require.True(t, ok)
			var ev agentquality.Event
			require.NoError(t, json.Unmarshal(raw, &ev))
			assert.Equal(t, agentquality.EventReflection, ev.Name)
			assert.Equal(t, tt.input.Trigger, ev.Reflection.Trigger)
			assert.Equal(t, tt.input.Severity, ev.Reflection.Severity)
			assert.Equal(t, tt.input.ToolName, ev.Reflection.ToolName)
			assert.Equal(t, tt.input.Consecutive, ev.Reflection.Consecutive)
			assert.Equal(t, tt.status, ev.FinalStatus)
			assert.Equal(t, tt.failureType, ev.FailureType)
		})
	}
}
