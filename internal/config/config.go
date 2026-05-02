package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/chef-guo/agents-hive/internal/errs"
	"github.com/chef-guo/agents-hive/internal/llm"
	"github.com/chef-guo/agents-hive/internal/skills"
)

// Config 包含 agents-hive 系统的所有配置
type Config struct {
	Server          ServerConfig             `json:"server"`
	LLM             LLMConfig                `json:"llm"`
	Agent           AgentConfig              `json:"agent"`
	MCP             MCPConfig                `json:"mcp"`
	Logging         LoggingConfig            `json:"logging"`
	HITL            HITLConfig               `json:"hitl"`
	PromptLanguage  string                   `json:"prompt_language,omitempty"` // LLM 提示词语言: "zh-CN" | "en-US"
	Channel         ChannelConfig            `json:"channel,omitempty"`
	Gateway         GatewayConfig            `json:"gateway,omitempty"`
	Security        SecurityConfig           `json:"security,omitempty"`
	Tools           ToolsConfig              `json:"tools,omitempty"`
	Sandbox         SandboxConfig            `json:"sandbox,omitempty"`
	ControlPlane    ControlPlaneConfig       `json:"control_plane,omitempty"`
	ACPServer       ACPServerConfig          `json:"acp_server,omitempty"`
	Plugin          PluginConfig             `json:"plugin,omitempty"`
	WebUI           WebUIConfig              `json:"webui,omitempty"`
	LSP             LSPConfig                `json:"lsp,omitempty"`
	CustomToolsDir  string                   `json:"custom_tools_dir,omitempty"`                                   // 自定义工具目录，默认 .claw/tools
	SessionsDir     string                   `json:"sessions_dir,omitempty"`                                       // 会话存储目录，默认 ~/.claw/sessions
	Store           StoreConfig              `json:"store,omitempty"`                                              // 存储后端配置
	Commands        map[string]CommandConfig `json:"commands,omitempty"`                                           // 用户自定义命令
	InstructionURLs []string                 `json:"instruction_urls,omitempty" yaml:"instruction_urls,omitempty"` // 远程指令文件 URL 列表
	RemoteAgents    []RemoteAgentConfig      `json:"remote_agents,omitempty"`                                      // 远程 ACP Agent 配置
	Memory          MemoryConfig             `json:"memory,omitempty"`                                             // 记忆系统配置
	Auth            AuthConfig               `json:"auth,omitempty"`
	PromptsDir      string                   `json:"prompts_dir,omitempty"` // 外部 prompt 文件目录（可选，优先于 go:embed）
	SpecDriven      SpecDrivenConfig         `json:"spec_driven,omitempty"` // Spec-driven Phase 2 总开关（默认 mode=legacy，零成本短路）
	RuntimePolicy   RuntimePolicyConfig      `json:"runtime_policy,omitempty"`
}

// RuntimePolicyConfig 定义运行时 timeout、容量和成本配置。
// 保持在 config 包内，避免 config 反向依赖 runtimepolicy 包。
type RuntimePolicyConfig struct {
	LLMCallTimeout      time.Duration `json:"llm_call_timeout,omitempty"`
	ToolTimeout         time.Duration `json:"tool_timeout,omitempty"`
	TaskTimeout         time.Duration `json:"task_timeout,omitempty"`
	SpawnAgentTimeout   time.Duration `json:"spawn_agent_timeout,omitempty"`
	ACPPromptTimeout    time.Duration `json:"acp_prompt_timeout,omitempty"`
	ACPReconnectTimeout time.Duration `json:"acp_reconnect_timeout,omitempty"`
	SubagentMaxTurns    int           `json:"subagent_max_turns,omitempty"`
	SubagentMaxDepth    int           `json:"subagent_max_depth,omitempty"`
	PerSessionParallel  int           `json:"per_session_parallel,omitempty"`
	GlobalWorkers       int           `json:"global_workers,omitempty"`
	MaxSessionCostUSD   float64       `json:"max_session_cost_usd,omitempty"`
}

// SpecDrivenConfig 是 spec-driven cognition Phase 2 的总配置。
//
// 设计原则（openspec/changes/harden-spec-driven-phase2/design.md）：
//   - 默认 Mode="legacy"——所有 spec 路径完全 short-circuit，零成本、零行为变更。
//   - 非法 Mode 值在 intake.ResolveIntakeDecision 内部 fail-closed 回落 legacy。
//   - Continuation.Default="off"——FM-1 纪律：未显式 ON 不应用 MRU 续写。
//   - Planner.TokenBudget=800——planner 调用默认最便宜模型 + 硬上限。
//
// 配置文件示例：
//
//	"spec_driven": {
//	    "mode": "legacy",
//	    "continuation": { "default": "off" },
//	    "planner": { "token_budget": 800 }
//	}
type SpecDrivenConfig struct {
	// Mode 总开关：legacy | dual | spec。默认 legacy。
	// - legacy: 所有 spec 路径跳过（零开销）
	// - dual:   spec + legacy 双跑，响应以 legacy 为准，差异 → diff log
	// - spec:   spec 为 primary，legacy 仅 fallback
	Mode string `json:"mode,omitempty"`

	// Continuation 是 continuation resolver 的子配置。
	Continuation SpecContinuationConfig `json:"continuation,omitempty"`

	// Planner 是 planner 模块的子配置。
	Planner SpecPlannerConfig `json:"planner,omitempty"`

	// SubagentMode SubAgent 派生策略 — legacy | dual | spec-only。默认 legacy。
	// 依赖 Enabled()==true（Mode != "legacy"）；true 且 Mode=legacy → bootstrap fail-fast
	// （hive-skill-on-demand spec.md D15/validateFlagCombination）。
	SubagentMode string `json:"subagent_mode,omitempty"`

	// SkillsSemanticRouting 是否让 planner 走 SpecSkillResolver（本地+远程语义路由）。
	// 依赖 Enabled()==true；true 且 Mode=legacy → bootstrap fail-fast。
	SkillsSemanticRouting bool `json:"skills_semantic_routing,omitempty"`
}

// Enabled 判断 spec-driven 是否启用（mode != "legacy"）。
// 4-dim flag 矩阵（D15）里的第一维"specdriven.enabled"实际由 Mode 决定：
//   - Mode == "legacy" → 关（零成本短路）
//   - Mode ∈ {"dual", "spec"} → 开
func (s SpecDrivenConfig) Enabled() bool {
	return s.Mode != "" && s.Mode != DefaultSpecDrivenMode
}

// SubagentModeEnabled 判断 subagent_mode 维度是否开启。
func (s SpecDrivenConfig) SubagentModeEnabled() bool {
	return s.SubagentMode != "" && s.SubagentMode != "legacy"
}

// SpecContinuationConfig 控制 continuation resolver（FM-1 反例：默认 OFF）。
type SpecContinuationConfig struct {
	// Default：新 session 未显式设置时的续写策略——off | on | ask。默认 off。
	// FM-1 纪律：不允许静默 MRU 续写，必须用户显式 ON 或关键词触发。
	Default string `json:"default,omitempty"`
}

// SpecPlannerConfig 控制 planner 模块（Guard 4：token budget + schema gate）。
type SpecPlannerConfig struct {
	// TokenBudget：planner 单次调用的 max_tokens 上限。默认 800。
	// 超过此预算的 planner 响应 → downshift 到 legacy (DownshiftPlannerOverBudget)。
	TokenBudget int `json:"token_budget,omitempty"`
}

// MemoryConfig 记忆系统配置
type MemoryConfig struct {
	Enabled          bool   `json:"enabled"`                     // 主开关，默认 true
	MaxMemories      int    `json:"max_memories,omitempty"`      // 最大记忆数量，默认 10000
	RetentionDays    int    `json:"retention_days,omitempty"`    // 记忆保留天数，默认 90
	AutoExtract      bool   `json:"auto_extract,omitempty"`      // 自动提取记忆，默认 true
	InjectMaxTokens  int    `json:"inject_max_tokens,omitempty"` // 注入上下文的最大 token 数，默认 2000
	InjectTopK       int    `json:"inject_top_k,omitempty"`      // 注入上下文的最大记忆条数，默认 5
	EmbeddingEnabled bool   `json:"embedding_enabled,omitempty"` // 启用向量嵌入搜索
	EmbeddingModel   string `json:"embedding_model,omitempty"`   // 嵌入模型名称
	VectorStoreType  string `json:"vector_store_type,omitempty"` // 向量索引类型："auto"(默认) | "memory" | "pgvector"
}

// RemoteAgentConfig 远程 ACP Agent 配置
type RemoteAgentConfig struct {
	Name        string            `json:"name"`              // 唯一名称，用作 SubAgent ID
	Description string            `json:"description"`       // Agent 能力描述（供 Planner 参考）
	Transport   string            `json:"transport"`         // "stdio" | "http"
	Command     string            `json:"command,omitempty"` // stdio 模式：启动命令
	Args        []string          `json:"args,omitempty"`    // stdio 模式：命令参数
	URL         string            `json:"url,omitempty"`     // http 模式：远程 URL
	Headers     map[string]string `json:"headers,omitempty"` // http 模式：HTTP headers
	Skills      []string          `json:"skills,omitempty"`  // Agent 声明的能力标签
	Enabled     bool              `json:"enabled"`           // 是否启用
}

