package cli

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"go.uber.org/zap"

	acp "github.com/coder/acp-go-sdk"

	"github.com/chef-guo/agents-hive/internal/acpserver"
	"github.com/chef-guo/agents-hive/internal/bootstrap"
	"github.com/chef-guo/agents-hive/internal/command"
	"github.com/chef-guo/agents-hive/internal/config"
	"github.com/chef-guo/agents-hive/internal/errs"
	"github.com/chef-guo/agents-hive/internal/i18n"
	"github.com/chef-guo/agents-hive/internal/journal"
	"github.com/chef-guo/agents-hive/internal/llm"
	"github.com/chef-guo/agents-hive/internal/master"
	"github.com/chef-guo/agents-hive/internal/mcphost"
	"github.com/chef-guo/agents-hive/internal/memory"
	"github.com/chef-guo/agents-hive/internal/plugin"
	"github.com/chef-guo/agents-hive/internal/sandbox"
	"github.com/chef-guo/agents-hive/internal/skills"
	"github.com/chef-guo/agents-hive/internal/store"
	"github.com/chef-guo/agents-hive/internal/subagent"
	"github.com/chef-guo/agents-hive/internal/taskboard"
	"github.com/chef-guo/agents-hive/internal/tools"
)

// App 是 CLI 应用程序
type App struct {
	master      *master.Master
	skillReg    *skills.Registry
	skillFinder *skills.Finder
	cmdRegistry *command.Registry
	logger      *zap.Logger
	config      *config.Config
	lastTaskID  string
	mcpClients  []*mcphost.RemoteMCPClient
	mcpHost     *mcphost.Host        // MCP 工具宿主（ACP 模式需要传递给会话）
	pgStore     *store.PostgresStore // PostgreSQL 存储（postgres 模式）
	memoryStore memory.MemoryStore   // 记忆存储
	executor    sandbox.Executor     // 沙箱执行器
}

