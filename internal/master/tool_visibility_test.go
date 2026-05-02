package master

import (
	"testing"

	"github.com/chef-guo/agents-hive/internal/llm"
	"github.com/chef-guo/agents-hive/internal/mcphost"
	"github.com/chef-guo/agents-hive/internal/sessiontodo"
)

func TestModelVisibleTools_DefaultsHideExtensionsUntilDiscovered(t *testing.T) {
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

	session.RecordDiscoveredTools([]string{"custom_ext", "acme__publish"})
	afterDiscovery := modelVisibleToolsForSession(session, catalog)
	if !hasTool(afterDiscovery, "custom_ext") {
		t.Fatal("discovered extension tool should become model-visible")
	}
	if !hasTool(afterDiscovery, "acme__publish") {
		t.Fatal("discovered external MCP tool should become model-visible")
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
	}
	for _, name := range []string{"write_file", "bash", "taskboard", "send_im_message"} {
		if hasTool(visible, name) {
			t.Fatalf("plan mode should hide write/control tool %q", name)
		}
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

func hasTool(tools []mcphost.ToolDefinition, name string) bool {
	for _, tool := range tools {
		if tool.Name == name {
			return true
		}
	}
	return false
}
