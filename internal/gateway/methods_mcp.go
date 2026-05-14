package gateway

import (
	"context"
	"encoding/json"
	"sort"
	"strings"

	"github.com/chef-guo/agents-hive/internal/errs"
	"github.com/chef-guo/agents-hive/internal/mcphost"
	"github.com/chef-guo/agents-hive/internal/router"
)

type mcpToolSummary struct {
	Name              string `json:"name"`
	Description       string `json:"description,omitempty"`
	Server            string `json:"server,omitempty"`
	Core              bool   `json:"core,omitempty"`
	IsConcurrencySafe bool   `json:"is_concurrency_safe,omitempty"`
	Trusted           bool   `json:"trusted,omitempty"`
	Risk              string `json:"risk,omitempty"`
	ReadOnly          bool   `json:"read_only,omitempty"`
	RequiresApproval  bool   `json:"requires_approval,omitempty"`
}

type mcpToolsByServer struct {
	Name      string           `json:"name"`
	Count     int              `json:"count"`
	Tools     []mcpToolSummary `json:"tools"`
	Resources int              `json:"resources,omitempty"`
	Prompts   int              `json:"prompts,omitempty"`
}

type mcpToolsListResponse struct {
	Total      int                `json:"total"`
	MCPCount   int                `json:"mcp_count"`
	LocalCount int                `json:"local_count"`
	Servers    []mcpToolsByServer `json:"servers"`
	Tools      []mcpToolSummary   `json:"tools"`
}

// registerMCPMethods 注册 MCP 资源和提示相关的 RPC 方法
func registerMCPMethods(gw *Gateway, deps Deps) {
	gw.Register(MethodDef{
		Name:        "mcp.tools.list",
		Description: "列出当前运行时已注册的 MCP 工具目录",
		AuthScope:   "read",
		Handler: func(ctx context.Context, params json.RawMessage) (json.RawMessage, error) {
			return json.Marshal(buildMCPToolsListResponse(deps.MCPHost))
		},
	})

	gw.Register(MethodDef{
		Name:        "mcp.resources.list",
		Description: "列出所有已注册的 MCP 资源",
		AuthScope:   "read",
		Handler: func(ctx context.Context, params json.RawMessage) (json.RawMessage, error) {
			return json.Marshal(deps.MCPHost.ListResources())
		},
	})

	gw.Register(MethodDef{
		Name:        "mcp.resources.read",
		Description: "读取指定 MCP 资源",
		AuthScope:   "read",
		Handler: func(ctx context.Context, params json.RawMessage) (json.RawMessage, error) {
			var p struct {
				URI string `json:"uri"`
			}
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, errs.Wrap(errs.CodeInvalidArgument, "参数无效", err)
			}
			if p.URI == "" {
				return nil, errs.New(errs.CodeInvalidArgument, "缺少必需参数 uri")
			}
			content, err := deps.MCPHost.ReadResource(ctx, p.URI)
			if err != nil {
				return nil, err
			}
			return json.Marshal(content)
		},
	})

	gw.Register(MethodDef{
		Name:        "mcp.prompts.list",
		Description: "列出所有已注册的 MCP 提示",
		AuthScope:   "read",
		Handler: func(ctx context.Context, params json.RawMessage) (json.RawMessage, error) {
			return json.Marshal(deps.MCPHost.ListPrompts())
		},
	})

	gw.Register(MethodDef{
		Name:        "mcp.prompts.get",
		Description: "获取并执行指定 MCP 提示",
		AuthScope:   "read",
		Handler: func(ctx context.Context, params json.RawMessage) (json.RawMessage, error) {
			var p struct {
				Name string            `json:"name"`
				Args map[string]string `json:"args"`
			}
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, errs.Wrap(errs.CodeInvalidArgument, "参数无效", err)
			}
			if p.Name == "" {
				return nil, errs.New(errs.CodeInvalidArgument, "缺少必需参数 name")
			}
			messages, err := deps.MCPHost.GetPrompt(ctx, p.Name, p.Args)
			if err != nil {
				return nil, err
			}
			return json.Marshal(messages)
		},
	})
}

