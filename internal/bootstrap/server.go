package bootstrap

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/chef-guo/agents-hive/internal/accounting"
	"github.com/chef-guo/agents-hive/internal/agentquality"
	"github.com/chef-guo/agents-hive/internal/asset"
	"github.com/chef-guo/agents-hive/internal/auth"
	"github.com/chef-guo/agents-hive/internal/i18n"

	"github.com/chef-guo/agents-hive/internal/acpclient"
	"github.com/chef-guo/agents-hive/internal/airouter"
	"github.com/chef-guo/agents-hive/internal/channel"
	"github.com/chef-guo/agents-hive/internal/channel/dingtalk"
	"github.com/chef-guo/agents-hive/internal/channel/feishu"
	pushsvc "github.com/chef-guo/agents-hive/internal/channel/push"
	"github.com/chef-guo/agents-hive/internal/channel/wechatbot"
	"github.com/chef-guo/agents-hive/internal/channel/wecom"
	"github.com/chef-guo/agents-hive/internal/config"
	"github.com/chef-guo/agents-hive/internal/controlplane"
	"github.com/chef-guo/agents-hive/internal/cs"
	"github.com/chef-guo/agents-hive/internal/gateway"
	"github.com/chef-guo/agents-hive/internal/imcore"
	"github.com/chef-guo/agents-hive/internal/journal"
	"github.com/chef-guo/agents-hive/internal/kb"
	"github.com/chef-guo/agents-hive/internal/llm"
	"github.com/chef-guo/agents-hive/internal/master"
	"github.com/chef-guo/agents-hive/internal/mcphost"
	"github.com/chef-guo/agents-hive/internal/memory"
	"github.com/chef-guo/agents-hive/internal/memoryobs"
	"github.com/chef-guo/agents-hive/internal/observability"
	"github.com/chef-guo/agents-hive/internal/plugin"
	"github.com/chef-guo/agents-hive/internal/runtimepolicy"
	"github.com/chef-guo/agents-hive/internal/sandbox"
	"github.com/chef-guo/agents-hive/internal/sessiontodo"
	"github.com/chef-guo/agents-hive/internal/skills"
	"github.com/jackc/pgx/v5/pgxpool"
	// Blank-import to force skill_install_confirmation choice_type registration
	// at server startup — task 6.0 / BLOCKER 2. The init() in this leaf package
	// registers the HITL choice_type before any skill_install handler can emit.
	_ "github.com/chef-guo/agents-hive/internal/skillhitl"
	"github.com/chef-guo/agents-hive/internal/specdriven/ingress"
	"github.com/chef-guo/agents-hive/internal/specdriven/planner"
	"github.com/chef-guo/agents-hive/internal/store"
	"github.com/chef-guo/agents-hive/internal/subagent"
	"github.com/chef-guo/agents-hive/internal/subagent/compaction"
	"github.com/chef-guo/agents-hive/internal/taskboard"
	"github.com/chef-guo/agents-hive/internal/tools"
)

func runtimePolicyFromConfig(c config.RuntimePolicyConfig) runtimepolicy.Policy {
	return runtimepolicy.Policy{
		LLMCallTimeout:      c.LLMCallTimeout,
		ToolTimeout:         c.ToolTimeout,
		TaskTimeout:         c.TaskTimeout,
		SpawnAgentTimeout:   c.SpawnAgentTimeout,
		ACPPromptTimeout:    c.ACPPromptTimeout,
		ACPReconnectTimeout: c.ACPReconnectTimeout,
		SubagentMaxTurns:    c.SubagentMaxTurns,
		SubagentMaxDepth:    c.SubagentMaxDepth,
		PerSessionParallel:  c.PerSessionParallel,
		GlobalWorkers:       c.GlobalWorkers,
		MaxSessionCostUSD:   c.MaxSessionCostUSD,
	}
}

// ServerComponents 持有 Server 模式所有已初始化的组件
type ServerComponents struct {
	SkillReg              *skills.OverlayRegistry // 双层 skill 注册表（FS + DB）
	SkillStore            *store.SkillStore       // Skill DB 存储（可选，DB 不可用时为 nil）
	SkillSvc              *skills.SkillService    // Skill 热重载服务（可选）
	SkillFinder           *skills.Finder
	QualityCandidateStore *agentquality.PGCandidateStore
	OptimizationStore     agentquality.OptimizationSuggestionStore
	// hive-skill-on-demand 新增（task 11.3-11.6）——按需解析 + 权限 + spec 路由聚合
	SkillDiscovery      *skills.Discovery        // marketplace 远程解析器（按需拉取）；OnDemandEnabled=false 时仍构造，用于未来路径
	AdminChecker        skills.AdminChecker      // public scope 安装权限判定（auth 启用时真实，否则 DenyAll）
	SpecSkillResolver   skills.SpecSkillResolver // 本地 Registry + 远程 Discovery 的语义路由聚合入口
	MCPHost             *mcphost.Host
	MCPClients          []*mcphost.RemoteMCPClient
	MCPClientsMu        sync.Mutex
	PluginMgr           *plugin.Manager
	SessionStore        store.SessionStore
	DB                  store.Store
	AgentReg            *subagent.Registry
	AIRouter            *airouter.Router
	LLMClient           *llm.Client
	Master              *master.Master
	ChannelRouter       *channel.Router
	WeChatBotService    wechatbot.ConnectionService
	PushService         *pushsvc.Service
	FeishuChatStateRepo feishu.ChatStateRepo
	ACPPool             *acpclient.ACPClientPool
	Gateway             *gateway.Gateway
	ConfigMu            sync.RWMutex
	MemStore            memory.MemoryStore
	Executor            sandbox.Executor // 沙箱执行器
	TaskBoard           taskboard.TaskBoard
	SessionTodoStore    sessiontodo.Store
	AuthEngine          *auth.Engine
	AssetService        *asset.AssetService
	AssetAccessResolver asset.AccessResolver
	KBService           *kb.Service
	CustomerService     *cs.Service
	PromptLoader        interface {
		Start(context.Context)
		InvalidateDBCache(key string)
	} // *i18n.PromptLoader（用 interface 避免循环依赖）
	PromptStore interface {
		Get(ctx context.Context, key, language string) (string, bool, error)
		Upsert(ctx context.Context, key, language, content, updatedBy string) error
		Delete(ctx context.Context, key, language string) error
		List(ctx context.Context, page, size int) ([]store.PromptRecord, int, error)
	} // *store.PromptStore（可选，DB 不可用时为 nil）
	FeishuIngressBridge *feishuIngressModeBridge

	refreshCancel          context.CancelFunc
	cleanupCancel          context.CancelFunc // usage_records 清理定时任务
	notifyCtx              context.Context    // PG NOTIFY LISTEN goroutine 生命周期
	notifyCancel           context.CancelFunc // shutdown 时取消 LISTEN
	embeddingBacklogCancel context.CancelFunc // memory embedding backlog worker 生命周期
}

type feishuIngressModeBridge struct {
	mu           sync.RWMutex
	fallbackMode config.FeishuIngressMode
	getter       func() config.FeishuIngressMode
	setter       func(config.FeishuIngressMode)
	gateGetter   func() config.FeishuIngressMode
	gateSetter   func(config.FeishuIngressMode)
}

func newFeishuIngressModeBridge(fallback config.FeishuIngressMode) *feishuIngressModeBridge {
	if fallback == "" {
		fallback = config.FeishuIngressModeWebhook
	}
	return &feishuIngressModeBridge{fallbackMode: fallback}
}

func (b *feishuIngressModeBridge) Get() config.FeishuIngressMode {
	if b == nil {
		return config.FeishuIngressModeWebhook
	}
	b.mu.RLock()
	getter := b.getter
	fallback := b.fallbackMode
	b.mu.RUnlock()
	if getter == nil {
		return fallback
	}
	mode := getter()
	if mode == "" {
		return fallback
	}
	return mode
}

func (b *feishuIngressModeBridge) Set(mode config.FeishuIngressMode) {
	if b == nil {
		return
	}
	b.mu.RLock()
	setter := b.setter
	b.mu.RUnlock()
	if setter != nil {
		setter(mode)
		return
	}
	if mode == "" {
		mode = config.FeishuIngressModeWebhook
	}
	b.mu.Lock()
	b.fallbackMode = mode
	b.mu.Unlock()
}

func (b *feishuIngressModeBridge) GetGate() config.FeishuIngressMode {
	if b == nil {
		return config.FeishuIngressModeWebhook
	}
	b.mu.RLock()
	getter := b.gateGetter
	fallback := b.fallbackMode
	b.mu.RUnlock()
	if getter == nil {
		return fallback
	}
	mode := getter()
	if mode == "" {
		return fallback
	}
	return mode
}

func (b *feishuIngressModeBridge) SetGate(mode config.FeishuIngressMode) {
	if b == nil {
		return
	}
	b.mu.RLock()
	setter := b.gateSetter
	b.mu.RUnlock()
	if setter != nil {
		setter(mode)
		return
	}
	if mode == "" {
		mode = config.FeishuIngressModeWebhook
	}
	b.mu.Lock()
	b.fallbackMode = mode
	b.mu.Unlock()
}

func (b *feishuIngressModeBridge) Bind(
	getter func() config.FeishuIngressMode,
	setter func(config.FeishuIngressMode),
	gateGetter func() config.FeishuIngressMode,
	gateSetter func(config.FeishuIngressMode),
) {
	if b == nil {
		return
	}
	b.mu.Lock()
	b.getter = getter
	b.setter = setter
	b.gateGetter = gateGetter
	b.gateSetter = gateSetter
	b.mu.Unlock()
}

