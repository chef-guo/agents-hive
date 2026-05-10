package router

import (
	"encoding/json"
	"sort"
	"strings"
)

// IntentCapabilityRule declares the host-side capability requirements for an intent.
type IntentCapabilityRule struct {
	Required []Capability
}

// SkillDomainRule declares what a trusted local skill domain can do.
type SkillDomainRule struct {
	Capabilities       []Capability
	AllowedIntentKinds []IntentKind
	CallableTool       string
}

// BuiltinToolRule declares host-owned metadata for built-in tools.
type BuiltinToolRule struct {
	Domain       string
	Invocation   InvocationMode
	Risk         RiskLevel
	ReadOnly     bool
	Destructive  bool
	Idempotent   bool
	OpenWorld    bool
	SideEffect   bool
	Capabilities []Capability
}

// ToolActionRiskRule declares action/operation values that require approval even
// when the tool itself has benign read/list operations.
type ToolActionRiskRule struct {
	ToolName string
	Actions  []string
}

type HostToolSet string

const (
	HostToolSetDefaultVisible HostToolSet = "default_visible"
	HostToolSetPlanControl    HostToolSet = "plan_control"
	HostToolSetPlanAllowed    HostToolSet = "plan_allowed"
)

var intentCapabilityRules = map[IntentKind]IntentCapabilityRule{
	IntentCreateSkill:   {Required: []Capability{CapabilityMetaSkillCreate}},
	IntentModifySkill:   {Required: []Capability{CapabilityMetaSkillModify}},
	IntentManageTool:    {Required: []Capability{CapabilityMetaToolRegister}},
	IntentExternalWrite: {Required: []Capability{CapabilityExternalSend}},
}

var skillDomainRules = map[string]SkillDomainRule{
	"skill_authoring": {
		Capabilities:       []Capability{CapabilityMetaSkillCreate, CapabilityMetaSkillModify},
		AllowedIntentKinds: []IntentKind{IntentCreateSkill, IntentModifySkill},
		CallableTool:       "skill",
	},
	"mcp_server_building": {
		Capabilities:       []Capability{CapabilityMetaToolRegister},
		AllowedIntentKinds: []IntentKind{IntentManageTool},
		CallableTool:       "skill",
	},
}

var knownSkillWorkflowDomains = map[string]string{
	"skill-creator": "skill_authoring",
	"mcp-builder":   "mcp_server_building",
}

