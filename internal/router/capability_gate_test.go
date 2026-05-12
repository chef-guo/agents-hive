package router

import (
	"strings"
	"testing"

	"github.com/chef-guo/agents-hive/internal/mcphost"
)

func TestCapabilityGateAllowsWhenIntentToolSessionAndPlanIntersect(t *testing.T) {
	got := CheckCapabilityGate(CapabilityGateInput{
		IntentRequired: []Capability{CapabilityExternalSend},
		ToolGranted:    []Capability{CapabilityExternalSend, CapabilityRuntimeExec},
		SessionGranted: []Capability{CapabilityExternalSend},
		PlanAllowed:    []Capability{CapabilityExternalSend},
	})
	if !got.Allowed {
		t.Fatalf("Allowed = false, want true: %+v", got)
	}
}

func TestCapabilityGateFailsClosedOnMissingOrDeniedCapability(t *testing.T) {
	missing := CheckCapabilityGate(CapabilityGateInput{
		IntentRequired: []Capability{CapabilityMetaToolRegister},
		ToolGranted:    []Capability{CapabilityMetaSkillCreate},
	})
	if missing.Allowed || missing.Reason != "capability missing" {
		t.Fatalf("missing result = %+v, want capability missing", missing)
	}

	denied := CheckCapabilityGate(CapabilityGateInput{
		IntentRequired: []Capability{CapabilityExternalSend},
		ToolGranted:    []Capability{CapabilityExternalSend},
		Deny:           []Capability{CapabilityExternalSend},
	})
	if denied.Allowed || denied.Reason != "capability denied" {
		t.Fatalf("denied result = %+v, want capability denied", denied)
	}
}

func TestRequiredCapabilitiesForIntent(t *testing.T) {
	tests := []struct {
		intent IntentKind
		want   Capability
	}{
		{IntentCreateSkill, CapabilityMetaSkillCreate},
		{IntentModifySkill, CapabilityMetaSkillModify},
		{IntentManageTool, CapabilityMetaToolRegister},
		{IntentExternalWrite, CapabilityExternalSend},
	}
	for _, tt := range tests {
		got := RequiredCapabilitiesForIntent(IntentFrame{Kind: tt.intent})
		if len(got) != 1 || got[0] != tt.want {
			t.Fatalf("RequiredCapabilitiesForIntent(%q) = %+v, want %q", tt.intent, got, tt.want)
		}
	}
	if got := RequiredCapabilitiesForIntent(IntentFrame{Kind: IntentRead}); len(got) != 0 {
		t.Fatalf("read intent should not require write capability, got %+v", got)
	}
}

func TestCapabilityGateExternalSendPlatformHints(t *testing.T) {
	tests := []struct {
		name  string
		hints []string
		want  Capability
	}{
		{name: "feishu", hints: []string{"feishu"}, want: CapabilityExternalSendFeishu},
		{name: "wechatbot", hints: []string{"wechatbot"}, want: CapabilityExternalSendWechatBot},
		{name: "wecom", hints: []string{"wecom"}, want: CapabilityExternalSendWeCom},
		{name: "dingtalk", hints: []string{"dingtalk"}, want: CapabilityExternalSendDingTalk},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			required := RequiredCapabilitiesForIntent(IntentFrame{Kind: IntentExternalWrite, AllowedDomainsHint: tt.hints})
			if len(required) != 1 || required[0] != tt.want {
				t.Fatalf("RequiredCapabilitiesForIntent(%q) = %+v, want %q", tt.name, required, tt.want)
			}
			umbrellaOnly := CheckCapabilityGate(CapabilityGateInput{
				IntentRequired: required,
				ToolGranted:    []Capability{CapabilityExternalSend},
			})
			if umbrellaOnly.Allowed {
				t.Fatalf("umbrella external-send must not satisfy %q platform route", tt.name)
			}
		})
	}
}

func TestCapabilityGateMultiPlatformExternalSendIsUnsatisfied(t *testing.T) {
	required := RequiredCapabilitiesForIntent(IntentFrame{
		Kind:               IntentExternalWrite,
		AllowedDomainsHint: []string{"feishu", "wechatbot"},
	})
	if len(required) != 1 || required[0] == CapabilityExternalSend {
		t.Fatalf("multi-platform requirements = %+v, want explicit unsatisfied capability", required)
	}
	got := CheckCapabilityGate(CapabilityGateInput{
		IntentRequired: required,
		ToolGranted:    allExternalSendCapabilities(),
	})
	if got.Allowed {
		t.Fatalf("multi-platform external send must stay unauthorized: %+v", got)
	}
}

