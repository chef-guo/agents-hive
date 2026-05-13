package master

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"go.uber.org/zap"

	"github.com/chef-guo/agents-hive/internal/a2abridge"
	"github.com/chef-guo/agents-hive/internal/accounting"
	"github.com/chef-guo/agents-hive/internal/airouter"
	"github.com/chef-guo/agents-hive/internal/auth"
	"github.com/chef-guo/agents-hive/internal/config"
	"github.com/chef-guo/agents-hive/internal/errs"
	"github.com/chef-guo/agents-hive/internal/i18n"
	"github.com/chef-guo/agents-hive/internal/journal"
	"github.com/chef-guo/agents-hive/internal/llm"
	"github.com/chef-guo/agents-hive/internal/mcphost"
	"github.com/chef-guo/agents-hive/internal/memory"
	"github.com/chef-guo/agents-hive/internal/observability"
	"github.com/chef-guo/agents-hive/internal/plugin"
	"github.com/chef-guo/agents-hive/internal/runtimepolicy"
	"github.com/chef-guo/agents-hive/internal/security"
	"github.com/chef-guo/agents-hive/internal/sessiontodo"
	"github.com/chef-guo/agents-hive/internal/skills"
	"github.com/chef-guo/agents-hive/internal/specdriven/ingress"
	"github.com/chef-guo/agents-hive/internal/store"
	"github.com/chef-guo/agents-hive/internal/subagent"
	"github.com/chef-guo/agents-hive/internal/toolctx"
	"github.com/chef-guo/agents-hive/internal/tools"
	"github.com/chef-guo/agents-hive/internal/trajectory"
)

// SkillRegistryProvider 是 Master 需要的 skill 注册表接口。
// *skills.Registry 和 *skills.OverlayRegistry 均满足此接口。
// 变参 userID：不传 = public；传 = personal + public 合并（personal 优先）。
type SkillRegistryProvider interface {
	ListForModel(userID ...string) []skills.SkillMetadata
	GetToolBridge() *skills.ToolBridge
	SetForkHandler(handler skills.ForkHandler)
}

// Config 配置 master agent
type Config struct {
	MaxConcurrentTasks          int // Worker Pool 大小，<=0 时默认 4（SessionLoop 并发处理）
	MaxConcurrentAgents         int // 并行执行步骤的最大并发数（<=0 时默认 10）
	Model                       string
	BaseURL                     string
	APIKey                      string
	DisableJSONMode             bool
	Provider                    string
	ReasoningEffort             string                     // 统一推理努力级别: "low"/"medium"/"high"/"max"
	PromptLanguage              string                     // LLM 提示词语言
	ShellTimeout                time.Duration              // Shell 命令超时
	ScriptTimeout               time.Duration              // 脚本执行超时
	SyncInterval                time.Duration              // 后台同步间隔
	ContextCompression          config.CompactionConfig    // 上下文压缩配置
	SecurityRules               []config.ExecRuleConfig    // 安全执行规则
	SecurityDefaultPolicy       string                     // 未匹配规则时的默认策略: "allow"(默认) | "ask" | "deny"
	SecurityPermissionMode      string                     // "minimal"（默认）| "strict"（一键回滚路径）
	SecurityDestructivePatterns []config.ExecRuleConfig    // 追加到 BuiltinDangerousRules 之后的用户规则
	PluginMgr                   *plugin.Manager            // 插件管理器（可选）
	InstructionURLs             []string                   // 远程指令文件 URL 列表
	StorePrivacy                bool                       // 隐私保护：为 OpenAI/Copilot 设置 store=false
	PromptCacheKey              bool                       // 是否设置 prompt_cache_key
	ServiceTier                 string                     // 交互式请求 service_tier
	APIFormat                   string                     // API 格式: "chat" 或 "responses"，默认 "chat"
	Router                      *airouter.Router           // AI 服务路由器（可选，设置后替代直接 llmClient）
	ToolPolicy                  config.ToolPolicyConfig    // 工具过滤策略配置
	ToolRecall                  config.ToolRecallConfig    // 每轮隐藏工具召回配置
	FirstToken                  config.FirstTokenConfig    // 首 token 快路径配置
	ActionGuardEnabled          bool                       // ActionGuard 防护开关
	MaxSessionCost              float64                    // P0-3: per-session 成本预算上限（USD），<=0 表示不限制（需要 PostgreSQL 成本追踪启用）
	SpecDriven                  config.SpecDrivenConfig    // Spec-driven Phase 2 总开关（默认 mode=legacy，零成本短路 session_loop intake hook）
	PlanRuntime                 config.PlanRuntimeConfig   // session 级 plan/todos runtime 配置
	QualityGuards               config.QualityGuardsConfig // P0 质量护栏灰度开关（见 docs/计划与路线/Agent-质量护栏治理计划.md）
	Reflection                  config.ReflectionConfig    // 运行时反思与 shadow 评估配置
	ReasoningEffortAuto         config.ReasoningEffortAutoConfig
	Observability               config.ObservabilityConfig
	RuntimePolicy               runtimepolicy.Policy // 运行期 timeout、容量和成本策略
}

// BroadcastMessage 是 WebSocket 广播消息
type BroadcastMessage struct {
	Type      string      `json:"type"`                 // "input_request", "message", "event", "tool_call", "agent_start", "skill_exec"
	Payload   interface{} `json:"payload"`              // 消息载荷
	SessionID string      `json:"session_id,omitempty"` // 关联的会话 ID（可选）
}

// 事件类型常量
const (
	EventTypeInputRequest    = "input_request"     // HITL 输入请求
	EventTypeInputResponse   = "input_response"    // HITL 输入响应（关键事件，供 EmitInputRequest 订阅）
	EventTypeMessage         = "message"           // 通用消息
	EventTypeEvent           = "event"             // 通用事件
	EventTypeToolCall        = "tool_call"         // 工具调用
	EventTypeAgentStart      = "agent_start"       // Agent 启动
	EventTypeSkillExec       = "skill_exec"        // Skill 执行
	EventTypeError           = "error"             // 错误消息
	EventTypeToolListChanged = "tool_list_changed" // 工具列表变更
	EventTypeAgentStatus     = "agent_status"      // Agent 状态变更（thinking/completed/error）
	EventTypeInputReceived   = "input_received"    // 用户输入已抵达 master（早于 LLM 调用），供 renderer 做 ack
	EventTypeTodoSnapshot    = "todo_snapshot"     // 当前 session todo 完整快照（非关键事件，可由 API 恢复）
	EventTypePlanModeChanged = "plan_mode_changed" // plan mode / plan status 变化（关键事件）
	// EventTypeSpecContinuationAmbiguous 是 spec-driven Phase 2 Guard 1 的 UI 事件：
	// continuation.Resolve 返回 DecisionAsk 时广播，前端据此弹出候选 change 让用户确认。
	// payload 为 SpecContinuationAmbiguousEvent——含 AskReason + Trigger + Candidates。
	EventTypeSpecContinuationAmbiguous = "spec_continuation_ambiguous"
)

// InputReceivedEvent 是 EventTypeInputReceived 的 Payload，
// 在 master 收到用户消息、开始处理（进入 LLM 前）时广播。
// renderer 消费此事件做表情回执（飞书 GET/KEYBOARD）或 UI "已收到" 态切换。
type InputReceivedEvent struct {
	SessionID        string `json:"session_id"`
	ChannelMessageID string `json:"channel_message_id,omitempty"` // 平台原消息 ID，供 ack 表情；非 IM 通道为空
}

// 并行任务与 Agent 进度事件类型
const (
	EventTypeTaskGroup     = "task_group"     // 并行任务组生命周期
	EventTypeTaskProgress  = "task_progress"  // 单个任务内部进度
	EventTypeAgentProgress = "agent_progress" // SubAgent 工具调用级进度
)

// 动态 Agent 事件类型
const (
	EventTypeAgentCreated   = "agent_created"   // 动态 Agent 已创建
	EventTypeAgentDestroyed = "agent_destroyed" // 动态 Agent 已销毁
)

// TaskGroupEvent 并行任务组事件
type TaskGroupEvent struct {
	GroupID   string      `json:"group_id"`
	Status    string      `json:"status"` // "started", "completed", "failed"
	Total     int         `json:"total"`
	Completed int         `json:"completed"`
	Tasks     []TaskBrief `json:"tasks,omitempty"`
	Results   interface{} `json:"results,omitempty"` // 完成时附带结果摘要
}

