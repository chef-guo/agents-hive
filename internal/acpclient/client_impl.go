package acpclient

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	acp "github.com/coder/acp-go-sdk"
	"go.uber.org/zap"
)

// acpClientImpl 实现 acp.Client 接口，处理远程 Agent 的反向请求。
// agent-to-agent 场景下，大部分方法返回 NotSupported，
// 仅实现 SessionUpdate 接收流式进度。
type acpClientImpl struct {
	agentName     string
	logger        *zap.Logger
	workspaceRoot string
	mu            sync.Mutex
	terminalSeq   int
	terminals     map[string]acpTerminal
	// onUpdate 回调，用于接收远程 Agent 的会话更新通知
	onUpdate func(acp.SessionNotification)
}

func newACPClientImpl(agentName string, logger *zap.Logger) *acpClientImpl {
	cwd, err := os.Getwd()
	if err != nil {
		cwd = "."
	}
	return &acpClientImpl{
		agentName:     agentName,
		logger:        logger.With(zap.String("remote_agent", agentName)),
		workspaceRoot: cwd,
		terminals:     map[string]acpTerminal{},
	}
}

func (c *acpClientImpl) notSupported(method string) error {
	return fmt.Errorf("远程 ACP Agent %q 反向调用 %s 不支持", c.agentName, method)
}

// ReadTextFile 远程 Agent 请求读取工作区内文本文件。
func (c *acpClientImpl) ReadTextFile(_ context.Context, req acp.ReadTextFileRequest) (acp.ReadTextFileResponse, error) {
	path, err := c.safeWorkspacePath(req.Path)
	if err != nil {
		return acp.ReadTextFileResponse{}, err
	}

	b, err := os.ReadFile(path)
	if err != nil {
		return acp.ReadTextFileResponse{}, err
	}
	content := string(b)
	if req.Line != nil || req.Limit != nil {
		content = sliceTextLines(content, req.Line, req.Limit)
	}
	return acp.ReadTextFileResponse{Content: content}, nil
}

// WriteTextFile 远程 Agent 请求写入文件。
// 只允许创建工作区内的新文件；覆盖/截断已有文件仍需要人工审批桥接。
func (c *acpClientImpl) WriteTextFile(_ context.Context, req acp.WriteTextFileRequest) (acp.WriteTextFileResponse, error) {
	path, err := c.safeWorkspaceWritePath(req.Path)
	if err != nil {
		return acp.WriteTextFileResponse{}, err
	}
	if _, err := os.Lstat(path); err == nil {
		return acp.WriteTextFileResponse{}, fmt.Errorf("覆盖已有文件需要人工审批: %s", req.Path)
	} else if !os.IsNotExist(err) {
		return acp.WriteTextFileResponse{}, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return acp.WriteTextFileResponse{}, err
	}
	if err := os.WriteFile(path, []byte(req.Content), 0o644); err != nil {
		return acp.WriteTextFileResponse{}, err
	}
	return acp.WriteTextFileResponse{Meta: map[string]any{"path": path, "created": true}}, nil
}

// RequestPermission 远程 Agent 请求权限。
// 普通读取自动放行；删除、覆盖等危险操作在无人工审批桥接时保守拒绝。
func (c *acpClientImpl) RequestPermission(_ context.Context, req acp.RequestPermissionRequest) (acp.RequestPermissionResponse, error) {
	if permissionRequestLooksDangerous(req) {
		if opt, ok := selectPermissionOption(req.Options, acp.PermissionOptionKindRejectOnce, acp.PermissionOptionKindRejectAlways); ok {
			return acp.RequestPermissionResponse{Outcome: acp.NewRequestPermissionOutcomeSelected(opt.OptionId)}, nil
		}
		return acp.RequestPermissionResponse{Outcome: acp.NewRequestPermissionOutcomeCancelled()}, nil
	}
	if opt, ok := selectPermissionOption(req.Options, acp.PermissionOptionKindAllowOnce, acp.PermissionOptionKindAllowAlways); ok {
		return acp.RequestPermissionResponse{Outcome: acp.NewRequestPermissionOutcomeSelected(opt.OptionId)}, nil
	}
	return acp.RequestPermissionResponse{Outcome: acp.NewRequestPermissionOutcomeCancelled()}, nil
}

// SessionUpdate 接收远程 Agent 的会话更新通知（流式进度）
func (c *acpClientImpl) SessionUpdate(_ context.Context, params acp.SessionNotification) error {
	if c.onUpdate != nil {
		c.onUpdate(params)
	}
	return nil
}

type acpTerminal struct {
	output    string
	truncated bool
	exitCode  *int
	signal    *string
}

