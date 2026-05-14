package master

import (
	"reflect"
	"strings"
	"testing"

	"github.com/chef-guo/agents-hive/internal/config"
	"github.com/chef-guo/agents-hive/internal/llm"
	"github.com/chef-guo/agents-hive/internal/mcphost"
	"github.com/chef-guo/agents-hive/internal/router"
	"github.com/chef-guo/agents-hive/internal/sessiontodo"
	"github.com/chef-guo/agents-hive/internal/skills"
)

func TestModelVisibleTools_DefaultsHideExtensionsAfterDiscoveryUntilRouted(t *testing.T) {
	session := &SessionState{ID: "s1"}
	catalog := []mcphost.ToolDefinition{
		{Name: "read_file", Core: true},
		{Name: "tool_search", Core: true},
		{Name: "skill"},
		{Name: "custom_ext"},
		{Name: "acme__publish"},
	}

	initial := modelVisibleToolsForSession(session, catalog)
	if hasTool(initial, "custom_ext") {
		t.Fatal("non-core extension tool should not be model-visible before discovery")
	}
	if hasTool(initial, "acme__publish") {
		t.Fatal("external MCP tool should not be model-visible before discovery")
	}
	if !hasTool(initial, "read_file") || !hasTool(initial, "tool_search") || !hasTool(initial, "skill") {
		t.Fatal("default core and quality-leverage tools should remain model-visible")
	}
	if allowed, ok := session.AllowedToolInput("skill", "name"); !ok || allowed != routeEmptyInputValue {
		t.Fatalf("default-visible skill should be constrained to list mode, got %q/%v", allowed, ok)
	}

	session.RecordDiscoveredTools([]string{"custom_ext", "acme__publish"})
	afterDiscovery := modelVisibleToolsForSession(session, catalog)
	if hasTool(afterDiscovery, "custom_ext") {
		t.Fatal("tool_search discovery must not make custom extension model-visible")
	}
	if hasTool(afterDiscovery, "acme__publish") {
		t.Fatal("tool_search discovery must not make external MCP tool model-visible")
	}
	if !session.IsToolDiscovered("custom_ext") || !session.IsToolDiscovered("acme__publish") {
		t.Fatal("tool_search discovery state should still be recorded for audit")
	}
}

func TestModelVisibleTools_ReturnsStableToolOrder(t *testing.T) {
	session := &SessionState{ID: "s-stable-tools"}
	catalog := []mcphost.ToolDefinition{
		{Name: "tool_search", Core: true},
		{Name: "memory"},
		{Name: "read_file", Core: true},
		{Name: "skill"},
	}

	visible := modelVisibleToolsForSession(session, catalog)
	got := toolNamesForTest(visible)
	want := []string{"memory", "read_file", "skill", "tool_search"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("visible tool order = %v, want %v", got, want)
	}
}

func TestModelVisibleTools_ReturnsStableOrderWithRecalledTool(t *testing.T) {
	session := &SessionState{ID: "s-stable-recall"}
	catalog := []mcphost.ToolDefinition{
		{Name: "tool_search", Core: true},
		{
			Name:        "send_im_message",
			Description: "发送消息到 IM 平台（钉钉/飞书/企业微信/个人微信）",
			InputSchema: []byte(`{"type":"object","properties":{"platform":{"type":"string"},"content":{"type":"string"}}}`),
		},
		{Name: "memory"},
		{Name: "read_file", Core: true},
	}

	visible, _ := modelVisibleToolsForSessionWithRecallObservationAndSkillsAndIntent(
		session,
		catalog,
		nil,
		"发送给飞书用户郭松",
		config.DefaultToolRecallConfig(),
		externalSendIntentForVisibilityTest(),
	)

	got := toolNamesForTest(visible)
	want := []string{"memory", "read_file", "send_im_message", "tool_search"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("visible tool order = %v, want %v", got, want)
	}
}

func TestModelVisibleTools_FastPathIgnoresCoreAndAppliesBudget(t *testing.T) {
	session := &SessionState{ID: "s-fast-path-budget"}
	catalog := []mcphost.ToolDefinition{
		{Name: "write_file", Core: true},
		{Name: "bash", Core: true},
		{Name: "read_file", Core: true},
		{Name: "grep", Core: true},
		{Name: "glob", Core: true},
		{Name: "tool_search", Core: true},
		{Name: "question", Core: true},
		{Name: "memory"},
		{Name: "skill"},
		{Name: "todo_write", Core: true},
		{Name: "browser_interact", Core: true},
	}

	visible, obs := modelVisibleToolsForSessionWithRecallObservationAndSkillsAndIntentWithOptions(
		session,
		catalog,
		nil,
		"普通聊天",
		config.DefaultToolRecallConfig(),
		router.IntentFrame{Kind: router.IntentAnswer},
		toolVisibilityOptions{FastPath: true, MaxModelVisibleTools: 8},
	)

	got := toolNamesForTest(visible)
	want := []string{"glob", "grep", "memory", "question", "read_file", "skill", "tool_search"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("fast path visible tools = %v, want %v", got, want)
	}
	for _, name := range []string{"bash", "browser_interact", "todo_write", "write_file"} {
		if hasTool(visible, name) {
			t.Fatalf("fast path must ignore Core-only write/control tool %q, visible=%v", name, got)
		}
	}
	if obs.VisibleBeforeCount != len(want) || obs.VisibleAfterCount != len(want) {
		t.Fatalf("visible counts = %d/%d, want %d/%d", obs.VisibleBeforeCount, obs.VisibleAfterCount, len(want), len(want))
	}
	if obs.MaxVisibleTools != 8 {
		t.Fatalf("MaxVisibleTools = %d, want 8", obs.MaxVisibleTools)
	}
}

func TestModelVisibleTools_FastPathMaxVisibleToolsTrimsWithStablePriority(t *testing.T) {
	session := &SessionState{ID: "s-fast-path-trim"}
	catalog := []mcphost.ToolDefinition{
		{Name: "websearch", Description: "网络搜索"},
		{Name: "read_file", Core: true},
		{Name: "grep", Core: true},
		{Name: "glob", Core: true},
		{Name: "tool_search", Core: true},
		{Name: "question", Core: true},
		{Name: "memory"},
		{Name: "skill"},
	}

	visible, obs := modelVisibleToolsForSessionWithRecallObservationAndSkillsAndIntentWithOptions(
		session,
		catalog,
		nil,
		"网络搜索一下状态",
		config.DefaultToolRecallConfig(),
		router.IntentFrame{Kind: router.IntentRead},
		toolVisibilityOptions{FastPath: true, MaxModelVisibleTools: 5},
	)

	got := toolNamesForTest(visible)
	want := []string{"memory", "question", "skill", "tool_search", "websearch"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("trimmed visible tools = %v, want %v", got, want)
	}
	if obs.VisibleTrimmedCount != 3 {
		t.Fatalf("VisibleTrimmedCount = %d, want 3", obs.VisibleTrimmedCount)
	}
	if obs.VisibleAfterCount > 5 {
		t.Fatalf("VisibleAfterCount = %d, want <= 5", obs.VisibleAfterCount)
	}
	if !hasTool(visible, "tool_search") {
		t.Fatalf("tool_search must survive trimming, visible=%v", got)
	}
}

