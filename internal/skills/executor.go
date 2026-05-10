package skills

import (
	"context"
	"regexp"
	"strings"
	"time"

	"github.com/chef-guo/agents-hive/internal/errs"
)

// ShellExecutor 为可测试性抽象 shell 命令执行。
// Eng Review #2 决策：返回 stderr，签名为 (stdout, stderr, err)。
type ShellExecutor interface {
	Execute(command string) (stdout string, stderr string, err error)
}

// SandboxExecutor 是 sandbox.Executor 的本地接口镜像，避免 import cycle。
type SandboxExecutor interface {
	Execute(ctx context.Context, req SandboxExecRequest) (SandboxExecResult, error)
	Close() error
}

// SandboxExecRequest 镜像 sandbox.ExecRequest。
type SandboxExecRequest struct {
	Command   string
	SessionID string
	Timeout   time.Duration
	WorkDir   string
	Env       map[string]string
}

// SandboxExecResult 镜像 sandbox.ExecResult。
type SandboxExecResult struct {
	Stdout     string
	Stderr     string
	ExitCode   int
	Diagnostic *SandboxExecDiagnostic
}

type SandboxExecDiagnostic struct {
	FailureType          string
	Summary              string
	RequiresUserApproval bool
	SuggestedAction      string
	SuggestedEnv         map[string]string
}

// DefaultShellExecutor 委托 SandboxExecutor 执行命令。
type DefaultShellExecutor struct {
	Executor SandboxExecutor // 沙箱执行器（由 bootstrap 注入）
	Timeout  time.Duration   // 默认 600s（如果为零）
	WorkDir  string
}

// Execute 通过 SandboxExecutor 运行命令并返回 stdout 和 stderr。
func (e *DefaultShellExecutor) Execute(command string) (string, string, error) {
	if e.Executor == nil {
		return "", "", errs.New(errs.CodeExecutionFailed, "沙箱执行器未初始化，无法执行技能命令")
	}

	timeout := e.Timeout
	if timeout == 0 {
		timeout = 600 * time.Second
	}
	result, err := e.Executor.Execute(context.Background(), SandboxExecRequest{
		Command:   command,
		SessionID: "skills-default",
		Timeout:   timeout,
		WorkDir:   e.WorkDir,
	})
	if err != nil {
		return "", "", err
	}
	if result.ExitCode != 0 {
		output := result.Stdout
		if result.Stderr != "" {
			output += result.Stderr
		}
		if result.Diagnostic != nil {
			output += "\n\n" + formatSandboxExecDiagnostic(result.Diagnostic)
		}
		return result.Stdout, result.Stderr, errs.New(errs.CodeSkillExecFailed, strings.TrimSpace(output))
	}
	return result.Stdout, result.Stderr, nil
}

func formatSandboxExecDiagnostic(diag *SandboxExecDiagnostic) string {
	if diag == nil {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("诊断:\n")
	if diag.Summary != "" {
		sb.WriteString(diag.Summary)
		sb.WriteString("\n")
	}
	if diag.RequiresUserApproval {
		sb.WriteString("建议: 需要用户批准切换到有权限的执行环境。\n")
	}
	if diag.SuggestedAction == "use_writable_go_cache" {
		sb.WriteString("建议: 将 Go 缓存指向可写目录后重试，不需要切换用户。\n")
	}
	return strings.TrimSpace(sb.String())
}

// reDynamic 匹配 !`command` 动态上下文占位符
var reDynamic = regexp.MustCompile("!`([^`]+)`")

// ExecuteDynamicContext 将内容中的 !`command` 占位符替换为
// 通过给定 ShellExecutor 执行每个命令的输出
func ExecuteDynamicContext(content string, executor ShellExecutor) (string, error) {
	var lastErr error
	result := reDynamic.ReplaceAllStringFunc(content, func(match string) string {
		sub := reDynamic.FindStringSubmatch(match)
		if len(sub) < 2 {
			return match
		}
		stdout, _, err := executor.Execute(sub[1])
		if err != nil {
			lastErr = err
			return match
		}
		return stdout
	})
	return result, lastErr
}