// TaskBrief 任务摘要（用于事件广播）
type TaskBrief struct {
	ID          string `json:"id"`
	AgentID     string `json:"agent_id"`
	Instruction string `json:"instruction"`
	Status      string `json:"status"` // "pending", "running", "completed", "failed"
	Error       string `json:"error,omitempty"`
}

// AgentProgressEvent SubAgent 工具调用级进度事件
type AgentProgressEvent struct {
	StepID    string `json:"step_id,omitempty"` // Plan 模式下的 Step ID
	TaskID    string `json:"task_id,omitempty"` // 并行派发模式下的 Task ID
	AgentID   string `json:"agent_id"`
	Turn      int    `json:"turn"`
	MaxTurns  int    `json:"max_turns"`
	ToolName  string `json:"tool_name,omitempty"`
	Status    string `json:"status"` // "tool_start", "tool_done", "turn_done"
	Error     string `json:"error,omitempty"`
	SessionID string `json:"session_id,omitempty"`
}

// ToolCallEvent 工具调用事件
type ToolCallEvent struct {
	ToolCallID           string `json:"tool_call_id"` // 对应 LLM 工具调用 ID
	ToolName             string `json:"tool_name"`
	TurnID               string `json:"turn_id,omitempty"`
	Status               string `json:"status"`             // "start", "success", "error"
	Duration             int64  `json:"duration,omitempty"` // 执行耗时（毫秒）
	Error                string `json:"error,omitempty"`    // 错误信息（仅 error 状态）
	FailureType          string `json:"failure_type,omitempty"`
	RequiresUserApproval bool   `json:"requires_user_approval,omitempty"`
	SuggestedAction      string `json:"suggested_action,omitempty"`
	SessionID            string `json:"session_id,omitempty"` // 关联会话 ID，用于前端过滤
}

// AgentStartEvent Agent 启动事件
type AgentStartEvent struct {
	AgentName string `json:"agent_name"`
	TaskDesc  string `json:"task_desc,omitempty"`
}

// SkillExecEvent Skill 执行事件
type SkillExecEvent struct {
	SkillName string `json:"skill_name"`
	Args      string `json:"args,omitempty"`
}

// Master 是实现 Session Loop + ReAct Tool-Use 循环的中央协调器
type Master struct {
	config       Config
	hitlConfig   config.HITLConfig
	llmClient    *llm.Client      // 主 LLM 客户端（无 Router 时的 fallback）
	router       *airouter.Router // AI 服务路由器（优先于 llmClient）
	llmMu        sync.RWMutex     // 保护 llmClient 和 config.Model 的并发读写
	forkExecutor *ForkExecutor    // context:fork 模式的 skill 隔离执行
	transport    *a2abridge.InProcessTransport
	registry     *subagent.Registry
	skillReg     SkillRegistryProvider
	permMgr      *skills.PermissionManager
	pluginMgr    *plugin.Manager        // 插件管理器（可选）
	agentFactory *subagent.AgentFactory // 动态 Agent 工厂

	// 会话级权限路由：ACP 多会话场景下，每个会话有独立的权限提示函数
	sessionPermFns   map[string]func(context.Context, skills.PermissionRequest) (skills.PermissionResponse, error)
	sessionPermFnsMu sync.RWMutex
	logger           *zap.Logger

	// 提示词上下文（指令、Agent 定义、PromptManager）
	promptCtx *PromptContext

	// LLM Client Pool（用于多模型支持，无 Router 时使用）
	llmPool *llm.ClientPool

	// 持久化
	store store.SessionStore

	// spec-driven intake path runner（Sprint 3.3.a：替换 ErrSpecRunnerNotImplemented 桩）。
	// 可选字段——nil 时 applySpecDrivenIntake 等价于旧 stub 路径 fail-closed 降级 legacy。
	// 生产路径由 bootstrap/server.go 注入 *ingress.MinimalRunner（持有 *airouter.Router）。
	specRunner ingress.Runner

	// spec-driven change store（Sprint 3.3.b：把 Sprint 2.3 的 CASConflictObserver
	// 基础设施接到 master 的 metric 队列）。可选字段——nil 时 wireSpecChangeStoreMetrics
	// no-op（内存 / pg 不可用的启动路径）。
	specStore *store.SpecChangeStore

	// 提取的子组件
	sessionMgr *SessionManager // 会话管理
	hitlBroker *HITLBroker     // 人机交互
	eventBus   *EventBus       // 事件广播

	mcpHost       *mcphost.Host                // MCP 工具宿主（可选，用于监听工具变更）
	mcpServerEnvs map[string]map[string]string // MCP 服务端环境变量（serverName → envKey → envValue）

	toolPolicy   *skills.ToolPolicy // 工具策略引擎（可选，用于 Profile/Group 过滤）
	toolBridge   *skills.ToolBridge // 工具桥接（复用 SubAgent 的工具执行路径）
	masterFilter *skills.ToolFilter // Master 的工具过滤器（启动时由 toolPolicy 构建）

	memoryInjector    *memory.Injector            // 记忆注入器（可选，用于将相关记忆注入 LLM 上下文）
	feedbackExtractor memory.FeedbackExtractor    // feedback 记忆提取器（可选，用于质量反馈闭环）
	costTracker       accounting.CostTracker      // 成本追踪器（可选，nil 时不记录）
	asyncRecorder     *accounting.AsyncRecorder   // 异步写入包装器（channel+worker，shutdown 安全）
	authEngine        *auth.Engine                // 认证引擎（可选，nil 时不检查配额）
	journal           journal.Journal             // 开发日志（可选，nil 时不记录）
	tracer            observability.Tracer        // 可观测性 Tracer（可选，nil 时不记录）
	metricsWriter     observability.MetricsWriter // 可观测性 MetricsWriter（可选，nil 时不记录）
	logWriter         observability.LogWriter     // 可观测性 LogWriter（可选，nil 时不记录）
	trajectoryStore   trajectory.Store            // 诊断级 step snapshot 存储（可选）
	validationExec    ValidationExecutor          // test-driven shadow 验证执行器（可选）
	reflectionEval    ReflectionEvaluator         // evaluator shadow 评估器（可选）
	obsCh             chan observabilityEntry     // 异步 observability 写入队列
	obsDone           chan struct{}               // observability worker 退出信号
	spansDropped      atomic.Int64
	metricsDropped    atomic.Int64
	logsDropped       atomic.Int64
	journalCh         chan journalEntry // 异步 journal 写入队列（支持 tool call / file change / decision）
	journalDone       chan struct{}     // journal worker 退出信号

	stopOnce  sync.Once
	closeOnce sync.Once // 保护 channel 关闭
	stopCh    chan struct{}

	cronMu                    sync.Mutex
	cronJobs                  map[string]*cronJobState
	scheduledPromptDispatcher func(context.Context, string) error
	scheduledTaskUserResolver scheduledTaskUserResolver
	scheduledTaskPushService  scheduledTaskPushService

	// 当前安全默认策略（热更新时同步，由 llmMu 保护）
	currentDefaultPolicy string

	// 当前活跃的安全执行器，createPermissionPromptFn 直接调 MatchPolicy。
	// 用 atomic.Pointer 支持热重载原子替换，零锁读。nil 时表示 Master 未完成安全初始化。
	safeExecutor atomic.Pointer[security.SafeExecutor]
	spanCounts   sync.Map // map[sessionID]*atomic.Int64

	// 可配置的后台同步间隔
	syncInterval time.Duration

	// 压缩统计跟踪器
	compactionTracker *CompactionTracker

	// 外部资源缓存
	externalResources []*store.ExternalResourceRecord
	resourcesMu       sync.RWMutex

	// per-session 任务取消函数（用于支持用户主动停止指定会话的任务）
	taskCancels sync.Map // map[sessionID]context.CancelFunc

	// per-session 信号量：确保同一 session 同一时间只有一个任务在执行
	// key=sessionID, value=chan struct{}(cap=1)
	sessionSems sync.Map

	// HITL 批准时用户指定的工具参数覆盖（key=toolName, value=json.RawMessage）
	// createPermissionPromptFn 写入，executeTool 读出并清除（LoadAndDelete）
	toolArgOverrides sync.Map

	// Prompt 外部化加载器（可选，nil 时 buildSystemPrompt 使用硬编码默认值）
	promptLoader *i18n.PromptLoader

	// EnableStreamingExecutor 启用并发工具执行（默认 false，向后兼容）
	// safe 工具（IsConcurrencySafe=true）并发执行，unsafe 工具串行
	EnableStreamingExecutor bool

	middlewarePipeline MiddlewarePipeline

	sessionTodoStore sessiontodo.Store
	runtimeEpoch     string
	autoContinueMu   sync.Mutex
	autoContinueRuns map[string]int
}

