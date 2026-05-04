package sessiontodo

import (
	"fmt"
	"strings"
)

type ResumeMode string

const (
	ResumeModeManual ResumeMode = "manual"
	ResumeModeAuto   ResumeMode = "auto"
)

type ResumeOptions struct {
	Mode                 ResumeMode
	BudgetOK             bool
	RuntimeEpoch         string
	ExpectedRuntimeEpoch string
	Execute              bool
}

type ResumeAction struct {
	Allowed        bool       `json:"allowed"`
	Mode           ResumeMode `json:"mode"`
	Reason         string     `json:"reason,omitempty"`
	Prompt         string     `json:"prompt,omitempty"`
	PlanVersion    int64      `json:"plan_version,omitempty"`
	RuntimeEpoch   string     `json:"runtime_epoch,omitempty"`
	PendingTodoIDs []string   `json:"pending_todo_ids,omitempty"`
}

func PlanResumeAction(snapshot Snapshot, opts ResumeOptions) ResumeAction {
	mode := opts.Mode
	if mode == "" {
		mode = ResumeModeManual
	}
	action := ResumeAction{Mode: mode}
	if snapshot.PlanStatus != PlanStatusPaused {
		action.Reason = fmt.Sprintf("plan status is not paused: %s", snapshot.PlanStatus)
		return action
	}
	if mode == ResumeModeAuto && !opts.BudgetOK {
		action.Reason = "budget guard rejected auto continue"
		return action
	}
	if opts.ExpectedRuntimeEpoch != "" && snapshot.RuntimeEpoch != "" && opts.ExpectedRuntimeEpoch != snapshot.RuntimeEpoch {
		action.Reason = "runtime epoch mismatch"
		return action
	}
	if opts.Execute && snapshot.RuntimeEpoch != "" && opts.ExpectedRuntimeEpoch == "" {
		action.Reason = "expected runtime epoch is required for execute"
		return action
	}
	pending := pendingTodos(snapshot.Todos)
	if len(pending) == 0 {
		action.Reason = "no pending todos to resume"
		return action
	}
	action.Allowed = true
	action.PlanVersion = snapshot.PlanVersion
	action.RuntimeEpoch = snapshot.RuntimeEpoch
	action.PendingTodoIDs = make([]string, 0, len(pending))
	parts := make([]string, 0, len(pending))
	for _, todo := range pending {
		action.PendingTodoIDs = append(action.PendingTodoIDs, todo.ID)
		content := strings.TrimSpace(todo.Content)
		if content == "" {
			content = todo.ID
		}
		parts = append(parts, "- "+content)
	}
	action.Prompt = "继续当前 paused plan，优先完成以下 pending todos：\n" + strings.Join(parts, "\n")
	return action
}

func pendingTodos(todos []Todo) []Todo {
	out := make([]Todo, 0)
	for _, todo := range todos {
		if todo.Status == TodoStatusPending || todo.Status == TodoStatusInProgress {
			out = append(out, todo)
		}
	}
	return out
}
