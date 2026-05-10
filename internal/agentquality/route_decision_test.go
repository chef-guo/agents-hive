package agentquality

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/chef-guo/agents-hive/internal/router"
)

func TestRouteDecisionEventJSONStable(t *testing.T) {
	ev := RouteDecisionEvent{
		Mode:              "discover",
		IntentKind:        "create_skill",
		Domain:            "skill_authoring",
		RoutingConfidence: 0.8,
		AllowedTools:      []string{"skill"},
		CallableTools:     []string{"skill"},
		RecommendedTools:  []string{"tool_search"},
		AllowedToolInputs: map[string]map[string]string{"skill": {"name": "skill-creator"}},
		VisibleOnly:       []string{"tool_search"},
		BlockedTools:      []string{"mcp-builder"},
		BlockedReasons:    map[string]string{"mcp-builder": "domain_mismatch"},
		Reason:            "shadow observe",
	}

	b, err := json.Marshal(ev)
	if err != nil {
		t.Fatalf("marshal RouteDecisionEvent: %v", err)
	}

	got := string(b)
	for _, want := range []string{
		`"mode":"discover"`,
		`"intent_kind":"create_skill"`,
		`"domain":"skill_authoring"`,
		`"routing_confidence":0.8`,
		`"allowed_tools":["skill"]`,
		`"callable_tools":["skill"]`,
		`"recommended_tools":["tool_search"]`,
		`"allowed_tool_inputs":{"skill":{"name":"skill-creator"}}`,
		`"visible_only":["tool_search"]`,
		`"blocked_tools":["mcp-builder"]`,
		`"blocked_reasons":{"mcp-builder":"domain_mismatch"}`,
	} {
		if !containsString(got, want) {
			t.Fatalf("RouteDecisionEvent JSON missing %s in %s", want, got)
		}
	}
}

func TestRouteDecisionEventFromRouter(t *testing.T) {
	decision := router.BuildRouteDecision(router.IntentFrame{Kind: router.IntentCreateSkill, Subject: "skill_authoring"}, []router.ToolProfile{
		skillCreatorProfile(),
		mcpBuilderProfile(),
	})

	ev := RouteDecisionEventFromRouter(decision)

	if ev.IntentKind != "create_skill" {
		t.Fatalf("IntentKind = %q, want create_skill", ev.IntentKind)
	}
	if len(ev.AllowedTools) != 1 || ev.AllowedTools[0] != "skill" {
		t.Fatalf("AllowedTools = %+v", ev.AllowedTools)
	}
	if len(ev.CallableTools) != 1 || ev.CallableTools[0] != "skill" {
		t.Fatalf("CallableTools = %+v", ev.CallableTools)
	}
	if ev.AllowedToolInputs["skill"]["name"] != "skill-creator" {
		t.Fatalf("AllowedToolInputs = %+v", ev.AllowedToolInputs)
	}
	if ev.BlockedReasons["mcp-builder"] != "domain_mismatch" {
		t.Fatalf("BlockedReasons = %+v", ev.BlockedReasons)
	}
}

