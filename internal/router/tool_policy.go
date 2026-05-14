package router

import (
	"encoding/json"
	"strings"
)

type ToolPolicyAction string

const (
	ToolPolicyAllow ToolPolicyAction = "allow"
	ToolPolicyAsk   ToolPolicyAction = "ask"
	ToolPolicyDeny  ToolPolicyAction = "deny"
)

type ToolRouteStatus string

const (
	ToolRouteDiscoveryOnly                 ToolRouteStatus = "discovery_only"
	ToolRouteCallableReadOnly              ToolRouteStatus = "callable_read_only"
	ToolRouteCallableWithActionConstraints ToolRouteStatus = "callable_with_action_constraints"
	ToolRouteRequiresSideEffectIntent      ToolRouteStatus = "requires_side_effect_intent"
	ToolRouteRequiresMatchingIntent        ToolRouteStatus = "requires_matching_intent"
	ToolRouteBlockedDangerous              ToolRouteStatus = "blocked_dangerous"
	ToolRouteBlockedUnknown                ToolRouteStatus = "blocked_unknown"
)

type ToolPolicyContext struct {
	Intent    IntentFrame
	Input     json.RawMessage
	ForRoute  bool
	ForAction bool
}

type ToolPolicyDecision struct {
	Action                   ToolPolicyAction `json:"action"`
	RouteStatus              ToolRouteStatus  `json:"route_status"`
	CallableNow              bool             `json:"callable_now"`
	RequiresApproval         bool             `json:"requires_approval"`
	RequiresSideEffectIntent bool             `json:"requires_side_effect_intent"`
	Reason                   string           `json:"reason,omitempty"`
	Source                   string           `json:"source,omitempty"`
	ReadOnly                 bool             `json:"read_only,omitempty"`
	SideEffect               bool             `json:"side_effect,omitempty"`
}

// EvaluateToolPolicy 是工具路由、展示和运行时守卫共享的唯一策略入口。
func EvaluateToolPolicy(profile ToolProfile, ctx ToolPolicyContext) ToolPolicyDecision {
	profile = ToolActionProfile(profile, ctx.Input)
	if profile.Risk == RiskUnknown && profile.Destructive {
		return policyDecision(ToolPolicyDeny, ToolRouteBlockedDangerous, false, true, "dangerous_or_open_world_tool", profile)
	}
	if isDiscoveryOnlyProfile(profile) {
		return policyDecision(ToolPolicyDeny, ToolRouteDiscoveryOnly, false, false, "discovery_only", profile)
	}
	if profile.Risk == RiskUnknown {
		return policyDecision(ToolPolicyDeny, ToolRouteBlockedUnknown, false, true, "unknown_tool", profile)
	}
	if profile.OpenWorld || profile.Destructive || profile.Risk == RiskDestructive {
		return policyDecision(ToolPolicyDeny, ToolRouteBlockedDangerous, false, true, "dangerous_or_open_world_tool", profile)
	}
	if profile.Risk == RiskRuntimeExec {
		if ctx.Intent.Kind == IntentManageTool && ctx.Intent.AllowsSideEffects {
			return policyDecision(ToolPolicyAsk, ToolRouteRequiresMatchingIntent, true, true, "runtime_exec_under_manage_intent", profile)
		}
		return policyDecision(ToolPolicyDeny, ToolRouteBlockedDangerous, false, true, "runtime_exec_not_allowed", profile)
	}
	if IsMixedReadWriteTool(profile.Name) {
		return evaluateMixedToolPolicy(profile, ctx)
	}
	if profile.Invocation == InvocationSkillTool {
		callable := skillWorkflowCallableForIntent(profile, ctx.Intent)
		return policyDecision(ToolPolicyAsk, ToolRouteRequiresMatchingIntent, callable, true, "requires_matching_intent", profile)
	}
	if profile.ReadOnly && !ProfileHasSideEffect(profile) && profile.Risk == RiskReadOnly {
		return policyDecision(ToolPolicyAllow, ToolRouteCallableReadOnly, true, false, "read_only", profile)
	}
	if ProfileHasSideEffect(profile) {
		callable := ctx.Intent.AllowsSideEffects && sideEffectIntentAllowsProfile(ctx.Intent, profile)
		decision := policyDecision(ToolPolicyAsk, ToolRouteRequiresSideEffectIntent, callable, true, "side_effect_requires_intent", profile)
		decision.RequiresSideEffectIntent = !callable
		return decision
	}
	return policyDecision(ToolPolicyDeny, ToolRouteBlockedUnknown, false, true, "unclassified_tool_policy", profile)
}