func TestCapabilityRegistryReturnsCopies(t *testing.T) {
	required := RequiredCapabilitiesForIntent(IntentFrame{Kind: IntentCreateSkill})
	required[0] = CapabilityExternalSend
	got := RequiredCapabilitiesForIntent(IntentFrame{Kind: IntentCreateSkill})
	if len(got) != 1 || got[0] != CapabilityMetaSkillCreate {
		t.Fatalf("intent rule leaked mutable slice: %+v", got)
	}

	caps := inferSkillWorkflowCapabilities("skill_authoring")
	caps[0] = CapabilityRuntimeExec
	gotCaps := inferSkillWorkflowCapabilities("skill_authoring")
	if len(gotCaps) != 2 || gotCaps[0] != CapabilityMetaSkillCreate {
		t.Fatalf("skill domain rule leaked mutable slice: %+v", gotCaps)
	}
}

func TestBuiltinCapabilityRegistryCoversCommonHostToolNames(t *testing.T) {
	for _, name := range []string{
		"websearch",
		"webfetch",
		"browser_interact",
		"todo_write",
		"taskboard",
		"im_api",
		"send_im_message",
		"skill_install",
		"skill_search",
		"generate_video",
		"lsp_diagnostics",
	} {
		t.Run(name, func(t *testing.T) {
			rule, ok := builtinToolRule(name)
			if !ok {
				t.Fatalf("builtinToolRule(%q) missing", name)
			}
			if rule.Domain == "" || rule.Invocation == "" || rule.Risk == "" {
				t.Fatalf("builtinToolRule(%q) incomplete: %+v", name, rule)
			}
		})
	}
}

func TestHostToolSets(t *testing.T) {
	if !IsHostToolInSet(HostToolSetDefaultVisible, "tool_search") {
		t.Fatal("tool_search should be default-visible as discovery entrypoint")
	}
	if IsHostToolInSet(HostToolSetDefaultVisible, "send_im_message") {
		t.Fatal("side-effect messaging tool must not be default-visible")
	}
	for _, name := range []string{"batch", "task", "spawn_agent", "parallel_dispatch"} {
		if IsHostToolInSet(HostToolSetDefaultVisible, name) {
			t.Fatalf("execution entrypoint %q must not be default-visible", name)
		}
	}
	if !IsHostToolInSet(HostToolSetPlanControl, "finish_plan") {
		t.Fatal("finish_plan should be a plan control tool")
	}
	if !IsHostToolInSet(HostToolSetPlanAllowed, "read_file") {
		t.Fatal("read_file should be allowed in plan mode")
	}
	if IsHostToolInSet(HostToolSetPlanAllowed, "bash") {
		t.Fatal("bash must not be allowed in plan mode")
	}
	members := HostToolSetMembers(HostToolSetPlanAllowed)
	if len(members) == 0 {
		t.Fatal("plan allowed set should not be empty")
	}
	members[0] = "mutated"
	if IsHostToolInSet(HostToolSetPlanAllowed, "mutated") {
		t.Fatal("HostToolSetMembers must return a copy")
	}
}

func TestCapabilityRegistryHostToolSetsAreSubsetsOfBuiltinRules(t *testing.T) {
	for _, set := range []HostToolSet{HostToolSetDefaultVisible, HostToolSetPlanControl, HostToolSetPlanAllowed} {
		for _, name := range HostToolSetMembers(set) {
			if !IsKnownHostTool(name) {
				t.Fatalf("%s contains unknown host tool %q", set, name)
			}
		}
	}
}

func TestCapabilityRegistryStructuredDangerousOperations(t *testing.T) {
	tests := []struct {
		name     string
		tool     string
		input    string
		wantRisk bool
	}{
		{name: "feishu send", tool: "feishu_api", input: `{"action":"send_message"}`, wantRisk: false},
		{name: "feishu read", tool: "feishu_api", input: `{"action":"get_user"}`, wantRisk: false},
		{name: "send im", tool: "send_im_message", input: `{"platform":"feishu"}`, wantRisk: false},
		{name: "feishu create approval", tool: "feishu_api", input: `{"action":"create_approval"}`, wantRisk: true},
		{name: "memory save", tool: "memory", input: `{"operation":"save","content":"note"}`, wantRisk: false},
		{name: "memory delete", tool: "memory", input: `{"operation":"delete","id":1}`, wantRisk: true},
		{name: "taskboard update", tool: "taskboard", input: `{"operation":"update","id":"task-1","status":"done"}`, wantRisk: false},
		{name: "taskboard delete", tool: "taskboard", input: `{"operation":"delete","id":"task-1"}`, wantRisk: true},
		{name: "create tool", tool: "create_tool", input: `{}`, wantRisk: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := StructuredDangerousOperation(tt.tool, []byte(tt.input)); got != tt.wantRisk {
				t.Fatalf("StructuredDangerousOperation(%q, %s) = %v, want %v", tt.tool, tt.input, got, tt.wantRisk)
			}
		})
	}
}

