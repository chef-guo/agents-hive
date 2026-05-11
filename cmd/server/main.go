package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"go.uber.org/zap"

	"github.com/chef-guo/agents-hive/internal/api"
	"github.com/chef-guo/agents-hive/internal/bootstrap"
	"github.com/chef-guo/agents-hive/internal/channel"
	"github.com/chef-guo/agents-hive/internal/channel/feishu"
	"github.com/chef-guo/agents-hive/internal/config"
	"github.com/chef-guo/agents-hive/internal/security"
)

func main() {
	var (
		configPath = flag.String("config", "", "配置文件路径 (JSON)")
		model      = flag.String("model", "", "LLM 模型名称")
		baseURL    = flag.String("base-url", "", "LLM API Base URL")
		apiKey     = flag.String("api-key", "", "LLM API 密钥")
		logLevel   = flag.String("log-level", "", "日志级别 (debug, info, warn, error)")
	)
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "加载配置错误: %v\n", err)
		os.Exit(1)
	}

	// CLI 标志覆盖所有配置（最高优先级）
	cfg.ApplyOverrides(*model, *baseURL, *apiKey, *logLevel)

	// Server 模式：如果 ConsoleLevel 使用默认值 "error"，改为 "info"
	// （Server 运行在后台，需要看到更多日志用于监控）
	if cfg.Logging.ConsoleLevel == "error" {
		cfg.Logging.ConsoleLevel = "info"
	}

	logger, err := cfg.NewLogger()
	if err != nil {
		fmt.Fprintf(os.Stderr, "创建日志器错误: %v\n", err)
		os.Exit(1)
	}
	defer logger.Sync()

	// 全量初始化所有组件
	sc := bootstrap.InitServer(cfg, *configPath, logger)

	// 启动 Master Agent
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	sc.Master.Start(ctx)

	// 启动 PromptLoader 文件监听（热重载外部 prompt 文件）
	if sc.PromptLoader != nil {
		sc.PromptLoader.Start(ctx)
	}

	// 启动 SessionLoop goroutine（处理会话请求）
	go func() {
		if err := sc.Master.SessionLoop(ctx); err != nil && err != context.Canceled {
			logger.Error("SessionLoop 异常退出", zap.Error(err))
		}
	}()

	// 安全执行系统（环境变量完整性校验）
	if cfg.Security.Enabled && len(cfg.Security.WatchEnvVars) > 0 {
		envValidator := security.NewEnvValidator()
		envValidator.Snapshot(cfg.Security.WatchEnvVars)
		logger.Info("环境变量完整性校验已启用",
			zap.Strings("vars", cfg.Security.WatchEnvVars))
	}

	// 启动 IM 插件
	sc.StartChannels(ctx, logger)

	// 启动 Skill Watcher（热重载）
	sc.StartSkillWatcher(ctx, logger)

	// 创建 HTTP 服务器
	server := api.NewServer(cfg.Server, cfg.HITL, cfg.WebUI, sc.Master, sc.SkillReg, cfg, *configPath, sc.ChannelRouter, sc.DB, sc.AuthEngine, logger)
	server.SetWSPingInterval(cfg.Agent.WSPingInterval)
	if sc.SessionTodoStore != nil {
		server.SetSessionTodoStore(sc.SessionTodoStore)
	}
	if sc.FeishuIngressBridge != nil {
		sc.FeishuIngressBridge.Bind(
			server.GetFeishuIngressMode,
			server.SetFeishuIngressMode,
			server.GetFeishuWebhookGateMode,
			server.SetFeishuWebhookGateMode,
		)
	}
	if ct := sc.Master.CostTracker(); ct != nil {
		server.SetCostTracker(ct)
	}
	if sc.ChannelRouter != nil {
		server.SetChannelRouter(sc.ChannelRouter)
	}
	if sc.WeChatBotService != nil {
		server.SetWeChatBotService(sc.WeChatBotService)
	}
	if sc.PushService != nil {
		server.SetPushService(sc.PushService)
	}
	if cfg.Channel.Feishu.AppID != "" && cfg.Channel.Feishu.AppSecret != "" {
		if plugin, ok := sc.ChannelRouter.GetPlugin(channel.PlatformFeishu); ok {
			if provider, ok := plugin.(interface{ Client() *feishu.Client }); ok {
				server.SetFeishuHealthClient(provider.Client())
			}
		}
	}
	if sc.Gateway != nil {
		server.SetGateway(sc.Gateway)
	}
	// 注入 Prompt 管理依赖（可选，DB 不可用时 prompt API 返回 503）
	if sc.PromptStore != nil {
		server.SetPromptStore(sc.PromptStore)
	}
	if sc.PromptLoader != nil {
		server.SetPromptLoader(sc.PromptLoader)
	}
	if sc.AIRouter != nil {
		server.SetAIRouter(sc.AIRouter)
	}
	// 注入 Skill 管理依赖（可选，DB 不可用时 skill 管理 API 返回 503）
	if sc.SkillStore != nil {
		server.SetSkillStore(sc.SkillStore)
	}
	if sc.QualityCandidateStore != nil {
		server.SetQualityCandidateStore(sc.QualityCandidateStore)
	}
	if sc.OptimizationStore != nil {
		server.SetOptimizationSuggestionStore(sc.OptimizationStore)
	}
	if sc.MemStore != nil {
		server.SetMemoryStore(sc.MemStore)
	}

	go func() {
		if err := server.Start(); err != nil && err != http.ErrServerClosed {
			logger.Fatal("服务器错误", zap.Error(err))
		}
	}()

	logger.Info("服务器已启动",
		zap.Int("port", cfg.Server.Port),
		zap.String("model", cfg.LLM.Model),
		zap.String("base_url", cfg.LLM.BaseURL),
	)

	// 等待关闭信号
	<-ctx.Done()
	logger.Info("正在关闭...")

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer shutdownCancel()

	if err := server.Shutdown(shutdownCtx); err != nil {
		logger.Error("关闭错误", zap.Error(err))
	}
	sc.Shutdown()
	logger.Info("关闭完成")
}