// InitServer 执行 Server 模式的全量初始化，返回所有组件
func InitServer(cfg *config.Config, configPath string, logger *zap.Logger) *ServerComponents {
	if err := ensureFileConvDependencies(context.Background(), cfg, logger); err != nil {
		logger.Fatal("文档转换依赖初始化失败", zap.Error(err))
	}

	sc := &ServerComponents{
		FeishuIngressBridge: newFeishuIngressModeBridge(cfg.Channel.Feishu.ResolvedIngressMode()),
	}

	// 0. 启动期 fail-fast 校验（hive-skill-on-demand MINOR 2 / task 10.3）：
	//    - 4-dim feature flag 依赖违反（`subagent_mode` / `skills_semantic_routing` 依赖 specdriven.enabled）
	//    - `on_demand_enabled=true` 但 `marketplace_urls` 空
	//    校验失败 → 立即 zap.Fatal 终止，防止在错误配置下启动服务。
	if err := config.ValidateFlagCombination(cfg); err != nil {
		logger.Fatal("feature flag validation failed", zap.Error(err))
	}
	if err := config.ValidateSkillsConfig(cfg.Agent.Skills); err != nil {
		logger.Fatal("skills config validation failed", zap.Error(err))
	}
	// 打印 4 维 flag 激活组合（grep 契约：tasks.md 10.4/12.3）
	logger.Info(config.SnapshotFeatureFlags(cfg).String())

	// PG NOTIFY LISTEN 上下文（与 server 生命周期相同）
	sc.notifyCtx, sc.notifyCancel = context.WithCancel(context.Background())

	// 1. Skills (含 Discovery / AdminChecker / SpecSkillResolver 一并装配——§11.2-11.6)
	sc.SkillReg, sc.SkillFinder, sc.SkillDiscovery = initSkills(cfg, logger)
	sc.AdminChecker = skills.NewDenyAllAdminChecker() // 认证引擎就绪后于 initAuthEngine 之后覆盖
	sc.SpecSkillResolver = initSpecSkillResolver(cfg, sc.SkillReg, sc.SkillDiscovery)

	// 2. 模型注册表
	llm.InitModelRegistry(logger, cfg.LLM.ModelRegistryURL)
	refreshCtx, refreshCancel := context.WithCancel(context.Background())
	sc.refreshCancel = refreshCancel
	llm.StartModelRegistryRefresh(refreshCtx)

	// 3. MCP Host
	sc.MCPHost, sc.MCPClients = initMCPHost(cfg, logger)

	// 4. Skill ↔ Tool 桥接
	toolBridge := skills.NewToolBridge(sc.MCPHost, logger)
	sc.SkillReg.SetToolBridge(toolBridge)
	// 注入共享 Metrics（与 CLI 模式保持一致，确保 /api/v1/metrics/skills 端点有数据）
	if m := sc.SkillReg.GetMetrics(); m != nil {
		toolBridge.SetMetrics(m)
	}

	// 5. 插件
	sc.PluginMgr = initPlugins(cfg, logger)
	if sc.PluginMgr != nil {
		toolBridge.SetPluginManager(sc.PluginMgr)
	}

	// 6. Agent Registry（LLM Client 延迟到 DB 配置加载之后）
	sc.AgentReg = subagent.NewRegistry(logger)

	// 7. Store + DB 配置加载（DB 为运行时配置的唯一真相源）
	sc.SessionStore, sc.DB = initStore(cfg, logger)

	if sc.DB != nil {
		// 种子：首次启动将 config.json 中的配置写入 DB（幂等）
		seedLLMConfig(cfg, sc.DB, logger)
		MigrateConfigToDB(sc.DB, cfg, logger)

		// 加载：从 DB 覆盖到内存 cfg（DB 优先）
		LoadAllConfigFromDB(sc.DB, cfg, logger)
		LoadChannelConfigsFromDB(sc.DB, cfg, logger)
		LoadMCPServersFromDB(sc.DB, cfg, logger)
		LoadLLMFromDB(sc.DB, cfg, logger)
	}

	// 7.0 认证引擎（在 DB 配置加载之后初始化）
	var pgPool *pgxpool.Pool
	if pgStore, ok := sc.DB.(*store.PostgresStore); ok {
		pgPool = pgStore.Pool()
	}
	sc.AuthEngine = initAuthEngine(context.Background(), cfg, pgPool, logger)
	sc.AdminChecker = initAdminChecker(sc.AuthEngine)
	sc.AssetService = initAssetService(cfg, pgPool, logger)

	// 7.0.5 Skill DB 覆盖层（pg 可用时启用热重载）
	if pgPool != nil {
		sc.SkillStore = store.NewSkillStore(pgPool, logger)
		sc.SkillSvc = skills.NewSkillService(sc.SkillStore, sc.SkillReg, logger)
		sc.QualityCandidateStore = agentquality.NewPGCandidateStore(pgPool, logger)
		sc.OptimizationStore = agentquality.NewPGOptimizationSuggestionStore(pgPool, logger)
		if err := sc.SkillSvc.LoadAll(sc.notifyCtx); err != nil {
			logger.Warn("Skill DB 全量加载失败（忽略，继续启动）", zap.Error(err))
		}
		sc.SkillSvc.Start(sc.notifyCtx)
		logger.Info("Skill DB 覆盖层已启用")
		logger.Info("Agent Quality 候选用例池已启用")
	}

	if cfg.Agent.PlanRuntime.Enabled {
		sc.SessionTodoStore = initSessionTodoStore(context.Background(), cfg, pgPool, logger)
	}

	// 7.1 沙箱执行器（在 DB 配置加载之后创建，确保 cfg.Sandbox 已被 DB 覆盖）
	sc.Executor = initExecutor(cfg, logger)
	tools.SetExecutor(sc.Executor)
	// 连接 DB 加载后的外部 MCP 服务端（确保 DB 中的配置生效）
	sc.MCPClients = connectMCPServers(cfg, sc.MCPHost, logger)

	// 7.5 LLM Client（通过 AIRouter 统一管理，使用 DB 覆盖后的 cfg）
	sc.AIRouter = airouter.NewRouter(airouter.RouterConfig{
		Store:            sc.DB,
		Logger:           logger,
		DefaultModel:     cfg.LLM.Model,
		DefaultProvider:  cfg.LLM.Provider,
		DefaultBaseURL:   cfg.LLM.BaseURL,
		DefaultAPIKey:    cfg.LLM.APIKey,
		DefaultAPIFormat: cfg.LLM.APIFormat,
		DisableJSONMode:  cfg.LLM.DisableJSONMode,
		ReasoningEffort:  cfg.LLM.ReasoningEffort,
		StorePrivacy:     cfg.LLM.StorePrivacy,
		PromptCacheKey:   cfg.LLM.PromptCacheKeyEnabled,
		ServiceTier:      cfg.LLM.InteractiveServiceTier,
	})
	sc.LLMClient = sc.AIRouter.GetUserLLMClient()

	if pgPool != nil {
		kbOptions := []kb.ServiceOption{kb.WithSummaryGenerator(newAirouterKBSummaryGenerator(sc.AIRouter, logger))}
		if sc.AssetService != nil {
			kbOptions = append(kbOptions, kb.WithAssetUploader(newKBAssetUploader(sc.AssetService)))
		}
		sc.KBService = kb.NewService(kb.NewPGStore(pgPool), kbOptions...)
		logger.Info("KB 服务已初始化")
	}
	if sc.AssetService != nil {
		sc.AssetAccessResolver = initAssetAccessResolver(logger, sc.KBService)
	}

	// 8. Master（使用 DB 覆盖后的 cfg）
	sc.Master = master.NewMaster(master.Config{
		Model:                       cfg.LLM.Model,
		BaseURL:                     cfg.LLM.BaseURL,
		APIKey:                      cfg.LLM.APIKey,
		DisableJSONMode:             cfg.LLM.DisableJSONMode,
		MaxConcurrentAgents:         cfg.Agent.MaxConcurrentAgents,
		ReasoningEffort:             cfg.LLM.ReasoningEffort,
		Provider:                    cfg.LLM.Provider,
		APIFormat:                   cfg.LLM.APIFormat,
		PromptLanguage:              cfg.PromptLanguage,
		ShellTimeout:                cfg.Agent.ShellTimeout,
		ScriptTimeout:               cfg.Agent.ScriptTimeout,
		SyncInterval:                cfg.Agent.SyncInterval,
		ContextCompression:          cfg.Agent.ContextCompression,
		SecurityRules:               cfg.Security.ExecRules,
		SecurityDefaultPolicy:       cfg.Security.DefaultPolicy,
		SecurityPermissionMode:      cfg.Security.PermissionMode,
		SecurityDestructivePatterns: cfg.Security.DestructivePatterns,
		PluginMgr:                   sc.PluginMgr,
		InstructionURLs:             cfg.InstructionURLs,
		StorePrivacy:                cfg.LLM.StorePrivacy,
		PromptCacheKey:              cfg.LLM.PromptCacheKeyEnabled,
		ServiceTier:                 cfg.LLM.InteractiveServiceTier,
		Router:                      sc.AIRouter,
		ToolPolicy:                  cfg.Agent.ToolPolicy,
		Tools:                       cfg.Tools,
		ToolRecall:                  cfg.Agent.ToolRecall,
		FirstToken:                  cfg.Agent.FirstToken,
		ActionGuardEnabled:          cfg.Agent.ActionGuardEnabled,
		MaxModelVisibleTools:        cfg.Agent.MaxModelVisibleTools,
		MaxSessionCost:              cfg.Agent.MaxSessionCost,
		SpecDriven:                  cfg.SpecDriven,
		PlanRuntime:                 cfg.Agent.PlanRuntime,
		QualityGuards:               cfg.Agent.QualityGuards,
		Reflection:                  cfg.Agent.Reflection,
		ReasoningEffortAuto:         cfg.Agent.ReasoningEffortAuto,
		Observability:               cfg.Agent.Observability,
		RuntimePolicy:               runtimePolicyFromConfig(cfg.RuntimePolicy),
	}, cfg.HITL, sc.AgentReg, sc.SkillReg, sc.SessionStore, logger)

	sc.Master.EnableStreamingExecutor = true // 域D: 并发工具执行已就绪，正式启用
	sc.Master.SetValidationExecutor(sc.Executor)
	sc.Master.SetMCPHost(sc.MCPHost)
	if sc.AssetService != nil {
		sc.Master.SetAssetService(sc.AssetService)
	}
	if sc.SessionTodoStore != nil {
		sc.Master.SetSessionTodoStore(sc.SessionTodoStore)
	}
	if sc.KBService != nil {
		sc.Master.SetKBEvidenceReader(sc.KBService)
	}
	if sc.KBService != nil {
		sc.KBService.SetQualityRecorder(sc.Master)
	}

	// hive-skill-on-demand §11.5：把 Master 适配为 mcphost.HITLEmitter 注入 Host。
	// tasks.md §6.2c 契约：SetHITLEmitter 后 Host.EmitInputRequest 才会把 HITL
	// input_request 路由到 Master，否则返回 ErrHITLEmitterNotConfigured。
	// 在 SetMCPHost 之后立即注入，确保所有工具注册路径 (上 table 300+) 都能走 HITL。
	if emitter := newMasterHITLAdapter(sc.Master); emitter != nil {
		sc.MCPHost.SetHITLEmitter(emitter)
	}

	// spec-driven intake runner 接线（Sprint 3.3.b b5 落地真实 spine）。
	//
	// RealRunner 持 clientProvider 闭包 + tokenBudget，Run 里真调 planner.Generate
	// （airouter.TaskPlanning 拉 LLM client），产出真 Usage + BudgetExceeded 判定。
	// applySpecDrivenIntake 拿到 RunStats 后 emit token_cost / overbudget / fallback。
	//
	// 降级兜底：sc.AIRouter == nil（极少数无模型配置启动场景）时退回 MinimalRunner
	// 保持 fail-closed 语义——session_loop 依旧走 legacy_downshift_planner_schema。
	//
	// 纪律（Sprint 3.3.a DONE 判据 #2）：runner 在 bootstrap 拿 sc.AIRouter，
	// 不能在 master 包内自己创建 router——这条在 RealRunner 时代同样成立（
	// clientProvider 闭包里才 capture router）。
	if sc.AIRouter != nil {
		clientProvider := func(ctx context.Context) (planner.LLMClient, error) {
			client := sc.AIRouter.GetLLMClient(airouter.TaskPlanning)
			if client == nil {
				return nil, errors.New("specdriven.ingress: airouter.TaskPlanning 无可用 LLM 模型")
			}
			return client, nil
		}
		sc.Master.SetSpecRunner(ingress.NewRealRunner(clientProvider, int64(cfg.SpecDriven.Planner.TokenBudget), logger))
	} else {
		sc.Master.SetSpecRunner(ingress.NewMinimalRunner(sc.AIRouter, logger))
	}

	// 7.1.b SpecChangeStore + CAS conflict observer 接线（Sprint 3.3.b）
	// pgPool 可用时才启用——内存启动 / pg 不可用保持 nil（SetSpecChangeStore 内 wire 自动 no-op）
	// 流向：store.UpsertWithCAS 冲突 → observer → m.emitCASConflict → enqueueMetric → obsCh → pg_writer
	if pgPool != nil {
		sc.Master.SetSpecChangeStore(store.NewSpecChangeStore(pgPool, logger))
	} else {
		// Round 5 N3：pgPool 缺席静默降级会让 spec 路径在生产环境哑火（CAS counter 永远 0、
		// CASConflictObserver 永不被回调），operators 无从分辨"真没冲突"还是"路径压根没跑"。
		// 显式 warn + 启动期 emit 一条 spec_change_store_disabled=1 让 dashboard 可见。
		logger.Warn("spec_change_store disabled — PG pool absent, spec write path will degrade " +
			"(cas_conflict_total / spec_change_upsert_total counters will stay at 0). " +
			"To enable: configure DATABASE_URL and restart.")
		sc.Master.EmitSpecChangeStoreDisabled()
	}

	// 7.2 PromptLoader（三层优先级：DB > 文件 > go:embed）
	// 必须在 Master 创建之后调用，initPromptLoader 内部会调用 sc.Master.SetPromptLoader
	initPromptLoader(sc, cfg, pgPool, logger, sc.notifyCtx)

	// 8.5 注册 AI 服务适配器
	if sc.AIRouter != nil {
		sc.AIRouter.RegisterAdapter(airouter.NewImageAdapter(sc.AIRouter, logger))
		sc.AIRouter.RegisterAdapter(airouter.NewVideoAdapter(sc.AIRouter, logger))
		sc.AIRouter.RegisterAdapter(airouter.NewTTSAdapter(sc.AIRouter, logger))
		sc.AIRouter.RegisterAdapter(airouter.NewSTTAdapter(sc.AIRouter, logger))
		sc.AIRouter.RegisterAdapter(airouter.NewEmbeddingAdapter(sc.AIRouter, logger))
	}

	// 9. 成本追踪（P2-4）— 必须在 registerSubAgents 之前，使固定 Agent 能拿到 LLMCompleteCallback
	initCostTracker(sc, logger)

	// 9.5 Phase 5B: 注入认证引擎到 Master（配额检查 + AsyncRecorder 配额累加）
	if sc.AuthEngine != nil {
		sc.Master.SetAuthEngine(sc.AuthEngine)
	}

	// 10. 注册 SubAgents
	registerSubAgents(sc, cfg, toolBridge, logger)

	// 11. 记忆系统
	initMemory(sc, cfg, logger)

	// 11.5 Journal 日志系统
	initJournal(sc, cfg, logger)

	// 10.57 可观测性（P2-6）
	initObservability(sc, logger)

	// 10.6 TaskBoard 工作项管理
	initTaskBoard(sc, cfg, logger)

	// 11. 注册内置工具
	if pgStore, ok := sc.DB.(*store.PostgresStore); ok && pgStore != nil && pgStore.Pool() != nil {
		sc.CustomerService = cs.NewService(cs.NewPGStore(pgStore.Pool()))
	} else {
		sc.CustomerService = cs.NewService(cs.NewMemoryStore())
	}
	// 注入 HITL 审批桥接（用于 create_tool 审批 和 exec_rules ask 策略）
	if sc.Master != nil {
		tools.SetApprovalBridge(NewApprovalBridge(sc.Master.GetHITLBroker()))
	}
	tools.RegisterBuiltinTools(sc.MCPHost, logger, cfg, sc.Master, sc.Master,
		cfg.CustomToolsDir, nil, sc.SkillReg, sc.PluginMgr, nil, sc.MemStore, sc.Master.GetAgentFactory(), sc.SessionTodoStore, sc.Master, sc.TaskBoard, sc.KBService, sc.CustomerService)

	// 11.1 hive-skill-on-demand：按需注册 skill_install / skill_search。
	// OnDemandEnabled=false 时完全 skip，对旧部署 byte-identical（§8.4 回归基线配套）。
	if cfg.Agent.Skills.OnDemandEnabled {
		// 把 *mcphost.Host 当作 HITL emitter 传入，tools 内部做 type-assert。
		// sc.Master 已在上文注入 HITLEmitter 适配器（见 bootstrap/hitl_adapter.go），
		// 故 Host.EmitInputRequest 可透传到 Master.EmitInputRequest。
		tools.RegisterSkillInstallPublic(
			sc.MCPHost, logger,
			sc.SkillReg,       // skills.SkillInstallRegistry（OverlayRegistry 已实现）
			sc.SkillDiscovery, // marketplace 解析器
			sc.Master,         // EventBus 广播（nil 时自动 no-op）
			sc.AdminChecker,   // public scope 准入
			sc.MCPHost,        // HITLEmitter（已适配）
		)
		tools.RegisterSkillSearchPublic(
			sc.MCPHost, logger,
			sc.SkillReg,       // skills.SkillSearchLister
			sc.SkillDiscovery, // 远程搜索
		)
		logger.Info("skill_install / skill_search 已注册（on_demand_enabled=true）")
	}

	// 注册 TaskBoard 工具
	if sc.TaskBoard != nil {
		tools.RegisterTaskBoard(sc.MCPHost, logger, sc.TaskBoard)
	}

	// 注册 AI 能力工具（图片生成、视频生成、TTS）
	if sc.AIRouter != nil {
		tools.RegisterImageGen(sc.MCPHost, sc.AIRouter, logger, cfg.Server.ServerBaseURL())
		tools.RegisterVideoGen(sc.MCPHost, sc.AIRouter, logger)
		tools.RegisterTTS(sc.MCPHost, sc.AIRouter, logger)
	}

	// 12. 远程 ACP Agents
	sc.ACPPool = initACPAgents(sc.Master, cfg, logger)

	// 13. IM Channels（使用 DB 覆盖后的 cfg）
	sc.ChannelRouter = initChannels(sc, cfg, logger)

	// 14.1 延迟注册 IM 相关工具（依赖 channelRouter 和飞书凭证）
	if sc.ChannelRouter != nil {
		tools.RegisterSendIMMessageWithStore(sc.MCPHost, logger, sc.ChannelRouter, sc.DB)
		logger.Info("send_im_message 工具已注册")
	}
	imService := imcore.NewService()
	if cfg.Channel.Feishu.AppID != "" && cfg.Channel.Feishu.AppSecret != "" {
		feishuClient := feishu.NewClient(cfg.Channel.Feishu.AppID, cfg.Channel.Feishu.AppSecret, logger)
		feishuClient.ApplyOutboundConfig(cfg.Channel.Feishu)
		adapter := feishu.NewToolAdapter(feishuClient)
		tools.RegisterFeishuToolsWithOptions(sc.MCPHost, logger, adapter, tools.NewHumanReadableFormatter(), tools.FeishuToolOptions{
			EnableBinaryTransfer: cfg.Channel.Feishu.BinaryTransferEnabledResolved(),
			AuditSink:            feishu.NewJSONLAuditSink(""),
		})
		imService.Register(imcore.NewFeishuAdapter(adapter))
	}
	registerIMAPIService(sc, cfg, logger, imService)

	// 15. Gateway
	sc.Gateway = initGateway(sc, cfg, configPath, logger)

	return sc
}