// CreateTerminal 远程 Agent 请求创建终端。
// 只允许明确安全的只读命令；危险命令在无人工审批桥接时直接拒绝。
func (c *acpClientImpl) CreateTerminal(ctx context.Context, req acp.CreateTerminalRequest) (acp.CreateTerminalResponse, error) {
	if err := c.validateSafeTerminalRequest(req); err != nil {
		return acp.CreateTerminalResponse{}, err
	}
	cwd := c.workspaceRoot
	if req.Cwd != nil && strings.TrimSpace(*req.Cwd) != "" {
		path, err := c.safeWorkspacePath(*req.Cwd)
		if err != nil {
			return acp.CreateTerminalResponse{}, err
		}
		cwd = path
	}

	runCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(runCtx, req.Command, req.Args...)
	cmd.Dir = cwd
	output, err := cmd.CombinedOutput()
	term := acpTerminal{output: string(output)}
	if req.OutputByteLimit != nil && *req.OutputByteLimit >= 0 {
		term.output, term.truncated = truncateOutput(term.output, *req.OutputByteLimit)
	}
	if exitErr, ok := err.(*exec.ExitError); ok {
		code := exitErr.ExitCode()
		term.exitCode = &code
	} else if err != nil {
		return acp.CreateTerminalResponse{}, err
	} else {
		code := 0
		term.exitCode = &code
	}
	if runCtx.Err() == context.DeadlineExceeded {
		signal := "timeout"
		term.signal = &signal
		code := -1
		term.exitCode = &code
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	c.terminalSeq++
	id := fmt.Sprintf("term_%06d", c.terminalSeq)
	c.terminals[id] = term
	return acp.CreateTerminalResponse{TerminalId: id}, nil
}

// KillTerminal 远程 Agent 请求终止终端命令（不释放终端资源）。
func (c *acpClientImpl) KillTerminal(_ context.Context, req acp.KillTerminalRequest) (acp.KillTerminalResponse, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	term, ok := c.terminals[req.TerminalId]
	if !ok {
		return acp.KillTerminalResponse{}, fmt.Errorf("terminal %s 不存在", req.TerminalId)
	}
	signal := "killed"
	term.signal = &signal
	if term.exitCode == nil {
		code := -1
		term.exitCode = &code
	}
	c.terminals[req.TerminalId] = term
	return acp.KillTerminalResponse{}, nil
}

// TerminalOutput 远程 Agent 请求获取终端输出。
func (c *acpClientImpl) TerminalOutput(_ context.Context, req acp.TerminalOutputRequest) (acp.TerminalOutputResponse, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	term, ok := c.terminals[req.TerminalId]
	if !ok {
		return acp.TerminalOutputResponse{}, fmt.Errorf("terminal %s 不存在", req.TerminalId)
	}
	return acp.TerminalOutputResponse{
		Output:    term.output,
		Truncated: term.truncated,
		ExitStatus: &acp.TerminalExitStatus{
			ExitCode: term.exitCode,
			Signal:   term.signal,
		},
	}, nil
}

// ReleaseTerminal 远程 Agent 请求释放终端。
func (c *acpClientImpl) ReleaseTerminal(_ context.Context, req acp.ReleaseTerminalRequest) (acp.ReleaseTerminalResponse, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.terminals[req.TerminalId]; !ok {
		return acp.ReleaseTerminalResponse{}, fmt.Errorf("terminal %s 不存在", req.TerminalId)
	}
	delete(c.terminals, req.TerminalId)
	return acp.ReleaseTerminalResponse{}, nil
}

// WaitForTerminalExit 远程 Agent 请求等待终端退出。
func (c *acpClientImpl) WaitForTerminalExit(_ context.Context, req acp.WaitForTerminalExitRequest) (acp.WaitForTerminalExitResponse, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	term, ok := c.terminals[req.TerminalId]
	if !ok {
		return acp.WaitForTerminalExitResponse{}, fmt.Errorf("terminal %s 不存在", req.TerminalId)
	}
	return acp.WaitForTerminalExitResponse{ExitCode: term.exitCode, Signal: term.signal}, nil
}

func (c *acpClientImpl) safeWorkspacePath(path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("路径不能为空")
	}
	if !filepath.IsAbs(path) {
		return "", fmt.Errorf("路径必须是绝对路径: %s", path)
	}

	root, err := filepath.Abs(c.workspaceRoot)
	if err != nil {
		return "", err
	}
	if resolvedRoot, err := filepath.EvalSymlinks(root); err == nil {
		root = resolvedRoot
	}

	cleanPath := filepath.Clean(path)
	resolvedPath, err := filepath.EvalSymlinks(cleanPath)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(root, resolvedPath)
	if err != nil {
		return "", err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", fmt.Errorf("路径逃逸工作区: %s", path)
	}
	return resolvedPath, nil
}

func (c *acpClientImpl) safeWorkspaceWritePath(path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("路径不能为空")
	}
	if !filepath.IsAbs(path) {
		return "", fmt.Errorf("路径必须是绝对路径: %s", path)
	}

	root, err := filepath.Abs(c.workspaceRoot)
	if err != nil {
		return "", err
	}
	if resolvedRoot, err := filepath.EvalSymlinks(root); err == nil {
		root = resolvedRoot
	}

	cleanPath := filepath.Clean(path)
	parent := filepath.Dir(cleanPath)
	resolvedParent, err := filepath.EvalSymlinks(parent)
	if err != nil {
		if !os.IsNotExist(err) {
			return "", err
		}
		nearest := parent
		var missing []string
		for {
			if nearest == "" || nearest == string(filepath.Separator) || nearest == "." {
				return "", err
			}
			if resolved, evalErr := filepath.EvalSymlinks(nearest); evalErr == nil {
				resolvedParent = filepath.Join(append([]string{resolved}, reverseStrings(missing)...)...)
				break
			} else if !os.IsNotExist(evalErr) {
				return "", evalErr
			}
			missing = append(missing, filepath.Base(nearest))
			nearest = filepath.Dir(nearest)
		}
	}
	resolvedPath := filepath.Join(resolvedParent, filepath.Base(cleanPath))
	rel, err := filepath.Rel(root, resolvedPath)
	if err != nil {
		return "", err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", fmt.Errorf("路径逃逸工作区: %s", path)
	}
	return resolvedPath, nil
}