func TestModelVisibleTools_MaxVisibleToolsZeroRestoresCoreVisibility(t *testing.T) {
	session := &SessionState{ID: "s-fast-path-rollback"}
	catalog := []mcphost.ToolDefinition{
		{Name: "bash", Core: true},
		{Name: "read_file", Core: true},
		{Name: "tool_search", Core: true},
	}

	visible, obs := modelVisibleToolsForSessionWithRecallObservationAndSkillsAndIntentWithOptions(
		session,
		catalog,
		nil,
		"普通聊天",
		config.DefaultToolRecallConfig(),
		router.IntentFrame{Kind: router.IntentAnswer},
		toolVisibilityOptions{FastPath: true, MaxModelVisibleTools: 0},
	)

	if !hasTool(visible, "bash") {
		t.Fatalf("max_model_visible_tools=0 should restore Core visibility, visible=%v", toolNamesForTest(visible))
	}
	if obs.VisibleTrimmedCount != 0 || obs.MaxVisibleTools != 0 {
		t.Fatalf("rollback should not trim or report budget, obs=%+v", obs)
	}
}

func TestModelVisibleTools_DefaultVisibleSetDoesNotExposeExecutionEntrypoints(t *testing.T) {
	session := &SessionState{ID: "s-default-entrypoints"}
	catalog := []mcphost.ToolDefinition{
		{Name: "tool_search", Core: true},
		{Name: "skill"},
		{Name: "batch"},
		{Name: "task"},
		{Name: "spawn_agent", Core: true},
		{Name: "parallel_dispatch"},
		{Name: "memory"},
	}

	visible := modelVisibleToolsForSession(session, catalog)

	for _, name := range []string{"batch", "task", "spawn_agent", "parallel_dispatch"} {
		if hasTool(visible, name) {
			t.Fatalf("execution entrypoint %q must not be default-visible for read/answer turns", name)
		}
	}
	for _, name := range []string{"tool_search", "skill", "memory"} {
		if !hasTool(visible, name) {
			t.Fatalf("%q should remain default-visible", name)
		}
	}
}

func TestToolVisibility_RuntimeAllowedToolsAreRouteDecisionBounded(t *testing.T) {
	session := &SessionState{ID: "s-runtime-allowed"}
	catalog := []mcphost.ToolDefinition{
		{Name: "read_file", Core: true},
		{Name: "write_file", Core: true},
		{Name: "bash", Core: true},
		{Name: "tool_search", Core: true},
		{Name: "memory"},
	}

	modelVisibleToolsForSessionWithRecallObservationAndSkillsAndIntent(
		session,
		catalog,
		nil,
		"读取项目状态",
		config.DefaultToolRecallConfig(),
		router.IntentFrame{Kind: router.IntentRead},
	)

	for _, name := range []string{"read_file", "tool_search", "memory"} {
		if !session.IsAllowedTool(name) {
			t.Fatalf("%q should be runtime-allowed for read/default visible turn, allowed=%v", name, session.AllowedToolsSnapshot())
		}
	}
	for _, name := range []string{"write_file", "bash"} {
		if session.IsAllowedTool(name) {
			t.Fatalf("%q must not be runtime-allowed for read turn, allowed=%v", name, session.AllowedToolsSnapshot())
		}
	}

	gotAllowed := session.AllowedToolsSnapshot()
	wantAllowed := []string{"memory", "read_file", "tool_search"}
	if !reflect.DeepEqual(gotAllowed, wantAllowed) {
		t.Fatalf("runtime allowed tools order = %v, want %v", gotAllowed, wantAllowed)
	}
}

func TestModelVisibleTools_DiscoveredReadOnlyToolEntersNextTurnCandidates(t *testing.T) {
	session := &SessionState{ID: "s-discovered-read"}
	session.RecordDiscoveredTools([]string{"websearch"})
	catalog := []mcphost.ToolDefinition{
		{Name: "tool_search", Core: true},
		{Name: "websearch", Description: "网络搜索工具"},
	}

	visible, obs := modelVisibleToolsForSessionWithRecallObservationAndSkillsAndIntent(
		session,
		catalog,
		nil,
		"继续",
		config.DefaultToolRecallConfig(),
		router.IntentFrame{Kind: router.IntentAnswer},
	)

	if !hasTool(visible, "websearch") {
		t.Fatalf("tool_search-discovered read-only tool should enter next-turn candidates, visible=%v", toolNamesForTest(visible))
	}
	if !obs.RecalledToolNames["websearch"] {
		t.Fatalf("discovered tool should be marked as recalled for audit: %#v", obs.RecalledToolNames)
	}
}

func TestModelVisibleTools_TrustedRemoteReadToolCanEnterCandidates(t *testing.T) {
	session := &SessionState{ID: "s-trusted-mcp-read"}
	session.RecordDiscoveredTools([]string{"metamcp__query_prometheus"})
	catalog := []mcphost.ToolDefinition{
		{Name: "tool_search", Core: true},
		{
			Name:         "metamcp__query_prometheus",
			Description:  "Query Prometheus metrics",
			SourceServer: "metamcp",
			Trusted:      true,
		},
	}

	visible, obs := modelVisibleToolsForSessionWithRecallObservationAndSkillsAndIntent(
		session,
		catalog,
		nil,
		"查一下 prometheus 指标",
		config.DefaultToolRecallConfig(),
		router.IntentFrame{Kind: router.IntentExternalRead, RequiresExternal: true},
	)

	if !hasTool(visible, "metamcp__query_prometheus") {
		t.Fatalf("trusted remote read tool should be model-visible after discovery/recall: %v", toolNamesForTest(visible))
	}
	if !session.IsAllowedTool("metamcp__query_prometheus") {
		t.Fatalf("trusted remote read tool should be runtime-allowed, allowed=%v", session.AllowedToolsSnapshot())
	}
	entry := obs.Entries["metamcp__query_prometheus"]
	if !entry.TaskCallable || entry.MayRequireApproval || entry.DiscoveryOnly {
		t.Fatalf("trusted remote read admission should be callable without approval: %+v", entry)
	}
}

func TestModelVisibleTools_TrustedRemoteDangerousToolNeedsRouteBlock(t *testing.T) {
	session := &SessionState{ID: "s-trusted-mcp-danger"}
	session.RecordDiscoveredTools([]string{"metamcp__delete_dashboard"})
	catalog := []mcphost.ToolDefinition{
		{Name: "tool_search", Core: true},
		{
			Name:         "metamcp__delete_dashboard",
			Description:  "Delete Grafana dashboard",
			SourceServer: "metamcp",
			Trusted:      true,
		},
	}

	visible, obs := modelVisibleToolsForSessionWithRecallObservationAndSkillsAndIntent(
		session,
		catalog,
		nil,
		"查一下 dashboard",
		config.DefaultToolRecallConfig(),
		router.IntentFrame{Kind: router.IntentExternalRead, RequiresExternal: true},
	)

	if hasTool(visible, "metamcp__delete_dashboard") {
		t.Fatalf("trusted but destructive MCP tool should not be visible for read intent: %v", toolNamesForTest(visible))
	}
	entry := obs.Entries["metamcp__delete_dashboard"]
	if !entry.MayRequireApproval || entry.TaskCallable {
		t.Fatalf("trusted destructive MCP admission should require approval and not be task-callable: %+v", entry)
	}
}

