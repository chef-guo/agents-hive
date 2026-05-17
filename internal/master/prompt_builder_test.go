package master

import (
	"strings"
	"testing"

	"github.com/chef-guo/agents-hive/internal/config"
	"github.com/chef-guo/agents-hive/internal/i18n"
	"github.com/chef-guo/agents-hive/internal/mcphost"
	"github.com/chef-guo/agents-hive/internal/skills"
	"github.com/chef-guo/agents-hive/internal/store"
	"github.com/chef-guo/agents-hive/internal/subagent"
	"github.com/stretchr/testify/assert"
	"go.uber.org/zap/zaptest"
)

func newTestMaster(t *testing.T) (*Master, *skills.Registry) {
	t.Helper()
	logger := zaptest.NewLogger(t)
	skillReg := skills.NewRegistry(logger)
	st := store.NewMemoryStore()
	registry := subagent.NewRegistry(logger)
	cfg := Config{}
	hitlCfg := config.HITLConfig{Enabled: false}
	return NewMaster(cfg, hitlCfg, registry, skillReg, st, logger), skillReg
}

func TestMasterPrompt_NoFixedAgentReference(t *testing.T) {
	m, _ := newTestMaster(t)
	prompt := m.buildSystemPrompt(nil)

	// 不应引用已删除的固定 Agent
	forbidden := []string{
		"general / code / research / ops",
		"code / research / ops / general",
		"固定 Agent",
		"固定Agent",
		"系统已有固定 Agent",
	}
	for _, s := range forbidden {
		assert.False(t, strings.Contains(prompt, s),
			"prompt 不应包含固定 Agent 引用: %q", s)
	}
}

func TestBuildSystemPrompt_SkillListing_DomainMetadata(t *testing.T) {
	m, skillReg := newTestMaster(t)

	// 注册一个带 domain/trigger_keywords 的 skill
	tr := true
	if err := skillReg.Register(&skills.Skill{
		Metadata: skills.SkillMetadata{
			Name:            "roi-analysis",
			Description:     "ROI 分析规范",
			Domain:          "analytics",
			TriggerKeywords: []string{"ROI", "投资回报"},
			Priority:        7,
			Complexity:      "medium",
			UserInvocable:   &tr,
		},
		Content: "ROI analysis content",
		Loaded:  skills.LevelMetadataOnly,
	}); err != nil {
		t.Fatalf("Register error: %v", err)
	}

	prompt := m.buildSystemPrompt(nil)

	assert.True(t, strings.Contains(prompt, "roi-analysis"), "prompt 应包含 skill 名称")
	assert.True(t, strings.Contains(prompt, "领域: analytics"), "prompt 应包含域信息")
	assert.True(t, strings.Contains(prompt, "ROI"), "prompt 应包含触发词")
}

func TestMasterPrompt_ContainsKeyGuidance(t *testing.T) {
	m, _ := newTestMaster(t)
	prompt := m.buildSystemPrompt([]mcphost.ToolDefinition{
		{Name: "bash", Description: "执行命令", Core: true},
	})

	required := []struct {
		section string
		keyword string
	}{
		{"身份定义", "你是 Hive"},
		{"任务执行策略", "优先直接使用工具"},
		{"工具选择指南-并行", "parallel_dispatch"},
		{"工具选择指南-信息获取", "聚焦可执行结论"},
		{"工具选择指南-业务写入发现", "先用 tool_search 查找可用工具与 action_capabilities"},
		{"工具选择指南-action不是工具", "action_capabilities 中的 action 不是独立工具名"},
		{"工具选择指南-飞书任务调用契约", "arguments.action=create_task"},
		{"可恢复工具错误", "recoverable_tool_call"},
		{"可恢复工具错误-自动修复", "自动修复下一次工具调用"},
		{"可恢复工具错误-禁止直接结束", "不要回复“没有可调用工具/API/权限”来结束任务"},
		{"迭代执行", "工具调用循环"},
		{"不确定时的处理", "question 工具确认意图"},
		{"代码编辑规范", "edit/multiedit"},
		{"运维安全规范", "破坏性操作前务必确认"},
		{"回复规范", "直接回答问题"},
		{"anti-hallucination-来源", "标注来源"},
		{"anti-hallucination-不确定性", "标记不确定性"},
		{"spawn_agent 使用规范", "最多同时 3 个子代理"},
		{"可用工具", "bash"},
	}
	for _, r := range required {
		assert.True(t, strings.Contains(prompt, r.keyword),
			"prompt 应包含 %s 的关键指导: %q", r.section, r.keyword)
	}
}

