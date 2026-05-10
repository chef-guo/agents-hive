package sandbox

import (
	"context"
	"time"
)

// Executor 是命令执行的统一接口。
// LocalExecutor 和 DockerExecutor 都实现此接口。
type Executor interface {
	Execute(ctx context.Context, req ExecRequest) (ExecResult, error)
	Close() error
}

// ExecRequest 封装一次命令执行的所有参数。
type ExecRequest struct {
	Command   string            // shell 命令字符串
	SessionID string            // Phase 1 忽略，所有命令共享一个容器
	Timeout   time.Duration     // 从 bashInput.Timeout 传入
	WorkDir   string            // 宿主机绝对路径，直接透传
	Env       map[string]string // 从 plugin ShellEnv hook 传入
}

// ExecResult 封装命令执行的结果。
type ExecResult struct {
	Stdout     string
	Stderr     string
	ExitCode   int
	Diagnostic *ExecFailureDiagnostic
}

// ExecFailureDiagnostic 是命令运行失败后的机器可读诊断。
// 预执行权限策略由 SafeExecutorWrapper/Master 处理；这里记录的是命令已经运行后，
// 执行环境本身暴露出的权限、沙箱或资源类问题。
type ExecFailureDiagnostic struct {
	FailureType          string            `json:"failure_type"`
	Summary              string            `json:"summary"`
	RequiresUserApproval bool              `json:"requires_user_approval,omitempty"`
	SuggestedAction      string            `json:"suggested_action,omitempty"`
	SuggestedEnv         map[string]string `json:"suggested_env,omitempty"`
}

// DockerConfig Docker 沙箱的详细配置（sandbox 包内部使用，避免 import cycle）。
type DockerConfig struct {
	Image       string
	CPULimit    string
	MemoryLimit string
	PidsLimit   int
	TmpfsSize   string
	Network     string

	// 安全加固选项（域E Phase 2）
	NetworkDisabled bool   // true → --network=none，完全断网隔离
	SeccompProfile  string // seccomp 配置文件路径；空 = 使用 Docker 默认 seccomp
	ReadOnlyWorkDir bool   // true → workDir bind mount 以只读模式挂载（容器内无法写宿主机目录）
}