func TestAdmissionEntriesUseUnifiedPolicyForSafeTools(t *testing.T) {
	session := &SessionState{ID: "s-unified-policy-admission"}
	session.RecordDiscoveredTools([]string{"project_status", "metamcp__query_prometheus"})
	catalog := []mcphost.ToolDefinition{
		{Name: "tool_search", Core: true},
		{Name: "project_status", Description: "查询项目状态", IsConcurrencySafe: true},
		{Name: "metamcp__query_prometheus", Description: "Query Prometheus metrics", SourceServer: "metamcp", Trusted: true},
	}

	_, obs := modelVisibleToolsForSessionWithRecallObservationAndSkillsAndIntent(
		session,
		catalog,
		nil,
		"查询状态和 prometheus 指标",
		config.DefaultToolRecallConfig(),
		router.IntentFrame{Kind: router.IntentExternalRead, RequiresExternal: true},
	)

	for _, name := range []string{"project_status", "metamcp__query_prometheus"} {
		entry := obs.Entries[name]
		if !entry.TaskCallable || entry.MayRequireApproval || entry.DiscoveryOnly {
			t.Fatalf("%s should be callable safe admission, got %+v", name, entry)
		}
	}
}

func TestModelVisibleTools_DiscoveredUnknownToolStillRequiresRouteAuthorization(t *testing.T) {
	session := &SessionState{ID: "s-discovered-unknown"}
	session.RecordDiscoveredTools([]string{"custom_ext"})
	catalog := []mcphost.ToolDefinition{
		{Name: "tool_search", Core: true},
		{Name: "custom_ext", Description: "自定义扩展工具"},
	}

	visible, obs := modelVisibleToolsForSessionWithRecallObservationAndSkillsAndIntent(
		session,
		catalog,
		nil,
		"继续",
		config.DefaultToolRecallConfig(),
		router.IntentFrame{Kind: router.IntentAnswer},
	)

	if hasTool(visible, "custom_ext") {
		t.Fatalf("discovered unknown/open-world tool must not become callable without route authorization, visible=%v", toolNamesForTest(visible))
	}
	if entry := obs.Entries["custom_ext"]; entry.TaskCallable || entry.PrimaryBlockReason == "" {
		t.Fatalf("unknown discovered tool should be blocked by RouteDecision: %#v", entry)
	}
}

func TestModelVisibleTools_PlanModeUsesExecutionGate(t *testing.T) {
	session := &SessionState{
		ID:         "s-plan",
		PlanMode:   true,
		PlanStatus: sessiontodo.PlanStatusPlanning,
	}
	catalog := []mcphost.ToolDefinition{
		{Name: "read_file", Core: true},
		{Name: "grep", Core: true},
		{Name: "question", Core: true},
		{Name: "todo_write", Core: true},
		{Name: "exit_plan_mode", Core: true},
		{Name: "write_file", Core: true},
		{Name: "bash", Core: true},
		{Name: "taskboard", Core: true},
		{Name: "send_im_message", Core: true},
	}

	visible := modelVisibleToolsForSession(session, catalog)

	for _, name := range []string{"read_file", "grep", "question", "todo_write", "exit_plan_mode"} {
		if !hasTool(visible, name) {
			t.Fatalf("plan mode should keep %q visible", name)
		}
		if !session.IsAllowedTool(name) {
			t.Fatalf("plan mode should runtime-allow visible plan tool %q, allowed=%v", name, session.AllowedToolsSnapshot())
		}
	}
	for _, name := range []string{"write_file", "bash", "taskboard", "send_im_message"} {
		if hasTool(visible, name) {
			t.Fatalf("plan mode should hide write/control tool %q", name)
		}
	}
}

func TestModelVisibleTools_PerTurnRecallAddsHiddenToolsWithoutDiscovery(t *testing.T) {
	session := &SessionState{ID: "s1"}
	catalog := []mcphost.ToolDefinition{
		{Name: "read_file", Core: true},
		{Name: "tool_search", Core: true},
		{
			Name:        "send_im_message",
			Description: "发送消息到 IM 平台（钉钉/飞书/企业微信/个人微信）",
			InputSchema: []byte(`{
				"type": "object",
				"properties": {
					"platform": {"type": "string", "enum": ["dingtalk", "feishu", "wecom"]},
					"chat_id": {"type": "string", "description": "聊天 ID"},
					"content": {"type": "string", "description": "消息内容"}
				}
			}`),
		},
	}

	initial := modelVisibleToolsForSession(session, catalog)
	if hasTool(initial, "send_im_message") {
		t.Fatal("hidden IM tool should not be baseline-visible before discovery")
	}

	recalled := visibleToolsForIntent(session, catalog, "发送给飞书用户:郭松", config.DefaultToolRecallConfig(), externalSendIntentForVisibilityTest())
	if !hasTool(recalled, "send_im_message") {
		t.Fatal("structured external-send per-turn recall should add matching hidden IM tool")
	}
	if session.IsToolDiscovered("send_im_message") {
		t.Fatal("per-turn recall should not persist hidden tool into session discovery state")
	}

	baselineAfterRecall := modelVisibleToolsForSession(session, catalog)
	if hasTool(baselineAfterRecall, "send_im_message") {
		t.Fatal("per-turn recall must not expand the baseline-visible tool set")
	}
}

func TestReflectionBlockRouteDecisionHidesRecalledTool(t *testing.T) {
	session := &SessionState{ID: "s-reflection"}
	if !session.AddReflectionBlock(router.ReflectionBlock{
		ToolName:    "send_im_message",
		Mode:        "exec",
		Reason:      "permission denied",
		FailureKind: "permission_denied",
	}) {
		t.Fatal("expected structural failure to create reflection block")
	}
	catalog := []mcphost.ToolDefinition{
		{Name: "read_file", Core: true},
		{Name: "tool_search", Core: true},
		{
			Name:        "send_im_message",
			Description: "发送消息到 IM 平台（钉钉/飞书/企业微信/个人微信）",
			InputSchema: []byte(`{"type":"object","properties":{"platform":{"type":"string"},"content":{"type":"string"}}}`),
		},
	}

	visible, obs := modelVisibleToolsForSessionWithRecallObservationAndSkillsAndIntent(session, catalog, nil, "发送给飞书用户:郭松", config.DefaultToolRecallConfig(), externalSendIntentForVisibilityTest())

	if hasTool(visible, "send_im_message") {
		t.Fatal("reflection block should hide recalled tool from model-visible set")
	}
	if len(obs.RouteDecision.BlockedTools) != 1 || obs.RouteDecision.BlockedTools[0].Name != "send_im_message" {
		t.Fatalf("RouteDecision blocked tools = %+v, want send_im_message", obs.RouteDecision.BlockedTools)
	}
	if !strings.Contains(obs.RouteDecision.BlockedTools[0].Reason, "permission_denied") {
		t.Fatalf("RouteDecision block reason = %q, want failure kind", obs.RouteDecision.BlockedTools[0].Reason)
	}
}

