package config

import (
	"time"

	"github.com/chef-guo/agents-hive/internal/skills"
)

// 默认配置值
const (
	DefaultServerPort          = 8080
	DefaultLogLevel            = "info"
	DefaultLogFile             = "~/.claw/logs/claw.log" // 默认日志文件路径
	DefaultConsoleLevel        = "error"                 // CLI 模式默认控制台只显示错误
	DefaultLogMaxSize          = 100                     // 日志文件最大 100MB
	DefaultLogMaxBackups       = 3                       // 保留 3 个旧日志文件
	DefaultLogMaxAge           = 7                       // 日志文件保留 7 天
	DefaultAgentTimeout        = 10 * time.Minute
	DefaultMCPTimeout          = 30 * time.Second
	DefaultHealthInterval      = 10 * time.Second
	DefaultMaxConcurrentAgents = 10
	DefaultModel               = "" // 由 Resolve 从 Provider 填充
	DefaultBaseURL             = "" // 由 Resolve 从 Provider 填充
	DefaultProvider            = "openai"
	DefaultDisableJSONMode     = false   // 默认启用 JSON mode（仅禁用用于不兼容的 API）
	DefaultPromptLanguage      = "en-US" // 默认使用英文提示词（最佳 LLM 效果）

	// HITL 默认值
	DefaultHITLEnabled          = false
	DefaultHITLStepConfirmation = "none"
	DefaultHITLInputTimeout     = 30 * time.Minute
	DefaultHITLWebSocket        = false

	// 运行时超时默认值
	DefaultShellTimeout   = 10 * time.Second
	DefaultScriptTimeout  = 30 * time.Second
	DefaultWSPingInterval = 30 * time.Second
	DefaultSyncInterval   = 5 * time.Minute

	// 上下文压缩默认值
	// 现代大模型普遍支持 100K~1M token 上下文，默认阈值设为 500K
	DefaultCompactionEnabled       = true
	DefaultCompactionStrategy      = "llm_summary" // CompactStrategy 类型在 config.go 中定义
	DefaultCompactionMaxTokens     = 500000
	DefaultCompactionReserve       = 10000
	DefaultCompactionTimeout       = 30 * time.Second
	DefaultCompactionTiktoken      = true
	DefaultCompactionLazyMode      = true   // 启用懒惰压缩模式
	DefaultCompactionLazyThreshold = 500000 // 懒惰模式触发阈值（token 数）

	// 可插拔压缩管线默认值（P2-2）
	DefaultCompactionToolOutputMaxTokens = 20 * 1024 // 20KB

	// LSP 默认值
	DefaultLSPEnabled        = true
	DefaultLSPTimeout        = 10 * time.Second
	DefaultLSPMaxServers     = 5
	DefaultLSPHealthInterval = 30 * time.Second

	// 自定义工具默认值
	DefaultCustomToolsDir = ".claw/tools"

	// 会话存储默认值
	DefaultSessionsDir = "~/.claw/sessions"

	// 隐私与远程指令默认值
	DefaultStorePrivacy = false // 默认不设置 store=false（不影响 OpenAI 默认行为）

	// 每轮工具召回默认值。默认保持既有行为：召回 5 个候选并注入本轮 model-visible tools。
	DefaultToolRecallMode               = "inject"
	DefaultToolRecallLimit              = 5
	DefaultToolRecallMaxLimit           = 20
	DefaultToolRecallMinScore           = 0.35
	DefaultToolRecallSideEffectMinScore = 0.65
	DefaultToolRecallLogCandidates      = true

	// Spec-driven Phase 2 默认值（openspec/changes/harden-spec-driven-phase2）
	// FM-1 反例：continuation.default 必须 off——不允许静默 MRU 续写。
	// FM-4 反例：planner.token_budget 硬上限——schema fail 触发 DownshiftPlannerSchemaFailed。
	DefaultSpecDrivenMode          = "legacy" // 零成本短路，默认行为与 Phase 2 前一致
	DefaultSpecContinuationDefault = "off"    // FM-1 反例：强制 fail-closed
	DefaultSpecPlannerTokenBudget  = 800      // 单次 planner 调用 max_tokens 硬上限
)

