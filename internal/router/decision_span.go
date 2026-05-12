package router

import "time"

// DecisionSpan 是单次路由决策的本地可重放快照。
type DecisionSpan struct {
	TraceID        string                       `json:"trace_id"`
	SessionIDHash  string                       `json:"session_id_hash,omitempty"`
	CreatedAt      time.Time                    `json:"created_at"`
	Intent         DecisionSpanIntent           `json:"intent"`
	Candidates     []DecisionSpanCandidate      `json:"candidates,omitempty"`
	Allowed        []string                     `json:"allowed,omitempty"`
	AllowedInputs  map[string]map[string]string `json:"allowed_inputs,omitempty"`
	VisibleOnly    []string                     `json:"visible_only,omitempty"`
	Blocked        []string                     `json:"blocked,omitempty"`
	BlockedReasons map[string]string            `json:"blocked_reasons,omitempty"`
	Mode           DecisionMode                 `json:"mode"`
	Reason         string                       `json:"reason,omitempty"`
}

// DecisionSpanIntent 是 replay 需要的意图投影，避免依赖完整分类器实现。
type DecisionSpanIntent struct {
	Kind               IntentKind `json:"kind"`
	Subject            string     `json:"subject,omitempty"`
	AllowedDomainsHint []string   `json:"allowed_domains_hint,omitempty"`
	Confidence         float64    `json:"confidence,omitempty"`
	Source             string     `json:"source,omitempty"`
	Degraded           bool       `json:"degraded,omitempty"`
}

// DecisionSpanCandidate 是候选工具画像的稳定投影。
type DecisionSpanCandidate struct {
	Name        string           `json:"name"`
	Kind        CapabilityKind   `json:"kind,omitempty"`
	Domain      string           `json:"domain,omitempty"`
	Source      CapabilitySource `json:"source,omitempty"`
	Invocation  InvocationMode   `json:"invocation,omitempty"`
	Risk        RiskLevel        `json:"risk,omitempty"`
	Trust       TrustLevel       `json:"trust,omitempty"`
	ReadOnly    bool             `json:"read_only,omitempty"`
	SideEffect  bool             `json:"side_effect,omitempty"`
	OpenWorld   bool             `json:"open_world,omitempty"`
	Destructive bool             `json:"destructive,omitempty"`
}

// DecisionSpanOptions 补充 RouteDecision 没有直接携带的追踪上下文。
type DecisionSpanOptions struct {
	TraceID        string
	SessionIDHash  string
	CreatedAt      time.Time
	IntentSource   string
	IntentDegraded bool
}

// NewDecisionSpan 从路由决策和候选画像构造本地 replay 快照。
func NewDecisionSpan(decision RouteDecision, candidates []ToolProfile, opts DecisionSpanOptions) DecisionSpan {
	createdAt := opts.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}

	blocked := make([]string, 0, len(decision.BlockedTools))
	blockedReasons := make(map[string]string, len(decision.BlockedTools))
	for _, item := range decision.BlockedTools {
		if item.Name == "" {
			continue
		}
		blocked = append(blocked, item.Name)
		if item.Reason != "" {
			blockedReasons[item.Name] = item.Reason
		}
	}
	if len(blockedReasons) == 0 {
		blockedReasons = nil
	}

	return DecisionSpan{
		TraceID:       opts.TraceID,
		SessionIDHash: opts.SessionIDHash,
		CreatedAt:     createdAt,
		Intent: DecisionSpanIntent{
			Kind:               decision.Intent.Kind,
			Subject:            decision.Intent.Subject,
			AllowedDomainsHint: cloneStrings(decision.Intent.AllowedDomainsHint),
			Confidence:         decision.Intent.Confidence,
			Source:             opts.IntentSource,
			Degraded:           opts.IntentDegraded,
		},
		Candidates:     decisionSpanCandidates(candidates),
		Allowed:        append([]string(nil), decision.AllowedTools...),
		AllowedInputs:  cloneDecisionSpanInputs(decision.AllowedToolInputs),
		VisibleOnly:    append([]string(nil), decision.VisibleOnly...),
		Blocked:        blocked,
		BlockedReasons: blockedReasons,
		Mode:           decision.Mode,
		Reason:         decision.Reason,
	}
}

func decisionSpanCandidates(candidates []ToolProfile) []DecisionSpanCandidate {
	if len(candidates) == 0 {
		return nil
	}
	out := make([]DecisionSpanCandidate, 0, len(candidates))
	for _, profile := range candidates {
		if profile.Name == "" {
			continue
		}
		out = append(out, DecisionSpanCandidate{
			Name:        profile.Name,
			Kind:        profile.Kind,
			Domain:      profile.Domain,
			Source:      profile.Source,
			Invocation:  profile.Invocation,
			Risk:        profile.Risk,
			Trust:       profile.Trust,
			ReadOnly:    profile.ReadOnly,
			SideEffect:  profile.SideEffect,
			OpenWorld:   profile.OpenWorld,
			Destructive: profile.Destructive,
		})
	}
	return out
}

func cloneDecisionSpanInputs(in map[string]map[string]string) map[string]map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]map[string]string, len(in))
	for tool, values := range in {
		if len(values) == 0 {
			continue
		}
		copied := make(map[string]string, len(values))
		for key, value := range values {
			copied[key] = value
		}
		out[tool] = copied
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