// StoreConfig 存储后端配置（PostgreSQL）
type StoreConfig struct {
	Type     string         `json:"type,omitempty"`     // 保留用于兼容，实际只支持 postgres
	Postgres PostgresConfig `json:"postgres,omitempty"` // PostgreSQL 存储配置
}

// PostgresConfig PostgreSQL 连接配置
type PostgresConfig struct {
	DSN      string `json:"dsn,omitempty"` // 完整连接串（优先）
	Host     string `json:"host,omitempty"`
	Port     int    `json:"port,omitempty"`
	Database string `json:"database,omitempty"`
	User     string `json:"user,omitempty"`
	Password string `json:"password,omitempty"`
	SSLMode  string `json:"ssl_mode,omitempty"`
	MaxConns int    `json:"max_conns,omitempty"` // 连接池大小，默认 10
}

// CommandConfig 用户自定义命令配置
type CommandConfig struct {
	Description string `json:"description,omitempty"`
	Template    string `json:"template"`
	Agent       string `json:"agent,omitempty"`
	Model       string `json:"model,omitempty"`
	Subtask     bool   `json:"subtask,omitempty"`
}

// ServerConfig 配置 HTTP API 服务器
type ServerConfig struct {
	Port        int      `json:"port"`
	Host        string   `json:"host"`
	BaseURL     string   `json:"base_url,omitempty"`     // 对外可访问的 base URL，用于构造图片等资源链接；为空时默认 http://localhost:<port>
	CORSOrigins []string `json:"cors_origins,omitempty"` // 允许的来源；空表示仅 localhost
}

// ServerBaseURL 返回对外可访问的 base URL。
// 优先使用配置的 BaseURL，否则 fallback 到 http://localhost:<port>。
func (s ServerConfig) ServerBaseURL() string {
	if s.BaseURL != "" {
		return s.BaseURL
	}
	return fmt.Sprintf("http://localhost:%d", s.Port)
}

// HITLConfig 配置人机协同行为
type HITLConfig struct {
	Enabled                 bool                    `json:"enabled"`                             // 主开关，默认 false
	StepConfirmation        string                  `json:"step_confirmation"`                   // "none" 或 "all"
	InputTimeout            time.Duration           `json:"input_timeout"`                       // 等待输入超时时间，默认 5分钟
	WebSocketEnabled        bool                    `json:"websocket_enabled"`                   // 启用 WebSocket 端点
	WebSocketInsecureOrigin bool                    `json:"websocket_insecure_origin"`           // 跳过来源检查（仅开发环境）
	WebSocketToken          string                  `json:"websocket_token,omitempty"`           // WebSocket 认证 token
	WebSocketMaxConnPerIP   int                     `json:"websocket_max_conn_per_ip,omitempty"` // 单 IP 最大连接数，默认 5
	PermissionRules         []skills.PermissionRule `json:"permission_rules,omitempty"`          // 工具权限规则

	// 域E Phase 2：LLM 语义分类器
	// AutoClassify=true 时，在 glob 规则未命中后先调用 LLM 分类器判断是否安全；
	// 分类为安全的操作自动放行，不触发 HITL。失败时 fail closed（依然触发 HITL）。
	AutoClassify bool `json:"auto_classify,omitempty"` // 启用 LLM 分类器自动审批（默认 false）
}

// Validate 检查 HITLConfig 值的正确性
func (h HITLConfig) Validate() error {
	if h.StepConfirmation != "" && h.StepConfirmation != "none" && h.StepConfirmation != "all" {
		return errs.New(errs.CodeConfigInvalid, fmt.Sprintf("hitl.step_confirmation must be \"none\" or \"all\", got %q", h.StepConfirmation))
	}
	if h.InputTimeout < 0 {
		return errs.New(errs.CodeConfigInvalid, "hitl.input_timeout must be non-negative")
	}
	return nil
}

// ModelProfile 是一个命名的 LLM 配置预设
type ModelProfile struct {
	Name     string `json:"name"`               // 显示名称，例如 "deepseek"、"gpt4o"
	Provider string `json:"provider,omitempty"` // provider 名称（可选，用于推断 BaseURL）
	Model    string `json:"model"`              // 发送到 API 的模型 ID
	BaseURL  string `json:"base_url,omitempty"` // API 端点
	APIKey   string `json:"api_key,omitempty"`  // 提供商特定的密钥（可选）
}

// LLMConfig 配置 LLM 提供商
type LLMConfig struct {
	Provider        string         `json:"provider,omitempty"` // provider 名称: "openai"/"deepseek"/"anthropic"/"google"/"azure"/"groq"/"mistral"/"xai"/"custom"
	APIKey          string         `json:"api_key"`
	Model           string         `json:"model"`
	BaseURL         string         `json:"base_url"`
	DisableJSONMode bool           `json:"disable_json_mode,omitempty"` // 禁用 response_format 参数（用于不兼容的 API）
	Models          []ModelProfile `json:"models,omitempty"`            // 可用的模型预设

	// Google 特有配置
	GoogleAPIKey string `json:"google_api_key,omitempty"` // Google Gemini API Key

	// Azure 特有配置
	AzureAPIKey     string `json:"azure_api_key,omitempty"`    // Azure OpenAI API Key
	AzureDeployment string `json:"azure_deployment,omitempty"` // Azure 部署名称
	AzureEndpoint   string `json:"azure_endpoint,omitempty"`   // Azure 端点 URL

	// 统一推理努力级别，支持 "low"/"medium"/"high"/"max"，空表示不启用
	ReasoningEffort string `json:"reasoning_effort,omitempty"`

	// 隐私选项：为 OpenAI/Copilot 请求设置 store=false，防止数据用于训练
	StorePrivacy bool `json:"store_privacy,omitempty" yaml:"store_privacy,omitempty"`

	// API 格式："chat"（Chat Completions）或 "responses"（Responses API），默认 "chat"
	APIFormat string `json:"api_format,omitempty" yaml:"api_format,omitempty"`

	// 远程模型注册表 URL（可选）；为空则跳过远程获取，仅使用内置默认模型
	ModelRegistryURL string `json:"model_registry_url,omitempty" yaml:"model_registry_url,omitempty"`
}

// SkillsConfig 配置远程和本地 skill 发现
//
// 按需安装（hive-skill-on-demand）引入 5 个新字段：
//   - MarketplaceURLs：按顺序枚举的远程 marketplace index.json URL；支持多 marketplace 按顺序查
//   - OnDemandEnabled：主开关，关则不注册 skill_install / skill_search 工具
//   - PublicSkillsDir / PersonalSkillsDir：两个 scope 各自的本地存放根；空串走 $HIVE_DATA 下默认子目录
//   - PinnedVersions：映射 skill.name → semver 约束，强制 Registry 选择特定版本而非最新
//
// 启动期校验见 ValidateFlagCombination / ValidateSkillsConfig。
type SkillsConfig struct {
	Paths []string `json:"paths,omitempty"` // 额外的本地搜索路径（旧字段，仍兼容）
	URLs  []string `json:"urls,omitempty"`  // 远程 skill 仓库 URL 列表（旧字段，启动期会并入 MarketplaceURLs 兜底）

	// OnDemandEnabled 开关按需安装 skill_install / skill_search 工具注册。
	// false（默认）→ 工具未注册，planner 走旧路径；true → 工具注册 + 语义路由生效。
	OnDemandEnabled bool `json:"on_demand_enabled,omitempty"`

	// MarketplaceURLs 按优先级顺序枚举 marketplace index.json URL。
	// 启动期：OnDemandEnabled=true 且 MarketplaceURLs 空 → bootstrap fail-fast。
	MarketplaceURLs []string `json:"marketplace_urls,omitempty"`

	// PublicSkillsDir public scope 的 skill 存放目录（扫描根）；空串使用默认 $HIVE_DATA/skills/public。
	PublicSkillsDir string `json:"public_skills_dir,omitempty"`

	// PersonalSkillsDir personal scope 的 skill 存放目录（扫描根）；空串使用默认 $HIVE_DATA/skills/users。
	// 真实路径以 userID 为子目录：<PersonalSkillsDir>/<userID>/<skillName>/SKILL.md。
	PersonalSkillsDir string `json:"personal_skills_dir,omitempty"`

	// PinnedVersions 强制版本映射：skill name → semver 约束（如 "1.2.3" 或 ">=1.0,<2.0"）。
	// Registry.Register 按 semver 降序选取满足约束的最高版本；不满足 → 拒绝注册。
	PinnedVersions map[string]string `json:"pinned_versions,omitempty"`
}