// DefaultSpecDrivenConfig 返回 spec-driven Phase 2 的默认配置（mode=legacy 零开销）。
// CLIDefaults / Load 路径都应读此值；DB 种子后续由 config 迁移 SQL 回填。
var DefaultSpecDrivenConfig = SpecDrivenConfig{
	Mode: DefaultSpecDrivenMode,
	Continuation: SpecContinuationConfig{
		Default: DefaultSpecContinuationDefault,
	},
	Planner: SpecPlannerConfig{
		TokenBudget: DefaultSpecPlannerTokenBudget,
	},
}

// DefaultToolRecallConfigValue 返回每轮工具召回默认配置。
var DefaultToolRecallConfigValue = ToolRecallConfig{
	Mode:               DefaultToolRecallMode,
	Limit:              DefaultToolRecallLimit,
	MinScore:           DefaultToolRecallMinScore,
	SideEffectMinScore: DefaultToolRecallSideEffectMinScore,
	LogCandidates:      DefaultToolRecallLogCandidates,
}

// DefaultCompactionPipelineStages 默认管线阶段：tool_budget -> session_memory -> truncate
var DefaultCompactionPipelineStages = []string{"tool_budget", "session_memory", "truncate"}

// DefaultPermissionRules 定义默认的工具权限规则。
// 默认采用低摩擦策略：常规读写、计划、编排工具放行；危险 shell、外部发送、删除/账号/社交副作用进入权限层细分。
var DefaultPermissionRules = []skills.PermissionRule{
	// ── 自动允许 (allow) - 常规开发/规划路径 ──
	{ToolName: "read_file", Action: skills.PermissionAllow},
	{ToolName: "write_file", Action: skills.PermissionAllow},
	{ToolName: "edit", Action: skills.PermissionAllow},
	{ToolName: "multiedit", Action: skills.PermissionAllow},
	{ToolName: "apply_patch", Action: skills.PermissionAllow},
	{ToolName: "glob", Action: skills.PermissionAllow},
	{ToolName: "grep", Action: skills.PermissionAllow},
	{ToolName: "ls", Action: skills.PermissionAllow},
	{ToolName: "websearch", Action: skills.PermissionAllow},
	{ToolName: "webfetch", Action: skills.PermissionAllow},
	{ToolName: "browser_interact", Action: skills.PermissionAllow},
	{ToolName: "skill", Action: skills.PermissionAllow},
	{ToolName: "task", Action: skills.PermissionAllow},
	{ToolName: "question", Action: skills.PermissionAllow},
	{ToolName: "batch", Action: skills.PermissionAllow},
	{ToolName: "todo_write", Action: skills.PermissionAllow},
	{ToolName: "enter_plan_mode", Action: skills.PermissionAllow},
	{ToolName: "exit_plan_mode", Action: skills.PermissionAllow},
	{ToolName: "finish_plan", Action: skills.PermissionAllow},
	{ToolName: "handoff_summary", Action: skills.PermissionAllow},
	{ToolName: "promote_todos_to_taskboard", Action: skills.PermissionAllow},
	{ToolName: "spawn_agent", Action: skills.PermissionAllow},
	{ToolName: "parallel_dispatch", Action: skills.PermissionAllow},

	// ── 需要权限层细分 (ask) - 危险 shell / 外部发送 / 删除或账号社交副作用 ──
	{ToolName: "bash", Action: skills.PermissionAsk},
	{ToolName: "shell", Action: skills.PermissionAsk},
	{ToolName: "exec", Action: skills.PermissionAsk},
	{ToolName: "run_command", Action: skills.PermissionAsk},
	{ToolName: "memory", Pattern: "delete", Action: skills.PermissionAsk},
	{ToolName: "memory", Action: skills.PermissionAllow},
	{ToolName: "taskboard", Pattern: "delete", Action: skills.PermissionAsk},
	{ToolName: "taskboard", Action: skills.PermissionAllow},
	{ToolName: "create_tool", Action: skills.PermissionAsk},
	{ToolName: "remove_tool", Action: skills.PermissionAsk},
	{ToolName: "send_im_message", Action: skills.PermissionAsk},
	{ToolName: "feishu_api", Pattern: "send_message", Action: skills.PermissionAsk},
	{ToolName: "feishu_api", Pattern: "send_image", Action: skills.PermissionAsk},
	{ToolName: "feishu_api", Pattern: "send_file", Action: skills.PermissionAsk},
	{ToolName: "feishu_api", Pattern: "create_approval", Action: skills.PermissionAsk},
	{ToolName: "feishu_api", Pattern: "create_bitable_record", Action: skills.PermissionAsk},
	{ToolName: "feishu_api", Pattern: "update_bitable_record", Action: skills.PermissionAsk},
	{ToolName: "feishu_api", Pattern: "create_task", Action: skills.PermissionAsk},
	{ToolName: "feishu_api", Pattern: "complete_task", Action: skills.PermissionAsk},
	{ToolName: "feishu_api", Pattern: "write_sheet", Action: skills.PermissionAsk},
	{ToolName: "feishu_api", Action: skills.PermissionAllow},
	{ToolName: "wechat_send_rich_message", Action: skills.PermissionAsk},
	{ToolName: "wechat_contacts", Pattern: "add", Action: skills.PermissionAsk},
	{ToolName: "wechat_contacts", Pattern: "accept", Action: skills.PermissionAsk},
	{ToolName: "wechat_contacts", Pattern: "delete", Action: skills.PermissionAsk},
	{ToolName: "wechat_contacts", Action: skills.PermissionAllow},
	{ToolName: "wechat_groups", Pattern: "create", Action: skills.PermissionAsk},
	{ToolName: "wechat_groups", Pattern: "invite", Action: skills.PermissionAsk},
	{ToolName: "wechat_groups", Pattern: "remove", Action: skills.PermissionAsk},
	{ToolName: "wechat_groups", Pattern: "set_name", Action: skills.PermissionAsk},
	{ToolName: "wechat_groups", Pattern: "set_announcement", Action: skills.PermissionAsk},
	{ToolName: "wechat_groups", Pattern: "quit", Action: skills.PermissionAsk},
	{ToolName: "wechat_groups", Action: skills.PermissionAllow},
	{ToolName: "wechat_profile", Pattern: "set_nickname", Action: skills.PermissionAsk},
	{ToolName: "wechat_profile", Pattern: "set_signature", Action: skills.PermissionAsk},
	{ToolName: "wechat_profile", Pattern: "remark", Action: skills.PermissionAsk},
	{ToolName: "wechat_profile", Action: skills.PermissionAllow},
	{ToolName: "wechat_moments", Pattern: "post", Action: skills.PermissionAsk},
	{ToolName: "wechat_moments", Pattern: "like", Action: skills.PermissionAsk},
	{ToolName: "wechat_moments", Pattern: "comment", Action: skills.PermissionAsk},
	{ToolName: "wechat_moments", Action: skills.PermissionAllow},
	{ToolName: "wechat_status", Action: skills.PermissionAllow},
	// 外部 MCP 工具默认需要审批（通配符匹配所有带前缀的工具）
	{ToolName: "wenyan__preview_article", Action: skills.PermissionAllow}, // 预览是只读操作，自动放行
	{ToolName: "wenyan__*", Action: skills.PermissionAsk},
}