func TestModelVisibleTools_PerTurnRecallDoesNotExpandDangerousBaseline(t *testing.T) {
	session := &SessionState{ID: "s1"}
	catalog := []mcphost.ToolDefinition{
		{Name: "read_file", Core: true},
		{Name: "tool_search", Core: true},
		{
			Name:        "github__create_issue",
			Description: "[github] Create a GitHub issue",
			InputSchema: []byte(`{"type":"object","properties":{"title":{"type":"string"},"body":{"type":"string"}}}`),
		},
	}

	recalled := modelVisibleToolsForSessionWithRecall(session, catalog, "create a github issue", config.DefaultToolRecallConfig())
	if hasTool(recalled, "github__create_issue") {
		t.Fatal("per-turn recall must not expose open-world MCP tools without RouteDecision authorization")
	}
	if session.IsToolDiscovered("github__create_issue") {
		t.Fatal("per-turn recall should not mark dangerous hidden tools as discovered")
	}

	baselineAfterRecall := modelVisibleToolsForSession(session, catalog)
	if hasTool(baselineAfterRecall, "github__create_issue") {
		t.Fatal("dangerous recalled tool must not become baseline-visible without explicit tool_search discovery")
	}
}

func TestModelVisibleTools_CreateSkillRoutesThroughSkillCreatorNotMCPBuilder(t *testing.T) {
	session := &SessionState{ID: "s1"}
	catalog := []mcphost.ToolDefinition{
		{Name: "read_file", Core: true},
		{Name: "tool_search", Core: true},
		{Name: "skill"},
		{
			Name:        "mcp-builder",
			Description: "Guide for creating high-quality MCP servers as a skill workflow",
		},
	}
	skillMetas := []skills.SkillMetadata{
		{Name: "skill-creator", Description: "Create or modify Codex skills"},
		{Name: "mcp-builder", Description: "Build MCP servers"},
	}

	visible, obs := modelVisibleToolsForSessionWithRecallObservationAndSkillsAndIntent(session, catalog, skillMetas, "创建一个跟我打招呼的技能", config.DefaultToolRecallConfig(), router.IntentFrame{
		Kind:              router.IntentCreateSkill,
		AllowsSideEffects: true,
	})

	if hasTool(visible, "mcp-builder") {
		t.Fatal("create-skill intent must not expose mcp-builder as a callable tool")
	}
	if !hasTool(visible, "skill") {
		t.Fatal("create-skill intent should keep the skill entrypoint visible")
	}
	if obs.RouteDecision.AllowedToolInputs["skill"]["name"] != "skill-creator" {
		t.Fatalf("allowed skill name = %#v, want skill-creator", obs.RouteDecision.AllowedToolInputs)
	}
	if allowed, ok := session.AllowedToolInput("skill", "name"); !ok || allowed != "skill-creator" {
		t.Fatalf("session allowed skill input = %q/%v, want skill-creator/true", allowed, ok)
	}
}

func TestModelVisibleTools_CreateMCPServerRoutesMCPBuilderAsSkillWorkflow(t *testing.T) {
	session := &SessionState{ID: "s1"}
	catalog := []mcphost.ToolDefinition{
		{Name: "tool_search", Core: true},
		{Name: "skill"},
		{
			Name:        "mcp-builder",
			Description: "Guide for creating high-quality MCP servers as a skill workflow",
		},
	}
	skillMetas := []skills.SkillMetadata{
		{Name: "mcp-builder", Description: "Build MCP servers"},
	}

	visible, obs := modelVisibleToolsForSessionWithRecallObservationAndSkillsAndIntent(session, catalog, skillMetas, "创建 MCP server 接入 GitHub API", config.DefaultToolRecallConfig(), router.IntentFrame{
		Kind:              router.IntentManageTool,
		AllowsSideEffects: true,
		RequiresExternal:  true,
	})

	if hasTool(visible, "mcp-builder") {
		t.Fatal("mcp-builder should not be exposed as a direct tool; it is invoked through skill")
	}
	if !hasTool(visible, "skill") {
		t.Fatal("MCP server creation should keep the skill entrypoint visible")
	}
	if obs.RouteDecision.AllowedToolInputs["skill"]["name"] != "mcp-builder" {
		t.Fatalf("allowed skill name = %#v, want mcp-builder", obs.RouteDecision.AllowedToolInputs)
	}
}

func TestModelVisibleTools_PerTurnRecallRespectsPlanModeGate(t *testing.T) {
	session := &SessionState{
		ID:         "s-plan",
		PlanMode:   true,
		PlanStatus: sessiontodo.PlanStatusPlanning,
	}
	catalog := []mcphost.ToolDefinition{
		{Name: "read_file", Core: true},
		{Name: "tool_search", Core: true},
		{
			Name:        "send_im_message",
			Description: "发送消息到 IM 平台（钉钉/飞书/企业微信/个人微信）",
			InputSchema: []byte(`{
				"type": "object",
				"properties": {
					"platform": {"type": "string", "enum": ["dingtalk", "feishu", "wecom"]},
					"chat_id": {"type": "string", "description": "聊天 ID"},
					"content": {"type": "string", "description": "消息内容"}
				}
			}`),
		},
	}

	visible := visibleToolsForIntent(session, catalog, "发送给飞书用户:郭松", config.DefaultToolRecallConfig(), externalSendIntentForVisibilityTest())
	if hasTool(visible, "send_im_message") {
		t.Fatal("per-turn recall must still respect plan mode execution gate")
	}
}

func TestModelVisibleToolsFromPreparedMessages_UsesLatestUserMessage(t *testing.T) {
	session := &SessionState{ID: "s1"}
	catalog := []mcphost.ToolDefinition{
		{Name: "read_file", Core: true},
		{Name: "tool_search", Core: true},
		{
			Name:        "websearch",
			Description: "网络搜索工具，用于查询天气和公开网页信息",
		},
	}
	messages := []llm.MessageWithTools{
		{Role: "user", Content: llm.NewTextContent("先别查")},
		{Role: "assistant", Content: llm.NewTextContent("好的")},
		{Role: "user", Content: llm.NewTextContent("搜索今天的天气")},
	}

	visible := modelVisibleToolsForPreparedMessages(session, catalog, messages)
	if !hasTool(visible, "websearch") {
		t.Fatal("prepared messages should use the latest user query for per-turn recall")
	}
}

func TestModelVisibleTools_FeishuDomainRecallKeepsFeishuAPIAndUnifiedIM(t *testing.T) {
	session := &SessionState{ID: "s1"}
	catalog := []mcphost.ToolDefinition{
		{Name: "read_file", Core: true},
		{Name: "tool_search", Core: true},
		{
			Name:        "send_im_message",
			Description: "发送消息到 IM 平台（钉钉/飞书/企业微信/个人微信）",
			InputSchema: []byte(`{
				"type": "object",
				"properties": {
					"platform": {"type": "string", "enum": ["dingtalk", "feishu", "wecom"]},
					"chat_id": {"type": "string"},
					"content": {"type": "string"}
				}
			}`),
		},
		{
			Name:        "feishu_api",
			Description: "飞书应用 API 工具。访问飞书文档、通讯录、消息、审批、任务、电子表格、多维表格和资源。",
			InputSchema: []byte(`{
				"type": "object",
				"properties": {
					"action": {
						"type": "string",
						"enum": ["search_contacts", "send_message", "search_docs", "create_task", "read_sheet", "list_approvals"]
					},
					"query": {"type": "string"},
					"chat_id": {"type": "string"},
					"content": {"type": "string"}
				}
			}`),
		},
	}

	visible := visibleToolsForIntent(session, catalog, "发送给飞书用户:郭松", config.DefaultToolRecallConfig(), externalSendIntentForVisibilityTest())
	if !hasTool(visible, "feishu_api") {
		t.Fatal("feishu domain recall should include feishu_api")
	}
	if !hasTool(visible, "send_im_message") {
		t.Fatal("feishu domain recall should keep the unified IM send tool visible")
	}
}