func buildMCPToolsListResponse(host *mcphost.Host) mcpToolsListResponse {
	if host == nil {
		return mcpToolsListResponse{}
	}

	tools := host.ListTools()
	summaries := make([]mcpToolSummary, 0, len(tools))
	servers := make(map[string]*mcpToolsByServer)
	for _, tool := range tools {
		profile := router.InferToolProfile(tool, router.ProfileHint{})
		policy := router.EvaluateToolPolicy(profile, router.ToolPolicyContext{
			Intent:   router.IntentFrame{Kind: router.IntentRead},
			ForRoute: true,
		})
		summary := mcpToolSummary{
			Name:              tool.Name,
			Description:       tool.Description,
			Server:            mcpServerNameFromTool(tool.Name),
			Core:              tool.Core,
			IsConcurrencySafe: tool.IsConcurrencySafe,
			Trusted:           profile.Trust == router.TrustTrusted,
			Risk:              string(profile.Risk),
			ReadOnly:          policy.Action == router.ToolPolicyAllow && policy.RouteStatus == router.ToolRouteCallableReadOnly,
			RequiresApproval:  policy.RequiresApproval || policy.Action == router.ToolPolicyDeny,
		}
		summaries = append(summaries, summary)
		if summary.Server == "" {
			continue
		}
		entry := servers[summary.Server]
		if entry == nil {
			entry = &mcpToolsByServer{Name: summary.Server}
			servers[summary.Server] = entry
		}
		entry.Tools = append(entry.Tools, summary)
	}
	sort.SliceStable(summaries, func(i, j int) bool {
		return summaries[i].Name < summaries[j].Name
	})

	for _, res := range host.ListResources() {
		server := mcpServerNameFromResourceURI(res.URI)
		if server == "" {
			continue
		}
		entry := ensureMCPServerEntry(servers, server)
		entry.Resources++
	}
	for _, prompt := range host.ListPrompts() {
		server := mcpServerNameFromTool(prompt.Name)
		if server == "" {
			continue
		}
		entry := ensureMCPServerEntry(servers, server)
		entry.Prompts++
	}

	serverList := make([]mcpToolsByServer, 0, len(servers))
	for _, entry := range servers {
		sort.SliceStable(entry.Tools, func(i, j int) bool {
			return entry.Tools[i].Name < entry.Tools[j].Name
		})
		entry.Count = len(entry.Tools)
		serverList = append(serverList, *entry)
	}
	sort.SliceStable(serverList, func(i, j int) bool {
		return serverList[i].Name < serverList[j].Name
	})

	return mcpToolsListResponse{
		Total:      len(summaries),
		MCPCount:   countRemoteMCPTools(summaries),
		LocalCount: countLocalTools(summaries),
		Servers:    serverList,
		Tools:      summaries,
	}
}

func ensureMCPServerEntry(servers map[string]*mcpToolsByServer, server string) *mcpToolsByServer {
	entry := servers[server]
	if entry == nil {
		entry = &mcpToolsByServer{Name: server}
		servers[server] = entry
	}
	return entry
}

func mcpServerNameFromTool(name string) string {
	parts := strings.SplitN(strings.TrimSpace(name), "__", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return ""
	}
	return parts[0]
}

func mcpServerNameFromResourceURI(uri string) string {
	parts := strings.SplitN(strings.TrimSpace(uri), "://", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return ""
	}
	return parts[0]
}

func countRemoteMCPTools(tools []mcpToolSummary) int {
	count := 0
	for _, tool := range tools {
		if tool.Server != "" {
			count++
		}
	}
	return count
}

func countLocalTools(tools []mcpToolSummary) int {
	return len(tools) - countRemoteMCPTools(tools)
}