// Channel 默认值
var DefaultChannelConfig = ChannelConfig{
	Enabled: false,
	Feishu: FeishuConfig{
		Reliability: FeishuReliabilityConfig{
			LongconnGapFetchEnabled: false,
			HeartbeatStaleWindow:    60 * time.Second,
			GapFetchMaxWindow:       10 * time.Minute,
		},
		Identity: FeishuIdentityConfig{
			UserCacheSize:   5000,
			UserCacheTTLSec: int((12 * time.Hour) / time.Second),
		},
	},
	WeChat: WeChatConfig{
		WeChatPadPro: WeChatPadProInstanceConfig{
			Enabled: false,
			BaseURL: "http://localhost:8848",
			Timeout: 30,
		},
	},
}

// Gateway 默认值
var DefaultGatewayConfig = GatewayConfig{Enabled: false}

// 注意: 安全配置尚未接入运行时
var DefaultSecurityConfig = SecurityConfig{}

// ControlPlane 默认值
var DefaultControlPlaneConfig = ControlPlaneConfig{
	Enabled:     false,
	MaxSessions: 100,
	RateLimit:   10,
	RateBurst:   20,
}

// ACPServer 默认值
var DefaultACPServerConfig = ACPServerConfig{
	Enabled:     false,
	AuthMethod:  "none",
	MaxSessions: 50,
}

