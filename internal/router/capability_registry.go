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

// ToolActionRiskRule 声明单个 action/operation 中需要审批的危险动作。
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
	"batch":                      {Domain: "agent", Invocation: InvocationDirectTool, Risk: RiskLocalWrite, SideEffect: true},
	"browser_interact":           {Domain: "web", Invocation: InvocationDirectTool, Risk: RiskLocalWrite, SideEffect: true},
	"create_handoff_summary":     {Domain: "agent", Invocation: InvocationDirectTool, Risk: RiskReadOnly, ReadOnly: true},
	"create_tool":                {Domain: "tools", Invocation: InvocationDirectTool, Risk: RiskLocalWrite, SideEffect: true, Capabilities: []Capability{CapabilityMetaToolRegister}},
	"edit":                       {Domain: "filesystem", Invocation: InvocationDirectTool, Risk: RiskLocalWrite, SideEffect: true},
	"enter_plan_mode":            {Domain: "planning", Invocation: InvocationDirectTool, Risk: RiskReadOnly, ReadOnly: true},
	"exit_plan_mode":             {Domain: "planning", Invocation: InvocationDirectTool, Risk: RiskReadOnly, ReadOnly: true},
	"feishu_api":                 {Domain: "messaging", Invocation: InvocationDirectTool, Risk: RiskExternalWrite, SideEffect: true, Capabilities: []Capability{CapabilityExternalSend, CapabilityExternalSendFeishu}},
	"finish_plan":                {Domain: "planning", Invocation: InvocationDirectTool, Risk: RiskReadOnly, ReadOnly: true},
	"generate_image":             {Domain: "media", Invocation: InvocationDirectTool, Risk: RiskLocalWrite, SideEffect: true},
	"generate_video":             {Domain: "media", Invocation: InvocationDirectTool, Risk: RiskLocalWrite, SideEffect: true},
	"glob":                       {Domain: "filesystem", Invocation: InvocationDirectTool, Risk: RiskReadOnly, ReadOnly: true},
	"grep":                       {Domain: "filesystem", Invocation: InvocationDirectTool, Risk: RiskReadOnly, ReadOnly: true},
	"im_api":                     {Domain: "messaging", Invocation: InvocationDirectTool, Risk: RiskExternalWrite, SideEffect: true, Capabilities: allExternalSendCapabilities()},
	"ls":                         {Domain: "filesystem", Invocation: InvocationDirectTool, Risk: RiskReadOnly, ReadOnly: true},
	"memory":                     {Domain: "agent", Invocation: InvocationDirectTool, Risk: RiskLocalWrite, SideEffect: true},
	"multi_edit":                 {Domain: "filesystem", Invocation: InvocationDirectTool, Risk: RiskLocalWrite, SideEffect: true},
	"multiedit":                  {Domain: "filesystem", Invocation: InvocationDirectTool, Risk: RiskLocalWrite, SideEffect: true},
	"parallel_dispatch":          {Domain: "agent", Invocation: InvocationDirectTool, Risk: RiskLocalWrite, SideEffect: true},
	"promote_todos_to_taskboard": {Domain: "taskboard", Invocation: InvocationDirectTool, Risk: RiskLocalWrite, SideEffect: true},
	"question":                   {Domain: "agent", Invocation: InvocationDirectTool, Risk: RiskReadOnly, ReadOnly: true},
	"read_file":                  {Domain: "filesystem", Invocation: InvocationDirectTool, Risk: RiskReadOnly, ReadOnly: true},
	"remove_tool":                {Domain: "tools", Invocation: InvocationDirectTool, Risk: RiskLocalWrite, SideEffect: true, Capabilities: []Capability{CapabilityMetaToolRegister}},
	"send_im_message":            {Domain: "messaging", Invocation: InvocationDirectTool, Risk: RiskExternalWrite, SideEffect: true, Capabilities: allExternalSendCapabilities()},
	"skill":                      {Domain: "skills", Invocation: InvocationDirectTool, Risk: RiskLocalWrite, SideEffect: true},
	"skill_install":              {Domain: "skills", Invocation: InvocationDirectTool, Risk: RiskLocalWrite, SideEffect: true, Capabilities: []Capability{CapabilityMetaSkillCreate}},
	"skill_search":               {Domain: "skills", Invocation: InvocationDirectTool, Risk: RiskReadOnly, ReadOnly: true},
	"spawn_agent":                {Domain: "agent", Invocation: InvocationDirectTool, Risk: RiskLocalWrite, SideEffect: true},
	"task":                       {Domain: "agent", Invocation: InvocationDirectTool, Risk: RiskLocalWrite, SideEffect: true},
	"taskboard":                  {Domain: "taskboard", Invocation: InvocationDirectTool, Risk: RiskLocalWrite, SideEffect: true},
	"text_to_speech":             {Domain: "media", Invocation: InvocationDirectTool, Risk: RiskReadOnly, ReadOnly: true},
	"tool_search":                {Domain: "discovery", Invocation: InvocationDiscoveryOnly, Risk: RiskReadOnly, ReadOnly: true},
	"todo_write":                 {Domain: "planning", Invocation: InvocationDirectTool, Risk: RiskLocalWrite, SideEffect: true},
	"web_fetch":                  {Domain: "web", Invocation: InvocationDirectTool, Risk: RiskReadOnly, ReadOnly: true},
	"web_search":                 {Domain: "web", Invocation: InvocationDirectTool, Risk: RiskReadOnly, ReadOnly: true},
	"webfetch":                   {Domain: "web", Invocation: InvocationDirectTool, Risk: RiskReadOnly, ReadOnly: true},
	"websearch":                  {Domain: "web", Invocation: InvocationDirectTool, Risk: RiskReadOnly, ReadOnly: true},
	"write_file":                 {Domain: "filesystem", Invocation: InvocationDirectTool, Risk: RiskLocalWrite, SideEffect: true},
}

