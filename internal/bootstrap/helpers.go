package bootstrap

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/chef-guo/agents-hive/internal/channel"
	"github.com/chef-guo/agents-hive/internal/channel/dingtalk"
	"github.com/chef-guo/agents-hive/internal/channel/feishu"
	"github.com/chef-guo/agents-hive/internal/channel/wechatbot"
	"github.com/chef-guo/agents-hive/internal/channel/wecom"
	"github.com/chef-guo/agents-hive/internal/config"
	"github.com/chef-guo/agents-hive/internal/master"
	"github.com/chef-guo/agents-hive/internal/mcphost"
	"github.com/chef-guo/agents-hive/internal/observability"
	"github.com/chef-guo/agents-hive/internal/store"
	"github.com/chef-guo/agents-hive/internal/toolctx"
)

// hitlApprovalBridge 将 HITLBroker 适配为 tools.ApprovalBridge 接口
type hitlApprovalBridge struct {
	broker *master.HITLBroker
}

type feishuLifecyclePluginClient interface {
	Client() *feishu.Client
}

var buildFeishuPluginFn = buildFeishuPlugin

func buildFeishuGovernance(
	cfg config.FeishuConfig,
	repo feishu.ChatStateRepo,
	terminator feishu.SessionTerminator,
	groupAdminChecker feishu.GroupAdminChecker,
	auditStore feishu.AuditStore,
	logger *zap.Logger,
) *feishu.GovernanceService {
	governance := feishu.NewGovernanceService(repo, logger).
		WithTerminator(terminator).
		WithModelAllowlist(cfg.Governance.ModelAllowlist).
		WithDebugEnabled(cfg.Governance.DebugEnabled).
		WithMultiAgentEnabled(cfg.Governance.MultiAgentEnabled).
		WithAuditStore(auditStore)
	if groupAdminChecker != nil || len(cfg.Governance.CommandACL.ResetAllowlist) > 0 {
		governance = governance.WithACL(feishu.NewGroupAdminACL(groupAdminChecker, flattenUniqueStrings(cfg.Governance.CommandACL.ResetAllowlist)))
	}
	return governance
}

func flattenUniqueStrings(values map[string][]string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]struct{})
	out := make([]string, 0)
	for _, ids := range values {
		for _, id := range ids {
			if id == "" {
				continue
			}
			if _, ok := seen[id]; ok {
				continue
			}
			seen[id] = struct{}{}
			out = append(out, id)
		}
	}
	return out
}

func buildFeishuWelcomeSender(
	welcome feishu.WelcomeSender,
	clientProvider feishuLifecyclePluginClient,
	retryQueue channel.RetryQueue,
	logger *zap.Logger,
) feishu.WelcomeSender {
	if welcome != nil {
		return welcome
	}
	if clientProvider == nil {
		return nil
	}
	return feishu.NewBotAddedWelcomeSender(clientProvider.Client(), logger).WithRetryQueue(retryQueue)
}

func (h *hitlApprovalBridge) RequestApproval(ctx context.Context, toolName, description string, details map[string]string) (bool, error) {
	// 构建审批提示
	prompt := fmt.Sprintf("请求创建工具: %s\n%s", toolName, description)
	for k, v := range details {
		prompt += fmt.Sprintf("\n  %s: %s", k, v)
	}

	// 从 context 中提取 sessionID，确保审批请求只推送给对应会话的用户
	sessionID := toolctx.GetSessionID(ctx)
	taskID := "create-tool"
	if sessionID != "" {
		taskID = sessionID
	}

	req := h.broker.RequestInput(taskID, "", master.InputApproval, prompt, []string{"approve", "reject"}, sessionID)
	resp, err := h.broker.WaitForInput(ctx, taskID, req)
	if err != nil {
		return false, err
	}
	return resp.Action == "approve", nil
}

// NewApprovalBridge 创建 HITLBroker 到 ApprovalBridge 的适配器
func NewApprovalBridge(broker *master.HITLBroker) *hitlApprovalBridge {
	return &hitlApprovalBridge{broker: broker}
}

// MigrateConfigToDB 首次启动时将 config.json 中的 IM/MCP 配置迁移到数据库
// 如果数据库中已有 channel_configs 记录则跳过（说明已迁移过）
func MigrateConfigToDB(db store.Store, cfg *config.Config, logger *zap.Logger) {
	ctx := context.Background()

	// 检查是否已迁移过（通道或 MCP 服务端表中有记录则跳过）
	existingChannels, _ := db.ListChannelConfigs(ctx)
	existingMCP, _ := db.ListMCPServers(ctx)
	if len(existingChannels) > 0 || len(existingMCP) > 0 {
		return
	}

	logger.Info("首次启动：将 config.json 中的 IM/MCP 配置迁移到数据库")

	// Section 7 NIT-3：迁移前 Normalize 飞书配置，确保首启就把非法 AckEmoji / 零值 ThrottleMs
	// 在写库之前归一化——避免"首次写入不合规 → 下次 LoadChannelConfigsFromDB 才 warn"的日志错位。
	// Normalize 幂等，多次调用安全。
	cfg.Channel.Feishu.Normalize(func(msg, original, fallback string) {
		logger.Warn(msg,
			zap.String("original", original),
			zap.String("fallback", fallback),
			zap.String("phase", "migrate_legacy"))
	})

	// 迁移 IM 通道配置
	channelMappings := []struct {
		platform string
		enabled  bool
		cfg      any
	}{
		{"dingtalk", cfg.Channel.DingTalk.Enabled, cfg.Channel.DingTalk},
		{"feishu", cfg.Channel.Feishu.Enabled, cfg.Channel.Feishu},
		{"wecom", cfg.Channel.WeCom.Enabled, cfg.Channel.WeCom},
		{"wechatbot", cfg.Channel.WeChatBot.Enabled, cfg.Channel.WeChatBot},
	}
	for _, m := range channelMappings {
		data, err := json.Marshal(m.cfg)
		if err != nil {
			continue
		}
		_ = db.SaveChannelConfig(ctx, &store.ChannelConfigRecord{
			Platform:   m.platform,
			Enabled:    m.enabled,
			ConfigJSON: string(data),
		})
	}

	// 迁移 MCP 服务端配置
	for name, srv := range cfg.MCP.Servers {
		argsJSON, _ := json.Marshal(srv.Args)
		envJSON, _ := json.Marshal(srv.Env)
		headersJSON, _ := json.Marshal(srv.Headers)
		timeout := srv.Timeout
		if timeout == "" {
			timeout = "30s"
		}
		transport := srv.Transport
		if transport == "" {
			transport = "stdio"
		}
		_ = db.SaveMCPServer(ctx, &store.MCPServerRecord{
			Name:      name,
			Transport: transport,
			Command:   srv.Command,
			Args:      string(argsJSON),
			Env:       string(envJSON),
			URL:       srv.URL,
			Headers:   string(headersJSON),
			Timeout:   timeout,
			Enabled:   true,
		})
	}

	logger.Info("config.json 配置已迁移到数据库",
		zap.Int("channels", len(channelMappings)),
		zap.Int("mcp_servers", len(cfg.MCP.Servers)))
}

