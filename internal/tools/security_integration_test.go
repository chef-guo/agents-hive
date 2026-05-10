package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/chef-guo/agents-hive/internal/mcphost"
	"github.com/chef-guo/agents-hive/internal/sandbox"
	"github.com/chef-guo/agents-hive/internal/search"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zaptest"
)

// mockSafeExec 模拟的安全执行器
type mockSafeExec struct {
	rules map[string]string // pattern -> policy
}

func (m *mockSafeExec) MatchPolicy(command string) string {
	for pattern, policy := range m.rules {
		if strings.Contains(command, pattern) {
			return policy
		}
	}
	return "allow"
}

// setupTestExecutorWithChecker 创建一个带安全检查器的测试 executor，
// 注入到 globalExecutor，返回清理函数。
func setupTestExecutorWithChecker(t *testing.T, checker sandbox.SafeExecChecker) func() {
	t.Helper()
	shell, err := NewPersistentShell()
	require.NoError(t, err)

	inner := sandbox.NewLocalExecutor(shell, nil)
	wrapper := sandbox.NewSafeExecutorWrapper(inner, checker)

	old := globalExecutor
	globalExecutor = wrapper

	// 初始化全局搜索引擎（使用同一 executor）
	oldGrep := globalGrepEngine
	globalGrepEngine = search.NewShellGrep(wrapper)

	return func() {
		globalExecutor = old
		globalGrepEngine = oldGrep
		shell.Close()
	}
}

// setupTestExecutorNoChecker 创建一个不带安全检查器的测试 executor。
func setupTestExecutorNoChecker(t *testing.T) func() {
	t.Helper()
	shell, err := NewPersistentShell()
	require.NoError(t, err)

	inner := sandbox.NewLocalExecutor(shell, nil)
	wrapper := sandbox.NewSafeExecutorWrapper(inner, nil)

	old := globalExecutor
	globalExecutor = wrapper
	return func() {
		globalExecutor = old
		shell.Close()
	}
}

// TestBash_SecurityDeny 测试 bash 工具通过 SafeExecutorWrapper 的 deny 策略
func TestBash_SecurityDeny(t *testing.T) {
	logger := zaptest.NewLogger(t)
	host := mcphost.NewHost(logger)

	checker := &mockSafeExec{rules: map[string]string{"rm -rf": "deny"}}
	cleanup := setupTestExecutorWithChecker(t, checker)
	defer cleanup()

	registerBash(host, logger, nil)

	input := map[string]any{"command": "rm -rf /tmp/test"}
	inputJSON, _ := json.Marshal(input)

	result, err := host.ExecuteTool(context.Background(), "bash", inputJSON)
	require.NoError(t, err)

	assert.True(t, result.IsError)
	assert.Contains(t, string(result.Content), "命令被安全策略拒绝")
}

// TestBash_SecurityAsk 测试 bash 工具的 ask 策略：
// ask 策略由 globalSafeExec 层处理 HITL 审批，SafeExecutorWrapper 层透传执行。
// 此测试只注入 SafeExecutorWrapper checker，不注入 globalSafeExec，
// 因此 ask 策略透传，命令正常执行（curl 可能失败但不是安全拒绝）。
func TestBash_SecurityAsk(t *testing.T) {
	logger := zaptest.NewLogger(t)
	host := mcphost.NewHost(logger)

	// 只注入 SafeExecutorWrapper checker（不注入 globalSafeExec），
	// ask 策略透传，命令正常执行
	checker := &mockSafeExec{rules: map[string]string{"curl": "ask"}}
	cleanup := setupTestExecutorWithChecker(t, checker)
	defer cleanup()

	registerBash(host, logger, nil)

	input := map[string]any{"command": "curl http://example.com"}
	inputJSON, _ := json.Marshal(input)

	result, err := host.ExecuteTool(context.Background(), "bash", inputJSON)
	require.NoError(t, err)

	// SafeExecutorWrapper 层不再拦截 ask，命令会尝试执行（curl 可能失败但不是安全拒绝）
	// 关键：错误信息不应包含"命令需要审批"（那是旧的 wrapper 层拦截行为）
	assert.NotContains(t, string(result.Content), "命令需要审批但审批系统未初始化")
}

// TestBash_SecurityAllow 测试 bash 工具通过 SafeExecutorWrapper 的 allow 策略
func TestBash_SecurityAllow(t *testing.T) {
	logger := zaptest.NewLogger(t)
	host := mcphost.NewHost(logger)

	checker := &mockSafeExec{rules: map[string]string{"echo": "allow"}}
	cleanup := setupTestExecutorWithChecker(t, checker)
	defer cleanup()

	registerBash(host, logger, nil)

	input := map[string]any{"command": "echo hello"}
	inputJSON, _ := json.Marshal(input)

	result, err := host.ExecuteTool(context.Background(), "bash", inputJSON)
	require.NoError(t, err)

	assert.False(t, result.IsError, "Command should execute successfully")
}