// NewApp 创建新的 CLI 应用程序
func NewApp(cfg *config.Config, logger *zap.Logger) *App {
	// 0. 沙箱执行器（CLI 模式降级到 LocalExecutor 并输出警告）
	appExec := initCLIExecutor(cfg, logger)
	tools.SetExecutor(appExec)

	// 1. 技能：从文件系统自动发现
	skillReg := skills.NewRegistry(logger)
	finderOpts := []skills.FinderOption{
		skills.WithNestedDiscovery("."),
	}

	// 远程 skill 发现
	if len(cfg.Agent.Skills.URLs) > 0 {
		cacheDir := os.ExpandEnv("$HOME/.claw/cache/skills")
		discovery := skills.NewDiscovery(cacheDir, logger)
		finderOpts = append(finderOpts, skills.WithRemoteURLs(cfg.Agent.Skills.URLs, discovery))
	}

	finder := skills.NewFinder(skillReg, logger,
		[]string{".claude/skills", os.ExpandEnv("$HOME/.claude/skills"), "skills"},
		finderOpts...,
	)
	finder.DiscoverAndRegister()

	// 启动 Watcher（Phase 7: Hot-Reload），在 RunInteractive/RunACP 的 ctx 中运行
	// 注意：watcher 需要在 master.Start(ctx) 之后启动，这里先保存 finder 引用
	// 实际启动在 RunInteractive/RunACP 中通过 startWatcher 方法完成

	// 初始化统一命令注册表
	cmdReg := command.NewRegistry(logger)
	cmdReg.LoadBuiltins()
	cmdReg.LoadFromSkills(skillReg)
	cmdReg.LoadFromConfig(cfg.Commands)

	// 初始化模型注册表（在创建 LLM Client 之前，确保模型元数据可用）
	llm.InitModelRegistry(logger, cfg.LLM.ModelRegistryURL)
	refreshCtx, refreshCancel := context.WithCancel(context.Background())
	_ = refreshCancel // App 退出时由进程回收
	llm.StartModelRegistryRefresh(refreshCtx)

	// 使用内置工具初始化 MCP 宿主
	mcpHost := mcphost.NewHost(logger)

	// 注册内置系统资源（system://info, system://tools）
	mcphost.RegisterBuiltinResources(mcpHost, logger)

	// 注册内置提示模板
	mcphost.RegisterBuiltinPrompts(mcpHost, logger)

	// 连接配置中声明的远程 MCP 服务端（SSE / HTTP 传输）
	var mcpClients []*mcphost.RemoteMCPClient
	for name, serverCfg := range cfg.MCP.Servers {
		if serverCfg.Transport != "sse" && serverCfg.Transport != "http" {
			// stdio 模式暂不在此处理
			continue
		}

		spec := mcphost.MCPServerSpec{
			Name:      name,
			Command:   serverCfg.Command,
			Args:      serverCfg.Args,
			Transport: serverCfg.Transport,
			URL:       serverCfg.URL,
			Headers:   serverCfg.Headers,
		}
		if serverCfg.Timeout != "" {
			if d, err := time.ParseDuration(serverCfg.Timeout); err == nil {
				spec.Timeout = d
			} else {
				logger.Warn("MCP 服务端超时时间格式无效，使用默认值",
					zap.String("服务端", name),
					zap.String("timeout", serverCfg.Timeout),
					zap.Error(err),
				)
			}
		}

		// 转换 OAuth 配置（config.OAuthConfig → mcphost.OAuthConfig）
		if serverCfg.OAuth != nil {
			spec.OAuth = &mcphost.OAuthConfig{
				ClientID:     serverCfg.OAuth.ClientID,
				ClientSecret: serverCfg.OAuth.ClientSecret,
				AuthURL:      serverCfg.OAuth.AuthURL,
				TokenURL:     serverCfg.OAuth.TokenURL,
				Scopes:       serverCfg.OAuth.Scopes,
			}
		}

		// 创建传输层实例（OAuth 会自动注入 AuthProvider）
		transport, err := mcphost.BuildTransport(spec, nil, logger)
		if err != nil {
			logger.Warn("创建 MCP 传输失败，跳过该服务端",
				zap.String("服务端", name),
				zap.Error(err),
			)
			continue
		}

		// 连接远程 MCP 服务端并自动发现/注册工具
		client, err := mcphost.ConnectRemoteMCP(context.Background(), transport, mcpHost, name, logger)
		if err != nil {
			logger.Warn("连接远程 MCP 服务端失败，跳过",
				zap.String("服务端", name),
				zap.Error(err),
			)
			continue
		}

		logger.Info("已连接远程 MCP 服务端",
			zap.String("服务端", name),
			zap.String("传输类型", serverCfg.Transport),
			zap.String("url", serverCfg.URL),
		)

		mcpClients = append(mcpClients, client)
	}

	// 初始化插件管理器
	var pluginMgr *plugin.Manager
	if cfg.Plugin.Enabled {
		pluginMgr = plugin.NewManager(logger)
		pluginDir := cfg.Plugin.Dir
		if pluginDir == "" {
			// 使用配置常量替代硬编码路径
			pluginDir = ".claw/plugins"
		}
		if err := pluginMgr.LoadFromDir(pluginDir, plugin.PluginInput{
			WorkDir: ".", Logger: logger,
		}); err != nil {
			logger.Warn("加载插件失败", zap.Error(err))
		}
	}

	// 如果配置了 API 密钥则创建 LLM 客户端
	var llmClient *llm.Client
	if cfg.LLM.APIKey != "" {
		provDef := llm.LookupProvider(cfg.LLM.Provider)
		provDef.APIFormat = cfg.LLM.APIFormat
		llmClient = llm.NewClient(llm.ClientConfig{
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
	}

	// ===== PostgreSQL 存储初始化：会话存储 + 记忆系统 =====
	var sessionStore store.SessionStore
	var memStore memory.MemoryStore

	pgCfg := store.PostgresConfig{
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
		logger.Fatal("初始化 PostgreSQL 存储失败", zap.Error(err))
	}
	sessionStore = pgStore
	logger.Info("PostgreSQL 会话存储已初始化",
		zap.String("host", pgCfg.Host),
		zap.String("database", pgCfg.Database))

	// 记忆系统
	if cfg.Memory.Enabled {
		pgMemStore, err := memory.NewPostgresMemoryStore(pgStore.Pool(), logger)
		if err != nil {
			logger.Warn("初始化 PostgreSQL 记忆存储失败", zap.Error(err))
		} else {
			memStore = pgMemStore
			logger.Info("PostgreSQL 记忆系统已初始化")
		}
	}

	// LLM 配置种子（仅首次）
	if err := store.SeedLLMFromConfig(context.Background(), pgStore, store.SeedLLMConfig{
		Provider: store.SeedLLMProvider{
			Name:         cfg.LLM.Provider,
			ProviderType: cfg.LLM.Provider,
			APIKey:       cfg.LLM.APIKey,
			BaseURL:      cfg.LLM.BaseURL,
		},
		Models:       buildSeedModels(cfg),
		DefaultModel: cfg.LLM.Model,
	}, logger); err != nil {
		logger.Warn("LLM 配置种子失败", zap.Error(err))
	}

	// 从配置获取自定义工具目录
	tools.RegisterBuiltinTools(mcpHost, logger, cfg, nil, nil, cfg.CustomToolsDir, nil, skillReg, pluginMgr, nil, memStore)

	// 注册 TaskBoard 工具（CLI 模式使用内存实现，与 server 模式行为一致）
	tb := taskboard.NewInMemoryTaskBoard()
	tools.RegisterTaskBoard(mcpHost, logger, tb)

	// 创建 ToolBridge 并连接到技能注册表
	toolBridge := skills.NewToolBridge(mcpHost, logger)
	skillReg.SetToolBridge(toolBridge)

	// 设置 Metrics（Phase 8）
	skillMetrics := skills.NewMetrics()
	skillReg.SetMetrics(skillMetrics)
	toolBridge.SetMetrics(skillMetrics)

	// 设置插件管理器到 ToolBridge（用于 ToolExecuteBefore/After hook）
	if pluginMgr != nil {
		toolBridge.SetPluginManager(pluginMgr)
	}

	agentReg := subagent.NewRegistry(logger)

	m := master.NewMaster(master.Config{
		MaxConcurrentAgents: cfg.Agent.MaxConcurrentAgents,
		Model:               cfg.LLM.Model,
		BaseURL:             cfg.LLM.BaseURL,
		APIKey:              cfg.LLM.APIKey,
		DisableJSONMode:     cfg.LLM.DisableJSONMode,
		Provider:            cfg.LLM.Provider,
		ReasoningEffort:     cfg.LLM.ReasoningEffort,
		PromptLanguage:      cfg.PromptLanguage,
		ShellTimeout:        cfg.Agent.ShellTimeout,
		ScriptTimeout:       cfg.Agent.ScriptTimeout,
		SyncInterval:        cfg.Agent.SyncInterval,
		ContextCompression:  cfg.Agent.ContextCompression,
		PluginMgr:           pluginMgr,
		InstructionURLs:     cfg.InstructionURLs,
		StorePrivacy:        cfg.LLM.StorePrivacy,
		PromptCacheKey:      cfg.LLM.PromptCacheKeyEnabled,
		ServiceTier:         cfg.LLM.InteractiveServiceTier,
		ToolPolicy:          cfg.Agent.ToolPolicy,
		ToolRecall:          cfg.Agent.ToolRecall,
		QualityGuards:       cfg.Agent.QualityGuards,
		Reflection:          cfg.Agent.Reflection,
		ReasoningEffortAuto: cfg.Agent.ReasoningEffortAuto,
		Observability:       cfg.Agent.Observability,
	}, cfg.HITL, agentReg, skillReg, sessionStore, logger)
	m.SetValidationExecutor(appExec)

	// 设置记忆注入器（在 LLM 调用前注入相关记忆到上下文）
	if memStore != nil {
		injector := memory.NewInjector(memStore, cfg.Memory.InjectMaxTokens, cfg.Memory.InjectTopK, logger)
		m.SetMemoryInjector(injector)
		logger.Info("记忆注入器已设置")

		// 向量搜索（可选）：初始化 embedding + HybridSearcher
		if cfg.Memory.EmbeddingEnabled && cfg.LLM.APIKey != "" {
			var vecStore memory.VectorStore
			var vecLoader func(ctx context.Context, v *memory.VecIndex) (int, error)

			vsType := cfg.Memory.VectorStoreType
			if vsType == "" {
				vsType = "auto"
			}

			switch vsType {
			case "memory":
				vecLoader = func(ctx context.Context, v *memory.VecIndex) (int, error) {
					return v.LoadFromPool(ctx, pgStore.Pool())
				}
				logger.Info("强制使用 InMemoryVecStore（配置 vector_store_type=memory）")

			case "pgvector":
				if store.IsPgvectorAvailable(context.Background(), pgStore.Pool()) {
					vecStore = memory.NewPgVectorStore(pgStore.Pool(), logger)
					logger.Info("使用 PgVectorStore（配置 vector_store_type=pgvector）")
				} else {
					vecLoader = func(ctx context.Context, v *memory.VecIndex) (int, error) {
						return v.LoadFromPool(ctx, pgStore.Pool())
					}
					logger.Warn("配置要求 pgvector 但不可用，回退到 InMemoryVecStore")
				}

			default: // "auto"
				pgvecReady := store.IsPgvectorAvailable(context.Background(), pgStore.Pool())
				backfillDone := pgvecReady && store.IsBackfillComplete(context.Background(), pgStore.Pool())
				if pgvecReady && backfillDone {
					vecStore = memory.NewPgVectorStore(pgStore.Pool(), logger)
					logger.Info("使用 PgVectorStore（pgvector 可用且回填完成）")
				} else {
					vecLoader = func(ctx context.Context, v *memory.VecIndex) (int, error) {
						return v.LoadFromPool(ctx, pgStore.Pool())
					}
					if pgvecReady {
						logger.Info("pgvector 可用但回填未完成，使用 InMemoryVecStore")
					} else {
						logger.Info("pgvector 不可用，使用 InMemoryVecStore")
					}
				}
			}

			hybrid, _, _, err := memory.SetupEmbedding(context.Background(), memStore, vecStore, vecLoader,
				memory.EmbeddingSetupConfig{
					BaseURL:        cfg.LLM.BaseURL,
					APIKey:         cfg.LLM.APIKey,
					EmbeddingModel: cfg.Memory.EmbeddingModel,
					Provider:       cfg.LLM.Provider,
				}, logger)
			if err != nil {
				logger.Warn("向量搜索初始化失败", zap.Error(err))
			} else {
				injector.SetHybridSearcher(hybrid)
			}
		}
	}

	// 设置 MCP 工具宿主，使 Master 能监听工具列表变更
	m.SetMCPHost(mcpHost)

	// Journal 日志系统（30s 超时，避免 DDL 权限不足时无限阻塞）
	if pgStore != nil {
		jCtx, jCancel := context.WithTimeout(context.Background(), 30*time.Second)
		j, err := journal.NewPGJournal(jCtx, pgStore.Pool(), logger)
		jCancel()
		if err != nil {
			logger.Error("Journal 日志初始化失败，该功能已禁用", zap.Error(err))
		} else {
			m.SetJournal(j)
			logger.Info("Journal 日志系统已初始化")
		}
	}

	// 注册 question 和 task 工具（需要 Master 实例作为 Bridge/Executor）
	if cfg.HITL.Enabled {
		tools.RegisterQuestionTool(mcpHost, logger, m)
	}
	tools.RegisterTaskTool(mcpHost, logger, m)

	// 设置工具调用回调（用于事件广播）
	toolBridge.SetOnToolCall(func(toolName string, _ string) {
		m.BroadcastGenericMessage(master.EventTypeToolCall, master.ToolCallEvent{
			ToolName: toolName,
			Status:   "start",
		})
	})

	// 获取 PermissionManager
	permMgr := m.GetPermissionManager()

	// 创建 Agent 回调（进度 + 流式内容）
	agentCallbacks := subagent.AgentCallbacks{
		ProgressFn: m.CreateAgentProgressCallback(),
		StreamFn:   m.CreateAgentStreamCallback(),
	}

	// 初始化 PromptLoader（CLI 单实例模式：store=nil 直接走 embed 兜底）
	var promptStore *store.PromptStore
	if pgStore != nil {
		promptStore = store.NewPromptStore(pgStore.Pool(), logger)
	}
	promptLoader := i18n.NewPromptLoader(promptStore, "", cfg.PromptLanguage, logger)
	m.SetPromptLoader(promptLoader)

	// 注册所有固定 Agent（CLI/Server 共享入口）
	compactionAgent := bootstrap.RegisterFixedAgents(m, bootstrap.AgentRegistryConfig{
		SkillReg:           skillReg,
		LLMClient:          llmClient,
		ToolBridge:         toolBridge,
		PermMgr:            permMgr,
		Callbacks:          agentCallbacks,
		ContextCompression: cfg.Agent.ContextCompression,
		PromptLoader:       promptLoader,
		Logger:             logger,
	})

	// 设置记忆提取器（压缩摘要时自动提取记忆）
	if memStore != nil && cfg.Memory.AutoExtract {
		extractor := memory.NewExtractor(memStore, logger)
		compactionAgent.SetMemoryExtractor(extractor)
		logger.Info("记忆提取器已设置")
	}

	return &App{
		master:      m,
		skillReg:    skillReg,
		skillFinder: finder,
		cmdRegistry: cmdReg,
		logger:      logger,
		config:      cfg,
		mcpClients:  mcpClients,
		mcpHost:     mcpHost,
		pgStore:     pgStore,
		memoryStore: memStore,
		executor:    appExec,
	}
}

// Close 清理 App 资源（远程 MCP 连接、数据库连接等）
// 顺序：MCP clients → Master.Stop（保存会话） → MemStore.Close（等待 embedding workers） → DB pool → Executor
func (a *App) Close() {
	for _, c := range a.mcpClients {
		_ = c.Close()
	}
	// Master.Stop 需要 DB pool 活着（SaveAllSessions）
	if a.master != nil {
		a.master.Stop()
	}
	// MemStore.Close 等待 embedding workers 完成，需要 DB pool 活着
	if a.memoryStore != nil {
		_ = a.memoryStore.Close()
	}
	if a.pgStore != nil {
		_ = a.pgStore.Close()
	}
	if a.executor != nil {
		_ = a.executor.Close()
	}
}

// sandboxToSkillsAdapter 将 sandbox.Executor 适配为 skills.SandboxExecutor
type sandboxToSkillsAdapter struct {
	inner sandbox.Executor
}

func (ad *sandboxToSkillsAdapter) Execute(ctx context.Context, req skills.SandboxExecRequest) (skills.SandboxExecResult, error) {
	result, err := ad.inner.Execute(ctx, sandbox.ExecRequest{
		Command:   req.Command,
		SessionID: req.SessionID,
		Timeout:   req.Timeout,
		WorkDir:   req.WorkDir,
		Env:       req.Env,
	})
	if err != nil {
		return skills.SandboxExecResult{}, err
	}
	return skills.SandboxExecResult{
		Stdout:     result.Stdout,
		Stderr:     result.Stderr,
		ExitCode:   result.ExitCode,
		Diagnostic: toSkillsExecDiagnostic(result.Diagnostic),
	}, nil
}

func (ad *sandboxToSkillsAdapter) Close() error { return ad.inner.Close() }

func toSkillsExecDiagnostic(diag *sandbox.ExecFailureDiagnostic) *skills.SandboxExecDiagnostic {
	if diag == nil {
		return nil
	}
	return &skills.SandboxExecDiagnostic{
		FailureType:          diag.FailureType,
		Summary:              diag.Summary,
		RequiresUserApproval: diag.RequiresUserApproval,
		SuggestedAction:      diag.SuggestedAction,
		SuggestedEnv:         diag.SuggestedEnv,
	}
}

// skillsSandboxExecutor 返回 skills.SandboxExecutor 适配器，如果 executor 为 nil 则返回 nil
func (a *App) skillsSandboxExecutor() skills.SandboxExecutor {
	if a.executor == nil {
		return nil
	}
	return &sandboxToSkillsAdapter{inner: a.executor}
}

// RunOnce 执行单次请求并返回结果
func (a *App) RunOnce(ctx context.Context, request string) error {
	if strings.TrimSpace(request) == "" {
		return errs.New(errs.CodeInvalidInput, "请求不能为空")
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	a.master.Start(ctx)
	defer a.master.Stop()

	// 使用 ProcessMessage 的 per-request 响应模式，无需启动 SessionLoop
	// 但 SessionLoop 需要运行以处理请求
	sessionErr := make(chan error, 1)
	go func() {
		sessionErr <- a.master.SessionLoop(ctx)
	}()

	// 使用 ProcessMessage 替代直接操作 channel，自带超时保护
	resp, err := a.master.ProcessMessage(ctx, "", request)
	if err != nil {
		return err
	}

	if resp.Error != "" {
		return errs.New(errs.CodeInternal, resp.Error)
	}
	if resp.Message != "" {
		fmt.Println(resp.Message)
	}
	if resp.Content != "" {
		fmt.Println(resp.Content)
	}
	return nil
}

// RunInteractive 启动交互式 REPL 会话
func (a *App) RunInteractive(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	a.master.Start(ctx)
	defer a.master.Stop()

	// 启动 Skill Watcher（Phase 7: Hot-Reload）
	if a.skillFinder != nil {
		watcher := skills.NewWatcher(a.skillFinder, a.skillReg, 5*time.Second, a.logger)
		go watcher.Start(ctx)
	}

	// 订阅事件广播（用于显示工具调用、Agent 启动等事件）
	subID, eventCh := a.master.SubscribeWSBroadcast()
	defer a.master.UnsubscribeWSBroadcast(subID)

	// 启动事件处理 goroutine
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case msg, ok := <-eventCh:
				if !ok {
					return
				}
				// 格式化并打印事件到控制台
				formatAndPrintEvent(msg)
			}
		}
	}()

	// 启动 SessionLoop goroutine
	sessionErr := make(chan error, 1)
	go func() {
		sessionErr <- a.master.SessionLoop(ctx)
	}()

	// 确保活动模型在配置列表中
	a.config.EnsureActiveInProfiles()

	fmt.Println("agents-hive 交互模式")
	fmt.Printf("活动模型: %s\n", a.config.ActiveProfileName())
	fmt.Println("输入您的请求,或输入 'quit' / 'exit' 退出。")
	fmt.Println("使用 /model 列出模型,/model <名称> 切换模型。")
	fmt.Println("使用 /session 管理会话,/skills 列出可用技能。")
	fmt.Println()

	scanner := bufio.NewScanner(os.Stdin)

	// REPL 循环
	for {
		// 显示会话名称和 ID（截短显示）
		sessionID, sessionName := a.master.GetCurrentSessionInfo()
		if sessionName == "" {
			sessionName = "main"
		}
		shortID := sessionID
		if len(shortID) > 16 {
			shortID = shortID[:16]
		}
		fmt.Printf("claw [%s:%s]> ", sessionName, shortID)

		if !scanner.Scan() {
			break
		}

		input := strings.TrimSpace(scanner.Text())
		if input == "" {
			continue
		}

		// 检查退出命令(在发送到 SessionLoop 前处理)
		if input == "quit" || input == "exit" {
			fmt.Println("再见！")
			cancel()
			return nil
		}

		// 处理 CLI 特有的 / 命令(不发送到 SessionLoop)
		if strings.HasPrefix(input, "/") {
			if err := a.handleSlashCommand(ctx, input); err != nil {
				fmt.Printf("错误: %v\n", err)
			}
			fmt.Println()
			continue
		}

		// 发送请求到 SessionLoop
		select {
		case a.master.RequestCh() <- master.SessionRequest{Input: input}:
		case <-ctx.Done():
			return ctx.Err()
		}

		// 等待响应
		select {
		case resp := <-a.master.ResponseCh():
			if resp.Exit {
				fmt.Println("再见！")
				return nil
			}

			if resp.Message != "" {
				fmt.Println(resp.Message)
			}

			if resp.Content != "" {
				fmt.Println(resp.Content)
			}

			if resp.Error != "" {
				fmt.Fprintf(os.Stderr, "错误: %s\n", resp.Error)
			}

		case <-ctx.Done():
			return ctx.Err()
		}

		fmt.Println()
	}

	// 检查 SessionLoop 错误
	select {
	case err := <-sessionErr:
		if err != nil && err != context.Canceled {
			return err
		}
		return nil
	default:
		return scanner.Err()
	}
}

