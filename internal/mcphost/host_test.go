package mcphost

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"go.uber.org/zap"

	"github.com/chef-guo/agents-hive/internal/errs"
)

func TestHost_RegisterAndExecute(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	host := NewHost(logger)

	host.RegisterTool(
		ToolDefinition{Name: "echo", Description: "echoes input"},
		func(ctx context.Context, input json.RawMessage) (*ToolResult, error) {
			return &ToolResult{Content: input}, nil
		},
	)

	input := json.RawMessage(`{"msg":"hello"}`)
	result, err := host.ExecuteTool(context.Background(), "echo", input)
	if err != nil {
		t.Fatalf("ExecuteTool error: %v", err)
	}
	if string(result.Content) != string(input) {
		t.Errorf("expected %s, got %s", input, result.Content)
	}
}

func TestHost_ToolNotFound(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	host := NewHost(logger)

	_, err := host.ExecuteTool(context.Background(), "missing", nil)
	if !errs.IsCode(err, errs.CodeMCPToolNotFound) {
		t.Errorf("expected CodeMCPToolNotFound, got %v", err)
	}
}

func TestHost_ListTools(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	host := NewHost(logger)

	host.RegisterTool(ToolDefinition{Name: "a"}, nil)
	host.RegisterTool(ToolDefinition{Name: "b"}, nil)

	tools := host.ListTools()
	if len(tools) != 2 {
		t.Fatalf("expected 2 tools, got %d", len(tools))
	}
}

func TestHost_UnregisterTool(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	host := NewHost(logger)

	host.RegisterTool(ToolDefinition{Name: "x"}, nil)
	if err := host.UnregisterTool("x"); err != nil {
		t.Fatalf("UnregisterTool error: %v", err)
	}
	if len(host.ListTools()) != 0 {
		t.Error("expected 0 tools after unregister")
	}
}

func TestToolSet_Basic(t *testing.T) {
	ts := NewToolSet("test-set")
	if ts.Name() != "test-set" {
		t.Errorf("expected name test-set, got %s", ts.Name())
	}

	ts.Add(ToolDefinition{Name: "a", Description: "tool a"})
	ts.Add(ToolDefinition{Name: "b", Description: "tool b"})

	if ts.Count() != 2 {
		t.Fatalf("expected 2, got %d", ts.Count())
	}

	got, ok := ts.Get("a")
	if !ok {
		t.Fatal("expected to find tool a")
	}
	if got.Description != "tool a" {
		t.Errorf("expected tool a description, got %s", got.Description)
	}

	ts.Remove("a")
	if ts.Count() != 1 {
		t.Errorf("expected 1 after remove, got %d", ts.Count())
	}
}

func TestHost_GetTool_Success(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	host := NewHost(logger)

	host.RegisterTool(
		ToolDefinition{
			Name:         "fetch",
			Description:  "fetches data",
			InputSchema:  json.RawMessage(`{"type":"object"}`),
			OutputSchema: json.RawMessage(`{"type":"object","required":["data"]}`),
		},
		func(ctx context.Context, input json.RawMessage) (*ToolResult, error) {
			return &ToolResult{Content: input}, nil
		},
	)

	def, err := host.GetTool("fetch")
	if err != nil {
		t.Fatalf("GetTool error: %v", err)
	}
	if def.Name != "fetch" {
		t.Errorf("expected name 'fetch', got %q", def.Name)
	}
	if def.Description != "fetches data" {
		t.Errorf("expected description 'fetches data', got %q", def.Description)
	}
	if string(def.InputSchema) != `{"type":"object"}` {
		t.Errorf("expected InputSchema %q, got %q", `{"type":"object"}`, string(def.InputSchema))
	}
	if string(def.OutputSchema) != `{"type":"object","required":["data"]}` {
		t.Errorf("expected OutputSchema %q, got %q", `{"type":"object","required":["data"]}`, string(def.OutputSchema))
	}
}

func TestHost_GetTool_NotFound(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	host := NewHost(logger)

	_, err := host.GetTool("nonexistent")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errs.IsCode(err, errs.CodeMCPToolNotFound) {
		t.Errorf("expected CodeMCPToolNotFound, got %v", err)
	}
}

func TestHost_UnregisterNotFound(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	host := NewHost(logger)

	err := host.UnregisterTool("nonexistent")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errs.IsCode(err, errs.CodeMCPToolNotFound) {
		t.Errorf("expected CodeMCPToolNotFound, got %v", err)
	}
}

