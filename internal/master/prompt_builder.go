package master

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"strings"
	"time"

	"github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
	"go.uber.org/zap"

	"github.com/chef-guo/agents-hive/internal/airouter"
	"github.com/chef-guo/agents-hive/internal/asset"
	"github.com/chef-guo/agents-hive/internal/compaction"
	"github.com/chef-guo/agents-hive/internal/config"
	"github.com/chef-guo/agents-hive/internal/errs"
	"github.com/chef-guo/agents-hive/internal/fileconv"
	"github.com/chef-guo/agents-hive/internal/i18n"
	"github.com/chef-guo/agents-hive/internal/llm"
	"github.com/chef-guo/agents-hive/internal/mcphost"
	"github.com/chef-guo/agents-hive/internal/plugin"
)

// buildUserContent 构建用户消息内容（支持附件）
func (m *Master) buildUserContent(ctx context.Context, text string, attachments []FileAttachment) llm.Content {
	if len(attachments) == 0 {
		return llm.NewTextContent(text)
	}

	parts := []llm.ContentPart{llm.TextPart(text)}
	for _, a := range attachments {
		if strings.TrimSpace(a.Data) == "" {
			if m != nil && m.assetService != nil && strings.TrimSpace(a.AssetURI) != "" {
				data, rec, err := m.assetService.Download(ctx, asset.AssetURI(strings.TrimSpace(a.AssetURI)))
				if err == nil {
					a.Data = base64.StdEncoding.EncodeToString(data)
					if strings.TrimSpace(a.MimeType) == "" && rec != nil {
						a.MimeType = rec.MimeType
					}
					if strings.TrimSpace(a.Filename) == "" && rec != nil {
						a.Filename = rec.Filename
					}
				}
			}
		}
		if strings.TrimSpace(a.Data) == "" {
			parts = append(parts, llm.TextPart(attachmentReferenceText(a)))
			continue
		}
		m.logger.Debug("附件转换开始",
			zap.String("filename", a.Filename),
			zap.String("mime_type", a.MimeType),
		)
		result, err := fileconv.Convert(ctx, a.Filename, a.MimeType, a.Data, m.transcribeAudio)
		if err != nil {
			m.logger.Error("附件转换失败",
				zap.String("filename", a.Filename),
				zap.Error(err),
			)
			parts = append(parts, llm.TextPart(fmt.Sprintf("[文件处理失败: %s] %s", a.Filename, err.Error())))
			continue
		}
		m.logger.Debug("附件转换成功",
			zap.String("filename", a.Filename),
			zap.String("result_type", result.Type),
			zap.Int("text_len", len(result.Text)),
		)
		switch result.Type {
		case "text":
			parts = append(parts, llm.TextPart(result.Text))
		case "image":
			parts = append(parts, llm.ImageBase64Part(a.MimeType, a.Data))
		case "file":
			parts = append(parts, llm.ContentPart{Type: llm.ContentFile, FileData: a.Data, Filename: a.Filename})
		}
	}
	return llm.NewMultiContent(parts...)
}

// transcribeAudio 使用 OpenAI Whisper API 转录音频
func (m *Master) transcribeAudio(ctx context.Context, audioData []byte, filename string) (string, error) {
	if m.config.APIKey == "" {
		return "", errs.New(errs.CodeInvalidInput, "需要 API Key 才能使用音频转录功能")
	}
	// 创建用于 Whisper 的 openai client
	opts := []option.RequestOption{option.WithAPIKey(m.config.APIKey)}
	if m.config.BaseURL != "" {
		opts = append(opts, option.WithBaseURL(m.config.BaseURL))
	}
	client := openai.NewClient(opts...)
	resp, err := client.Audio.Transcriptions.New(ctx, openai.AudioTranscriptionNewParams{
		Model: "whisper-1",
		File:  openai.File(bytes.NewReader(audioData), filename, "audio/mpeg"),
	})
	if err != nil {
		return "", errs.Wrap(errs.CodeInternal, "音频转录失败", err)
	}
	return resp.Text, nil
}

