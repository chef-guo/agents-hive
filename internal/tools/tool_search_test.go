package tools

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"go.uber.org/zap"

	"github.com/chef-guo/agents-hive/internal/mcphost"
	"github.com/chef-guo/agents-hive/internal/router"
	"github.com/chef-guo/agents-hive/internal/toolruntime"
)

type toolSearchParameterHintForTest struct {
	Name        string   `json:"name"`
	Type        string   `json:"type"`
	Required    bool     `json:"required"`
	Source      string   `json:"source"`
	Format      string   `json:"format"`
	Description string   `json:"description"`
	Enum        []string `json:"enum"`
}

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
			Name               string  `json:"name"`
			Description        string  `json:"description"`
			DangerLevel        string  `json:"danger_level"`
			RequiresApproval   bool    `json:"requires_approval"`
			MayRequireApproval bool    `json:"may_require_approval"`
			IsConcurrencySafe  bool    `json:"is_concurrency_safe"`
			Kind               string  `json:"kind"`
			Domain             string  `json:"domain"`
			Source             string  `json:"source"`
			Risk               string  `json:"risk"`
			Visibility         string  `json:"visibility"`
			Invocation         string  `json:"invocation"`
			RouteStatus        string  `json:"route_status"`
			Score              float64 `json:"score"`
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
	if got.MayRequireApproval {
		t.Fatal("read-only safe tool should not be marked as possibly requiring approval")
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
	if got.Risk != "read_only" {
		t.Fatalf("risk = %q, want read_only", got.Risk)
	}
	if got.Visibility != "system" {
		t.Fatalf("visibility = %q, want system", got.Visibility)
	}
	if got.Invocation != "direct_tool" {
		t.Fatalf("invocation = %q, want direct_tool", got.Invocation)
	}
	if got.RouteStatus != "discovery_only" {
		t.Fatalf("route_status = %q, want discovery_only", got.RouteStatus)
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
			Name               string `json:"name"`
			DangerLevel        string `json:"danger_level"`
			RequiresApproval   bool   `json:"requires_approval"`
			MayRequireApproval bool   `json:"may_require_approval"`
			Kind               string `json:"kind"`
			Domain             string `json:"domain"`
			Source             string `json:"source"`
			Risk               string `json:"risk"`
			Visibility         string `json:"visibility"`
			Invocation         string `json:"invocation"`
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
	if got.RequiresApproval {
		t.Fatal("blocked external MCP tool should not imply current approval can make it callable")
	}
	if !got.MayRequireApproval {
		t.Fatal("external MCP tool should expose may_require_approval metadata")
	}
	if got.Kind != "mcp_tool" || got.Domain != "github" || got.Source != "mcp_server" || got.Invocation != "direct_tool" {
		t.Fatalf("unexpected typed metadata: %+v", got)
	}
	if got.Risk != "destructive" || got.Visibility != "workspace" {
		t.Fatalf("unexpected risk/visibility metadata: %+v", got)
	}
}

func TestToolSearchExposesTrustedRemoteReadOnlyMetadata(t *testing.T) {
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
			Name               string `json:"name"`
			DangerLevel        string `json:"danger_level"`
			RequiresApproval   bool   `json:"requires_approval"`
			MayRequireApproval bool   `json:"may_require_approval"`
			Kind               string `json:"kind"`
			Domain             string `json:"domain"`
			Source             string `json:"source"`
			Risk               string `json:"risk"`
			Visibility         string `json:"visibility"`
			Invocation         string `json:"invocation"`
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
		t.Fatalf("trusted remote read tool metadata wrong: %+v", got)
	}
	if got.MayRequireApproval {
		t.Fatalf("trusted remote read tool should not expose may_require_approval: %+v", got)
	}
	if got.Kind != "mcp_tool" || got.Domain != "metamcp" || got.Source != "mcp_server" || got.Invocation != "direct_tool" {
		t.Fatalf("unexpected typed metadata: %+v", got)
	}
	if got.Risk != "read_only" || got.Visibility != "workspace" {
		t.Fatalf("unexpected risk/visibility metadata: %+v", got)
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
	if out.Results[0].RouteStatus != "discovery_only" {
		t.Fatalf("route_status = %q, want discovery_only", out.Results[0].RouteStatus)
	}
	if out.Results[0].CallableNow {
		t.Fatal("tool_search result must not claim the matched tool is callable now")
	}
	if !strings.Contains(out.Results[0].ExecutionNote, "不授权执行") || !strings.Contains(out.Results[0].ExecutionNote, "RouteDecision") {
		t.Fatalf("execution_note should explain discovery is not authorization, got %q", out.Results[0].ExecutionNote)
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
			Name                 string   `json:"name"`
			DangerLevel          string   `json:"danger_level"`
			RequiresApproval     bool     `json:"requires_approval"`
			MayRequireApproval   bool     `json:"may_require_approval"`
			DangerousActions     []string `json:"dangerous_actions"`
			ActionField          string   `json:"action_field"`
			ReadOnlyActions      []string `json:"read_only_actions"`
			LocalWriteActions    []string `json:"local_write_actions"`
			ExternalSendActions  []string `json:"external_send_actions"`
			ExternalWriteActions []string `json:"external_write_actions"`
			ActionCapabilities   []struct {
				ToolName           string                           `json:"tool_name"`
				Action             string                           `json:"action"`
				ActionField        string                           `json:"action_field"`
				CapabilityID       string                           `json:"capability_id"`
				RequiredFields     []string                         `json:"required_fields"`
				ParameterHints     []toolSearchParameterHintForTest `json:"parameter_hints"`
				PreparatoryActions []string                         `json:"preparatory_actions"`
				ExampleArgs        map[string]any                   `json:"example_args"`
				ExampleToolCall    struct {
					Name      string         `json:"name"`
					Arguments map[string]any `json:"arguments"`
				} `json:"example_tool_call"`
				InvocationHint string `json:"invocation_hint"`
				RepairHint     string `json:"repair_hint"`
			} `json:"action_capabilities"`
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
	if !got.MayRequireApproval {
		t.Fatalf("feishu_api should expose possible approval for write/send branches: %+v", got)
	}
	if got.ActionField != "action" {
		t.Fatalf("action_field = %q, want action", got.ActionField)
	}
	if !containsStringForToolTest(got.DangerousActions, "create_task") || !containsStringForToolTest(got.ReadOnlyActions, "get_doc_content") || !containsStringForToolTest(got.ExternalSendActions, "send_message") {
		t.Fatalf("missing action-aware metadata: %+v", got)
	}
	if !containsStringForToolTest(got.ExternalWriteActions, "create_task") || !containsStringForToolTest(got.ExternalWriteActions, "write_sheet") {
		t.Fatalf("missing external write metadata: %+v", got)
	}
	var createTask *struct {
		ToolName           string                           `json:"tool_name"`
		Action             string                           `json:"action"`
		ActionField        string                           `json:"action_field"`
		CapabilityID       string                           `json:"capability_id"`
		RequiredFields     []string                         `json:"required_fields"`
		ParameterHints     []toolSearchParameterHintForTest `json:"parameter_hints"`
		PreparatoryActions []string                         `json:"preparatory_actions"`
		ExampleArgs        map[string]any                   `json:"example_args"`
		ExampleToolCall    struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		} `json:"example_tool_call"`
		InvocationHint string `json:"invocation_hint"`
		RepairHint     string `json:"repair_hint"`
	}
	for i := range got.ActionCapabilities {
		if got.ActionCapabilities[i].Action == "create_task" {
			createTask = &got.ActionCapabilities[i]
			break
		}
	}
	if createTask == nil {
		t.Fatalf("tool_search should expose create_task action capability hints: %+v", got.ActionCapabilities)
	}
	if createTask.ToolName != "feishu_api" || createTask.ActionField != "action" {
		t.Fatalf("create_task should declare concrete tool/action field, got %+v", createTask)
	}
	if createTask.CapabilityID != router.ActionCapabilityExternalTaskCreate {
		t.Fatalf("create_task capability = %q", createTask.CapabilityID)
	}
	if !containsStringForToolTest(createTask.RequiredFields, "summary") {
		t.Fatalf("create_task required fields = %+v", createTask.RequiredFields)
	}
	if !hasParameterHintForToolTest(createTask.ParameterHints, "summary", true, "user_text") {
		t.Fatalf("create_task should expose required summary parameter hint from user_text: %+v", createTask.ParameterHints)
	}
	if !hasParameterHintForToolTest(createTask.ParameterHints, "due_time", false, "user_text") {
		t.Fatalf("create_task should expose optional due_time parameter hint from user_text: %+v", createTask.ParameterHints)
	}
	if createTask.ExampleArgs["action"] != "create_task" || createTask.ExampleArgs["summary"] == "" {
		t.Fatalf("create_task example args missing executable hint: %+v", createTask.ExampleArgs)
	}
	if createTask.ExampleToolCall.Name != "feishu_api" {
		t.Fatalf("create_task example tool call should use feishu_api, got %+v", createTask.ExampleToolCall)
	}
	if createTask.ExampleToolCall.Arguments["action"] != "create_task" || createTask.ExampleToolCall.Arguments["summary"] == "" {
		t.Fatalf("create_task example tool call should set arguments.action, got %+v", createTask.ExampleToolCall)
	}
	if !strings.Contains(createTask.InvocationHint, "调用工具 feishu_api") || !strings.Contains(createTask.InvocationHint, "arguments.action") || !strings.Contains(createTask.InvocationHint, "不是独立工具名") {
		t.Fatalf("create_task invocation hint should prevent feishu_api.create_task confusion, got %q", createTask.InvocationHint)
	}
	if createTask.RepairHint == "" {
		t.Fatal("create_task repair hint should be present")
	}
}

func hasParameterHintForToolTest(hints []toolSearchParameterHintForTest, name string, required bool, source string) bool {
	for _, hint := range hints {
		if hint.Name == name && hint.Required == required && hint.Source == source {
			return true
		}
	}
	return false
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
			Name               string   `json:"name"`
			DangerLevel        string   `json:"danger_level"`
			RequiresApproval   bool     `json:"requires_approval"`
			MayRequireApproval bool     `json:"may_require_approval"`
			DangerousActions   []string `json:"dangerous_actions"`
			ActionField        string   `json:"action_field"`
			ReadOnlyActions    []string `json:"read_only_actions"`
			LocalWriteActions  []string `json:"local_write_actions"`
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
	if !got.MayRequireApproval {
		t.Fatalf("memory should expose possible approval for write/delete branches: %+v", got)
	}
	if got.ActionField != "operation" {
		t.Fatalf("action_field = %q, want operation", got.ActionField)
	}
	if !containsStringForToolTest(got.ReadOnlyActions, "search") || !containsStringForToolTest(got.LocalWriteActions, "save") || !containsStringForToolTest(got.DangerousActions, "delete") {
		t.Fatalf("missing operation-aware metadata: %+v", got)
	}
}

func TestToolSearchUsesUnifiedPolicyForCustomAndBuiltinTools(t *testing.T) {
	logger := zap.NewNop()
	host := mcphost.NewHost(logger)
	registerToolSearch(host, logger)
	host.RegisterTool(mcphost.ToolDefinition{Name: "read_file", Core: true, Description: "读取文件"}, nil)
	host.RegisterTool(mcphost.ToolDefinition{Name: "project_status", Description: "查询项目状态", IsConcurrencySafe: true}, nil)
	host.RegisterTool(mcphost.ToolDefinition{Name: "opaque_candidate", Description: "opaque extension"}, nil)

	result, err := host.ExecuteTool(context.Background(), "tool_search", []byte(`{"query":"","limit":10}`))
	if err != nil {
		t.Fatalf("ExecuteTool(tool_search): %v", err)
	}

	var out struct {
		Results []struct {
			Name               string `json:"name"`
			RouteStatus        string `json:"route_status"`
			CallableNow        bool   `json:"callable_now"`
			RequiresApproval   bool   `json:"requires_approval"`
			MayRequireApproval bool   `json:"may_require_approval"`
		} `json:"results"`
	}
	if err := json.Unmarshal([]byte(result.DecodeContent()), &out); err != nil {
		t.Fatalf("decode tool_search output: %v; content=%s", err, result.DecodeContent())
	}

	byName := map[string]struct {
		RouteStatus        string
		CallableNow        bool
		RequiresApproval   bool
		MayRequireApproval bool
	}{}
	for _, hit := range out.Results {
		byName[hit.Name] = struct {
			RouteStatus        string
			CallableNow        bool
			RequiresApproval   bool
			MayRequireApproval bool
		}{hit.RouteStatus, hit.CallableNow, hit.RequiresApproval, hit.MayRequireApproval}
	}

	if got := byName["read_file"]; got.RouteStatus != "discovery_only" || got.CallableNow || got.RequiresApproval || got.MayRequireApproval {
		t.Fatalf("read_file metadata = %+v", got)
	}
	if got := byName["project_status"]; got.RouteStatus != "discovery_only" || got.CallableNow || got.RequiresApproval || got.MayRequireApproval {
		t.Fatalf("project_status metadata = %+v", got)
	}
	if got := byName["opaque_candidate"]; got.RouteStatus != "discovery_only" || got.CallableNow || got.RequiresApproval || !got.MayRequireApproval {
		t.Fatalf("opaque_candidate metadata = %+v", got)
	}
}

func TestToolSearchDoesNotChangeRouteDecisionAllowedTools(t *testing.T) {
	logger := zap.NewNop()
	host := mcphost.NewHost(logger)
	registerToolSearch(host, logger)
	host.RegisterTool(mcphost.ToolDefinition{Name: "read_file", Core: true, Description: "读取文件"}, nil)
	host.RegisterTool(mcphost.ToolDefinition{Name: "danger_delete", Description: "delete production data"}, nil)

	profiles := toolSearchProfilesForTest(host.ListTools())
	before := router.BuildRouteDecision(router.IntentFrame{Kind: router.IntentRead}, profiles)
	if !reflect.DeepEqual(before.AllowedTools, []string{"read_file"}) {
		t.Fatalf("unexpected baseline AllowedTools = %+v", before.AllowedTools)
	}

	result, err := host.ExecuteTool(context.Background(), "tool_search", []byte(`{"query":"delete","limit":5}`))
	if err != nil {
		t.Fatalf("ExecuteTool(tool_search): %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected tool_search error: %s", result.DecodeContent())
	}

	after := router.BuildRouteDecision(router.IntentFrame{Kind: router.IntentRead}, toolSearchProfilesForTest(host.ListTools()))
	if !reflect.DeepEqual(after.AllowedTools, before.AllowedTools) {
		t.Fatalf("tool_search must not change AllowedTools: before=%+v after=%+v content=%s", before.AllowedTools, after.AllowedTools, result.DecodeContent())
	}
	if !reflect.DeepEqual(after.VisibleOnly, before.VisibleOnly) {
		t.Fatalf("tool_search must not change VisibleOnly discovery tools: before=%+v after=%+v", before.VisibleOnly, after.VisibleOnly)
	}
	if after.Mode != before.Mode || after.Reason != before.Reason {
		t.Fatalf("tool_search changed route decision: before=%+v after=%+v", before, after)
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
			Risk        string `json:"risk"`
			Visibility  string `json:"visibility"`
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
		Risk        string
		Visibility  string
		Invocation  string
		RouteStatus string
	}{}
	for _, hit := range out.Results {
		byName[hit.Name] = struct {
			Kind        string
			Domain      string
			Source      string
			Risk        string
			Visibility  string
			Invocation  string
			RouteStatus string
		}{hit.Kind, hit.Domain, hit.Source, hit.Risk, hit.Visibility, hit.Invocation, hit.RouteStatus}
	}

	assertToolSearchMeta(t, byName, "tool_search", "builtin_tool", "discovery", "builtin", "read_only", "system", "discovery_only", "discovery_only")
	assertToolSearchMeta(t, byName, "mcp-builder", "skill_workflow", "mcp_server_building", "local_skill", "local_write", "system", "skill_tool", "discovery_only")
	assertToolSearchMeta(t, byName, "github__create_issue", "mcp_tool", "github", "mcp_server", "destructive", "workspace", "direct_tool", "discovery_only")
	assertToolSearchMeta(t, byName, "project_status", "custom_tool", "custom", "custom_dir", "unknown", "system", "direct_tool", "discovery_only")
	assertToolSearchMeta(t, byName, "opaque_candidate", "unknown", "unknown", "unknown", "unknown", "system", "discovery_only", "discovery_only")
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

func TestToolSearchKnowledgeBaseQueryPrefersKBTools(t *testing.T) {
	logger := zap.NewNop()
	host := mcphost.NewHost(logger)
	registerKBTools(host, logger, nil)
	registerMemory(host, logger, nil)
	registerToolSearch(host, logger)

	result, err := host.ExecuteTool(context.Background(), "tool_search", json.RawMessage(`{"query":"列出当前知识库","limit":3}`))
	if err != nil {
		t.Fatalf("ExecuteTool(tool_search): %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected tool_search error: %s", result.DecodeContent())
	}

	var out struct {
		Results []struct {
			Name string `json:"name"`
		} `json:"results"`
	}
	if err := json.Unmarshal([]byte(result.DecodeContent()), &out); err != nil {
		t.Fatalf("decode tool_search output: %v; content=%s", err, result.DecodeContent())
	}
	if len(out.Results) == 0 {
		t.Fatalf("expected KB tool hit, got content=%s", result.DecodeContent())
	}
	if out.Results[0].Name != "kb.doc.meta" {
		t.Fatalf("knowledge base query should prefer kb.doc.meta, got %q content=%s", out.Results[0].Name, result.DecodeContent())
	}
	for _, hit := range out.Results {
		if hit.Name == "memory" {
			t.Fatalf("knowledge base top results should not prefer memory over KB tools: %+v", out.Results)
		}
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
	Risk        string
	Visibility  string
	Invocation  string
	RouteStatus string
}, name, kind, domain, source, risk, visibility, invocation, routeStatus string) {
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
	if meta.Risk != risk {
		t.Fatalf("%s risk = %q, want %q", name, meta.Risk, risk)
	}
	if meta.Visibility != visibility {
		t.Fatalf("%s visibility = %q, want %q", name, meta.Visibility, visibility)
	}
	if meta.Invocation != invocation {
		t.Fatalf("%s invocation = %q, want %q", name, meta.Invocation, invocation)
	}
	if meta.RouteStatus != routeStatus {
		t.Fatalf("%s route_status = %q, want %q", name, meta.RouteStatus, routeStatus)
	}
}

func toolSearchProfilesForTest(defs []mcphost.ToolDefinition) []router.ToolProfile {
	profiles := make([]router.ToolProfile, 0, len(defs))
	for _, def := range defs {
		profiles = append(profiles, toolruntime.DescriptorFromDefinition(def).Profile)
	}
	return profiles
}

func containsStringForToolTest(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}
