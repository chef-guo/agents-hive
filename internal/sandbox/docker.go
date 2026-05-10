package sandbox

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"os/user"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
)

// DockerExecutor 通过 Docker 容器执行命令，实现 Executor 接口。
// Phase 1：单容器模式，所有命令共享一个长期运行的容器。
type DockerExecutor struct {
	mu          sync.Mutex
	rebuilding  bool
	rebuildCond *sync.Cond
	containerID string
	config      DockerConfig
	workDir     string // 宿主机工作目录（bind mount）
	logger      *zap.Logger
}

// NewDockerExecutor 创建 DockerExecutor 并启动容器。
func NewDockerExecutor(cfg DockerConfig, workDir string, logger *zap.Logger) (*DockerExecutor, error) {
	e := &DockerExecutor{
		config:  applyDockerDefaults(cfg),
		workDir: workDir,
		logger:  logger,
	}
	e.rebuildCond = sync.NewCond(&e.mu)

	if err := e.createContainer(); err != nil {
		return nil, fmt.Errorf("create sandbox container: %w", err)
	}

	return e, nil
}

func applyDockerDefaults(cfg DockerConfig) DockerConfig {
	if cfg.Image == "" {
		cfg.Image = "hive-sandbox:latest"
	}
	if cfg.CPULimit == "" {
		cfg.CPULimit = "1.0"
	}
	if cfg.MemoryLimit == "" {
		cfg.MemoryLimit = "512m"
	}
	if cfg.PidsLimit == 0 {
		cfg.PidsLimit = 100
	}
	if cfg.TmpfsSize == "" {
		cfg.TmpfsSize = "256m"
	}
	// NetworkDisabled 优先：断网模式下强制 network=none，忽略 Network 字段
	if cfg.NetworkDisabled {
		cfg.Network = "none"
	} else if cfg.Network == "" {
		cfg.Network = "bridge"
	}
	return cfg
}

// createContainer 创建并启动沙箱容器。
func (e *DockerExecutor) createContainer() error {
	// 确定 UID:GID
	uid, gid := getUIDGID()

	args := []string{
		"run", "-d",
		"--name", "hive-sandbox-" + randomSuffix(),
		"--read-only",
		"--security-opt=no-new-privileges",
		"--cap-drop=ALL",
		fmt.Sprintf("--cpus=%s", e.config.CPULimit),
		fmt.Sprintf("--memory=%s", e.config.MemoryLimit),
		fmt.Sprintf("--pids-limit=%d", e.config.PidsLimit),
		fmt.Sprintf("--network=%s", e.config.Network),
		fmt.Sprintf("--user=%s:%s", uid, gid),
	}

	// seccomp 配置：明确指定 profile 时覆盖 Docker 默认值
	if e.config.SeccompProfile != "" {
		args = append(args, fmt.Sprintf("--security-opt=seccomp=%s", e.config.SeccompProfile))
	}

	// bind mount 宿主机工作目录到容器内相同路径
	// ReadOnlyWorkDir=true 时以只读模式挂载，防止容器写回宿主机文件系统
	workDirMount := fmt.Sprintf("%s:%s", e.workDir, e.workDir)
	if e.config.ReadOnlyWorkDir {
		workDirMount += ":ro"
	}
	args = append(args,
		"-v", workDirMount,
		// tmpfs 挂载
		"--tmpfs", fmt.Sprintf("/tmp:rw,size=%s,noexec,nosuid,nodev", e.config.TmpfsSize),
		"--tmpfs", homeSandboxTmpfs(uid, gid),
		e.config.Image,
	)

	stdout, stderr, exitCode, err := dockerCmd(context.Background(), args...)
	if err != nil {
		return fmt.Errorf("docker run failed: %w (stderr: %s)", err, stderr)
	}
	if exitCode != 0 {
		return fmt.Errorf("docker run exit %d: %s", exitCode, stderr)
	}

	e.containerID = strings.TrimSpace(stdout)
	e.logger.Info("沙箱容器已创建", zap.String("container_id", e.containerID[:12]))
	return nil
}

