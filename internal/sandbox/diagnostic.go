package sandbox

import "strings"

const (
	FailureTypePermissionDenied = "permission_denied"

	ActionRequestPrivilegedExecution = "request_privileged_execution"
	ActionUseWritableGoCache         = "use_writable_go_cache"
)

// DiagnoseExecFailure classifies runtime failures that the shell process was able
// to report. It intentionally does not classify command syntax errors or test
// failures; those should remain normal non-zero exits.
func DiagnoseExecFailure(req ExecRequest, result ExecResult, execErr error) *ExecFailureDiagnostic {
	if execErr == nil && result.ExitCode == 0 {
		return nil
	}

	raw := strings.TrimSpace(strings.Join([]string{result.Stdout, result.Stderr, errorString(execErr)}, "\n"))
	if raw == "" {
		return nil
	}
	lower := strings.ToLower(raw)

	if isGoModuleCachePermissionFailure(lower) {
		return &ExecFailureDiagnostic{
			FailureType:          FailureTypePermissionDenied,
			Summary:              "Go 模块缓存路径不可写，当前执行身份无法创建默认 GOPATH/GOMODCACHE 目录",
			RequiresUserApproval: false,
			SuggestedAction:      ActionUseWritableGoCache,
			SuggestedEnv: map[string]string{
				"GOCACHE":    "/tmp/go-build-cache",
				"GOMODCACHE": "/tmp/go-mod-cache",
				"GOPATH":     "/tmp/go",
			},
		}
	}

	if isRuntimePermissionFailure(lower) {
		return &ExecFailureDiagnostic{
			FailureType:          FailureTypePermissionDenied,
			Summary:              "当前执行身份或沙箱挂载没有足够权限完成命令",
			RequiresUserApproval: true,
			SuggestedAction:      ActionRequestPrivilegedExecution,
		}
	}

	return nil
}

func attachExecDiagnostic(req ExecRequest, result ExecResult, execErr error) ExecResult {
	if result.Diagnostic == nil {
		result.Diagnostic = DiagnoseExecFailure(req, result, execErr)
	}
	return result
}

func isRuntimePermissionFailure(lower string) bool {
	return strings.Contains(lower, "permission denied") ||
		strings.Contains(lower, "operation not permitted") ||
		strings.Contains(lower, "access denied") ||
		strings.Contains(lower, "read-only file system")
}

func isGoModuleCachePermissionFailure(lower string) bool {
	if !strings.Contains(lower, "permission denied") {
		return false
	}
	return strings.Contains(lower, "could not create module cache") ||
		strings.Contains(lower, "gomodcache") ||
		strings.Contains(lower, "gopath") ||
		strings.Contains(lower, "/home/sandbox/go")
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