// handleSlashCommand 处理 /命令名 风格的命令
func (a *App) handleSlashCommand(ctx context.Context, input string) error {
	// 移除前导 /
	raw := input[1:]

	// 处理内置 CLI 控制命令（不走命令注册表）
	switch {
	case raw == "skills":
		return a.listSkills()
	case raw == "model":
		return a.listModels()
	case strings.HasPrefix(raw, "model "):
		name := strings.TrimSpace(raw[6:])
		return a.switchModel(name)
	case raw == "pause":
		return a.sendInteractiveCommand(master.CmdPause)
	case raw == "resume":
		return a.sendInteractiveCommand(master.CmdResume)
	case raw == "cancel":
		return a.sendInteractiveCommand(master.CmdCancel)
	case raw == "session" || strings.HasPrefix(raw, "session "):
		return a.handleSessionCommand(ctx, raw)
	}

	// 解析命令名称和参数
	parts := strings.Fields(raw)
	if len(parts) == 0 {
		return nil
	}
	cmdName := parts[0]
	args := parts[1:]

	// /help：打印所有命令列表
	if cmdName == "help" {
		return a.printHelp()
	}

	// 从命令注册表查找
	cmd, err := a.cmdRegistry.Get(cmdName)
	if err != nil {
		// 回退到直接调用技能
		return a.invokeSkillDirect(ctx, raw)
	}

	// 渲染模板并发送给 master
	message := cmd.Render(args)
	if message == "" {
		message = cmdName
	}

	req := master.SessionRequest{Input: message}
	if cmd.Model != "" {
		req.ModelOverride = cmd.Model
	}

	select {
	case a.master.RequestCh() <- req:
	case <-ctx.Done():
		return ctx.Err()
	}

	// 等待响应
	select {
	case resp := <-a.master.ResponseCh():
		if resp.Exit {
			fmt.Println("再见！")
			return nil
		}
		if resp.Message != "" {
			fmt.Println(resp.Message)
		}
		if resp.Content != "" {
			fmt.Println(resp.Content)
		}
		if resp.Error != "" {
			fmt.Fprintf(os.Stderr, "错误: %s\n", resp.Error)
		}
	case <-ctx.Done():
		return ctx.Err()
	}

	return nil
}

