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

var unsafeSQLPattern = regexp.MustCompile(`(?i)\b(insert|update|delete|drop|truncate|alter|create|replace|merge|grant|revoke|vacuum|copy|call|exec|execute)\b`)

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

	if input.ToolDef != nil {
		profile := router.InferToolProfile(*input.ToolDef, router.ProfileHint{})
		policy := router.EvaluateToolPolicy(profile, router.ToolPolicyContext{
			Input:     input.Arguments,
			ForAction: true,
		})
		if policy.Action == router.ToolPolicyDeny {
			return actionGuardDecision(ActionGuardDeny, policy.Reason, "tool_policy", "")
		}
		if isPlainTextIMSendAction(toolName, input.Arguments) {
			return actionGuardDecision(ActionGuardAllow, "plain_text_im_send", "tool_policy", "")
		}
		if externalSendActionRequiresApproval(toolName, input.Arguments) {
			return actionGuardDecision(ActionGuardAsk, "external_send", "tool_policy", "")
		}
		if toolArgumentsRequireApproval(input.Arguments) {
			return actionGuardDecision(ActionGuardAsk, "argument_side_effect", "tool_policy", "")
		}
		if router.StructuredDangerousOperation(toolName, input.Arguments) {
			return actionGuardDecision(ActionGuardAsk, "structured_dangerous_operation", "tool_policy", "")
		}
		switch policy.Action {
		case router.ToolPolicyAllow:
			return actionGuardDecision(ActionGuardAllow, policy.Reason, "tool_policy", "")
		case router.ToolPolicyAsk:
			return actionGuardDecision(ActionGuardAsk, policy.Reason, "tool_policy", "")
		default:
			return actionGuardDecision(ActionGuardDeny, policy.Reason, "tool_policy", "")
		}
	}

	profile, ok := router.BuiltinToolProfile(toolName)
	if !ok {
		return actionGuardDecision(ActionGuardDeny, "unknown_tool", "policy", "")
	}

	policy := router.EvaluateToolPolicy(profile, router.ToolPolicyContext{
		Input:     input.Arguments,
		ForAction: true,
	})
	if policy.Action == router.ToolPolicyDeny {
		return actionGuardDecision(ActionGuardDeny, policy.Reason, "tool_policy", "")
	}
	if isPlainTextIMSendAction(toolName, input.Arguments) {
		return actionGuardDecision(ActionGuardAllow, "plain_text_im_send", "tool_policy", "")
	}
	if externalSendActionRequiresApproval(toolName, input.Arguments) {
		return actionGuardDecision(ActionGuardAsk, "external_send", "tool_policy", "")
	}
	if toolArgumentsRequireApproval(input.Arguments) {
		return actionGuardDecision(ActionGuardAsk, "argument_side_effect", "tool_policy", "")
	}
	if router.StructuredDangerousOperation(toolName, input.Arguments) {
		return actionGuardDecision(ActionGuardAsk, "structured_dangerous_operation", "tool_policy", "")
	}
	switch policy.Action {
	case router.ToolPolicyAllow:
		return actionGuardDecision(ActionGuardAllow, policy.Reason, "tool_policy", "")
	case router.ToolPolicyAsk:
		return actionGuardDecision(ActionGuardAsk, policy.Reason, "tool_policy", "")
	default:
		return actionGuardDecision(ActionGuardDeny, policy.Reason, "tool_policy", "")
	}
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

func toolArgumentsRequireApproval(input json.RawMessage) bool {
	if len(input) == 0 {
		return false
	}
	var payload any
	if err := json.Unmarshal(input, &payload); err != nil {
		return false
	}
	return toolValueRequiresApproval("", payload)
}

func toolValueRequiresApproval(key string, value any) bool {
	keyLower := strings.ToLower(strings.TrimSpace(key))
	switch v := value.(type) {
	case map[string]any:
		for k, child := range v {
			if toolValueRequiresApproval(k, child) {
				return true
			}
		}
	case []any:
		for _, child := range v {
			if toolValueRequiresApproval(keyLower, child) {
				return true
			}
		}
	case string:
		text := strings.TrimSpace(v)
		if text == "" {
			return false
		}
		if keyLower == "sql" || strings.Contains(keyLower, "query") || strings.Contains(keyLower, "statement") {
			return unsafeSQLPattern.MatchString(text)
		}
		if toolActionKeyRequiresApproval(keyLower) {
			action := strings.ToLower(text)
			return router.StructuredDangerousAction("tool_policy", action) || toolActionLooksDangerous(action)
		}
	}
	return false
}

func toolActionKeyRequiresApproval(key string) bool {
	switch key {
	case "action", "operation", "op", "command", "cmd", "method", "mutation":
		return true
	default:
		return false
	}
}

func toolActionLooksDangerous(action string) bool {
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

func isPlainTextIMSendAction(toolName string, input json.RawMessage) bool {
	switch toolName {
	case "send_im_message":
		return hasPlainTextIMSendContent(input)
	case "feishu_api":
		return actionGuardStructuredAction(input) == "send_message" && hasPlainTextIMSendContent(input)
	case "im_api":
		return actionGuardStructuredAction(input) == "send_message" && hasPlainTextIMSendContent(input)
	default:
		return false
	}
}

func externalSendActionRequiresApproval(toolName string, input json.RawMessage) bool {
	switch toolName {
	case "send_im_message":
		return !hasPlainTextIMSendContent(input)
	case "feishu_api":
		action := actionGuardStructuredAction(input)
		switch action {
		case "send_message":
			return !hasPlainTextIMSendContent(input)
		case "send_file", "send_image", "upload_file", "upload_image":
			return true
		default:
			return false
		}
	case "im_api":
		return actionGuardStructuredAction(input) == "send_message" && !hasPlainTextIMSendContent(input)
	default:
		return false
	}
}

func hasPlainTextIMSendContent(input json.RawMessage) bool {
	if len(input) == 0 {
		return false
	}
	var payload map[string]any
	if err := json.Unmarshal(input, &payload); err != nil {
		return false
	}
	for _, key := range []string{"content", "text", "message"} {
		if value, ok := payload[key].(string); ok && strings.TrimSpace(value) != "" {
			return true
		}
	}
	return false
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