// NewMaster 创建一个新的 Master agent
func NewMaster(cfg Config, hitlCfg config.HITLConfig, registry *subagent.Registry, skillReg SkillRegistryProvider, st store.SessionStore, logger *zap.Logger) *Master {
	cfg.RuntimePolicy = cfg.RuntimePolicy.WithDefaults()
	if err := cfg.RuntimePolicy.Validate(); err != nil {
		logger.Warn("runtime policy 非法，使用默认值", zap.Error(err))
		cfg.RuntimePolicy = runtimepolicy.Default()
	}

	// 创建 PromptManager
	promptLang := cfg.PromptLanguage
	if promptLang == "" {
		promptLang = config.DefaultPromptLanguage
	}
	promptMgr := i18n.NewPromptManager(promptLang)

	// 推断 provider 用于区分提示词模板
	providerKey := i18n.ProviderKey(llm.DetectProvider(cfg.BaseURL))
	if cfg.Provider != "" {
		providerKey = i18n.ProviderKey(cfg.Provider)
	}

	transport := a2abridge.NewInProcessTransport(logger)

	// 创建主 LLM Client（如果未提供 Router）
	var llmClient *llm.Client
	if cfg.Router == nil && cfg.APIKey != "" {
		provDef := llm.LookupProvider(cfg.Provider)
		provDef.APIFormat = cfg.APIFormat
		llmClient = llm.NewClient(llm.ClientConfig{
			APIKey:          cfg.APIKey,
			BaseURL:         cfg.BaseURL,
			Model:           cfg.Model,
			DisableJSONMode: cfg.DisableJSONMode,
			Provider:        provDef,
			ReasoningEffort: cfg.ReasoningEffort,
			StorePrivacy:    cfg.StorePrivacy,
			PromptCacheKey:  cfg.PromptCacheKey,
			ServiceTier:     cfg.ServiceTier,
		}, logger)
	}

	stopCh := make(chan struct{})

	// 创建子组件
	eventBus := NewEventBus(logger)
	hitlBroker := NewHITLBroker(hitlCfg, eventBus, stopCh, logger)
	sessionMgr := NewSessionManager(stopCh, logger)

	m := &Master{
		config:             cfg,
		hitlConfig:         hitlCfg,
		llmClient:          llmClient,
		router:             cfg.Router,
		forkExecutor:       nil, // 在 agentFactory 初始化后设置
		transport:          transport,
		registry:           registry,
		skillReg:           skillReg,
		pluginMgr:          cfg.PluginMgr,
		logger:             logger,
		llmPool:            llm.NewClientPool(logger),
		store:              st,
		sessionMgr:         sessionMgr,
		hitlBroker:         hitlBroker,
		eventBus:           eventBus,
		stopCh:             stopCh,
		syncInterval:       cfg.SyncInterval,
		promptCtx:          NewPromptContext(promptMgr, providerKey),
		compactionTracker:  NewCompactionTracker(),
		cronJobs:           make(map[string]*cronJobState),
		middlewarePipeline: buildMiddlewarePipeline(cfg.QualityGuards),
		runtimeEpoch:       observability.NewTraceID(),
		autoContinueRuns:   make(map[string]int),
	}

	// 加载自定义 Agent 定义和指令文件
	workDir, err := os.Getwd()
	if err != nil {
		logger.Warn("获取工作目录失败，跳过加载自定义配置", zap.Error(err))
	} else {
		// 加载 .claw/agents/ 目录下的自定义 Agent 定义
		agentDefs, loadErr := config.LoadAgentDefinitions(filepath.Join(workDir, ".claw", "agents"), logger)
		if loadErr != nil {
			logger.Warn("加载自定义 Agent 定义失败", zap.Error(loadErr))
		} else if len(agentDefs) > 0 {
			m.promptCtx.SetAgentDefs(agentDefs)
			logger.Info("已加载自定义 Agent 定义", zap.Int("count", len(agentDefs)))
		}

		// 加载指令文件（.claw/AGENTS.md 或 CLAUDE.md，支持远程 URL 追加）
		instructions := config.LoadInstructionsWithRemote(workDir, cfg.InstructionURLs, logger)
		if instructions != "" {
			m.promptCtx.SetInstructions(instructions)
			logger.Info("已加载指令文件", zap.Int("size", len(instructions)))
		}
	}

	// 初始化安全执行系统（内置危险规则始终生效，用户配置规则追加）
	{
		var userRules []security.ExecRule
		for _, rule := range cfg.SecurityRules {
			userRules = append(userRules, security.ExecRule{
				Pattern:     rule.Pattern,
				Policy:      security.ExecPolicy(rule.Policy),
				Description: rule.Description,
			})
		}
		// 追加 SecurityDestructivePatterns（不覆盖 builtin，appended 到 rules 之后）
		for _, rule := range cfg.SecurityDestructivePatterns {
			userRules = append(userRules, security.ExecRule{
				Pattern:     rule.Pattern,
				Policy:      security.ExecPolicy(rule.Policy),
				Description: rule.Description,
			})
		}
		defaultPolicy := security.ExecPolicy(cfg.SecurityDefaultPolicy)
		safeExec := security.NewSafeExecutorWithDefault(userRules, defaultPolicy, logger)
		checker := safeExecAdapter{executor: safeExec}
		tools.SetSafeExecutor(checker)
		// 将安全检查器注入到 SafeExecutorWrapper（executor 主路径也经过安全检查）
		tools.SetExecutorChecker(checker)
		// 提升为 Master 字段：createPermissionPromptFn 走 MatchPolicy-first 路径需要
		m.safeExecutor.Store(safeExec)
		logger.Info("安全执行系统已初始化",
			zap.Int("builtin_rules", len(security.BuiltinDangerousRules)),
			zap.Int("user_rules", len(userRules)),
		)
	}

	if hitlCfg.Enabled {
		m.permMgr = skills.NewPermissionManager(hitlCfg.PermissionRules, m.createPermissionPromptFn())
	} else {
		m.permMgr = skills.NewPermissionManager(hitlCfg.PermissionRules, nil)
	}

	// 设置插件管理器到权限管理器（用于 PermissionAsk hook）
	if m.pluginMgr != nil && m.permMgr != nil {
		m.permMgr.SetPluginManager(m.pluginMgr)
	}

	// 域E Phase 2：AutoClassify=true 时注入 LLM 分类器，在 HITL 前做语义安全判断
	if hitlCfg.AutoClassify && m.permMgr != nil && llmClient != nil {
		classifier := security.NewLLMClassifier(llmClient, logger)
		m.permMgr.SetLLMClassifier(classifier)
		logger.Info("LLM 权限分类器已启用（AutoClassify）")
	}
	// 同步注入 pluginMgr 到 sessionMgr（用于 SessionEnd hook on delete）
	m.sessionMgr.pluginMgr = m.pluginMgr

	// 连接权限持久化存储
	if m.permMgr != nil {
		if permStore, ok := st.(skills.PermissionStore); ok && permStore != nil {
			m.permMgr.SetStore(permStore)
			logger.Info("权限持久化已使用主存储")
		} else {
			logger.Warn("主存储不支持权限持久化，权限将仅保留在内存中")
		}
	}

	if bridge := skillReg.GetToolBridge(); bridge != nil {
		m.toolBridge = bridge
		bridge.SetExecutionGate(m.CheckNestedToolInputRouteAllowed)
		logger.Debug("权限管理器已创建", zap.Int("rules", len(hitlCfg.PermissionRules)))
	}

	// 初始化工具策略引擎
	m.toolPolicy = buildToolPolicy(cfg.ToolPolicy)
	for _, w := range m.toolPolicy.Warnings() {
		logger.Warn("工具策略配置问题", zap.String("warning", w))
	}
	m.masterFilter = m.toolPolicy.MasterFilter()
	if m.masterFilter != nil {
		logger.Info("Master 工具过滤已启用", zap.String("profile", cfg.ToolPolicy.MasterProfile))
	} else if cfg.ToolPolicy.MasterProfile != "" && cfg.ToolPolicy.MasterProfile != "full" {
		logger.Warn("master_profile 已配置但 MasterFilter 为 nil，可能 profile 名称有误",
			zap.String("profile", cfg.ToolPolicy.MasterProfile))
	}

	// 初始化动态 Agent 工厂
	// AgentFactory 需要 *skills.Registry；从接口中提取底层类型
	var baseSkillReg *skills.Registry
	switch r := skillReg.(type) {
	case *skills.OverlayRegistry:
		baseSkillReg = r.Registry
	case *skills.Registry:
		baseSkillReg = r
	}
	m.agentFactory = subagent.NewAgentFactory(
		llmClient, // 直接传入（CLI 路径直接可用；Server 路径通过 SetAgentFactoryDeps 覆盖为 Router 客户端）
		skillReg.GetToolBridge(),
		m.permMgr,
		baseSkillReg,
		m, // Master 实现了 AgentRegistrar 接口
		logger,
	)
	// 设置工具策略引擎到 AgentFactory（用于 subagent deny 和 group/profile 展开）
	if m.toolPolicy != nil {
		m.agentFactory.SetToolPolicy(m.toolPolicy)
	}
	// 设置动态 Agent 的进度回调和流式内容回调
	m.agentFactory.SetProgressCallback(m.CreateAgentProgressCallback())
	m.agentFactory.SetStreamCallback(m.CreateAgentStreamCallback())
	// CostTracker 回调在 SetCostTracker 中延迟注入（agentFactory 先于 CostTracker 初始化）

	// 初始化 ForkExecutor（依赖 AgentFactory）
	m.forkExecutor = NewForkExecutor(m.agentFactory, logger)

	// 将 ForkExecutor 注册到 SkillRegistry，使 context=fork 的 skill 可被隔离执行
	if skillReg != nil {
		skillReg.SetForkHandler(m.forkExecutor)
	}

	// EventBus 消息丢弃埋点：每次丢弃时记录 metrics
	m.eventBus.SetOnDrop(func(msgType string, total int64) {
		m.enqueueMetric(observability.Metric{
			Name:   "hive.eventbus.dropped",
			Value:  1,
			Labels: map[string]any{"msg_type": msgType, "total": total},
		})
	})

	return m
}