func TestModelVisibleTools_NonFeishuPlatformHintsPruneFeishuAPIAndKeepUnifiedIM(t *testing.T) {
	cases := []struct {
		name     string
		query    string
		platform string
	}{
		{name: "wechatbot", query: "给微信用户发一条消息", platform: "wechatbot"},
		{name: "wecom", query: "给企微用户发一条消息", platform: "wecom"},
		{name: "dingtalk", query: "给钉钉用户发一条消息", platform: "dingtalk"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			session := &SessionState{ID: "s-" + tc.name}
			catalog := imVisibilityCatalogForPlatformTests()
			intent := externalSendIntentForPlatformVisibilityTest(tc.platform)

			visible := visibleToolsForIntent(session, catalog, tc.query, config.DefaultToolRecallConfig(), intent)
			if hasTool(visible, "feishu_api") {
				t.Fatalf("%s send should not expose feishu_api, visible=%v", tc.platform, toolNamesForTest(visible))
			}
			if !hasTool(visible, "send_im_message") {
				t.Fatalf("%s send should expose send_im_message, visible=%v", tc.platform, toolNamesForTest(visible))
			}
			if !hasTool(visible, "im_api") {
				t.Fatalf("%s send should prefer im_api when catalog has it, visible=%v", tc.platform, toolNamesForTest(visible))
			}
		})
	}
}

func TestModelVisibleTools_MultiPlatformExternalSendRequiresQuestion(t *testing.T) {
	session := &SessionState{ID: "s-multi-platform"}
	catalog := append(imVisibilityCatalogForPlatformTests(), mcphost.ToolDefinition{Name: "question", Core: true})
	intent := router.IntentFrame{
		Kind:              router.IntentExternalWrite,
		AllowsSideEffects: true,
		RequiresExternal:  true,
		Signals:           []string{"external_send_multi_platform_requires_question"},
	}

	visible, obs := modelVisibleToolsForSessionWithRecallObservationAndSkillsAndIntent(
		session,
		catalog,
		nil,
		"飞书和微信都发一遍",
		config.DefaultToolRecallConfig(),
		intent,
	)

	for _, name := range []string{"feishu_api", "send_im_message", "im_api"} {
		if hasTool(visible, name) {
			t.Fatalf("multi-platform send must not directly expose %s, visible=%v", name, toolNamesForTest(visible))
		}
		if obs.Entries[name].TaskCallable {
			t.Fatalf("multi-platform send must not mark %s callable: %#v", name, obs.Entries[name])
		}
	}
	if !hasTool(visible, "question") {
		t.Fatalf("multi-platform send should keep question visible, visible=%v", toolNamesForTest(visible))
	}
}

func TestModelVisibleTools_ToolRecallModes(t *testing.T) {
	session := &SessionState{ID: "s1"}
	catalog := []mcphost.ToolDefinition{
		{Name: "read_file", Core: true},
		{Name: "tool_search", Core: true},
		{
			Name:        "send_im_message",
			Description: "发送消息到 IM 平台（钉钉/飞书/企业微信/个人微信）",
			InputSchema: []byte(`{
				"type": "object",
				"properties": {
					"platform": {"type": "string", "enum": ["dingtalk", "feishu", "wecom"]},
					"content": {"type": "string"}
				}
			}`),
		},
	}

	off := config.DefaultToolRecallConfig()
	off.Mode = "off"
	if hasTool(visibleToolsForIntent(session, catalog, "发送给飞书用户:郭松", off, externalSendIntentForVisibilityTest()), "send_im_message") {
		t.Fatal("off mode should not inject recalled tools")
	}

	observe := config.DefaultToolRecallConfig()
	observe.Mode = "observe"
	if hasTool(visibleToolsForIntent(session, catalog, "发送给飞书用户:郭松", observe, externalSendIntentForVisibilityTest()), "send_im_message") {
		t.Fatal("observe mode should recall without changing visible tools")
	}

	inject := config.DefaultToolRecallConfig()
	inject.Mode = "inject"
	if !hasTool(visibleToolsForIntent(session, catalog, "发送给飞书用户:郭松", inject, externalSendIntentForVisibilityTest()), "send_im_message") {
		t.Fatal("inject mode should add recalled tools")
	}
}

func TestToolRecallObservation_LogCandidatesAndPreview(t *testing.T) {
	session := &SessionState{ID: "s1"}
	catalog := []mcphost.ToolDefinition{
		{Name: "read_file", Core: true},
		{Name: "tool_search", Core: true},
		{
			Name:        "send_im_message",
			Description: "发送消息到 IM 平台（钉钉/飞书/企业微信/个人微信）",
			InputSchema: []byte(`{"type":"object","properties":{"platform":{"type":"string","enum":["feishu"]},"content":{"type":"string"}}}`),
		},
	}

	query := strings.Repeat("发送给飞书用户郭松", 10)
	cfg := config.DefaultToolRecallConfig()
	visible, obs := modelVisibleToolsForSessionWithRecallObservationAndSkillsAndIntent(session, catalog, nil, query, cfg, externalSendIntentForVisibilityTest())
	if !hasTool(visible, "send_im_message") {
		t.Fatal("inject mode should expose recalled tool")
	}
	if obs.Mode != "inject" {
		t.Fatalf("mode = %q, want inject", obs.Mode)
	}
	if len([]rune(obs.QueryPreview)) > 80 {
		t.Fatalf("query preview too long: %d", len([]rune(obs.QueryPreview)))
	}
	if obs.CandidateCount == 0 || len(obs.CandidateNames) == 0 || len(obs.CandidateScores) == 0 {
		t.Fatalf("expected candidate details, got %#v", obs)
	}
	if obs.VisibleBeforeCount != 2 || obs.VisibleAfterCount != 3 {
		t.Fatalf("visible counts = %d/%d, want 2/3", obs.VisibleBeforeCount, obs.VisibleAfterCount)
	}
	if obs.SideEffectCandidateCount == 0 {
		t.Fatal("send_im_message should count as side effect candidate")
	}

	cfg.LogCandidates = false
	_, privateObs := modelVisibleToolsForSessionWithRecallObservationAndSkillsAndIntent(session, catalog, nil, query, cfg, externalSendIntentForVisibilityTest())
	if len(privateObs.CandidateNames) != 0 || len(privateObs.CandidateScores) != 0 {
		t.Fatalf("log_candidates=false should hide details, got names=%v scores=%v", privateObs.CandidateNames, privateObs.CandidateScores)
	}
	if privateObs.CandidateCount == 0 {
		t.Fatal("log_candidates=false should keep aggregate candidate count")
	}
}

func TestToolRecallObservation_PlanGateBlockedCandidate(t *testing.T) {
	session := &SessionState{
		ID:         "s-plan",
		PlanMode:   true,
		PlanStatus: sessiontodo.PlanStatusPlanning,
	}
	catalog := []mcphost.ToolDefinition{
		{Name: "read_file", Core: true},
		{Name: "tool_search", Core: true},
		{
			Name:        "send_im_message",
			Description: "发送消息到 IM 平台（钉钉/飞书/企业微信/个人微信）",
			InputSchema: []byte(`{"type":"object","properties":{"platform":{"type":"string","enum":["feishu"]},"content":{"type":"string"}}}`),
		},
	}

	visible, obs := modelVisibleToolsForSessionWithRecallObservationAndSkillsAndIntent(session, catalog, nil, "发送给飞书用户郭松", config.DefaultToolRecallConfig(), externalSendIntentForVisibilityTest())
	if hasTool(visible, "send_im_message") {
		t.Fatal("plan mode should block recalled side-effect tool")
	}
	if !obs.BlockedByPlanGate {
		t.Fatalf("blocked_by_plan_gate = false, want true: %#v", obs)
	}
}