// AgentConfig 配置 Agent 行为
type AgentConfig struct {
	Timeout             time.Duration       `json:"timeout"`
	MaxConcurrentAgents int                 `json:"max_concurrent_agents"`
	HealthInterval      time.Duration       `json:"health_interval"`
	ShellTimeout        time.Duration       `json:"shell_timeout"`                 // Shell 命令执行超时，默认 10s
	ScriptTimeout       time.Duration       `json:"script_timeout"`                // 脚本执行超时，默认 30s
	WSPingInterval      time.Duration       `json:"ws_ping_interval"`              // WebSocket ping 间隔，默认 30s
	SyncInterval        time.Duration       `json:"sync_interval"`                 // 后台会话同步间隔，默认 5m
	ContextCompression  CompactionConfig    `json:"context_compression,omitempty"` // 上下文压缩配置
	Skills              SkillsConfig        `json:"skills,omitempty"`              // 远程/本地 skill 配置
	ToolPolicy          ToolPolicyConfig    `json:"tool_policy,omitempty"`         // 工具过滤策略配置
	MaxSessionCost      float64             `json:"max_session_cost,omitempty"`    // per-session 成本预算上限（USD），<=0 不限制（需要 PostgreSQL 成本追踪启用）
	QualityGuards       QualityGuardsConfig `json:"quality_guards,omitempty"`      // 质量护栏灰度开关（见 docs/计划与路线/Agent-质量护栏治理计划.md）
	PlanRuntime         PlanRuntimeConfig   `json:"plan_runtime,omitempty"`        // session 级 plan/todos runtime，默认关闭
}

// PlanRuntimeConfig 控制 session-scoped todos 与 Plan Runtime Guard。
type PlanRuntimeConfig struct {
	Enabled bool `json:"enabled,omitempty"`
}

// QualityGuardsConfig 控制 agent-quality-remediation-plan P0 措施的灰度启用。
// 三个开关彼此独立，任一关闭即恢复该项的旧行为。
type QualityGuardsConfig struct {
	// ToolChoiceForce 启用 P0-A：ReAct 主循环根据用户查询自动决定 tool_choice。
	// 启用后会在事实型问题 / URL / 文件路径 / ".skill" 引用上强制 required，纯闲聊上强制 none。
	ToolChoiceForce bool `json:"tool_choice_force,omitempty"`
	// WebsearchStrict 启用 P0-B：websearch 零结果转 IsError=true，避免"HTTP 200 + 空 DOM"静默成功。
	WebsearchStrict bool `json:"websearch_strict,omitempty"`
	// PostValidation 启用 P0-D：后置 grounding validator 与 middleware pipeline（Phase 2 交付）。
	PostValidation bool `json:"post_validation,omitempty"`
}

// MCPConfig 配置 MCP 工具集成
type MCPConfig struct {
	Timeout time.Duration              `json:"timeout"`
	Servers map[string]MCPServerConfig `json:"servers,omitempty"` // 命名的 MCP 服务端配置
}

// MCPServerConfig 单个 MCP 服务端配置
type MCPServerConfig struct {
	Command   string            `json:"command,omitempty"`   // stdio 模式的命令
	Args      []string          `json:"args,omitempty"`      // stdio 模式的参数
	Env       map[string]string `json:"env,omitempty"`       // stdio 模式的附加环境变量
	Transport string            `json:"transport,omitempty"` // "stdio"（默认）| "sse" | "http"
	URL       string            `json:"url,omitempty"`       // SSE/HTTP 模式的服务端 URL
	Headers   map[string]string `json:"headers,omitempty"`   // 自定义 HTTP 头
	Timeout   string            `json:"timeout,omitempty"`   // 超时时间，如 "30s"
	OAuth     *OAuthConfig      `json:"oauth,omitempty"`     // OAuth PKCE 配置（可选）
}

// OAuthConfig MCP 服务器 OAuth 配置
type OAuthConfig struct {
	ClientID     string   `json:"client_id" yaml:"client_id"`
	ClientSecret string   `json:"client_secret,omitempty" yaml:"client_secret,omitempty"`
	AuthURL      string   `json:"auth_url" yaml:"auth_url"`
	TokenURL     string   `json:"token_url" yaml:"token_url"`
	Scopes       []string `json:"scopes,omitempty" yaml:"scopes,omitempty"`
}

// LoggingConfig 配置日志记录
type LoggingConfig struct {
	Level        string `json:"level"`         // 日志级别: debug/info/warn/error
	Format       string `json:"format"`        // 格式: "json" 或 "console"
	File         string `json:"file"`          // 日志文件路径（空表示不写文件）
	ConsoleLevel string `json:"console_level"` // 控制台日志级别（CLI模式，默认 "error"）
	MaxSize      int    `json:"max_size"`      // 日志文件最大大小（MB，默认 100）
	MaxBackups   int    `json:"max_backups"`   // 保留的旧日志文件数量（默认 3）
	MaxAge       int    `json:"max_age"`       // 日志文件保留天数（默认 7）
}

// Default 返回包含引导默认值的 Config。
// 运行时配置（HITL/Agent/Memory/Channel 等）的默认值由数据库 SQL 种子提供，
// 此处仅设置连接数据库之前就需要的引导参数。
func Default() *Config {
	return &Config{
		Server: ServerConfig{
			Port: DefaultServerPort,
			Host: "0.0.0.0",
		},
		Logging: LoggingConfig{
			Level:        DefaultLogLevel,
			Format:       "json",
			File:         DefaultLogFile,
			ConsoleLevel: DefaultConsoleLevel,
			MaxSize:      DefaultLogMaxSize,
			MaxBackups:   DefaultLogMaxBackups,
			MaxAge:       DefaultLogMaxAge,
		},
		Gateway: DefaultGatewayConfig,
		Store: StoreConfig{
			Type: "postgres",
		},
		SpecDriven: DefaultSpecDrivenConfig,
		// LLM 及其他运行时配置的默认值由 DB SQL 种子提供，服务器模式下由 LoadLLMFromDB / LoadAllConfigFromDB 填充。
		// CLI 模式（无 DB）使用 CLIDefaults() 填充。
	}
}

// CLIDefaults 为 CLI 模式（无数据库）填充运行时配置默认值。
// 服务器模式下这些值由 DB 种子提供，不需要调用此函数。
func (c *Config) CLIDefaults() {
	c.LLM = LLMConfig{
		Provider: DefaultProvider,
		Model:    DefaultModel,
		BaseURL:  DefaultBaseURL,
	}
	c.Agent = AgentConfig{
		Timeout:             DefaultAgentTimeout,
		MaxConcurrentAgents: DefaultMaxConcurrentAgents,
		HealthInterval:      DefaultHealthInterval,
		ShellTimeout:        DefaultShellTimeout,
		ScriptTimeout:       DefaultScriptTimeout,
		WSPingInterval:      DefaultWSPingInterval,
		SyncInterval:        DefaultSyncInterval,
		ContextCompression: CompactionConfig{
			Enabled:             DefaultCompactionEnabled,
			Strategy:            CompactStrategy(DefaultCompactionStrategy),
			MaxTokens:           DefaultCompactionMaxTokens,
			ReserveTokens:       DefaultCompactionReserve,
			CompactTimeout:      DefaultCompactionTimeout,
			UseTiktoken:         DefaultCompactionTiktoken,
			LazyMode:            DefaultCompactionLazyMode,
			LazyThreshold:       DefaultCompactionLazyThreshold,
			PipelineStages:      DefaultCompactionPipelineStages,
			ToolOutputMaxTokens: DefaultCompactionToolOutputMaxTokens,
		},
	}
	c.MCP = MCPConfig{
		Timeout: DefaultMCPTimeout,
	}
	c.HITL = HITLConfig{
		Enabled:          DefaultHITLEnabled,
		StepConfirmation: DefaultHITLStepConfirmation,
		InputTimeout:     DefaultHITLInputTimeout,
		WebSocketEnabled: DefaultHITLWebSocket,
		PermissionRules:  DefaultPermissionRules,
	}
	c.PromptLanguage = DefaultPromptLanguage
	c.Channel = DefaultChannelConfig
	c.Security = DefaultSecurityConfig
	c.Tools = ToolsConfig{CreateRequiresApproval: true}
	c.ControlPlane = DefaultControlPlaneConfig
	c.ACPServer = DefaultACPServerConfig
	c.Plugin = DefaultPluginConfig
	c.WebUI = DefaultWebUIConfig
	c.Memory = DefaultMemoryConfig
	c.LSP = DefaultLSPConfig
	c.CustomToolsDir = DefaultCustomToolsDir
	c.Agent.ToolPolicy = DefaultToolPolicyConfig
	c.SessionsDir = DefaultSessionsDir
	c.SpecDriven = DefaultSpecDrivenConfig
}

