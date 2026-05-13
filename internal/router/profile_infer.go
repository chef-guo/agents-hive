package router

import (
	"encoding/json"
	"strings"

	"github.com/chef-guo/agents-hive/internal/mcphost"
)

// ProfileHint 是宿主侧从本地可信来源补充的工具画像提示。
type ProfileHint struct {
	Kind               CapabilityKind
	Domain             string
	Source             CapabilitySource
	Invocation         InvocationMode
	Risk               RiskLevel
	Trust              TrustLevel
	ReadOnly           bool
	Destructive        bool
	Idempotent         bool
	OpenWorld          bool
	SideEffect         bool
	Capabilities       []Capability
	AllowedIntentKinds []IntentKind
}

// InferToolProfile 把 MCP ToolDefinition 转成 RouteDecision 使用的可信最小画像。
// description/schema 只参与弱分类和展示，不用于授权；未知或外部开放工具默认 fail-closed。
func InferToolProfile(def mcphost.ToolDefinition, hint ProfileHint) ToolProfile {
	namePolicy := ToolNamePolicy{}
	name := namePolicy.Normalize(def.Name)
	nameLower := strings.ToLower(name)
	descResult := DescriptionSanitizer{}.SanitizeDetailed(def.Description)
	desc := descResult.Text
	descLower := strings.ToLower(desc)

	profile := ToolProfile{
		Name:           name,
		Kind:           CapabilityKindUnknown,
		Domain:         "unknown",
		Source:         CapabilitySourceUnknown,
		Invocation:     InvocationDiscoveryOnly,
		Risk:           RiskUnknown,
		Trust:          TrustUnknown,
		RawDescription: def.Description,
		Metadata: map[string]string{
			"description": desc,
		},
	}

	switch {
	case hint.Kind != "":
		applyProfileHint(&profile, hint)
	case isBuiltinToolName(nameLower) || def.Core:
		profile.Kind = CapabilityKindBuiltinTool
		profile.Source = CapabilitySourceBuiltin
		profile.Trust = TrustBuiltIn
		applyBuiltinToolRule(&profile, nameLower)
	case strings.Contains(nameLower, "__"):
		if def.Trusted || strings.TrimSpace(def.SourceServer) != "" {
			profile = TrustedMCPToolProfile(def, name, desc)
		} else {
			profile = UnknownMCPToolProfile(name)
		}
		profile.Domain = inferMCPToolDomain(nameLower)
	case isKnownSkillWorkflow(nameLower, descLower):
		profile.Kind = CapabilityKindSkillWorkflow
		profile.Source = CapabilitySourceLocalSkill
		profile.Invocation = InvocationSkillTool
		profile.Risk = RiskLocalWrite
		profile.Trust = TrustLocal
		profile.Domain = inferSkillWorkflowDomain(nameLower, descLower)
		profile.Capabilities = inferSkillWorkflowCapabilities(profile.Domain)
		profile.AllowedIntentKinds = inferSkillWorkflowAllowedIntents(profile.Domain)
	case def.IsConcurrencySafe:
		profile.Kind = CapabilityKindCustomTool
		profile.Source = CapabilitySourceCustomDir
		profile.Invocation = InvocationDirectTool
		profile.Domain = "custom"
		profile.Risk = RiskReadOnly
		profile.Trust = TrustLocal
		profile.ReadOnly = true
		profile.Idempotent = true
	case isLikelyCustomTool(def):
		profile.Kind = CapabilityKindCustomTool
		profile.Source = CapabilitySourceCustomDir
		profile.Invocation = InvocationDirectTool
		profile.Domain = "custom"
		profile.Risk = RiskUnknown
		profile.Trust = TrustUnknown
		profile.OpenWorld = true
	}

	if profile.Metadata == nil {
		profile.Metadata = map[string]string{}
	}
	if def.Description != "" {
		profile.Metadata["raw_description_present"] = "true"
	}
	if descResult.Truncated {
		profile.Metadata["description_truncated"] = "true"
	}
	var sanitizeReasons []string
	sanitizeReasons = append(sanitizeReasons, descResult.Reasons...)
	if reason := namePolicy.RejectionReason(profile.Name); reason != "" {
		sanitizeReasons = append(sanitizeReasons, reason)
	}
	schemaTermResult := schemaTerms(def.InputSchema)
	sanitizeReasons = append(sanitizeReasons, schemaTermResult.Reasons...)
	if len(sanitizeReasons) > 0 {
		markProfileSanitizeBlocked(&profile, sanitizeReasons...)
	} else {
		profile.Metadata["schema_terms"] = strings.Join(schemaTermResult.Terms, " ")
	}
	return profile
}