var builtinToolRules = map[string]BuiltinToolRule{
	"apply_patch":                {Domain: "filesystem", Invocation: InvocationDirectTool, Risk: RiskLocalWrite, SideEffect: true},
	"bash":                       {Domain: "filesystem", Invocation: InvocationDirectTool, Risk: RiskRuntimeExec, SideEffect: true, Capabilities: []Capability{CapabilityRuntimeExec}},
	"batch":                      {Domain: "agent", Invocation: InvocationDirectTool, Risk: RiskReadOnly, ReadOnly: true},
	"browser_interact":           {Domain: "web", Invocation: InvocationDirectTool, Risk: RiskReadOnly, ReadOnly: true},
	"create_handoff_summary":     {Domain: "agent", Invocation: InvocationDirectTool, Risk: RiskReadOnly, ReadOnly: true},
	"create_tool":                {Domain: "tools", Invocation: InvocationDirectTool, Risk: RiskLocalWrite, SideEffect: true, Capabilities: []Capability{CapabilityMetaToolRegister}},
	"edit":                       {Domain: "filesystem", Invocation: InvocationDirectTool, Risk: RiskLocalWrite, SideEffect: true},
	"enter_plan_mode":            {Domain: "planning", Invocation: InvocationDirectTool, Risk: RiskReadOnly, ReadOnly: true},
	"exit_plan_mode":             {Domain: "planning", Invocation: InvocationDirectTool, Risk: RiskReadOnly, ReadOnly: true},
	"feishu_api":                 {Domain: "messaging", Invocation: InvocationDirectTool, Risk: RiskExternalWrite, SideEffect: true, Capabilities: []Capability{CapabilityExternalSend}},
	"finish_plan":                {Domain: "planning", Invocation: InvocationDirectTool, Risk: RiskReadOnly, ReadOnly: true},
	"generate_image":             {Domain: "media", Invocation: InvocationDirectTool, Risk: RiskLocalWrite, SideEffect: true},
	"glob":                       {Domain: "filesystem", Invocation: InvocationDirectTool, Risk: RiskReadOnly, ReadOnly: true},
	"grep":                       {Domain: "filesystem", Invocation: InvocationDirectTool, Risk: RiskReadOnly, ReadOnly: true},
	"ls":                         {Domain: "filesystem", Invocation: InvocationDirectTool, Risk: RiskReadOnly, ReadOnly: true},
	"memory":                     {Domain: "agent", Invocation: InvocationDirectTool, Risk: RiskReadOnly, ReadOnly: true},
	"multi_edit":                 {Domain: "filesystem", Invocation: InvocationDirectTool, Risk: RiskLocalWrite, SideEffect: true},
	"multiedit":                  {Domain: "filesystem", Invocation: InvocationDirectTool, Risk: RiskLocalWrite, SideEffect: true},
	"parallel_dispatch":          {Domain: "agent", Invocation: InvocationDirectTool, Risk: RiskReadOnly, ReadOnly: true},
	"promote_todos_to_taskboard": {Domain: "taskboard", Invocation: InvocationDirectTool, Risk: RiskLocalWrite, SideEffect: true},
	"question":                   {Domain: "agent", Invocation: InvocationDirectTool, Risk: RiskReadOnly, ReadOnly: true},
	"read_file":                  {Domain: "filesystem", Invocation: InvocationDirectTool, Risk: RiskReadOnly, ReadOnly: true},
	"remove_tool":                {Domain: "tools", Invocation: InvocationDirectTool, Risk: RiskLocalWrite, SideEffect: true, Capabilities: []Capability{CapabilityMetaToolRegister}},
	"send_im_message":            {Domain: "messaging", Invocation: InvocationDirectTool, Risk: RiskExternalWrite, SideEffect: true, Capabilities: []Capability{CapabilityExternalSend}},
	"skill":                      {Domain: "skills", Invocation: InvocationDirectTool, Risk: RiskLocalWrite, SideEffect: true},
	"skill_search":               {Domain: "skills", Invocation: InvocationDirectTool, Risk: RiskReadOnly, ReadOnly: true},
	"spawn_agent":                {Domain: "agent", Invocation: InvocationDirectTool, Risk: RiskReadOnly, ReadOnly: true},
	"task":                       {Domain: "agent", Invocation: InvocationDirectTool, Risk: RiskReadOnly, ReadOnly: true},
	"taskboard":                  {Domain: "taskboard", Invocation: InvocationDirectTool, Risk: RiskLocalWrite, SideEffect: true},
	"text_to_speech":             {Domain: "media", Invocation: InvocationDirectTool, Risk: RiskReadOnly, ReadOnly: true},
	"tool_search":                {Domain: "discovery", Invocation: InvocationDiscoveryOnly, Risk: RiskReadOnly, ReadOnly: true},
	"todo_write":                 {Domain: "planning", Invocation: InvocationDirectTool, Risk: RiskLocalWrite, SideEffect: true},
	"web_fetch":                  {Domain: "web", Invocation: InvocationDirectTool, Risk: RiskReadOnly, ReadOnly: true},
	"web_search":                 {Domain: "web", Invocation: InvocationDirectTool, Risk: RiskReadOnly, ReadOnly: true},
	"webfetch":                   {Domain: "web", Invocation: InvocationDirectTool, Risk: RiskReadOnly, ReadOnly: true},
	"websearch":                  {Domain: "web", Invocation: InvocationDirectTool, Risk: RiskReadOnly, ReadOnly: true},
	"wechat_contacts":            {Domain: "messaging", Invocation: InvocationDirectTool, Risk: RiskExternalWrite, SideEffect: true, Capabilities: []Capability{CapabilityExternalSend}},
	"wechat_groups":              {Domain: "messaging", Invocation: InvocationDirectTool, Risk: RiskExternalWrite, SideEffect: true, Capabilities: []Capability{CapabilityExternalSend}},
	"wechat_moments":             {Domain: "messaging", Invocation: InvocationDirectTool, Risk: RiskExternalWrite, SideEffect: true, Capabilities: []Capability{CapabilityExternalSend}},
	"wechat_profile":             {Domain: "messaging", Invocation: InvocationDirectTool, Risk: RiskExternalWrite, SideEffect: true, Capabilities: []Capability{CapabilityExternalSend}},
	"wechat_send_rich_message":   {Domain: "messaging", Invocation: InvocationDirectTool, Risk: RiskExternalWrite, SideEffect: true, Capabilities: []Capability{CapabilityExternalSend}},
	"wechat_status":              {Domain: "messaging", Invocation: InvocationDirectTool, Risk: RiskReadOnly, ReadOnly: true},
	"write_file":                 {Domain: "filesystem", Invocation: InvocationDirectTool, Risk: RiskLocalWrite, SideEffect: true},
}