// LoadChannelConfigsFromDB 从数据库加载 IM 通道配置覆盖到运行时 Config
func LoadChannelConfigsFromDB(db store.Store, cfg *config.Config, logger *zap.Logger) {
	records, err := db.ListChannelConfigs(context.Background())
	if err != nil || len(records) == 0 {
		return
	}

	for _, rec := range records {
		switch rec.Platform {
		case "dingtalk":
			var dtCfg config.DingTalkConfig
			if err := json.Unmarshal([]byte(rec.ConfigJSON), &dtCfg); err == nil {
				cfg.Channel.DingTalk = dtCfg
				logger.Info("从数据库加载钉钉配置")
			}
		case "feishu":
			var fsCfg config.FeishuConfig
			if err := json.Unmarshal([]byte(rec.ConfigJSON), &fsCfg); err == nil {
				fsCfg.Normalize(func(msg, original, fallback string) {
					logger.Warn(msg,
						zap.String("original", original),
						zap.String("fallback", fallback))
				})
				cfg.Channel.Feishu = fsCfg
				logger.Info("从数据库加载飞书配置",
					zap.String("ack_emoji", fsCfg.AckEmoji),
					zap.Bool("renderer_enabled", fsCfg.RendererEnabled()),
					zap.Int("renderer_throttle_ms", fsCfg.Renderer.ThrottleMs))
			}
		case "wecom":
			var wcCfg config.WeComConfig
			if err := json.Unmarshal([]byte(rec.ConfigJSON), &wcCfg); err == nil {
				cfg.Channel.WeCom = wcCfg
				logger.Info("从数据库加载企业微信配置")
			}
		case "wechatbot":
			var wbCfg config.WeChatBotConfig
			if err := json.Unmarshal([]byte(rec.ConfigJSON), &wbCfg); err == nil {
				cfg.Channel.WeChatBot = wbCfg
				logger.Info("从数据库加载官方 wechatbot 配置")
			}
		}
	}
}

// LoadMCPServersFromDB 从数据库加载 MCP 服务端配置覆盖到运行时 Config
func LoadMCPServersFromDB(db store.Store, cfg *config.Config, logger *zap.Logger) {
	records, err := db.ListMCPServers(context.Background())
	if err != nil || len(records) == 0 {
		return
	}

	if cfg.MCP.Servers == nil {
		cfg.MCP.Servers = make(map[string]config.MCPServerConfig)
	}

	for _, rec := range records {
		if !rec.Enabled {
			continue
		}

		var args []string
		_ = json.Unmarshal([]byte(rec.Args), &args)
		var env map[string]string
		_ = json.Unmarshal([]byte(rec.Env), &env)
		var headers map[string]string
		_ = json.Unmarshal([]byte(rec.Headers), &headers)

		cfg.MCP.Servers[rec.Name] = config.MCPServerConfig{
			Command:   rec.Command,
			Args:      args,
			Env:       env,
			Transport: rec.Transport,
			URL:       rec.URL,
			Headers:   headers,
			Timeout:   rec.Timeout,
		}
		logger.Info("从数据库加载 MCP 服务端配置", zap.String("name", rec.Name))
	}
}

func restoreFeishuPushSchedules(ctx context.Context, m *master.Master, db store.Store, dispatcher func(context.Context, string) error, logger *zap.Logger) error {
	if m == nil || db == nil {
		return nil
	}
	if dispatcher == nil {
		return nil
	}
	records, err := db.ListScheduledPushes(ctx, string(channel.PlatformFeishu))
	if err != nil {
		return err
	}
	for _, rec := range records {
		if rec == nil || !rec.Enabled {
			continue
		}
		job := master.CronJob{
			ID:       rec.ID,
			Name:     "scheduled-push:" + rec.ID,
			Interval: time.Duration(rec.IntervalSec) * time.Second,
			Prompt:   rec.Prompt,
			Callback: func(rec *store.ScheduledPushRecord) func(context.Context) error {
				return func(runCtx context.Context) error {
					runAt := time.Now().UTC()
					var lastError string
					err := dispatcher(runCtx, rec.Prompt)
					if err != nil {
						lastError = err.Error()
					}
					_ = db.UpdateScheduledPushRun(runCtx, rec.ID, runAt, runAt.Add(time.Duration(rec.IntervalSec)*time.Second), lastError)
					return err
				}
			}(rec),
		}
		if err := m.CronCreate(job); err != nil {
			logger.Warn("恢复飞书定时推送失败", zap.String("schedule_id", rec.ID), zap.Error(err))
			continue
		}
	}
	return nil
}

func restoreScheduledTasksAsync(ctx context.Context, m *master.Master, db store.Store, logger *zap.Logger) {
	if m == nil || db == nil {
		return
	}
	if logger == nil {
		logger = zap.NewNop()
	}
	m.StopCron("scheduled-task:poller")
	err := m.CronCreate(master.CronJob{
		ID:       "scheduled-task-poller",
		Name:     "scheduled-task:poller",
		Interval: 30 * time.Second,
		Callback: func(runCtx context.Context) error {
			return runDueScheduledTasks(runCtx, m, db, logger)
		},
	})
	if err != nil {
		logger.Warn("恢复 Agent 定时任务扫描器失败", zap.Error(err))
		m.RecordScheduledTaskMetric("scheduled_task.reload_total", map[string]any{"result": "failed"})
		return
	}
	m.RecordScheduledTaskMetric("scheduled_task.reload_total", map[string]any{"result": "ok"})
	validateScheduledTaskReloadsAsync(ctx, m, db, logger)
	logger.Info("Agent 定时任务扫描器已恢复")
	restoreScheduledTaskHistoryGCAsync(ctx, m, db, logger)
}

func validateScheduledTaskReloadsAsync(ctx context.Context, m *master.Master, db store.Store, logger *zap.Logger) {
	go func() {
		validateScheduledTaskReloads(ctx, m, db, logger)
	}()
}

