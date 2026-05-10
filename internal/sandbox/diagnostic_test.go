package sandbox

import (
	"strings"
	"testing"
)

func TestDiagnoseExecFailure_GoModuleCachePermission(t *testing.T) {
	result := ExecResult{
		ExitCode: 1,
		Stderr:   "go: could not create module cache: mkdir /home/sandbox/go: permission denied",
	}

	diag := DiagnoseExecFailure(ExecRequest{Command: "go test ./..."}, result, nil)
	if diag == nil {
		t.Fatal("expected diagnostic")
	}
	if diag.FailureType != FailureTypePermissionDenied {
		t.Fatalf("failure type = %q, want %q", diag.FailureType, FailureTypePermissionDenied)
	}
	if diag.SuggestedAction != ActionUseWritableGoCache {
		t.Fatalf("suggested action = %q, want %q", diag.SuggestedAction, ActionUseWritableGoCache)
	}
	if diag.RequiresUserApproval {
		t.Fatal("go cache permission should suggest writable cache env before user escalation")
	}
	if diag.SuggestedEnv["GOMODCACHE"] == "" || diag.SuggestedEnv["GOPATH"] == "" {
		t.Fatalf("expected Go cache env suggestions, got %#v", diag.SuggestedEnv)
	}
}

func TestDiagnoseExecFailure_GenericPermissionDeniedRequiresApproval(t *testing.T) {
	result := ExecResult{
		ExitCode: 1,
		Stderr:   "mkdir: cannot create directory '/var/lib/app': Permission denied",
	}

	diag := DiagnoseExecFailure(ExecRequest{Command: "mkdir /var/lib/app"}, result, nil)
	if diag == nil {
		t.Fatal("expected diagnostic")
	}
	if diag.FailureType != FailureTypePermissionDenied {
		t.Fatalf("failure type = %q, want %q", diag.FailureType, FailureTypePermissionDenied)
	}
	if !diag.RequiresUserApproval {
		t.Fatal("generic permission denied should require user approval")
	}
	if diag.SuggestedAction != ActionRequestPrivilegedExecution {
		t.Fatalf("suggested action = %q, want %q", diag.SuggestedAction, ActionRequestPrivilegedExecution)
	}
}

func TestDiagnoseExecFailure_NonPermissionExitIsNotClassified(t *testing.T) {
	result := ExecResult{ExitCode: 1, Stderr: "package ./foo: no Go files"}

	if diag := DiagnoseExecFailure(ExecRequest{Command: "go test ./foo"}, result, nil); diag != nil {
		t.Fatalf("unexpected diagnostic: %#v", diag)
	}
}

func TestHomeSandboxTmpfsIncludesCurrentUIDGID(t *testing.T) {
	got := homeSandboxTmpfs("501", "20")
	for _, want := range []string{"uid=501", "gid=20", "mode=1777"} {
		if !strings.Contains(got, want) {
			t.Fatalf("tmpfs options %q missing %q", got, want)
		}
	}
}