// CancelRefresh 停止后台模型刷新
func (sc *ServerComponents) CancelRefresh() {
	if sc.refreshCancel != nil {
		sc.refreshCancel()
	}
}

// Shutdown 按顺序关闭所有组件
// 顺序原则：需要 DB pool 的组件（Master.SaveAllSessions、MemStore.embeddingWg）必须在 DB.Close 之前执行
func (sc *ServerComponents) Shutdown() {
	// 先取消 LISTEN goroutine（依赖 DB pool，不能最后取消）
	if sc.notifyCancel != nil {
		sc.notifyCancel()
	}
	sc.CancelRefresh()
	if sc.cleanupCancel != nil {
		sc.cleanupCancel()
	}
	if sc.embeddingBacklogCancel != nil {
		sc.embeddingBacklogCancel()
	}
	if sc.ChannelRouter != nil {
		sc.ChannelRouter.Stop()
	}
	sc.MCPClientsMu.Lock()
	for _, c := range sc.MCPClients {
		_ = c.Close()
	}
	sc.MCPClientsMu.Unlock()
	if sc.ACPPool != nil {
		sc.ACPPool.CloseAll()
	}
	// Master.Stop 会调用 SaveAllSessions，需要 DB pool 活着
	if sc.Master != nil {
		sc.Master.Stop()
	}
	// MemStore.Close 等待 embedding workers 完成，需要 DB pool 活着
	if sc.MemStore != nil {
		_ = sc.MemStore.Close()
	}
	// 最后关闭 DB pool
	if sc.DB != nil {
		_ = sc.DB.Close()
	}
	if sc.Executor != nil {
		_ = sc.Executor.Close()
	}
}

