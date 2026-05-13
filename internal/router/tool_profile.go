package router

import (
	"encoding/json"
	"strings"

	"github.com/chef-guo/agents-hive/internal/mcphost"
)

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

// TrustedMCPToolProfile 返回已配置远程 MCP 服务端工具的默认画像。
// 默认信任的是服务端来源，不是任意动作：明显写入/删除/执行/发送类工具仍标记为有副作用。
func TrustedMCPToolProfile(def mcphost.ToolDefinition, name, sanitizedDescription string) ToolProfile {
	profile := ToolProfile{
		Name:           name,
		Kind:           CapabilityKindMCPTool,
		Domain:         "mcp_server",
		Source:         CapabilitySourceMCPServer,
		Invocation:     InvocationDirectTool,
		Risk:           RiskReadOnly,
		Trust:          TrustTrusted,
		ReadOnly:       true,
		Idempotent:     true,
		Metadata:       map[string]string{},
		RawDescription: def.Description,
	}
	if server := strings.TrimSpace(def.SourceServer); server != "" {
		profile.Metadata["source_server"] = server
	}
	if sanitizedDescription != "" {
		profile.Metadata["description"] = sanitizedDescription
	}

	classification := classifyTrustedMCPToolRisk(def)
	profile.Metadata["mcp_risk_source"] = classification.Source
	profile.Metadata["mcp_risk_reason"] = classification.Reason
	switch classification.Risk {
	case RiskDestructive:
		profile.Risk = RiskDestructive
		profile.ReadOnly = false
		profile.Idempotent = false
		profile.Destructive = true
		profile.SideEffect = true
	case RiskRuntimeExec:
		profile.Risk = RiskRuntimeExec
		profile.ReadOnly = false
		profile.Idempotent = false
		profile.OpenWorld = true
		profile.SideEffect = true
	case RiskExternalWrite, RiskLocalWrite:
		profile.Risk = classification.Risk
		profile.ReadOnly = false
		profile.Idempotent = false
		profile.SideEffect = true
		profile.Capabilities = []Capability{CapabilityExternalSend}
	default:
		profile.Risk = RiskReadOnly
		profile.ReadOnly = true
		profile.Idempotent = true
		profile.SideEffect = false
		profile.Destructive = false
		profile.OpenWorld = false
	}
	return profile
}

type trustedMCPRiskClassification struct {
	Risk   RiskLevel
	Source string
	Reason string
}

func classifyTrustedMCPToolRisk(def mcphost.ToolDefinition) trustedMCPRiskClassification {
	text := strings.ToLower(strings.Join([]string{
		def.Name,
		def.Description,
		string(def.InputSchema),
	}, " "))
	if risk, reason, ok := highRiskFromMCPAnnotations(def.Annotations); ok {
		return trustedMCPRiskClassification{Risk: risk, Source: "annotations", Reason: reason}
	}
	if reason := firstKeywordReason(text, mcpRuntimeExecKeywords); reason != "" {
		return trustedMCPRiskClassification{Risk: RiskRuntimeExec, Source: "heuristic", Reason: reason}
	}
	if reason := firstKeywordReason(text, mcpDestructiveKeywords); reason != "" {
		return trustedMCPRiskClassification{Risk: RiskDestructive, Source: "heuristic", Reason: reason}
	}
	if reason := firstKeywordReason(text, mcpExternalWriteKeywords); reason != "" {
		return trustedMCPRiskClassification{Risk: RiskExternalWrite, Source: "heuristic", Reason: reason}
	}
	if reason := firstKeywordReason(text, mcpLocalWriteKeywords); reason != "" {
		return trustedMCPRiskClassification{Risk: RiskLocalWrite, Source: "heuristic", Reason: reason}
	}
	if risk, reason, ok := lowRiskFromMCPAnnotations(def.Annotations); ok {
		return trustedMCPRiskClassification{Risk: risk, Source: "annotations", Reason: reason}
	}
	return trustedMCPRiskClassification{Risk: RiskReadOnly, Source: "default", Reason: "trusted_mcp_default_read_only"}
}

func highRiskFromMCPAnnotations(raw json.RawMessage) (RiskLevel, string, bool) {
	if len(raw) == 0 {
		return "", "", false
	}
	var annotations struct {
		ReadOnlyHint    *bool `json:"readOnlyHint"`
		DestructiveHint *bool `json:"destructiveHint"`
		IdempotentHint  *bool `json:"idempotentHint"`
		OpenWorldHint   *bool `json:"openWorldHint"`
	}
	if err := json.Unmarshal(raw, &annotations); err != nil {
		return "", "", false
	}
	if annotations.DestructiveHint != nil && *annotations.DestructiveHint {
		return RiskDestructive, "destructiveHint=true", true
	}
	if annotations.OpenWorldHint != nil && *annotations.OpenWorldHint {
		return RiskRuntimeExec, "openWorldHint=true", true
	}
	return "", "", false
}

func lowRiskFromMCPAnnotations(raw json.RawMessage) (RiskLevel, string, bool) {
	if len(raw) == 0 {
		return "", "", false
	}
	var annotations struct {
		ReadOnlyHint *bool `json:"readOnlyHint"`
	}
	if err := json.Unmarshal(raw, &annotations); err != nil {
		return "", "", false
	}
	if annotations.ReadOnlyHint != nil && *annotations.ReadOnlyHint {
		return RiskReadOnly, "readOnlyHint=true", true
	}
	return "", "", false
}

var mcpRuntimeExecKeywords = []string{
	"exec_command", "execute_command", "shell", "bash", "command", "terminal", "script", "run_command", "kubectl", "ssh",
}

var mcpDestructiveKeywords = []string{
	"delete", "drop", "truncate", "destroy", "remove", "purge", "wipe", "kill", "terminate", "restart", "shutdown", "reboot",
}

var mcpExternalWriteKeywords = []string{
	"send", "publish", "post", "message", "email", "notify", "deploy", "release", "rollback", "merge", "approve",
}

var mcpLocalWriteKeywords = []string{
	"write", "create", "update", "insert", "upsert", "patch", "modify", "save", "set_", "configure", "mutation",
}

func firstKeywordReason(text string, keywords []string) string {
	tokens := riskTokens(text)
	for _, keyword := range keywords {
		if keyword == "set_" {
			keyword = "set"
		}
		parts := riskTokens(keyword)
		if len(parts) == 0 {
			continue
		}
		if riskTokenSequenceContains(tokens, parts) {
			return "keyword:" + keyword
		}
	}
	return ""
}

func riskTokens(text string) []string {
	return strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return (r < 'a' || r > 'z') && (r < '0' || r > '9')
	})
}

func riskTokenSequenceContains(tokens, parts []string) bool {
	if len(parts) > len(tokens) {
		return false
	}
	for i := 0; i <= len(tokens)-len(parts); i++ {
		matched := true
		for j := range parts {
			if tokens[i+j] != parts[j] {
				matched = false
				break
			}
		}
		if matched {
			return true
		}
	}
	return false
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