// RequestCh 返回 SessionLoop 的请求通道
func (m *Master) RequestCh() chan<- SessionRequest {
	return m.sessionMgr.RequestCh()
}

// ResponseCh 返回 SessionLoop 的响应通道
func (m *Master) ResponseCh() <-chan TaskResponse {
	return m.sessionMgr.ResponseCh()
}

// RegisterAgent 注册一个 sub-agent
func (m *Master) RegisterAgent(agent subagent.Agent) error {
	if err := m.registry.Register(agent); err != nil {
		return err
	}
	m.transport.RegisterAgent(agent)
	return nil
}

// RegisterDynamic 实现 subagent.AgentRegistrar 接口 — 注册动态 Agent
func (m *Master) RegisterDynamic(agent subagent.Agent) error {
	if err := m.registry.Register(agent); err != nil {
		return err
	}
	m.transport.RegisterAgent(agent)

	card := agent.Card()
	// 广播动态 Agent 创建事件
	// no session scope by design — agent registry 是全局视图，所有用户共享 agent 元数据
	m.eventBus.BroadcastGenericMessage(EventTypeAgentCreated, map[string]interface{}{
		"agent_id":    agent.ID(),
		"agent_name":  card.Name,
		"description": card.Description,
	})
	// 触发 AgentSpawned hook
	if m.pluginMgr != nil {
		hookCtx, cancel := context.WithTimeout(context.Background(), plugin.HookCallTimeout)
		_ = m.pluginMgr.TriggerAgentSpawned(hookCtx, &plugin.AgentLifecycleInput{
			AgentID:     agent.ID(),
			AgentName:   card.Name,
			Description: card.Description,
		})
		cancel()
	}
	return nil
}

// UnregisterDynamic 实现 subagent.AgentRegistrar 接口 — 注销动态 Agent
func (m *Master) UnregisterDynamic(id string) {
	var agentName string
	if a, err := m.registry.Get(id); err == nil {
		agentName = a.Card().Name
	}

	if err := m.registry.Unregister(id); err != nil {
		m.logger.Warn("注销动态Agent失败", zap.String("agent_id", id), zap.Error(err))
	}
	m.transport.UnregisterAgent(id)

	// 广播动态 Agent 销毁事件
	// no session scope by design — agent 销毁通知所有 session（agent 是跨 session 共享资源）
	m.eventBus.BroadcastGenericMessage(EventTypeAgentDestroyed, map[string]interface{}{
		"agent_id": id,
	})
	// 触发 AgentDestroyed hook
	if m.pluginMgr != nil {
		hookCtx, cancel := context.WithTimeout(context.Background(), plugin.HookCallTimeout)
		_ = m.pluginMgr.TriggerAgentDestroyed(hookCtx, &plugin.AgentLifecycleInput{
			AgentID:   id,
			AgentName: agentName,
		})
		cancel()
	}
}

// GetAgentFactory 返回动态 Agent 工厂
func (m *Master) GetAgentFactory() *subagent.AgentFactory {
	return m.agentFactory
}

// GetToolPolicy 返回 Master 持有的工具策略引擎，供固定 Agent 注册时使用
func (m *Master) GetToolPolicy() *skills.ToolPolicy {
	return m.toolPolicy
}

// CreateAgentProgressCallback 创建一个用于 SubAgent AgentLoop 的进度回调函数
// 将工具调用级进度事件通过 EventBus 广播到前端。
// subagent-session-scoping: 必须使用 BroadcastSessionMessage 携带 SessionID，否则
// session A 的 subagent 进度会泄漏到 session B 的订阅端（IM EventRenderer / WebSocket）。
func (m *Master) CreateAgentProgressCallback() subagent.ProgressCallback {
	return func(event subagent.ProgressEvent) {
		m.eventBus.BroadcastSessionMessage(event.SessionID, BroadcastMessage{
			Type:      EventTypeAgentProgress,
			SessionID: event.SessionID,
			Payload: AgentProgressEvent{
				AgentID:  event.AgentID,
				Turn:     event.Turn,
				MaxTurns: event.MaxTurns,
				ToolName: event.ToolName,
				Status:   event.Status,
				Error:    event.Error,
			},
		})
	}
}

// CreateAgentStreamCallback 创建一个用于 SubAgent AgentLoop 的流式内容回调函数
// 将 sub-agent 的实时生成内容通过 EventBus 广播到前端。
// subagent-session-scoping: callback 签名 BREAKING — 新增 sessionID 第 2 参；走
// BroadcastSessionMessage 路径以避免跨 session 泄漏（spec 12.4 contract 扩展）。
func (m *Master) CreateAgentStreamCallback() subagent.StreamCallback {
	return func(agentID string, sessionID string, content string, reasoning string) {
		payload := map[string]interface{}{
			"agent_id":   agentID,
			"session_id": sessionID,
			"content":    content,
			"status":     "streaming",
		}
		if reasoning != "" {
			payload["reasoning_content"] = reasoning
		}
		m.eventBus.BroadcastSessionMessage(sessionID, BroadcastMessage{
			Type:      EventTypeAgentProgress,
			SessionID: sessionID,
			Payload:   payload,
		})
	}
}

// ListAgents 返回所有已注册的 sub-agents
func (m *Master) ListAgents() []subagent.AgentCard {
	return m.registry.List()
}

// SetMCPHost 设置 MCP 工具宿主，使 Master 能监听工具列表变更
func (m *Master) SetMCPHost(host *mcphost.Host) {
	m.mcpHost = host
}

