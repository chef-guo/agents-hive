package sandbox

import (
	"context"
	"fmt"
	"strings"
	"time"

	"go.uber.org/zap"
)

// LocalExecutor 包装现有 PersistentShell，实现 Executor 接口。
// 用于 CLI 开发环境，命令直接在宿主机执行。
type LocalExecutor struct {
	shell  PersistentShellIface
	logger *zap.Logger
}

// PersistentShellIface 抽象 PersistentShell，便于测试。
type PersistentShellIface interface {
	Execute(ctx context.Context, command string) (stdout, stderr string, exitCode int, err error)
	Close() error
}

// NewLocalExecutor 创建 LocalExecutor。
func NewLocalExecutor(shell PersistentShellIface, logger *zap.Logger) *LocalExecutor {
	return &LocalExecutor{shell: shell, logger: logger}
}

// Execute 通过 PersistentShell 执行命令，注入 WorkDir 和 Env。
func (e *LocalExecutor) Execute(ctx context.Context, req ExecRequest) (ExecResult, error) {
	if req.Command == "" {
		return ExecResult{}, fmt.Errorf("empty command")
	}

	// 超时兜底：req.Timeout 为 0 时用 120s 默认值
	timeout := req.Timeout
	if timeout == 0 {
		timeout = 120 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// 构建完整命令：env exports + cd workdir + command
	var cmdBuilder strings.Builder

	// Env 注入：export K='V' 前缀
	if len(req.Env) > 0 {
		for k, v := range req.Env {
			cmdBuilder.WriteString(fmt.Sprintf("export %s='%s'\n", k, strings.ReplaceAll(v, "'", "'\\''")))
		}
	}

	// WorkDir 注入：cd 前缀
	if req.WorkDir != "" {
		cmdBuilder.WriteString(fmt.Sprintf("cd %q && ", req.WorkDir))
	}

	cmdBuilder.WriteString(req.Command)

	stdout, stderr, exitCode, err := e.shell.Execute(ctx, cmdBuilder.String())
	if err != nil {
		return attachExecDiagnostic(req, ExecResult{Stdout: stdout, Stderr: stderr, ExitCode: exitCode}, err), err
	}

	result := ExecResult{
		Stdout:   stdout,
		Stderr:   stderr,
		ExitCode: exitCode,
	}
	return attachExecDiagnostic(req, result, nil), nil
}

// Close 关闭底层 shell。
func (e *LocalExecutor) Close() error {
	return e.shell.Close()
}
