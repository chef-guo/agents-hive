package master

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"

	"github.com/chef-guo/agents-hive/internal/mcphost"
	"github.com/chef-guo/agents-hive/internal/router"
	"github.com/chef-guo/agents-hive/internal/security"
)

const (
	ActionGuardAllow = "allow"
	ActionGuardAsk   = "ask"
	ActionGuardDeny  = "deny"
)

// ActionGuardInput 是确定性动作守卫的最小输入。
type ActionGuardInput struct {
	SessionID  string
	UserID     string
	ToolCallID string
	ToolName   string
	Arguments  json.RawMessage

	SafeExecutor *security.SafeExecutor
	ToolDef      *mcphost.ToolDefinition
}

// ActionGuardDecision 描述一次工具调用的确定性安全决策。
type ActionGuardDecision struct {
	Action               string
	Reason               string
	Source               string
	Pattern              string
	RequiresConfirmation bool
}

// ActionGuard 不依赖 LLM，按工具名和结构化参数作确定性 allow/ask/deny。
type ActionGuard interface {
	Decide(context.Context, ActionGuardInput) ActionGuardDecision
}

type deterministicActionGuard struct{}

var trustedMCPUnsafeSQLPattern = regexp.MustCompile(`(?i)\b(insert|update|delete|drop|truncate|alter|create|replace|merge|grant|revoke|vacuum|copy|call|exec|execute)\b`)

func newDeterministicActionGuard() ActionGuard {
	return deterministicActionGuard{}
}

func (deterministicActionGuard) Decide(_ context.Context, input ActionGuardInput) ActionGuardDecision {
	toolName := strings.TrimSpace(strings.ToLower(input.ToolName))
	if toolName == "" {
		return actionGuardDecision(ActionGuardDeny, "empty_tool_name", "policy", "")
	}

	if router.IsShellCommandTool(toolName) {
		return decideShellAction(input)
	}

	if isExternalSendAction(toolName, input.Arguments) {
		return actionGuardDecision(ActionGuardAsk, "external_send", "policy", "")
	}

	if router.StructuredDangerousOperation(toolName, input.Arguments) {
		return actionGuardDecision(ActionGuardAsk, "structured_dangerous_operation", "policy", "")
	}

	if input.ToolDef != nil {
		profile := router.InferToolProfile(*input.ToolDef, router.ProfileHint{})
		if profile.Kind == router.CapabilityKindMCPTool && profile.Trust == router.TrustTrusted {
			if trustedMCPArgumentsRequireApproval(input.Arguments) {
				return actionGuardDecision(ActionGuardAsk, "trusted_mcp_argument_side_effect", "policy", "")
			}
			if profile.ReadOnly && !router.ProfileHasSideEffect(profile) {
				return actionGuardDecision(ActionGuardAllow, "trusted_mcp_read_only", "policy", "")
			}
			if profile.OpenWorld || profile.Destructive || profile.Risk == router.RiskRuntimeExec || profile.Risk == router.RiskDestructive {
				return actionGuardDecision(ActionGuardDeny, "trusted_mcp_dangerous", "policy", "")
			}
			return actionGuardDecision(ActionGuardAsk, "trusted_mcp_side_effect", "policy", "")
		}
		if profile.ReadOnly && !router.ProfileHasSideEffect(profile) {
			return actionGuardDecision(ActionGuardAllow, "profile_read_only", "policy", "")
		}
		if profile.Risk == router.RiskUnknown {
			return actionGuardDecision(ActionGuardDeny, "unknown_tool", "policy", "")
		}
		if profile.OpenWorld || profile.Destructive || profile.Risk == router.RiskRuntimeExec || profile.Risk == router.RiskDestructive || profile.Risk == router.RiskUnknown {
			return actionGuardDecision(ActionGuardDeny, "profile_dangerous_or_unknown", "policy", "")
		}
	}

	profile, ok := router.BuiltinToolProfile(toolName)
	if !ok {
		return actionGuardDecision(ActionGuardDeny, "unknown_tool", "policy", "")
	}

	if profile.ReadOnly && !router.ProfileHasSideEffect(profile) {
		return actionGuardDecision(ActionGuardAllow, "builtin_read_only", "policy", "")
	}

	if profile.OpenWorld || profile.Destructive {
		return actionGuardDecision(ActionGuardDeny, "builtin_open_world_or_destructive", "policy", "")
	}

	return actionGuardDecision(ActionGuardAllow, "builtin_local_side_effect", "policy", "")
}