func TestBuildSystemPrompt_DefaultIncludesPlanRuntime(t *testing.T) {
	m, _ := newTestMaster(t)
	m.config.PlanRuntime.Enabled = true

	build := m.buildSystemPromptWithMeta(nil)

	assert.Contains(t, build.Content, "Plan Runtime")
	assert.Contains(t, build.Content, "enter_plan_mode")
	assert.Contains(t, build.Content, "todo_write")
	assert.Contains(t, build.Content, "finish_plan")
	assert.Contains(t, strings.Join(build.Versions(), "\n"), "system/plan_runtime@embedded@")
}

func TestBuildSystemPrompt_PlanRuntimeCanBeDisabled(t *testing.T) {
	m, _ := newTestMaster(t)
	m.config.PlanRuntime.Enabled = false

	build := m.buildSystemPromptWithMeta(nil)

	assert.NotContains(t, build.Content, "Plan Runtime")
	assert.NotContains(t, build.Content, "enter_plan_mode")
	assert.NotContains(t, build.Content, "todo_write")
	assert.NotContains(t, build.Content, "finish_plan")
	assert.NotContains(t, strings.Join(build.Versions(), "\n"), "system/plan_runtime@")
}

func TestBuildSystemPrompt_RemovesStaleBusinessAndExploreRules(t *testing.T) {
	m, _ := newTestMaster(t)

	prompt := m.buildSystemPrompt(nil)

	forbidden := []string{
		"xiaohongshu-writing",
		"video-script",
		"meeting-minutes",
		"brand-guide",
		"代码库探索任务通过 task 工具委派给 explore Agent，不要自己逐文件读取",
	}
	for _, s := range forbidden {
		assert.NotContains(t, prompt, s)
	}
}

func TestEmbeddedSystemPromptFilesExist(t *testing.T) {
	for _, key := range systemPromptKeys(true) {
		content := i18n.LoadEmbeddedPrompt(key)
		assert.NotEmpty(t, content, "embedded prompt %s should exist", key)
	}
}

func TestBuildSystemPrompt_EmbeddedFallbackMatchesPromptLoader(t *testing.T) {
	m, _ := newTestMaster(t)
	m.config.PlanRuntime.Enabled = true

	fallback := m.buildSystemPromptWithMeta(nil)
	fallbackSystem := systemPromptContentFromEmbedded(true)

	m.SetPromptLoader(i18n.NewPromptLoader(nil, "", "zh-CN", zaptest.NewLogger(t)))
	loaded := m.buildSystemPromptWithMeta(nil)
	loadedSystem := strings.TrimSuffix(loaded.Content, m.buildToolPrompt(nil))

	assert.Contains(t, fallback.Content, fallbackSystem)
	assert.Equal(t, fallbackSystem, loadedSystem)
	assert.Equal(t, loaded.Versions(), fallback.Versions())
}

func TestBuildSystemPromptWithMeta_ReportsPromptVersions(t *testing.T) {
	m, _ := newTestMaster(t)
	build := m.buildSystemPromptWithMeta(nil)

	assert.NotEmpty(t, build.Content)
	versions := build.Versions()
	assert.NotEmpty(t, versions)
	assert.Contains(t, versions[0], "system/base@embedded@sha256:")
	assert.True(t, strings.Contains(build.Content, "你是 Hive"))
}

func TestBuildToolPrompt_UsesToolSearchForDeferredDiscovery(t *testing.T) {
	m, _ := newTestMaster(t)

	prompt := m.buildToolPrompt([]mcphost.ToolDefinition{
		{Name: "read_file", Description: "读取文件", Core: true},
		{Name: "tool_search", Description: "搜索工具", Core: true},
		{Name: "custom_ext", Description: "扩展工具"},
	})

	assert.Contains(t, prompt, "tool_search")
	assert.Contains(t, prompt, "查看工具目录")
	assert.NotContains(t, prompt, "直接调用任何已注册的工具")
	assert.NotContains(t, prompt, "**custom_ext**")
}

func TestBuildToolPrompt_ToolSearchDoesNotPromiseAuthorization(t *testing.T) {
	m, _ := newTestMaster(t)
	prompt := m.buildToolPrompt([]mcphost.ToolDefinition{
		{Name: "tool_search", Description: "搜索工具", Core: true},
	})

	assert.Contains(t, prompt, "tool_search")
	assert.Contains(t, prompt, "只用于发现工具")
	assert.Contains(t, prompt, "不会授权执行")
	assert.NotContains(t, prompt, "发现后的工具会在后续轮次进入可调用列表")
}