// StartSkillWatcher 启动 Skill 热重载 Watcher（与 CLI 模式保持一致）
func (sc *ServerComponents) StartSkillWatcher(ctx context.Context, logger *zap.Logger) {
	if sc.SkillFinder == nil {
		return
	}
	watcher := skills.NewWatcher(sc.SkillFinder, sc.SkillReg.Registry, 5*time.Second, logger)
	go watcher.Start(ctx)
	logger.Info("Skill Watcher 已启动（热重载）")
}

// StartChannels 启动需要 ctx 的通道插件。
func (sc *ServerComponents) StartChannels(ctx context.Context, logger *zap.Logger) {
}

// --- 内部初始化函数 ---

func initSkills(cfg *config.Config, logger *zap.Logger) (*skills.OverlayRegistry, *skills.Finder, *skills.Discovery) {
	overlayReg := skills.NewOverlayRegistry(logger)

	// 初始化 Metrics（与 CLI 模式保持一致）
	skillMetrics := skills.NewMetrics()
	overlayReg.Registry.SetMetrics(skillMetrics)

	// Discovery 始终构造一份，让 Finder 启动期拉取 + 按需拉取（skill_install / self-heal）
	// 共享同一 cache。MarketplaceURLs 优先于 legacy URLs；二者合并去重后作为 discovery 的 source set。
	cacheDir := os.ExpandEnv("$HOME/.claw/cache/skills")
	marketplaces := mergeMarketplaceURLs(cfg.Agent.Skills.MarketplaceURLs, cfg.Agent.Skills.URLs)
	discovery := skills.NewDiscoveryWithMarketplaces(cacheDir, marketplaces, logger)

	finderOpts := []skills.FinderOption{
		skills.WithNestedDiscovery("."),
	}
	if len(marketplaces) > 0 {
		finderOpts = append(finderOpts, skills.WithRemoteURLs(marketplaces, discovery))
	}

	// 扫描根：默认三路径 + 配置里 PublicSkillsDir / PersonalSkillsDir 扩展。
	// PersonalSkillsDir 形如 $HIVE_DATA/skills/users；finder 内部按 userID 子目录归到 personal scope。
	roots := []string{".claude/skills", os.ExpandEnv("$HOME/.claude/skills"), "skills"}
	if d := strings.TrimSpace(cfg.Agent.Skills.PublicSkillsDir); d != "" {
		roots = append(roots, os.ExpandEnv(d))
	}
	if d := strings.TrimSpace(cfg.Agent.Skills.PersonalSkillsDir); d != "" {
		roots = append(roots, os.ExpandEnv(d))
	}
	finder := skills.NewFinder(overlayReg.Registry, logger, roots, finderOpts...)
	finder.DiscoverAndRegister()
	return overlayReg, finder, discovery
}

// mergeMarketplaceURLs 合并 MarketplaceURLs（主）+ legacy URLs（兜底），
// 去重后按原顺序返回。空串和纯空白被过滤掉。
// tasks.md 10.1 规定 MarketplaceURLs 为主字段——URLs 只为向后兼容保留。
func mergeMarketplaceURLs(primary, legacy []string) []string {
	seen := make(map[string]bool, len(primary)+len(legacy))
	out := make([]string, 0, len(primary)+len(legacy))
	add := func(list []string) {
		for _, u := range list {
			u = strings.TrimSpace(u)
			if u == "" || seen[u] {
				continue
			}
			seen[u] = true
			out = append(out, u)
		}
	}
	add(primary)
	add(legacy)
	return out
}

// initAdminChecker 根据认证引擎是否挂载选择 AdminChecker（与 HTTP authEngine 语义一致）。
func initAdminChecker(authEngine *auth.Engine) skills.AdminChecker {
	if authEngine != nil {
		return NewAuthAdminChecker()
	}
	return skills.NewDenyAllAdminChecker()
}

// initSpecSkillResolver 构造本地 Registry + 远程 Discovery 的语义路由聚合入口。
// tasks.md 4.3 + 11.6：remoteAllow 闭包组合 OnDemandEnabled && SkillsSemanticRouting，
// flag 关时 resolver 绝不调远程；SubAgent 派生 (D16) 直接复用同一实例。
func initSpecSkillResolver(cfg *config.Config, reg *skills.OverlayRegistry, disc *skills.Discovery) skills.SpecSkillResolver {
	remoteAllow := func() bool {
		return cfg.Agent.Skills.OnDemandEnabled && cfg.SpecDriven.SkillsSemanticRouting
	}
	return skills.NewSpecSkillResolver(reg, disc, remoteAllow)
}

func initMCPHost(_ *config.Config, logger *zap.Logger) (*mcphost.Host, []*mcphost.RemoteMCPClient) {
	mcpHost := mcphost.NewHost(logger)
	mcphost.RegisterBuiltinResources(mcpHost, logger)
	mcphost.RegisterBuiltinPrompts(mcpHost, logger)
	// 外部 MCP 服务端连接由 connectMCPServers 在 DB 配置加载后执行
	return mcpHost, nil
}

func registerIMAPIService(sc *ServerComponents, cfg *config.Config, logger *zap.Logger, service *imcore.Service) {
	if sc == nil || sc.MCPHost == nil || cfg == nil || !cfg.Agent.IMAPI.Enabled {
		return
	}
	if service == nil {
		service = imcore.NewService()
	}
	if sc.ChannelRouter != nil {
		if sc.DB != nil {
			service.Register(imcore.NewWechatBotAdapter(sc.DB, sc.ChannelRouter))
		}
		service.Register(imcore.NewSendOnlyAdapter(imcore.PlatformWeCom, sc.ChannelRouter))
		service.Register(imcore.NewSendOnlyAdapter(imcore.PlatformDingTalk, sc.ChannelRouter))
	}
	var metricsWriter observability.MetricsWriter
	if sc.ChannelRouter != nil {
		metricsWriter = sc.ChannelRouter.MetricsWriter()
	}
	if metricsWriter == nil {
		if pgStore, ok := sc.DB.(*store.PostgresStore); ok && pgStore != nil {
			metricsWriter = observability.NewPgMetricsWriter(pgStore.Pool(), logger)
		}
	}
	tools.RegisterIMAPIToolWithOptions(sc.MCPHost, logger, service, tools.IMAPIToolOptions{
		ForceDryRun:   cfg.Agent.IMAPI.ForceDryRun,
		MetricsWriter: metricsWriter,
	})
	logger.Info("im_api 统一 IM 工具已注册",
		zap.Bool("force_dry_run", cfg.Agent.IMAPI.ForceDryRun),
		zap.Bool("preferred_over_legacy", cfg.Agent.IMAPI.PreferredOverLegacy))
}

// connectMCPServers 根据 cfg.MCP.Servers 并行连接所有外部 MCP 服务端（在 DB 配置加载后调用）
func connectMCPServers(cfg *config.Config, mcpHost *mcphost.Host, logger *zap.Logger) []*mcphost.RemoteMCPClient {
	const maxRetries = 3

	type result struct {
		client *mcphost.RemoteMCPClient
	}

	resultCh := make(chan result, len(cfg.MCP.Servers))
	var wg sync.WaitGroup

	for name, serverCfg := range cfg.MCP.Servers {
		wg.Add(1)
		go func(name string, serverCfg config.MCPServerConfig) {
			defer wg.Done()

			spec := mcphost.MCPServerSpec{
				Name:      name,
				Command:   serverCfg.Command,
				Args:      serverCfg.Args,
				Env:       serverCfg.Env,
				Transport: serverCfg.Transport,
				URL:       serverCfg.URL,
				Headers:   serverCfg.Headers,
			}
			if serverCfg.Timeout != "" {
				if d, err := time.ParseDuration(serverCfg.Timeout); err == nil {
					spec.Timeout = d
				}
			}
			if serverCfg.OAuth != nil {
				spec.OAuth = &mcphost.OAuthConfig{
					ClientID:     serverCfg.OAuth.ClientID,
					ClientSecret: serverCfg.OAuth.ClientSecret,
					AuthURL:      serverCfg.OAuth.AuthURL,
					TokenURL:     serverCfg.OAuth.TokenURL,
					Scopes:       serverCfg.OAuth.Scopes,
				}
			}

			var lastErr error
			for attempt := 1; attempt <= maxRetries; attempt++ {
				transport, err := mcphost.BuildTransport(spec, nil, logger)
				if err != nil {
					lastErr = err
					if attempt < maxRetries {
						logger.Warn("创建 MCP 传输失败，重试中",
							zap.String("服务端", name),
							zap.Int("attempt", attempt),
							zap.Error(err))
						time.Sleep(time.Duration(attempt) * time.Second)
					}
					continue
				}

				c, err := mcphost.ConnectRemoteMCP(context.Background(), transport, mcpHost, name, logger)
				if err != nil {
					lastErr = err
					if attempt < maxRetries {
						logger.Warn("连接远程 MCP 服务端失败，重试中",
							zap.String("服务端", name),
							zap.Int("attempt", attempt),
							zap.Error(err))
						time.Sleep(time.Duration(attempt) * time.Second)
					}
					continue
				}

				resultCh <- result{client: c}
				return
			}

			logger.Error("MCP 服务端连接最终失败",
				zap.String("服务端", name),
				zap.Int("retries", maxRetries),
				zap.Error(lastErr))
		}(name, serverCfg)
	}

	wg.Wait()
	close(resultCh)

	var clients []*mcphost.RemoteMCPClient
	for r := range resultCh {
		clients = append(clients, r.client)
	}
	return clients
}