// buildToolPolicy 将配置类型转换为 skills.ToolPolicy 引擎
func buildToolPolicy(cfg config.ToolPolicyConfig) *skills.ToolPolicy {
	groups := make([]skills.ToolGroupInput, len(cfg.Groups))
	for i, g := range cfg.Groups {
		groups[i] = skills.ToolGroupInput{Name: g.Name, Tools: g.Tools}
	}
	profiles := make([]skills.ToolProfileInput, len(cfg.Profiles))
	for i, p := range cfg.Profiles {
		profiles[i] = skills.ToolProfileInput{Name: p.Name, Tools: p.Tools}
	}
	return skills.NewToolPolicy(skills.ToolPolicyInput{
		Groups:           groups,
		Profiles:         profiles,
		GlobalDeny:       cfg.GlobalDeny,
		SubagentDeny:     cfg.SubagentDeny,
		SubagentLeafDeny: cfg.SubagentLeafDeny,
		MasterProfile:    cfg.MasterProfile,
	})
}

// SetMemoryInjector 设置记忆注入器，使 Master 能在 LLM 调用前注入相关记忆
func (m *Master) SetMemoryInjector(inj *memory.Injector) {
	m.memoryInjector = inj
}

// SetFeedbackExtractor 设置 feedback 记忆提取器，使质量反馈能进入记忆闭环。
func (m *Master) SetFeedbackExtractor(ext memory.FeedbackExtractor) {
	m.feedbackExtractor = ext
}

// SetTrajectoryStore 设置诊断级 step snapshot 存储。
func (m *Master) SetTrajectoryStore(store trajectory.Store) {
	m.trajectoryStore = store
}

// SetValidationExecutor 设置 test-driven shadow 使用的安全执行器。
func (m *Master) SetValidationExecutor(exec ValidationExecutor) {
	m.validationExec = exec
}

// SetReflectionEvaluator 设置 evaluator shadow 使用的评估器。
func (m *Master) SetReflectionEvaluator(evaluator ReflectionEvaluator) {
	m.reflectionEval = evaluator
}

// SetJournal 设置开发日志器，使 Master 能在会话和工具调用时记录结构化日志
func (m *Master) SetJournal(j journal.Journal) {
	m.journal = j
	m.journalCh = make(chan journalEntry, 256)
	m.journalDone = make(chan struct{})
	m.sessionMgr.journal = j
	// 同步注入 pluginMgr（SetJournal 通常在 pluginMgr 已设置后调用）
	m.sessionMgr.pluginMgr = m.pluginMgr
}

// SetAuthEngine 设置认证引擎，使 Master 能在 runReActLoop 入口检查用户配额
func (m *Master) SetAuthEngine(engine *auth.Engine) {
	m.authEngine = engine
	// 同步将 authEngine 注入 AsyncRecorder，使 LLM 调用后能累加用户配额
	if engine != nil && m.asyncRecorder != nil {
		m.asyncRecorder.SetAuthEngine(engine)
	}
}

// SetCostTracker 设置成本追踪器，使 Master 能在 LLM 调用后记录用量和成本
func (m *Master) SetCostTracker(ct accounting.CostTracker) {
	m.costTracker = ct
	m.asyncRecorder = accounting.NewAsyncRecorder(ct, m.logger)
	// 同步注入到 AgentFactory，使动态 SubAgent 的 LLM 调用也被成本追踪覆盖
	if m.agentFactory != nil && ct != nil {
		m.agentFactory.SetLLMCompleteCallback(m.buildLLMCompleteCallback())
	}
}

// CostTracker 返回成本追踪器（Admin API 使用）
func (m *Master) CostTracker() accounting.CostTracker {
	return m.costTracker
}

// RuntimePolicySnapshot 返回当前生效的运行时策略。
func (m *Master) RuntimePolicySnapshot() runtimepolicy.Policy {
	if m == nil {
		return runtimepolicy.Default()
	}
	return m.config.RuntimePolicy.WithDefaults()
}

// BuildLLMCompleteCallback 返回基于当前 asyncRecorder 的 LLM 完成回调，供固定 Agent 注册时使用。
// 若 costTracker 尚未设置则返回 nil。
func (m *Master) BuildLLMCompleteCallback() subagent.LLMCompleteCallback {
	if m.asyncRecorder == nil {
		return nil
	}
	return m.buildLLMCompleteCallback()
}

// buildLLMCompleteCallback 构建 LLM 完成回调，通过 AsyncRecorder.RecordUsage 异步写入（内部复用）
func (m *Master) buildLLMCompleteCallback() subagent.LLMCompleteCallback {
	return func(agentID, sessionID, userID, model string, usage llm.Usage) {
		m.asyncRecorder.RecordUsageWithMeta(sessionID, userID, model, usage, accounting.UsageMeta{TaskType: "subagent"})
	}
}

// SetTracer 设置可观测性 Tracer
func (m *Master) SetTracer(t observability.Tracer) {
	m.tracer = t
	if m.obsCh == nil {
		m.obsCh = make(chan observabilityEntry, 512)
		m.obsDone = make(chan struct{})
	}
}

// observabilityEntry 异步写入队列的条目（span 或 metric 二选一）
type observabilityEntry struct {
	span   *observability.Span
	metric *observability.Metric
	log    *observability.LogEntry
}

// journalEntry 异步 journal 写入队列的条目（三种类型三选一）
type journalEntry struct {
	toolCall   *journal.ToolCallEntry
	fileChange *journal.FileChangeEntry
	decision   *journal.DecisionEntry
}

// SetMetricsWriter 设置可观测性 MetricsWriter
// 如果 Tracer 尚未设置（obsCh 为 nil），也会初始化队列，确保 metrics-only 模式可用
func (m *Master) SetMetricsWriter(w observability.MetricsWriter) {
	m.metricsWriter = w
	if m.obsCh == nil {
		m.obsCh = make(chan observabilityEntry, 512)
		m.obsDone = make(chan struct{})
	}
}

// SetLogWriter 设置可观测性 LogWriter。
func (m *Master) SetLogWriter(w observability.LogWriter) {
	m.logWriter = w
	if m.obsCh == nil {
		m.obsCh = make(chan observabilityEntry, 512)
		m.obsDone = make(chan struct{})
	}
}

// SetPromptLoader 设置 Prompt 外部化加载器（可选，nil 时使用硬编码默认值）
func (m *Master) SetPromptLoader(loader *i18n.PromptLoader) {
	m.promptLoader = loader
}

// SetSpecRunner 注入 spec-driven intake path 的 runner 执行器（Sprint 3.3.a）。
//
// 生产路径：bootstrap/server.go 应在 Master 构造后、Start() 前调用此方法，
// 传入 ingress.NewMinimalRunner(router, logger)。
//
// 测试路径：可以注入 nil（等价于旧 stub fail-closed 路径——applySpecDrivenIntake
// 会把没 runner 视同 planner schema 失败，降级 legacy），或注入 fake runner
// 校验 runner 契约。
func (m *Master) SetSpecRunner(r ingress.Runner) {
	m.specRunner = r
}

// StartObsWorker 启动异步 observability 写入 worker goroutine
// 必须在 SetTracer 之后调用，shutdown 时通过 ctx 取消触发 drain
func (m *Master) StartObsWorker(ctx context.Context) {
	if m.obsCh == nil {
		return
	}
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		writeEntry := func(e observabilityEntry) {
			wCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			if e.span != nil && m.tracer != nil {
				_ = m.tracer.RecordSpan(wCtx, *e.span)
			}
			if e.metric != nil && m.metricsWriter != nil {
				_ = m.metricsWriter.Record(wCtx, *e.metric)
			}
			if e.log != nil && m.logWriter != nil {
				_ = m.logWriter.Write(wCtx, *e.log)
			}
		}
		for {
			select {
			case <-ctx.Done():
				// drain 剩余条目
				for {
					select {
					case e := <-m.obsCh:
						writeEntry(e)
					default:
						m.flushObservabilityDropped(context.Background())
						close(m.obsDone)
						return
					}
				}
			case <-ticker.C:
				m.flushObservabilityDropped(context.Background())
			case e, ok := <-m.obsCh:
				if !ok {
					m.flushObservabilityDropped(context.Background())
					close(m.obsDone)
					return
				}
				writeEntry(e)
			}
		}
	}()
}

// enqueueSpan 将 span 放入异步写入队列（nil 安全，队列满时丢弃）
func (m *Master) enqueueSpan(span observability.Span) {
	if m.obsCh == nil {
		return
	}
	if !m.tracingEnabled() || !m.tryCountSessionSpan(span.SessionID) {
		m.spansDropped.Add(1)
		return
	}
	select {
	case m.obsCh <- observabilityEntry{span: &span}:
	default:
		m.spansDropped.Add(1)
	}
}

