package llm

import (
	"strings"
	"testing"

	"github.com/chef-guo/agents-hive/internal/mcphost"
)

func TestConvertToolsForChatCompletionsSortsByName(t *testing.T) {
	tools, err := convertToolsForChatCompletions([]mcphost.ToolDefinition{
		{Name: "zeta", Description: "z", InputSchema: []byte(`{"type":"object"}`)},
		{Name: "alpha", Description: "a", InputSchema: []byte(`{"type":"object"}`)},
		{Name: "middle", Description: "m", InputSchema: []byte(`{"type":"object"}`)},
	})
	if err != nil {
		t.Fatalf("convertToolsForChatCompletions returned error: %v", err)
	}
	got := []string{tools[0].Function.Name, tools[1].Function.Name, tools[2].Function.Name}
	want := []string{"alpha", "middle", "zeta"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("tool order = %v, want %v", got, want)
		}
	}
}

func TestConvertToolsForResponsesSortsByName(t *testing.T) {
	tools, err := convertToolsForResponses([]mcphost.ToolDefinition{
		{Name: "zeta", Description: "z", InputSchema: []byte(`{"type":"object"}`)},
		{Name: "alpha", Description: "a", InputSchema: []byte(`{"type":"object"}`)},
		{Name: "middle", Description: "m", InputSchema: []byte(`{"type":"object"}`)},
	})
	if err != nil {
		t.Fatalf("convertToolsForResponses returned error: %v", err)
	}
	got := []string{
		tools[0].OfFunction.Name,
		tools[1].OfFunction.Name,
		tools[2].OfFunction.Name,
	}
	want := []string{"alpha", "middle", "zeta"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("tool order = %v, want %v", got, want)
		}
	}
}

func TestConvertToolsForResponsesRejectsInvalidSchema(t *testing.T) {
	_, err := convertToolsForResponses([]mcphost.ToolDefinition{
		{Name: "bad_schema", InputSchema: []byte(`{"type":`)},
	})
	if err == nil {
		t.Fatal("convertToolsForResponses should reject invalid input schema")
	}
	if !strings.Contains(err.Error(), "bad_schema") {
		t.Fatalf("error should include tool name, got %v", err)
	}
}
