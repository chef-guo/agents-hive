package agentquality

import "time"

type EventName string

const (
	EventAgentTurn          EventName = "quality.agent_turn"
	EventToolDecision       EventName = "quality.tool_decision"
	EventContextBuild       EventName = "quality.context_build"
	EventPermissionDecision EventName = "quality.permission_decision"
	EventDelegation         EventName = "quality.delegation"
	EventReflection         EventName = "quality.reflection"
	EventToolRecall         EventName = "quality.tool_recall"
	EventRouteDecision      EventName = "quality.route_decision"
	EventBudgetExit         EventName = "quality.budget_exit"
)

type FailureType string

const (
	FailureNone       FailureType = "none"
	FailurePrompt     FailureType = "prompt"
	FailureTool       FailureType = "tool"
	FailureSkill      FailureType = "skill"
	FailureContext    FailureType = "context"
	FailureModel      FailureType = "model"
	FailurePermission FailureType = "permission"
	FailureRuntime    FailureType = "runtime"
	FailureUserInput  FailureType = "user_input"
)

type FinalStatus string

const (
	StatusPass      FinalStatus = "pass"
	StatusFail      FinalStatus = "fail"
	StatusBlocked   FinalStatus = "blocked"
	StatusNeedsUser FinalStatus = "needs_user"
)

type Decision string

const (
	DecisionExpected   Decision = "expected"
	DecisionAllowed    Decision = "allowed"
	DecisionUnexpected Decision = "unexpected"
	DecisionRejected   Decision = "rejected"
)

type PromptRef struct {
	Key      string `json:"key,omitempty"`
	Version  string `json:"version,omitempty"`
	Source   string `json:"source,omitempty"`
	Language string `json:"language,omitempty"`
}

type ToolDecision struct {
	Expected []string `json:"expected,omitempty"`
	Actual   string   `json:"actual,omitempty"`
	Decision Decision `json:"decision,omitempty"`
	ArgsHash string   `json:"args_hash,omitempty"`
}

type ToolRecall struct {
	Mode                     string             `json:"mode,omitempty"`
	TurnID                   string             `json:"turn_id,omitempty"`
	TraceID                  string             `json:"trace_id,omitempty"`
	QueryPreview             string             `json:"query_preview,omitempty"`
	CandidateCount           int                `json:"candidate_count,omitempty"`
	CandidateNames           []string           `json:"candidate_names,omitempty"`
	CandidateScores          map[string]float64 `json:"candidate_scores,omitempty"`
	VisibleBeforeCount       int                `json:"visible_before_count,omitempty"`
	VisibleAfterCount        int                `json:"visible_after_count,omitempty"`
	VisibleTrimmedCount      int                `json:"visible_trimmed_count,omitempty"`
	MaxVisibleTools          int                `json:"max_visible_tools,omitempty"`
	SelectedTool             string             `json:"selected_tool,omitempty"`
	ModelUsedRecalledTool    bool               `json:"model_used_recalled_tool,omitempty"`
	BlockedByPlanGate        bool               `json:"blocked_by_plan_gate,omitempty"`
	SideEffectCandidateCount int                `json:"side_effect_candidate_count,omitempty"`
}

type Delegation struct {
	ParentTraceID string   `json:"parent_trace_id,omitempty"`
	ChildTraceID  string   `json:"child_trace_id,omitempty"`
	AgentID       string   `json:"agent_id,omitempty"`
	AgentType     string   `json:"agent_type,omitempty"`
	GroupID       string   `json:"group_id,omitempty"`
	SpawnDepth    int      `json:"spawn_depth,omitempty"`
	MaxTurns      int      `json:"max_turns,omitempty"`
	ToolWhitelist []string `json:"tool_whitelist,omitempty"`
	StopReason    string   `json:"stop_reason,omitempty"`
}