// Load 读取配置文件并返回 Config，缺失的值使用默认值
func Load(path string) (*Config, error) {
	cfg := Default()
	if path == "" {
		cfg.applyEnvOverrides()
		cfg.Resolve()
		return cfg, nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			cfg.applyEnvOverrides()
			cfg.Resolve()
			return cfg, nil
		}
		return nil, errs.Wrap(errs.CodeConfigInvalid, "读取配置文件失败", err)
	}

	// 展开 JSON 中的 ${VAR_NAME} 环境变量占位符
	expanded := os.ExpandEnv(string(data))

	if err := json.Unmarshal([]byte(expanded), cfg); err != nil {
		return nil, errs.Wrap(errs.CodeConfigInvalid, "解析配置文件失败", err)
	}

	cfg.applyEnvOverrides()
	cfg.Resolve()

	if err := cfg.HITL.Validate(); err != nil {
		return nil, errs.Wrap(errs.CodeConfigInvalid, "验证配置失败", err)
	}

	// P0-#14 dual-ingress fatal guard：webhook + longconn 同进程并存即拒绝启动。
	// Validate 只校验这一条 invariant，不修改字段（Normalize 才修字段）。
	if err := cfg.Channel.Feishu.Validate(); err != nil {
		return nil, errs.Wrap(errs.CodeConfigInvalid, "验证飞书配置失败", err)
	}

	return cfg, nil
}

// applyEnvOverrides 应用环境变量覆盖
// 优先级: CLAW_* > OPENAI_* > 配置文件 > 默认值
func (c *Config) applyEnvOverrides() {
	// Provider: CLAW_PROVIDER > 配置文件 > 默认值
	if envProvider := os.Getenv("CLAW_PROVIDER"); envProvider != "" {
		c.LLM.Provider = envProvider
	}

	// 模型: CLAW_MODEL > 配置文件 > 默认值
	if envModel := os.Getenv("CLAW_MODEL"); envModel != "" {
		c.LLM.Model = envModel
	}

	// API 密钥: CLAW_API_KEY > OPENAI_API_KEY > 配置文件
	if c.LLM.APIKey == "" {
		if key := os.Getenv("CLAW_API_KEY"); key != "" {
			c.LLM.APIKey = key
		} else {
			c.LLM.APIKey = os.Getenv("OPENAI_API_KEY")
		}
	}

	// Base URL: CLAW_BASE_URL > OPENAI_BASE_URL > 配置文件 > 默认值
	if envURL := os.Getenv("CLAW_BASE_URL"); envURL != "" {
		c.LLM.BaseURL = envURL
	} else if c.LLM.BaseURL == "" {
		if envURL := os.Getenv("OPENAI_BASE_URL"); envURL != "" {
			c.LLM.BaseURL = envURL
		}
	}

	// 日志级别: CLAW_LOG_LEVEL > 配置文件 > 默认值
	if envLevel := os.Getenv("CLAW_LOG_LEVEL"); envLevel != "" {
		c.Logging.Level = envLevel
	}

	// 日志文件: CLAW_LOG_FILE > 配置文件 > 默认值
	if envFile := os.Getenv("CLAW_LOG_FILE"); envFile != "" {
		c.Logging.File = envFile
	}

	// 控制台级别: CLAW_CONSOLE_LEVEL > 配置文件 > 默认值
	if envConsoleLevel := os.Getenv("CLAW_CONSOLE_LEVEL"); envConsoleLevel != "" {
		c.Logging.ConsoleLevel = envConsoleLevel
	}

	// 提示词语言: CLAW_PROMPT_LANGUAGE > 配置文件 > 默认值
	if envLang := os.Getenv("CLAW_PROMPT_LANGUAGE"); envLang != "" {
		c.PromptLanguage = envLang
	}

	// 推理努力级别: CLAW_REASONING_EFFORT > 配置文件 > 默认值（空）
	if envEffort := os.Getenv("CLAW_REASONING_EFFORT"); envEffort != "" {
		c.LLM.ReasoningEffort = envEffort
	}

	// Google 特有环境变量: GOOGLE_API_KEY > CLAW_GOOGLE_API_KEY
	if c.LLM.GoogleAPIKey == "" {
		if key := os.Getenv("GOOGLE_API_KEY"); key != "" {
			c.LLM.GoogleAPIKey = key
		} else if key := os.Getenv("CLAW_GOOGLE_API_KEY"); key != "" {
			c.LLM.GoogleAPIKey = key
		}
	}

	// Azure 特有环境变量
	if c.LLM.AzureAPIKey == "" {
		if key := os.Getenv("AZURE_OPENAI_API_KEY"); key != "" {
			c.LLM.AzureAPIKey = key
		} else if key := os.Getenv("CLAW_AZURE_API_KEY"); key != "" {
			c.LLM.AzureAPIKey = key
		}
	}
	if c.LLM.AzureDeployment == "" {
		if dep := os.Getenv("AZURE_DEPLOYMENT"); dep != "" {
			c.LLM.AzureDeployment = dep
		} else if dep := os.Getenv("CLAW_AZURE_DEPLOYMENT"); dep != "" {
			c.LLM.AzureDeployment = dep
		}
	}
	if c.LLM.AzureEndpoint == "" {
		if endpoint := os.Getenv("AZURE_OPENAI_ENDPOINT"); endpoint != "" {
			c.LLM.AzureEndpoint = endpoint
		} else if endpoint := os.Getenv("CLAW_AZURE_ENDPOINT"); endpoint != "" {
			c.LLM.AzureEndpoint = endpoint
		}
	}

	// PostgreSQL: DATABASE_URL 优先，否则读各字段环境变量
	if dsn := os.Getenv("DATABASE_URL"); dsn != "" {
		c.Store.Postgres.DSN = dsn
	} else {
		if v := os.Getenv("POSTGRES_HOST"); v != "" {
			c.Store.Postgres.Host = v
		}
		if v := os.Getenv("POSTGRES_PORT"); v != "" {
			if p, err := strconv.Atoi(v); err == nil {
				c.Store.Postgres.Port = p
			}
		}
		if v := os.Getenv("POSTGRES_DB"); v != "" {
			c.Store.Postgres.Database = v
		}
		if v := os.Getenv("POSTGRES_USER"); v != "" {
			c.Store.Postgres.User = v
		}
		if v := os.Getenv("POSTGRES_PASSWORD"); v != "" {
			c.Store.Postgres.Password = v
		}
		if v := os.Getenv("POSTGRES_SSL_MODE"); v != "" {
			c.Store.Postgres.SSLMode = v
		}
	}

	// 会话目录: SESSIONS_DIR > 配置文件 > 默认值
	if v := os.Getenv("SESSIONS_DIR"); v != "" {
		c.SessionsDir = v
	}

	// 自定义工具目录: CUSTOM_TOOLS_DIR > 配置文件 > 默认值
	if v := os.Getenv("CUSTOM_TOOLS_DIR"); v != "" {
		c.CustomToolsDir = v
	}

}

// ApplyOverrides 应用 CLI 标志覆盖（最高优先级）
func (c *Config) ApplyOverrides(model, baseURL, apiKey, logLevel string) {
	if model != "" {
		c.LLM.Model = model
	}
	if baseURL != "" {
		c.LLM.BaseURL = baseURL
	}
	if apiKey != "" {
		c.LLM.APIKey = apiKey
	}
	if logLevel != "" {
		c.Logging.Level = logLevel
	}
}

// FindProfile 按名称查找模型配置（不区分大小写）
func (c *Config) FindProfile(name string) (ModelProfile, bool) {
	lower := strings.ToLower(name)
	for _, p := range c.LLM.Models {
		if strings.ToLower(p.Name) == lower {
			return p, true
		}
	}
	return ModelProfile{}, false
}

// ActiveProfileName 返回当前活动模型的显示名称
// 如果活动模型匹配某个配置，则返回该配置的名称
// 否则返回原始模型 ID
func (c *Config) ActiveProfileName() string {
	for _, p := range c.LLM.Models {
		if p.Model == c.LLM.Model && p.BaseURL == c.LLM.BaseURL {
			return p.Name
		}
	}
	return c.LLM.Model
}

// EnsureActiveInProfiles 确保活动的 LLM 配置出现在配置列表中
func (c *Config) EnsureActiveInProfiles() {
	for _, p := range c.LLM.Models {
		if p.Model == c.LLM.Model {
			return
		}
	}
	// 将活动模型添加为配置
	c.LLM.Models = append([]ModelProfile{{
		Name:    c.LLM.Model,
		Model:   c.LLM.Model,
		BaseURL: c.LLM.BaseURL,
		APIKey:  c.LLM.APIKey,
	}}, c.LLM.Models...)
}