// invokeSkillDirect 直接调用技能（命令注册表未找到时的回退）
func (a *App) invokeSkillDirect(ctx context.Context, raw string) error {
	parts := strings.SplitN(raw, " ", 2)
	skillName := parts[0]
	var args string
	if len(parts) > 1 {
		args = parts[1]
	}

	rctx := skills.RenderContext{
		Arguments: args,
	}

	executor := &skills.DefaultShellExecutor{
		Timeout:  10 * time.Second,
		Executor: a.skillsSandboxExecutor(),
	}
	runner := skills.NewScriptRunner(30*time.Second, a.logger)
	runner.Executor = a.skillsSandboxExecutor()
	hookRunner := skills.NewHookRunner(executor, a.logger)

	result, err := a.skillReg.InvokeFull(ctx, skillName, rctx, executor, runner, hookRunner)
	if err != nil {
		return errs.Wrap(errs.CodeSkillExecFailed, "技能 "+skillName, err)
	}

	fmt.Println(result)
	return nil
}

// printHelp 打印所有可用命令列表
func (a *App) printHelp() error {
	cmds := a.cmdRegistry.List()
	fmt.Println("可用命令:")
	for _, cmd := range cmds {
		desc := cmd.Description
		if desc == "" {
			desc = "-"
		}
		fmt.Printf("  /%-20s %s (%s)\n", cmd.Name, desc, string(cmd.Source))
	}
	return nil
}

