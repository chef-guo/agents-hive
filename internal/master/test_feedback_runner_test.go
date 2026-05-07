package master

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/chef-guo/agents-hive/internal/agentquality"
	"github.com/chef-guo/agents-hive/internal/config"
	"github.com/chef-guo/agents-hive/internal/sandbox"
)

type fakeValidationExecutor struct {
	result sandbox.ExecResult
	err    error
	calls  int
}

func (e *fakeValidationExecutor) Execute(context.Context, sandbox.ExecRequest) (sandbox.ExecResult, error) {
	e.calls++
	return e.result, e.err
}

func TestValidationFeedbackRunnerNoopsWhenDisabled(t *testing.T) {
	exec := &fakeValidationExecutor{}
	var events []agentquality.Event
	runner := validationFeedbackRunner{
		enabled:  false,
		executor: exec,
		record: func(ev agentquality.Event) {
			events = append(events, ev)
		},
	}

	runner.Run(context.Background(), "session-1", "trace-1", []agentquality.ValidationCommand{{Name: "go test", Command: "go test ./...", TimeoutSec: 1}})

	if exec.calls != 0 {
		t.Fatalf("executor calls = %d, want 0", exec.calls)
	}
	if len(events) != 0 {
		t.Fatalf("events = %d, want 0", len(events))
	}
}

func TestChangedFilesFromToolCall(t *testing.T) {
	cases := []struct {
		name string
		tool string
		args json.RawMessage
		want []string
	}{
		{
			name: "write_file",
			tool: "write_file",
			args: json.RawMessage(`{"path":"internal/master/example.go"}`),
			want: []string{"internal/master/example.go"},
		},
		{
			name: "multiedit",
			tool: "multiedit",
			args: json.RawMessage(`{"edits":[{"path":"internal/a/a.go"},{"path":"frontend/src/App.tsx"}]}`),
			want: []string{"frontend/src/App.tsx", "internal/a/a.go"},
		},
		{
			name: "apply_patch",
			tool: "apply_patch",
			args: json.RawMessage(`{"patch":"--- a/internal/agentquality/a.go\n+++ b/internal/agentquality/a.go\n@@ -1 +1 @@\n-old\n+new\n"}`),
			want: []string{"internal/agentquality/a.go"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := changedFilesFromToolCall(tc.tool, tc.args)
			if len(got) != len(tc.want) {
				t.Fatalf("changed files = %#v, want %#v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("changed files = %#v, want %#v", got, tc.want)
				}
			}
		})
	}
}

func TestRunTestDrivenShadowForToolChange(t *testing.T) {
	exec := &fakeValidationExecutor{}
	m := &Master{
		config: Config{
			Reflection: config.ReflectionConfig{
				TestDrivenShadow: config.ReflectionShadowConfig{Enabled: true},
			},
		},
		validationExec: exec,
		obsCh:          make(chan observabilityEntry, 4),
	}

	m.runTestDrivenShadowForToolChange(context.Background(), "session-1", "trace-1", "span-1", "write_file", json.RawMessage(`{"path":"internal/master/shadow.go"}`))

	if exec.calls != 1 {
		t.Fatalf("executor calls = %d, want 1", exec.calls)
	}
	if len(m.obsCh) != 2 {
		t.Fatalf("obs events = %d, want quality metric+log", len(m.obsCh))
	}
}

func TestValidationFeedbackRunnerRecordsReflection(t *testing.T) {
	exec := &fakeValidationExecutor{result: sandbox.ExecResult{ExitCode: 1, Stderr: "failed"}}
	var events []agentquality.Event
	runner := validationFeedbackRunner{
		enabled:  true,
		executor: exec,
		record: func(ev agentquality.Event) {
			events = append(events, ev)
		},
	}

	runner.Run(context.Background(), "session-1", "trace-1", []agentquality.ValidationCommand{{Name: "go test", Command: "go test ./...", TimeoutSec: 1}})

	if exec.calls != 1 {
		t.Fatalf("executor calls = %d, want 1", exec.calls)
	}
	if len(events) != 1 {
		t.Fatalf("events = %d, want 1", len(events))
	}
	if events[0].Name != agentquality.EventReflection || events[0].Reflection.Trigger != "test_driven" || events[0].Reflection.Severity != "warn" {
		t.Fatalf("unexpected event: %+v", events[0])
	}
}

func TestValidationFeedbackRunnerRecordsExecutorError(t *testing.T) {
	exec := &fakeValidationExecutor{err: errors.New("denied")}
	var events []agentquality.Event
	runner := validationFeedbackRunner{
		enabled:  true,
		executor: exec,
		record: func(ev agentquality.Event) {
			events = append(events, ev)
		},
	}

	runner.Run(context.Background(), "session-1", "trace-1", []agentquality.ValidationCommand{{Name: "go test", Command: "go test ./...", TimeoutSec: 1}})

	if len(events) != 1 {
		t.Fatalf("events = %d, want 1", len(events))
	}
	if got := events[0].Attributes["error"]; got != "denied" {
		t.Fatalf("error attribute = %v, want denied", got)
	}
}