func systemPromptKeys(planRuntimeEnabled bool) []string {
	keys := []string{
		"system/base",
		"system/execution",
		"system/business",
		"system/code_editing",
		"system/safety",
		"system/reply",
	}
	if !planRuntimeEnabled {
		return keys
	}
	return []string{
		"system/base",
		"system/execution",
		"system/plan_runtime",
		"system/business",
		"system/code_editing",
		"system/safety",
		"system/reply",
	}
}

type systemPromptBuild struct {
	Content string
	Metas   []i18n.PromptMeta
}

func (b systemPromptBuild) Versions() []string {
	out := make([]string, 0, len(b.Metas))
	for _, meta := range b.Metas {
		out = append(out, meta.Key+"@"+meta.Source+"@"+meta.Hash)
	}
	return out
}

func (m *Master) buildSystemPrompt(tools []mcphost.ToolDefinition) string {
	return m.buildSystemPromptWithMeta(tools).Content
}

func (m *Master) buildSystemPromptWithMeta(tools []mcphost.ToolDefinition) systemPromptBuild {
	var b strings.Builder
	var metas []i18n.PromptMeta

	writePromptValue := func(v i18n.PromptValue) {
		if v.Content == "" {
			return
		}
		b.WriteString(v.Content)
		metas = append(metas, v.Meta)
	}

	// nil 防御：promptLoader 未注入时直接复用 go:embed 内置 prompt，避免维护第二份硬编码文案。
	if m.promptLoader == nil {
		for _, key := range systemPromptKeys(m.config.PlanRuntime.Enabled) {
			content := i18n.LoadEmbeddedPrompt(key)
			writePromptValue(i18n.PromptValue{
				Content: content,
				Meta: i18n.PromptMeta{
					Key:    key,
					Source: "embedded",
					Hash:   i18n.HashPromptForQuality(content),
				},
			})
		}
		b.WriteString("\n")
	} else {
		// 三层优先级加载核心 prompt 段落
		writePrompt := func(key, fallback string) {
			v := m.promptLoader.LoadWithMetaOrDefault(key, fallback)
			writePromptValue(v)
		}
		for _, key := range systemPromptKeys(m.config.PlanRuntime.Enabled) {
			writePrompt(key, "")
		}
		b.WriteString("\n")
	}

	b.WriteString(m.buildToolPrompt(tools))
	return systemPromptBuild{Content: b.String(), Metas: metas}
}

