package tools

import (
	"context"
	"encoding/json"
	"testing"

	"go.uber.org/zap"

	"github.com/chef-guo/agents-hive/internal/mcphost"
)

func TestRegisterBuiltinToolsRegistersToolSearch(t *testing.T) {
	logger := zap.NewNop()
	host := mcphost.NewHost(logger)

	RegisterBuiltinTools(host, logger, nil, nil, nil, "", nil, nil, nil, nil, nil)

	def, err := host.GetTool("tool_search")
	if err != nil {
		t.Fatalf("tool_search should be registered as builtin read-only tool: %v", err)
	}
	if !def.Core {
		t.Fatal("tool_search should be a default-visible core tool for deferred discovery")
	}
}

func TestToolSearchFindsRegisteredToolsWithoutMutatingRegistry(t *testing.T) {
	logger := zap.NewNop()
	host := mcphost.NewHost(logger)
	host.RegisterTool(mcphost.ToolDefinition{
		Name:              "alpha_read",
		Description:       "读取 alpha 数据",
		IsConcurrencySafe: true,
	}, func(ctx context.Context, input json.RawMessage) (*mcphost.ToolResult, error) {
		t.Fatal("tool_search must not execute matched tools")
		return nil, nil
	})
	host.RegisterTool(mcphost.ToolDefinition{
		Name:        "danger_delete",
		Description: "删除危险数据",
	}, func(ctx context.Context, input json.RawMessage) (*mcphost.ToolResult, error) {
		t.Fatal("tool_search must not execute matched tools")
		return nil, nil
	})
	registerToolSearch(host, logger)
	beforeCount := len(host.ListTools())

	input, _ := json.Marshal(map[string]any{
		"query": "alpha",
	})
	result, err := host.ExecuteTool(context.Background(), "tool_search", input)
	if err != nil {
		t.Fatalf("ExecuteTool(tool_search): %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected tool_search error: %s", result.DecodeContent())
	}
	if afterCount := len(host.ListTools()); afterCount != beforeCount {
		t.Fatalf("tool_search should not mutate registry: before=%d after=%d", beforeCount, afterCount)
	}

	var out struct {
		Count   int `json:"count"`
		Results []struct {
			Name              string  `json:"name"`
			Description       string  `json:"description"`
			DangerLevel       string  `json:"danger_level"`
			RequiresApproval  bool    `json:"requires_approval"`
			IsConcurrencySafe bool    `json:"is_concurrency_safe"`
			Score             float64 `json:"score"`
		} `json:"results"`
	}
	if err := json.Unmarshal([]byte(result.DecodeContent()), &out); err != nil {
		t.Fatalf("decode tool_search output: %v; content=%s", err, result.DecodeContent())
	}
	if out.Count != 1 || len(out.Results) != 1 {
		t.Fatalf("expected one alpha hit, got count=%d results=%d content=%s", out.Count, len(out.Results), result.DecodeContent())
	}
	got := out.Results[0]
	if got.Name != "alpha_read" {
		t.Fatalf("expected alpha_read hit, got %q", got.Name)
	}
	if got.Description == "" {
		t.Fatal("expected description in result")
	}
	if got.DangerLevel == "" {
		t.Fatal("expected danger_level metadata")
	}
	if got.RequiresApproval {
		t.Fatal("read-only safe tool should not require approval")
	}
	if !got.IsConcurrencySafe {
		t.Fatal("expected concurrency-safe metadata")
	}
	if got.Score <= 0 {
		t.Fatalf("expected positive score, got %f", got.Score)
	}
}

func TestToolSearchFindsToolsBySchemaEnumValues(t *testing.T) {
	logger := zap.NewNop()
	host := mcphost.NewHost(logger)
	host.RegisterTool(mcphost.ToolDefinition{
		Name:        "schema_only_send",
		Description: "internal router",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"platform": {
					"type": "string",
					"enum": ["feishu", "dingtalk"]
				},
				"chat_id": {
					"type": "string"
				},
				"content": {
					"type": "string"
				}
			},
			"required": ["platform", "chat_id", "content"]
		}`),
	}, func(ctx context.Context, input json.RawMessage) (*mcphost.ToolResult, error) {
		t.Fatal("tool_search must not execute matched tools")
		return nil, nil
	})
	registerToolSearch(host, logger)

	input, _ := json.Marshal(map[string]any{
		"query": "feishu",
	})
	result, err := host.ExecuteTool(context.Background(), "tool_search", input)
	if err != nil {
		t.Fatalf("ExecuteTool(tool_search): %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected tool_search error: %s", result.DecodeContent())
	}

	var out struct {
		Count   int `json:"count"`
		Results []struct {
			Name string `json:"name"`
		} `json:"results"`
	}
	if err := json.Unmarshal([]byte(result.DecodeContent()), &out); err != nil {
		t.Fatalf("decode tool_search output: %v; content=%s", err, result.DecodeContent())
	}
	if out.Count == 0 || len(out.Results) == 0 {
		t.Fatalf("expected schema enum hit, got count=%d content=%s", out.Count, result.DecodeContent())
	}
	if out.Results[0].Name != "schema_only_send" {
		t.Fatalf("expected schema_only_send as top hit, got %q content=%s", out.Results[0].Name, result.DecodeContent())
	}
}

func TestToolSearchFindsToolsBySchemaFieldName(t *testing.T) {
	logger := zap.NewNop()
	host := mcphost.NewHost(logger)
	host.RegisterTool(mcphost.ToolDefinition{
		Name:        "schema_only_notify",
		Description: "internal router",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"chat_id": {
					"type": "string",
					"description": "目标聊天 ID"
				},
				"content": {
					"type": "string",
					"description": "消息内容"
				}
			},
			"required": ["chat_id", "content"]
		}`),
	}, func(ctx context.Context, input json.RawMessage) (*mcphost.ToolResult, error) {
		t.Fatal("tool_search must not execute matched tools")
		return nil, nil
	})
	registerToolSearch(host, logger)

	input, _ := json.Marshal(map[string]any{
		"query": "chat id",
	})
	result, err := host.ExecuteTool(context.Background(), "tool_search", input)
	if err != nil {
		t.Fatalf("ExecuteTool(tool_search): %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected tool_search error: %s", result.DecodeContent())
	}

	var out struct {
		Count   int `json:"count"`
		Results []struct {
			Name string `json:"name"`
		} `json:"results"`
	}
	if err := json.Unmarshal([]byte(result.DecodeContent()), &out); err != nil {
		t.Fatalf("decode tool_search output: %v; content=%s", err, result.DecodeContent())
	}
	if out.Count == 0 || len(out.Results) == 0 {
		t.Fatalf("expected schema field hit, got count=%d content=%s", out.Count, result.DecodeContent())
	}
	if out.Results[0].Name != "schema_only_notify" {
		t.Fatalf("expected schema_only_notify as top hit, got %q content=%s", out.Results[0].Name, result.DecodeContent())
	}
}

