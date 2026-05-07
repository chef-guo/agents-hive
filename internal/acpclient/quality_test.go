package acpclient

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	acp "github.com/coder/acp-go-sdk"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/chef-guo/agents-hive/internal/subagent"
	"github.com/chef-guo/agents-hive/internal/tools"
)

type fakePromptClient struct {
	resp     acp.PromptResponse
	err      error
	onPrompt func(context.Context, acp.PromptRequest)
}

func (f fakePromptClient) Prompt(ctx context.Context, req acp.PromptRequest) (acp.PromptResponse, error) {
	if f.onPrompt != nil {
		f.onPrompt(ctx, req)
	}
	return f.resp, f.err
}

type recordingDelegationObserver struct {
	mu     sync.Mutex
	events []tools.DelegationEvent
}

func (o *recordingDelegationObserver) RecordDelegation(_ context.Context, ev tools.DelegationEvent) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.events = append(o.events, ev)
}

func (o *recordingDelegationObserver) snapshot() []tools.DelegationEvent {
	o.mu.Lock()
	defer o.mu.Unlock()
	out := make([]tools.DelegationEvent, len(o.events))
	copy(out, o.events)
	return out
}

func TestRemoteACPAgentRecordsPromptStopReason(t *testing.T) {
	tests := []struct {
		name            string
		resp            acp.PromptResponse
		err             error
		wantTaskStatus  string
		wantEventStatus string
		wantStopReason  string
	}{
		{
			name:            "cancelled",
			resp:            acp.PromptResponse{StopReason: acp.StopReasonCancelled},
			wantTaskStatus:  "failed",
			wantEventStatus: "failed",
			wantStopReason:  "cancelled",
		},
		{
			name:            "refusal",
			resp:            acp.PromptResponse{StopReason: acp.StopReasonRefusal},
			wantTaskStatus:  "failed",
			wantEventStatus: "failed",
			wantStopReason:  "refusal",
		},
		{
			name:            "transport error",
			err:             errors.New("pipe closed"),
			wantTaskStatus:  "failed",
			wantEventStatus: "failed",
			wantStopReason:  "transport_error",
		},
		{
			name:            "completed",
			resp:            acp.PromptResponse{StopReason: acp.StopReasonEndTurn},
			wantTaskStatus:  "completed",
			wantEventStatus: "completed",
			wantStopReason:  "end_turn",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			observer := &recordingDelegationObserver{}
			agent := NewRemoteACPAgentWithPromptClient(
				RemoteAgentConfig{Name: "remote-1"},
				fakePromptClient{resp: tt.resp, err: tt.err},
				"acp-remote",
				zap.NewNop(),
				observer,
			)

			payload, _ := json.Marshal(map[string]string{"instruction": "执行远程任务"})
			resp := agent.handleTask(context.Background(), subagent.TaskRequest{
				ID:            "task-1",
				SessionID:     "session-1",
				ParentTraceID: "trace-parent",
				TraceID:       "trace-parent:remote-1",
				Payload:       payload,
			})
			assert.Equal(t, tt.wantTaskStatus, resp.Status)

			events := observer.snapshot()
			require.Len(t, events, 1)
			assert.Equal(t, "session-1", events[0].SessionID)
			assert.Equal(t, "remote-1", events[0].AgentID)
			assert.Equal(t, "acp", events[0].AgentType)
			assert.Equal(t, tt.wantEventStatus, events[0].Status)
			assert.Equal(t, tt.wantStopReason, events[0].StopReason)
			assert.Equal(t, "trace-parent", events[0].ParentTraceID)
			assert.Equal(t, "trace-parent:remote-1", events[0].ChildTraceID)
		})
	}
}

func TestRemoteACPAgentDerivesChildTraceWhenRequestTraceMissing(t *testing.T) {
	observer := &recordingDelegationObserver{}
	agent := NewRemoteACPAgentWithPromptClient(
		RemoteAgentConfig{Name: "remote-1"},
		fakePromptClient{resp: acp.PromptResponse{StopReason: acp.StopReasonEndTurn}},
		"acp-remote",
		zap.NewNop(),
		observer,
	)

	resp := agent.handleTask(context.Background(), subagent.TaskRequest{
		ID:            "task-1",
		SessionID:     "session-1",
		ParentTraceID: "trace-parent",
		Payload:       json.RawMessage(`{"instruction":"执行远程任务"}`),
	})
	require.Equal(t, "completed", resp.Status)

	events := observer.snapshot()
	require.Len(t, events, 1)
	assert.Equal(t, "trace-parent", events[0].ParentTraceID)
	assert.Equal(t, "trace-parent:remote-1", events[0].ChildTraceID)
}

func TestRemoteACPAgentAggregatesAssistantOutputFromSessionUpdates(t *testing.T) {
	agent := NewRemoteACPAgentWithPromptClient(
		RemoteAgentConfig{Name: "remote-1"},
		fakePromptClient{},
		"acp-remote",
		zap.NewNop(),
		nil,
	)
	agent.conn = fakePromptClient{
		resp: acp.PromptResponse{StopReason: acp.StopReasonEndTurn},
		onPrompt: func(_ context.Context, _ acp.PromptRequest) {
			agent.handleSessionUpdate(acp.SessionNotification{
				SessionId: "acp-remote",
				Update: acp.SessionUpdate{AgentMessageChunk: &acp.SessionUpdateAgentMessageChunk{
					Content: acp.TextBlock("hello "),
				}},
			})
			agent.handleSessionUpdate(acp.SessionNotification{
				SessionId: "acp-remote",
				Update: acp.SessionUpdate{AgentMessageChunk: &acp.SessionUpdateAgentMessageChunk{
					Content: acp.TextBlock("world"),
				}},
			})
		},
	}

	resp := agent.handleTask(context.Background(), subagent.TaskRequest{
		ID:      "task-1",
		Payload: json.RawMessage(`{"instruction":"say hello"}`),
	})

	require.Equal(t, "completed", resp.Status)
	var result map[string]string
	require.NoError(t, json.Unmarshal(resp.Result, &result))
	assert.Equal(t, "hello world", result["content"])
	assert.Equal(t, "end_turn", result["stop_reason"])
}

func TestRemoteACPAgentAggregatesAssistantOutputFromPromptMeta(t *testing.T) {
	agent := NewRemoteACPAgentWithPromptClient(
		RemoteAgentConfig{Name: "remote-1"},
		fakePromptClient{resp: acp.PromptResponse{
			StopReason: acp.StopReasonEndTurn,
			Meta: map[string]any{
				"result": map[string]any{"content": "meta result"},
			},
		}},
		"acp-remote",
		zap.NewNop(),
		nil,
	)

	resp := agent.handleTask(context.Background(), subagent.TaskRequest{
		ID:      "task-1",
		Payload: json.RawMessage(`{"instruction":"return meta"}`),
	})

	require.Equal(t, "completed", resp.Status)
	var result map[string]string
	require.NoError(t, json.Unmarshal(resp.Result, &result))
	assert.Equal(t, "meta result", result["content"])
	assert.Equal(t, "end_turn", result["stop_reason"])
}