func allExternalSendCapabilities() []Capability {
	return []Capability{
		CapabilityExternalSend,
		CapabilityExternalSendFeishu,
		CapabilityExternalSendWechatBot,
		CapabilityExternalSendWeCom,
		CapabilityExternalSendDingTalk,
	}
}

var shellCommandTools = map[string]bool{
	"bash":        true,
	"shell":       true,
	"exec":        true,
	"run_command": true,
}

var structuredDangerousActions = map[string]map[string]bool{
	"feishu_api": {
		"create_approval":       true,
		"create_bitable_record": true,
		"update_bitable_record": true,
		"create_task":           true,
		"complete_task":         true,
		"write_sheet":           true,
	},
	"memory":    {"delete": true},
	"taskboard": {"delete": true},
}

var structuredDangerousTools = map[string]bool{
	"create_tool": true,
	"remove_tool": true,
}

type mixedActionRule struct {
	Field               string
	ReadOnlyActions     []string
	LocalWriteActions   []string
	ExternalSendActions []string
}

var mixedActionRules = map[string]mixedActionRule{
	"feishu_api": {
		Field: "action",
		ReadOnlyActions: []string{
			"search_docs", "get_doc_content", "wiki_get_node", "wiki_list_nodes",
			"search_contacts", "get_user_info",
			"get_calendar_events",
			"get_chat_info", "get_chat_admins", "list_chat_members",
			"list_approvals", "get_approval",
			"list_bitable_tables", "list_bitable_records",
			"list_tasks",
			"read_sheet",
			"download_message_resource",
		},
		ExternalSendActions: []string{
			"search_contacts", "get_user_info",
			"get_chat_info", "get_chat_admins", "list_chat_members",
			"upload_image", "upload_file",
			"send_message", "send_image", "send_file",
		},
	},
	"memory": {
		Field:             "operation",
		ReadOnlyActions:   []string{"search", "list"},
		LocalWriteActions: []string{"save", "update"},
	},
	"taskboard": {
		Field:             "operation",
		ReadOnlyActions:   []string{"get", "list"},
		LocalWriteActions: []string{"create", "update"},
	},
	"browser_interact": {
		Field:             "commands[].action",
		ReadOnlyActions:   []string{"navigate", "snapshot", "wait", "screenshot"},
		LocalWriteActions: []string{"click", "fill", "eval", "close"},
	},
}

var systemDelegationAgents = map[string]bool{
	"codereview":  true,
	"compaction":  true,
	"title-agent": true,
	"summary":     true,
}