func (m *Master) tracingEnabled() bool {
	if m == nil {
		return false
	}
	tracing := m.config.Observability.Tracing
	if !tracing.Enabled {
		// Config{} 是大量测试的零值构造，按默认开启处理。
		return tracing.SampleRate == 0 && tracing.MaxSpanPerSession == 0
	}
	return true
}

func (m *Master) maxSpanPerSession() int64 {
	limit := m.config.Observability.Tracing.MaxSpanPerSession
	if limit <= 0 {
		return 2000
	}
	return int64(limit)
}

func (m *Master) tryCountSessionSpan(sessionID string) bool {
	if sessionID == "" {
		return true
	}
	limit := m.maxSpanPerSession()
	value, _ := m.spanCounts.LoadOrStore(sessionID, &atomic.Int64{})
	counter := value.(*atomic.Int64)
	return counter.Add(1) <= limit
}

// enqueueMetric 将 metric 放入异步写入队列（nil 安全，队列满时丢弃）
func (m *Master) enqueueMetric(metric observability.Metric) {
	if m.obsCh == nil {
		return
	}
	select {
	case m.obsCh <- observabilityEntry{metric: &metric}:
	default:
		m.metricsDropped.Add(1)
	}
}

// enqueueLog 将 log entry 放入异步写入队列（nil 安全，队列满时丢弃）。
func (m *Master) enqueueLog(entry observability.LogEntry) {
	if m.obsCh == nil {
		return
	}
	select {
	case m.obsCh <- observabilityEntry{log: &entry}:
	default:
		m.logsDropped.Add(1)
	}
}

func (m *Master) flushObservabilityDropped(ctx context.Context) {
	if m.metricsWriter == nil {
		return
	}
	m.flushDroppedCounter(ctx, "span", "obs_queue_full", &m.spansDropped)
	m.flushDroppedCounter(ctx, "metric", "obs_queue_full", &m.metricsDropped)
	m.flushDroppedCounter(ctx, "log", "obs_queue_full", &m.logsDropped)
}

func (m *Master) flushDroppedCounter(ctx context.Context, kind, reason string, counter *atomic.Int64) {
	dropped := counter.Load()
	if dropped <= 0 {
		return
	}
	if !counter.CompareAndSwap(dropped, 0) {
		return
	}
	err := m.metricsWriter.Record(ctx, observability.Metric{
		Name:  "hive.observability.dropped",
		Value: float64(dropped),
		Labels: map[string]any{
			"kind":   kind,
			"reason": reason,
		},
	})
	if err != nil {
		counter.Add(dropped)
	}
}

// StartJournalWorker 启动异步 journal 写入 worker goroutine（#8）
// 必须在 SetJournal 之后调用。支持 ToolCall / FileChange / Decision 三种条目。
func (m *Master) StartJournalWorker(ctx context.Context) {
	if m.journal == nil || m.journalCh == nil {
		return
	}
	go func() {
		writeEntry := func(e journalEntry) {
			jCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			switch {
			case e.toolCall != nil:
				if err := m.journal.LogToolCall(jCtx, *e.toolCall); err != nil {
					m.logger.Warn("Journal LogToolCall 异步写入失败", zap.Error(err))
				} else {
					// 广播 journal_event 到回放页 Live 模式
					m.eventBus.BroadcastSessionMessage(e.toolCall.SessionID, BroadcastMessage{
						Type: "journal_event",
						Payload: journal.JournalEvent{
							Type:       "tool_call",
							Timestamp:  e.toolCall.Timestamp,
							ToolName:   e.toolCall.ToolName,
							Arguments:  e.toolCall.Arguments,
							Result:     e.toolCall.Result,
							IsError:    e.toolCall.IsError,
							DurationMs: e.toolCall.Duration.Milliseconds(),
						},
					})
					if m.pluginMgr != nil {
						_ = m.pluginMgr.TriggerJournalEntry(jCtx, &plugin.JournalEntryInput{
							SessionID:  e.toolCall.SessionID,
							ToolName:   e.toolCall.ToolName,
							DurationMs: e.toolCall.Duration.Milliseconds(),
						})
					}
				}
			case e.fileChange != nil:
				if err := m.journal.LogFileChange(jCtx, *e.fileChange); err != nil {
					m.logger.Warn("Journal LogFileChange 异步写入失败", zap.Error(err))
				} else {
					m.eventBus.BroadcastSessionMessage(e.fileChange.SessionID, BroadcastMessage{
						Type: "journal_event",
						Payload: journal.JournalEvent{
							Type:      "file_change",
							Timestamp: e.fileChange.Timestamp,
							FilePath:  e.fileChange.FilePath,
							Action:    e.fileChange.Action,
							Summary:   e.fileChange.Summary,
						},
					})
				}
			case e.decision != nil:
				if err := m.journal.LogDecision(jCtx, *e.decision); err != nil {
					m.logger.Warn("Journal LogDecision 异步写入失败", zap.Error(err))
				} else {
					m.eventBus.BroadcastSessionMessage(e.decision.SessionID, BroadcastMessage{
						Type: "journal_event",
						Payload: journal.JournalEvent{
							Type:      "decision",
							Timestamp: e.decision.Timestamp,
							Decision:  e.decision.Decision,
							Reason:    e.decision.Reason,
						},
					})
				}
			}
		}
		for {
			select {
			case <-ctx.Done():
				for {
					select {
					case e := <-m.journalCh:
						jCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
						_ = cancel
						writeEntry(e)
						_ = jCtx
					default:
						close(m.journalDone)
						return
					}
				}
			case e, ok := <-m.journalCh:
				if !ok {
					close(m.journalDone)
					return
				}
				writeEntry(e)
			}
		}
	}()
}

// StopJournalWorker 发送停止信号并等待 worker 退出
func (m *Master) StopJournalWorker() {
	if m.journalCh == nil {
		return
	}
	close(m.journalCh)
	<-m.journalDone
}

// GetSessionJournal 获取指定会话的统一事件流（回放用）。
// after 非零时只返回 timestamp > after 的增量事件。
func (m *Master) GetSessionJournal(ctx context.Context, sessionID string, limit int, after time.Time) ([]journal.JournalEvent, error) {
	if m.journal == nil {
		return nil, journal.ErrJournalNotAvailable
	}
	return m.journal.GetJournalEvents(ctx, sessionID, limit, after)
}

// GetJournalStats 批量查询多个 session 的 journal 统计摘要（画廊页用）
func (m *Master) GetJournalStats(ctx context.Context, sessionIDs []string) (map[string]*journal.JournalStats, error) {
	if m.journal == nil {
		return nil, journal.ErrJournalNotAvailable
	}
	return m.journal.GetJournalStats(ctx, sessionIDs)
}

// loadExternalResources 从 Store 加载外部资源配置到内存缓存
func (m *Master) loadExternalResources() {
	if m.store == nil {
		return
	}
	fullStore, ok := m.store.(store.Store)
	if !ok {
		return
	}
	resources, err := fullStore.ListExternalResources(context.Background())
	if err != nil {
		m.logger.Warn("加载外部资源配置失败", zap.Error(err))
		return
	}
	m.resourcesMu.Lock()
	m.externalResources = resources
	m.resourcesMu.Unlock()
}

// getExternalResources 返回缓存的外部资源列表
func (m *Master) getExternalResources() []*store.ExternalResourceRecord {
	m.resourcesMu.RLock()
	defer m.resourcesMu.RUnlock()
	return m.externalResources
}

// SetAgentFactoryDeps 延迟设置 AgentFactory 的 LLM 客户端依赖
// 在 cmd/server/main.go 中 llmClient 创建后调用
func (m *Master) SetAgentFactoryDeps(llmClient *llm.Client) {
	if m.agentFactory != nil {
		m.agentFactory.SetLLMClient(llmClient)
	}
}

// SetAgentFactoryLLMResolver 设置 AgentFactory 的动态 LLM 客户端获取函数。
// 动态 Agent 在创建时使用 resolver 获取当前最优的 LLM client（走 AIRouter task-type 选路）。
func (m *Master) SetAgentFactoryLLMResolver(resolver subagent.LLMClientResolver) {
	if m.agentFactory != nil {
		m.agentFactory.SetLLMResolver(resolver)
	}
}

// copyMap 创建 map 的浅拷贝
func copyMap(m map[string]any) map[string]any {
	if m == nil {
		return make(map[string]any)
	}
	result := make(map[string]any, len(m))
	for k, v := range m {
		result[k] = v
	}
	return result
}

func parseTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil && s != "" {
		// 非空字符串解析失败时返回零值，调用方可根据零值判断
		return time.Time{}
	}
	return t
}

// SwitchModel 在运行时切换 LLM 模型（由前端 PUT /api/v1/model 触发）
func (m *Master) SwitchModel(name, model, baseURL, provider, apiFormat string) {
	m.llmMu.Lock()
	defer m.llmMu.Unlock()

	// 优先使用 Router（运行时配置由 DB + Router 管理）
	if m.router != nil {
		m.router.SwitchUserModel(name, model, baseURL, provider, apiFormat)
	} else if m.llmClient != nil {
		// Fallback: 无 Router 场景（如无 DB），直接重配置 LLM Client
		// 更新 m.config 作为 fallback 路径的状态（getSessionLLM/prompt_builder 依赖它）
		m.config.Model = model
		if baseURL != "" {
			m.config.BaseURL = baseURL
		}
		if provider != "" {
			m.config.Provider = provider
		}
		if apiFormat != "" {
			m.config.APIFormat = apiFormat
		}
		provDef := llm.LookupProvider(m.config.Provider)
		provDef.APIFormat = m.config.APIFormat
		m.llmClient.Reconfigure(model, m.config.BaseURL, provDef)
	}
	m.logger.Info("模型已切换", zap.String("model", model))
}

// GetRouter 返回 AI 服务路由器（可能为 nil）
func (m *Master) GetRouter() *airouter.Router {
	return m.router
}

// ActiveModel 返回当前活跃的模型 ID
func (m *Master) ActiveModel() string {
	if m.router != nil {
		return m.router.ActiveModel()
	}
	m.llmMu.RLock()
	defer m.llmMu.RUnlock()
	return m.config.Model
}

// GetCurrentSessionInfo 获取当前活跃会话的信息（委托给 SessionManager）
func (m *Master) GetCurrentSessionInfo() (sessionID, sessionName string) {
	return m.sessionMgr.GetCurrentSessionInfo()
}

// setTaskCancel 注册指定 session 的取消函数
func (m *Master) setTaskCancel(sessionID string, cancel context.CancelFunc) {
	m.taskCancels.Store(sessionID, cancel)
}

// clearTaskCancel 清除指定 session 的取消函数
func (m *Master) clearTaskCancel(sessionID string) {
	m.taskCancels.Delete(sessionID)
}

// StopSessionTask 停止指定 session 的任务，返回是否成功取消
func (m *Master) StopSessionTask(sessionID string) bool {
	if v, ok := m.taskCancels.LoadAndDelete(sessionID); ok {
		v.(context.CancelFunc)()
		return true
	}
	return false
}

// StopCurrentTask 停止所有运行中的任务（兼容旧调用），返回是否成功取消
func (m *Master) StopCurrentTask() bool {
	stopped := false
	m.taskCancels.Range(func(key, value any) bool {
		value.(context.CancelFunc)()
		m.taskCancels.Delete(key)
		stopped = true
		return true
	})
	return stopped
}

// terminateSession 终止指定会话。
// 语义：
//   - 幂等：重复调用直接返回 nil
//   - 若存在运行中任务，触发取消
//   - 始终等待该会话 semaphore 释放，确保无并发任务仍在执行
//   - journal.EndSession 为 best-effort
func (m *Master) terminateSession(sessionID, reason string) error {
	if sessionID == "" {
		return nil
	}

	session := m.sessionMgr.GetSession(sessionID)
	if session != nil {
		first := session.MarkTerminated(reason)
		if first {
			m.endTerminatedSessionJournal(session, reason)
		}
	}

	_ = m.StopSessionTask(sessionID)

	sem := m.getSessionSem(sessionID)
	sem <- struct{}{}
	<-sem

	return nil
}

// endTerminatedSessionJournal 结束已终止会话的 journal，失败仅记录日志。
func (m *Master) endTerminatedSessionJournal(session *SessionState, reason string) {
	if m.journal == nil || session == nil {
		return
	}
	if !session.MarkTerminationJournalEnded() {
		return
	}
	endCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := m.journal.EndSession(endCtx, session.ID, reason); err != nil {
		m.logger.Warn("Journal EndSession 失败",
			zap.String("session_id", session.ID),
			zap.Error(err))
	}
}

// getSessionSem 获取指定 session 的信号量 channel（不存在时自动创建）
func (m *Master) getSessionSem(sessionID string) chan struct{} {
	v, _ := m.sessionSems.LoadOrStore(sessionID, make(chan struct{}, 1))
	return v.(chan struct{})
}

// ListAllSessions 获取所有会话列表
func (m *Master) ListAllSessions(ctx context.Context) ([]*store.SessionRecord, error) {
	if m.store == nil {
		return []*store.SessionRecord{}, nil
	}
	// auth 未启用 → 返回全部
	if !auth.IsAuthEnabled(ctx) {
		return m.store.ListSessions(ctx)
	}
	user := auth.UserFrom(ctx)
	if user == nil {
		return nil, errs.New(errs.CodePermissionDenied, "未登录")
	}
	// 所有用户（包括 admin）只能看到自己的会话，不再有跨用户特权
	return m.store.ListSessionsByUser(ctx, user.ID, false)
}

// GetSessionByID 获取指定会话的详细信息（委托给 SessionManager）
func (m *Master) GetSessionByID(ctx context.Context, sessionID string) (*store.SessionRecord, error) {
	record, err := m.checkSessionAccess(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	if record == nil {
		return nil, errs.New(errs.CodeNotFound, "会话不存在")
	}
	return record, nil
}

// GetSessionMessages 获取会话的消息历史
func (m *Master) GetSessionMessages(ctx context.Context, sessionID string, limit int) ([]store.MessageRecord, error) {
	if m.store == nil {
		return nil, errs.New(errs.CodeInternal, "存储未初始化")
	}
	if _, err := m.checkSessionAccess(ctx, sessionID); err != nil {
		return nil, err
	}
	return m.store.GetMessages(ctx, sessionID, limit)
}

// RevertSessionDB 在 DB 层回滚会话到指定消息索引（与 SessionCommandRevert 配套使用）。
// store 为 nil 时静默忽略。
func (m *Master) RevertSessionDB(ctx context.Context, sessionID string, revertTo int) error {
	if m.store == nil {
		return nil
	}
	return m.store.RevertSession(ctx, sessionID, revertTo)
}

// UpdateSessionStar 更新会话收藏状态
func (m *Master) UpdateSessionStar(ctx context.Context, userID, sessionID string, starred bool) error {
	if m.store == nil {
		return errs.New(errs.CodeInternal, "存储未初始化")
	}
	return m.store.UpsertSessionPref(ctx, userID, sessionID, starred)
}

// UpdateSessionTags 更新会话标签（独立于 SaveSession，不覆盖其他字段）
func (m *Master) UpdateSessionTags(ctx context.Context, sessionID string, tags []string) error {
	if m.store == nil {
		return errs.New(errs.CodeInternal, "存储未初始化")
	}
	if err := m.store.UpdateSessionTags(ctx, sessionID, tags); err != nil {
		return err
	}
	m.sessionMgr.SyncSessionTags(sessionID, tags)
	return nil
}

// getSessionLLM 获取会话绑定的 LLM Client
func (m *Master) getSessionLLM(session *SessionState) *llm.Client {
	// 使用 Router 时，每次都获取最新的客户端（支持热切换）
	if m.router != nil {
		client := m.router.GetLLMClient(airouter.TaskChat)
		session.mu.Lock()
		session.activeLLM = client
		session.activeModel = m.router.ActiveModel()
		session.mu.Unlock()
		return client
	}

	// Fallback: 无 Router 时使用固定的 llmClient
	session.mu.Lock()
	if session.activeLLM != nil {
		client := session.activeLLM
		session.mu.Unlock()
		return client
	}
	session.activeLLM = m.llmClient
	session.activeModel = m.config.Model
	session.mu.Unlock()
	return m.llmClient
}

// ExecuteTask 实现 tools.TaskExecutor 接口
// 执行子任务并返回结果
func (m *Master) ExecuteTask(ctx context.Context, agentID string, instruction string, taskContext map[string]interface{}) (string, error) {
	// agent_id 为空时返回错误（general Agent 已删除，不再有默认值）
	if agentID == "" {
		return "", fmt.Errorf("agent_id 不能为空，请指定目标 Agent（如 explore）或使用 spawn_agent 创建临时 Agent")
	}

	m.logger.Info("执行子任务",
		zap.String("agent_id", agentID),
		zap.String("instruction", instruction),
	)

	// 获取 SubAgent
	agent, err := m.registry.Get(agentID)
	if err != nil {
		return "", errs.Wrap(errs.CodeAgentNotFound, fmt.Sprintf("SubAgent %q 不存在", agentID), err)
	}

	// 构造 payload
	payload := map[string]interface{}{
		"instruction": instruction,
	}
	if taskContext != nil {
		payload["context"] = taskContext
	}

	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return "", errs.Wrap(errs.CodeInvalidInput, "序列化任务 payload 失败", err)
	}

	// 构造任务请求
	tc := toolctx.GetToolContext(ctx)
	taskReq := subagent.TaskRequest{
		ID:            fmt.Sprintf("task-%d", time.Now().UnixNano()),
		Type:          "execute",
		SessionID:     toolctx.GetSessionID(ctx),
		UserID:        auth.UserIDFrom(ctx),
		TraceID:       tools.DeriveChildTraceID(tc.TraceID, agentID),
		ParentSpanID:  tc.SpanID,
		ParentTraceID: tc.TraceID,
		TurnID:        tc.TurnIDOrTraceID(),
		ToolCallID:    tc.ToolCallID,
		Payload:       payloadJSON,
	}

	// 直接使用上游 ctx 的超时（executeTool 已对 task/spawn_agent/parallel_dispatch 豁免 2 分钟超时，
	// 上游调用方如 parallel_dispatch 会设置自己的 per-task 超时，这里不再叠加硬超时）
	resp, err := agent.SendTask(ctx, taskReq)
	if err != nil {
		return "", errs.Wrap(errs.CodePlanExecFailed, fmt.Sprintf("SubAgent %q 执行失败", agentID), err)
	}

	if resp.Error != "" {
		return "", errs.New(errs.CodePlanExecFailed, fmt.Sprintf("SubAgent %q 返回错误: %s", agentID, resp.Error))
	}

	m.logger.Info("子任务执行完成",
		zap.String("agent_id", agentID),
		zap.Int("result_len", len(resp.Result)),
	)

	// 将 Result (json.RawMessage) 转换为字符串
	return string(resp.Result), nil
}