func TestBash_RuntimePermissionDiagnostic(t *testing.T) {
	logger := zaptest.NewLogger(t)
	host := mcphost.NewHost(logger)

	old := globalExecutor
	globalExecutor = &diagnosticExecutor{result: sandbox.ExecResult{
		Stderr:   "go: could not create module cache: mkdir /home/sandbox/go: permission denied",
		ExitCode: 1,
		Diagnostic: &sandbox.ExecFailureDiagnostic{
			FailureType:     sandbox.FailureTypePermissionDenied,
			Summary:         "Go 模块缓存路径不可写，当前执行身份无法创建默认 GOPATH/GOMODCACHE 目录",
			SuggestedAction: sandbox.ActionUseWritableGoCache,
			SuggestedEnv: map[string]string{
				"GOCACHE":    "/tmp/go-build-cache",
				"GOMODCACHE": "/tmp/go-mod-cache",
				"GOPATH":     "/tmp/go",
			},
		},
	}}
	t.Cleanup(func() { globalExecutor = old })

	registerBash(host, logger, nil)

	inputJSON, _ := json.Marshal(map[string]any{"command": "go test ./..."})
	result, err := host.ExecuteTool(context.Background(), "bash", inputJSON)
	require.NoError(t, err)
	require.True(t, result.IsError)

	content := mcphost.DecodeToolContent(result.Content)
	assert.Contains(t, content, "诊断")
	assert.Contains(t, content, "Go 模块缓存路径不可写")
	assert.Contains(t, content, "GOMODCACHE=/tmp/go-mod-cache")
	assert.Contains(t, content, "GOPATH=/tmp/go")
}

type diagnosticExecutor struct {
	result sandbox.ExecResult
	err    error
}

func (e *diagnosticExecutor) Execute(context.Context, sandbox.ExecRequest) (sandbox.ExecResult, error) {
	return e.result, e.err
}

func (e *diagnosticExecutor) Close() error { return nil }

func TestFormatCommandFailure_GenericPermissionAsksForApproval(t *testing.T) {
	content := formatCommandFailure(sandbox.ExecResult{
		ExitCode: 1,
		Stderr:   "mkdir: cannot create directory '/var/lib/app': Permission denied",
		Diagnostic: &sandbox.ExecFailureDiagnostic{
			FailureType:          sandbox.FailureTypePermissionDenied,
			Summary:              "当前执行身份或沙箱挂载没有足够权限完成命令",
			RequiresUserApproval: true,
			SuggestedAction:      sandbox.ActionRequestPrivilegedExecution,
		},
	})

	assert.Contains(t, content, "需要用户批准切换到有权限的执行环境")
	assert.Contains(t, content, "Permission denied")
}

// TestGrep_SecurityCheck 测试 grep 工具通过 SafeExecutorWrapper 的 deny 策略
func TestGrep_SecurityCheck(t *testing.T) {
	logger := zaptest.NewLogger(t)
	host := mcphost.NewHost(logger)

	checker := &mockSafeExec{rules: map[string]string{"password": "deny"}}
	cleanup := setupTestExecutorWithChecker(t, checker)
	defer cleanup()

	registerGrep(host, logger)

	input := map[string]any{"pattern": "password", "path": "."}
	inputJSON, _ := json.Marshal(input)

	result, err := host.ExecuteTool(context.Background(), "grep", inputJSON)
	require.NoError(t, err)

	assert.True(t, result.IsError)
	assert.Contains(t, string(result.Content), "命令被安全策略拒绝")
}

// TestGrep_SecurityAsk 测试 grep 工具的 ask 策略：SafeExecutorWrapper 层透传，不拦截。
func TestGrep_SecurityAsk(t *testing.T) {
	logger := zaptest.NewLogger(t)
	host := mcphost.NewHost(logger)

	checker := &mockSafeExec{rules: map[string]string{"secret": "ask"}}
	cleanup := setupTestExecutorWithChecker(t, checker)
	defer cleanup()

	registerGrep(host, logger)

	input := map[string]any{"pattern": "secret", "path": "."}
	inputJSON, _ := json.Marshal(input)

	result, err := host.ExecuteTool(context.Background(), "grep", inputJSON)
	require.NoError(t, err)

	// SafeExecutorWrapper 层不再拦截 ask，grep 会正常执行（可能无匹配但不是安全拒绝）
	assert.NotContains(t, string(result.Content), "命令需要审批")
}

// TestCustomLoader_SecurityCheck 测试自定义工具通过 SafeExecutorWrapper 的安全检查
func TestCustomLoader_SecurityCheck(t *testing.T) {
	logger := zaptest.NewLogger(t)

	checker := &mockSafeExec{rules: map[string]string{"rm -rf": "deny"}}
	cleanup := setupTestExecutorWithChecker(t, checker)
	defer cleanup()

	tool := CustomTool{
		Name:        "test_tool",
		Type:        "shell",
		Command:     "rm -rf /tmp/test",
		AllowWrite:  true,
		Timeout:     10,
		Description: "Test tool",
	}

	_, err := executeShellTool(tool, nil, logger)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "命令被安全策略拒绝")
}

// TestSecurity_NoRules 测试没有安全检查器时的正常行为
func TestSecurity_NoRules(t *testing.T) {
	logger := zaptest.NewLogger(t)
	host := mcphost.NewHost(logger)

	cleanup := setupTestExecutorNoChecker(t)
	defer cleanup()

	registerBash(host, logger, nil)

	input := map[string]any{"command": "echo test"}
	inputJSON, _ := json.Marshal(input)

	result, err := host.ExecuteTool(context.Background(), "bash", inputJSON)
	require.NoError(t, err)

	assert.False(t, result.IsError)
}

// TestBash_ExecutorNil 测试 globalExecutor 为 nil 时 fail closed
func TestBash_ExecutorNil(t *testing.T) {
	logger := zaptest.NewLogger(t)
	host := mcphost.NewHost(logger)

	old := globalExecutor
	globalExecutor = nil
	defer func() { globalExecutor = old }()

	registerBash(host, logger, nil)

	input := map[string]any{"command": "echo hello"}
	inputJSON, _ := json.Marshal(input)

	result, err := host.ExecuteTool(context.Background(), "bash", inputJSON)
	require.NoError(t, err)

	assert.True(t, result.IsError)
	assert.Contains(t, string(result.Content), "沙箱执行器未初始化")
}