func validateScheduledTaskReloads(ctx context.Context, m *master.Master, db store.Store, logger *zap.Logger) {
	tasks, err := db.ListEnabledScheduledTasks(ctx)
	if err != nil {
		logger.Warn("读取 Agent 定时任务恢复列表失败", zap.Error(err))
		m.RecordScheduledTaskMetric("scheduled_task.reload_total", map[string]any{"result": "failed"})
		return
	}
	failures := make(map[string]string)
	for _, task := range tasks {
		if task == nil {
			continue
		}
		spec := master.ScheduleSpec{
			Interval: time.Duration(task.IntervalSec) * time.Second,
			CronExpr: task.CronExpr,
			Timezone: task.Timezone,
		}
		if err := master.ValidateScheduleSpec(spec, master.ScheduledTaskAdminMinInterval); err != nil {
			failures[task.ID] = "定时任务恢复失败: " + err.Error()
			m.RecordScheduledTaskMetric("scheduled_task.reload_total", map[string]any{"result": "failed"})
			continue
		}
		if _, err := master.NextScheduledRun(spec, time.Now().UTC()); err != nil {
			failures[task.ID] = "定时任务恢复失败: " + err.Error()
			m.RecordScheduledTaskMetric("scheduled_task.reload_total", map[string]any{"result": "failed"})
			continue
		}
		m.RecordScheduledTaskMetric("scheduled_task.reload_total", map[string]any{"result": "ok"})
	}
	if err := db.BulkMarkScheduledTaskReloadFailures(ctx, failures); err != nil {
		logger.Warn("标记 Agent 定时任务恢复失败状态失败", zap.Error(err))
		m.RecordScheduledTaskMetric("scheduled_task.reload_total", map[string]any{"result": "failed"})
		return
	}
	for taskID, msg := range failures {
		logger.Warn("Agent 定时任务恢复失败,已停用", zap.String("task_id", taskID), zap.String("error", msg))
	}
}

func restoreScheduledTaskHistoryGCAsync(ctx context.Context, m *master.Master, db store.Store, logger *zap.Logger) {
	if m == nil || db == nil {
		return
	}
	m.StopCron("scheduled-task:history-gc")
	maintain := func(runCtx context.Context) error {
		if err := db.MaintainScheduledTaskRunPartitions(runCtx, time.Now().UTC(), 4); err != nil {
			m.RecordScheduledTaskMetric("scheduled_task.partition_ensure_total", map[string]any{"result": "failed"})
			return err
		}
		m.RecordScheduledTaskMetric("scheduled_task.partition_ensure_total", map[string]any{"result": "ok"})
		return nil
	}
	go func() {
		if err := maintain(ctx); err != nil {
			logger.Warn("维护 Agent 定时任务 run 分区失败", zap.Error(err))
		}
	}()
	err := m.CronCreate(master.CronJob{
		ID:   "scheduled-task-history-gc",
		Name: "scheduled-task:history-gc",
		Schedule: master.ScheduleSpec{
			CronExpr: "0 3 * * 1",
			Timezone: "UTC",
		},
		Callback: maintain,
	})
	if err != nil {
		logger.Warn("恢复 Agent 定时任务历史分区维护失败", zap.Error(err))
		m.RecordScheduledTaskMetric("scheduled_task.reload_total", map[string]any{"result": "failed"})
		return
	}
	m.RecordScheduledTaskMetric("scheduled_task.reload_total", map[string]any{"result": "ok"})
}

func runDueScheduledTasks(ctx context.Context, m *master.Master, db store.Store, logger *zap.Logger) error {
	tasks, err := db.ListEnabledScheduledTasks(ctx)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	for _, task := range tasks {
		if task == nil || task.NextRunAt == nil || task.NextRunAt.After(now) {
			continue
		}
		nextRunAt, err := master.NextScheduledRun(master.ScheduleSpec{
			Interval: time.Duration(task.IntervalSec) * time.Second,
			CronExpr: task.CronExpr,
			Timezone: task.Timezone,
		}, now)
		if err != nil {
			logger.Warn("计算 Agent 定时任务下一次运行时间失败", zap.String("task_id", task.ID), zap.Error(err))
			continue
		}
		runID := newScheduleRunID()
		run, err := db.ClaimDueScheduledTaskRun(ctx, task.ID, now, runID, now.Add(35*time.Minute), nextRunAt, "master")
		if err != nil {
			if !errors.Is(err, store.ErrNotFound) {
				m.RecordScheduledTaskMetric("scheduled_task.claim_total", map[string]any{"result": "error", "target_type": task.TargetType})
				logger.Warn("claim Agent 定时任务失败", zap.String("task_id", task.ID), zap.Error(err))
			} else {
				m.RecordScheduledTaskMetric("scheduled_task.claim_total", map[string]any{"result": "skipped", "target_type": task.TargetType})
				logger.Debug("scheduled task claim skipped", zap.String("task_id", task.ID), zap.String("reason", "not_due_or_already_claimed"), zap.String("run_id", runID))
			}
			continue
		}
		m.RecordScheduledTaskMetric("scheduled_task.claim_total", map[string]any{"result": "claimed", "target_type": task.TargetType})
		go executeScheduledTaskRun(context.Background(), m, db, *task, run, logger)
	}
	return nil
}

func executeScheduledTaskRun(ctx context.Context, m *master.Master, db store.Store, task store.ScheduledTask, run *store.ScheduledTaskRun, logger *zap.Logger) {
	if run == nil {
		return
	}
	execCtx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()

	sessionID, output, attempts, err := m.DispatchScheduledTaskWithRetry(execCtx, task, run.ID)
	finishedAt := time.Now().UTC()
	run.FinishedAt = &finishedAt
	run.SessionID = sessionID
	run.Output = output
	run.AttemptCount += attempts
	if err != nil {
		if errors.Is(execCtx.Err(), context.DeadlineExceeded) {
			run.Status = "timeout"
			run.Error = "scheduled task timed out after 30m"
		} else {
			run.Status = "failed"
			run.Error = err.Error()
		}
	} else {
		run.Status = "succeeded"
		run.Error = ""
	}
	if finishErr := db.FinishScheduledTaskRun(context.Background(), run); finishErr != nil {
		logger.Warn("完成 Agent 定时任务 run 失败", zap.String("task_id", run.TaskID), zap.String("run_id", run.ID), zap.Error(finishErr))
		return
	}
	m.RecordScheduledTaskMetric("scheduled_task.run_total", map[string]any{"status": run.Status, "target_type": task.TargetType})
	logger.Info("Agent 定时任务 run 已完成", zap.String("task_id", run.TaskID), zap.String("run_id", run.ID), zap.String("status", run.Status))
}