// Plugin 默认值
var DefaultPluginConfig = PluginConfig{
	Enabled:      false,
	AutoDiscover: false,
}

// WebUI 默认值
var DefaultWebUIConfig = WebUIConfig{Enabled: true}

// ToolPolicy 默认值
var DefaultToolPolicyConfig = ToolPolicyConfig{
	Groups: []ToolGroupConfig{
		{Name: "fs", Tools: []string{"read_file", "write_file", "edit", "glob", "grep", "ls", "multiedit", "apply_patch"}},
		{Name: "runtime", Tools: []string{"bash"}},
		{Name: "web", Tools: []string{"websearch", "webfetch", "browser_interact"}},
		{Name: "lsp", Tools: []string{"lsp_definition", "lsp_references", "lsp_hover", "lsp_symbols", "lsp_diagnostics", "lsp_rename", "lsp_code_action", "lsp_format", "lsp_completion"}},
		{Name: "agent", Tools: []string{"spawn_agent", "parallel_dispatch", "task"}},
		{Name: "discovery", Tools: []string{"tool_search"}},
	},
	Profiles: []ToolProfileConfig{
		{Name: "full", Tools: []string{"*"}},
		{Name: "coding", Tools: []string{"group:fs", "group:runtime", "group:web", "group:lsp", "group:discovery", "skill", "memory", "batch", "question"}},
		{Name: "readonly", Tools: []string{"read_file", "glob", "grep", "ls", "websearch", "webfetch"}},
		{Name: "messaging", Tools: []string{"send_im_message", "wechat_ops", "skill"}},
		// Master 编排器最小工具集：只保留路由/委托/会话管理所需工具，不持有高副作用执行工具
		{Name: "master", Tools: []string{"skill", "memory", "question", "taskboard", "task", "spawn_agent", "parallel_dispatch"}},
		// P0-3: Master 直接执行 profile — 包含所有常用工具，Master ReAct 循环直接执行任务
		{Name: "master_direct", Tools: []string{
			"group:fs", "group:runtime", "group:web", "group:lsp", "group:agent", "group:discovery",
			"create_tool", "remove_tool",
			"skill", "memory", "question", "taskboard", "batch",
			"send_im_message", "feishu_api",
			"wechat_send_rich_message", "wechat_contacts", "wechat_groups",
			"wechat_profile", "wechat_moments", "wechat_status",
		}},
	},
	SubagentDeny:     []string{"spawn_agent", "create_tool", "remove_tool"},
	SubagentLeafDeny: []string{"parallel_dispatch", "task"},
	MasterProfile:    "master_direct", // P0-3: 切换到包含所有常用工具的 profile
}

// Memory 默认值
var DefaultMemoryConfig = MemoryConfig{
	Enabled:         true,
	MaxMemories:     10000,
	RetentionDays:   90,
	AutoExtract:     true,
	InjectMaxTokens: 2000,
	InjectTopK:      5,
	VectorStoreType: "auto",
}

// LSP 默认值
var DefaultLSPConfig = LSPConfig{
	Enabled:        DefaultLSPEnabled,
	Timeout:        DefaultLSPTimeout,
	MaxServers:     DefaultLSPMaxServers,
	HealthInterval: DefaultLSPHealthInterval,
	Languages: map[string]LanguageSpec{
		"go": {
			Command:    "gopls",
			Args:       []string{"serve"},
			Extensions: []string{".go"},
			Disabled:   false,
		},
		"python": {
			Command:    "pyright-langserver",
			Args:       []string{"--stdio"},
			Extensions: []string{".py"},
			Disabled:   false,
		},
		"typescript": {
			Command:    "typescript-language-server",
			Args:       []string{"--stdio"},
			Extensions: []string{".ts", ".tsx", ".js", ".jsx"},
			Disabled:   false,
		},
	},
}