// CompactionStatsSnapshot 压缩统计快照
type CompactionStatsSnapshot struct {
	TriggerCount uint64        // 触发压缩次数
	SkippedCount uint64        // 懒惰模式跳过次数
	AverageDelay time.Duration // 平均延迟时间
}

// GetCompactionStats 获取压缩统计信息（线程安全）
func (m *Master) GetCompactionStats() CompactionStatsSnapshot {
	return m.compactionTracker.Stats()
}

// ResetCompactionStats 重置压缩统计信息（测试用）
func (m *Master) ResetCompactionStats() {
	m.compactionTracker.Reset()
}

// safeExecAdapter 将 security.SafeExecutor 适配到 tools.SafeExecChecker 接口
type safeExecAdapter struct {
	executor *security.SafeExecutor
}

func (a safeExecAdapter) MatchPolicy(command string) string {
	return string(a.executor.MatchPolicy(command))
}

// UpdateSecurityRules 热更新命令安全规则，保留当前 DefaultPolicy（DB 写入由调用方负责）
func (m *Master) UpdateSecurityRules(rules []config.ExecRuleConfig) {
	m.llmMu.RLock()
	currentPolicy := m.currentDefaultPolicy
	m.llmMu.RUnlock()
	m.UpdateSecurityConfig(rules, currentPolicy)
}

// UpdateSecurityConfig 热更新命令安全规则和默认策略
func (m *Master) UpdateSecurityConfig(rules []config.ExecRuleConfig, defaultPolicy string) {
	userRules := make([]security.ExecRule, 0, len(rules))
	for _, r := range rules {
		userRules = append(userRules, security.ExecRule{
			Pattern:     r.Pattern,
			Policy:      security.ExecPolicy(r.Policy),
			Description: r.Description,
		})
	}
	safeExec := security.NewSafeExecutorWithDefault(userRules, security.ExecPolicy(defaultPolicy), m.logger)
	checker := safeExecAdapter{executor: safeExec}
	tools.SetSafeExecutor(checker)
	tools.SetExecutorChecker(checker)
	// 原子替换 createPermissionPromptFn 使用的 executor，零锁读路径不打断
	m.safeExecutor.Store(safeExec)

	m.llmMu.Lock()
	m.currentDefaultPolicy = defaultPolicy
	m.llmMu.Unlock()

	m.logger.Info("安全执行规则已热更新",
		zap.Int("user_rules", len(userRules)),
		zap.String("default_policy", defaultPolicy),
	)
}

// GetOrCreateSession 获取或创建指定 ID 的会话（供 acpserver 使用）
// 新建会话时同步触发 SessionStart hook，确保 ACP 路径与 SessionLoop 路径行为一致
func (m *Master) GetOrCreateSession(sessionID string) *SessionState {
	session, isNew := m.sessionMgr.GetOrCreateSession(sessionID)
	if isNew && m.pluginMgr != nil {
		hookCtx, cancel := context.WithTimeout(context.Background(), plugin.HookCallTimeout)
		_ = m.pluginMgr.TriggerSessionStart(hookCtx, &plugin.SessionStartInput{
			SessionID: session.ID,
		})
		cancel()
	}
	return session
}

// GetEventBus 返回事件总线（供 acpserver 使用）
func (m *Master) GetEventBus() *EventBus {
	return m.eventBus
}

// GetHITLBroker 返回 HITL 代理（供 acpserver 使用）
func (m *Master) GetHITLBroker() *HITLBroker {
	return m.hitlBroker
}

// SetPermissionPromptFn 动态替换权限提示函数（用于 ACP 权限桥接）
// 注意：单会话场景直接调用此函数；多会话场景请使用 SetSessionPermissionFn
func (m *Master) SetPermissionPromptFn(fn func(context.Context, skills.PermissionRequest) (skills.PermissionResponse, error)) {
	m.permMgr.SetPromptFn(fn)
}

// SetSessionPermissionFn 为指定 ACP 会话设置权限提示函数（多会话安全）
// SessionLoop 是单线程的，权限请求发生时当前 active session 即为触发者
func (m *Master) SetSessionPermissionFn(sessionID string, fn func(context.Context, skills.PermissionRequest) (skills.PermissionResponse, error)) {
	m.sessionPermFnsMu.Lock()
	if m.sessionPermFns == nil {
		m.sessionPermFns = make(map[string]func(context.Context, skills.PermissionRequest) (skills.PermissionResponse, error))
	}
	m.sessionPermFns[sessionID] = fn
	m.sessionPermFnsMu.Unlock()

	// 注入路由函数：从 ctx 读取 sessionID 分派到对应的会话级 fn
	// P2 fix: 不再依赖 activeSessionID，避免并发时路由到错误 session
	m.permMgr.SetPromptFn(func(ctx context.Context, req skills.PermissionRequest) (skills.PermissionResponse, error) {
		targetID := toolctx.GetSessionID(ctx)
		if targetID == "" {
			// fallback: ctx 中无 sessionID 时退回到 activeSessionID
			targetID = m.sessionMgr.GetActiveSessionID()
		}
		m.sessionPermFnsMu.RLock()
		sessionFn, ok := m.sessionPermFns[targetID]
		m.sessionPermFnsMu.RUnlock()
		if ok {
			return sessionFn(ctx, req)
		}
		// 找不到会话级 fn，默认拒绝（避免路由到错误会话）
		return skills.PermissionResponse{Granted: false}, nil
	})
}

// ClearSessionPermissionFn 清除指定 ACP 会话的权限提示函数（会话断开时调用）
func (m *Master) ClearSessionPermissionFn(sessionID string) {
	m.sessionPermFnsMu.Lock()
	delete(m.sessionPermFns, sessionID)
	remaining := len(m.sessionPermFns)
	m.sessionPermFnsMu.Unlock()

	if remaining == 0 {
		// 所有 ACP 会话已断开，恢复默认（无提示）
		m.permMgr.SetPromptFn(nil)
	}
}