func TestToolRecallObservation_EntriesForExternalSend(t *testing.T) {
	session := &SessionState{ID: "s-admit"}
	catalog := []mcphost.ToolDefinition{
		{Name: "tool_search", Core: true},
		{
			Name:        "feishu_api",
			Description: "飞书应用 API 工具。search_contacts send_message",
			InputSchema: []byte(`{"type":"object","properties":{"action":{"type":"string","enum":["search_contacts","send_message"]}}}`),
		},
	}

	_, obs := modelVisibleToolsForSessionWithRecallObservationAndSkillsAndIntent(
		session,
		catalog,
		nil,
		"发送给飞书用户郭松",
		config.DefaultToolRecallConfig(),
		externalSendIntentForVisibilityTest(),
	)
	entry, ok := obs.Entries["feishu_api"]
	if !ok {
		t.Fatalf("missing feishu_api entry in observation: %#v", obs.Entries)
	}
	if !entry.SurvivedPolicy {
		t.Fatalf("feishu_api came from filtered catalog and should mark SurvivedPolicy: %#v", entry)
	}
	if !entry.VisibleToModel {
		t.Fatalf("feishu_api should be visible after per-turn recall: %#v", entry)
	}
	if !entry.ExecutableByRuntime {
		t.Fatalf("feishu_api should pass runtime plan gate outside plan mode: %#v", entry)
	}
	if !entry.TaskCallable {
		t.Fatalf("feishu_api should be task-callable for external send: %#v", entry)
	}
	if entry.DiscoveryOnly {
		t.Fatalf("feishu_api should not be discovery-only: %#v", entry)
	}
	if entry.MayRequireApproval {
		t.Fatalf("normal feishu send/read actions should not be advertised as blanket approval-required: %#v", entry)
	}
	if !strings.Contains(entry.AllowedInputs["action"], "send_message") || strings.Contains(entry.AllowedInputs["action"], "create_task") {
		t.Fatalf("external-send admission should constrain feishu_api actions, got %#v", entry.AllowedInputs)
	}
	if len(entry.DangerousActions) == 0 {
		t.Fatalf("feishu_api should still advertise dangerous action-level exceptions: %#v", entry)
	}
	if entry.PrimaryBlockReason != "" {
		t.Fatalf("unexpected block reason: %#v", entry)
	}
}

func TestToolRecallObservation_FeishuReadBlocksExternalMixedTool(t *testing.T) {
	session := &SessionState{ID: "s-feishu-read"}
	catalog := []mcphost.ToolDefinition{
		{Name: "tool_search", Core: true},
		{
			Name:        "feishu_api",
			Description: "飞书应用 API 工具。get_doc_content read_sheet send_message create_task",
			InputSchema: []byte(`{"type":"object","properties":{"action":{"type":"string","enum":["get_doc_content","read_sheet","send_message","create_task"]}}}`),
		},
	}

	visible, obs := modelVisibleToolsForSessionWithRecallObservationAndSkillsAndIntent(
		session,
		catalog,
		nil,
		"读取飞书文档内容",
		config.DefaultToolRecallConfig(),
		router.IntentFrame{Kind: router.IntentRead},
	)

	if hasTool(visible, "feishu_api") {
		t.Fatalf("ordinary read intent must not expose external mixed feishu_api, visible=%v", toolNamesForTest(visible))
	}
	entry := obs.Entries["feishu_api"]
	if entry.TaskCallable {
		t.Fatalf("feishu_api must not be task-callable for ordinary read intent: %#v", entry)
	}
	if entry.PrimaryBlockReason != "side effect not allowed by intent" {
		t.Fatalf("block reason = %q, want side effect not allowed by intent", entry.PrimaryBlockReason)
	}
	if len(entry.AllowedInputs) != 0 {
		t.Fatalf("blocked feishu_api must not advertise read actions in admission entry: %#v", entry)
	}
}

func TestToolRecallObservation_FeishuExternalReadAllowsOnlyReadActions(t *testing.T) {
	session := &SessionState{ID: "s-feishu-external-read"}
	catalog := []mcphost.ToolDefinition{
		{Name: "tool_search", Core: true},
		{
			Name:        "feishu_api",
			Description: "飞书应用 API 工具。get_doc_content read_sheet send_message create_task",
			InputSchema: []byte(`{"type":"object","properties":{"action":{"type":"string","enum":["get_doc_content","read_sheet","send_message","create_task"]}}}`),
		},
	}

	visible, obs := modelVisibleToolsForSessionWithRecallObservationAndSkillsAndIntent(
		session,
		catalog,
		nil,
		"读取飞书文档内容",
		config.DefaultToolRecallConfig(),
		router.IntentFrame{Kind: router.IntentExternalRead, RequiresExternal: true},
	)

	if !hasTool(visible, "feishu_api") {
		t.Fatalf("external-read intent should expose mixed feishu_api for read-only actions, visible=%v", toolNamesForTest(visible))
	}
	entry := obs.Entries["feishu_api"]
	if !entry.TaskCallable {
		t.Fatalf("feishu_api should be task-callable for external-read intent: %#v", entry)
	}
	if entry.MayRequireApproval {
		t.Fatalf("feishu_api read admission should not be blanket approval-required: %#v", entry)
	}
	allowedActions := entry.AllowedInputs["action"]
	if !strings.Contains(allowedActions, "get_doc_content") || !strings.Contains(allowedActions, "read_sheet") {
		t.Fatalf("read admission should include read actions, got %q", allowedActions)
	}
	if strings.Contains(allowedActions, "send_message") || strings.Contains(allowedActions, "create_task") {
		t.Fatalf("read admission must not allow write/send actions, got %q", allowedActions)
	}
}

func TestToolRecallObservation_EntriesForToolSearchDiscoveryOnly(t *testing.T) {
	session := &SessionState{ID: "s-admit"}
	catalog := []mcphost.ToolDefinition{{Name: "tool_search", Core: true}}

	_, obs := modelVisibleToolsForSessionWithRecallObservationAndSkillsAndIntent(
		session,
		catalog,
		nil,
		"发送给飞书用户郭松",
		config.DefaultToolRecallConfig(),
		externalSendIntentForVisibilityTest(),
	)
	entry, ok := obs.Entries["tool_search"]
	if !ok {
		t.Fatalf("missing tool_search entry: %#v", obs.Entries)
	}
	if !entry.VisibleToModel || !entry.ExecutableByRuntime {
		t.Fatalf("tool_search should be visible and executable: %#v", entry)
	}
	if entry.TaskCallable {
		t.Fatalf("tool_search must not be task-callable: %#v", entry)
	}
	if !entry.DiscoveryOnly {
		t.Fatalf("tool_search should be discovery-only: %#v", entry)
	}
	if entry.MayRequireApproval {
		t.Fatalf("tool_search should not require approval: %#v", entry)
	}
	if entry.PrimaryBlockReason != "discovery_only" {
		t.Fatalf("block reason = %q, want discovery_only", entry.PrimaryBlockReason)
	}
}