func initPlugins(cfg *config.Config, logger *zap.Logger) *plugin.Manager {
	if !cfg.Plugin.Enabled {
		return nil
	}
	pluginDir := cfg.Plugin.Dir
	if pluginDir == "" {
		home, _ := os.UserHomeDir()
		pluginDir = filepath.Join(home, ".claw", "plugins")
	}

	mgr := plugin.NewManager(logger)
	if err := mgr.LoadFromDir(pluginDir, plugin.PluginInput{}); err != nil {
		logger.Error("加载插件失败", zap.Error(err))
	} else {
		logger.Info("插件系统已启用", zap.String("dir", pluginDir))
	}
	return mgr
}

func initStore(cfg *config.Config, logger *zap.Logger) (store.SessionStore, store.Store) {
	pgCfg := store.PostgresConfig{
		DSN:      cfg.Store.Postgres.DSN,
		Host:     cfg.Store.Postgres.Host,
		Port:     cfg.Store.Postgres.Port,
		Database: cfg.Store.Postgres.Database,
		User:     cfg.Store.Postgres.User,
		Password: cfg.Store.Postgres.Password,
		SSLMode:  cfg.Store.Postgres.SSLMode,
		MaxConns: cfg.Store.Postgres.MaxConns,
	}
	pgStore, err := store.NewPostgresStore(context.Background(), pgCfg, logger)
	if err != nil {
		logger.Fatal("PostgreSQL 存储初始化失败", zap.Error(err))
	}
	logger.Info("PostgreSQL 存储已初始化")

	return pgStore, pgStore
}

func seedLLMConfig(cfg *config.Config, db store.Store, logger *zap.Logger) {
	seedCfg := store.SeedLLMConfig{
		Provider: store.SeedLLMProvider{
			Name:         cfg.LLM.Provider,
			ProviderType: cfg.LLM.Provider,
			APIKey:       cfg.LLM.APIKey,
			BaseURL:      cfg.LLM.BaseURL,
			ExtraConfig:  BuildLLMExtraConfig(cfg),
		},
		DefaultModel: cfg.LLM.Model,
	}
	for _, mp := range cfg.LLM.Models {
		seedCfg.Models = append(seedCfg.Models, store.SeedLLMModel{
			Name:         mp.Name,
			ProviderName: mp.Provider,
			Model:        mp.Model,
			BaseURL:      mp.BaseURL,
			APIKey:       mp.APIKey,
		})
	}
	if err := store.SeedLLMFromConfig(context.Background(), db, seedCfg, logger); err != nil {
		logger.Warn("LLM 配置种子失败", zap.Error(err))
	}
}

func registerSubAgents(sc *ServerComponents, cfg *config.Config, toolBridge *skills.ToolBridge, logger *zap.Logger) {
	permMgr := sc.Master.GetPermissionManager()

	agentCallbacks := subagent.AgentCallbacks{
		ProgressFn:    sc.Master.CreateAgentProgressCallback(),
		StreamFn:      sc.Master.CreateAgentStreamCallback(),
		LLMCompleteFn: sc.Master.BuildLLMCompleteCallback(), // 成本追踪（initCostTracker 已在此之前执行）
	}

	// 构建动态 LLM resolver map（通过 AIRouter 按 task-type 选路）
	var resolvers map[string]subagent.LLMClientResolver
	if sc.AIRouter != nil {
		router := sc.AIRouter
		resolvers = map[string]subagent.LLMClientResolver{
			"explore":    func() *llm.Client { return router.GetLLMClient(airouter.TaskAgent) },
			"codereview": func() *llm.Client { return router.GetLLMClient(airouter.TaskCodeReview) },
			"title":      func() *llm.Client { return router.GetLLMClient(airouter.TaskTitle) },
			"summary":    func() *llm.Client { return router.GetLLMClient(airouter.TaskSummary) },
			"compaction": func() *llm.Client { return router.GetLLMClient(airouter.TaskSummary) },
		}
	}

	RegisterFixedAgents(sc.Master, AgentRegistryConfig{
		SkillReg:           sc.SkillReg.Registry,
		LLMClient:          sc.LLMClient,
		LLMResolvers:       resolvers,
		ToolBridge:         toolBridge,
		PermMgr:            permMgr,
		Callbacks:          agentCallbacks,
		ContextCompression: cfg.Agent.ContextCompression,
		PromptLoader:       sc.PromptLoader,
		Logger:             logger,
	})

	// 动态 Agent 工厂也走 AIRouter 的 TaskAgent 选路
	if sc.AIRouter != nil {
		router := sc.AIRouter
		sc.Master.SetAgentFactoryLLMResolver(func() *llm.Client {
			return router.GetLLMClient(airouter.TaskAgent)
		})
	} else {
		sc.Master.SetAgentFactoryDeps(sc.LLMClient)
	}

	// 记忆系统中 compactionAgent 需要 extractor 设置，由 initMemory 处理
}
func initMemory(sc *ServerComponents, cfg *config.Config, logger *zap.Logger) {
	if !cfg.Memory.Enabled || sc.DB == nil {
		return
	}

	pgStore, ok := sc.DB.(*store.PostgresStore)
	if !ok {
		logger.Warn("记忆系统需要 PostgreSQL 存储后端")
		return
	}

	memStore, err := memory.NewPostgresMemoryStore(pgStore.Pool(), logger)
	if err != nil {
		logger.Warn("记忆存储初始化失败", zap.Error(err))
		return
	}
	memoryMetrics := memory.NewExternalMetricRecorder(memoryobs.NewWriter(observability.NewPgMetricsWriter(pgStore.Pool(), logger)))
	var metricAwareStore memory.MetricAwareStore = memStore
	metricAwareStore.SetMetrics(memoryMetrics)

	sc.MemStore = memStore

	injector := memory.NewInjectorWithConfig(memStore, memory.InjectionConfig{
		MinConfidence:     cfg.Memory.InjectMinConfidence,
		MinScore:          cfg.Memory.InjectMinScore,
		FeedbackTopK:      cfg.Memory.FeedbackTopK,
		MemoryTopK:        cfg.Memory.MemoryTopK,
		FeedbackMaxTokens: cfg.Memory.FeedbackMaxTokens,
		MemoryMaxTokens:   cfg.Memory.MemoryMaxTokens,
	}, logger)
	sc.Master.SetMemoryInjector(injector)

	// 向量搜索（可选）
	if cfg.Memory.EmbeddingEnabled && cfg.LLM.APIKey != "" {
		// 根据配置选择 VectorStore 实现
		var vecStore memory.VectorStore
		var vecLoader func(ctx context.Context, vecIdx *memory.VecIndex) (int, error)

		vsType := cfg.Memory.VectorStoreType
		if vsType == "" {
			vsType = "auto"
		}

		switch vsType {
		case "memory":
			// 强制内存模式（开发/测试用）
			vecLoader = func(ctx context.Context, vi *memory.VecIndex) (int, error) {
				return vi.LoadFromPool(ctx, pgStore.Pool())
			}
			logger.Info("强制使用 InMemoryVecStore（配置 vector_store_type=memory）")

		case "pgvector":
			// 强制 pgvector（上线后稳定态）
			if store.IsPgvectorAvailable(context.Background(), pgStore.Pool()) {
				vecStore = memory.NewPgVectorStore(pgStore.Pool(), logger)
				logger.Info("使用 PgVectorStore（配置 vector_store_type=pgvector）")
			} else {
				vecLoader = func(ctx context.Context, vi *memory.VecIndex) (int, error) {
					return vi.LoadFromPool(ctx, pgStore.Pool())
				}
				logger.Warn("配置要求 pgvector 但不可用，回退到 InMemoryVecStore")
			}

		default: // "auto"
			// 仅在 pgvector 可用且回填完成后才切换
			pgvecReady := store.IsPgvectorAvailable(context.Background(), pgStore.Pool())
			backfillDone := pgvecReady && store.IsBackfillComplete(context.Background(), pgStore.Pool())
			if pgvecReady && backfillDone {
				vecStore = memory.NewPgVectorStore(pgStore.Pool(), logger)
				logger.Info("使用 PgVectorStore（pgvector 可用且回填完成）")
			} else {
				vecLoader = func(ctx context.Context, vi *memory.VecIndex) (int, error) {
					return vi.LoadFromPool(ctx, pgStore.Pool())
				}
				if pgvecReady {
					logger.Info("pgvector 可用但回填未完成，使用 InMemoryVecStore")
				} else {
					logger.Info("pgvector 不可用，使用 InMemoryVecStore")
				}
			}
		}

		embeddingCfg := memory.EmbeddingSetupConfig{
			BaseURL:        cfg.LLM.BaseURL,
			APIKey:         cfg.LLM.APIKey,
			EmbeddingModel: cfg.Memory.EmbeddingModel,
			Provider:       cfg.LLM.Provider,
		}
		hybrid, embedder, activeVecStore, err := memory.SetupEmbedding(context.Background(), memStore, vecStore, vecLoader,
			embeddingCfg, logger)
		if err != nil {
			logger.Warn("向量搜索初始化失败", zap.Error(err))
		} else {
			hybrid.SetMetrics(memoryMetrics)
			injector.SetHybridSearcher(hybrid)
			startEmbeddingBacklogWorker(sc, memStore, pgStore.Pool(), embedder, activeVecStore, memoryMetrics, logger)
		}
	}

	// 自动提取
	if cfg.Memory.AutoExtract {
		extractor := newMemoryExtractor(memStore, cfg, logger)
		sc.Master.SetFeedbackExtractor(extractor)
		if agent, err := sc.AgentReg.Get("compaction"); err == nil {
			if ca, ok := agent.(*compaction.Agent); ok {
				ca.SetMemoryExtractor(extractor)
			}
		}
	}

	logger.Info("记忆系统已初始化",
		zap.Int("inject_max_tokens", cfg.Memory.InjectMaxTokens),
		zap.Int("inject_top_k", cfg.Memory.InjectTopK),
		zap.Float64("inject_min_confidence", cfg.Memory.InjectMinConfidence),
		zap.Float64("inject_min_score", cfg.Memory.InjectMinScore),
		zap.Int("feedback_top_k", cfg.Memory.FeedbackTopK),
		zap.Int("memory_top_k", cfg.Memory.MemoryTopK),
		zap.Int("feedback_max_tokens", cfg.Memory.FeedbackMaxTokens),
		zap.Int("memory_max_tokens", cfg.Memory.MemoryMaxTokens),
		zap.Bool("auto_extract", cfg.Memory.AutoExtract),
		zap.Bool("embedding_enabled", cfg.Memory.EmbeddingEnabled),
	)
}