type Reflection struct {
	Trigger     string   `json:"trigger,omitempty"`  // batch_loop | call_failure | guard_failure | validation_failure
	Severity    string   `json:"severity,omitempty"` // info | warn | hard_stop
	ToolName    string   `json:"tool_name,omitempty"`
	Consecutive int      `json:"consecutive,omitempty"`
	Summary     string   `json:"summary,omitempty"`
	Recommended []string `json:"recommended,omitempty"`
	Injected    bool     `json:"injected,omitempty"`
}

type ContextBuild struct {
	MessageCount          int      `json:"message_count"`
	Compressed            bool     `json:"compressed"`
	MemoryInjected        bool     `json:"memory_injected"`
	MemoryIDs             []int64  `json:"memory_ids,omitempty"`
	SkippedMemoryIDs      []int64  `json:"skipped_memory_ids,omitempty"`
	SkippedExpired        int      `json:"skipped_expired,omitempty"`
	SkippedLowTrust       int      `json:"skipped_low_trust,omitempty"`
	SkippedCrossUser      int      `json:"skipped_cross_user,omitempty"`
	SkippedScope          int      `json:"skipped_scope,omitempty"`
	SkippedLowScore       int      `json:"skipped_low_score,omitempty"`
	SkippedTokenBudget    int      `json:"skipped_token_budget,omitempty"`
	SkippedFeedbackBudget int      `json:"skipped_feedback_budget,omitempty"`
	SkippedRegularBudget  int      `json:"skipped_regular_budget,omitempty"`
	SkippedMemoryTotal    int      `json:"skipped_memory_total,omitempty"`
	FeedbackMemoryCount   int      `json:"feedback_memory_count,omitempty"`
	RegularMemoryCount    int      `json:"regular_memory_count,omitempty"`
	AttachmentCount       int      `json:"attachment_count"`
	PromptVersions        []string `json:"prompt_versions,omitempty"`
	EstimatedTokens       int      `json:"estimated_tokens,omitempty"`
	ContaminationCheck    string   `json:"contamination_check,omitempty"`
}

type Event struct {
	Name          EventName          `json:"name"`
	CaseID        string             `json:"case_id,omitempty"`
	SessionIDHash string             `json:"session_id_hash,omitempty"`
	Route         string             `json:"route,omitempty"`
	Prompt        PromptRef          `json:"prompt,omitempty"`
	ToolDecision  ToolDecision       `json:"tool_decision,omitempty"`
	ToolRecall    ToolRecall         `json:"tool_recall,omitempty"`
	RouteDecision RouteDecisionEvent `json:"route_decision,omitempty"`
	ContextBuild  ContextBuild       `json:"context_build,omitempty"`
	Delegation    Delegation         `json:"delegation,omitempty"`
	Reflection    Reflection         `json:"reflection,omitempty"`
	FailureType   FailureType        `json:"failure_type,omitempty"`
	RetryReason   string             `json:"retry_reason,omitempty"`
	FinalStatus   FinalStatus        `json:"final_status,omitempty"`
	ReplayRef     string             `json:"replay_ref,omitempty"`
	Attributes    map[string]any     `json:"attributes,omitempty"`
	Ts            time.Time          `json:"ts"`
}

func MetricLabels(ev Event) map[string]any {
	labels := map[string]any{
		"route":        emptyAsUnknown(ev.Route),
		"failure_type": emptyAsUnknown(string(ev.FailureType)),
		"status":       emptyAsUnknown(string(ev.FinalStatus)),
	}
	if ev.ToolDecision.Actual != "" {
		labels["tool_name"] = ev.ToolDecision.Actual
	}
	if ev.ToolDecision.Decision != "" {
		labels["decision"] = ev.ToolDecision.Decision
	}
	if ev.RetryReason != "" {
		labels["retry_reason"] = ev.RetryReason
	}
	if ev.Name == EventReflection {
		if ev.Reflection.Trigger != "" {
			labels["reflection_trigger"] = ev.Reflection.Trigger
		}
		if ev.Reflection.Severity != "" {
			labels["severity"] = ev.Reflection.Severity
		}
	}
	return labels
}

func emptyAsUnknown(v string) string {
	if v == "" {
		return "unknown"
	}
	return v
}