var shellCommandTools = map[string]bool{
	"bash":        true,
	"shell":       true,
	"exec":        true,
	"run_command": true,
}

var structuredDangerousActions = map[string]map[string]bool{
	"feishu_api": {
		"send_message":          true,
		"send_image":            true,
		"send_file":             true,
		"create_approval":       true,
		"create_bitable_record": true,
		"update_bitable_record": true,
		"create_task":           true,
		"complete_task":         true,
		"write_sheet":           true,
	},
	"memory":                   {"delete": true},
	"taskboard":                {"delete": true},
	"wechat_contacts":          {"add": true, "accept": true, "delete": true},
	"wechat_groups":            {"create": true, "invite": true, "remove": true, "set_name": true, "set_announcement": true, "quit": true},
	"wechat_moments":           {"post": true, "like": true, "comment": true},
	"wechat_profile":           {"set_nickname": true, "set_signature": true, "remark": true},
	"wechat_send_rich_message": {"*": true},
}

var structuredDangerousTools = map[string]bool{
	"create_tool":     true,
	"remove_tool":     true,
	"send_im_message": true,
}

var mixedReadWriteTools = map[string]bool{
	"wechat_contacts": true,
	"wechat_groups":   true,
	"wechat_moments":  true,
	"wechat_profile":  true,
}

var systemDelegationAgents = map[string]bool{
	"codereview":  true,
	"compaction":  true,
	"title-agent": true,
	"summary":     true,
}

var hostToolSets = map[HostToolSet]map[string]bool{
	HostToolSetDefaultVisible: {
		"batch":             true,
		"ls":                true,
		"memory":            true,
		"parallel_dispatch": true,
		"question":          true,
		"skill":             true,
		"task":              true,
		"tool_search":       true,
	},
	HostToolSetPlanControl: {
		"todo_write":                 true,
		"finish_plan":                true,
		"enter_plan_mode":            true,
		"exit_plan_mode":             true,
		"create_handoff_summary":     true,
		"promote_todos_to_taskboard": true,
	},
	HostToolSetPlanAllowed: {
		"exit_plan_mode":             true,
		"glob":                       true,
		"grep":                       true,
		"ls":                         true,
		"memory":                     true,
		"question":                   true,
		"read_file":                  true,
		"skill":                      true,
		"todo_write":                 true,
		"create_handoff_summary":     true,
		"promote_todos_to_taskboard": true,
		"tool_search":                true,
		"webfetch":                   true,
		"websearch":                  true,
		"web_fetch":                  true,
		"web_search":                 true,
	},
}

var hostToolGroups = map[string][]string{
	"agent":     {"spawn_agent", "parallel_dispatch", "task"},
	"discovery": {"tool_search"},
	"fs":        {"read_file", "write_file", "edit", "glob", "grep", "ls", "multiedit", "multi_edit", "apply_patch"},
	"lsp":       {"lsp_definition", "lsp_references", "lsp_hover", "lsp_symbols", "lsp_diagnostics", "lsp_rename", "lsp_code_action", "lsp_format", "lsp_completion"},
	"runtime":   {"bash"},
	"web":       {"websearch", "webfetch", "web_search", "web_fetch", "browser_interact"},
}

