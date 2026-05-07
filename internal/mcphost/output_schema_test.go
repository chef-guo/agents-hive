package mcphost

import (
	"encoding/json"
	"testing"
)

func TestValidateToolOutput(t *testing.T) {
	t.Run("空 schema 跳过校验", func(t *testing.T) {
		diag, err := ValidateToolOutput(nil, json.RawMessage(`not-json`))
		if err != nil {
			t.Fatalf("expected nil error, got %v", err)
		}
		if diag != nil {
			t.Fatalf("expected nil diagnostic, got %+v", diag)
		}
	})

	t.Run("匹配 required keys 时成功", func(t *testing.T) {
		schema := json.RawMessage(`{"type":"object","required":["summary","score"]}`)
		content := json.RawMessage(`{"summary":"ok","score":0.9}`)

		diag, err := ValidateToolOutput(schema, content)
		if err != nil {
			t.Fatalf("expected nil error, got %v", err)
		}
		if diag != nil {
			t.Fatalf("expected nil diagnostic, got %+v", diag)
		}
	})

	t.Run("缺少 required keys 生成诊断", func(t *testing.T) {
		schema := json.RawMessage(`{"type":"object","required":["summary","score"]}`)
		content := json.RawMessage(`{"summary":"ok"}`)

		diag, err := ValidateToolOutput(schema, content)
		if err != nil {
			t.Fatalf("expected nil error, got %v", err)
		}
		if diag == nil {
			t.Fatal("expected diagnostic for schema mismatch")
		}
		if diag.Keyword != "required" {
			t.Fatalf("expected keyword required, got %q", diag.Keyword)
		}
		if diag.Message == "" {
			t.Fatal("expected mismatch message")
		}
	})

	t.Run("非 JSON 输出生成诊断但不返回 error", func(t *testing.T) {
		schema := json.RawMessage(`{"type":"object","required":["summary"]}`)
		content := json.RawMessage(`not-json`)

		diag, err := ValidateToolOutput(schema, content)
		if err != nil {
			t.Fatalf("expected nil error, got %v", err)
		}
		if diag == nil {
			t.Fatal("expected diagnostic for invalid json")
		}
		if diag.Keyword != "json_parse" {
			t.Fatalf("expected json_parse keyword, got %q", diag.Keyword)
		}
	})
}

func TestValidateToolResultDoesNotConvertSuccessToFailure(t *testing.T) {
	def := ToolDefinition{
		Name:         "summarize",
		OutputSchema: json.RawMessage(`{"type":"object","required":["summary"]}`),
	}
	result := &ToolResult{Content: json.RawMessage(`{"score":1}`)}

	diag, err := ValidateToolResult(def, result)
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if diag == nil {
		t.Fatal("expected diagnostic for schema mismatch")
	}
	if result.IsError {
		t.Fatal("schema mismatch should not convert successful result into error")
	}
}