// Resolve 根据 Provider 填充缺失的 LLM 配置字段
// 调用时机：Load() 中 applyEnvOverrides() 之后
//
// 逻辑：
// 1. 若 Provider 为空但 BaseURL 有值 → 从 URL 推断 Provider
// 2. 若 Provider 为空且 BaseURL 也为空 → 默认 "openai"
// 3. 从 ProviderDef 填充 BaseURL（若空）、Model（若空）、DisableJSONMode
func (c *Config) Resolve() {
	l := &c.LLM

	// 推断 Provider
	if l.Provider == "" {
		if l.BaseURL != "" {
			l.Provider = llm.InferProviderFromURL(l.BaseURL)
		}
		if l.Provider == "" {
			l.Provider = DefaultProvider
		}
	}

	// 查找 ProviderDef
	def := llm.LookupProvider(l.Provider)

	// 填充 BaseURL（若空）
	if l.BaseURL == "" && def.BaseURL != "" {
		l.BaseURL = def.BaseURL
	}

	// 填充 Model（若空）
	if l.Model == "" && def.DefaultModel != "" {
		l.Model = def.DefaultModel
	}

	// 继承 DisableJSONMode（仅当用户未显式设置时）
	if def.DisableJSONMode && !l.DisableJSONMode {
		l.DisableJSONMode = true
	}

	// 解析 ModelProfile 条目
	for i := range l.Models {
		mp := &l.Models[i]
		if mp.Provider == "" && mp.BaseURL != "" {
			mp.Provider = llm.InferProviderFromURL(mp.BaseURL)
		}
		if mp.Provider != "" {
			mpDef := llm.LookupProvider(mp.Provider)
			if mp.BaseURL == "" && mpDef.BaseURL != "" {
				mp.BaseURL = mpDef.BaseURL
			}
			if mp.Model == "" && mpDef.DefaultModel != "" {
				mp.Model = mpDef.DefaultModel
			}
		}
		if mp.APIKey == "" {
			mp.APIKey = l.APIKey
		}
	}

	// WebUI 启用时自动启用 WebSocket（前端依赖 WebSocket 推送消息）
	if c.WebUI.Enabled && !c.HITL.WebSocketEnabled {
		c.HITL.WebSocketEnabled = true
	}

	// 校验数值配置，非法值使用默认值（Resolve 阶段无 logger，静默修正）
	if c.Agent.ContextCompression.MaxTokens < 0 {
		c.Agent.ContextCompression.MaxTokens = 0 // 0 表示不限制
	}
	if c.ControlPlane.MaxSessions < 0 {
		c.ControlPlane.MaxSessions = DefaultControlPlaneConfig.MaxSessions
	}
	if c.ControlPlane.RateLimit < 0 {
		c.ControlPlane.RateLimit = DefaultControlPlaneConfig.RateLimit
	}

	// P0-3: MaxSessionCost <= 0 表示不限制，不设默认值（react_processor 用 > 0 判断）
	if c.Agent.MaxSessionCost < 0 {
		c.Agent.MaxSessionCost = 0
	}
}

// BootstrapFileConfig 仅包含需要保存到配置文件的引导参数。
// 运行时配置（LLM/HITL/Agent/Memory 等）存储在数据库中，不写入文件。
type BootstrapFileConfig struct {
	Server  ServerConfig  `json:"server"`
	Store   StoreConfig   `json:"store,omitempty"`
	Logging LoggingConfig `json:"logging"`
	Gateway GatewayConfig `json:"gateway,omitempty"`
}

// SaveToFile 将引导配置保存到文件（仅 server/store/logging/gateway）
func (c *Config) SaveToFile(path string) error {
	if path == "" {
		return errs.New(errs.CodeConfigInvalid, "配置文件路径不能为空")
	}

	bootstrap := BootstrapFileConfig{
		Server:  c.Server,
		Store:   c.Store,
		Logging: c.Logging,
		Gateway: c.Gateway,
	}
	data, err := json.MarshalIndent(bootstrap, "", "  ")
	if err != nil {
		return errs.Wrap(errs.CodeConfigInvalid, "序列化配置失败", err)
	}

	// 原子写入：先写临时文件，再 rename 替换，避免崩溃时损坏配置文件
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".claw-config-tmp-")
	if err != nil {
		return errs.Wrap(errs.CodeConfigInvalid, "创建临时配置文件失败", err)
	}
	tmpPath := tmp.Name()

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return errs.Wrap(errs.CodeConfigInvalid, "写入临时配置文件失败", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return errs.Wrap(errs.CodeConfigInvalid, "同步临时配置文件失败", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return errs.Wrap(errs.CodeConfigInvalid, "关闭临时配置文件失败", err)
	}
	if err := os.Chmod(tmpPath, 0600); err != nil {
		os.Remove(tmpPath)
		return errs.Wrap(errs.CodeConfigInvalid, "设置配置文件权限失败", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath)
		return errs.Wrap(errs.CodeConfigInvalid, "重命名临时配置文件失败", err)
	}

	return nil
}

// ChannelConfig 配置 IM Channel 插件系统
type ChannelConfig struct {
	Enabled  bool           `json:"enabled"`
	DingTalk DingTalkConfig `json:"dingtalk,omitempty"`
	Feishu   FeishuConfig   `json:"feishu,omitempty"`
	WeCom    WeComConfig    `json:"wecom,omitempty"`
	WeChat   WeChatConfig   `json:"wechat,omitempty"`
}

// WeChatConfig 个人微信配置（多协议支持）
type WeChatConfig struct {
	// 向后兼容字段（已废弃，保留用于配置迁移）
	Enabled  bool   `json:"enabled,omitempty"`
	Protocol string `json:"protocol,omitempty"`

	// 多协议独立配置（每个协议有独立的开关）
	Wechaty      WechatyInstanceConfig      `json:"wechaty,omitempty"`
	WeChatPadPro WeChatPadProInstanceConfig `json:"wechatpadpro,omitempty"`
}

// UnmarshalJSON 自定义反序列化，支持旧配置格式自动转换
func (w *WeChatConfig) UnmarshalJSON(data []byte) error {
	// 先反序列化到临时结构
	type Alias WeChatConfig
	aux := &struct {
		*Alias
	}{
		Alias: (*Alias)(w),
	}

	if err := json.Unmarshal(data, aux); err != nil {
		return err
	}

	// 向后兼容：如果使用旧格式（enabled=true + protocol 字段），自动转换为新格式
	if w.Enabled && w.Protocol != "" {
		switch w.Protocol {
		case "wechaty":
			if !w.Wechaty.Enabled {
				w.Wechaty.Enabled = true
			}
		case "wechatpadpro":
			if !w.WeChatPadPro.Enabled {
				w.WeChatPadPro.Enabled = true
			}
		}
	}

	return nil
}

// WechatyInstanceConfig go-wechaty gRPC 桥接配置
type WechatyInstanceConfig struct {
	Enabled  bool   `json:"enabled"`
	Endpoint string `json:"endpoint"` // gRPC gateway 地址，例如 localhost:8788
	Token    string `json:"token"`    // Wechaty Puppet Token
}

// WeChatPadProInstanceConfig WeChatPadPro iPad 协议配置
type WeChatPadProInstanceConfig struct {
	Enabled  bool   `json:"enabled"`
	BaseURL  string `json:"base_url"`            // WeChatPadPro API 地址，默认 http://localhost:8848
	AppID    string `json:"app_id"`              // 应用 ID（可选）
	Token    string `json:"token"`               // API Token（可选，用于鉴权）
	AdminKey string `json:"admin_key,omitempty"` // 管理员 Key，用于自动生成 Token
	// Timeout 超时时间（秒）- 历史原因使用 int 而非 time.Duration，暂不改动
	Timeout int `json:"timeout"`
}

// DingTalkConfig 钉钉配置
type DingTalkConfig struct {
	Enabled   bool   `json:"enabled"`
	AppKey    string `json:"app_key"`
	AppSecret string `json:"app_secret"`
	Token     string `json:"token"`   // 回调签名 token
	AESKey    string `json:"aes_key"` // 回调加密 key
	AgentID   int64  `json:"agent_id"`
}