var hostToolPolicyProfiles = map[string][]string{
	"coding": {
		"group:fs", "group:runtime", "group:web", "group:lsp", "group:discovery",
		"skill", "memory", "batch", "question",
	},
	"full":      {"*"},
	"messaging": {"send_im_message", "feishu_api", "wechat_send_rich_message", "wechat_contacts", "wechat_groups", "wechat_profile", "wechat_moments", "wechat_status", "skill"},
	"readonly":  {"read_file", "glob", "grep", "ls", "websearch", "webfetch", "web_search", "web_fetch"},
	"master":    {"skill", "memory", "question", "taskboard", "task", "spawn_agent", "parallel_dispatch"},
	"master_direct": {
		"group:fs", "group:runtime", "group:web", "group:lsp", "group:agent", "group:discovery",
		"create_tool", "remove_tool",
		"skill", "memory", "question", "taskboard", "batch",
		"send_im_message", "feishu_api",
		"wechat_send_rich_message", "wechat_contacts", "wechat_groups",
		"wechat_profile", "wechat_moments", "wechat_status",
	},
}

var subagentDeniedHostTools = []string{"spawn_agent", "create_tool", "remove_tool"}
var subagentLeafDeniedHostTools = []string{"parallel_dispatch", "task"}

func intentCapabilityRule(kind IntentKind) (IntentCapabilityRule, bool) {
	rule, ok := intentCapabilityRules[kind]
	return IntentCapabilityRule{Required: cloneCapabilities(rule.Required)}, ok
}

func skillDomainRule(domain string) (SkillDomainRule, bool) {
	rule, ok := skillDomainRules[strings.TrimSpace(domain)]
	return SkillDomainRule{
		Capabilities:       cloneCapabilities(rule.Capabilities),
		AllowedIntentKinds: cloneIntentKinds(rule.AllowedIntentKinds),
		CallableTool:       rule.CallableTool,
	}, ok
}

func knownSkillWorkflowDomain(name string) (string, bool) {
	domain, ok := knownSkillWorkflowDomains[strings.TrimSpace(strings.ToLower(name))]
	return domain, ok
}

func builtinToolRule(nameLower string) (BuiltinToolRule, bool) {
	nameLower = strings.TrimSpace(strings.ToLower(nameLower))
	if shellCommandTools[nameLower] {
		return BuiltinToolRule{Domain: "filesystem", Invocation: InvocationDirectTool, Risk: RiskRuntimeExec, SideEffect: true, Capabilities: []Capability{CapabilityRuntimeExec}}, true
	}
	if strings.HasPrefix(nameLower, "lsp_") {
		return BuiltinToolRule{Domain: "lsp", Invocation: InvocationDirectTool, Risk: RiskReadOnly, ReadOnly: true}, true
	}
	rule, ok := builtinToolRules[nameLower]
	return BuiltinToolRule{
		Domain:       rule.Domain,
		Invocation:   rule.Invocation,
		Risk:         rule.Risk,
		ReadOnly:     rule.ReadOnly,
		Destructive:  rule.Destructive,
		Idempotent:   rule.Idempotent,
		OpenWorld:    rule.OpenWorld,
		SideEffect:   rule.SideEffect,
		Capabilities: cloneCapabilities(rule.Capabilities),
	}, ok
}

// BuiltinToolProfile returns the canonical trusted profile for a host-owned tool.
func BuiltinToolProfile(name string) (ToolProfile, bool) {
	normalized := ToolNamePolicy{}.Normalize(name)
	rule, ok := builtinToolRule(normalized)
	if !ok {
		return ToolProfile{}, false
	}
	return ToolProfile{
		Name:         normalized,
		Kind:         CapabilityKindBuiltinTool,
		Domain:       rule.Domain,
		Source:       CapabilitySourceBuiltin,
		Invocation:   rule.Invocation,
		Risk:         rule.Risk,
		Trust:        TrustBuiltIn,
		ReadOnly:     rule.ReadOnly,
		Destructive:  rule.Destructive,
		Idempotent:   rule.Idempotent,
		OpenWorld:    rule.OpenWorld,
		SideEffect:   rule.SideEffect,
		Capabilities: cloneCapabilities(rule.Capabilities),
	}, true
}

// IsKnownHostTool reports whether the host owns this tool name family.
func IsKnownHostTool(name string) bool {
	_, ok := builtinToolRule(name)
	return ok
}

// IsShellCommandTool reports whether a tool input is a shell command payload.
func IsShellCommandTool(name string) bool {
	return shellCommandTools[strings.TrimSpace(strings.ToLower(name))]
}

// IsMixedReadWriteTool reports whether a tool exposes both read operations and
// write operations behind an action/operation field.
func IsMixedReadWriteTool(name string) bool {
	return mixedReadWriteTools[strings.TrimSpace(strings.ToLower(name))]
}