// InferSkillWorkflowProfile 从本地 skill 注册表元数据生成具体 skill workflow 画像。
func InferSkillWorkflowProfile(name, description string) ToolProfile {
	name = ToolNamePolicy{}.Normalize(name)
	descResult := DescriptionSanitizer{}.SanitizeDetailed(description)
	desc := descResult.Text
	nameLower := strings.ToLower(name)
	descLower := strings.ToLower(desc)
	domain := inferSkillWorkflowDomain(nameLower, descLower)
	profile := ToolProfile{
		Name:               name,
		Kind:               CapabilityKindSkillWorkflow,
		Domain:             domain,
		Source:             CapabilitySourceLocalSkill,
		Invocation:         InvocationSkillTool,
		Risk:               RiskLocalWrite,
		Trust:              TrustLocal,
		Capabilities:       inferSkillWorkflowCapabilities(domain),
		AllowedIntentKinds: inferSkillWorkflowAllowedIntents(domain),
		Metadata:           map[string]string{"description": desc},
		RawDescription:     description,
	}
	var sanitizeReasons []string
	sanitizeReasons = append(sanitizeReasons, descResult.Reasons...)
	if reason := (ToolNamePolicy{}).RejectionReason(profile.Name); reason != "" {
		sanitizeReasons = append(sanitizeReasons, reason)
	}
	if descResult.Truncated {
		profile.Metadata["description_truncated"] = "true"
	}
	if len(sanitizeReasons) > 0 {
		markProfileSanitizeBlocked(&profile, sanitizeReasons...)
	}
	return profile
}

func applyProfileHint(profile *ToolProfile, hint ProfileHint) {
	profile.Kind = hint.Kind
	profile.Domain = hint.Domain
	profile.Source = hint.Source
	profile.Invocation = hint.Invocation
	profile.Risk = hint.Risk
	profile.Trust = hint.Trust
	profile.ReadOnly = hint.ReadOnly
	profile.Destructive = hint.Destructive
	profile.Idempotent = hint.Idempotent
	profile.OpenWorld = hint.OpenWorld
	profile.SideEffect = hint.SideEffect
	profile.Capabilities = append([]Capability(nil), hint.Capabilities...)
	profile.AllowedIntentKinds = append([]IntentKind(nil), hint.AllowedIntentKinds...)
}

func isBuiltinToolName(nameLower string) bool {
	_, ok := builtinToolRule(nameLower)
	return ok
}

func isKnownSkillWorkflow(nameLower, descLower string) bool {
	if _, ok := knownSkillWorkflowDomain(nameLower); ok {
		return true
	}
	return false
}

func isLikelyCustomTool(def mcphost.ToolDefinition) bool {
	name := strings.TrimSpace(def.Name)
	if name == "" || strings.TrimSpace(def.Description) == "" {
		return false
	}
	nameLower := strings.ToLower(name)
	descLower := strings.ToLower(def.Description)
	return !strings.Contains(nameLower, "__") && !isBuiltinToolName(nameLower) && !isKnownSkillWorkflow(nameLower, descLower)
}

func inferMCPToolDomain(nameLower string) string {
	if idx := strings.Index(nameLower, "__"); idx > 0 {
		return strings.TrimSpace(nameLower[:idx])
	}
	return "mcp_server"
}

func applyBuiltinToolRule(profile *ToolProfile, nameLower string) {
	rule, ok := builtinToolRule(nameLower)
	if !ok {
		profile.Domain = "unknown"
		profile.Invocation = InvocationDiscoveryOnly
		profile.Risk = RiskUnknown
		return
	}
	profile.Domain = rule.Domain
	profile.Invocation = rule.Invocation
	profile.Risk = rule.Risk
	profile.ReadOnly = rule.ReadOnly
	profile.Destructive = rule.Destructive
	profile.Idempotent = rule.Idempotent
	profile.OpenWorld = rule.OpenWorld
	profile.SideEffect = rule.SideEffect
	profile.Capabilities = rule.Capabilities
}

