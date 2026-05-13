package tools

import (
	"context"
	"encoding/json"
	"strings"
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
			Kind              string  `json:"kind"`
			Domain            string  `json:"domain"`
			Source            string  `json:"source"`
			Invocation        string  `json:"invocation"`
			RouteStatus       string  `json:"route_status"`
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
	if got.Kind != "custom_tool" {
		t.Fatalf("kind = %q, want custom_tool", got.Kind)
	}
	if got.Source != "custom_dir" {
		t.Fatalf("source = %q, want custom_dir", got.Source)
	}
	if got.Invocation != "direct_tool" {
		t.Fatalf("invocation = %q, want direct_tool", got.Invocation)
	}
	if got.RouteStatus != "callable_read_only" {
		t.Fatalf("route_status = %q, want callable_read_only", got.RouteStatus)
	}
	if got.Score <= 0 {
		t.Fatalf("expected positive score, got %f", got.Score)
	}
}

func TestToolSearchExposesExternalMCPRiskMetadata(t *testing.T) {
	logger := zap.NewNop()
	host := mcphost.NewHost(logger)
	host.RegisterTool(mcphost.ToolDefinition{
		Name:        "github__create_issue",
		Description: "Create a GitHub issue",
	}, func(ctx context.Context, input json.RawMessage) (*mcphost.ToolResult, error) {
		t.Fatal("tool_search must not execute matched tools")
		return nil, nil
	})
	registerToolSearch(host, logger)

	result, err := host.ExecuteTool(context.Background(), "tool_search", []byte(`{"query":"github"}`))
	if err != nil {
		t.Fatalf("ExecuteTool(tool_search): %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected tool_search error: %s", result.DecodeContent())
	}

	var out struct {
		Results []struct {
			Name             string `json:"name"`
			DangerLevel      string `json:"danger_level"`
			RequiresApproval bool   `json:"requires_approval"`
			Kind             string `json:"kind"`
			Domain           string `json:"domain"`
			Source           string `json:"source"`
			Invocation       string `json:"invocation"`
		} `json:"results"`
	}
	if err := json.Unmarshal([]byte(result.DecodeContent()), &out); err != nil {
		t.Fatalf("decode tool_search output: %v; content=%s", err, result.DecodeContent())
	}
	if len(out.Results) == 0 {
		t.Fatalf("expected github__create_issue in results, got content=%s", result.DecodeContent())
	}
	got := out.Results[0]
	if got.Name != "github__create_issue" {
		t.Fatalf("expected github__create_issue top hit, got %q", got.Name)
	}
	if got.DangerLevel != "dangerous" {
		t.Fatalf("danger_level = %q, want dangerous", got.DangerLevel)
	}
	if !got.RequiresApproval {
		t.Fatal("external MCP tool should require approval metadata")
	}
	if got.Kind != "mcp_tool" || got.Domain != "github" || got.Source != "mcp_server" || got.Invocation != "direct_tool" {
		t.Fatalf("unexpected typed metadata: %+v", got)
	}
}

func TestToolSearchExposesTrustedMCPReadOnlyMetadata(t *testing.T) {
	logger := zap.NewNop()
	host := mcphost.NewHost(logger)
	host.RegisterTool(mcphost.ToolDefinition{
		Name:         "metamcp__query_prometheus",
		Description:  "Query Prometheus metrics",
		SourceServer: "metamcp",
		Trusted:      true,
	}, func(ctx context.Context, input json.RawMessage) (*mcphost.ToolResult, error) {
		t.Fatal("tool_search must not execute matched tools")
		return nil, nil
	})
	registerToolSearch(host, logger)

	result, err := host.ExecuteTool(context.Background(), "tool_search", []byte(`{"query":"prometheus"}`))
	if err != nil {
		t.Fatalf("ExecuteTool(tool_search): %v", err)
	}

	var out struct {
		Results []struct {
			Name             string `json:"name"`
			DangerLevel      string `json:"danger_level"`
			RequiresApproval bool   `json:"requires_approval"`
			Kind             string `json:"kind"`
			Domain           string `json:"domain"`
			Source           string `json:"source"`
			Invocation       string `json:"invocation"`
		} `json:"results"`
	}
	if err := json.Unmarshal([]byte(result.DecodeContent()), &out); err != nil {
		t.Fatalf("decode tool_search output: %v; content=%s", err, result.DecodeContent())
	}
	if len(out.Results) == 0 {
		t.Fatalf("expected metamcp tool in results, got content=%s", result.DecodeContent())
	}
	got := out.Results[0]
	if got.Name != "metamcp__query_prometheus" {
		t.Fatalf("expected metamcp__query_prometheus top hit, got %q", got.Name)
	}
	if got.DangerLevel != "read_only" || got.RequiresApproval {
		t.Fatalf("trusted MCP read tool metadata wrong: %+v", got)
	}
	if got.Kind != "mcp_tool" || got.Domain != "metamcp" || got.Source != "mcp_server" || got.Invocation != "direct_tool" {
		t.Fatalf("unexpected typed metadata: %+v", got)
	}
}

