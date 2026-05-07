package master

import (
	"context"
	"fmt"
	"time"

	"github.com/chef-guo/agents-hive/internal/agentquality"
	"github.com/chef-guo/agents-hive/internal/sandbox"
)

type ValidationExecutor interface {
	Execute(ctx context.Context, req sandbox.ExecRequest) (sandbox.ExecResult, error)
}

type validationFeedbackRunner struct {
	enabled  bool
	executor ValidationExecutor
	record   func(agentquality.Event)
}

func (r validationFeedbackRunner) Run(ctx context.Context, sessionID, traceID string, commands []agentquality.ValidationCommand) {
	if !r.enabled || r.executor == nil || r.record == nil {
		return
	}
	for _, command := range commands {
		timeout := time.Duration(command.TimeoutSec) * time.Second
		result, err := r.executor.Execute(ctx, sandbox.ExecRequest{
			Command:   command.Command,
			SessionID: sessionID,
			Timeout:   timeout,
		})
		severity := "info"
		status := agentquality.StatusPass
		if err != nil || result.ExitCode != 0 {
			severity = "warn"
			status = agentquality.StatusNeedsUser
		}
		attrs := map[string]any{
			"command":   command.Name,
			"exit_code": result.ExitCode,
		}
		if err != nil {
			attrs["error"] = err.Error()
		}
		if result.Stderr != "" {
			attrs["stderr"] = result.Stderr
		}
		r.record(agentquality.Event{
			Name:        agentquality.EventReflection,
			FailureType: agentquality.FailureRuntime,
			FinalStatus: status,
			Reflection: agentquality.Reflection{
				Trigger:  "test_driven",
				Severity: severity,
				Summary:  fmt.Sprintf("validation command %s finished", command.Name),
			},
			Attributes: attrs,
		})
	}
}