// listSkills 打印所有用户可调用的技能
func (a *App) listSkills() error {
	invocable := a.skillReg.ListUserInvocable()
	if len(invocable) == 0 {
		fmt.Println("没有可用的技能。")
		return nil
	}

	fmt.Println("可用技能:")
	for _, sm := range invocable {
		hint := ""
		if sm.ArgumentHint != "" {
			hint = " " + sm.ArgumentHint
		}
		fmt.Printf("  /%s%s — %s\n", sm.Name, hint, sm.Description)
	}
	return nil
}

// listModels 打印所有配置的模型并标记活动模型
func (a *App) listModels() error {
	profiles := a.config.LLM.Models
	if len(profiles) == 0 {
		fmt.Printf("未配置模型。活动模型: %s\n", a.config.LLM.Model)
		fmt.Println("在配置文件的 llm.models 下添加模型，例如:")
		fmt.Println(`  {"llm": {"models": [`)
		fmt.Println(`    {"name": "gpt4o", "model": "gpt-5.2", "base_url": "http://45.205.26.177:9999"},`)
		fmt.Println(`    {"name": "deepseek", "model": "deepseek-chat", "base_url": "https://api.deepseek.com/v1"}`)
		fmt.Println(`  ]}}`)
		return nil
	}

	activeName := a.config.ActiveProfileName()
	fmt.Println("可用模型:")
	for _, p := range profiles {
		marker := "  "
		if p.Name == activeName {
			marker = "* "
		}
		fmt.Printf("  %s%-12s  %s  (%s)\n", marker, p.Name, p.Model, p.BaseURL)
	}
	fmt.Println()
	fmt.Println("使用 /model <名称> 切换模型。")
	return nil
}