func (m *Master) buildToolPrompt(tools []mcphost.ToolDefinition) string {
	var b strings.Builder

	// 可用工具提示（仅列出核心工具，减少 system prompt 体积）
	if len(tools) > 0 {
		b.WriteString("## 可用工具\n")
		b.WriteString("你可以使用以下核心工具完成任务：\n")
		for _, t := range tools {
			if t.Core {
				b.WriteString(fmt.Sprintf("- **%s**: %s\n", t.Name, t.Description))
			}
		}
		b.WriteString("\n更多扩展工具默认不进入候选集。需要不常用、外部 MCP 或自定义能力时，可以先调用 **tool_search** 查看工具目录。tool_search 只用于发现工具，不会授权执行；某个工具是否能在当前回合调用，取决于本轮工具列表、RouteDecision、plan mode 和权限审批。\n\n")

		// 外部 MCP 工具提示（按服务端分组，帮助 LLM 了解可用的外部集成）
		externalTools := make(map[string][]mcphost.ToolDefinition)
		for _, t := range tools {
			if parts := strings.SplitN(t.Name, "__", 2); len(parts) == 2 {
				externalTools[parts[0]] = append(externalTools[parts[0]], t)
			}
		}
		if len(externalTools) > 0 {
			b.WriteString("## 外部集成工具\n")
			b.WriteString("以下是通过 MCP 协议集成的外部工具，可直接调用：\n")
			for server, serverTools := range externalTools {
				b.WriteString(fmt.Sprintf("### %s\n", server))
				for _, st := range serverTools {
					b.WriteString(fmt.Sprintf("- **%s**: %s\n", st.Name, st.Description))
				}
			}
			b.WriteString("\n")

			// 外部 MCP 工具专属规范（从 PromptLoader 动态加载）
			for mcpName := range externalTools {
				if m.promptLoader != nil {
					if toolPrompt := m.promptLoader.Load(fmt.Sprintf("tools/%s", mcpName)); toolPrompt != "" {
						b.WriteString(toolPrompt)
						b.WriteString("\n")
					}
				} else if mcpName == "wenyan" {
					// 硬编码 wenyan fallback
					b.WriteString("#### wenyan 发布规范\n")
					b.WriteString("调用 wenyan__publish_article 时，文章内容（content 参数）必须包含 YAML frontmatter，其中 title 字段为必填。\n\n")
				}
			}
		}
	}

	// 外部操作引导
	b.WriteString("## 外部操作\n\n")
	b.WriteString("直接使用 bash 和 webfetch 工具完成外部操作。\n")
	b.WriteString("已配置的外部资源连接信息见下方。\n\n")
	if hasPromptTool(tools, "kb.doc.meta") || hasPromptTool(tools, "kb.doc.structure") || hasPromptTool(tools, "kb.section.text") || hasPromptTool(tools, "memory") {
		b.WriteString("### 知识库与长期记忆边界\n")
		b.WriteString("- 用户说“知识库”、“KB”、“namespace”、“上传文档”、“文档目录”或要求读取项目/业务文档时，使用 `kb.doc.meta` -> `kb.doc.structure` -> `kb.section.text`；不要用 memory 代替 KB。\n")
		b.WriteString("- `memory` 只表示用户长期记忆和工作方式反馈，用于偏好、经验、项目片段备忘；它不是文档知识库，也不能证明当前会话绑定了哪些 KB namespace。\n")
		b.WriteString("- 如果 KB 工具返回未绑定或不可用，直接说明当前会话没有可用 KB 绑定，并提示用户绑定 namespace；不要把 memory list/search 的结果称为知识库。\n\n")
	}
	if hasPromptTool(tools, "filesystem") {
		b.WriteString("### 文件系统工具选择\n")
		b.WriteString("- 文件系统操作优先使用 filesystem.action。查找、搜索、读取使用 list/glob/grep/read；单处精确替换使用 edit；多处精确替换使用 multiedit。\n")
		b.WriteString("- 大段补丁、unified diff 或跨结构调整优先使用 apply_patch；测试、构建、git、包管理和任意 shell 命令使用 bash。\n")
		b.WriteString("- 不要用 bash 执行 cat/less/head/tail 替代 filesystem.read；只在执行真实 shell 命令时使用 bash。\n")
		b.WriteString("- Plan mode 或只读任务中只能使用 filesystem 的 list/glob/grep/read，不得调用 write/edit/multiedit。\n\n")
	}
	if hasPromptTool(tools, "im_api") {
		b.WriteString("### IM 外发工具选择\n")
		b.WriteString("- IM 外发统一优先使用 im_api。\n")
		b.WriteString("- feishu_api 只用于飞书文档、表格、审批等飞书业务域，不要用 feishu_api 代替 IM 外发。\n")
		b.WriteString("- 用户给出平台 hint 时必须匹配对应平台工具与 platform 参数，不得把微信、企微、钉钉请求发到飞书。\n")
		b.WriteString("- 微信 list_recent_conversations 后如果没有明确目标会话或联系人，必须调用 question 工具向用户确认后再发送。\n\n")
	}

	// spawn_agent 使用规范（从 PromptLoader 加载）
	if m.promptLoader != nil {
		if spawnPrompt := m.promptLoader.Load("tools/spawn_agent"); spawnPrompt != "" {
			b.WriteString(spawnPrompt)
			b.WriteString("\n")
		}
	} else {
		b.WriteString("## spawn_agent 使用规范\n\n")
		b.WriteString("spawn_agent 是同步的：调用后等待子 Agent 完成才返回结果。最多同时 3 个子代理。\n\n")
	}

	// 动态工具创建引导（从 PromptLoader 加载）
	if m.promptLoader != nil {
		if dynPrompt := m.promptLoader.Load("tools/dynamic_tools"); dynPrompt != "" {
			b.WriteString(dynPrompt)
			b.WriteString("\n")
		}
	} else {
		b.WriteString("## 动态工具创建\n")
		b.WriteString("需要重复调用某个外部 API 或命令时，使用 create_tool 创建专用工具。\n\n")
	}

	// 注入已配置的外部资源
	if resources := m.getExternalResources(); len(resources) > 0 {
		b.WriteString("### 已配置的外部资源\n")
		b.WriteString("以下外部资源已由用户预配置，创建专用 Agent 时请直接使用对应的连接信息：\n")
		for _, res := range resources {
			if !res.Enabled {
				continue
			}
			b.WriteString(fmt.Sprintf("- **%s** [%s][%s]: %s\n", res.Name, res.Type, res.Environment, res.Description))
			if res.Connection != "" {
				b.WriteString(fmt.Sprintf("  连接: `%s`\n", res.Connection))
			}
			if res.Endpoint != "" {
				b.WriteString(fmt.Sprintf("  端点: `%s`\n", res.Endpoint))
			}
			if res.ReadOnly {
				b.WriteString("  （只读模式）\n")
			}
		}
		b.WriteString("\n")
	}

	// Skills 提示
	if m.skillReg != nil {
		if available := m.skillReg.ListForModel(); len(available) > 0 {
			b.WriteString("## 可用技能（Skills）\n\n")
			b.WriteString("使用 skill 工具调用以下技能。根据描述判断何时使用：\n\n")
			for _, sm := range available {
				b.WriteString(fmt.Sprintf("- **%s**: %s", sm.Name, sm.Description))
				if sm.ArgumentHint != "" {
					b.WriteString(fmt.Sprintf(" | 参数: `%s`", sm.ArgumentHint))
				}
				if sm.Domain != "" {
					b.WriteString(fmt.Sprintf(" | 领域: %s", sm.Domain))
				}
				if len(sm.TriggerKeywords) > 0 {
					b.WriteString(fmt.Sprintf(" | 触发词: %s", strings.Join(sm.TriggerKeywords, ", ")))
				}
				if sm.Context == "fork" {
					b.WriteString(" | [隔离执行]")
				}
				if sm.Model != "" {
					b.WriteString(fmt.Sprintf(" | 模型: %s", sm.Model))
				}
				b.WriteString("\n")
			}
			b.WriteString("\n")
		}
	}

	// 追加用户自定义指令（从 .claw/AGENTS.md 或 CLAUDE.md 加载）
	if m.promptCtx.Instructions() != "" {
		b.WriteString("## 项目指令\n")
		b.WriteString(m.promptCtx.Instructions())
		b.WriteString("\n\n")
	}

	// 注入动态上下文（模型感知格式：Git 状态、日期、OS 信息）
	if m.promptCtx.PromptManager() != nil {
		contextBlock := i18n.NewPromptBuilder(m.promptCtx.PromptManager(), m.logger).
			WithProvider(m.promptCtx.ProviderKey()).
			WithGitStatus().
			WithCurrentDate().
			WithOSInfo().
			BuildContext()
		if contextBlock != "" {
			b.WriteString(contextBlock)
			b.WriteString("\n\n")
		}
	}

	return b.String()
}

