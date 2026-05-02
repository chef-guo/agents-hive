package master

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/chef-guo/agents-hive/internal/llm"
	"github.com/chef-guo/agents-hive/internal/mcphost"
)

var defaultModelVisibleTools = map[string]bool{
	"batch":             true,
	"ls":                true,
	"memory":            true,
	"parallel_dispatch": true,
	"question":          true,
	"skill":             true,
	"task":              true,
	"tool_search":       true,
}

// modelVisibleToolsForSession 收窄模型默认候选集：核心工具和质量杠杆工具默认可见，
// 其他扩展/MCP/自定义工具需要先通过 tool_search 发现。
func modelVisibleToolsForSession(session *SessionState, catalog []mcphost.ToolDefinition) []mcphost.ToolDefinition {
	if len(catalog) == 0 {
		return nil
	}
	out := make([]mcphost.ToolDefinition, 0, len(catalog))
	for _, tool := range catalog {
		if decision := EvaluatePlanToolGate(context.Background(), session, tool.Name); !decision.Allowed {
			continue
		}
		if tool.Core || defaultModelVisibleTools[tool.Name] || (session != nil && session.IsToolDiscovered(tool.Name)) {
			out = append(out, tool)
		}
	}
	return out
}

func recordToolDiscoveryFromResult(session *SessionState, toolCall llm.ToolCall, content string, isError bool) {
	if session == nil || isError || toolCall.Name != "tool_search" {
		return
	}
	session.RecordDiscoveredTools(discoveredToolNamesFromToolSearchResult(content))
}

func discoveredToolNamesFromToolSearchResult(content string) []string {
	content = strings.TrimSpace(content)
	if content == "" {
		return nil
	}
	var payload struct {
		Results []struct {
			Name string `json:"name"`
		} `json:"results"`
	}
	if err := json.Unmarshal([]byte(content), &payload); err != nil {
		return nil
	}
	names := make([]string, 0, len(payload.Results))
	seen := make(map[string]bool, len(payload.Results))
	for _, result := range payload.Results {
		name := strings.TrimSpace(result.Name)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		names = append(names, name)
	}
	return names
}
