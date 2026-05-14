package router

import (
	"slices"
	"strings"
	"time"
)

// DecisionMode 描述 RouteDecision 对工具暴露面的影响。
type DecisionMode string

const (
	DecisionModeNone     DecisionMode = "none"
	DecisionModeDiscover DecisionMode = "discover"
	DecisionModeAllow    DecisionMode = "allow"
)

// BlockedTool 记录被路由决策排除的候选。
type BlockedTool struct {
	Name   string `json:"name"`
	Reason string `json:"reason,omitempty"`
}

// ReflectionBlock 记录反思链路产出的会话级工具阻断。
type ReflectionBlock struct {
	ToolName    string    `json:"tool_name"`
	Mode        string    `json:"mode,omitempty"`
	Reason      string    `json:"reason,omitempty"`
	FailureKind string    `json:"failure_kind,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
}

// RouteDecisionOptions 描述 BuildRouteDecision 的可选运行时约束。
type RouteDecisionOptions struct {
	ReflectionMode   string
	ReflectionBlocks []ReflectionBlock
	SessionGranted   []Capability
	PlanAllowed      []Capability
	CapabilityDeny   []Capability
}

// RouteDecision 是宿主在单轮中产出的工具可见性决策。
type RouteDecision struct {
	Intent            IntentFrame                  `json:"intent"`
	AllowedTools      []string                     `json:"allowed_tools,omitempty"`
	AllowedToolInputs map[string]map[string]string `json:"allowed_tool_inputs,omitempty"`
	VisibleOnly       []string                     `json:"visible_only,omitempty"`
	BlockedTools      []BlockedTool                `json:"blocked_tools,omitempty"`
	Mode              DecisionMode                 `json:"mode"`
	Reason            string                       `json:"reason,omitempty"`
}

// BuildRouteDecision 根据结构化意图和 typed profile 产出最小可调用集合。
// 它只做宿主侧硬边界判断；文本召回分数只能决定候选排序，不能绕过这里。
func BuildRouteDecision(intent IntentFrame, profiles []ToolProfile) RouteDecision {
	return BuildRouteDecisionWithOptions(intent, profiles, RouteDecisionOptions{})
}

func BuildRouteDecisionWithBlocks(intent IntentFrame, profiles []ToolProfile, mode string, blocks []ReflectionBlock) RouteDecision {
	return BuildRouteDecisionWithOptions(intent, profiles, RouteDecisionOptions{
		ReflectionMode:   mode,
		ReflectionBlocks: blocks,
	})
}

func BuildRouteDecisionWithOptions(intent IntentFrame, profiles []ToolProfile, opts RouteDecisionOptions) RouteDecision {
	decision := RouteDecision{
		Intent:            intent,
		AllowedToolInputs: map[string]map[string]string{},
		VisibleOnly:       []string{"tool_search"},
		Mode:              DecisionModeDiscover,
	}
	if isBlockedIntent(intent.Kind) {
		decision.Mode = DecisionModeNone
		decision.Reason = "intent blocked"
		for _, profile := range profiles {
			if profile.Name == "" {
				continue
			}
			decision.BlockedTools = append(decision.BlockedTools, BlockedTool{Name: profile.Name, Reason: "intent blocked"})
		}
		return decision
	}
	if externalSendRequiresPlatformQuestion(intent) {
		decision.VisibleOnly = []string{"question"}
		decision.Reason = "external_send_multi_platform_requires_question"
		for _, profile := range profiles {
			if profile.Name == "" {
				continue
			}
			if (ToolNamePolicy{}).Normalize(profile.Name) == "question" {
				continue
			}
			decision.BlockedTools = append(decision.BlockedTools, BlockedTool{Name: profile.Name, Reason: "external_send_multi_platform_requires_question"})
		}
		return decision
	}
	for _, profile := range profiles {
		if profile.Name == "" {
			continue
		}
		profile = ToolActionProfile(profile, nil)
		if block, ok := matchingReflectionBlock(profile.Name, opts.ReflectionMode, opts.ReflectionBlocks); ok {
			decision.BlockedTools = append(decision.BlockedTools, BlockedTool{Name: profile.Name, Reason: reflectionBlockReason(block)})
			continue
		}
		if reason := blockReason(intent, profile); reason != "" {
			decision.BlockedTools = append(decision.BlockedTools, BlockedTool{Name: profile.Name, Reason: reason})
			continue
		}
		if gate := CheckCapabilityGate(CapabilityGateInput{
			IntentRequired: RequiredCapabilitiesForIntent(intent),
			ToolGranted:    ToolCapabilitiesFromProfile(profile),
			SessionGranted: opts.SessionGranted,
			PlanAllowed:    opts.PlanAllowed,
			Deny:           opts.CapabilityDeny,
		}); !gate.Allowed {
			decision.BlockedTools = append(decision.BlockedTools, BlockedTool{Name: profile.Name, Reason: gate.Reason})
			continue
		}
		policy := EvaluateToolPolicy(profile, ToolPolicyContext{Intent: intent, ForRoute: true})
		if toolName, toolArgs, ok := isCallable(intent, profile, policy); ok {
			if len(toolArgs) > 0 {
				if existing, ok := decision.AllowedToolInputs[toolName]; ok && !sameToolArgs(existing, toolArgs) {
					decision.BlockedTools = append(decision.BlockedTools, BlockedTool{Name: profile.Name, Reason: "callable input conflict"})
					continue
				}
				decision.AllowedToolInputs[toolName] = toolArgs
			}
			if !slices.Contains(decision.AllowedTools, toolName) {
				decision.AllowedTools = append(decision.AllowedTools, toolName)
			}
			decision.Mode = DecisionModeAllow
			continue
		}
		decision.BlockedTools = append(decision.BlockedTools, BlockedTool{Name: profile.Name, Reason: "not callable for intent"})
	}
	if decision.Mode == DecisionModeAllow {
		decision.Reason = "matched intent and capability profile"
	} else if len(decision.VisibleOnly) > 0 || len(decision.BlockedTools) > 0 {
		decision.Reason = "discovery only"
	} else {
		decision.Mode = DecisionModeNone
		decision.Reason = "no candidates"
	}
	return decision
}

func sameToolArgs(left, right map[string]string) bool {
	if len(left) != len(right) {
		return false
	}
	for key, leftValue := range left {
		if right[key] != leftValue {
			return false
		}
	}
	return true
}

func matchingReflectionBlock(toolName, mode string, blocks []ReflectionBlock) (ReflectionBlock, bool) {
	toolName = strings.TrimSpace(toolName)
	mode = strings.TrimSpace(mode)
	if toolName == "" || len(blocks) == 0 {
		return ReflectionBlock{}, false
	}
	for _, block := range blocks {
		if strings.TrimSpace(block.ToolName) != toolName {
			continue
		}
		blockMode := strings.TrimSpace(block.Mode)
		if blockMode == "" || blockMode == mode {
			return block, true
		}
	}
	return ReflectionBlock{}, false
}

func reflectionBlockReason(block ReflectionBlock) string {
	reason := strings.TrimSpace(block.Reason)
	if reason == "" {
		reason = "reflection block"
	}
	if kind := strings.TrimSpace(block.FailureKind); kind != "" {
		return reason + ": " + kind
	}
	return reason
}

func isBlockedIntent(kind IntentKind) bool {
	return kind == IntentUnknown
}

func blockReason(intent IntentFrame, profile ToolProfile) string {
	if externalSendMixedToolBlockedForIntent(intent, profile) {
		return "side effect not allowed by intent"
	}
	if intent.Kind == IntentCreateSkill && profile.Kind == CapabilityKindSkillWorkflow && profile.Domain == "mcp_server_building" {
		return "domain_mismatch"
	}
	if len(profile.AllowedIntentKinds) > 0 && !slices.Contains(profile.AllowedIntentKinds, intent.Kind) {
		return "intent kind not allowed by profile"
	}
	policy := EvaluateToolPolicy(profile, ToolPolicyContext{Intent: intent, ForRoute: true})
	if policy.Action == ToolPolicyDeny {
		return blockReasonForPolicyDecision(policy)
	}
	if isDiscoveryOnlyProfile(profile) {
		return "discovery only"
	}
	if policy.RequiresSideEffectIntent && !policy.CallableNow && profile.SideEffect {
		return "side effect not allowed by intent"
	}
	return ""
}

func blockReasonForPolicyDecision(policy ToolPolicyDecision) string {
	switch policy.RouteStatus {
	case ToolRouteDiscoveryOnly:
		return "discovery only"
	case ToolRouteBlockedDangerous, ToolRouteBlockedUnknown:
		if policy.Reason == "runtime_exec_not_allowed" {
			return "runtime execution not required by intent"
		}
		return "unknown destructive/open-world tool"
	default:
		if policy.Reason != "" {
			return policy.Reason
		}
		return "not callable for intent"
	}
}

func isCallable(intent IntentFrame, profile ToolProfile, policy ToolPolicyDecision) (string, map[string]string, bool) {
	if isDiscoveryOnlyProfile(profile) || policy.Action == ToolPolicyDeny || !policy.CallableNow {
		return "", nil, false
	}
	switch intent.Kind {
	case IntentCreateSkill:
		if profile.Kind == CapabilityKindSkillWorkflow && profile.Domain == "skill_authoring" {
			if rule, ok := skillDomainRule(profile.Domain); ok && rule.CallableTool != "" {
				return rule.CallableTool, map[string]string{"name": profile.Name}, true
			}
		}
		return "", nil, false
	case IntentManageTool:
		if profile.Kind == CapabilityKindSkillWorkflow && profile.Domain == "mcp_server_building" &&
			slices.Contains(profile.Capabilities, CapabilityMetaToolRegister) {
			if rule, ok := skillDomainRule(profile.Domain); ok && rule.CallableTool != "" {
				return rule.CallableTool, map[string]string{"name": profile.Name}, true
			}
		}
		return "", nil, false
	case IntentRead, IntentExternalRead, IntentAnswer, IntentPlan:
		if profile.ReadOnly && !profile.SideEffect && profile.Risk == RiskReadOnly {
			return profile.Name, nil, true
		}
		if IsMixedReadWriteTool(profile.Name) {
			if allowed := MixedAllowedToolInputsForIntent(intent, profile.Name); len(allowed) > 0 {
				return profile.Name, allowed, true
			}
		}
		return "", nil, false
	case IntentWriteLocal:
		if !intent.AllowsSideEffects || profile.OpenWorld {
			return "", nil, false
		}
		if IsMixedReadWriteTool(profile.Name) {
			if allowed := MixedAllowedToolInputsForIntent(intent, profile.Name); len(allowed) > 0 {
				return profile.Name, allowed, true
			}
		}
		if profile.Risk == RiskLocalWrite && !profile.Destructive && !profile.OpenWorld {
			return profile.Name, nil, true
		}
		return "", nil, false
	case IntentExternalWrite:
		if intent.AllowsSideEffects && !profile.OpenWorld && externalSendCallableForIntent(intent, profile) {
			if allowed := MixedAllowedToolInputsForIntent(intent, profile.Name); len(allowed) > 0 {
				return profile.Name, allowed, true
			}
			if IsMixedReadWriteTool(profile.Name) {
				return "", nil, false
			}
			return profile.Name, nil, true
		}
		return "", nil, false
	default:
		return "", nil, false
	}
}

func externalSendRequiresPlatformQuestion(intent IntentFrame) bool {
	return intent.Kind == IntentExternalWrite && len(normalizedPlatformHints(intent.AllowedDomainsHint)) > 1
}

func externalSendCallableForIntent(intent IntentFrame, profile ToolProfile) bool {
	platforms := normalizedPlatformHints(intent.AllowedDomainsHint)
	if len(platforms) == 0 {
		return slices.Contains(profile.Capabilities, CapabilityExternalSend)
	}
	if len(platforms) > 1 {
		return false
	}
	capability, ok := externalSendCapabilityForPlatform(platforms[0])
	if !ok {
		return slices.Contains(profile.Capabilities, CapabilityExternalSend)
	}
	return slices.Contains(profile.Capabilities, capability)
}

func externalSendMixedToolBlockedForIntent(intent IntentFrame, profile ToolProfile) bool {
	if !IsMixedReadWriteTool(profile.Name) || len(ExternalSendActions(profile.Name)) == 0 {
		return false
	}
	switch intent.Kind {
	case IntentExternalRead:
		return false
	case IntentExternalWrite:
		return !intent.AllowsSideEffects
	default:
		return true
	}
}