func hasPromptTool(tools []mcphost.ToolDefinition, name string) bool {
	for _, tool := range tools {
		if strings.TrimSpace(tool.Name) == name {
			return true
		}
	}
	return false
}

// buildCompactionPipeline 根据配置构建可插拔压缩管线（P2-2）
func (m *Master) buildCompactionPipeline() *compaction.Pipeline {
	cfg := m.config.ContextCompression

	// 统一构建 TokenCounter（tiktoken 模式下所有 compactor 共享同一实例）
	var tc *llm.TokenCounter
	if cfg.UseTiktoken {
		var err error
		tc, err = llm.NewTokenCounter()
		if err != nil {
			m.logger.Warn("tiktoken 初始化失败，降级到启发式估算", zap.Error(err))
		}
	}

	// 注册所有可用的 Compactor
	registry := map[string]compaction.Compactor{}

	// tool_budget: 工具输出截断
	registry["tool_budget"] = &compaction.ToolResultBudgetCompactor{
		ProtectedTurns:  PruneProtectedTurns,
		OutputThreshold: cfg.ToolOutputMaxTokens,
		ContextBudget:   cfg.ToolOutputMaxTokens * 2,
		Observer:        toolsReadTrackerTrimObserver{},
	}

	// session_memory: 提取会话记忆
	registry["session_memory"] = &compaction.SessionMemoryCompactor{
		MaxExtractMessages: 20,
	}

	// history_snip: 首尾保留，中间压缩
	registry["history_snip"] = &compaction.HistorySnipCompactor{
		KeepFirst: true,
		KeepLast:  4,
	}

	// llm_summary: LLM 智能摘要
	registry["llm_summary"] = m.buildLLMSummaryCompactor(cfg, tc)

	// truncate: 简单截断（兜底）
	registry["truncate"] = &compaction.TruncateCompactor{
		UseTiktoken:  cfg.UseTiktoken,
		TokenCounter: tc,
	}

	// 使用配置的阶段列表；空列表则用默认值
	stages := cfg.PipelineStages
	if len(stages) == 0 {
		stages = config.DefaultCompactionPipelineStages
	}

	pipeline, skipped := compaction.NewPipeline(registry, stages)
	if len(skipped) > 0 {
		m.logger.Warn("压缩管线跳过未知阶段",
			zap.Strings("skipped_stages", skipped),
			zap.Strings("configured_stages", stages))
	}
	if pipeline == nil || len(pipeline.StageNames()) == 0 {
		m.logger.Warn("压缩管线为空，禁用压缩",
			zap.Strings("configured_stages", cfg.PipelineStages))
	}
	return pipeline
}