func newMemoryExtractor(memStore memory.MemoryStore, cfg *config.Config, logger *zap.Logger) *memory.Extractor {
	if cfg == nil || cfg.LLM.APIKey == "" || cfg.LLM.Model == "" {
		return memory.NewExtractor(memStore, logger)
	}
	provDef := llm.LookupProvider(cfg.LLM.Provider)
	provDef.APIFormat = cfg.LLM.APIFormat
	client := llm.NewClient(llm.ClientConfig{
		APIKey:          cfg.LLM.APIKey,
		BaseURL:         cfg.LLM.BaseURL,
		Model:           cfg.LLM.Model,
		DisableJSONMode: cfg.LLM.DisableJSONMode,
		Provider:        provDef,
		ReasoningEffort: cfg.LLM.ReasoningEffort,
		StorePrivacy:    cfg.LLM.StorePrivacy,
		PromptCacheKey:  cfg.LLM.PromptCacheKeyEnabled,
		ServiceTier:     cfg.LLM.InteractiveServiceTier,
	}, logger)
	return memory.NewExtractorWithStructured(memStore, memory.JSONStructuredExtractor{
		Generate: func(ctx context.Context, prompt string) (string, error) {
			resp, err := client.Chat(ctx, llm.ChatRequest{
				SystemPrompt: "Extract durable memory facts from the user/session text. Return only a JSON array of objects with type, content, confidence, and evidence. Valid type values are user, project, reference, feedback. Do not include ordinary transient task facts unless they affect future behavior.",
				Messages: []llm.Message{
					{Role: "user", Content: llm.NewTextContent(prompt)},
				},
				Temperature: 0,
				MaxTokens:   800,
				JSONMode:    true,
			})
			if err != nil {
				return "", err
			}
			return resp.Content, nil
		},
		MaxRunes: 12000,
	}, logger)
}

type embeddingBacklogProcessor interface {
	ProcessOne(ctx context.Context) (bool, error)
}

func startEmbeddingBacklogWorker(
	sc *ServerComponents,
	memStore *memory.PostgresMemoryStore,
	pool *pgxpool.Pool,
	embedder memory.EmbeddingProvider,
	vecStore memory.VectorStore,
	metrics memory.MetricRecorder,
	logger *zap.Logger,
) {
	if sc == nil || memStore == nil || pool == nil || embedder == nil {
		return
	}
	if vecStore != nil {
		memStore.SetEmbedding(embedder, vecStore)
	}
	backlog := memory.NewPGEmbeddingBacklog(pool)
	worker := memory.NewEmbeddingBacklogWorker(backlog, embedder, memStore, memory.EmbeddingBacklogWorkerOptions{
		WorkerID:    "server-memory-embedding",
		VectorSpace: memory.DefaultVectorSpaceName,
		Metrics:     metrics,
	})
	ctx, cancel := context.WithCancel(context.Background())
	sc.embeddingBacklogCancel = cancel
	go runEmbeddingBacklogWorker(ctx, worker, logger, 5*time.Second)
	logger.Info("memory embedding backlog worker 已启动")
}

func runEmbeddingBacklogWorker(ctx context.Context, worker embeddingBacklogProcessor, logger *zap.Logger, idleDelay time.Duration) {
	if worker == nil {
		return
	}
	if idleDelay <= 0 {
		idleDelay = 5 * time.Second
	}
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		drainEmbeddingBacklogWorker(ctx, worker, logger)
		timer := time.NewTimer(idleDelay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func drainEmbeddingBacklogWorker(ctx context.Context, worker embeddingBacklogProcessor, logger *zap.Logger) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		processed, err := worker.ProcessOne(ctx)
		if err != nil && logger != nil {
			logger.Warn("memory embedding backlog job 处理失败", zap.Error(err))
		}
		if !processed {
			return
		}
	}
}

func initJournal(sc *ServerComponents, cfg *config.Config, logger *zap.Logger) {
	if sc.DB == nil {
		return
	}
	pgStore, ok := sc.DB.(*store.PostgresStore)
	if !ok {
		return
	}
	// 使用 30s 超时，避免 DDL 权限不足或 DB 慢时无限阻塞启动（#13）
	initCtx, initCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer initCancel()
	j, err := journal.NewPGJournal(initCtx, pgStore.Pool(), logger)
	if err != nil {
		logger.Error("Journal 日志初始化失败，该功能已禁用", zap.Error(err))
		return
	}
	sc.Master.SetJournal(j)
	logger.Info("Journal 日志系统已初始化")
}

func initCostTracker(sc *ServerComponents, logger *zap.Logger) {
	if sc.DB == nil {
		return
	}
	pgStore, ok := sc.DB.(*store.PostgresStore)
	if !ok {
		return
	}
	ct := accounting.NewPgTracker(pgStore.Pool(), logger)
	sc.Master.SetCostTracker(ct)

	// 启动 daily cleanup ticker，清理 90 天前的 usage_records
	cleanupCtx, cleanupCancel := context.WithCancel(context.Background())
	sc.cleanupCancel = cleanupCancel
	go func() {
		ticker := time.NewTicker(24 * time.Hour)
		defer ticker.Stop()

		// 启动时立即执行一次清理
		if deleted, err := ct.Cleanup(cleanupCtx, 90); err != nil {
			logger.Warn("usage_records 清理失败", zap.Error(err))
		} else if deleted > 0 {
			logger.Info("usage_records 清理完成", zap.Int64("deleted", deleted))
		}

		for {
			select {
			case <-ticker.C:
				if deleted, err := ct.Cleanup(cleanupCtx, 90); err != nil {
					logger.Warn("usage_records 定时清理失败", zap.Error(err))
				} else if deleted > 0 {
					logger.Info("usage_records 定时清理完成", zap.Int64("deleted", deleted))
				}
			case <-cleanupCtx.Done():
				return
			}
		}
	}()

	logger.Info("CostTracker 成本追踪已初始化（含 daily cleanup）")
}

func initTaskBoard(sc *ServerComponents, _ *config.Config, logger *zap.Logger) {
	if sc.DB == nil {
		// 无 DB 时使用内存实现
		sc.TaskBoard = taskboard.NewInMemoryTaskBoard()
		logger.Info("TaskBoard 已初始化（内存模式）")
		return
	}
	pgStore, ok := sc.DB.(*store.PostgresStore)
	if !ok {
		sc.TaskBoard = taskboard.NewInMemoryTaskBoard()
		logger.Warn("TaskBoard 需要 PostgreSQL 存储后端，回退到内存模式")
		return
	}
	initCtx, initCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer initCancel()
	tb, err := taskboard.NewPGTaskBoard(initCtx, pgStore.Pool(), logger)
	if err != nil {
		logger.Error("TaskBoard 初始化失败，回退到内存模式", zap.Error(err))
		sc.TaskBoard = taskboard.NewInMemoryTaskBoard()
		return
	}
	sc.TaskBoard = tb
	logger.Info("TaskBoard 已初始化（PostgreSQL 模式）")
}

func initSessionTodoStore(ctx context.Context, _ *config.Config, pgPool *pgxpool.Pool, logger *zap.Logger) sessiontodo.Store {
	if pgPool == nil {
		logger.Warn("Plan Runtime 已启用，但 PostgreSQL pool 不可用，session todos 回退到内存模式")
		return sessiontodo.NewMemoryStore()
	}
	initCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	st, err := sessiontodo.NewPGStore(initCtx, pgPool)
	if err != nil {
		logger.Error("session todo store 初始化失败，回退到内存模式", zap.Error(err))
		return sessiontodo.NewMemoryStore()
	}
	logger.Info("Plan Runtime session todo store 已初始化（PostgreSQL 模式）")
	return st
}

func initObservability(sc *ServerComponents, logger *zap.Logger) {
	if sc.DB == nil {
		return
	}
	pgStore, ok := sc.DB.(*store.PostgresStore)
	if !ok {
		return
	}
	pool := pgStore.Pool()
	sc.Master.SetTracer(observability.NewPgTracer(pool, logger))
	sc.Master.SetMetricsWriter(observability.NewPgMetricsWriter(pool, logger))
	sc.Master.SetLogWriter(observability.NewPgLogWriter(pool, logger))
	if sc.ChannelRouter != nil {
		sc.ChannelRouter.SetMetricsWriter(observability.NewPgMetricsWriter(pool, logger))
	}
	logger.Info("Observability 可观测性已初始化（PostgreSQL 模式）")
}

func initACPAgents(m *master.Master, cfg *config.Config, logger *zap.Logger) *acpclient.ACPClientPool {
	pool := acpclient.NewPoolWithObserver(logger, m)
	for _, raCfg := range cfg.RemoteAgents {
		if !raCfg.Enabled {
			continue
		}
		agent, err := pool.Connect(context.Background(), raCfg)
		if err != nil {
			logger.Warn("连接远程 ACP Agent 失败",
				zap.String("name", raCfg.Name), zap.Error(err))
			continue
		}
		if err := m.RegisterAgent(agent); err != nil {
			logger.Warn("注册远程 ACP Agent 失败",
				zap.String("name", raCfg.Name), zap.Error(err))
			continue
		}
		logger.Info("远程 ACP Agent 已注册",
			zap.String("name", raCfg.Name),
			zap.String("transport", raCfg.Transport))
	}
	return pool
}