func decideShellAction(input ActionGuardInput) ActionGuardDecision {
	cmd, ok := extractShellCommand(input.Arguments)
	if !ok {
		return actionGuardDecision(ActionGuardDeny, "malformed_shell_input", "safe_executor", "")
	}
	if input.SafeExecutor == nil {
		return actionGuardDecision(ActionGuardDeny, "safe_executor_missing", "safe_executor", "")
	}

	policy, pattern := input.SafeExecutor.MatchPolicyWithRule(cmd)
	switch policy {
	case security.PolicyDeny:
		return actionGuardDecision(ActionGuardDeny, "shell_policy_deny", "safe_executor", pattern)
	case security.PolicyAsk:
		return actionGuardDecision(ActionGuardAsk, "shell_policy_ask", "safe_executor", pattern)
	case security.PolicyAllow:
		return actionGuardDecision(ActionGuardAllow, "shell_policy_allow", "safe_executor", pattern)
	default:
		return actionGuardDecision(ActionGuardDeny, "shell_policy_unknown", "safe_executor", pattern)
	}
}

func trustedMCPArgumentsRequireApproval(input json.RawMessage) bool {
	if len(input) == 0 {
		return false
	}
	var payload any
	if err := json.Unmarshal(input, &payload); err != nil {
		return false
	}
	return trustedMCPValueRequiresApproval("", payload)
}

func trustedMCPValueRequiresApproval(key string, value any) bool {
	keyLower := strings.ToLower(strings.TrimSpace(key))
	switch v := value.(type) {
	case map[string]any:
		for k, child := range v {
			if trustedMCPValueRequiresApproval(k, child) {
				return true
			}
		}
	case []any:
		for _, child := range v {
			if trustedMCPValueRequiresApproval(keyLower, child) {
				return true
			}
		}
	case string:
		text := strings.TrimSpace(v)
		if text == "" {
			return false
		}
		if keyLower == "sql" || strings.Contains(keyLower, "query") || strings.Contains(keyLower, "statement") {
			return trustedMCPUnsafeSQLPattern.MatchString(text)
		}
		if trustedMCPActionKeyRequiresApproval(keyLower) {
			action := strings.ToLower(text)
			return router.StructuredDangerousAction("trusted_mcp", action) || trustedMCPActionLooksDangerous(action)
		}
	}
	return false
}

func trustedMCPActionKeyRequiresApproval(key string) bool {
	switch key {
	case "action", "operation", "op", "command", "cmd", "method", "mutation":
		return true
	default:
		return false
	}
}

func trustedMCPActionLooksDangerous(action string) bool {
	tokens := strings.FieldsFunc(strings.ToLower(action), func(r rune) bool {
		return (r < 'a' || r > 'z') && (r < '0' || r > '9')
	})
	for _, keyword := range []string{
		"delete", "drop", "truncate", "update", "insert", "write", "create", "send", "publish", "deploy", "restart", "exec", "execute", "shell",
	} {
		for _, token := range tokens {
			if token == keyword {
				return true
			}
		}
	}
	return false
}

func isExternalSendAction(toolName string, input json.RawMessage) bool {
	switch toolName {
	case "send_im_message":
		return true
	case "feishu_api":
		action := actionGuardStructuredAction(input)
		return action == "send_message" || action == "send_file" || action == "send_image" || action == "upload_file" || action == "upload_image"
	case "im_api":
		return actionGuardStructuredAction(input) == "send_message"
	default:
		return false
	}
}

func actionGuardStructuredAction(input json.RawMessage) string {
	if len(input) == 0 {
		return ""
	}
	var payload struct {
		Action    string `json:"action"`
		Operation string `json:"operation"`
	}
	if err := json.Unmarshal(input, &payload); err != nil {
		return ""
	}
	if payload.Action != "" {
		return strings.TrimSpace(strings.ToLower(payload.Action))
	}
	return strings.TrimSpace(strings.ToLower(payload.Operation))
}

func actionGuardDecision(action, reason, source, pattern string) ActionGuardDecision {
	return ActionGuardDecision{
		Action:               action,
		Reason:               reason,
		Source:               source,
		Pattern:              pattern,
		RequiresConfirmation: action == ActionGuardAsk,
	}
}
