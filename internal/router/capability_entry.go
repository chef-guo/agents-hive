package router

// Capability 是宿主侧授权与路由使用的能力标签。
type Capability string

const (
	CapabilityMetaSkillCreate  Capability = "meta.skill.create"
	CapabilityMetaSkillModify  Capability = "meta.skill.modify"
	CapabilityMetaToolRegister Capability = "meta.tool.register"
	CapabilityExternalSend     Capability = "external.send"
	CapabilityRuntimeExec      Capability = "runtime.exec"
)

// CapabilityKind 标记目录条目的真实类别。
type CapabilityKind string

const (
	CapabilityKindUnknown           CapabilityKind = "unknown"
	CapabilityKindSkillWorkflow     CapabilityKind = "skill_workflow"
	CapabilityKindMCPTool           CapabilityKind = "mcp_tool"
	CapabilityKindBuiltinTool       CapabilityKind = "builtin_tool"
	CapabilityKindCustomTool        CapabilityKind = "custom_tool"
	CapabilityKindAgent             CapabilityKind = "agent"
	CapabilityKindSandboxCapability CapabilityKind = "sandbox_capability"
)

// CapabilitySource 标记目录条目的来源。
type CapabilitySource string

const (
	CapabilitySourceBuiltin          CapabilitySource = "builtin"
	CapabilitySourceLocalSkill       CapabilitySource = "local_skill"
	CapabilitySourceMarketplaceSkill CapabilitySource = "marketplace_skill"
	CapabilitySourceMCPServer        CapabilitySource = "mcp_server"
	CapabilitySourceCustomDir        CapabilitySource = "custom_dir"
	CapabilitySourcePlugin           CapabilitySource = "plugin"
	CapabilitySourceUnknown          CapabilitySource = "unknown"
)

// InvocationMode 标记条目如何被调用或展示。
type InvocationMode string

const (
	InvocationDirectTool    InvocationMode = "direct_tool"
	InvocationSkillTool     InvocationMode = "skill_tool"
	InvocationAgentTool     InvocationMode = "agent_tool"
	InvocationDiscoveryOnly InvocationMode = "discovery_only"
)

// RiskLevel 是路由与权限层共享的风险分层。
type RiskLevel string

const (
	RiskReadOnly      RiskLevel = "read_only"
	RiskLocalWrite    RiskLevel = "local_write"
	RiskExternalWrite RiskLevel = "external_write"
	RiskRuntimeExec   RiskLevel = "runtime_exec"
	RiskDestructive   RiskLevel = "destructive"
	RiskUnknown       RiskLevel = "unknown"
)

// CapabilityEntry 是 typed catalog 的最小一等条目。
type CapabilityEntry struct {
	Name         string           `json:"name"`
	Kind         CapabilityKind   `json:"kind"`
	Domain       string           `json:"domain,omitempty"`
	Source       CapabilitySource `json:"source"`
	Invocation   InvocationMode   `json:"invocation"`
	Risk         RiskLevel        `json:"risk"`
	Capabilities []Capability     `json:"capabilities,omitempty"`
	Description  string           `json:"description,omitempty"`
}
