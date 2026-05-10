package tools

import (
	"fmt"
	"sort"
	"strings"

	"github.com/chef-guo/agents-hive/internal/mcphost"
	"github.com/chef-guo/agents-hive/internal/sandbox"
)

func formatCommandOutput(result sandbox.ExecResult) string {
	var output strings.Builder
	if result.Stdout != "" {
		output.WriteString(result.Stdout)
	}
	if result.Stderr != "" {
		if output.Len() > 0 {
			output.WriteString("\n")
		}
		output.WriteString("stderr:\n")
		output.WriteString(result.Stderr)
	}
	return output.String()
}

func formatCommandFailure(result sandbox.ExecResult) string {
	var sb strings.Builder
	if result.ExitCode != 0 {
		fmt.Fprintf(&sb, "退出码 %d\n", result.ExitCode)
	}

	output := formatCommandOutput(result)
	if output != "" {
		sb.WriteString(output)
	}

	if diag := result.Diagnostic; diag != nil {
		if sb.Len() == 0 {
			sb.WriteString("执行失败")
		}
		if sb.Len() > 0 {
			sb.WriteString("\n\n")
		}
		sb.WriteString("诊断:\n")
		if diag.Summary != "" {
			sb.WriteString(diag.Summary)
			sb.WriteString("\n")
		}
		if diag.RequiresUserApproval {
			sb.WriteString("建议: 需要用户批准切换到有权限的执行环境。\n")
		}
		switch diag.SuggestedAction {
		case sandbox.ActionUseWritableGoCache:
			sb.WriteString("建议: 将 Go 缓存指向可写目录后重试，不需要切换用户。\n")
		case sandbox.ActionRequestPrivilegedExecution:
			sb.WriteString("建议: 请求切换到有权限的执行环境后重试。\n")
		}
		if len(diag.SuggestedEnv) > 0 {
			sb.WriteString("建议环境:\n")
			keys := make([]string, 0, len(diag.SuggestedEnv))
			for k := range diag.SuggestedEnv {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				fmt.Fprintf(&sb, "%s=%s\n", k, diag.SuggestedEnv[k])
			}
		}
	}

	return truncateOutput(sb.String())
}

func commandFailureToolResult(result sandbox.ExecResult) *mcphost.ToolResult {
	tr := &mcphost.ToolResult{
		Content: jsonText(formatCommandFailure(result)),
		IsError: true,
	}
	if result.Diagnostic != nil {
		tr.FailureType = result.Diagnostic.FailureType
		tr.RequiresUserApproval = result.Diagnostic.RequiresUserApproval
		tr.SuggestedAction = result.Diagnostic.SuggestedAction
	}
	return tr
}