func TestRunRouteEvalCasesCoversCoreRoutingRegressions(t *testing.T) {
	cases := []RouteEvalCase{
		{
			ID: "create_greeting_skill_routes_to_skill_creator",
			Intent: router.IntentFrame{
				Kind:    router.IntentCreateSkill,
				Subject: "跟我打招呼的技能",
			},
			Candidates: []router.ToolProfile{
				skillCreatorProfile(),
				mcpBuilderProfile(),
			},
			WantAllowedTools:  []string{"skill"},
			WantBlockedTools:  []string{"mcp-builder"},
			WantVisibleOnly:   []string{"tool_search"},
			WantAllowedInputs: map[string]map[string]string{"skill": {"name": "skill-creator"}},
		},
		{
			ID: "create_mcp_server_can_use_mcp_builder_as_skill_workflow",
			Intent: router.IntentFrame{
				Kind:    router.IntentManageTool,
				Subject: "创建 MCP server 接入 GitHub API",
			},
			Candidates: []router.ToolProfile{
				mcpBuilderProfile(),
			},
			WantAllowedTools:  []string{"skill"},
			WantVisibleOnly:   []string{"tool_search"},
			WantAllowedInputs: map[string]map[string]string{"skill": {"name": "mcp-builder"}},
		},
		{
			ID: "negated_send_blocks_external_write",
			Intent: router.IntentFrame{
				Kind:           router.IntentWriteLocal,
				Subject:        "飞书通知文案",
				NegatedActions: []string{"send"},
			},
			Candidates: []router.ToolProfile{
				{
					Name:         "feishu_api",
					Kind:         router.CapabilityKindBuiltinTool,
					Domain:       "messaging",
					Source:       router.CapabilitySourceBuiltin,
					Invocation:   router.InvocationDirectTool,
					Risk:         router.RiskExternalWrite,
					Trust:        router.TrustBuiltIn,
					SideEffect:   true,
					Capabilities: []router.Capability{router.CapabilityExternalSend},
				},
			},
			WantBlockedTools: []string{"feishu_api"},
			WantVisibleOnly:  []string{"tool_search"},
		},
		{
			ID: "unknown_open_world_mcp_blocks_by_default",
			Intent: router.IntentFrame{
				Kind:    router.IntentManageTool,
				Subject: "检查未知 MCP 工具",
			},
			Candidates: []router.ToolProfile{
				router.UnknownMCPToolProfile("unknown__danger"),
			},
			WantBlockedTools: []string{"unknown__danger"},
			WantVisibleOnly:  []string{"tool_search"},
		},
		{
			ID: "read_intent_allows_read_only_builtin",
			Intent: router.IntentFrame{
				Kind:    router.IntentRead,
				Subject: "读取本地配置",
			},
			Candidates: []router.ToolProfile{
				readOnlyBuiltinProfile("read_file"),
			},
			WantAllowedTools: []string{"read_file"},
			WantVisibleOnly:  []string{"tool_search"},
		},
		{
			ID: "read_intent_blocks_side_effect_tool",
			Intent: router.IntentFrame{
				Kind:    router.IntentRead,
				Subject: "读取飞书消息",
			},
			Candidates: []router.ToolProfile{
				feishuAPIProfile(),
			},
			WantBlockedTools: []string{"feishu_api"},
			WantVisibleOnly:  []string{"tool_search"},
		},
		{
			ID: "external_write_allows_external_send_when_side_effects_allowed",
			Intent: router.IntentFrame{
				Kind:              router.IntentExternalWrite,
				Subject:           "发送飞书通知",
				AllowsSideEffects: true,
			},
			Candidates: []router.ToolProfile{
				feishuAPIProfile(),
			},
			WantAllowedTools: []string{"feishu_api"},
			WantVisibleOnly:  []string{"tool_search"},
		},
		{
			ID: "external_write_blocks_external_send_when_side_effects_disallowed",
			Intent: router.IntentFrame{
				Kind:    router.IntentExternalWrite,
				Subject: "发送飞书通知",
			},
			Candidates: []router.ToolProfile{
				feishuAPIProfile(),
			},
			WantBlockedTools: []string{"feishu_api"},
			WantVisibleOnly:  []string{"tool_search"},
		},
		{
			ID: "runtime_exec_blocks_for_non_manage_tool_intent",
			Intent: router.IntentFrame{
				Kind:    router.IntentRead,
				Subject: "查看 shell 输出",
			},
			Candidates: []router.ToolProfile{
				runtimeExecProfile("shell_exec"),
			},
			WantBlockedTools: []string{"shell_exec"},
			WantVisibleOnly:  []string{"tool_search"},
		},
		{
			ID: "runtime_exec_allowed_only_under_manage_tool",
			Intent: router.IntentFrame{
				Kind:    router.IntentManageTool,
				Subject: "注册本地执行工具",
			},
			Candidates: []router.ToolProfile{
				runtimeExecProfile("shell_exec"),
			},
			WantBlockedTools: []string{"shell_exec"},
			WantVisibleOnly:  []string{"tool_search"},
		},
		{
			ID: "create_skill_blocks_arbitrary_custom_tool",
			Intent: router.IntentFrame{
				Kind:    router.IntentCreateSkill,
				Subject: "创建一个日报 skill",
			},
			Candidates: []router.ToolProfile{
				customLocalWriteProfile("custom_skill_writer"),
			},
			WantBlockedTools: []string{"custom_skill_writer"},
			WantVisibleOnly:  []string{"tool_search"},
		},
	}

	results := RunRouteEvalCases(cases)
	for _, result := range results {
		if !result.Passed {
			t.Fatalf("%s failed: %+v decision=%+v", result.CaseID, result.Failures, result.Decision)
		}
	}
}

func TestRouteEvalCorpusLoadsAndRuns(t *testing.T) {
	cases, err := LoadRouteEvalCases(filepath.Join("testdata", "route_eval"))
	if err != nil {
		t.Fatalf("LoadRouteEvalCases: %v", err)
	}
	if len(cases) < DefaultRouteEvalGateThresholds.MinCases {
		t.Fatalf("route eval corpus has %d cases, want at least %d", len(cases), DefaultRouteEvalGateThresholds.MinCases)
	}

	results := RunRouteEvalCases(cases)
	for _, result := range results {
		if !result.Passed {
			t.Fatalf("%s failed: %+v decision=%+v", result.CaseID, result.Failures, result.Decision)
		}
	}
}