func TestHost_ExecuteToolError(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	host := NewHost(logger)

	host.RegisterTool(
		ToolDefinition{Name: "failingtool", Description: "always fails"},
		func(ctx context.Context, input json.RawMessage) (*ToolResult, error) {
			return nil, fmt.Errorf("disk full")
		},
	)

	_, err := host.ExecuteTool(context.Background(), "failingtool", json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errs.IsCode(err, errs.CodeMCPToolExecFailed) {
		t.Errorf("expected CodeMCPToolExecFailed, got %v", err)
	}
}

func TestToolSet_ToJSON(t *testing.T) {
	ts := NewToolSet("json-set")
	ts.Add(ToolDefinition{Name: "alpha", Description: "first tool"})
	ts.Add(ToolDefinition{Name: "beta", Description: "second tool"})

	data, err := ts.ToJSON()
	if err != nil {
		t.Fatalf("ToJSON error: %v", err)
	}

	var parsed map[string]json.RawMessage
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("invalid JSON returned: %v", err)
	}
	if len(parsed) != 2 {
		t.Errorf("expected 2 entries in JSON, got %d", len(parsed))
	}
	if _, ok := parsed["alpha"]; !ok {
		t.Error("expected 'alpha' key in JSON output")
	}
	if _, ok := parsed["beta"]; !ok {
		t.Error("expected 'beta' key in JSON output")
	}
}

func TestToolSet_GetNotFound(t *testing.T) {
	ts := NewToolSet("empty-set")

	_, ok := ts.Get("nonexistent")
	if ok {
		t.Error("expected ok=false for non-existent tool, got true")
	}
}

func TestToolSet_List(t *testing.T) {
	ts := NewToolSet("list-set")
	ts.Add(ToolDefinition{Name: "tool1", Description: "first"})
	ts.Add(ToolDefinition{Name: "tool2", Description: "second"})
	ts.Add(ToolDefinition{Name: "tool3", Description: "third"})

	tools := ts.List()
	if len(tools) != 3 {
		t.Fatalf("expected 3 tools, got %d", len(tools))
	}

	names := make(map[string]bool)
	for _, tool := range tools {
		names[tool.Name] = true
	}
	for _, expected := range []string{"tool1", "tool2", "tool3"} {
		if !names[expected] {
			t.Errorf("expected tool %q in list, not found", expected)
		}
	}
}

func TestDecodeToolContent(t *testing.T) {
	tests := []struct {
		name string
		raw  json.RawMessage
		want string
	}{
		{
			name: "JSON 编码字符串",
			raw:  json.RawMessage(`"hello world"`),
			want: "hello world",
		},
		{
			name: "JSON 编码字符串含转义",
			raw:  json.RawMessage(`"line1\nline2"`),
			want: "line1\nline2",
		},
		{
			name: "MCP 格式单文本",
			raw:  json.RawMessage(`[{"type":"text","text":"file content here"}]`),
			want: "file content here",
		},
		{
			name: "MCP 格式含特殊字符",
			raw:  json.RawMessage(`[{"type":"text","text":"错误: 文件不存在\n路径: /tmp/test"}]`),
			want: "错误: 文件不存在\n路径: /tmp/test",
		},
		{
			name: "MCP 格式多项取第一个 text",
			raw:  json.RawMessage(`[{"type":"image","data":"base64..."},{"type":"text","text":"caption"}]`),
			want: "caption",
		},
		{
			name: "原始字符串降级",
			raw:  json.RawMessage(`not valid json`),
			want: "not valid json",
		},
		{
			name: "空内容",
			raw:  json.RawMessage(``),
			want: "",
		},
		{
			name: "nil 内容",
			raw:  nil,
			want: "",
		},
		{
			name: "JSON 编码空字符串",
			raw:  json.RawMessage(`""`),
			want: "",
		},
		{
			name: "JSON 编码含引号字符串",
			raw:  json.RawMessage(`"he said \"hello\""`),
			want: `he said "hello"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DecodeToolContent(tt.raw)
			if got != tt.want {
				t.Errorf("DecodeToolContent() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestToolResult_DecodeContent(t *testing.T) {
	t.Run("nil ToolResult", func(t *testing.T) {
		var r *ToolResult
		if got := r.DecodeContent(); got != "" {
			t.Errorf("nil ToolResult.DecodeContent() = %q, want empty", got)
		}
	})

	t.Run("JSON 字符串", func(t *testing.T) {
		r := &ToolResult{Content: json.RawMessage(`"test content"`)}
		if got := r.DecodeContent(); got != "test content" {
			t.Errorf("DecodeContent() = %q, want %q", got, "test content")
		}
	})

	t.Run("MCP 格式", func(t *testing.T) {
		r := &ToolResult{Content: json.RawMessage(`[{"type":"text","text":"mcp result"}]`)}
		if got := r.DecodeContent(); got != "mcp result" {
			t.Errorf("DecodeContent() = %q, want %q", got, "mcp result")
		}
	})

	t.Run("空 Content", func(t *testing.T) {
		r := &ToolResult{Content: nil}
		if got := r.DecodeContent(); got != "" {
			t.Errorf("DecodeContent() = %q, want empty", got)
		}
	})
}
