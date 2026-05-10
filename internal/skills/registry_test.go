package skills

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/chef-guo/agents-hive/internal/errs"
)

// mockExecutor is a test double for ShellExecutor.
type mockExecutor struct {
	outputs map[string]string
}

func (m *mockExecutor) Execute(command string) (string, string, error) {
	out, ok := m.outputs[command]
	if !ok {
		return "", "", fmt.Errorf("unknown command: %s", command)
	}
	return out, "", nil
}

type recordingSandboxExecutor struct {
	calls []SandboxExecRequest
}

func (e *recordingSandboxExecutor) Execute(_ context.Context, req SandboxExecRequest) (SandboxExecResult, error) {
	e.calls = append(e.calls, req)
	return SandboxExecResult{Stdout: "script-output\n"}, nil
}

func (e *recordingSandboxExecutor) Close() error { return nil }

func newTestRegistry() *Registry {
	logger, _ := zap.NewDevelopment()
	return NewRegistry(logger)
}

func newTestSkill(name string) *Skill {
	return &Skill{
		Metadata: SkillMetadata{Name: name, Description: "test skill"},
		Content:  "Test content for " + name,
		Path:     "/test/" + name,
		Loaded:   LevelFullContent,
	}
}

func TestRegistry_RegisterAndGet(t *testing.T) {
	r := newTestRegistry()
	skill := newTestSkill("test-skill")

	r.Register(skill)

	got, err := r.Get("test-skill")
	if err != nil {
		t.Fatalf("Get returned error: %v", err)
	}
	if got.Metadata.Name != "test-skill" {
		t.Errorf("expected name test-skill, got %s", got.Metadata.Name)
	}
}

func TestRegistry_GetNotFound(t *testing.T) {
	r := newTestRegistry()

	_, err := r.Get("nonexistent")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errs.IsCode(err, errs.CodeSkillNotFound) {
		t.Errorf("expected CodeSkillNotFound, got %v", err)
	}
}

func TestRegistry_HotReload(t *testing.T) {
	r := newTestRegistry()
	oldSkill := &Skill{
		Metadata: SkillMetadata{Name: "test-skill", Description: "original"},
		Content:  "original content",
		Loaded:   LevelFullContent,
	}
	newSkill := &Skill{
		Metadata: SkillMetadata{Name: "test-skill", Description: "updated"},
		Content:  "updated content",
		Loaded:   LevelFullContent,
	}

	r.Register(oldSkill)
	r.Register(newSkill)

	got, _ := r.Get("test-skill")
	if got.Metadata.Description != "updated" {
		t.Errorf("expected description updated after hot-reload, got %s", got.Metadata.Description)
	}
	if r.Count() != 1 {
		t.Errorf("expected count 1, got %d", r.Count())
	}
}

func TestRegistry_Unregister(t *testing.T) {
	r := newTestRegistry()
	r.Register(newTestSkill("test-skill"))

	err := r.Unregister("test-skill")
	if err != nil {
		t.Fatalf("Unregister returned error: %v", err)
	}
	if r.Count() != 0 {
		t.Errorf("expected count 0 after unregister, got %d", r.Count())
	}
}

func TestRegistry_UnregisterNotFound(t *testing.T) {
	r := newTestRegistry()
	err := r.Unregister("nonexistent")
	if !errs.IsCode(err, errs.CodeSkillNotFound) {
		t.Errorf("expected CodeSkillNotFound, got %v", err)
	}
}

func TestRegistry_List(t *testing.T) {
	r := newTestRegistry()
	r.Register(newTestSkill("a"))
	r.Register(newTestSkill("b"))

	list := r.List()
	if len(list) != 2 {
		t.Fatalf("expected 2 skills, got %d", len(list))
	}

	names := map[string]bool{}
	for _, m := range list {
		names[m.Name] = true
	}
	if !names["a"] || !names["b"] {
		t.Errorf("expected skills a and b, got %v", names)
	}
}

func TestRegistry_Invoke(t *testing.T) {
	r := newTestRegistry()
	r.Register(&Skill{
		Metadata: SkillMetadata{Name: "greet"},
		Content:  "Hello, $ARGUMENTS! Welcome.",
		Loaded:   LevelFullContent,
	})

	result, err := r.Invoke("greet", RenderContext{Arguments: "world"})
	if err != nil {
		t.Fatalf("Invoke returned error: %v", err)
	}
	expected := "Hello, world! Welcome."
	if result != expected {
		t.Errorf("expected %q, got %q", expected, result)
	}
}