func inferSkillWorkflowDomain(nameLower, descLower string) string {
	if domain, ok := knownSkillWorkflowDomain(nameLower); ok {
		return domain
	}
	return "skill_workflow"
}

func inferSkillWorkflowCapabilities(domain string) []Capability {
	rule, ok := skillDomainRule(domain)
	if !ok {
		return nil
	}
	return rule.Capabilities
}

func inferSkillWorkflowAllowedIntents(domain string) []IntentKind {
	rule, ok := skillDomainRule(domain)
	if !ok {
		return nil
	}
	return rule.AllowedIntentKinds
}

type schemaTermsResult struct {
	Terms   []string
	Reasons []string
}

type SanitizedSchemaTermsResult struct {
	Terms   []string
	Blocked bool
	Reasons []string
}

func SanitizedSchemaTerms(schema json.RawMessage) SanitizedSchemaTermsResult {
	result := schemaTerms(schema)
	return SanitizedSchemaTermsResult{
		Terms:   append([]string(nil), result.Terms...),
		Blocked: len(result.Reasons) > 0,
		Reasons: append([]string(nil), result.Reasons...),
	}
}

func schemaTerms(schema json.RawMessage) schemaTermsResult {
	if len(schema) == 0 {
		return schemaTermsResult{}
	}
	var v any
	if err := json.Unmarshal(schema, &v); err != nil {
		return schemaTermsResult{}
	}
	terms := make([]string, 0, 16)
	var reasons []string
	collectSchemaTerms(v, &terms, &reasons)
	return schemaTermsResult{Terms: terms, Reasons: reasons}
}

func collectSchemaTerms(v any, out *[]string, reasons *[]string) {
	switch x := v.(type) {
	case map[string]any:
		for k, val := range x {
			key := strings.ToLower(strings.TrimSpace(k))
			*reasons = append(*reasons, schemaPromptInjectionReasons(k)...)
			if !schemaKeyBlacklist[key] {
				*out = append(*out, k)
			}
			collectSchemaTerms(val, out, reasons)
		}
	case []any:
		for _, item := range x {
			collectSchemaTerms(item, out, reasons)
		}
	case string:
		*reasons = append(*reasons, schemaPromptInjectionReasons(x)...)
		for _, part := range strings.FieldsFunc(x, func(r rune) bool {
			return r == '_' || r == '-' || r == '/' || r == '.' || r == ':' || r == ',' || r == ';' || r == '(' || r == ')'
		}) {
			part = strings.TrimSpace(part)
			if len([]rune(part)) >= 2 {
				*out = append(*out, part)
			}
		}
	}
}

func schemaPromptInjectionReasons(text string) []string {
	reasons := promptInjectionReasons(text)
	if len(reasons) == 0 {
		return nil
	}
	out := make([]string, 0, len(reasons))
	for _, reason := range reasons {
		out = append(out, "schema_"+reason)
	}
	return out
}

func markProfileSanitizeBlocked(profile *ToolProfile, reasons ...string) {
	if profile.Metadata == nil {
		profile.Metadata = map[string]string{}
	}
	unique := make([]string, 0, len(reasons))
	seen := map[string]bool{}
	for _, reason := range reasons {
		reason = strings.TrimSpace(reason)
		if reason == "" || seen[reason] {
			continue
		}
		seen[reason] = true
		unique = append(unique, reason)
	}
	if len(unique) == 0 {
		return
	}
	profile.Kind = CapabilityKindUnknown
	profile.Domain = "unknown"
	profile.Invocation = InvocationDiscoveryOnly
	profile.Risk = RiskUnknown
	profile.Trust = TrustUnknown
	profile.ReadOnly = false
	profile.Destructive = true
	profile.Idempotent = false
	profile.OpenWorld = true
	profile.SideEffect = true
	profile.Capabilities = nil
	profile.AllowedIntentKinds = nil
	profile.Metadata["sanitize_blocked"] = "true"
	profile.Metadata["sanitize_reasons"] = strings.Join(unique, ",")
}

var schemaKeyBlacklist = map[string]bool{
	"$schema":              true,
	"additionalproperties": true,
	"anyof":                true,
	"default":              true,
	"items":                true,
	"oneof":                true,
	"properties":           true,
	"required":             true,
	"type":                 true,
}