func TestToolRecallObservation_VisibleBeforeCountNoDoubleCount(t *testing.T) {
	session := &SessionState{ID: "s-no-double"}
	catalog := []mcphost.ToolDefinition{
		{Name: "tool_search", Core: true},
		{Name: "websearch", Core: true},
	}

	_, obs := modelVisibleToolsForSessionWithRecallObservationAndSkills(
		session,
		catalog,
		nil,
		"today's weather",
		config.DefaultToolRecallConfig(),
	)
	if obs.VisibleBeforeCount != 2 {
		t.Fatalf("VisibleBeforeCount = %d, want 2", obs.VisibleBeforeCount)
	}
}

func TestScreenshotScenario_SendWeatherToNamedPersonGetsSearchAndFeishuTools(t *testing.T) {
	session := &SessionState{ID: "s-screenshot"}
	catalog := []mcphost.ToolDefinition{
		{Name: "websearch", Core: true, Description: "网络搜索"},
		{Name: "webfetch", Core: true, Description: "网页获取"},
		{Name: "tool_search", Core: true, Description: "搜索工具"},
		{
			Name:        "feishu_api",
			Description: "飞书应用 API 工具。访问飞书文档、通讯录、消息、审批、任务、电子表格、多维表格和资源。",
			InputSchema: []byte(`{
				"type": "object",
				"properties": {
					"action": {
						"type": "string",
						"enum": ["search_contacts", "send_message", "search_docs", "get_doc_content"]
					},
					"query": {"type": "string"},
					"chat_id": {"type": "string"},
					"content": {"type": "string"}
				}
			}`),
		},
	}

	query := "给郭松发一下今天的天气信息"
	visible, obs := modelVisibleToolsForSessionWithRecallObservationAndSkillsAndIntent(session, catalog, nil, query, config.DefaultToolRecallConfig(), externalSendIntentForVisibilityTest())
	if !hasTool(visible, "feishu_api") {
		t.Fatalf("feishu_api should be model-visible, visible=%v", toolNamesForTest(visible))
	}
	if !hasTool(visible, "websearch") {
		t.Fatalf("websearch should remain visible for weather lookup, visible=%v", toolNamesForTest(visible))
	}
	if entry := obs.Entries["feishu_api"]; !entry.VisibleToModel || !entry.TaskCallable {
		t.Fatalf("feishu_api admission entry should be visible and callable: %#v", entry)
	}
}

func TestScreenshotScenario_FeishuNotBlockedByGenericSendTool(t *testing.T) {
	session := &SessionState{ID: "s-screenshot-mixed-send"}
	catalog := []mcphost.ToolDefinition{
		{Name: "websearch", Core: true, Description: "网络搜索"},
		{Name: "tool_search", Core: true, Description: "搜索工具"},
		{
			Name:        "send_im_message",
			Description: "发送消息到 IM 平台（钉钉/飞书/企业微信/个人微信）",
			InputSchema: []byte(`{"type":"object","properties":{"platform":{"type":"string","enum":["feishu"]},"chat_id":{"type":"string"},"content":{"type":"string"}}}`),
		},
		{
			Name:        "feishu_api",
			Description: "飞书应用 API 工具。访问飞书文档、通讯录、消息、审批、任务、电子表格、多维表格和资源。",
			InputSchema: []byte(`{
				"type": "object",
				"properties": {
					"action": {
						"type": "string",
						"enum": ["search_contacts", "send_message"]
					},
					"query": {"type": "string"},
					"chat_id": {"type": "string"},
					"content": {"type": "string"}
				}
			}`),
		},
	}

	visible, obs := modelVisibleToolsForSessionWithRecallObservationAndSkillsAndIntent(
		session,
		catalog,
		nil,
		"发送给飞书用户郭松",
		config.DefaultToolRecallConfig(),
		externalSendIntentForVisibilityTest(),
	)
	if !hasTool(visible, "feishu_api") {
		t.Fatalf("feishu_api must remain visible even when generic send tool is available; visible=%v", toolNamesForTest(visible))
	}
	if entry := obs.Entries["feishu_api"]; !entry.VisibleToModel || !entry.TaskCallable {
		t.Fatalf("feishu_api entry should be visible and task-callable: %#v", entry)
	}
}

func TestToolVisibility_ContinuationExposesFeishuOnlyWithPendingExternalSend(t *testing.T) {
	catalog := []mcphost.ToolDefinition{
		{Name: "tool_search", Core: true},
		{
			Name:        "feishu_api",
			Description: "飞书应用 API 工具。search_contacts send_message",
			InputSchema: []byte(`{"type":"object","properties":{"action":{"type":"string","enum":["search_contacts","send_message"]}}}`),
		},
	}

	noPending := &SessionState{ID: "s-no-pending"}
	noPendingIntent := resolveTurnIntent(noPending, "现在能不能发", router.IntentFrame{Kind: router.IntentAnswer})
	noPendingVisible := visibleToolsForIntent(noPending, catalog, "现在能不能发", config.DefaultToolRecallConfig(), noPendingIntent)
	if hasTool(noPendingVisible, "feishu_api") {
		t.Fatalf("short continuation without pending external-send must not expose feishu_api: %v", toolNamesForTest(noPendingVisible))
	}

	pending := &SessionState{ID: "s-pending"}
	pending.RememberPendingExternalSendIntent(externalSendIntentForVisibilityTest())
	pendingIntent := resolveTurnIntent(pending, "现在能不能发", router.IntentFrame{Kind: router.IntentAnswer})
	pendingVisible := visibleToolsForIntent(pending, catalog, "现在能不能发", config.DefaultToolRecallConfig(), pendingIntent)
	if !hasTool(pendingVisible, "feishu_api") {
		t.Fatalf("short continuation with pending external-send should expose feishu_api: %v", toolNamesForTest(pendingVisible))
	}
}

func TestToolVisibilityWrapperDoesNotRecoverSideEffectIntentFromText(t *testing.T) {
	session := &SessionState{ID: "s-wrapper-side-effect"}
	catalog := []mcphost.ToolDefinition{
		{Name: "tool_search", Core: true},
		{
			Name:        "send_im_message",
			Description: "发送消息到 IM 平台（钉钉/飞书/企业微信/个人微信）",
			InputSchema: []byte(`{"type":"object","properties":{"platform":{"type":"string","enum":["feishu"]},"content":{"type":"string"}}}`),
		},
	}

	visible, obs := modelVisibleToolsForSessionWithRecallObservation(session, catalog, "发送给飞书用户郭松", config.DefaultToolRecallConfig())
	if hasTool(visible, "send_im_message") {
		t.Fatalf("legacy wrapper must not recover side-effect intent from text; visible=%v", toolNamesForTest(visible))
	}
	if obs.RouteDecision.Intent.Kind == router.IntentExternalWrite || obs.RouteDecision.Intent.AllowsSideEffects {
		t.Fatalf("legacy wrapper route intent recovered side-effect intent: %+v", obs.RouteDecision.Intent)
	}
	if entry := obs.Entries["send_im_message"]; entry.TaskCallable {
		t.Fatalf("legacy wrapper should not mark send tool task-callable: %#v", entry)
	}
}