func TestRegistry_InvokeNotFound(t *testing.T) {
	r := newTestRegistry()
	_, err := r.Invoke("missing", RenderContext{})
	if !errs.IsCode(err, errs.CodeSkillNotFound) {
		t.Errorf("expected CodeSkillNotFound, got %v", err)
	}
}

func TestRegistry_InvokeLazyLoad(t *testing.T) {
	// Create a temp skill that starts at Level 1
	dir := t.TempDir()
	writeTestSkillMD(t, dir, `---
name: lazy-skill
description: test lazy loading
---

Lazy loaded content with $ARGUMENTS.`)

	r := newTestRegistry()
	r.Register(&Skill{
		Metadata: SkillMetadata{Name: "lazy-skill", Description: "test lazy loading"},
		Content:  "", // Level 1 — no content
		Path:     dir,
		Loaded:   LevelMetadataOnly,
	})

	result, err := r.Invoke("lazy-skill", RenderContext{Arguments: "test-arg"})
	if err != nil {
		t.Fatalf("Invoke returned error: %v", err)
	}
	expected := "Lazy loaded content with test-arg."
	if result != expected {
		t.Errorf("expected %q, got %q", expected, result)
	}

	// Verify it's now at Level 2
	s, _ := r.Get("lazy-skill")
	if s.Loaded != LevelFullContent {
		t.Errorf("expected LevelFullContent after Invoke, got %d", s.Loaded)
	}
}

func TestRegistry_ListSummaries(t *testing.T) {
	r := newTestRegistry()
	r.Register(newTestSkill("alpha"))
	r.Register(newTestSkill("beta"))

	summaries := r.ListSummaries()
	if len(summaries) != 2 {
		t.Fatalf("expected 2 summaries, got %d", len(summaries))
	}

	names := map[string]bool{}
	for _, s := range summaries {
		names[s.Name] = true
	}
	if !names["alpha"] || !names["beta"] {
		t.Errorf("expected alpha and beta, got %v", names)
	}
}

func TestRegistry_ListForModel(t *testing.T) {
	r := newTestRegistry()
	r.Register(&Skill{
		Metadata: SkillMetadata{Name: "visible", DisableModelInvocation: false},
		Content:  "visible skill",
		Loaded:   LevelFullContent,
	})
	r.Register(&Skill{
		Metadata: SkillMetadata{Name: "hidden", DisableModelInvocation: true},
		Content:  "hidden skill",
		Loaded:   LevelFullContent,
	})

	list := r.ListForModel()
	if len(list) != 1 {
		t.Fatalf("expected 1 model-visible skill, got %d", len(list))
	}
	if list[0].Name != "visible" {
		t.Errorf("expected visible skill, got %s", list[0].Name)
	}
}

func TestRegistry_Invoke_PopulatesSkillDir(t *testing.T) {
	dir := t.TempDir()
	writeTestSkillMD(t, dir, `---
name: dir-skill
description: test skill dir population
---

Script: ${CLAUDE_SKILL_DIR}/scripts/run.sh`)

	r := newTestRegistry()
	r.Register(&Skill{
		Metadata: SkillMetadata{Name: "dir-skill", Description: "test skill dir population"},
		Content:  "", // Level 1 — no content
		Path:     dir,
		Loaded:   LevelMetadataOnly,
	})

	// Invoke with empty SkillDir — should auto-populate from skill.Path
	result, err := r.Invoke("dir-skill", RenderContext{})
	if err != nil {
		t.Fatalf("Invoke returned error: %v", err)
	}
	expected := "Script: " + dir + "/scripts/run.sh"
	if result != expected {
		t.Errorf("expected %q, got %q", expected, result)
	}
}

func TestRegistry_InvokeWithDynamicContext(t *testing.T) {
	r := newTestRegistry()
	r.Register(&Skill{
		Metadata: SkillMetadata{Name: "dynamic"},
		Content:  "Version: !`echo v1.0`",
		Loaded:   LevelFullContent,
	})

	mock := &mockExecutor{outputs: map[string]string{"echo v1.0": "v1.0"}}
	result, err := r.InvokeWithDynamicContext("dynamic", RenderContext{}, mock)
	if err != nil {
		t.Fatalf("InvokeWithDynamicContext error: %v", err)
	}
	if result != "Version: v1.0" {
		t.Errorf("expected %q, got %q", "Version: v1.0", result)
	}
}

