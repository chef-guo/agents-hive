package sessiontodo

import (
	"strings"

	"github.com/chef-guo/agents-hive/internal/specdriven"
)

type SpecProgressPatch struct {
	SessionID      string `json:"session_id"`
	ChangeID       string `json:"change_id"`
	LastTaskKey    string `json:"last_task_key"`
	SourceRevision int64  `json:"source_revision"`
	PlanVersion    int64  `json:"plan_version"`
}

func BuildSpecProgressPatch(snapshot Snapshot) (SpecProgressPatch, bool) {
	changeID := ""
	bestTaskKey := ""
	var sourceRevision int64
	for _, todo := range snapshot.Todos {
		if todo.Source != SourceSpecProjected || todo.SourceChangeID == "" {
			continue
		}
		if changeID == "" {
			changeID = todo.SourceChangeID
		}
		if todo.SourceChangeID != changeID {
			return SpecProgressPatch{}, false
		}
		if todo.Status != TodoStatusCompleted {
			continue
		}
		taskKey := projectedTaskKey(todo.ID, todo.SourceChangeID)
		if taskKey == "" {
			continue
		}
		if bestTaskKey == "" || specdriven.CompareTaskKey(taskKey, bestTaskKey) > 0 {
			bestTaskKey = taskKey
			sourceRevision = todo.SourceRevision
		}
	}
	if changeID == "" || bestTaskKey == "" {
		return SpecProgressPatch{}, false
	}
	return SpecProgressPatch{
		SessionID:      snapshot.SessionID,
		ChangeID:       changeID,
		LastTaskKey:    bestTaskKey,
		SourceRevision: sourceRevision,
		PlanVersion:    snapshot.PlanVersion,
	}, true
}

func projectedTaskKey(todoID, changeID string) string {
	prefix := "spec:" + changeID + ":"
	if !strings.HasPrefix(todoID, prefix) {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(todoID, prefix))
}