func newScheduleRunID() string {
	return "run-" + uuid.NewString()
}

// BuildReloadChannelFunc 构建 IM 通道热重载回调
func BuildReloadChannelFunc(
	cfg *config.Config,
	router *channel.Router,
	hitlSubmitter feishu.InputSubmitter,
	lifecycleRepo feishu.ChatStateRepo,
	lifecycleTerminator feishu.SessionTerminator,
	lifecycleWelcome feishu.WelcomeSender,
	getCommittedFeishuIngressMode func() config.FeishuIngressMode,
	setCommittedFeishuIngressMode func(config.FeishuIngressMode),
	getFeishuWebhookGateMode func() config.FeishuIngressMode,
	setFeishuWebhookGateMode func(config.FeishuIngressMode),
	configMu *sync.RWMutex,
	logger *zap.Logger,
	reloadables ...feishu.Reloadable,
) func(string) error {
	return BuildReloadChannelFuncWithStore(
		cfg,
		router,
		nil,
		hitlSubmitter,
		lifecycleRepo,
		lifecycleTerminator,
		lifecycleWelcome,
		getCommittedFeishuIngressMode,
		setCommittedFeishuIngressMode,
		getFeishuWebhookGateMode,
		setFeishuWebhookGateMode,
		configMu,
		logger,
		reloadables...,
	)
}

func BuildReloadChannelFuncWithStore(
	cfg *config.Config,
	router *channel.Router,
	wechatStore wechatbot.Store,
	hitlSubmitter feishu.InputSubmitter,
	lifecycleRepo feishu.ChatStateRepo,
	lifecycleTerminator feishu.SessionTerminator,
	lifecycleWelcome feishu.WelcomeSender,
	getCommittedFeishuIngressMode func() config.FeishuIngressMode,
	setCommittedFeishuIngressMode func(config.FeishuIngressMode),
	getFeishuWebhookGateMode func() config.FeishuIngressMode,
	setFeishuWebhookGateMode func(config.FeishuIngressMode),
	configMu *sync.RWMutex,
	logger *zap.Logger,
	reloadables ...feishu.Reloadable,
) func(string) error {
	if router == nil {
		return nil
	}
	return func(platform string) error {
		configMu.RLock()
		channelCfg := cfg.Channel
		configMu.RUnlock()

		// 1. 卸载旧插件（忽略 not found 错误）
		// 2. 根据平台创建新插件
		switch platform {
		case "dingtalk":
			_ = router.UnregisterPlugin(channel.Platform(platform))
			if !channelCfg.DingTalk.Enabled {
				logger.Info("钉钉通道已禁用，仅卸载旧插件")
				return nil
			}
			dtPlugin := dingtalk.New(channelCfg.DingTalk, router, logger)
			router.RegisterPlugin(dtPlugin)
			logger.Info("钉钉通道已热重载")

		case "feishu":
			nextMode := channelCfg.Feishu.ResolvedIngressMode()
			currentMode := config.FeishuIngressModeWebhook
			if getCommittedFeishuIngressMode != nil {
				currentMode = getCommittedFeishuIngressMode()
			}
			currentGateMode := currentMode
			if getFeishuWebhookGateMode != nil {
				currentGateMode = getFeishuWebhookGateMode()
			}

			// 切换期间先把 gate 置为关闭态（非 webhook），防止 longconn->webhook 窗口期双入口。
			if setFeishuWebhookGateMode != nil {
				setFeishuWebhookGateMode(config.FeishuIngressModeLongconn)
			}
			restoreGate := true
			defer func() {
				if restoreGate && setFeishuWebhookGateMode != nil {
					setFeishuWebhookGateMode(currentGateMode)
				}
			}()

			if err := router.UnregisterPlugin(channel.Platform(platform)); err != nil {
				return err
			}
			if !channelCfg.Feishu.Enabled {
				router.SetInboundContextResolver(channel.PlatformFeishu, nil)
				if err := reloadFeishuComponents(channelCfg.Feishu, reloadables...); err != nil {
					return err
				}
				logger.Info("飞书通道已禁用，仅卸载旧插件")
				if setCommittedFeishuIngressMode != nil {
					setCommittedFeishuIngressMode(config.FeishuIngressModeLongconn)
				}
				restoreGate = false
				return nil
			}
			fsPlugin, err := buildFeishuPluginFn(
				channelCfg.Feishu,
				router,
				hitlSubmitter,
				buildFeishuGovernance(channelCfg.Feishu, lifecycleRepo, lifecycleTerminator, nil, feishu.NewJSONLAuditSink(""), logger),
				nil,
				logger,
			)
			if err != nil {
				return err
			}
			if lifecycleHandler := buildFeishuLifecycleHandler(lifecycleRepo, lifecycleTerminator, lifecycleWelcome, fsPlugin, router.RetryQueue(), logger); lifecycleHandler != nil {
				fsPlugin = fsPlugin.WithLifecycleHandler(lifecycleHandler)
			}
			fsPlugin = fsPlugin.WithChatStateRepo(lifecycleRepo)
			fsPlugin = fsPlugin.WithReliabilityLeaderGate(feishu.NewReliabilityLeaderGateFromChatStateRepo(lifecycleRepo, logger))
			fsPlugin = fsPlugin.WithGovernance(buildFeishuGovernance(channelCfg.Feishu, lifecycleRepo, lifecycleTerminator, fsPlugin.Client(), feishu.NewJSONLAuditSink(""), logger))
			_ = fsPlugin.Client().BotOpenID()
			if channelCfg.Feishu.InboundContextResolverEnabled() {
				router.SetInboundContextResolver(channel.PlatformFeishu,
					feishu.NewContextResolver(fsPlugin.Client(), logger).
						WithIdentityConfig(channelCfg.Feishu.Identity).
						WithRegion(channelCfg.Feishu.Region).
						WithNameLocale(channelCfg.Feishu.IdentityNameLocaleResolved()))
			} else {
				router.SetInboundContextResolver(channel.PlatformFeishu, nil)
			}
			if provider, ok := any(router).(interface {
				MetricsWriter() observability.MetricsWriter
			}); ok {
				fsPlugin.SetMetricsWriter(provider.MetricsWriter())
			}
			router.RegisterPlugin(fsPlugin)
			if err := fsPlugin.Start(); err != nil {
				return err
			}
			// Section 8 NIT-1（cross-review）：显式刷新 Router 的 RendererEnabled 回调。
			// 即便 BuildRendererEnabledFn 捕获的是 *config.Config 指针、对 `cfg.Channel.Feishu` 整块
			// 赋值后仍能读到最新值（当前实现），这里仍然显式重注入——避免未来有人把闭包改成值缓存
			// 后静默失效。这是"定目标-追过程-拿结果"的 defensive 操作，成本极低。
			router.SetRendererEnabled(BuildRendererEnabledFn(cfg))
			if setCommittedFeishuIngressMode != nil {
				setCommittedFeishuIngressMode(nextMode)
			}
			if setFeishuWebhookGateMode != nil {
				if nextMode == config.FeishuIngressModeWebhook {
					setFeishuWebhookGateMode(config.FeishuIngressModeWebhook)
				} else {
					setFeishuWebhookGateMode(config.FeishuIngressModeLongconn)
				}
			}
			if err := reloadFeishuComponents(channelCfg.Feishu, reloadables...); err != nil {
				return err
			}
			restoreGate = false
			logger.Info("飞书通道已热重载",
				zap.String("ingress_mode", string(nextMode)),
				zap.Bool("renderer_enabled", channelCfg.Feishu.RendererEnabled()),
				zap.Bool("context_resolver_enabled", channelCfg.Feishu.InboundContextResolverEnabled()))

		case "wecom":
			_ = router.UnregisterPlugin(channel.Platform(platform))
			if !channelCfg.WeCom.Enabled {
				logger.Info("企业微信通道已禁用，仅卸载旧插件")
				return nil
			}
			wcPlugin := wecom.New(channelCfg.WeCom, router, logger)
			router.RegisterPlugin(wcPlugin)
			logger.Info("企业微信通道已热重载")

		case "wechatbot":
			wbCfg := wechatbot.ConfigFromApp(channelCfg.WeChatBot, cfg.SessionsDir)
			if existing, ok := router.GetPlugin(channel.PlatformWeChatBot); ok {
				if configurable, ok := existing.(interface{ SetConfig(wechatbot.Config) }); ok {
					configurable.SetConfig(wbCfg)
					if !channelCfg.WeChatBot.Enabled {
						logger.Info("官方 wechatbot 通道已禁用")
					} else {
						logger.Info("官方 wechatbot 通道已热重载")
					}
					return nil
				}
				_ = router.UnregisterPlugin(channel.PlatformWeChatBot)
			}
			registry := wechatbot.NewRegistry(wbCfg, router, wechatStore, logger)
			plugin := wechatbot.NewPlugin(registry, logger)
			if provider, ok := any(router).(interface {
				MetricsWriter() observability.MetricsWriter
			}); ok {
				plugin.SetMetricsWriter(provider.MetricsWriter())
			}
			router.RegisterPlugin(plugin)
			logger.Info("官方 wechatbot 通道已热重载", zap.Bool("enabled", channelCfg.WeChatBot.Enabled))

		default:
			return fmt.Errorf("不支持的 IM 通道平台: %s", platform)
		}
		return nil
	}
}