// buildLLMSummaryCompactor 构建 LLM 摘要压缩器
func (m *Master) buildLLMSummaryCompactor(cfg config.CompactionConfig, tc *llm.TokenCounter) *compaction.LLMSummaryCompactor {
	var llmClient *llm.Client
	if m.router != nil {
		llmClient = m.router.GetLLMClient(airouter.TaskSummary)
	} else {
		provDef := llm.LookupProvider(m.config.Provider)
		provDef.APIFormat = m.config.APIFormat
		llmClient = m.llmPool.Get(llm.ClientConfig{
			Model:           m.config.Model,
			Provider:        provDef,
			BaseURL:         m.config.BaseURL,
			APIKey:          m.config.APIKey,
			DisableJSONMode: m.config.DisableJSONMode,
			ReasoningEffort: m.config.ReasoningEffort,
			StorePrivacy:    m.config.StorePrivacy,
			PromptCacheKey:  m.config.PromptCacheKey,
			ServiceTier:     m.config.ServiceTier,
		})
	}
	return &compaction.LLMSummaryCompactor{
		LLMClient:    llmClient,
		TokenCounter: tc,
		UseTiktoken:  cfg.UseTiktoken,
		Timeout:      cfg.CompactTimeout,
		Logger:       m.logger,
	}
}