// FeishuConfig 飞书配置。
//
// 默认行为（Normalize 后的终态）：
//   - Enabled: false（需显式开）
//   - AckEmoji: "Get"（收到消息贴 Get 表情；"none" 显式禁用）
//   - Renderer.Disabled: false（反向语义，零值 = EventRenderer 启用）
//   - Renderer.ThrottleMs: 300（卡片 PATCH 最小间隔）
//   - Renderer.ShowAgentProgress: false
//
// 运维文档见 docs/渠道对接/feishu.md（事件→卡片片段映射表）；架构决策见
// openspec/changes/im-streaming-reply/design.md（EventRenderer 决策 D3/D4）。
// 注：Channel 配置存在 DB 里，config.example.json 不含 feishu 段——首启由
// MigrateConfigToDB 把老 config.json 的 feishu 块写入数据库，之后以 DB 为准。
type FeishuConfig struct {
	Enabled             bool                    `json:"enabled"`
	AppID               string                  `json:"app_id"`
	AppSecret           string                  `json:"app_secret"`
	Region              string                  `json:"region,omitempty"`
	VerificationToken   string                  `json:"verification_token"`
	EncryptKey          string                  `json:"encrypt_key"`
	EventEncryptEnabled bool                    `json:"event_encrypt_enabled,omitempty"`
	IngressMode         FeishuIngressMode       `json:"ingress_mode,omitempty"`
	Reliability         FeishuReliabilityConfig `json:"reliability,omitempty"`

	// LongconnEnabled 是飞书 longconn(WebSocket) 入站开关。
	// Phase 0 CEO 决议：生产 ingress 必须 webhook XOR longconn，**严禁同进程并存**——
	// 红队链 B：双入口 × dedup 失败 → 单消息双投 → renderer 双 PATCH → 用户看到两条回复。
	// 因此 Validate 阶段加 fatal guard：LongconnEnabled && WebhookURL != "" → 启动 panic。
	// 详见 docs/渠道对接/feishu-bot/09-reliability.md §4.4 与 reviews/README.md §112。
	LongconnEnabled bool `json:"longconn_enabled,omitempty"`

	// WebhookURL 是飞书事件订阅指向本进程的 webhook URL（如 https://host/feishu/webhook）。
	// 仅在用户显式声明 webhook 入口时填写——既是给运维的"入口已开"声明，也是 dual-ingress
	// guard 的判定依据。空值表示"本进程不承载 webhook"，与 LongconnEnabled=true 兼容；
	// 非空 + LongconnEnabled=true → Validate fatal。
	WebhookURL string `json:"webhook_url,omitempty"`

	// AckEmoji 收到用户消息后机器人即刻贴的"已受理"表情类型；
	// 由 Feishu renderer 订阅 input_received 事件触发，取代原 longconn 私有 AddReaction。
	// 合法值："Get"（默认） / "Typing" / "none"（显式禁用 ack，不发 AddReaction）。
	// 注：飞书 reactions API 的 emoji_type 是 CamelCase（`Get` / `Typing`），历史上
	// 本仓库曾误写全大写 `GET` / `KEYBOARD`，直接调用会触发 `code=231001 reaction type is invalid`。
	// Normalize 阶段对老值做透明迁移：`GET`→`Get`，`KEYBOARD`→`Typing`（不 warn，纯升级）；
	// 其他非法值 warn + 回退到 "Get"。
	// 实现注：Normalize 保留 "none" 字面量（不归一到空串），renderer 侧同时识别 `""` 和 `"none"`
	// 作为 skip 条件。这样保证 Normalize 幂等——避免"none → '' → 'Get'"两次归一后"禁用"变"默认"。
	AckEmoji string `json:"ack_emoji,omitempty"`

	// Renderer 控制飞书专属 EventRenderer 行为（订阅 WSBroadcast 事件流式渲染卡片）。
	// 未配置等同于"全部默认"——即 EventRenderer 启用、ThrottleMs=300、不展示 Agent 中间状态。
	Renderer FeishuRendererConfig `json:"renderer,omitempty"`

	// Inbound 控制 Phase 1 消息摄取增强能力。
	// 目前只包含 ContextResolver 开关：默认开启；显式 false 时回滚到"只看原始 content"。
	Inbound FeishuInboundConfig `json:"inbound,omitempty"`

	// Governance 控制 Phase 2A 最小治理能力。
	// 目前只开放 /reset ACL allowlist，其他 rollout/mute 先走 chat_state 持久化真相源。
	Governance FeishuGovernanceConfig `json:"governance,omitempty"`

	// Identity 控制 Phase 3 身份能力。
	Identity FeishuIdentityConfig `json:"identity,omitempty"`

	// Outbound 控制 Phase 4 出站限流/重试/二进制工具开关。
	Outbound FeishuOutboundConfig `json:"outbound,omitempty"`

	// Security 控制 Phase 5 权限降级等安全治理。
	Security FeishuSecurityConfig `json:"security,omitempty"`

	// Push 控制 Phase 6 主动推送。
	Push FeishuPushConfig `json:"push,omitempty"`
}

// FeishuRendererConfig 飞书 EventRenderer 行为配置。
// 字段设计上刻意反转 Enabled → Disabled：Go zero-value (false) 即"启用"，
// 老 DB 中没有 `renderer` 字段的配置 Unmarshal 后自动保持"启用"状态，避免升级时静默回退到 legacy 一次性推送。
// 显式 `"disabled": true` 才是"回滚到 legacy Plugin.Send"开关。
type FeishuRendererConfig struct {
	// Disabled 回滚开关。零值 (false) = 启用 EventRenderer；
	// 显式 true = 禁用，Router 走 Plugin.Send 一次性文本推送。
	// 用户 / 运维在出现线上问题时通过该字段快速回滚。
	Disabled bool `json:"disabled,omitempty"`
	// ThrottleMs 卡片 PATCH 最小间隔（毫秒），默认 300；<= 0 时 Normalize 回退到 300。
	ThrottleMs int `json:"throttle_ms,omitempty"`
	// ShowAgentProgress 是否在卡片里展示 "Agent 思考中" 这类中间状态文案。
	ShowAgentProgress bool `json:"show_agent_progress,omitempty"`
}

// FeishuInboundConfig 控制飞书入站消息增强能力。
type FeishuInboundConfig struct {
	// EnableContextResolver 控制是否启用父消息 / 文档引用 / wiki 解析。
	// nil（未配置）视为 true，保证升级到 Phase 1 后默认获得增强能力；
	// 显式 false 作为快速回滚开关。
	EnableContextResolver *bool `json:"enable_context_resolver,omitempty"`
}

// FeishuReliabilityConfig 控制 Phase 2B longconn 可靠性相关能力。
// 当前仅承载 watchdog / gap fetch 所需配置；运行时默认保持保守关闭。
type FeishuReliabilityConfig struct {
	// LongconnEnabled 是 Phase 2B 起推荐的 longconn 开关。
	// 为兼容老配置，ResolvedIngressMode 仍会回退读取 FeishuConfig.LongconnEnabled。
	LongconnEnabled bool `json:"longconn_enabled,omitempty"`
	// LongconnGapFetchEnabled 控制重连后是否尝试 gap fetch，默认 false。
	LongconnGapFetchEnabled bool `json:"longconn_gap_fetch_enabled,omitempty"`
	// HeartbeatStaleWindow 是 watchdog 认为“事件流陈旧”的阈值。
	HeartbeatStaleWindow time.Duration `json:"heartbeat_stale_window,omitempty"`
	// GapFetchMaxWindow 限制单次 gap fetch 允许追补的最大时间窗口。
	GapFetchMaxWindow time.Duration `json:"gap_fetch_max_window,omitempty"`
}

// FeishuIdentityConfig 控制 Phase 3 身份解析能力。
type FeishuIdentityConfig struct {
	UserCacheSize     int    `json:"user_cache_size,omitempty"`
	UserCacheTTLSec   int    `json:"user_cache_ttl_sec,omitempty"`
	EnableGroupEnrich *bool  `json:"enable_group_enrich,omitempty"`
	NameLocale        string `json:"name_locale,omitempty"`
}

type FeishuOutboundConfig struct {
	GlobalQPS            int  `json:"global_qps,omitempty"`
	PerChatQPS           int  `json:"per_chat_qps,omitempty"`
	MaxRetries           int  `json:"max_retries,omitempty"`
	EnableBinaryTransfer bool `json:"enable_binary_transfer,omitempty"`
}

type FeishuSecurityConfig struct {
	PermissionDegradeThreshold int `json:"permission_degrade_threshold,omitempty"`
}

type FeishuPushConfig struct {
	Enabled           bool `json:"enabled,omitempty"`
	PerChatPerMinute  int  `json:"per_chat_per_minute,omitempty"`
	IdempotencyTTLSec int  `json:"idempotency_ttl_sec,omitempty"`
}

// FeishuGovernanceConfig 控制 Phase 2A 的最小治理开关。
type FeishuGovernanceConfig struct {
	CommandACL        FeishuCommandACLConfig `json:"command_acl,omitempty"`
	ModelAllowlist    []string               `json:"model_allowlist,omitempty"`
	DebugEnabled      bool                   `json:"debug_enabled,omitempty"`
	MultiAgentEnabled bool                   `json:"multi_agent_enabled,omitempty"`
}

// FeishuCommandACLConfig 控制命令 ACL。
type FeishuCommandACLConfig struct {
	// ResetAllowlist 为 tenant_key -> open_id 列表。
	// 群聊 /reset 走该 allowlist；私聊仍允许。
	ResetAllowlist map[string][]string `json:"reset_allowlist,omitempty"`
}

// FeishuIngressMode 定义飞书事件摄取入口模式。
type FeishuIngressMode string

const (
	FeishuIngressModeWebhook  FeishuIngressMode = "webhook"
	FeishuIngressModeLongconn FeishuIngressMode = "longconn"
)

// feishuAllowedAckEmoji 是合法 ack 表情白名单（飞书 reactions API emoji_type，CamelCase）。
// "none" 是语义哨兵——Normalize 阶段保留字面量（不归一到空串，否则破坏幂等），
// renderer 侧同时识别 "" 与 "none" 作为 skip 条件，跳过 AddReaction 调用。
var feishuAllowedAckEmoji = map[string]struct{}{
	"Get":    {},
	"Typing": {},
	"none":   {},
}