func initChannels(sc *ServerComponents, cfg *config.Config, logger *zap.Logger) *channel.Router {
	var channelRouter *channel.Router

	// 即使当前没有 channel 启用，也创建 router，使 channel.reload 热重载可正常工作
	if cfg.ControlPlane.Enabled {
		cp, err := controlplane.New(sc.Master, controlplane.Config{
			MaxSessions:  cfg.ControlPlane.MaxSessions,
			RateLimit:    cfg.ControlPlane.RateLimit,
			RateBurst:    cfg.ControlPlane.RateBurst,
			BindingsFile: cfg.ControlPlane.BindingsFile,
		}, logger)
		if err != nil {
			logger.Fatal("初始化控制平面失败", zap.Error(err))
		}
		logger.Info("控制平面已启用", zap.Int("max_sessions", cfg.ControlPlane.MaxSessions))
		channelRouter = channel.NewRouter(cp, logger)
	} else {
		channelRouter = channel.NewRouter(sc.Master, logger)
	}
	if pgStore, ok := sc.DB.(*store.PostgresStore); ok && pgStore != nil {
		channelRouter.SetMetricsWriter(observability.NewPgMetricsWriter(pgStore.Pool(), logger))
	}
	if sc.KBService != nil {
		channelRouter.SetKBService(sc.KBService)
		channelRouter.SetKBSessionDomainUpdater(func(sessionID, domainID string, enabled bool) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := sc.Master.SetSessionKBDomain(ctx, sessionID, domainID, enabled); err != nil {
				logger.Warn("IM KB 会话 domain 持久化失败",
					zap.String("session_id", sessionID),
					zap.String("domain_id", domainID),
					zap.Bool("enabled", enabled),
					zap.Error(err))
			}
		})
	}

	// P0-#7 & P0-#8：注入 RetryQueue 和 EventClaimer。
	// 如果配置了 DB，优先使用 Postgres 分布式实现；否则降级到内存版。
	var retryQueue channel.RetryQueue
	var eventClaimer master.EventClaimer
	var feishuRetryWorker *feishu.RetryQueueWorker
	var pushService *pushsvc.Service
	var feishuMetricsWriter observability.MetricsWriter

	if pgStore, ok := sc.DB.(*store.PostgresStore); ok && pgStore != nil {
		feishuMetricsWriter = observability.NewPgMetricsWriter(pgStore.Pool(), logger)
		pool := pgStore.Pool()
		logger.Info("DB 已连接，装配飞书 Postgres 版去重和重试队列")
		retryQueue = feishu.NewPostgresRetryQueue(pool, logger)
		eventClaimer = feishu.NewPostgresEventClaimer(pool, logger)

		// 启动飞书专属的 Postgres stale claim 清理 worker
		feishuReclaim := feishu.NewReclaimWorker(pool, logger)
		feishuReclaim.Start()

		feishuRetryWorker = feishu.NewRetryQueueWorker(pool, logger).
			WithMetricsWriter(feishuMetricsWriter)
	} else {
		logger.Info("DB 未连接或类型不支持，降级为内存版去重和重试队列")
		retryQueue = channel.NewMemoryRetryQueue(0, logger)
		eventClaimer = master.NewMemoryEventClaimer(0, logger)

		reclaimWorker := master.NewReclaimWorker(eventClaimer, 0, func(tok master.ClaimToken) {
			retryQueue.Enqueue(channel.RetryItem{
				EventID:  tok.EventID,
				Platform: string(channel.PlatformFeishu),
				Reason:   channel.RetryReasonClaimLost,
				ErrorMsg: "lease expired, reclaimed by worker",
			})
		}, logger)
		reclaimWorker.Start()
	}

	channelRouter.SetRetryQueue(retryQueue)
	channelRouter.SetEventClaimer(eventClaimer)
	if cfg.Channel.Feishu.Push.Enabled {
		pushService = pushsvc.NewService(channelRouter, pushsvc.Config{
			Enabled:          true,
			PerChatPerMinute: cfg.Channel.Feishu.PushPerChatPerMinuteResolved(),
			IdempotencyTTL:   cfg.Channel.Feishu.PushIdempotencyTTLResolved(),
		}, logger)
	}
	sc.PushService = pushService
	if sc.Master != nil {
		if pushService != nil {
			sc.Master.SetScheduledPromptDispatcher(pushService.DispatchScheduledPrompt)
			sc.Master.SetScheduledTaskPushService(pushService)
		}
		if sc.AuthEngine != nil {
			sc.Master.SetScheduledTaskUserResolver(sc.AuthEngine)
		}
		if sc.DB != nil {
			restoreScheduledTasksAsync(context.Background(), sc.Master, sc.DB, logger)
		}
	}

	// Section 8：装配 EventRenderer 订阅链路。
	// - EventBusSubscriber：无论 CP 启用与否都走 master——流事件源头在 master，CP 只负责资源/会话治理。
	// - RendererEnabled：平台级开关。当前仅 feishu 支持 EventRenderer，其他平台回落到 Plugin.Send（一次性文本）。
	//   读 `cfg.Channel.Feishu.RendererEnabled()` —— 语义为 `!Renderer.Disabled`，零值默认启用。
	if sc.Master != nil {
		channelRouter.SetEventBusSubscriber(sc.Master)
	}
	channelRouter.SetRendererEnabled(BuildRendererEnabledFn(cfg))
	logger.Info("Channel Router EventRenderer 已装配",
		zap.Bool("event_bus_subscriber", sc.Master != nil),
		zap.Bool("feishu_renderer_enabled", cfg.Channel.Feishu.RendererEnabled()),
		zap.Bool("wechatbot_renderer_enabled", cfg.Channel.WeChatBot.Enabled))

	// 注入用户上下文丰富器（auth 启用时，IM 消息可关联已注册用户）
	if sc.AuthEngine != nil {
		authEngine := sc.AuthEngine
		channelRouter.SetContextEnricher(func(ctx context.Context, externalID, provider string) context.Context {
			if provider == "feishu" {
				if imValue, ok := channel.IMContextFrom(ctx); ok && imValue.ChatType == channel.ChatGroup && !cfg.Channel.Feishu.GroupEnrichEnabledResolved() {
					return ctx
				}
			}
			// provider 是 platformToProvider 返回的 type（feishu/dingtalk），
			// 需要按 provider_type 查找，而非 provider name
			user, err := authEngine.GetUserByExternalIDAndProviderType(ctx, externalID, provider)
			if err != nil || user == nil {
				return ctx // 未找到用户，返回原 ctx，不阻塞消息处理
			}
			// 只关联 active 用户，disabled 用户不注入 context
			if user.Status != "active" {
				return ctx
			}
			return auth.WithUser(ctx, user)
		})
		channelRouter.SetOwnerUserResolver(authEngine.GetUserByIDCached)
		logger.Info("IM 用户关联已启用（ContextEnricher / OwnerUserResolver 已注入）")
	}

	// 注册各 IM 插件
	if pgStore, ok := sc.DB.(*store.PostgresStore); ok && pgStore != nil {
		sc.FeishuChatStateRepo = feishu.NewPostgresChatStateRepo(pgStore.Pool(), logger)
	}
	if cfg.Channel.DingTalk.Enabled {
		dtPlugin := dingtalk.New(cfg.Channel.DingTalk, channelRouter, logger)
		channelRouter.RegisterPlugin(dtPlugin)
		logger.Info("钉钉 Channel 插件已注册")
	}
	if cfg.Channel.Feishu.Enabled {
		fsPlugin, err := buildFeishuPlugin(
			cfg.Channel.Feishu,
			channelRouter,
			sc.Master.GetHITLBroker(),
			buildFeishuGovernance(cfg.Channel.Feishu, sc.FeishuChatStateRepo, sc.Master, nil, feishu.NewJSONLAuditSink(""), logger),
			nil,
			logger,
		)
		if err != nil {
			logger.Fatal("初始化飞书 Channel 插件失败", zap.Error(err))
		}
		if lifecycleHandler := buildFeishuLifecycleHandler(sc.FeishuChatStateRepo, sc.Master, nil, fsPlugin, retryQueue, logger); lifecycleHandler != nil {
			fsPlugin = fsPlugin.WithLifecycleHandler(lifecycleHandler)
		}
		fsPlugin = fsPlugin.WithChatStateRepo(sc.FeishuChatStateRepo)
		fsPlugin = fsPlugin.WithReliabilityLeaderGate(feishu.NewReliabilityLeaderGateFromChatStateRepo(sc.FeishuChatStateRepo, logger))
		fsPlugin = fsPlugin.WithGovernance(buildFeishuGovernance(cfg.Channel.Feishu, sc.FeishuChatStateRepo, sc.Master, fsPlugin.Client(), feishu.NewJSONLAuditSink(""), logger))
		_ = fsPlugin.Client().BotOpenID()
		if cfg.Channel.Feishu.InboundContextResolverEnabled() {
			channelRouter.SetInboundContextResolver(channel.PlatformFeishu,
				feishu.NewContextResolver(fsPlugin.Client(), logger).
					WithIdentityConfig(cfg.Channel.Feishu.Identity).
					WithRegion(cfg.Channel.Feishu.Region).
					WithNameLocale(cfg.Channel.Feishu.IdentityNameLocaleResolved()))
		} else {
			channelRouter.SetInboundContextResolver(channel.PlatformFeishu, nil)
		}
		if feishuMetricsWriter != nil {
			fsPlugin.SetMetricsWriter(feishuMetricsWriter)
		}
		channelRouter.RegisterPlugin(fsPlugin)
		if err := fsPlugin.Start(); err != nil {
			logger.Fatal("飞书长连接启动失败", zap.Error(err))
		}
		if feishuRetryWorker != nil {
			welcomeRetry := feishu.NewWelcomeRetryHandler(feishu.NewBotAddedWelcomeSender(fsPlugin.Client(), logger))
			pushRetry := pushsvc.NewRetryHandler(pushService)
			feishuRetryWorker.
				WithHandledReasons(channel.RetryReasonWelcomeSend, channel.RetryReasonPushSend).
				WithHandler(func(ctx context.Context, item channel.RetryItem) error {
					switch item.Reason {
					case channel.RetryReasonWelcomeSend:
						return welcomeRetry(ctx, item)
					case channel.RetryReasonPushSend:
						return pushRetry(ctx, item)
					default:
						return nil
					}
				}).
				Start()
		}
		logger.Info("飞书 Channel 插件已注册",
			zap.String("ingress_mode", string(cfg.Channel.Feishu.ResolvedIngressMode())),
			zap.Bool("longconn_enabled", cfg.Channel.Feishu.LongconnEnabledResolved()),
			zap.Bool("gap_fetch_enabled", cfg.Channel.Feishu.GapFetchEnabledResolved()),
			zap.Duration("heartbeat_stale_window", cfg.Channel.Feishu.HeartbeatStaleWindowResolved()),
			zap.Duration("gap_fetch_max_window", cfg.Channel.Feishu.GapFetchMaxWindowResolved()),
			zap.String("ack_emoji", cfg.Channel.Feishu.AckEmoji),
			zap.Bool("renderer_enabled", cfg.Channel.Feishu.RendererEnabled()),
			zap.Int("throttle_ms", cfg.Channel.Feishu.Renderer.ThrottleMs),
			zap.Bool("context_resolver_enabled", cfg.Channel.Feishu.InboundContextResolverEnabled()))
	}
	if cfg.Channel.WeCom.Enabled {
		wcPlugin := wecom.New(cfg.Channel.WeCom, channelRouter, logger)
		channelRouter.RegisterPlugin(wcPlugin)
		logger.Info("企业微信 Channel 插件已注册")
	}
	wbCfg := wechatbot.ConfigFromApp(cfg.Channel.WeChatBot, cfg.SessionsDir)
	registry := wechatbot.NewRegistry(wbCfg, channelRouter, sc.DB, logger)
	plugin := wechatbot.NewPlugin(registry, logger).WithInputCoordinator(sc.Master)
	if pgStore, ok := sc.DB.(*store.PostgresStore); ok && pgStore != nil {
		plugin.SetMetricsWriter(observability.NewPgMetricsWriter(pgStore.Pool(), logger))
	}
	channelRouter.RegisterPlugin(plugin)
	sc.WeChatBotService = wechatbot.NewService(registry, sc.DB)
	logger.Info("官方 wechatbot Channel 插件已注册", zap.Bool("enabled", cfg.Channel.WeChatBot.Enabled))
	return channelRouter
}