// Execute 在容器内执行命令。
func (e *DockerExecutor) Execute(ctx context.Context, req ExecRequest) (ExecResult, error) {
	if req.Command == "" {
		return ExecResult{}, fmt.Errorf("empty command")
	}

	e.mu.Lock()
	// 等待重建完成
	for e.rebuilding {
		e.rebuildCond.Wait()
	}

	containerID := e.containerID
	e.mu.Unlock()

	// 超时兜底
	timeout := req.Timeout
	if timeout == 0 {
		timeout = 120 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	result, err := e.dockerExec(ctx, containerID, req)
	if err != nil && isContainerGone(err) {
		// 自愈：重建容器并重试一次
		if rebuildErr := e.rebuild(); rebuildErr != nil {
			return ExecResult{}, fmt.Errorf("container gone and rebuild failed: %w", rebuildErr)
		}
		e.mu.Lock()
		containerID = e.containerID
		e.mu.Unlock()
		return e.dockerExec(ctx, containerID, req)
	}
	return result, err
}

func (e *DockerExecutor) dockerExec(ctx context.Context, containerID string, req ExecRequest) (ExecResult, error) {
	args := []string{"exec"}

	// WorkDir
	if req.WorkDir != "" {
		args = append(args, "-w", req.WorkDir)
	}

	// Env
	for k, v := range req.Env {
		args = append(args, "-e", fmt.Sprintf("%s=%s", k, v))
	}

	args = append(args, containerID, "/bin/bash", "-c", req.Command)

	stdout, stderr, exitCode, err := dockerCmd(ctx, args...)
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

// rebuild 重建容器（自愈机制）。
func (e *DockerExecutor) rebuild() error {
	e.mu.Lock()
	if e.rebuilding {
		// 另一个 goroutine 已在重建，等待完成
		for e.rebuilding {
			e.rebuildCond.Wait()
		}
		e.mu.Unlock()
		return nil
	}
	e.rebuilding = true
	oldID := e.containerID
	e.mu.Unlock()

	e.logger.Warn("沙箱容器不可用，正在重建", zap.String("old_container", oldID[:min(12, len(oldID))]))

	// 清理旧容器（忽略错误）
	dockerCmd(context.Background(), "rm", "-f", oldID)

	err := e.createContainer()

	e.mu.Lock()
	e.rebuilding = false
	e.rebuildCond.Broadcast()
	e.mu.Unlock()

	return err
}

// Close 停止并删除容器。
func (e *DockerExecutor) Close() error {
	e.mu.Lock()
	id := e.containerID
	e.mu.Unlock()

	if id == "" {
		return nil
	}

	_, _, _, _ = dockerCmd(context.Background(), "stop", "-t", "5", id)
	_, _, _, _ = dockerCmd(context.Background(), "rm", "-f", id)
	e.logger.Info("沙箱容器已清理", zap.String("container_id", id[:min(12, len(id))]))
	return nil
}

// CheckDockerAvailable 检查 Docker daemon 是否可用。
func CheckDockerAvailable() error {
	_, stderr, exitCode, err := dockerCmd(context.Background(), "info")
	if err != nil {
		return fmt.Errorf("docker not available: %w", err)
	}
	if exitCode != 0 {
		return fmt.Errorf("docker info failed: %s", stderr)
	}
	return nil
}

// --- helpers ---

func getUIDGID() (string, string) {
	u, err := user.Current()
	if err != nil {
		return "1000", "1000"
	}
	// Docker-in-Docker: root 强制 1000:1000
	if u.Uid == "0" {
		return "1000", "1000"
	}
	return u.Uid, u.Gid
}

func homeSandboxTmpfs(uid, gid string) string {
	return fmt.Sprintf("/home/sandbox:rw,size=64m,uid=%s,gid=%s,mode=1777,noexec,nosuid,nodev", uid, gid)
}

func randomSuffix() string {
	return fmt.Sprintf("%d", time.Now().UnixNano()%1000000)
}

func isContainerGone(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "No such container") ||
		strings.Contains(msg, "is not running") ||
		strings.Contains(msg, "removal") ||
		strings.Contains(msg, "not found")
}

// dockerCmd 执行 docker CLI 命令并返回结果。
func dockerCmd(ctx context.Context, args ...string) (stdout, stderr string, exitCode int, err error) {
	cmd := exec.CommandContext(ctx, "docker", args...)
	var stdoutBuf, stderrBuf bytes.Buffer
	cmd.Stdout = &stdoutBuf
	cmd.Stderr = &stderrBuf

	err = cmd.Run()
	stdout = stdoutBuf.String()
	stderr = stderrBuf.String()

	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
			err = nil // 非零退出码不算 error
		}
	}
	return
}