func TestBuildToolPrompt_KnowledgeBaseDoesNotUseMemory(t *testing.T) {
	m, _ := newTestMaster(t)
	prompt := m.buildToolPrompt([]mcphost.ToolDefinition{
		{Name: "kb.doc.meta", Description: "列出 KB 文档", Core: true},
		{Name: "kb.doc.structure", Description: "读取 KB 结构", Core: true},
		{Name: "kb.section.text", Description: "读取 KB 原文", Core: true},
		{Name: "memory", Description: "长期记忆"},
	})

	assert.Contains(t, prompt, "知识库与长期记忆边界")
	assert.Contains(t, prompt, "使用 `kb.doc.meta` -> `kb.doc.structure` -> `kb.section.text`")
	assert.Contains(t, prompt, "不要用 memory 代替 KB")
	assert.Contains(t, prompt, "不要把 memory list/search 的结果称为知识库")
}

func TestBuildSystemPrompt_IncludesIMAPIPriorityGuidanceWhenVisible(t *testing.T) {
	m, _ := newTestMaster(t)
	prompt := m.buildSystemPrompt([]mcphost.ToolDefinition{
		{Name: "im_api", Description: "统一 IM API"},
		{Name: "feishu_api", Description: "飞书 API"},
		{Name: "question", Description: "确认问题", Core: true},
	})

	assert.Contains(t, prompt, "IM 外发统一优先使用 im_api")
	assert.Contains(t, prompt, "feishu_api 只用于飞书文档、表格、审批等飞书业务域")
	assert.Contains(t, prompt, "不得把微信、企微、钉钉请求发到飞书")
	assert.Contains(t, prompt, "微信 list_recent_conversations 后如果没有明确目标会话或联系人")
	assert.Contains(t, prompt, "question 工具")
}

func TestBuildSystemPrompt_OmitsIMAPIPriorityGuidanceWhenUnavailable(t *testing.T) {
	m, _ := newTestMaster(t)
	prompt := m.buildSystemPrompt([]mcphost.ToolDefinition{
		{Name: "feishu_api", Description: "飞书 API"},
		{Name: "question", Description: "确认问题", Core: true},
	})

	assert.NotContains(t, prompt, "IM 外发统一优先使用 im_api")
	assert.NotContains(t, prompt, "不要用 feishu_api 代替 IM 外发")
	assert.NotContains(t, prompt, "list_recent_conversations")
}

func TestBuildSystemPrompt_IncludesFilesystemPriorityGuidanceWhenVisible(t *testing.T) {
	m, _ := newTestMaster(t)
	prompt := m.buildSystemPrompt([]mcphost.ToolDefinition{
		{Name: "filesystem", Description: "统一文件系统工具", Core: true},
		{Name: "apply_patch", Description: "应用补丁", Core: true},
		{Name: "bash", Description: "执行命令", Core: true},
	})

	assert.Contains(t, prompt, "文件系统操作优先使用 filesystem.action")
	assert.Contains(t, prompt, "list/glob/grep/read")
	assert.Contains(t, prompt, "edit")
	assert.Contains(t, prompt, "multiedit")
	assert.Contains(t, prompt, "apply_patch")
	assert.Contains(t, prompt, "bash")
	assert.Contains(t, prompt, "不要用 bash 执行 cat/less/head/tail 替代 filesystem.read")
	assert.Contains(t, prompt, "Plan mode")
	assert.Contains(t, prompt, "不得调用 write/edit/multiedit")
}

func TestBuildSystemPrompt_OmitsFilesystemPriorityGuidanceWhenUnavailable(t *testing.T) {
	m, _ := newTestMaster(t)
	prompt := m.buildSystemPrompt([]mcphost.ToolDefinition{
		{Name: "apply_patch", Description: "应用补丁", Core: true},
		{Name: "bash", Description: "执行命令", Core: true},
	})

	assert.NotContains(t, prompt, "文件系统操作优先使用 filesystem.action")
	assert.NotContains(t, prompt, "不得调用 write/edit/multiedit")
}

func systemPromptContentFromEmbedded(planRuntimeEnabled bool) string {
	var b strings.Builder
	for _, key := range systemPromptKeys(planRuntimeEnabled) {
		b.WriteString(i18n.LoadEmbeddedPrompt(key))
	}
	b.WriteString("\n")
	return b.String()
}
