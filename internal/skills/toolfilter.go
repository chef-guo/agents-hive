package skills

import (
	"fmt"

	"github.com/chef-guo/agents-hive/internal/errs"
	"github.com/chef-guo/agents-hive/internal/mcphost"
)

// ToolFilter 基于 allowed/denied 列表限制可用工具
type ToolFilter struct {
	allowedTools map[string]bool
	deniedTools  map[string]bool
}

// NewToolFilter 从允许的工具名称列表创建新的 ToolFilter。
// 如果 allowedTools 列表为空，则允许所有工具
func NewToolFilter(allowedTools []string) *ToolFilter {
	allowed := make(map[string]bool, len(allowedTools))
	for _, t := range allowedTools {
		allowed[t] = true
	}
	return &ToolFilter{allowedTools: allowed}
}

// NewToolFilterWithDeny 创建同时支持 allow 和 deny 列表的 ToolFilter。
// deny 优先于 allow：即使工具在 allow 列表中，若同时在 deny 列表中则被拒绝。
// allowed 为空表示允许所有（不在 deny 中的）工具。
func NewToolFilterWithDeny(allowed, denied []string) *ToolFilter {
	allowMap := make(map[string]bool, len(allowed))
	for _, t := range allowed {
		allowMap[t] = true
	}
	denyMap := make(map[string]bool, len(denied))
	for _, t := range denied {
		denyMap[t] = true
	}
	return &ToolFilter{allowedTools: allowMap, deniedTools: denyMap}
}

// IsEmpty 如果未配置任何限制则返回 true
func (f *ToolFilter) IsEmpty() bool {
	if f == nil {
		return true
	}
	return len(f.allowedTools) == 0 && len(f.deniedTools) == 0
}

// IsAllowed 检查工具名称是否被允许。
// deny 优先于 allow。如果过滤器为空或为 nil 则返回 true。
// 外部 MCP/custom 工具不会绕过 allow list；动态发现只提供候选，
// 是否可调用必须由上层路由与显式 profile 决定。
func (f *ToolFilter) IsAllowed(toolName string) bool {
	if f == nil {
		return true
	}
	// deny 优先
	if f.deniedTools[toolName] {
		return false
	}
	// 如果没有 allow 列表，允许所有（不在 deny 中的）
	if len(f.allowedTools) == 0 {
		return true
	}
	return f.allowedTools[toolName]
}

// CheckAllowed 如果工具不被允许则返回错误
func (f *ToolFilter) CheckAllowed(toolName string) error {
	if f == nil {
		return nil
	}
	if f.IsAllowed(toolName) {
		return nil
	}
	return errs.New(errs.CodeSkillToolBlocked, fmt.Sprintf("tool %q is not in the allowed-tools list for this skill", toolName))
}

// FilterTools 仅返回允许的工具。
// 如果过滤器为空或为 nil，则返回所有工具
func (f *ToolFilter) FilterTools(tools []mcphost.ToolDefinition) []mcphost.ToolDefinition {
	if f == nil || f.IsEmpty() {
		return tools
	}
	filtered := make([]mcphost.ToolDefinition, 0, len(tools))
	for _, t := range tools {
		if f.IsAllowed(t.Name) {
			filtered = append(filtered, t)
		}
	}
	return filtered
}