func TestDiscoveredToolNamesFromToolSearchResult(t *testing.T) {
	content := `{"count":2,"results":[{"name":"custom_ext"},{"name":"acme__publish"}]}`

	got := discoveredToolNamesFromToolSearchResult(content)

	if len(got) != 2 || got[0] != "custom_ext" || got[1] != "acme__publish" {
		t.Fatalf("unexpected discovered tools: %#v", got)
	}
}

func TestRecordToolDiscoveryFromToolSearchOnlyOnSuccess(t *testing.T) {
	session := &SessionState{ID: "s1"}

	recordToolDiscoveryFromResult(session, llm.ToolCall{Name: "grep"}, `{"results":[{"name":"custom_ext"}]}`, false)
	if len(session.DiscoveredTools()) != 0 {
		t.Fatal("non tool_search result should not record discovered tools")
	}

	recordToolDiscoveryFromResult(session, llm.ToolCall{Name: "tool_search"}, `{"results":[{"name":"custom_ext"}]}`, true)
	if len(session.DiscoveredTools()) != 0 {
		t.Fatal("errored tool_search result should not record discovered tools")
	}

	recordToolDiscoveryFromResult(session, llm.ToolCall{Name: "tool_search"}, `{"results":[{"name":"custom_ext"}]}`, false)
	if !session.IsToolDiscovered("custom_ext") {
		t.Fatal("successful tool_search result should record discovered tools")
	}
}

func TestToolVisibility_DefaultVisibleMixedToolsGetReadOnlyRuntimeConstraints(t *testing.T) {
	session := &SessionState{ID: "s-mixed-defaults"}
	catalog := []mcphost.ToolDefinition{
		{
			Name:        "memory",
			Description: "管理持久化记忆 save search update delete list",
			InputSchema: []byte(`{"type":"object","properties":{"operation":{"type":"string","enum":["save","search","update","delete","list"]}}}`),
		},
		{Name: "tool_search", Core: true},
		{Name: "browser_interact", Core: true},
	}

	_, obs := modelVisibleToolsForSessionWithRecallObservationAndSkillsAndIntent(
		session,
		catalog,
		nil,
		"读取页面并搜索记忆",
		config.DefaultToolRecallConfig(),
		router.IntentFrame{Kind: router.IntentRead},
	)

	allowed := session.AllowedToolInputsSnapshot()
	memoryOps := allowed["memory"]["operation"]
	if !strings.Contains(memoryOps, "search") || strings.Contains(memoryOps, "save") || strings.Contains(memoryOps, "delete") {
		t.Fatalf("default-visible memory should be constrained to read operations, got %#v", allowed["memory"])
	}
	browserActions := allowed["browser_interact"]["commands[].action"]
	if !strings.Contains(browserActions, "snapshot") || strings.Contains(browserActions, "click") || strings.Contains(browserActions, "eval") {
		t.Fatalf("default-visible browser_interact should be constrained to read actions, got %#v", allowed["browser_interact"])
	}
	if obs.Entries["memory"].AllowedInputs["operation"] != memoryOps {
		t.Fatalf("admission entry and runtime constraints diverged: entry=%#v runtime=%#v", obs.Entries["memory"].AllowedInputs, allowed["memory"])
	}
}

func TestToolVisibility_LocalWriteIntentWidensMemoryOperationConstraints(t *testing.T) {
	session := &SessionState{ID: "s-mixed-local-write"}
	catalog := []mcphost.ToolDefinition{
		{
			Name:        "memory",
			Description: "管理持久化记忆 save search update delete list",
			InputSchema: []byte(`{"type":"object","properties":{"operation":{"type":"string","enum":["save","search","update","delete","list"]}}}`),
		},
		{Name: "tool_search", Core: true},
	}

	visible, obs := modelVisibleToolsForSessionWithRecallObservationAndSkillsAndIntent(
		session,
		catalog,
		nil,
		"保存这条偏好到记忆",
		config.DefaultToolRecallConfig(),
		router.IntentFrame{Kind: router.IntentWriteLocal, AllowsSideEffects: true},
	)

	if !hasTool(visible, "memory") {
		t.Fatalf("memory should remain visible for local write intent: %v", toolNamesForTest(visible))
	}
	memoryOps := session.AllowedToolInputsSnapshot()["memory"]["operation"]
	if !strings.Contains(memoryOps, "save") || !strings.Contains(memoryOps, "update") {
		t.Fatalf("local write intent should allow memory save/update, got %q", memoryOps)
	}
	if strings.Contains(memoryOps, "delete") {
		t.Fatalf("local write intent must not allow memory delete, got %q", memoryOps)
	}
	if obs.Entries["memory"].AllowedInputs["operation"] != memoryOps {
		t.Fatalf("admission entry and runtime constraints diverged: entry=%#v runtime=%#v", obs.Entries["memory"].AllowedInputs, memoryOps)
	}
}

func hasTool(tools []mcphost.ToolDefinition, name string) bool {
	for _, tool := range tools {
		if tool.Name == name {
			return true
		}
	}
	return false
}

func externalSendIntentForVisibilityTest() router.IntentFrame {
	return router.IntentFrame{Kind: router.IntentExternalWrite, AllowsSideEffects: true, RequiresExternal: true}
}

func externalSendIntentForPlatformVisibilityTest(platform string) router.IntentFrame {
	intent := externalSendIntentForVisibilityTest()
	intent.AllowedDomainsHint = []string{platform}
	return intent
}

func imVisibilityCatalogForPlatformTests() []mcphost.ToolDefinition {
	return []mcphost.ToolDefinition{
		{Name: "tool_search", Core: true},
		{
			Name:        "send_im_message",
			Description: "发送消息到 IM 平台（钉钉/飞书/企业微信/个人微信）",
			InputSchema: []byte(`{"type":"object","properties":{"platform":{"type":"string","enum":["dingtalk","feishu","wecom","wechatbot"]},"chat_id":{"type":"string"},"content":{"type":"string"}}}`),
		},
		{
			Name:        "im_api",
			Description: "统一 IM Agent API，支持 search_recipients、list_recent_conversations、resolve_recipient、send_message",
			InputSchema: []byte(`{"type":"object","properties":{"action":{"type":"string","enum":["search_recipients","list_recent_conversations","resolve_recipient","send_message"]},"platform":{"type":"string","enum":["dingtalk","feishu","wecom","wechatbot"]},"content":{"type":"string"}}}`),
		},
		{
			Name:        "feishu_api",
			Description: "飞书应用 API 工具。访问飞书文档、通讯录、消息、审批、任务、电子表格、多维表格和资源。",
			InputSchema: []byte(`{"type":"object","properties":{"action":{"type":"string","enum":["search_contacts","send_message","search_docs"]},"query":{"type":"string"},"chat_id":{"type":"string"},"content":{"type":"string"}}}`),
		},
	}
}

func visibleToolsForIntent(session *SessionState, catalog []mcphost.ToolDefinition, query string, recallCfg config.ToolRecallConfig, intent router.IntentFrame) []mcphost.ToolDefinition {
	visible, _ := modelVisibleToolsForSessionWithRecallObservationAndSkillsAndIntent(session, catalog, nil, query, recallCfg, intent)
	return visible
}

func toolNamesForTest(tools []mcphost.ToolDefinition) []string {
	names := make([]string, 0, len(tools))
	for _, tool := range tools {
		names = append(names, tool.Name)
	}
	return names
}