func TestRouteEvalGateMetrics(t *testing.T) {
	cases, err := LoadRouteEvalCases(filepath.Join("testdata", "route_eval"))
	if err != nil {
		t.Fatalf("LoadRouteEvalCases: %v", err)
	}
	results := RunRouteEvalCases(cases)
	metrics, failures := EvaluateRouteEvalGate(cases, results, DefaultRouteEvalGateThresholds)
	if len(failures) > 0 {
		t.Fatalf("route eval gate failed: %+v metrics=%+v", failures, metrics)
	}
	if metrics.PromptInjectionCases < 5 {
		t.Fatalf("prompt injection cases = %d, want at least 5", metrics.PromptInjectionCases)
	}
	if metrics.PromptInjectionBypassCount != 0 {
		t.Fatalf("prompt injection bypass = %d, want 0", metrics.PromptInjectionBypassCount)
	}
}

func TestRouteEvalCorpusCoversPromptInjection(t *testing.T) {
	requireRouteEvalTag(t, "prompt-injection")
}

func TestRouteEvalCorpusCoversFalseMatch(t *testing.T) {
	requireRouteEvalTag(t, "false-match")
}

func TestRouteEvalCorpusCoversSkillVsMCPAndDiscoveryOnly(t *testing.T) {
	for _, tag := range []string{"skill-vs-mcp", "tool_search", "discovery-only", "unknown-mcp", "fail-closed"} {
		requireRouteEvalTag(t, tag)
	}
}

func requireRouteEvalTag(t *testing.T, tag string) {
	t.Helper()
	cases, err := LoadRouteEvalCases(filepath.Join("testdata", "route_eval"))
	if err != nil {
		t.Fatalf("LoadRouteEvalCases: %v", err)
	}
	for _, c := range cases {
		for _, got := range c.Tags {
			if got == tag {
				return
			}
		}
	}
	t.Fatalf("route eval corpus missing tag %q", tag)
}

func skillCreatorProfile() router.ToolProfile {
	return router.ToolProfile{
		Name:         "skill-creator",
		Kind:         router.CapabilityKindSkillWorkflow,
		Domain:       "skill_authoring",
		Source:       router.CapabilitySourceLocalSkill,
		Invocation:   router.InvocationSkillTool,
		Risk:         router.RiskLocalWrite,
		Trust:        router.TrustLocal,
		Capabilities: []router.Capability{router.CapabilityMetaSkillCreate},
		AllowedIntentKinds: []router.IntentKind{
			router.IntentCreateSkill,
			router.IntentModifySkill,
		},
	}
}

func mcpBuilderProfile() router.ToolProfile {
	return router.ToolProfile{
		Name:         "mcp-builder",
		Kind:         router.CapabilityKindSkillWorkflow,
		Domain:       "mcp_server_building",
		Source:       router.CapabilitySourceLocalSkill,
		Invocation:   router.InvocationSkillTool,
		Risk:         router.RiskLocalWrite,
		Trust:        router.TrustLocal,
		Capabilities: []router.Capability{router.CapabilityMetaToolRegister},
		AllowedIntentKinds: []router.IntentKind{
			router.IntentManageTool,
		},
	}
}

func readOnlyBuiltinProfile(name string) router.ToolProfile {
	return router.ToolProfile{
		Name:       name,
		Kind:       router.CapabilityKindBuiltinTool,
		Domain:     "filesystem",
		Source:     router.CapabilitySourceBuiltin,
		Invocation: router.InvocationDirectTool,
		Risk:       router.RiskReadOnly,
		Trust:      router.TrustBuiltIn,
		ReadOnly:   true,
	}
}

func feishuAPIProfile() router.ToolProfile {
	return router.ToolProfile{
		Name:         "feishu_api",
		Kind:         router.CapabilityKindBuiltinTool,
		Domain:       "messaging",
		Source:       router.CapabilitySourceBuiltin,
		Invocation:   router.InvocationDirectTool,
		Risk:         router.RiskExternalWrite,
		Trust:        router.TrustBuiltIn,
		SideEffect:   true,
		Capabilities: []router.Capability{router.CapabilityExternalSend},
	}
}

func runtimeExecProfile(name string) router.ToolProfile {
	return router.ToolProfile{
		Name:         name,
		Kind:         router.CapabilityKindCustomTool,
		Domain:       "runtime",
		Source:       router.CapabilitySourceCustomDir,
		Invocation:   router.InvocationDirectTool,
		Risk:         router.RiskRuntimeExec,
		Trust:        router.TrustLocal,
		SideEffect:   true,
		Capabilities: []router.Capability{router.CapabilityRuntimeExec},
	}
}

func customLocalWriteProfile(name string) router.ToolProfile {
	return router.ToolProfile{
		Name:       name,
		Kind:       router.CapabilityKindCustomTool,
		Domain:     "skill_authoring",
		Source:     router.CapabilitySourceCustomDir,
		Invocation: router.InvocationDirectTool,
		Risk:       router.RiskLocalWrite,
		Trust:      router.TrustLocal,
		SideEffect: true,
	}
}

func containsString(s, substr string) bool {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return substr == ""
}