// helpers

func writeTestSkillMD(t *testing.T, dir, content string) {
	t.Helper()
	if err := writeTestFile(dir, "SKILL.md", content); err != nil {
		t.Fatal(err)
	}
}

func writeTestFile(dir, name, content string) error {
	return os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644)
}

// ─────────────────────────────────────────────
// InvokeFull 测试
// ─────────────────────────────────────────────

func TestInvokeFull_BasicPipeline(t *testing.T) {
	r := newTestRegistry()
	r.Register(&Skill{
		Metadata: SkillMetadata{Name: "full-skill"},
		Content:  "Hello $ARGUMENTS",
		Loaded:   LevelFullContent,
	})

	ctx := context.Background()
	result, err := r.InvokeFull(ctx, "full-skill", RenderContext{Arguments: "world"}, nil, nil, nil)
	if err != nil {
		t.Fatalf("InvokeFull error: %v", err)
	}
	if result != "Hello world" {
		t.Errorf("expected %q, got %q", "Hello world", result)
	}
}

func TestInvokeFull_WithDynamicContext(t *testing.T) {
	r := newTestRegistry()
	r.Register(&Skill{
		Metadata: SkillMetadata{Name: "dyn-skill"},
		Content:  "Version: !`echo v2.0`",
		Loaded:   LevelFullContent,
	})

	mock := &mockExecutor{outputs: map[string]string{"echo v2.0": "v2.0"}}
	ctx := context.Background()
	result, err := r.InvokeFull(ctx, "dyn-skill", RenderContext{}, mock, nil, nil)
	if err != nil {
		t.Fatalf("InvokeFull with dynamic context error: %v", err)
	}
	if result != "Version: v2.0" {
		t.Errorf("expected %q, got %q", "Version: v2.0", result)
	}
}

func TestInvokeFull_PreHookFailure_StopsPipeline(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	r := newTestRegistry()
	r.Register(&Skill{
		Metadata: SkillMetadata{
			Name: "hook-fail-skill",
			Hooks: &HookConfig{
				PreInvoke: []string{"exit 1"},
			},
		},
		Content: "Should not render",
		Loaded:  LevelFullContent,
	})

	// hookRunner 的 executor 会对 "exit 1" 返回错误
	failExecutor := &mockExecutor{outputs: map[string]string{}}
	hookRunner := NewHookRunner(failExecutor, logger)

	ctx := context.Background()
	_, err := r.InvokeFull(ctx, "hook-fail-skill", RenderContext{}, nil, nil, hookRunner)
	if err == nil {
		t.Fatal("expected error from pre-invoke hook failure")
	}
}

func TestInvokeFull_PostHookFailure_ReturnsResultAndError(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	r := newTestRegistry()
	r.Register(&Skill{
		Metadata: SkillMetadata{
			Name: "post-hook-skill",
			Hooks: &HookConfig{
				PostInvoke: []string{"cleanup-fail"},
			},
		},
		Content: "Rendered OK $ARGUMENTS",
		Loaded:  LevelFullContent,
	})

	// pre-invoke 无 hook，post-invoke 的命令会失败
	failExecutor := &mockExecutor{outputs: map[string]string{}}
	hookRunner := NewHookRunner(failExecutor, logger)

	ctx := context.Background()
	result, err := r.InvokeFull(ctx, "post-hook-skill", RenderContext{Arguments: "test"}, nil, nil, hookRunner)
	// post-invoke 失败应返回错误
	if err == nil {
		t.Fatal("expected error from post-invoke hook failure")
	}
	// 但渲染结果仍然应该返回（InvokeFull 的行为是 return rendered, err）
	if result != "Rendered OK test" {
		t.Errorf("expected rendered result even on post-hook failure, got %q", result)
	}
}