var hostToolSets = map[HostToolSet]map[string]bool{
	HostToolSetDefaultVisible: {
		"ls":          true,
		"memory":      true,
		"question":    true,
		"skill":       true,
		"tool_search": true,
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
	"messaging": {"send_im_message", "feishu_api", "skill"},
	"readonly":  {"read_file", "glob", "grep", "ls", "websearch", "webfetch", "web_search", "web_fetch"},
	"master":    {"skill", "memory", "question", "taskboard", "task", "spawn_agent", "parallel_dispatch"},
	"master_direct": {
		"group:fs", "group:runtime", "group:web", "group:lsp", "group:agent", "group:discovery",
		"create_tool", "remove_tool",
		"skill", "memory", "question", "taskboard", "batch",
		"send_im_message", "feishu_api",
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
	_, ok := mixedActionRuleForTool(name)
	return ok
}

// MixedActionField 返回混合读写工具的动作字段路径。
func MixedActionField(name string) string {
	rule, ok := mixedActionRuleForTool(name)
	if !ok {
		return ""
	}
	return rule.Field
}

// MixedReadOnlyActions 返回混合工具在只读意图下可用的动作。
func MixedReadOnlyActions(name string) []string {
	rule, ok := mixedActionRuleForTool(name)
	if !ok {
		return nil
	}
	return cloneStrings(rule.ReadOnlyActions)
}

// MixedLocalWriteActions 返回混合工具的非危险本地写动作。
func MixedLocalWriteActions(name string) []string {
	rule, ok := mixedActionRuleForTool(name)
	if !ok {
		return nil
	}
	return cloneStrings(rule.LocalWriteActions)
}

// ExternalSendActions 返回外部发送意图下可用的检索和发送动作。
func ExternalSendActions(name string) []string {
	rule, ok := mixedActionRuleForTool(name)
	if !ok {
		return nil
	}
	return cloneStrings(rule.ExternalSendActions)
}

// MixedAllowedToolInputsForIntent 返回混合工具在当前意图下的输入约束。
func MixedAllowedToolInputsForIntent(intent IntentFrame, toolName string) map[string]string {
	rule, ok := mixedActionRuleForTool(toolName)
	if !ok || strings.TrimSpace(rule.Field) == "" {
		return nil
	}
	var actions []string
	switch intent.Kind {
	case IntentRead, IntentAnswer, IntentPlan:
		if len(rule.ExternalSendActions) > 0 {
			return nil
		}
		actions = rule.ReadOnlyActions
	case IntentExternalRead:
		actions = rule.ReadOnlyActions
	case IntentWriteLocal:
		if !intent.AllowsSideEffects {
			actions = rule.ReadOnlyActions
			break
		}
		actions = append(cloneStrings(rule.ReadOnlyActions), rule.LocalWriteActions...)
	case IntentExternalWrite:
		if !intent.AllowsSideEffects {
			return nil
		}
		actions = rule.ExternalSendActions
	}
	if len(actions) == 0 {
		return nil
	}
	return map[string]string{rule.Field: strings.Join(uniqueSortedStrings(actions), "|")}
}

// MixedReadOnlyToolInputs returns the safe read/list constraints for default
// visible mixed tools before a side-effect route decision grants wider access.
func MixedReadOnlyToolInputs(toolName string) map[string]string {
	return MixedAllowedToolInputsForIntent(IntentFrame{Kind: IntentRead}, toolName)
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

// StructuredDangerousAction reports whether a concrete action/operation value
// is dangerous for a mixed structured tool.
func StructuredDangerousAction(toolName, action string) bool {
	toolName = strings.TrimSpace(strings.ToLower(toolName))
	action = strings.TrimSpace(strings.ToLower(action))
	if toolName == "" || action == "" {
		return false
	}
	actions := structuredDangerousActions[toolName]
	return actions["*"] || actions[action]
}

// StructuredDangerousActions returns the dangerous action names for a tool.
func StructuredDangerousActions(toolName string) []string {
	toolName = strings.TrimSpace(strings.ToLower(toolName))
	actions := structuredDangerousActions[toolName]
	if len(actions) == 0 || actions["*"] {
		return nil
	}
	names := make([]string, 0, len(actions))
	for action := range actions {
		names = append(names, action)
	}
	sort.Strings(names)
	return names
}

// ProfileRequiresApproval reports whether the unified policy needs human
// attention for this profile before action-level runtime narrowing.
func ProfileRequiresApproval(profile ToolProfile) bool {
	if structuredDangerousTools[strings.TrimSpace(strings.ToLower(profile.Name))] {
		return true
	}
	decision := EvaluateToolPolicy(profile, ToolPolicyContext{ForRoute: true})
	return decision.RequiresApproval || decision.Action == ToolPolicyDeny
}

// ToolActionProfile specializes a mixed read/write tool profile using a
// structured action/operation value when available.
// Invariant: dangerous mixed operations return the original profile so
// EvaluateToolPolicy can ask through the mixed policy instead of outer deny.
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
	if containsActionString(MixedReadOnlyActions(profile.Name), action) {
		profile.Risk = RiskReadOnly
		profile.ReadOnly = true
		profile.SideEffect = false
		return profile
	}
	if containsActionString(MixedLocalWriteActions(profile.Name), action) {
		profile.Risk = RiskLocalWrite
		profile.ReadOnly = false
		profile.SideEffect = true
		return profile
	}
	if containsActionString(ExternalSendActions(profile.Name), action) {
		profile.Risk = RiskExternalWrite
		profile.ReadOnly = false
		profile.SideEffect = true
		return profile
	}
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

func mixedActionRuleForTool(name string) (mixedActionRule, bool) {
	rule, ok := mixedActionRules[strings.TrimSpace(strings.ToLower(name))]
	return mixedActionRule{
		Field:               rule.Field,
		ReadOnlyActions:     cloneStrings(rule.ReadOnlyActions),
		LocalWriteActions:   cloneStrings(rule.LocalWriteActions),
		ExternalSendActions: cloneStrings(rule.ExternalSendActions),
	}, ok
}

func containsActionString(values []string, want string) bool {
	want = strings.TrimSpace(strings.ToLower(want))
	if want == "" {
		return false
	}
	for _, value := range values {
		if strings.TrimSpace(strings.ToLower(value)) == want {
			return true
		}
	}
	return false
}

func uniqueSortedStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	sort.Strings(out)
	return out
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

func cloneStrings(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	return append([]string(nil), in...)
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