// switchModel 通过配置名称切换活动 LLM 模型
func (a *App) switchModel(name string) error {
	profile, ok := a.config.FindProfile(name)
	if !ok {
		// 为用户列出可用名称
		var names []string
		for _, p := range a.config.LLM.Models {
			names = append(names, p.Name)
		}
		if len(names) == 0 {
			return errs.New(errs.CodeConfigInvalid, "未配置模型；请在配置文件中添加模型")
		}
		return errs.New(errs.CodeNotFound, "未找到模型 "+name+"。可用: "+strings.Join(names, ", "))
	}

	// 确定 API 密钥: 配置特定 > 当前全局
	apiKey := profile.APIKey
	if apiKey == "" {
		apiKey = a.config.LLM.APIKey
	}

	// 更新 master (规划器)
	a.master.SwitchModel(profile.Name, profile.Model, profile.BaseURL, profile.Provider, "")

	// 更新配置以反映切换
	a.config.LLM.Model = profile.Model
	if profile.BaseURL != "" {
		a.config.LLM.BaseURL = profile.BaseURL
	}
	if profile.APIKey != "" {
		a.config.LLM.APIKey = profile.APIKey
	}

	fmt.Printf("已切换到模型: %s (%s)\n", profile.Name, profile.Model)
	return nil
}