func evaluateMixedToolPolicy(profile ToolProfile, ctx ToolPolicyContext) ToolPolicyDecision {
	if StructuredDangerousOperation(profile.Name, ctx.Input) {
		callable := ctx.Intent.AllowsSideEffects && sideEffectIntentAllowsProfile(ctx.Intent, profile)
		decision := policyDecision(ToolPolicyAsk, ToolRouteRequiresSideEffectIntent, callable, true, "structured_dangerous_operation", profile)
		decision.RequiresSideEffectIntent = !callable
		return decision
	}

	action := structuredAction(ctx.Input)
	if action != "" {
		switch {
		case containsActionString(MixedReadOnlyActions(profile.Name), action):
			return policyDecision(ToolPolicyAllow, ToolRouteCallableWithActionConstraints, true, false, "mixed_read_action", profile)
		case containsActionString(ExternalSendActions(profile.Name), action):
			callable := ctx.Intent.Kind == IntentExternalWrite && ctx.Intent.AllowsSideEffects
			decision := policyDecision(ToolPolicyAsk, ToolRouteRequiresSideEffectIntent, callable, true, "external_send_action", profile)
			decision.RequiresSideEffectIntent = !callable
			return decision
		case containsActionString(MixedLocalWriteActions(profile.Name), action):
			callable := ctx.Intent.AllowsSideEffects && sideEffectIntentAllowsProfile(ctx.Intent, ToolProfile{Risk: RiskLocalWrite})
			decision := policyDecision(ToolPolicyAsk, ToolRouteRequiresSideEffectIntent, callable, true, "local_write_action", profile)
			decision.RequiresSideEffectIntent = !callable
			return decision
		default:
			return policyDecision(ToolPolicyDeny, ToolRouteBlockedUnknown, false, true, "mixed_action_unknown:"+action, profile)
		}
	}

	if allowed := MixedAllowedToolInputsForIntent(ctx.Intent, profile.Name); len(allowed) > 0 {
		return policyDecision(ToolPolicyAllow, ToolRouteCallableWithActionConstraints, true, false, "mixed_action_constraints", profile)
	}
	if len(MixedReadOnlyActions(profile.Name)) > 0 {
		return policyDecision(ToolPolicyAllow, ToolRouteCallableWithActionConstraints, true, false, "mixed_read_actions_available", profile)
	}
	decision := policyDecision(ToolPolicyAsk, ToolRouteRequiresSideEffectIntent, ctx.Intent.AllowsSideEffects, true, "mixed_side_effect_requires_intent", profile)
	decision.RequiresSideEffectIntent = !decision.CallableNow
	return decision
}

func skillWorkflowCallableForIntent(profile ToolProfile, intent IntentFrame) bool {
	if len(profile.AllowedIntentKinds) > 0 && !containsIntentKind(profile.AllowedIntentKinds, intent.Kind) {
		return false
	}
	rule, ok := skillDomainRule(profile.Domain)
	if !ok || strings.TrimSpace(rule.CallableTool) == "" {
		return false
	}
	return containsIntentKind(rule.AllowedIntentKinds, intent.Kind)
}

func sideEffectIntentAllowsProfile(intent IntentFrame, profile ToolProfile) bool {
	switch profile.Risk {
	case RiskLocalWrite:
		return intent.Kind == IntentWriteLocal || intent.Kind == IntentExternalWrite
	case RiskExternalWrite:
		return intent.Kind == IntentExternalWrite
	default:
		return intent.AllowsSideEffects
	}
}

func policyDecision(action ToolPolicyAction, route ToolRouteStatus, callableNow, requiresApproval bool, reason string, profile ToolProfile) ToolPolicyDecision {
	return ToolPolicyDecision{
		Action:                   action,
		RouteStatus:              route,
		CallableNow:              callableNow,
		RequiresApproval:         requiresApproval,
		RequiresSideEffectIntent: false,
		Reason:                   reason,
		Source:                   "tool_policy",
		ReadOnly:                 profile.ReadOnly && !ProfileHasSideEffect(profile),
		SideEffect:               ProfileHasSideEffect(profile),
	}
}

func containsIntentKind(values []IntentKind, want IntentKind) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