func reverseStrings(values []string) []string {
	out := append([]string(nil), values...)
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

func (c *acpClientImpl) validateSafeTerminalRequest(req acp.CreateTerminalRequest) error {
	if strings.TrimSpace(req.Command) == "" {
		return fmt.Errorf("终端命令不能为空")
	}
	if len(req.Env) > 0 {
		return fmt.Errorf("终端环境变量暂不支持，避免远程 Agent 注入执行环境")
	}
	command := filepath.Base(req.Command)
	if command != req.Command || strings.Contains(req.Command, string(filepath.Separator)) {
		return fmt.Errorf("终端命令必须是允许列表中的命令名: %s", req.Command)
	}
	if terminalCommandLooksDangerous(req.Command, req.Args) {
		return fmt.Errorf("危险终端命令已拒绝: %s", strings.Join(append([]string{req.Command}, req.Args...), " "))
	}
	if !terminalCommandAllowed(req.Command, req.Args) {
		return fmt.Errorf("终端命令不在只读允许列表中: %s", req.Command)
	}
	return nil
}

func sliceTextLines(content string, line *int, limit *int) string {
	lines := strings.Split(content, "\n")
	start := 0
	if line != nil && *line > 1 {
		start = *line - 1
		if start > len(lines) {
			start = len(lines)
		}
	}
	end := len(lines)
	if limit != nil && *limit >= 0 && start+*limit < end {
		end = start + *limit
	}
	return strings.Join(lines[start:end], "\n")
}

func selectPermissionOption(options []acp.PermissionOption, kinds ...acp.PermissionOptionKind) (acp.PermissionOption, bool) {
	for _, kind := range kinds {
		for _, opt := range options {
			if opt.Kind == kind {
				return opt, true
			}
		}
	}
	return acp.PermissionOption{}, false
}

func permissionRequestLooksDangerous(req acp.RequestPermissionRequest) bool {
	var parts []string
	if req.ToolCall.Title != nil {
		parts = append(parts, *req.ToolCall.Title)
	}
	if req.ToolCall.Kind != nil {
		parts = append(parts, string(*req.ToolCall.Kind))
	}
	for _, loc := range req.ToolCall.Locations {
		parts = append(parts, loc.Path)
	}
	parts = append(parts, fmt.Sprint(req.ToolCall.RawInput))
	haystack := strings.ToLower(strings.Join(parts, " "))

	for _, marker := range []string{
		"delete", "remove", "rm -", "rm ", "unlink",
		"overwrite", "truncate", "write", "edit", "modify",
		"删除", "移除", "覆盖", "截断", "写入", "修改",
	} {
		if strings.Contains(haystack, marker) {
			return true
		}
	}
	return false
}

func terminalCommandAllowed(command string, args []string) bool {
	switch command {
	case "pwd", "ls", "cat", "head", "tail", "wc", "rg":
		return true
	case "git":
		if len(args) == 0 {
			return false
		}
		switch args[0] {
		case "status", "diff", "log", "show", "branch":
			return true
		default:
			return false
		}
	default:
		return false
	}
}

func terminalCommandLooksDangerous(command string, args []string) bool {
	parts := append([]string{command}, args...)
	haystack := strings.ToLower(strings.Join(parts, " "))
	for _, marker := range []string{
		"rm", "remove", "delete", "unlink",
		"mv", "cp", "touch", "mkdir", "rmdir",
		"chmod", "chown", "truncate", "dd", "tee",
		"git reset", "git checkout", "git clean", "git apply", "git commit", "git push", "git merge", "git rebase",
		"curl", "wget", "scp", "ssh",
		"sh", "bash", "zsh", "python", "python3", "node", "npm", "go",
		">", ">>", "|", "&&", "||", ";", "`", "$(",
		"删除", "移除", "覆盖", "截断", "写入", "修改",
	} {
		for _, part := range parts {
			if strings.EqualFold(part, marker) {
				return true
			}
		}
		if strings.Contains(haystack, marker+" ") || strings.Contains(haystack, " "+marker) {
			return true
		}
	}
	return false
}

func truncateOutput(output string, limit int) (string, bool) {
	if limit < 0 || len(output) <= limit {
		return output, false
	}
	if limit == 0 {
		return "", true
	}
	start := len(output) - limit
	for start < len(output) && !utf8.RuneStart(output[start]) {
		start++
	}
	return output[start:], true
}