func TestToolSearchResultsAreDiscoveryOnly(t *testing.T) {
	logger := zap.NewNop()
	host := mcphost.NewHost(logger)
	registerToolSearch(host, logger)
	host.RegisterTool(mcphost.ToolDefinition{
		Name:        "feishu_api",
		Description: "飞书应用 API 工具。search_contacts send_message",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"action":{"type":"string","enum":["search_contacts","send_message"]}}}`),
	}, func(context.Context, json.RawMessage) (*mcphost.ToolResult, error) {
		t.Fatal("tool_search must not execute matched tools")
		return nil, nil
	})

	result, err := host.ExecuteTool(context.Background(), "tool_search", json.RawMessage(`{"query":"飞书","limit":1}`))
	if err != nil {
		t.Fatalf("ExecuteTool(tool_search): %v", err)
	}

	var out struct {
		Results []struct {
			Name          string `json:"name"`
			RouteStatus   string `json:"route_status"`
			CallableNow   bool   `json:"callable_now"`
			ExecutionNote string `json:"execution_note"`
		} `json:"results"`
	}
	if err := json.Unmarshal([]byte(result.DecodeContent()), &out); err != nil {
		t.Fatalf("decode tool_search output: %v; content=%s", err, result.DecodeContent())
	}
	if len(out.Results) != 1 || out.Results[0].Name != "feishu_api" {
		t.Fatalf("expected one feishu_api result, got %#v", out.Results)
	}
	if out.Results[0].RouteStatus != "callable_with_action_constraints" {
		t.Fatalf("route_status = %q, want callable_with_action_constraints", out.Results[0].RouteStatus)
	}
	if !out.Results[0].CallableNow {
		t.Fatal("mixed read-capable tool should report callable_now for constrained read actions")
	}
	if !strings.Contains(out.Results[0].ExecutionNote, "只读动作") {
		t.Fatalf("execution_note should explain action constraints, got %q", out.Results[0].ExecutionNote)
	}
}

func TestToolSearchFeishuApprovalMetadataIsActionAware(t *testing.T) {
	logger := zap.NewNop()
	host := mcphost.NewHost(logger)
	RegisterFeishuTools(host, logger, &mockFeishuProvider{}, nil)
	registerToolSearch(host, logger)

	result, err := host.ExecuteTool(context.Background(), "tool_search", json.RawMessage(`{"query":"飞书","limit":1}`))
	if err != nil {
		t.Fatalf("ExecuteTool(tool_search): %v", err)
	}

	var out struct {
		Results []struct {
			Name                string   `json:"name"`
			DangerLevel         string   `json:"danger_level"`
			RequiresApproval    bool     `json:"requires_approval"`
			DangerousActions    []string `json:"dangerous_actions"`
			ActionField         string   `json:"action_field"`
			ReadOnlyActions     []string `json:"read_only_actions"`
			LocalWriteActions   []string `json:"local_write_actions"`
			ExternalSendActions []string `json:"external_send_actions"`
		} `json:"results"`
	}
	if err := json.Unmarshal([]byte(result.DecodeContent()), &out); err != nil {
		t.Fatalf("decode tool_search output: %v; content=%s", err, result.DecodeContent())
	}
	if len(out.Results) != 1 || out.Results[0].Name != "feishu_api" {
		t.Fatalf("expected one feishu_api result, got %#v", out.Results)
	}
	got := out.Results[0]
	if got.DangerLevel != "mixed" {
		t.Fatalf("danger_level = %q, want mixed", got.DangerLevel)
	}
	if got.RequiresApproval {
		t.Fatalf("feishu_api should not be blanket approval-required: %+v", got)
	}
	if got.ActionField != "action" {
		t.Fatalf("action_field = %q, want action", got.ActionField)
	}
	if !containsStringForToolTest(got.DangerousActions, "create_task") || !containsStringForToolTest(got.ReadOnlyActions, "get_doc_content") || !containsStringForToolTest(got.ExternalSendActions, "send_message") {
		t.Fatalf("missing action-aware metadata: %+v", got)
	}
}

func TestToolSearchMixedOperationApprovalMetadataIsActionAware(t *testing.T) {
	logger := zap.NewNop()
	host := mcphost.NewHost(logger)
	registerMemory(host, logger, nil)
	registerToolSearch(host, logger)

	result, err := host.ExecuteTool(context.Background(), "tool_search", json.RawMessage(`{"query":"memory","limit":1}`))
	if err != nil {
		t.Fatalf("ExecuteTool(tool_search): %v", err)
	}

	var out struct {
		Results []struct {
			Name              string   `json:"name"`
			DangerLevel       string   `json:"danger_level"`
			RequiresApproval  bool     `json:"requires_approval"`
			DangerousActions  []string `json:"dangerous_actions"`
			ActionField       string   `json:"action_field"`
			ReadOnlyActions   []string `json:"read_only_actions"`
			LocalWriteActions []string `json:"local_write_actions"`
		} `json:"results"`
	}
	if err := json.Unmarshal([]byte(result.DecodeContent()), &out); err != nil {
		t.Fatalf("decode tool_search output: %v; content=%s", err, result.DecodeContent())
	}
	if len(out.Results) != 1 || out.Results[0].Name != "memory" {
		t.Fatalf("expected one memory result, got %#v", out.Results)
	}
	got := out.Results[0]
	if got.DangerLevel != "mixed" || got.RequiresApproval {
		t.Fatalf("memory should be mixed without blanket approval, got %+v", got)
	}
	if got.ActionField != "operation" {
		t.Fatalf("action_field = %q, want operation", got.ActionField)
	}
	if !containsStringForToolTest(got.ReadOnlyActions, "search") || !containsStringForToolTest(got.LocalWriteActions, "save") || !containsStringForToolTest(got.DangerousActions, "delete") {
		t.Fatalf("missing operation-aware metadata: %+v", got)
	}
}

func TestToolSearchTypedMetadataPhase0Kinds(t *testing.T) {
	logger := zap.NewNop()
	host := mcphost.NewHost(logger)
	registerToolSearch(host, logger)
	host.RegisterTool(mcphost.ToolDefinition{
		Name:        "mcp-builder",
		Description: "Build high-quality MCP servers as a skill workflow",
	}, func(ctx context.Context, input json.RawMessage) (*mcphost.ToolResult, error) {
		t.Fatal("tool_search must not execute matched tools")
		return nil, nil
	})
	host.RegisterTool(mcphost.ToolDefinition{
		Name:        "github__create_issue",
		Description: "[github] Create an issue",
	}, func(ctx context.Context, input json.RawMessage) (*mcphost.ToolResult, error) {
		t.Fatal("tool_search must not execute matched tools")
		return nil, nil
	})
	host.RegisterTool(mcphost.ToolDefinition{
		Name:        "project_status",
		Description: "查询项目状态",
	}, func(ctx context.Context, input json.RawMessage) (*mcphost.ToolResult, error) {
		t.Fatal("tool_search must not execute matched tools")
		return nil, nil
	})
	host.RegisterTool(mcphost.ToolDefinition{
		Name: "opaque_candidate",
	}, func(ctx context.Context, input json.RawMessage) (*mcphost.ToolResult, error) {
		t.Fatal("tool_search must not execute matched tools")
		return nil, nil
	})

	result, err := host.ExecuteTool(context.Background(), "tool_search", []byte(`{}`))
	if err != nil {
		t.Fatalf("ExecuteTool(tool_search): %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected tool_search error: %s", result.DecodeContent())
	}

	var out struct {
		Results []struct {
			Name        string `json:"name"`
			Kind        string `json:"kind"`
			Domain      string `json:"domain"`
			Source      string `json:"source"`
			Invocation  string `json:"invocation"`
			RouteStatus string `json:"route_status"`
		} `json:"results"`
	}
	if err := json.Unmarshal([]byte(result.DecodeContent()), &out); err != nil {
		t.Fatalf("decode tool_search output: %v; content=%s", err, result.DecodeContent())
	}

	byName := map[string]struct {
		Kind        string
		Domain      string
		Source      string
		Invocation  string
		RouteStatus string
	}{}
	for _, hit := range out.Results {
		byName[hit.Name] = struct {
			Kind        string
			Domain      string
			Source      string
			Invocation  string
			RouteStatus string
		}{hit.Kind, hit.Domain, hit.Source, hit.Invocation, hit.RouteStatus}
	}

	assertToolSearchMeta(t, byName, "tool_search", "builtin_tool", "discovery", "builtin", "discovery_only", "discovery_only")
	assertToolSearchMeta(t, byName, "mcp-builder", "skill_workflow", "mcp_server_building", "local_skill", "skill_tool", "requires_matching_intent")
	assertToolSearchMeta(t, byName, "github__create_issue", "mcp_tool", "github", "mcp_server", "direct_tool", "blocked_dangerous")
	assertToolSearchMeta(t, byName, "project_status", "custom_tool", "custom", "custom_dir", "direct_tool", "blocked_dangerous")
	assertToolSearchMeta(t, byName, "opaque_candidate", "unknown", "unknown", "unknown", "discovery_only", "discovery_only")
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

func assertToolSearchMeta(t *testing.T, got map[string]struct {
	Kind        string
	Domain      string
	Source      string
	Invocation  string
	RouteStatus string
}, name, kind, domain, source, invocation, routeStatus string) {
	t.Helper()
	meta, ok := got[name]
	if !ok {
		t.Fatalf("missing hit %q", name)
	}
	if meta.Kind != kind {
		t.Fatalf("%s kind = %q, want %q", name, meta.Kind, kind)
	}
	if meta.Domain != domain {
		t.Fatalf("%s domain = %q, want %q", name, meta.Domain, domain)
	}
	if meta.Source != source {
		t.Fatalf("%s source = %q, want %q", name, meta.Source, source)
	}
	if meta.Invocation != invocation {
		t.Fatalf("%s invocation = %q, want %q", name, meta.Invocation, invocation)
	}
	if meta.RouteStatus != routeStatus {
		t.Fatalf("%s route_status = %q, want %q", name, meta.RouteStatus, routeStatus)
	}
}

func containsStringForToolTest(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}