func reloadFeishuComponents(cfg config.FeishuConfig, reloadables ...feishu.Reloadable) error {
	for _, r := range reloadables {
		if r == nil {
			continue
		}
		if err := r.ReloadFromConfig(cfg); err != nil {
			return err
		}
	}
	return nil
}

func buildFeishuPlugin(
	cfg config.FeishuConfig,
	router *channel.Router,
	hitlSubmitter feishu.InputSubmitter,
	governance *feishu.GovernanceService,
	lifecycleHandler *feishu.LifecycleHandler,
	logger *zap.Logger,
) (*feishu.Plugin, error) {
	plugin := feishu.New(cfg, router, logger)
	if hitlSubmitter != nil {
		plugin = plugin.WithHITLBridge(feishu.NewFeishuHITLBridge(hitlSubmitter, logger, nil))
	}
	if governance != nil {
		plugin = plugin.WithGovernance(governance)
	}
	if lifecycleHandler != nil {
		plugin = plugin.WithLifecycleHandler(lifecycleHandler)
	}
	return plugin, nil
}

func buildFeishuLifecycleHandler(
	repo feishu.ChatStateRepo,
	terminator feishu.SessionTerminator,
	welcome feishu.WelcomeSender,
	clientProvider feishuLifecyclePluginClient,
	retryQueue channel.RetryQueue,
	logger *zap.Logger,
) *feishu.LifecycleHandler {
	if repo == nil {
		return nil
	}
	return feishu.NewLifecycleHandler(repo, terminator, buildFeishuWelcomeSender(welcome, clientProvider, retryQueue, logger), logger)
}