// feishuLegacyAckEmojiMigration 把历史误写的全大写值透明升级到飞书 API 合法的 CamelCase。
// 背景：早期本仓库把 emoji_type 写成 "GET" / "KEYBOARD"，直接请求飞书 reactions API
// 会返回 code=231001 reaction type is invalid。DB 里可能已经存了旧值，Normalize 阶段
// 静默迁移，不 warn——老数据升级到新代码就会自动纠正，不打扰运维。
var feishuLegacyAckEmojiMigration = map[string]string{
	"GET":      "Get",
	"KEYBOARD": "Typing",
}

// Normalize 校验并归一化飞书配置：
//   - AckEmoji == "" → 归一到 "Get"（等价"默认"，不 warn）；
//   - AckEmoji ∈ {GET, KEYBOARD} → 静默迁移到 {Get, Typing}（老值兼容）；
//   - AckEmoji ∈ {Get, Typing, none} → 保留（renderer 端识别 "none" 跳过 AddReaction）；
//   - 其他值 → warn + 强制回退到 "Get"。
//   - Renderer.ThrottleMs <= 0 → 回退到 300。
//
// 幂等契约：对同一输入连续调用任意次，结果恒定。关键：不引入会互相转换的 sentinel
// （早期设计曾让 "none" → ""，但 "" 又归一到 "Get"，破坏幂等性并静默翻转"禁用"语义）。
// 两端调用：bootstrap DB 加载路径 + plugin.New 构造路径。
func (c *FeishuConfig) Normalize(warn func(msg string, original string, fallback string)) {
	if migrated, ok := feishuLegacyAckEmojiMigration[c.AckEmoji]; ok {
		c.AckEmoji = migrated
	}
	if c.AckEmoji == "" {
		c.AckEmoji = "Get"
	} else if _, ok := feishuAllowedAckEmoji[c.AckEmoji]; !ok {
		if warn != nil {
			warn("飞书 ack_emoji 配置值非法，已回退到 Get", c.AckEmoji, "Get")
		}
		c.AckEmoji = "Get"
	}
	if c.Renderer.ThrottleMs <= 0 {
		c.Renderer.ThrottleMs = 300
	}
}

// RendererEnabled 是 Renderer.Disabled 的反向视图，方便调用方只读判断是否启用 EventRenderer，
// 避免外部代码到处写 `!cfg.Renderer.Disabled` 的双重否定。
func (c FeishuConfig) RendererEnabled() bool {
	return !c.Renderer.Disabled
}

// ResolvedIngressMode 返回飞书最终生效的入口模式。
// 优先级：
//   - 显式 ingress_mode 优先
//   - 否则 longconn_enabled=true → longconn
//   - 否则默认 webhook
func (c FeishuConfig) ResolvedIngressMode() FeishuIngressMode {
	if c.IngressMode != "" {
		return c.IngressMode
	}
	if c.LongconnEnabledResolved() {
		return FeishuIngressModeLongconn
	}
	return FeishuIngressModeWebhook
}

// LongconnEnabledResolved 返回 longconn 是否显式启用。
// 优先读取 Phase 2B 的 reliability.longconn_enabled，再回退 legacy 字段。
func (c FeishuConfig) LongconnEnabledResolved() bool {
	if c.Reliability.LongconnEnabled {
		return true
	}
	return c.LongconnEnabled
}

// GapFetchEnabledResolved 返回当前是否启用 gap fetch。
func (c FeishuConfig) GapFetchEnabledResolved() bool {
	return c.Reliability.LongconnGapFetchEnabled
}

// HeartbeatStaleWindowResolved 返回 watchdog 陈旧窗口，默认 60s。
func (c FeishuConfig) HeartbeatStaleWindowResolved() time.Duration {
	if c.Reliability.HeartbeatStaleWindow > 0 {
		return c.Reliability.HeartbeatStaleWindow
	}
	return 60 * time.Second
}

// GapFetchMaxWindowResolved 返回 gap fetch 允许的最大追补窗口，默认 10m。
func (c FeishuConfig) GapFetchMaxWindowResolved() time.Duration {
	if c.Reliability.GapFetchMaxWindow > 0 {
		return c.Reliability.GapFetchMaxWindow
	}
	return 10 * time.Minute
}

func (c FeishuConfig) IdentityUserCacheSizeResolved() int {
	if c.Identity.UserCacheSize > 0 {
		return c.Identity.UserCacheSize
	}
	return 5000
}

func (c FeishuConfig) IdentityUserCacheTTLResolved() time.Duration {
	if c.Identity.UserCacheTTLSec > 0 {
		return time.Duration(c.Identity.UserCacheTTLSec) * time.Second
	}
	return 12 * time.Hour
}

func (c FeishuConfig) GroupEnrichEnabledResolved() bool {
	if c.Identity.EnableGroupEnrich == nil {
		return true
	}
	return *c.Identity.EnableGroupEnrich
}

func (c FeishuConfig) IdentityNameLocaleResolved() string {
	if c.Identity.NameLocale != "" {
		return c.Identity.NameLocale
	}
	switch c.Region {
	case "intl", "lark", "international":
		return "en-US"
	default:
		return "zh-CN"
	}
}

func (c FeishuConfig) OutboundGlobalQPSResolved() int {
	if c.Outbound.GlobalQPS > 0 {
		return c.Outbound.GlobalQPS
	}
	return 45
}

func (c FeishuConfig) OutboundPerChatQPSResolved() int {
	if c.Outbound.PerChatQPS > 0 {
		return c.Outbound.PerChatQPS
	}
	return 8
}

func (c FeishuConfig) OutboundMaxRetriesResolved() int {
	if c.Outbound.MaxRetries > 0 {
		return c.Outbound.MaxRetries
	}
	return 3
}

func (c FeishuConfig) BinaryTransferEnabledResolved() bool {
	return c.Outbound.EnableBinaryTransfer
}

func (c FeishuConfig) PermissionDegradeThresholdResolved() int {
	if c.Security.PermissionDegradeThreshold > 0 {
		return c.Security.PermissionDegradeThreshold
	}
	return 5
}

func (c FeishuConfig) PushPerChatPerMinuteResolved() int {
	if c.Push.PerChatPerMinute > 0 {
		return c.Push.PerChatPerMinute
	}
	return 10
}

func (c FeishuConfig) PushIdempotencyTTLResolved() time.Duration {
	if c.Push.IdempotencyTTLSec > 0 {
		return time.Duration(c.Push.IdempotencyTTLSec) * time.Second
	}
	return 5 * time.Minute
}

// InboundContextResolverEnabled 返回 Phase 1 ContextResolver 是否启用。
// nil 视为 true，显式 false 才关闭。
func (c FeishuConfig) InboundContextResolverEnabled() bool {
	return c.Inbound.ContextResolverEnabled()
}

// ContextResolverEnabled 返回入站 ContextResolver 是否启用。
func (c FeishuInboundConfig) ContextResolverEnabled() bool {
	if c.EnableContextResolver == nil {
		return true
	}
	return *c.EnableContextResolver
}

// Validate 校验飞书配置。Phase 0 P0-#14 唯一规则：
//
//	LongconnEnabled && WebhookURL != "" → fatal "dual ingress"
//
// 红队链 B：webhook + longconn 同进程并存 → 飞书事件双投 → DB dedup 失效时
// 单消息触发两次 renderer → 用户看到两条相同回复。生产 ingress 必须 XOR。
//
// Validate **只**检查这一条规则；其余字段（AckEmoji / Renderer 等）由 Normalize 负责。
// 故意不写在 Normalize 里：Normalize 是幂等"修正"，Validate 是 "拒绝启动"——
// 双 ingress 没有合理修正策略，必须人为决策选哪一边。
func (c FeishuConfig) Validate() error {
	if c.IngressMode != "" &&
		c.IngressMode != FeishuIngressModeWebhook &&
		c.IngressMode != FeishuIngressModeLongconn {
		return errs.New(errs.CodeConfigInvalid,
			fmt.Sprintf("feishu: ingress_mode must be %q or %q, got %q",
				FeishuIngressModeWebhook, FeishuIngressModeLongconn, c.IngressMode))
	}

	if c.LongconnEnabledResolved() && c.WebhookURL != "" {
		return errs.New(errs.CodeConfigInvalid,
			"feishu: dual ingress detected — longconn_enabled=true 与 webhook_url 同时配置，"+
				"生产环境必须二选一（红队链 B：双投 → 双回复）")
	}
	if c.Reliability.HeartbeatStaleWindow < 0 {
		return errs.New(errs.CodeConfigInvalid, "feishu: reliability.heartbeat_stale_window must be non-negative")
	}
	if c.Reliability.GapFetchMaxWindow < 0 {
		return errs.New(errs.CodeConfigInvalid, "feishu: reliability.gap_fetch_max_window must be non-negative")
	}
	if c.Security.PermissionDegradeThreshold < 0 {
		return errs.New(errs.CodeConfigInvalid, "feishu: security.permission_degrade_threshold must be non-negative")
	}
	return nil
}