func TestInvokeFull_WithHooksAndDynamicContext(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	r := newTestRegistry()
	skillDir := "/test/full-pipeline"
	r.Register(&Skill{
		Metadata: SkillMetadata{
			Name: "full-pipeline",
			Hooks: &HookConfig{
				PreInvoke:  []string{"echo setup"},
				PostInvoke: []string{"echo cleanup"},
			},
		},
		Content: "Data: !`fetch-data`",
		Path:    skillDir,
		Loaded:  LevelFullContent,
	})

	// executor 同时用于动态上下文和 hooks
	// hooks 会拼接 cd "{skillDir}" && {command}
	exec := &mockExecutor{outputs: map[string]string{
		"fetch-data": "result-42",
		fmt.Sprintf("cd %q && echo setup", skillDir):   "ok",
		fmt.Sprintf("cd %q && echo cleanup", skillDir): "ok",
	}}
	hookRunner := NewHookRunner(exec, logger)

	ctx := context.Background()
	result, err := r.InvokeFull(ctx, "full-pipeline", RenderContext{}, exec, nil, hookRunner)
	if err != nil {
		t.Fatalf("full pipeline error: %v", err)
	}
	if result != "Data: result-42" {
		t.Errorf("expected %q, got %q", "Data: result-42", result)
	}
}

func TestInvokeFull_SkillNotFound(t *testing.T) {
	r := newTestRegistry()
	ctx := context.Background()
	_, err := r.InvokeFull(ctx, "nonexistent", RenderContext{}, nil, nil, nil)
	if !errs.IsCode(err, errs.CodeSkillNotFound) {
		t.Errorf("expected CodeSkillNotFound, got %v", err)
	}
}