// prepareMessagesWithCompression 准备发送给 LLM 的消息（应用智能压缩）
// P2-2: 优先使用可插拔压缩管线，向后兼容旧策略
//
// harden-spec-driven-phase2 task 3.8：signature 增加 session 参数——pipeline 结束后调
// PreserveSpecStateOnCompact 注入 [SPEC-STATE] pin，保证 spec-driven 会话的 change_id/
// current_task_key/revision 不因为 truncate/summary 丢失 LLM 感知。session 可为 nil（测试路径），
// 此时 preservation 是 no-op——与原 messages-only 语义等价。
func (m *Master) prepareMessagesWithCompression(ctx context.Context, session *SessionState, messages []llm.MessageWithTools) []llm.MessageWithTools {
	cfg := m.config.ContextCompression
	if !cfg.Enabled {
		return messages
	}

	// 计算 token 数（用于懒惰模式和日志）
	var totalTokens int
	if cfg.UseTiktoken {
		tc, err := llm.NewTokenCounter()
		if err != nil {
			m.logger.Warn("tiktoken 初始化失败，降级到启发式估算", zap.Error(err))
			totalTokens = llm.EstimateMessagesTokens(messages)
		} else {
			totalTokens = tc.CountMessages(messages)
		}
	} else {
		totalTokens = llm.EstimateMessagesTokens(messages)
	}

	// tool_budget 裁剪：无论是否 LazyMode 都先执行，防止大型工具输出绕过压缩
	// （LazyMode 早返回会跳过整个 pipeline，但 tool output 过大仍需裁剪）
	toolBudgetCompactor := &compaction.ToolResultBudgetCompactor{
		ProtectedTurns:  PruneProtectedTurns,
		OutputThreshold: cfg.ToolOutputMaxTokens,
		ContextBudget:   cfg.ToolOutputMaxTokens * 2,
		Observer:        toolsReadTrackerTrimObserver{},
	}
	budget0 := cfg.MaxTokens - cfg.ReserveTokens
	if budget0 <= 0 {
		budget0 = cfg.MaxTokens
	}
	if trimmed, err := toolBudgetCompactor.Compact(ctx, messages, budget0); err == nil {
		messages = trimmed
	}

	// 懒惰模式：未超阈值则跳过（tool_budget 已在上方执行）
	if cfg.LazyMode && totalTokens <= cfg.LazyThreshold {
		m.compactionTracker.RecordSkipped()
		return messages
	}

	// 触发 PreCompact hook
	if m.pluginMgr != nil {
		hookCtx, cancel := context.WithTimeout(ctx, plugin.HookCallTimeout)
		_ = m.pluginMgr.TriggerPreCompact(hookCtx, &plugin.CompactInput{
			MessageCount: len(messages),
		})
		cancel()
	}

	// 使用可插拔压缩管线（每次调用时构建，确保 LLM client 始终最新）
	pipeline := m.buildCompactionPipeline()

	budget := cfg.MaxTokens - cfg.ReserveTokens
	if budget <= 0 {
		budget = cfg.MaxTokens
	}

	originalCount := len(messages)
	startTime := time.Now()

	result, err := pipeline.Compact(ctx, messages, budget)
	elapsed := time.Since(startTime)

	if err != nil {
		m.logger.Warn("压缩管线执行失败，回退到原始消息", zap.Error(err))
		result = messages
	}

	remainingCount := len(result)

	// 更新统计
	if remainingCount < originalCount {
		m.compactionTracker.RecordTrigger(elapsed)
		avgDelay := m.GetCompactionStats().AverageDelay
		m.logger.Info("上下文已压缩",
			zap.Int("original", originalCount),
			zap.Int("remaining", remainingCount),
			zap.Strings("stages", pipeline.StageNames()),
			zap.Int("original_tokens", totalTokens),
			zap.Duration("elapsed", elapsed),
			zap.Duration("avg_delay", avgDelay),
		)
	}

	// 触发 PostCompact hook
	if m.pluginMgr != nil {
		hookCtx, cancel := context.WithTimeout(ctx, plugin.HookCallTimeout)
		_ = m.pluginMgr.TriggerPostCompact(hookCtx, &plugin.CompactInput{
			MessageCount: remainingCount,
		})
		cancel()
	}

	// harden-spec-driven-phase2 task 3.8：pipeline 结束后注入 [SPEC-STATE] pin。
	// 顺序必须在 PostCompact hook 之后——hook 看到的是 pipeline 真实产物（pin 不算压缩
	// 对象），插件计数不会把 pin 当成"新增一条被压缩的消息"。
	result = PreserveSpecStateOnCompact(session, result)

	return result
}