// sendInteractiveCommand 向 master 发送用户命令以控制最后活动的任务
func (a *App) sendInteractiveCommand(cmdType master.UserCommandType) error {
	if a.lastTaskID == "" {
		return errs.New(errs.CodeTaskNotFound, "没有活动任务")
	}
	return a.master.SendCommand(master.UserCommand{
		Type:   cmdType,
		TaskID: a.lastTaskID,
	})
}

// buildSeedModels 从配置构建 LLM 种子模型列表
func buildSeedModels(cfg *config.Config) []store.SeedLLMModel {
	var models []store.SeedLLMModel
	for _, m := range cfg.LLM.Models {
		models = append(models, store.SeedLLMModel{
			Name:    m.Name,
			Model:   m.Model,
			BaseURL: m.BaseURL,
			APIKey:  m.APIKey,
		})
	}
	return models
}

// RunACP 以 ACP 协议模式运行，通过 stdin/stdout 与 IDE 客户端通信
// 安全说明：zap logger 的控制台输出已配置为写入 stderr（见 config.NewLogger），
// 不会污染 stdin/stdout 上的 NDJSON 协议通道
func (a *App) RunACP(ctx context.Context) error {
	a.logger.Info("ACP 模式启动，日志输出到 stderr")

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	a.master.Start(ctx)
	defer a.master.Stop()

	// 启动 Skill Watcher（Phase 7: Hot-Reload，与 RunInteractive 保持一致）
	if a.skillFinder != nil {
		watcher := skills.NewWatcher(a.skillFinder, a.skillReg, 5*time.Second, a.logger)
		go watcher.Start(ctx)
	}

	// 启动 SessionLoop（处理任务请求）
	sessionErrCh := make(chan error, 1)
	go func() {
		sessionErrCh <- a.master.SessionLoop(ctx)
	}()

	// 创建 ACP Agent 并连接 stdio
	agent := acpserver.NewClawAgent(a.master, a.config.ACPServer, a.logger, a.cmdRegistry, a.mcpHost)
	defer agent.CloseAllSessions() // 服务器关闭时释放所有会话级 MCP 资源
	conn := acp.NewAgentSideConnection(agent, os.Stdout, os.Stdin)
	agent.SetAgentConnection(conn)

	a.logger.Info("ACP 服务器已就绪，等待 IDE 客户端连接")

	// 阻塞直到客户端断连或 context 取消
	select {
	case <-conn.Done():
		a.logger.Info("ACP 客户端已断连")
	case <-ctx.Done():
		a.logger.Info("ACP 服务器收到关闭信号")
	}

	return nil
}

// initCLIExecutor 根据配置创建沙箱执行器。
// CLI 模式：Docker 不可用时降级到 LocalExecutor + 警告。
func initCLIExecutor(cfg *config.Config, logger *zap.Logger) sandbox.Executor {
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
				logger.Warn("Docker 不可用，降级到 LocalExecutor", zap.Error(err))
				shell, shellErr := tools.NewPersistentShell()
				if shellErr != nil {
					logger.Fatal("创建 PersistentShell 失败", zap.Error(shellErr))
				}
				inner = sandbox.NewLocalExecutor(shell, logger)
			} else {
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
			}

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