func TestInvokeFull_DoesNotAutoRunBundledScripts(t *testing.T) {
	dir := t.TempDir()

	scriptDir := filepath.Join(dir, "scripts")
	if err := os.MkdirAll(scriptDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeTestFile(scriptDir, "connections.py", "raise SystemExit('helper should not auto-run')\n"); err != nil {
		t.Fatal(err)
	}

	writeTestSkillMD(t, dir, `---
name: mcp-builder
description: skill with helper scripts
---

Base content.`)

	logger, _ := zap.NewDevelopment()
	r := newTestRegistry()
	r.Register(&Skill{
		Metadata: SkillMetadata{Name: "mcp-builder", Description: "skill with helper scripts"},
		Content:  "Base content.",
		Path:     dir,
		Loaded:   LevelFullContent,
		Bundled: BundledFiles{
			Scripts: []string{"connections.py"},
		},
	})

	runner := NewScriptRunner(5*time.Second, logger)
	recorder := &recordingSandboxExecutor{}
	runner.Executor = recorder
	ctx := context.Background()
	result, err := r.InvokeFull(ctx, "mcp-builder", RenderContext{}, nil, runner, nil)
	if err != nil {
		t.Fatalf("InvokeFull error: %v", err)
	}
	if result != "Base content." {
		t.Fatalf("got %q, want base content without script output", result)
	}
	if len(recorder.calls) != 0 {
		t.Fatalf("InvokeFull auto-ran bundled helper scripts: %+v", recorder.calls)
	}
}

// --- Phase 6: Skill Composition ---

func TestRegistry_Register_CircularDependency(t *testing.T) {
	r := newTestRegistry()

	// 注册 skill-a，依赖 skill-b
	skillA := &Skill{
		Metadata: SkillMetadata{Name: "skill-a", DependsOn: []string{"skill-b"}},
		Content:  "A content",
		Path:     "/test/skill-a",
		Loaded:   LevelFullContent,
	}
	if err := r.Register(skillA); err != nil {
		t.Fatalf("Register skill-a failed: %v", err)
	}

	// 注册 skill-b，依赖 skill-a（循环）
	skillB := &Skill{
		Metadata: SkillMetadata{Name: "skill-b", DependsOn: []string{"skill-a"}},
		Content:  "B content",
		Path:     "/test/skill-b",
		Loaded:   LevelFullContent,
	}
	err := r.Register(skillB)
	if err == nil {
		t.Fatal("expected circular dependency error, got nil")
	}
}

func TestRegistry_InvokeChain_WithDependencies(t *testing.T) {
	r := newTestRegistry()

	// 注册依赖 skill
	dep := &Skill{
		Metadata: SkillMetadata{Name: "dep-skill"},
		Content:  "Dependency content",
		Path:     "/test/dep-skill",
		Loaded:   LevelFullContent,
	}
	if err := r.Register(dep); err != nil {
		t.Fatalf("Register dep-skill: %v", err)
	}

	// 注册主 skill，depends-on dep-skill
	main := &Skill{
		Metadata: SkillMetadata{Name: "main-skill", DependsOn: []string{"dep-skill"}},
		Content:  "Main content",
		Path:     "/test/main-skill",
		Loaded:   LevelFullContent,
	}
	if err := r.Register(main); err != nil {
		t.Fatalf("Register main-skill: %v", err)
	}

	ctx := context.Background()
	result, err := r.InvokeChain(ctx, "main-skill", RenderContext{}, nil, nil, nil)
	if err != nil {
		t.Fatalf("InvokeChain error: %v", err)
	}
	if result == "" {
		t.Fatal("expected non-empty result")
	}
	// 结果应包含依赖内容和主 skill 内容
	if !contains(result, "Dependency content") {
		t.Errorf("expected dependency content in result, got: %s", result)
	}
	if !contains(result, "Main content") {
		t.Errorf("expected main content in result, got: %s", result)
	}
}

func TestRegistry_InvokeChain_NoDependencies(t *testing.T) {
	r := newTestRegistry()
	skill := newTestSkill("solo-skill")
	if err := r.Register(skill); err != nil {
		t.Fatalf("Register: %v", err)
	}

	ctx := context.Background()
	result, err := r.InvokeChain(ctx, "solo-skill", RenderContext{}, nil, nil, nil)
	if err != nil {
		t.Fatalf("InvokeChain error: %v", err)
	}
	if result != skill.Content {
		t.Errorf("expected %q, got %q", skill.Content, result)
	}
}

func TestSkill_Render_SkillRef(t *testing.T) {
	// 构建一个简单的 SkillResolver mock
	resolver := &mockSkillResolver{
		results: map[string]string{
			"other-skill": "resolved content",
		},
	}

	s := &Skill{
		Metadata: SkillMetadata{Name: "test"},
		Content:  "Before ${SKILL:other-skill} After",
		Loaded:   LevelFullContent,
	}

	result := s.Render(RenderContext{Resolver: resolver})
	expected := "Before resolved content After"
	if result != expected {
		t.Errorf("expected %q, got %q", expected, result)
	}
}

func TestSkill_Render_SkillRef_WithArgs(t *testing.T) {
	resolver := &mockSkillResolver{
		results: map[string]string{
			"other-skill": "resolved with args",
		},
		capturedArgs: make(map[string]string),
	}

	s := &Skill{
		Metadata: SkillMetadata{Name: "test"},
		Content:  "${SKILL:other-skill:arg1 arg2}",
		Loaded:   LevelFullContent,
	}

	result := s.Render(RenderContext{Resolver: resolver})
	if result != "resolved with args" {
		t.Errorf("expected resolved content, got %q", result)
	}
	if resolver.capturedArgs["other-skill"] != "arg1 arg2" {
		t.Errorf("expected args 'arg1 arg2', got %q", resolver.capturedArgs["other-skill"])
	}
}

func TestSkill_Render_SkillRef_MaxDepth(t *testing.T) {
	// 超过最大深度时，占位符保持原样
	resolver := &mockSkillResolver{
		results: map[string]string{
			"deep-skill": "deep content",
		},
	}

	s := &Skill{
		Metadata: SkillMetadata{Name: "test"},
		Content:  "${SKILL:deep-skill}",
		Loaded:   LevelFullContent,
	}

	// depth=3 时不应再解析
	result := s.Render(RenderContext{Resolver: resolver, depth: maxSkillRefDepth})
	if result != "${SKILL:deep-skill}" {
		t.Errorf("expected placeholder preserved at max depth, got %q", result)
	}
}

func TestSkill_Render_SkillRef_NoResolver(t *testing.T) {
	s := &Skill{
		Metadata: SkillMetadata{Name: "test"},
		Content:  "Before ${SKILL:other-skill} After",
		Loaded:   LevelFullContent,
	}

	// 无 Resolver 时，占位符保持原样
	result := s.Render(RenderContext{})
	expected := "Before ${SKILL:other-skill} After"
	if result != expected {
		t.Errorf("expected placeholder preserved without resolver, got %q", result)
	}
}

// --- Phase 8: Observability ---

func TestMetrics_RecordInvocation(t *testing.T) {
	m := NewMetrics()

	m.RecordInvocation("skill-a", 10*time.Millisecond, nil)
	m.RecordInvocation("skill-a", 5*time.Millisecond, nil)
	m.RecordInvocation("skill-a", 1*time.Millisecond, fmt.Errorf("oops"))

	snap := m.Snapshot()
	invocations, ok := snap["invocations"].(map[string]any)
	if !ok {
		t.Fatal("expected invocations map")
	}
	entry, ok := invocations["skill-a"].(map[string]int64)
	if !ok {
		t.Fatal("expected skill-a entry")
	}
	if entry["count"] != 3 {
		t.Errorf("expected count 3, got %d", entry["count"])
	}
	if entry["errors"] != 1 {
		t.Errorf("expected errors 1, got %d", entry["errors"])
	}
}

func TestMetrics_RecordToolCall(t *testing.T) {
	m := NewMetrics()

	m.RecordToolCall("read_file", 2*time.Millisecond, nil)
	m.RecordToolCall("read_file", 3*time.Millisecond, nil)
	m.RecordToolCall("bash", 100*time.Millisecond, fmt.Errorf("denied"))

	snap := m.Snapshot()
	tools, ok := snap["tools"].(map[string]any)
	if !ok {
		t.Fatal("expected tools map")
	}
	rf, ok := tools["read_file"].(map[string]int64)
	if !ok {
		t.Fatal("expected read_file entry")
	}
	if rf["count"] != 2 {
		t.Errorf("expected count 2, got %d", rf["count"])
	}
	if rf["errors"] != 0 {
		t.Errorf("expected errors 0, got %d", rf["errors"])
	}

	bash, ok := tools["bash"].(map[string]int64)
	if !ok {
		t.Fatal("expected bash entry")
	}
	if bash["errors"] != 1 {
		t.Errorf("expected bash errors 1, got %d", bash["errors"])
	}
}

func TestMetrics_PermissionCounters(t *testing.T) {
	m := NewMetrics()

	m.PermissionAsks.Add(3)
	m.PermissionGrants.Add(2)
	m.PermissionDenies.Add(1)

	snap := m.Snapshot()
	perms, ok := snap["permissions"].(map[string]int64)
	if !ok {
		t.Fatal("expected permissions map")
	}
	if perms["asks"] != 3 {
		t.Errorf("expected asks 3, got %d", perms["asks"])
	}
	if perms["grants"] != 2 {
		t.Errorf("expected grants 2, got %d", perms["grants"])
	}
	if perms["denies"] != 1 {
		t.Errorf("expected denies 1, got %d", perms["denies"])
	}
}

func TestRegistry_SetMetrics_RecordsInvocations(t *testing.T) {
	r := newTestRegistry()
	m := NewMetrics()
	r.SetMetrics(m)

	skill := newTestSkill("metered-skill")
	if err := r.Register(skill); err != nil {
		t.Fatalf("Register: %v", err)
	}

	ctx := context.Background()
	_, err := r.InvokeFull(ctx, "metered-skill", RenderContext{}, nil, nil, nil)
	if err != nil {
		t.Fatalf("InvokeFull: %v", err)
	}

	snap := m.Snapshot()
	invocations := snap["invocations"].(map[string]any)
	entry, ok := invocations["metered-skill"].(map[string]int64)
	if !ok {
		t.Fatal("expected metered-skill in metrics")
	}
	if entry["count"] != 1 {
		t.Errorf("expected count 1, got %d", entry["count"])
	}
}

// --- helpers ---

type mockSkillResolver struct {
	results      map[string]string
	capturedArgs map[string]string
}

func (m *mockSkillResolver) Invoke(name string, rctx RenderContext) (string, error) {
	if m.capturedArgs != nil {
		m.capturedArgs[name] = rctx.Arguments
	}
	if result, ok := m.results[name]; ok {
		return result, nil
	}
	return "", fmt.Errorf("skill %q not found", name)
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(substr) == 0 ||
		func() bool {
			for i := 0; i <= len(s)-len(substr); i++ {
				if s[i:i+len(substr)] == substr {
					return true
				}
			}
			return false
		}())
}