func TestToolSearchFindsToolsByNaturalLanguageQueryAndSchemaTerms(t *testing.T) {
	logger := zap.NewNop()
	host := mcphost.NewHost(logger)
	host.RegisterTool(mcphost.ToolDefinition{
		Name:        "schema_only_send",
		Description: "internal router",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"platform": {
					"type": "string",
					"enum": ["feishu", "dingtalk"]
				},
				"chat_id": {
					"type": "string"
				},
				"content": {
					"type": "string"
				}
			},
			"required": ["platform", "chat_id", "content"]
		}`),
	}, func(ctx context.Context, input json.RawMessage) (*mcphost.ToolResult, error) {
		t.Fatal("tool_search must not execute matched tools")
		return nil, nil
	})
	registerToolSearch(host, logger)

	input, _ := json.Marshal(map[string]any{
		"query": "发送给飞书用户:郭松",
	})
	result, err := host.ExecuteTool(context.Background(), "tool_search", input)
	if err != nil {
		t.Fatalf("ExecuteTool(tool_search): %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected tool_search error: %s", result.DecodeContent())
	}

	var out struct {
		Count   int `json:"count"`
		Results []struct {
			Name string `json:"name"`
		} `json:"results"`
	}
	if err := json.Unmarshal([]byte(result.DecodeContent()), &out); err != nil {
		t.Fatalf("decode tool_search output: %v; content=%s", err, result.DecodeContent())
	}
	if out.Count == 0 || len(out.Results) == 0 {
		t.Fatalf("expected natural-language schema hit, got count=%d content=%s", out.Count, result.DecodeContent())
	}
	if out.Results[0].Name != "schema_only_send" {
		t.Fatalf("expected schema_only_send as top hit, got %q content=%s", out.Results[0].Name, result.DecodeContent())
	}
}

func TestToolSearchPrefersFeishuAPIForFeishuDomainTasks(t *testing.T) {
	logger := zap.NewNop()
	cases := []struct {
		name  string
		query string
	}{
		{name: "send message", query: "发送给飞书用户:郭松"},
		{name: "contact", query: "查一下飞书联系人郭松"},
		{name: "document", query: "搜索飞书文档里的预算方案"},
		{name: "task", query: "在飞书创建一个任务"},
		{name: "sheet", query: "读取飞书电子表格 A1:C10"},
		{name: "approval", query: "查一下飞书审批状态"},
		{name: "wiki", query: "读取飞书 wiki 节点内容"},
		{name: "bitable", query: "查询飞书多维表格记录"},
		{name: "resource", query: "下载飞书消息里的图片文件"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			host := mcphost.NewHost(logger)
			RegisterSendIMMessage(host, logger, &MockIMRouter{})
			RegisterFeishuTools(host, logger, &mockFeishuProvider{}, nil)
			registerToolSearch(host, logger)

			input, _ := json.Marshal(map[string]any{
				"query": tc.query,
				"limit": 3,
			})
			result, err := host.ExecuteTool(context.Background(), "tool_search", input)
			if err != nil {
				t.Fatalf("ExecuteTool(tool_search): %v", err)
			}
			if result.IsError {
				t.Fatalf("unexpected tool_search error: %s", result.DecodeContent())
			}

			var out struct {
				Count   int `json:"count"`
				Results []struct {
					Name  string  `json:"name"`
					Score float64 `json:"score"`
				} `json:"results"`
			}
			if err := json.Unmarshal([]byte(result.DecodeContent()), &out); err != nil {
				t.Fatalf("decode tool_search output: %v; content=%s", err, result.DecodeContent())
			}
			if out.Count == 0 || len(out.Results) == 0 {
				t.Fatalf("expected feishu_api hit for %q, got content=%s", tc.query, result.DecodeContent())
			}
			if out.Results[0].Name != "feishu_api" {
				t.Fatalf("expected feishu_api as top hit for %q, got %q content=%s", tc.query, out.Results[0].Name, result.DecodeContent())
			}
		})
	}
}
