package router

// TrustLevel 描述宿主对工具画像来源的信任等级。
type TrustLevel string

const (
	TrustBuiltIn TrustLevel = "built_in"
	TrustLocal   TrustLevel = "local"
	TrustTrusted TrustLevel = "trusted"
	TrustUnknown TrustLevel = "unknown"
)

// ToolProfile 是工具目录条目在路由前的宿主可信画像。
type ToolProfile struct {
	Name               string            `json:"name"`
	Kind               CapabilityKind    `json:"kind"`
	Domain             string            `json:"domain,omitempty"`
	Source             CapabilitySource  `json:"source"`
	Invocation         InvocationMode    `json:"invocation"`
	Risk               RiskLevel         `json:"risk"`
	Trust              TrustLevel        `json:"trust"`
	ReadOnly           bool              `json:"read_only,omitempty"`
	Destructive        bool              `json:"destructive,omitempty"`
	Idempotent         bool              `json:"idempotent,omitempty"`
	OpenWorld          bool              `json:"open_world,omitempty"`
	SideEffect         bool              `json:"side_effect,omitempty"`
	Capabilities       []Capability      `json:"capabilities,omitempty"`
	AllowedIntentKinds []IntentKind      `json:"allowed_intent_kinds,omitempty"`
	Metadata           map[string]string `json:"metadata,omitempty"`
	RawDescription     string            `json:"raw_description,omitempty"`
}

// UnknownMCPToolProfile 返回外部 MCP 工具的 fail-closed 默认画像。
// MCP 工具的调用形态仍是 direct_tool；是否允许调用由 risk/open_world/destructive 控制。
func UnknownMCPToolProfile(name string) ToolProfile {
	return ToolProfile{
		Name:        name,
		Kind:        CapabilityKindMCPTool,
		Domain:      "mcp_server",
		Source:      CapabilitySourceMCPServer,
		Invocation:  InvocationDirectTool,
		Risk:        RiskDestructive,
		Trust:       TrustUnknown,
		Destructive: true,
		OpenWorld:   true,
		SideEffect:  true,
	}
}

// Entry 转换为 typed catalog 条目，供召回与审计共用。
func (p ToolProfile) Entry() CapabilityEntry {
	return CapabilityEntry{
		Name:         p.Name,
		Kind:         p.Kind,
		Domain:       p.Domain,
		Source:       p.Source,
		Invocation:   p.Invocation,
		Risk:         p.Risk,
		Capabilities: append([]Capability(nil), p.Capabilities...),
	}
}
