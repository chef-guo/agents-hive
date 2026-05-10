package router

// IntentKind 描述本轮用户请求的主意图。
type IntentKind string

const (
	IntentUnknown       IntentKind = "unknown"
	IntentAnswer        IntentKind = "answer"
	IntentRead          IntentKind = "read"
	IntentWriteLocal    IntentKind = "write_local"
	IntentExternalRead  IntentKind = "external_read"
	IntentExternalWrite IntentKind = "external_write"
	IntentCreateSkill   IntentKind = "create_skill"
	IntentModifySkill   IntentKind = "modify_skill"
	IntentManageTool    IntentKind = "manage_tool"
	IntentPlan          IntentKind = "plan"
)

// IntentFrame 是 RouteDecision 的输入骨架；Phase 1 只定义结构，不做分类。
type IntentFrame struct {
	Kind               IntentKind   `json:"kind"`
	Subject            string       `json:"subject,omitempty"`
	Constraints        []string     `json:"constraints,omitempty"`
	NegatedActions     []string     `json:"negated_actions,omitempty"`
	RequiresExternal   bool         `json:"requires_external,omitempty"`
	AllowsSideEffects  bool         `json:"allows_side_effects,omitempty"`
	Confidence         float64      `json:"confidence,omitempty"`
	Signals            []string     `json:"signals,omitempty"`
	SecondaryIntents   []IntentKind `json:"secondary_intents,omitempty"`
	AllowedDomainsHint []string     `json:"allowed_domains_hint,omitempty"`
}