// BuildReloadMCPFunc 构建 MCP 服务端热重载回调
func BuildReloadMCPFunc(
	cfg *config.Config,
	host *mcphost.Host,
	clients *[]*mcphost.RemoteMCPClient,
	clientsMu *sync.Mutex,
	configMu *sync.RWMutex,
	logger *zap.Logger,
) func(string) error {
	if host == nil {
		return nil
	}
	return func(serverName string) error {
		configMu.RLock()
		serverCfg, exists := cfg.MCP.Servers[serverName]
		configMu.RUnlock()

		// 1. 关闭旧连接（持锁保护切片并发访问）
		clientsMu.Lock()
		for i, c := range *clients {
			if c.Name() == serverName {
				_ = c.Close()
				*clients = append((*clients)[:i], (*clients)[i+1:]...)
				break
			}
		}
		clientsMu.Unlock()

		if !exists {
			logger.Info("MCP 服务端已删除", zap.String("name", serverName))
			return nil
		}

		spec := mcphost.MCPServerSpec{
			Name:      serverName,
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

		transport, err := mcphost.BuildTransport(spec, nil, logger)
		if err != nil {
			return fmt.Errorf("创建 MCP 传输失败 (%s): %w", serverName, err)
		}

		client, err := mcphost.ConnectRemoteMCP(context.Background(), transport, host, serverName, logger)
		if err != nil {
			return fmt.Errorf("连接 MCP 服务端失败 (%s): %w", serverName, err)
		}

		clientsMu.Lock()
		*clients = append(*clients, client)
		clientsMu.Unlock()

		logger.Info("MCP 服务端已热重载",
			zap.String("name", serverName),
			zap.String("transport", serverCfg.Transport))
		return nil
	}
}

// LoadAllConfigFromDB 从数据库 configs KV 表加载所有运行时配置覆盖到内存 Config
func LoadAllConfigFromDB(db store.Store, cfg *config.Config, logger *zap.Logger) {
	allCfg, err := db.GetAllConfig(context.Background())
	if err != nil || len(allCfg) == 0 {
		return
	}

	logger.Info("从数据库加载运行时配置", zap.Int("keys", len(allCfg)))

	// HITL
	cfgParseBool(allCfg, "hitl.enabled", &cfg.HITL.Enabled)
	cfgParseString(allCfg, "hitl.step_confirmation", &cfg.HITL.StepConfirmation)
	cfgParseDuration(allCfg, "hitl.input_timeout", &cfg.HITL.InputTimeout)
	cfgParseBool(allCfg, "hitl.websocket_enabled", &cfg.HITL.WebSocketEnabled)
	cfgParseBool(allCfg, "hitl.websocket_insecure_origin", &cfg.HITL.WebSocketInsecureOrigin)
	cfgParseInt(allCfg, "hitl.websocket_max_conn_per_ip", &cfg.HITL.WebSocketMaxConnPerIP)
	cfgParseString(allCfg, "hitl.websocket_token", &cfg.HITL.WebSocketToken)
	cfgParseJSON(allCfg, "hitl.permission_rules", &cfg.HITL.PermissionRules)

	// Agent
	cfgParseDuration(allCfg, "agent.timeout", &cfg.Agent.Timeout)
	cfgParseInt(allCfg, "agent.max_concurrent_agents", &cfg.Agent.MaxConcurrentAgents)
	cfgParseDuration(allCfg, "agent.health_interval", &cfg.Agent.HealthInterval)
	cfgParseDuration(allCfg, "agent.shell_timeout", &cfg.Agent.ShellTimeout)
	cfgParseDuration(allCfg, "agent.script_timeout", &cfg.Agent.ScriptTimeout)
	cfgParseDuration(allCfg, "agent.ws_ping_interval", &cfg.Agent.WSPingInterval)
	cfgParseDuration(allCfg, "agent.sync_interval", &cfg.Agent.SyncInterval)
	cfgParseBool(allCfg, "agent.plan_runtime.enabled", &cfg.Agent.PlanRuntime.Enabled)
	cfgParseBool(allCfg, "agent.plan_runtime.auto_continue", &cfg.Agent.PlanRuntime.AutoContinue)
	cfgParseInt(allCfg, "agent.plan_runtime.max_auto_continue", &cfg.Agent.PlanRuntime.MaxAutoContinue)
	cfgParseString(allCfg, "agent.tool_recall.mode", &cfg.Agent.ToolRecall.Mode)
	cfgParseInt(allCfg, "agent.tool_recall.limit", &cfg.Agent.ToolRecall.Limit)
	cfgParseFloat64(allCfg, "agent.tool_recall.min_score", &cfg.Agent.ToolRecall.MinScore)
	cfgParseFloat64(allCfg, "agent.tool_recall.side_effect_min_score", &cfg.Agent.ToolRecall.SideEffectMinScore)
	cfgParseBool(allCfg, "agent.tool_recall.log_candidates", &cfg.Agent.ToolRecall.LogCandidates)

	// Context Compression
	cfgParseBool(allCfg, "agent.context_compression.enabled", &cfg.Agent.ContextCompression.Enabled)
	if v, ok := allCfg["agent.context_compression.strategy"]; ok {
		cfg.Agent.ContextCompression.Strategy = config.CompactStrategy(v)
	}
	cfgParseInt(allCfg, "agent.context_compression.max_tokens", &cfg.Agent.ContextCompression.MaxTokens)
	cfgParseInt(allCfg, "agent.context_compression.reserve_tokens", &cfg.Agent.ContextCompression.ReserveTokens)
	cfgParseDuration(allCfg, "agent.context_compression.compact_timeout", &cfg.Agent.ContextCompression.CompactTimeout)
	cfgParseBool(allCfg, "agent.context_compression.use_tiktoken", &cfg.Agent.ContextCompression.UseTiktoken)
	cfgParseBool(allCfg, "agent.context_compression.lazy_mode", &cfg.Agent.ContextCompression.LazyMode)
	cfgParseInt(allCfg, "agent.context_compression.lazy_threshold", &cfg.Agent.ContextCompression.LazyThreshold)

	// Memory
	cfgParseBool(allCfg, "memory.enabled", &cfg.Memory.Enabled)
	cfgParseInt(allCfg, "memory.max_memories", &cfg.Memory.MaxMemories)
	cfgParseInt(allCfg, "memory.retention_days", &cfg.Memory.RetentionDays)
	cfgParseBool(allCfg, "memory.auto_extract", &cfg.Memory.AutoExtract)
	cfgParseInt(allCfg, "memory.inject_max_tokens", &cfg.Memory.InjectMaxTokens)
	cfgParseInt(allCfg, "memory.inject_top_k", &cfg.Memory.InjectTopK)
	cfgParseFloat64(allCfg, "memory.inject_min_confidence", &cfg.Memory.InjectMinConfidence)
	cfgParseFloat64(allCfg, "memory.inject_min_score", &cfg.Memory.InjectMinScore)
	cfgParseInt(allCfg, "memory.feedback_top_k", &cfg.Memory.FeedbackTopK)
	cfgParseInt(allCfg, "memory.memory_top_k", &cfg.Memory.MemoryTopK)
	cfgParseInt(allCfg, "memory.feedback_max_tokens", &cfg.Memory.FeedbackMaxTokens)
	cfgParseInt(allCfg, "memory.memory_max_tokens", &cfg.Memory.MemoryMaxTokens)
	cfgParseBool(allCfg, "memory.embedding_enabled", &cfg.Memory.EmbeddingEnabled)
	cfgParseString(allCfg, "memory.embedding_model", &cfg.Memory.EmbeddingModel)

	// Misc
	cfgParseString(allCfg, "prompt_language", &cfg.PromptLanguage)
	cfgParseBool(allCfg, "webui.enabled", &cfg.WebUI.Enabled)
	cfgParseBool(allCfg, "plugin.enabled", &cfg.Plugin.Enabled)
	cfgParseBool(allCfg, "plugin.auto_discover", &cfg.Plugin.AutoDiscover)
	cfgParseBool(allCfg, "control_plane.enabled", &cfg.ControlPlane.Enabled)
	cfgParseInt(allCfg, "control_plane.max_sessions", &cfg.ControlPlane.MaxSessions)
	cfgParseFloat64(allCfg, "control_plane.rate_limit", &cfg.ControlPlane.RateLimit)
	cfgParseInt(allCfg, "control_plane.rate_burst", &cfg.ControlPlane.RateBurst)
	cfgParseString(allCfg, "custom_tools_dir", &cfg.CustomToolsDir)
	cfgParseString(allCfg, "sessions_dir", &cfg.SessionsDir)
	// channel.enabled 已改为从各通道 Enabled 状态自动推导，不再从 KV 表读取

	// MCP
	cfgParseDuration(allCfg, "mcp.timeout", &cfg.MCP.Timeout)

	// Security
	cfgParseBool(allCfg, "security.enabled", &cfg.Security.Enabled)
	cfgParseString(allCfg, "security.default_policy", &cfg.Security.DefaultPolicy)
	cfgParseJSON(allCfg, "security.exec_rules", &cfg.Security.ExecRules)
	cfgParseJSON(allCfg, "security.watch_env_vars", &cfg.Security.WatchEnvVars)
	cfgParseString(allCfg, "security.permission_mode", &cfg.Security.PermissionMode)
	cfgParseJSON(allCfg, "security.destructive_patterns", &cfg.Security.DestructivePatterns)

	// Sandbox
	cfgParseBool(allCfg, "sandbox.enabled", &cfg.Sandbox.Enabled)
	cfgParseString(allCfg, "sandbox.type", &cfg.Sandbox.Type)
	cfgParseString(allCfg, "sandbox.docker.image", &cfg.Sandbox.Docker.Image)
	cfgParseString(allCfg, "sandbox.docker.cpu_limit", &cfg.Sandbox.Docker.CPULimit)
	cfgParseString(allCfg, "sandbox.docker.memory_limit", &cfg.Sandbox.Docker.MemoryLimit)
	cfgParseInt(allCfg, "sandbox.docker.pids_limit", &cfg.Sandbox.Docker.PidsLimit)
	cfgParseString(allCfg, "sandbox.docker.tmpfs_size", &cfg.Sandbox.Docker.TmpfsSize)
	cfgParseString(allCfg, "sandbox.docker.network", &cfg.Sandbox.Docker.Network)

	// ACP Server
	cfgParseBool(allCfg, "acp_server.enabled", &cfg.ACPServer.Enabled)
	cfgParseString(allCfg, "acp_server.auth_method", &cfg.ACPServer.AuthMethod)
	cfgParseInt(allCfg, "acp_server.max_sessions", &cfg.ACPServer.MaxSessions)

	// Runtime Policy
	cfgParseDuration(allCfg, "runtime_policy.llm_call_timeout", &cfg.RuntimePolicy.LLMCallTimeout)
	cfgParseDuration(allCfg, "runtime_policy.tool_timeout", &cfg.RuntimePolicy.ToolTimeout)
	cfgParseDuration(allCfg, "runtime_policy.task_timeout", &cfg.RuntimePolicy.TaskTimeout)
	cfgParseDuration(allCfg, "runtime_policy.spawn_agent_timeout", &cfg.RuntimePolicy.SpawnAgentTimeout)
	cfgParseDuration(allCfg, "runtime_policy.acp_prompt_timeout", &cfg.RuntimePolicy.ACPPromptTimeout)
	cfgParseDuration(allCfg, "runtime_policy.acp_reconnect_timeout", &cfg.RuntimePolicy.ACPReconnectTimeout)
	cfgParseInt(allCfg, "runtime_policy.subagent_max_turns", &cfg.RuntimePolicy.SubagentMaxTurns)
	cfgParseInt(allCfg, "runtime_policy.subagent_max_depth", &cfg.RuntimePolicy.SubagentMaxDepth)
	cfgParseInt(allCfg, "runtime_policy.per_session_parallel", &cfg.RuntimePolicy.PerSessionParallel)
	cfgParseInt(allCfg, "runtime_policy.global_workers", &cfg.RuntimePolicy.GlobalWorkers)
	cfgParseFloat64(allCfg, "runtime_policy.max_session_cost_usd", &cfg.RuntimePolicy.MaxSessionCostUSD)

	// LSP
	cfgParseBool(allCfg, "lsp.enabled", &cfg.LSP.Enabled)
	cfgParseDuration(allCfg, "lsp.timeout", &cfg.LSP.Timeout)
	cfgParseInt(allCfg, "lsp.max_servers", &cfg.LSP.MaxServers)
	cfgParseDuration(allCfg, "lsp.health_interval", &cfg.LSP.HealthInterval)
	cfgParseJSON(allCfg, "lsp.languages", &cfg.LSP.Languages)

	// DB 加载后重新执行联动逻辑：WebUI 启用时自动启用 WebSocket
	if cfg.WebUI.Enabled && !cfg.HITL.WebSocketEnabled {
		cfg.HITL.WebSocketEnabled = true
	}
}

// LoadLLMFromDB 从 llm_providers/llm_models 表加载 LLM 配置覆盖到内存 Config
func LoadLLMFromDB(db store.Store, cfg *config.Config, logger *zap.Logger) {
	ctx := context.Background()

	// 加载默认 provider
	providers, err := db.ListLLMProviders(ctx)
	if err != nil {
		logger.Warn("从数据库加载 LLM providers 失败", zap.Error(err))
		return
	}
	for _, p := range providers {
		if p.IsDefault && p.Enabled {
			cfg.LLM.Provider = p.ProviderType
			if p.APIKey != "" {
				cfg.LLM.APIKey = p.APIKey
			}
			if p.BaseURL != "" {
				cfg.LLM.BaseURL = p.BaseURL
			}
			// 解析 config_json 中的扩展配置
			if p.ConfigJSON != "" && p.ConfigJSON != "{}" {
				var extra map[string]interface{}
				if err := json.Unmarshal([]byte(p.ConfigJSON), &extra); err == nil {
					if v, ok := extra["google_api_key"].(string); ok && v != "" {
						cfg.LLM.GoogleAPIKey = v
					}
					if v, ok := extra["azure_api_key"].(string); ok && v != "" {
						cfg.LLM.AzureAPIKey = v
					}
					if v, ok := extra["azure_deployment"].(string); ok && v != "" {
						cfg.LLM.AzureDeployment = v
					}
					if v, ok := extra["azure_endpoint"].(string); ok && v != "" {
						cfg.LLM.AzureEndpoint = v
					}
					if v, ok := extra["reasoning_effort"].(string); ok && v != "" {
						cfg.LLM.ReasoningEffort = v
					}
					if v, ok := extra["disable_json_mode"].(bool); ok {
						cfg.LLM.DisableJSONMode = v
					}
					if v, ok := extra["store_privacy"].(bool); ok {
						cfg.LLM.StorePrivacy = v
					}
				}
			}
			if p.APIFormat != "" {
				cfg.LLM.APIFormat = p.APIFormat
			}
			logger.Info("从数据库加载默认 LLM provider",
				zap.String("name", p.Name),
				zap.String("provider", p.ProviderType),
				zap.String("base_url", p.BaseURL),
				zap.String("api_format", p.APIFormat))
			break
		}
	}

	// 加载模型列表
	models, err := db.ListLLMModels(ctx)
	if err != nil {
		logger.Warn("从数据库加载 LLM models 失败", zap.Error(err))
		return
	}

	cfg.LLM.Models = nil
	for _, m := range models {
		if !m.Enabled {
			continue
		}
		if m.IsDefault {
			cfg.LLM.Model = m.Model
			if m.BaseURL != "" {
				cfg.LLM.BaseURL = m.BaseURL
			}
			if m.APIKey != "" {
				cfg.LLM.APIKey = m.APIKey
			}
		}
		cfg.LLM.Models = append(cfg.LLM.Models, config.ModelProfile{
			Name:     m.Name,
			Provider: m.ProviderName,
			Model:    m.Model,
			BaseURL:  m.BaseURL,
			APIKey:   m.APIKey,
		})
	}

	if len(models) > 0 {
		logger.Info("从数据库加载 LLM models", zap.Int("count", len(cfg.LLM.Models)))
	}
}

// --- Config KV 解析 helpers ---

func cfgParseBool(m map[string]string, key string, target *bool) {
	if v, ok := m[key]; ok {
		switch v {
		case "true", "1", "yes":
			*target = true
		case "false", "0", "no":
			*target = false
		}
	}
}

func cfgParseDuration(m map[string]string, key string, target *time.Duration) {
	if v, ok := m[key]; ok {
		if d, err := time.ParseDuration(v); err == nil {
			*target = d
		}
	}
}

func cfgParseInt(m map[string]string, key string, target *int) {
	if v, ok := m[key]; ok {
		if n, err := fmt.Sscanf(v, "%d", target); n == 1 && err == nil {
			return
		}
	}
}

func cfgParseFloat64(m map[string]string, key string, target *float64) {
	if v, ok := m[key]; ok {
		if n, err := fmt.Sscanf(v, "%f", target); n == 1 && err == nil {
			return
		}
	}
}

func cfgParseString(m map[string]string, key string, target *string) {
	if v, ok := m[key]; ok {
		*target = v
	}
}

func cfgParseJSON(m map[string]string, key string, target any) {
	if v, ok := m[key]; ok && v != "" {
		_ = json.Unmarshal([]byte(v), target)
	}
}

// BuildReloadConfigFunc 构建配置重载回调，从 DB 全量加载到内存 Config。
// 注意：调用方应持有 ConfigMu 写锁。
func BuildReloadConfigFunc(cfg *config.Config, db store.Store, logger *zap.Logger) func() {
	if db == nil {
		return nil
	}
	return func() {
		LoadAllConfigFromDB(db, cfg, logger)
		LoadChannelConfigsFromDB(db, cfg, logger)
		LoadMCPServersFromDB(db, cfg, logger)
		LoadLLMFromDB(db, cfg, logger)
	}
}

// BuildLLMExtraConfig 从配置中提取提供商特有的扩展配置
func BuildLLMExtraConfig(cfg *config.Config) map[string]any {
	extra := make(map[string]any)

	if cfg.LLM.GoogleAPIKey != "" {
		extra["google_api_key"] = cfg.LLM.GoogleAPIKey
	}
	if cfg.LLM.AzureAPIKey != "" {
		extra["azure_api_key"] = cfg.LLM.AzureAPIKey
	}
	if cfg.LLM.AzureDeployment != "" {
		extra["azure_deployment"] = cfg.LLM.AzureDeployment
	}
	if cfg.LLM.AzureEndpoint != "" {
		extra["azure_endpoint"] = cfg.LLM.AzureEndpoint
	}
	if cfg.LLM.ReasoningEffort != "" {
		extra["reasoning_effort"] = cfg.LLM.ReasoningEffort
	}
	if cfg.LLM.DisableJSONMode {
		extra["disable_json_mode"] = true
	}
	if cfg.LLM.StorePrivacy {
		extra["store_privacy"] = true
	}
	if cfg.LLM.ModelRegistryURL != "" {
		extra["model_registry_url"] = cfg.LLM.ModelRegistryURL
	}

	return extra
}

// BuildRendererEnabledFn 构造 channel.Router.SetRendererEnabled 的平台级回调。
//
// 契约：
//   - 仅 PlatformFeishu 读配置 `cfg.Channel.Feishu.RendererEnabled()`（语义为 `!Renderer.Disabled`）。
//   - 其余平台一律返回 false——截至 Section 8，只有 feishu 实现 EventRenderer，
//     其他平台在 Router 侧会走 legacy Send，保持 bit-identical 行为。
//   - cfg == nil 返回全平台 false 的降级闭包，避免 server.go 误用导致 panic。
//   - 闭包 pointer-capture `*config.Config`；热重载对 `cfg.Channel.Feishu` 整块赋值后，
//     下一次调用读到最新 Disabled 字段。`BuildReloadChannelFunc` feishu 分支仍显式重调
//     `SetRendererEnabled` 做 defensive 双保险，防止未来重构破坏该隐式契约。
//
// 扩展规则：新平台实现 EventRenderer 时，本函数的 switch 与
// `server_wiring_test.go:TestBuildRendererEnabledFn/non_feishu_platforms_always_false`
// 两处必须同步更新，否则测试会回归失败保护你。
//
// 抽出为命名函数的目的：server.go 里的匿名闭包不可测，抽到这里后由
// TestBuildRendererEnabledFn 直接校验平台分支+语义，形成 Section 8.4 的测试闭环。
func BuildRendererEnabledFn(cfg *config.Config) func(channel.Platform) bool {
	if cfg == nil {
		return func(channel.Platform) bool { return false }
	}
	return func(p channel.Platform) bool {
		if p == channel.PlatformFeishu {
			return cfg.Channel.Feishu.RendererEnabled()
		}
		return false
	}
}