// WeComConfig 企业微信配置
type WeComConfig struct {
	Enabled        bool   `json:"enabled"`
	CorpID         string `json:"corp_id"`
	AgentID        int    `json:"agent_id"`
	Secret         string `json:"secret"`
	Token          string `json:"token"`
	EncodingAESKey string `json:"encoding_aes_key"`
}

// GatewayConfig 配置 RPC 网关
type GatewayConfig struct {
	Enabled bool `json:"enabled"`
	// Tokens 预设认证 token 列表。预留字段，当前版本未启用鉴权逻辑，配置后暂无效果。
	Tokens []string `json:"tokens,omitempty"`
}

// SecurityConfig 配置 OS 安全执行。
type SecurityConfig struct {
	Enabled       bool             `json:"enabled"`
	DefaultPolicy string           `json:"default_policy,omitempty"` // 未匹配规则时的默认策略: "allow"(默认) | "ask" | "deny"
	ExecRules     []ExecRuleConfig `json:"exec_rules,omitempty"`
	WatchEnvVars  []string         `json:"watch_env_vars,omitempty"` // 需要监控的环境变量
	// PermissionMode 控制 createPermissionPromptFn 行为：
	//   "minimal"（默认）→ 非 shell 工具放行、shell 工具 SafeExecutor-first；
	//   "strict"         → 任何工具都走 HITL 审批，等价 pre-v2 行为（一键回滚路径）。
	PermissionMode string `json:"permission_mode,omitempty"`
	// DestructivePatterns 用户自定义的追加破坏性规则，直接 append 到 BuiltinDangerousRules 之后。
	// 不覆盖 builtin；空切片时仅使用 builtin。
	DestructivePatterns []ExecRuleConfig `json:"destructive_patterns,omitempty"`
}

// ExecRuleConfig 命令执行规则配置（纯类型，避免 import cycle）
type ExecRuleConfig struct {
	Pattern     string `json:"pattern"`
	Policy      string `json:"policy"` // "allow", "ask", "deny"
	Description string `json:"description"`
}

// ToolsConfig 自定义工具安全配置
type ToolsConfig struct {
	CreateRequiresApproval bool     `json:"create_requires_approval"`  // create_tool 是否需要 HITL 审批
	AllowedDomains         []string `json:"allowed_domains,omitempty"` // HTTP 工具全局域名白名单
}

// SandboxConfig 配置命令沙箱（可插拔执行器）
type SandboxConfig struct {
	Enabled bool                `json:"enabled"`          // 主开关
	Type    string              `json:"type"`             // "local" | "docker"
	Docker  DockerSandboxConfig `json:"docker,omitempty"` // Docker 沙箱配置
}

// DockerSandboxConfig Docker 沙箱的详细配置
type DockerSandboxConfig struct {
	Image       string `json:"image,omitempty"`        // 镜像名称，默认 "hive-sandbox:latest"
	CPULimit    string `json:"cpu_limit,omitempty"`    // CPU 限制，默认 "1.0"
	MemoryLimit string `json:"memory_limit,omitempty"` // 内存限制，默认 "512m"
	PidsLimit   int    `json:"pids_limit,omitempty"`   // PID 限制，默认 100
	TmpfsSize   string `json:"tmpfs_size,omitempty"`   // tmpfs 大小，默认 "256m"
	Network     string `json:"network,omitempty"`      // 网络模式，默认 "bridge"

	// 安全加固选项（域E Phase 2）
	NetworkDisabled bool   `json:"network_disabled,omitempty"`  // true → --network=none 完全断网
	SeccompProfile  string `json:"seccomp_profile,omitempty"`   // seccomp profile 路径；空 = Docker 默认
	ReadOnlyWorkDir bool   `json:"read_only_workdir,omitempty"` // true → workDir bind mount 只读
}

// ControlPlaneConfig 配置内部控制平面（多会话管理）
type ControlPlaneConfig struct {
	Enabled      bool    `json:"enabled"`
	MaxSessions  int     `json:"max_sessions"`
	RateLimit    float64 `json:"rate_limit"`
	RateBurst    int     `json:"rate_burst"`
	BindingsFile string  `json:"bindings_file"`
}

// ACPServerConfig 配置 ACP 协议服务器（IDE 零配置接入）
type ACPServerConfig struct {
	Enabled     bool   `json:"enabled"`
	AuthMethod  string `json:"auth_method,omitempty"` // "none"（默认）| "token"
	AuthToken   string `json:"auth_token,omitempty"`  // token 认证时的密钥
	MaxSessions int    `json:"max_sessions,omitempty"`
}

// WebUIConfig 配置前端控制台
type WebUIConfig struct {
	Enabled bool `json:"enabled" yaml:"enabled"`
}

// PluginConfig 配置插件运行时
type PluginConfig struct {
	Enabled bool   `json:"enabled"`
	Dir     string `json:"dir"`
	// AutoDiscover 自动发现插件目录下的插件。预留字段，当前版本未启用，配置后暂无效果。
	AutoDiscover bool `json:"auto_discover"`
	// Plugins 显式指定要加载的插件列表。预留字段，当前版本未启用，配置后暂无效果。
	Plugins []string `json:"plugins,omitempty"`
}

// CompactStrategy 定义上下文压缩策略
type CompactStrategy string

const (
	// StrategyTruncate 简单截断策略（保留最近消息）
	StrategyTruncate CompactStrategy = "truncate"
	// StrategyLLMSummary LLM 驱动的智能摘要策略（默认）
	StrategyLLMSummary CompactStrategy = "llm_summary"
)

// CompactionConfig 配置上下文压缩行为
type CompactionConfig struct {
	Enabled        bool            `json:"enabled"`                   // 启用压缩（默认 true）
	Strategy       CompactStrategy `json:"strategy"`                  // 压缩策略（默认 llm_summary）
	MaxTokens      int             `json:"max_tokens"`                // 保留的最大 token 数（默认 8000）
	ReserveTokens  int             `json:"reserve_tokens"`            // 保留空间用于响应（默认 1000）
	CompactTimeout time.Duration   `json:"compact_timeout,omitempty"` // LLM 摘要超时（默认 2s）
	UseTiktoken    bool            `json:"use_tiktoken"`              // 使用 tiktoken 精确计数（默认 true）
	LazyMode       bool            `json:"lazy_mode"`                 // 启用懒惰压缩模式（默认 true）
	LazyThreshold  int             `json:"lazy_threshold"`            // 懒惰模式触发阈值（token 数，默认 10000）

	// 可插拔压缩管线配置（P2-2）
	PipelineStages      []string `json:"pipeline_stages,omitempty"`        // 管线阶段名称列表，如 ["tool_budget", "session_memory", "history_snip", "llm_summary", "truncate"]
	ToolOutputMaxTokens int      `json:"tool_output_max_tokens,omitempty"` // 工具输出截断阈值（字节），默认 20KB
}

// LSPConfig 配置 LSP 工具集
type LSPConfig struct {
	Enabled        bool                    `json:"enabled" yaml:"enabled"`
	Timeout        time.Duration           `json:"timeout" yaml:"timeout"`
	MaxServers     int                     `json:"max_servers" yaml:"max_servers"`
	HealthInterval time.Duration           `json:"health_interval" yaml:"health_interval"`
	Languages      map[string]LanguageSpec `json:"languages" yaml:"languages"`
}

// LanguageSpec 语言服务器配置
type LanguageSpec struct {
	Command    string   `json:"command" yaml:"command"`
	Args       []string `json:"args,omitempty" yaml:"args,omitempty"`
	Extensions []string `json:"extensions" yaml:"extensions"`
	Disabled   bool     `json:"disabled,omitempty" yaml:"disabled,omitempty"`
}

// AuthConfig 认证系统配置
type AuthConfig struct {
	Enabled     bool               `json:"enabled"`                // 主开关，默认 false
	JWTSecret   string             `json:"jwt_secret,omitempty"`   // 空=自动生成存 DB
	JWTTTL      string             `json:"jwt_ttl,omitempty"`      // 默认 "24h"
	JWTMaxTTL   string             `json:"jwt_max_ttl,omitempty"`  // refresh 绝对上限，默认 "168h"（7天）
	FrontendURL string             `json:"frontend_url,omitempty"` // 开发环境: http://localhost:3000，生产: 实际域名；空=相对路径
	Providers   []AuthProviderSeed `json:"providers,omitempty"`    // 启动时自动 seed 到 DB 的 provider 列表
}

// AuthProviderSeed 配置文件中的 provider 种子数据，启动时写入 auth_providers 表（已存在则跳过）
type AuthProviderSeed struct {
	Name         string         `json:"name"`
	ProviderType string         `json:"provider_type"` // feishu / dingtalk / ldap
	Enabled      bool           `json:"enabled"`
	Config       map[string]any `json:"config"` // app_id, app_secret, redirect_url 等
}