func TestCapabilityRegistryFeishuIsMixedActionAware(t *testing.T) {
	profile := InferToolProfile(mcphost.ToolDefinition{Name: "feishu_api"}, ProfileHint{})
	if !IsMixedReadWriteTool("feishu_api") {
		t.Fatal("feishu_api should be registered as a mixed read/write tool")
	}
	if ProfileRequiresApproval(profile) {
		t.Fatalf("feishu_api should not be blanket approval-required; dangerous actions are action-level: %+v", profile)
	}
	if !StructuredDangerousAction("feishu_api", "create_task") {
		t.Fatal("create_task should remain action-level dangerous")
	}
	if StructuredDangerousAction("feishu_api", "send_message") {
		t.Fatal("normal send_message should not be action-level dangerous")
	}
	readActions := strings.Join(MixedReadOnlyActions("feishu_api"), "|")
	if !strings.Contains(readActions, "get_doc_content") || strings.Contains(readActions, "send_message") {
		t.Fatalf("read-only action set wrong: %q", readActions)
	}
	sendActions := strings.Join(ExternalSendActions("feishu_api"), "|")
	if !strings.Contains(sendActions, "send_message") || strings.Contains(sendActions, "create_task") {
		t.Fatalf("external-send action set wrong: %q", sendActions)
	}
}

func TestCapabilityRegistryMixedOperationToolsAreActionAware(t *testing.T) {
	for _, toolName := range []string{"memory", "taskboard", "browser_interact"} {
		t.Run(toolName, func(t *testing.T) {
			profile := InferToolProfile(mcphost.ToolDefinition{Name: toolName, Core: true}, ProfileHint{})
			if !IsMixedReadWriteTool(toolName) {
				t.Fatalf("%s should be registered as a mixed read/write tool", toolName)
			}
			if ProfileRequiresApproval(profile) {
				t.Fatalf("%s should not be blanket approval-required; dangerous actions are action-level: %+v", toolName, profile)
			}
			if len(MixedReadOnlyActions(toolName)) == 0 {
				t.Fatalf("%s should expose read-only actions", toolName)
			}
		})
	}
	if MixedActionField("memory") != "operation" || MixedActionField("taskboard") != "operation" {
		t.Fatalf("memory/taskboard should use operation field, got memory=%q taskboard=%q", MixedActionField("memory"), MixedActionField("taskboard"))
	}
	if MixedActionField("browser_interact") != "commands[].action" {
		t.Fatalf("browser_interact action field = %q, want commands[].action", MixedActionField("browser_interact"))
	}
	if !StructuredDangerousAction("memory", "delete") || !StructuredDangerousAction("taskboard", "delete") {
		t.Fatal("delete operations should remain action-level dangerous")
	}
}

func TestProfileHasSideEffectUsesRiskAndCapabilityMetadata(t *testing.T) {
	tests := []struct {
		name    string
		profile ToolProfile
		want    bool
	}{
		{name: "read only", profile: ToolProfile{Risk: RiskReadOnly, ReadOnly: true}, want: false},
		{name: "external write", profile: ToolProfile{Risk: RiskExternalWrite}, want: true},
		{name: "runtime exec", profile: ToolProfile{Risk: RiskRuntimeExec}, want: true},
		{name: "unknown", profile: ToolProfile{Risk: RiskUnknown}, want: true},
		{name: "capability", profile: ToolProfile{Capabilities: []Capability{CapabilityExternalSend}}, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ProfileHasSideEffect(tt.profile); got != tt.want {
				t.Fatalf("ProfileHasSideEffect() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestRouteDecisionUsesCapabilityGate(t *testing.T) {
	decision := BuildRouteDecision(IntentFrame{
		Kind:              IntentExternalWrite,
		AllowsSideEffects: true,
	}, []ToolProfile{
		{
			Name:       "bad_sender",
			Risk:       RiskExternalWrite,
			SideEffect: true,
		},
	})

	if decision.Mode != DecisionModeDiscover {
		t.Fatalf("Mode = %q, want discover", decision.Mode)
	}
	if len(decision.BlockedTools) != 1 || decision.BlockedTools[0].Reason != "capability missing" {
		t.Fatalf("BlockedTools = %+v, want capability missing", decision.BlockedTools)
	}
}