// StructuredDangerousOperation reports whether a structured tool input requires
// HITL in minimal mode because the specific action/operation has side effects.
func StructuredDangerousOperation(toolName string, input json.RawMessage) bool {
	toolName = strings.TrimSpace(strings.ToLower(toolName))
	if structuredDangerousTools[toolName] {
		return true
	}
	action := structuredAction(input)
	if action == "" {
		return false
	}
	actions := structuredDangerousActions[toolName]
	return actions["*"] || actions[action]
}

// ToolActionProfile specializes a mixed read/write tool profile using a
// structured action/operation value when available.
func ToolActionProfile(profile ToolProfile, input json.RawMessage) ToolProfile {
	if profile.Name == "" || !IsMixedReadWriteTool(profile.Name) {
		return profile
	}
	if StructuredDangerousOperation(profile.Name, input) {
		return profile
	}
	action := structuredAction(input)
	if action == "" {
		return profile
	}
	profile.Risk = RiskReadOnly
	profile.ReadOnly = true
	profile.SideEffect = false
	return profile
}

// ToolActionRiskRules returns the canonical action-level rules for permission defaults.
func ToolActionRiskRules() []ToolActionRiskRule {
	out := make([]ToolActionRiskRule, 0, len(structuredDangerousActions))
	for tool, actions := range structuredDangerousActions {
		if actions["*"] {
			continue
		}
		names := make([]string, 0, len(actions))
		for action := range actions {
			names = append(names, action)
		}
		sort.Strings(names)
		out = append(out, ToolActionRiskRule{ToolName: tool, Actions: names})
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].ToolName < out[j].ToolName
	})
	return out
}

// IsSystemDelegationAgent reports whether an agent id is reserved for internal system jobs.
func IsSystemDelegationAgent(agentID string) bool {
	return systemDelegationAgents[strings.TrimSpace(strings.ToLower(agentID))]
}

// IsHostToolInSet reports whether a tool belongs to a named host policy set.
func IsHostToolInSet(set HostToolSet, name string) bool {
	tools, ok := hostToolSets[set]
	if !ok {
		return false
	}
	return tools[strings.TrimSpace(name)]
}

// HostToolSetMembers returns a copy of a named host policy set.
func HostToolSetMembers(set HostToolSet) []string {
	tools, ok := hostToolSets[set]
	if !ok || len(tools) == 0 {
		return nil
	}
	out := make([]string, 0, len(tools))
	for name := range tools {
		out = append(out, name)
	}
	return out
}

// HostToolPolicyGroups returns canonical tool groups used to build default config.
func HostToolPolicyGroups() map[string][]string {
	return cloneStringSliceMap(hostToolGroups)
}

// HostToolPolicyProfiles returns canonical default tool profiles.
func HostToolPolicyProfiles() map[string][]string {
	return cloneStringSliceMap(hostToolPolicyProfiles)
}

func SubagentDeniedHostTools() []string {
	return append([]string(nil), subagentDeniedHostTools...)
}

func SubagentLeafDeniedHostTools() []string {
	return append([]string(nil), subagentLeafDeniedHostTools...)
}

func isDiscoveryOnlyProfile(profile ToolProfile) bool {
	return profile.Invocation == InvocationDiscoveryOnly || strings.TrimSpace(profile.Name) == "tool_search"
}

func structuredAction(input json.RawMessage) string {
	if len(input) == 0 {
		return ""
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(input, &payload); err != nil {
		return ""
	}
	for _, name := range []string{"action", "operation"} {
		raw, ok := payload[name]
		if !ok {
			continue
		}
		var value string
		if err := json.Unmarshal(raw, &value); err == nil {
			return strings.ToLower(strings.TrimSpace(value))
		}
	}
	return ""
}

func cloneCapabilities(in []Capability) []Capability {
	if len(in) == 0 {
		return nil
	}
	return append([]Capability(nil), in...)
}

func cloneIntentKinds(in []IntentKind) []IntentKind {
	if len(in) == 0 {
		return nil
	}
	return append([]IntentKind(nil), in...)
}

func cloneStringSliceMap(in map[string][]string) map[string][]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string][]string, len(in))
	for key, values := range in {
		out[key] = append([]string(nil), values...)
	}
	return out
}
