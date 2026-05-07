package mcphost

import (
	"encoding/json"
	"testing"
)

func TestMCPToOpenAI(t *testing.T) {
	mcpTools := []ToolDefinition{
		{
			Name:         "read_file",
			Description:  "Read a file",
			InputSchema:  json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}}}`),
			OutputSchema: json.RawMessage(`{"type":"object","properties":{"content":{"type":"string"}}}`),
		},
	}

	result := MCPToOpenAI(mcpTools)
	if len(result) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(result))
	}
	if result[0].Type != "function" {
		t.Errorf("expected type function, got %s", result[0].Type)
	}
	if result[0].Function.Name != "read_file" {
		t.Errorf("expected name read_file, got %s", result[0].Function.Name)
	}
	if result[0].Function.Description != "Read a file" {
		t.Errorf("expected description 'Read a file', got %s", result[0].Function.Description)
	}
	if string(result[0].Function.Parameters) != `{"type":"object","properties":{"path":{"type":"string"}}}` {
		t.Errorf("expected parameters to preserve input schema, got %s", result[0].Function.Parameters)
	}
}

func TestOpenAIToMCP(t *testing.T) {
	openAITools := []OpenAITool{
		{
			Type: "function",
			Function: OpenAIFunction{
				Name:        "search",
				Description: "Search the web",
				Parameters:  json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"}}}`),
			},
		},
	}

	result := OpenAIToMCP(openAITools)
	if len(result) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(result))
	}
	if result[0].Name != "search" {
		t.Errorf("expected name search, got %s", result[0].Name)
	}
	if result[0].Description != "Search the web" {
		t.Errorf("expected description 'Search the web', got %s", result[0].Description)
	}
	if string(result[0].InputSchema) != `{"type":"object","properties":{"query":{"type":"string"}}}` {
		t.Errorf("expected input schema from OpenAI parameters, got %s", result[0].InputSchema)
	}
	if len(result[0].OutputSchema) != 0 {
		t.Errorf("expected no output schema from OpenAI conversion, got %s", result[0].OutputSchema)
	}
}

func TestConvertTools_MCPToOpenAI(t *testing.T) {
	mcpTools := []ToolDefinition{
		{
			Name:         "read_file",
			Description:  "Read a file",
			InputSchema:  json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}}}`),
			OutputSchema: json.RawMessage(`{"type":"object","properties":{"content":{"type":"string"}}}`),
		},
	}
	toolsJSON, _ := json.Marshal(mcpTools)

	result, err := ConvertTools("mcp_to_openai", toolsJSON)
	if err != nil {
		t.Fatalf("ConvertTools returned error: %v", err)
	}

	var openAITools []OpenAITool
	if err := json.Unmarshal(result, &openAITools); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if len(openAITools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(openAITools))
	}
	if openAITools[0].Function.Name != "read_file" {
		t.Errorf("expected name read_file, got %s", openAITools[0].Function.Name)
	}
	if string(openAITools[0].Function.Parameters) != `{"type":"object","properties":{"path":{"type":"string"}}}` {
		t.Errorf("expected parameters to preserve input schema, got %s", openAITools[0].Function.Parameters)
	}
}

func TestConvertTools_OpenAIToMCP(t *testing.T) {
	openAITools := []OpenAITool{
		{
			Type: "function",
			Function: OpenAIFunction{
				Name:        "search",
				Description: "Search the web",
				Parameters:  json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"}}}`),
			},
		},
	}
	toolsJSON, _ := json.Marshal(openAITools)

	result, err := ConvertTools("openai_to_mcp", toolsJSON)
	if err != nil {
		t.Fatalf("ConvertTools returned error: %v", err)
	}

	var mcpTools []ToolDefinition
	if err := json.Unmarshal(result, &mcpTools); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if len(mcpTools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(mcpTools))
	}
	if mcpTools[0].Name != "search" {
		t.Errorf("expected name search, got %s", mcpTools[0].Name)
	}
	if string(mcpTools[0].InputSchema) != `{"type":"object","properties":{"query":{"type":"string"}}}` {
		t.Errorf("expected input schema from OpenAI parameters, got %s", mcpTools[0].InputSchema)
	}
	if len(mcpTools[0].OutputSchema) != 0 {
		t.Errorf("expected no output schema from OpenAI tools, got %s", mcpTools[0].OutputSchema)
	}
}

func TestConvertTools_MCPRoundTripPreservesOutputSchema(t *testing.T) {
	mcpTools := []ToolDefinition{
		{
			Name:         "summarize",
			Description:  "Summarize text",
			InputSchema:  json.RawMessage(`{"type":"object","properties":{"text":{"type":"string"}}}`),
			OutputSchema: json.RawMessage(`{"type":"object","required":["summary"]}`),
		},
	}

	encoded, err := json.Marshal(mcpTools)
	if err != nil {
		t.Fatalf("marshal mcp tools: %v", err)
	}

	var decoded []ToolDefinition
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshal mcp tools: %v", err)
	}

	if len(decoded) != 1 {
		t.Fatalf("expected 1 tool after round trip, got %d", len(decoded))
	}
	if string(decoded[0].OutputSchema) != `{"type":"object","required":["summary"]}` {
		t.Fatalf("expected output schema to survive MCP round trip, got %s", decoded[0].OutputSchema)
	}
}

func TestConvertTools_InvalidDirection(t *testing.T) {
	_, err := ConvertTools("invalid", json.RawMessage(`[]`))
	if err == nil {
		t.Fatal("expected error for invalid direction")
	}
}

func TestConvertTools_InvalidInput(t *testing.T) {
	_, err := ConvertTools("mcp_to_openai", json.RawMessage(`{invalid`))
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}