func initGateway(sc *ServerComponents, cfg *config.Config, configPath string, logger *zap.Logger) *gateway.Gateway {
	if !cfg.Gateway.Enabled {
		return nil
	}

	authMgr := gateway.NewAuthManager(cfg.Gateway.Tokens)
	gw := gateway.New(authMgr, logger)
	gateway.RegisterAllMethods(gw, gateway.Deps{
		Master:        sc.Master,
		SkillRegistry: sc.SkillReg,
		ChannelRouter: sc.ChannelRouter,
		PluginLoader:  sc.PluginMgr,
		MCPHost:       sc.MCPHost,
		ACPClientPool: sc.ACPPool,
		Config:        cfg,
		ConfigMu:      &sc.ConfigMu,
		ConfigPath:    configPath,
		Store:         sc.DB,
		AIRouter:      sc.AIRouter,
		ReloadChannelFunc: BuildReloadChannelFuncWithStore(
			cfg,
			sc.ChannelRouter,
			sc.DB,
			sc.Master.GetHITLBroker(),
			sc.FeishuChatStateRepo,
			sc.Master,
			nil,
			sc.FeishuIngressBridge.Get,
			sc.FeishuIngressBridge.Set,
			sc.FeishuIngressBridge.GetGate,
			sc.FeishuIngressBridge.SetGate,
			&sc.ConfigMu,
			logger,
			sc.PushService,
		),
		ReloadMCPFunc:    BuildReloadMCPFunc(cfg, sc.MCPHost, &sc.MCPClients, &sc.MCPClientsMu, &sc.ConfigMu, logger),
		ReloadConfigFunc: BuildReloadConfigFunc(cfg, sc.DB, logger),
	})
	logger.Info("Gateway 网关已启用")
	return gw
}

// initExecutor 根据配置创建沙箱执行器。
// Server 模式：Docker 不可用时 fail closed（拒绝启动）。
// CLI 模式由 cli/app.go 单独处理降级逻辑。
func initExecutor(cfg *config.Config, logger *zap.Logger) sandbox.Executor {
	var inner sandbox.Executor

	if !cfg.Sandbox.Enabled {
		shell, err := tools.NewPersistentShell()
		if err != nil {
			logger.Fatal("创建 PersistentShell 失败", zap.Error(err))
		}
		logger.Info("沙箱未启用，使用 LocalExecutor")
		inner = sandbox.NewLocalExecutor(shell, logger)
	} else {
		switch cfg.Sandbox.Type {
		case "docker":
			if err := sandbox.CheckDockerAvailable(); err != nil {
				logger.Fatal("Docker 不可用，Server 模式拒绝启动", zap.Error(err))
			}
			workDir, _ := os.Getwd()
			dockerCfg := sandbox.DockerConfig{
				Image:           cfg.Sandbox.Docker.Image,
				CPULimit:        cfg.Sandbox.Docker.CPULimit,
				MemoryLimit:     cfg.Sandbox.Docker.MemoryLimit,
				PidsLimit:       cfg.Sandbox.Docker.PidsLimit,
				TmpfsSize:       cfg.Sandbox.Docker.TmpfsSize,
				Network:         cfg.Sandbox.Docker.Network,
				NetworkDisabled: cfg.Sandbox.Docker.NetworkDisabled,
				SeccompProfile:  cfg.Sandbox.Docker.SeccompProfile,
				ReadOnlyWorkDir: cfg.Sandbox.Docker.ReadOnlyWorkDir,
			}
			executor, err := sandbox.NewDockerExecutor(dockerCfg, workDir, logger)
			if err != nil {
				logger.Fatal("创建 DockerExecutor 失败", zap.Error(err))
			}
			logger.Info("沙箱已启用（Docker 模式）")
			inner = executor

		default: // "local" 或未指定
			shell, err := tools.NewPersistentShell()
			if err != nil {
				logger.Fatal("创建 PersistentShell 失败", zap.Error(err))
			}
			logger.Info("沙箱已启用（Local 模式）")
			inner = sandbox.NewLocalExecutor(shell, logger)
		}
	}

	// 用 SafeExecutorWrapper 包装，checker 延迟注入（master 初始化安全规则后调用 SetChecker）
	return sandbox.NewSafeExecutorWrapper(inner, nil)
}

func initAuthEngine(ctx context.Context, cfg *config.Config, pool *pgxpool.Pool, logger *zap.Logger) *auth.Engine {
	if pool == nil {
		return nil
	}
	// FrontendURL 验证：必须是合法的 HTTP/HTTPS URL
	if u := cfg.Auth.FrontendURL; u != "" &&
		!strings.HasPrefix(u, "https://") &&
		!strings.HasPrefix(u, "http://") {
		logger.Fatal("auth.frontend_url must be a valid http:// or https:// URL", zap.String("frontend_url", u))
	}
	jwtSecret, err := auth.ResolveJWTSecret(ctx, pool, cfg.Auth.JWTSecret)
	if err != nil {
		logger.Fatal("auth engine init failed: JWT secret 初始化失败", zap.Error(err))
	}
	ttl := parseDuration(cfg.Auth.JWTTTL, 24*time.Hour)
	maxTTL := parseDuration(cfg.Auth.JWTMaxTTL, 7*24*time.Hour)
	jwtMgr := auth.NewJWTManager(jwtSecret, ttl, maxTTL)
	authStore := auth.NewPGStore(pool)

	// Seed Auth Providers（UPSERT：name 冲突时更新配置，确保配置文件为 source of truth）
	for _, p := range cfg.Auth.Providers {
		cfgBytes, _ := json.Marshal(p.Config)
		err := authStore.UpsertProvider(ctx, auth.ProviderConfig{
			Name:         p.Name,
			ProviderType: p.ProviderType,
			Enabled:      p.Enabled,
			ConfigJSON:   cfgBytes,
		})
		if err != nil {
			logger.Warn("Failed to seed auth provider", zap.String("name", p.Name), zap.Error(err))
		} else {
			logger.Info("Seeded auth provider from config", zap.String("name", p.Name), zap.String("type", p.ProviderType))
		}
	}

	engine := auth.NewEngine(authStore, jwtMgr, logger)
	if err := engine.LoadProvidersFromDB(ctx); err != nil {
		logger.Fatal("auth engine init failed: 加载认证 Provider 失败", zap.Error(err))
	}
	if len(engine.ListProviders()) == 0 {
		logger.Warn("认证引擎已初始化但尚未配置 OAuth/LDAP Provider；本地账号仍可用")
	}
	if err := auth.SeedDefaultAdmin(ctx, authStore, auth.SeedDefaultAdminEnabled(cfg.Auth.SeedDefaultAdmin)); err != nil {
		logger.Warn("默认 admin 种子账号写入失败", zap.Error(err))
	}
	auth.StartCacheInvalidationListener(ctx, pool, engine, logger)
	logger.Info("认证引擎已初始化")
	return engine
}

func parseDuration(s string, defaultVal time.Duration) time.Duration {
	if s == "" {
		return defaultVal
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return defaultVal
	}
	return d
}

// initPromptLoader 初始化三层 prompt 加载器（DB > 文件 > go:embed）
func initPromptLoader(sc *ServerComponents, cfg *config.Config, pgPool *pgxpool.Pool, logger *zap.Logger, ctx context.Context) {
	var promptsDir string
	if cfg.PromptsDir != "" {
		promptsDir = cfg.PromptsDir
	}

	var ps *store.PromptStore
	var dbStore i18n.PromptStoreReader
	if pgPool != nil {
		ps = store.NewPromptStore(pgPool, logger)
		dbStore = ps
		sc.PromptStore = ps
	}

	loader := i18n.NewPromptLoader(dbStore, promptsDir, cfg.PromptLanguage, logger)
	sc.PromptLoader = loader

	// 注入到 Master（用于 buildSystemPrompt）
	sc.Master.SetPromptLoader(loader)

	// 注册跨实例缓存失效回调：任何实例通过 API 更新 prompt，都会触发所有实例的 InvalidateDBCache
	if ps != nil {
		ps.SetInvalidate(func(key string) {
			loader.InvalidateDBCache(key)
		})
		// 启动 PG NOTIFY LISTEN goroutine，监听其他实例的变更
		ps.StartNotifyListener(ctx, func(key string) {
			loader.InvalidateDBCache(key)
		})
		logger.Info("PromptLoader 跨实例缓存失效已启用（Upsert/Delete + PG NOTIFY）")
	}

	logger.Info("PromptLoader 初始化完成",
		zap.String("prompts_dir", promptsDir),
		zap.String("language", cfg.PromptLanguage),
		zap.Bool("db_enabled", dbStore != nil),
	)
}
