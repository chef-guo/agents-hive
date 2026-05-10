package skills

import (
	"encoding/json"
	"testing"

	"github.com/chef-guo/agents-hive/internal/errs"
	"github.com/chef-guo/agents-hive/internal/mcphost"
)

func TestToolFilter_IsAllowed(t *testing.T) {
	tests := []struct {
		name     string
		allowed  []string
		tool     string
		expected bool
	}{
		{name: "empty filter allows all", allowed: nil, tool: "anything", expected: true},
		{name: "allowed tool", allowed: []string{"tool-a", "tool-b"}, tool: "tool-a", expected: true},
		{name: "blocked tool", allowed: []string{"tool-a", "tool-b"}, tool: "tool-c", expected: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := NewToolFilter(tt.allowed)
			if got := f.IsAllowed(tt.tool); got != tt.expected {
				t.Errorf("IsAllowed(%q) = %v, want %v", tt.tool, got, tt.expected)
			}
		})
	}
}

func TestToolFilter_IsEmpty(t *testing.T) {
	if !NewToolFilter(nil).IsEmpty() {
		t.Error("nil list should be empty")
	}
	if !NewToolFilter([]string{}).IsEmpty() {
		t.Error("empty list should be empty")
	}
	if NewToolFilter([]string{"tool-a"}).IsEmpty() {
		t.Error("non-empty list should not be empty")
	}
}

func TestToolFilter_CheckAllowed(t *testing.T) {
	f := NewToolFilter([]string{"tool-a"})

	if err := f.CheckAllowed("tool-a"); err != nil {
		t.Errorf("tool-a should be allowed: %v", err)
	}

	err := f.CheckAllowed("tool-b")
	if err == nil {
		t.Fatal("tool-b should be blocked")
	}
	if !errs.IsCode(err, errs.CodeSkillToolBlocked) {
		t.Errorf("expected CodeSkillToolBlocked, got %v", err)
	}
}

func TestToolFilter_CheckAllowed_EmptyFilter(t *testing.T) {
	f := NewToolFilter(nil)
	if err := f.CheckAllowed("any-tool"); err != nil {
		t.Errorf("empty filter should allow all: %v", err)
	}
}

func TestToolFilter_FilterTools(t *testing.T) {
	tools := []mcphost.ToolDefinition{
		{Name: "tool-a", Description: "Tool A", InputSchema: json.RawMessage(`{}`)},
		{Name: "tool-b", Description: "Tool B", InputSchema: json.RawMessage(`{}`)},
		{Name: "tool-c", Description: "Tool C", InputSchema: json.RawMessage(`{}`)},
	}

	t.Run("with restrictions", func(t *testing.T) {
		f := NewToolFilter([]string{"tool-a", "tool-c"})
		filtered := f.FilterTools(tools)
		if len(filtered) != 2 {
			t.Fatalf("expected 2 tools, got %d", len(filtered))
		}
		names := map[string]bool{}
		for _, td := range filtered {
			names[td.Name] = true
		}
		if !names["tool-a"] || !names["tool-c"] {
			t.Errorf("expected tool-a and tool-c, got %v", names)
		}
	})

	t.Run("no restrictions", func(t *testing.T) {
		f := NewToolFilter(nil)
		filtered := f.FilterTools(tools)
		if len(filtered) != 3 {
			t.Fatalf("expected 3 tools, got %d", len(filtered))
		}
	})
}

func TestToolFilter_ExternalMCPToolRequiresExplicitAllow(t *testing.T) {
	f := NewToolFilter([]string{"read_file", "edit"})

	// 内部工具：不在 allow list 中应被拒绝
	if f.IsAllowed("bash") {
		t.Error("bash 不在 allow list 中，应被拒绝")
	}

	for _, name := range []string{"wenyan__search", "wenyan__translate", "custom__tool"} {
		if f.IsAllowed(name) {
			t.Errorf("%s 是外部动态工具，不应绕过 allow list", name)
		}
	}

	// 外部 MCP 工具在 deny list 中应被拒绝（deny 优先）
	fd := NewToolFilterWithDeny([]string{"read_file"}, []string{"wenyan__search"})
	if fd.IsAllowed("wenyan__search") {
		t.Error("wenyan__search 在 deny list 中，应被拒绝")
	}
	if fd.IsAllowed("wenyan__translate") {
		t.Error("wenyan__translate 不在 allow list 中，应被拒绝")
	}
	explicit := NewToolFilter([]string{"read_file", "wenyan__translate"})
	if !explicit.IsAllowed("wenyan__translate") {
		t.Error("显式 allow list 中的外部 MCP 工具应被允许")
	}

	// 空 allow list 时外部 MCP 工具也应放行（无限制模式）
	fe := NewToolFilter(nil)
	if !fe.IsAllowed("wenyan__search") {
		t.Error("空 filter 应允许所有工具包括外部 MCP")
	}
}

func TestToolFilter_FilterTools_ExternalMCP(t *testing.T) {
	tools := []mcphost.ToolDefinition{
		{Name: "read_file", Description: "Read", InputSchema: json.RawMessage(`{}`)},
		{Name: "bash", Description: "Bash", InputSchema: json.RawMessage(`{}`)},
		{Name: "wenyan__search", Description: "Wenyan Search", InputSchema: json.RawMessage(`{}`)},
	}

	f := NewToolFilter([]string{"read_file"})
	filtered := f.FilterTools(tools)
	if len(filtered) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(filtered))
	}
	names := map[string]bool{}
	for _, td := range filtered {
		names[td.Name] = true
	}
	if !names["read_file"] {
		t.Error("read_file 应在过滤结果中")
	}
	if names["wenyan__search"] {
		t.Error("wenyan__search 不应绕过 allow list")
	}
	if names["bash"] {
		t.Error("bash 不应在过滤结果中")
	}
}
