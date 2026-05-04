package sessiontodo

import (
	"context"
	"fmt"
	"strings"

	"github.com/chef-guo/agents-hive/internal/specdriven"
)

const SourceSpecProjected = "spec_projected"

type SyncFromSpecInput struct {
	SessionID string
	ChangeID  string
	Revision  int64
	Plan      specdriven.Plan
}

func SyncFromSpec(ctx context.Context, store Store, input SyncFromSpecInput) (Snapshot, error) {
	if store == nil {
		return Snapshot{}, fmt.Errorf("%w: store is required", ErrInvalidInput)
	}
	if strings.TrimSpace(input.SessionID) == "" {
		return Snapshot{}, fmt.Errorf("%w: sessionID is required", ErrInvalidInput)
	}
	if strings.TrimSpace(input.ChangeID) == "" {
		return Snapshot{}, fmt.Errorf("%w: changeID is required", ErrInvalidInput)
	}
	if input.Revision <= 0 {
		return Snapshot{}, fmt.Errorf("%w: revision must be positive", ErrInvalidInput)
	}
	if len(input.Plan.Steps) == 0 {
		return Snapshot{}, fmt.Errorf("%w: plan steps are required", ErrInvalidInput)
	}

	current, err := store.Snapshot(ctx, input.SessionID)
	if err != nil {
		return Snapshot{}, err
	}
	next := projectSpecTodos(current.Todos, input)
	return store.Replace(ctx, input.SessionID, current.PlanVersion, next)
}

func projectSpecTodos(current []Todo, input SyncFromSpecInput) []TodoInput {
	desiredByID := make(map[string]specdriven.PlanStep, len(input.Plan.Steps))
	desiredOrder := make([]string, 0, len(input.Plan.Steps))
	for _, step := range input.Plan.Steps {
		id := projectedTodoID(input.ChangeID, step.TaskKey)
		desiredByID[id] = step
		desiredOrder = append(desiredOrder, id)
	}

	out := make([]TodoInput, 0, len(current)+len(input.Plan.Steps))
	existingProjected := make(map[string]Todo, len(input.Plan.Steps))
	staleProjected := make([]Todo, 0)
	for _, todo := range current {
		if todo.Source != SourceSpecProjected || todo.SourceChangeID != input.ChangeID {
			out = append(out, todoInputFromTodo(todo, len(out)))
			continue
		}
		if _, ok := desiredByID[todo.ID]; ok {
			existingProjected[todo.ID] = todo
			continue
		}
		staleProjected = append(staleProjected, todo)
	}

	for _, id := range desiredOrder {
		step := desiredByID[id]
		if existing, ok := existingProjected[id]; ok {
			next := todoInputFromTodo(existing, len(out))
			next.Content = projectedTodoContent(step)
			next.Source = SourceSpecProjected
			next.SourceChangeID = input.ChangeID
			next.SourceRevision = input.Revision
			out = append(out, next)
			continue
		}
		out = append(out, TodoInput{
			ID:             id,
			Content:        projectedTodoContent(step),
			Status:         TodoStatusPending,
			Order:          len(out),
			Source:         SourceSpecProjected,
			SourceChangeID: input.ChangeID,
			SourceRevision: input.Revision,
		})
	}
	for _, todo := range staleProjected {
		next := todoInputFromTodo(todo, len(out))
		next.Status = TodoStatusCancelled
		next.SourceRevision = input.Revision
		out = append(out, next)
	}
	for i := range out {
		out[i].Order = i
	}
	return out
}

func todoInputFromTodo(todo Todo, order int) TodoInput {
	return TodoInput{
		ID:               todo.ID,
		Content:          todo.Content,
		Status:           todo.Status,
		Order:            order,
		Source:           todo.Source,
		TraceID:          todo.TraceID,
		SpanID:           todo.SpanID,
		TurnID:           todo.TurnID,
		RuntimeEpoch:     todo.RuntimeEpoch,
		SourceChangeID:   todo.SourceChangeID,
		SourceRevision:   todo.SourceRevision,
		SourceToolCallID: todo.SourceToolCallID,
	}
}

func projectedTodoID(changeID, taskKey string) string {
	return fmt.Sprintf("spec:%s:%s", strings.TrimSpace(changeID), strings.TrimSpace(taskKey))
}

func projectedTodoContent(step specdriven.PlanStep) string {
	taskKey := strings.TrimSpace(step.TaskKey)
	toolName := strings.TrimSpace(step.ToolName)
	if toolName == "" {
		return taskKey
	}
	return fmt.Sprintf("%s: %s", taskKey, toolName)
}
