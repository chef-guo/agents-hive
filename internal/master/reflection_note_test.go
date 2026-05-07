package master

import (
	"strings"
	"testing"
)

func TestBuildReflectionSystemNoteBatchLoop(t *testing.T) {
	got := buildReflectionSystemNote(reflectionNoteInput{Trigger: "batch_loop", Consecutive: 3})
	if !strings.Contains(got, "连续出现 3 次") {
		t.Fatalf("note = %q", got)
	}
}

func TestBuildReflectionSystemNoteCallFailure(t *testing.T) {
	got := buildReflectionSystemNote(reflectionNoteInput{Trigger: "call_failure", ToolName: "websearch", Consecutive: 2})
	if !strings.Contains(got, "工具 websearch") || !strings.Contains(got, "连续失败 2 次") {
		t.Fatalf("note = %q", got)
	}
}

func TestBuildReflectionSystemNoteDoesNotLeakDetail(t *testing.T) {
	detail := strings.Repeat("secret stack trace ", 20)
	got := buildReflectionSystemNote(reflectionNoteInput{Trigger: "guard_failure", Detail: detail})
	if strings.Contains(got, detail) || strings.Contains(got, "secret stack trace") {
		t.Fatalf("note leaked detail: %q", got)
	}
}
