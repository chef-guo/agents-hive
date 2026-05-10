package api

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/chef-guo/agents-hive/internal/accounting"
	"github.com/chef-guo/agents-hive/internal/agentquality"
	"github.com/chef-guo/agents-hive/internal/airouter"
	"github.com/chef-guo/agents-hive/internal/auth"
	"github.com/chef-guo/agents-hive/internal/channel"
	"github.com/chef-guo/agents-hive/internal/channel/feishu"
	"github.com/chef-guo/agents-hive/internal/channel/push"
	"github.com/chef-guo/agents-hive/internal/config"
	"github.com/chef-guo/agents-hive/internal/gateway"
	"github.com/chef-guo/agents-hive/internal/master"
	"github.com/chef-guo/agents-hive/internal/memory"
	"github.com/chef-guo/agents-hive/internal/memoryobs"
	"github.com/chef-guo/agents-hive/internal/observability"
	"github.com/chef-guo/agents-hive/internal/qualityworkbench"
	"github.com/chef-guo/agents-hive/internal/sessiontodo"
	"github.com/chef-guo/agents-hive/internal/skills"
	"github.com/chef-guo/agents-hive/internal/store"
	"github.com/chef-guo/agents-hive/internal/streaming"
	"github.com/google/uuid"
)

// promptStoreInterface Prompt CRUD 存储接口（避免循环依赖）
type promptStoreInterface interface {
	Get(ctx context.Context, key, language string) (string, bool, error)
	Upsert(ctx context.Context, key, language, content, updatedBy string) error
	Delete(ctx context.Context, key, language string) error
	List(ctx context.Context, page, size int) ([]store.PromptRecord, int, error)
}

// promptLoaderInterface PromptLoader 缓存失效接口
type promptLoaderInterface interface {
	InvalidateDBCache(key string)
}

type qualityCandidateStore interface {
	UpsertCandidate(ctx context.Context, rec agentquality.CandidateRecord) (*agentquality.CandidateRecord, error)
	ListCandidates(ctx context.Context, filter agentquality.CandidateFilter) ([]agentquality.CandidateRecord, int, error)
	GetCandidate(ctx context.Context, id string) (*agentquality.CandidateRecord, bool, error)
	UpdateCandidateStatus(ctx context.Context, id string, status agentquality.CandidateStatus, reviewer, note, promotedCaseID string) error
}

type toolDescriptionStore interface {
	UpsertToolDescription(ctx context.Context, toolName, description, updatedBy string) error
	GetToolDescription(ctx context.Context, toolName string) (string, bool, error)
	DeleteToolDescription(ctx context.Context, toolName string) error
}

type memoryGovernancePolicyStore interface {
	UpsertMemoryGovernancePolicy(ctx context.Context, policyName, policyJSON, updatedBy string) error
	GetMemoryGovernancePolicy(ctx context.Context, policyName string) (string, bool, error)
	DeleteMemoryGovernancePolicy(ctx context.Context, policyName string) error
}

type memoryInjectionExplainReader interface {
	RecentMemoryInjectionEvents(ctx context.Context, limit int) ([]agentquality.Event, error)
}

type memoryProductionMetricsReader interface {
	LoadProductionMetrics(ctx context.Context, since, until time.Time, bucketSize time.Duration) (memoryobs.ProductionMetrics, error)
}

// Server 是 HTTP API 服务器
type Server struct {
	httpServer                    *http.Server
	master                        *master.Master
	skillRegistry                 *skills.OverlayRegistry
	streamHandler                 *streaming.StreamHandler
	wsHandler                     *streaming.WSHandler
	hitlConfig                    config.HITLConfig
	corsOrigins                   []string
	serverPort                    string
	logger                        *zap.Logger
	mux                           *http.ServeMux              // 存储 mux 引用，支持动态追加路由
	webuiEnabled                  bool                        // 是否启用前端控制台
	config                        *config.Config              // 完整配置（用于配置管理 API）
	configMu                      sync.RWMutex                // 保护 config 并发读写
	feishuIngressMode             config.FeishuIngressMode    // 飞书已提交运行时入口模式
	feishuWebhookGate             config.FeishuIngressMode    // 飞书 webhook gate 当前放行模式
	configPath                    string                      // 配置文件路径（用于保存配置）
	channelRouter                 *channel.Router             // Channel 路由器（用于查询插件状态）
	store                         store.Store                 // 统一存储（PG），用于模型/配置管理
	reloadProtocolFunc            func(protocol string) error // 协议热加载回调
	authEngine                    *auth.Engine
	costTracker                   accounting.CostTracker
	promptStore                   promptStoreInterface  // Prompt CRUD 存储（可选）
	promptLoader                  promptLoaderInterface // PromptLoader 缓存失效（可选）
	aiRouter                      *airouter.Router      // AI 路由器（可选，LLM CRUD 后触发热重载）
	skillStore                    skillStoreInterface   // Skill CRUD 存储（可选）
	qualityCandidateStore         qualityCandidateStore
	qualityEvalRunner             agentquality.EvalRunner
	memoryStore                   memory.MemoryStore
	memoryEmbeddingBacklog        memory.EmbeddingBacklog
	optimizationStore             agentquality.OptimizationSuggestionStore
	optimizationRolloutStore      agentquality.OptimizationRolloutStore
	optimizationApprovalStore     agentquality.ApprovalStore
	optimizationRollbackStore     agentquality.RollbackAlertStore
	optimizationEvalDiffStore     agentquality.EvalDiffStore
	toolDescriptionStore          toolDescriptionStore
	memoryGovernancePolicyStore   memoryGovernancePolicyStore
	workbenchGroupingRuleStore    qualityworkbench.GroupingRuleStore
	workbenchReplayStore          qualityworkbench.ReplayJobStore
	workbenchBatchEvalStore       qualityworkbench.BatchEvalRunStore
	workbenchReportStore          qualityworkbench.WeeklyReportStore
	feishuHealthClient            *feishu.Client
	pushService                   *push.Service
	feishuAuditSink               feishu.AuditSink
	sessionTodoStore              sessionTodoSnapshotStore
	sessionTodoOpsReader          sessionTodoOpsReader
	traceReader                   observability.TraceReader
	memoryInjectionExplainReader  memoryInjectionExplainReader
	memoryProductionMetricsReader memoryProductionMetricsReader
}

type sessionTodoOpsReader interface {
	LoadOps(ctx context.Context, since, until time.Time) (sessiontodo.OpsDashboardInput, error)
}

type pushScheduleStore interface {
	SaveScheduledPush(ctx context.Context, rec *store.ScheduledPushRecord) error
	GetScheduledPush(ctx context.Context, id string) (*store.ScheduledPushRecord, error)
	DeleteScheduledPush(ctx context.Context, id string) error
	ListScheduledPushes(ctx context.Context, platform string) ([]*store.ScheduledPushRecord, error)
	UpdateScheduledPushRun(ctx context.Context, id string, lastRunAt, nextRunAt time.Time, lastError string) error
}

func newScheduleID() string {
	return "sched-" + uuid.NewString()
}

// NewServer 创建一个新的 API 服务器
func NewServer(
	cfg config.ServerConfig,
	hitlCfg config.HITLConfig,
	webuiCfg config.WebUIConfig,
	m *master.Master,
	skillReg *skills.OverlayRegistry,
	fullCfg *config.Config,
	configPath string,
	channelRouter *channel.Router,
	db store.Store,
	authEngine *auth.Engine,
	logger *zap.Logger,
) *Server {
	// 构建 WebSocket 允许的 Origin 列表
	wsOrigins := cfg.CORSOrigins
	// 仅当配置文件中显式设置 websocket_insecure_origin: true 时才跳过 Origin 校验
	wsInsecureOrigin := hitlCfg.WebSocketInsecureOrigin
	if len(wsOrigins) == 0 {
		// 未配置 CORSOrigins 时，使用与 CORS 中间件相同的默认开发端口白名单
		for _, port := range defaultDevPorts {
			wsOrigins = append(wsOrigins, "localhost:"+port, "127.0.0.1:"+port)
		}
		// 同时允许服务器自身端口
		serverPort := fmt.Sprintf("%d", cfg.Port)
		wsOrigins = append(wsOrigins, "localhost:"+serverPort, "127.0.0.1:"+serverPort)
	}

	s := &Server{
		master:        m,
		skillRegistry: skillReg,
		streamHandler: streaming.NewStreamHandler(m, logger),
		wsHandler:     streaming.NewWSHandlerWithOptions(m, logger, wsInsecureOrigin, wsOrigins...),
		hitlConfig:    hitlCfg,
		corsOrigins:   cfg.CORSOrigins,
		serverPort:    fmt.Sprintf("%d", cfg.Port),
		logger:        logger,
		webuiEnabled:  webuiCfg.Enabled,
		config:        fullCfg,
		configPath:    configPath,
		channelRouter: channelRouter,
		store:         db,
		authEngine:    authEngine,
	}
	s.optimizationStore = agentquality.NewInMemoryOptimizationSuggestionStore()
	s.optimizationRolloutStore = agentquality.NewInMemoryOptimizationRolloutStore()
	s.optimizationApprovalStore = agentquality.NewInMemoryApprovalStore()
	s.optimizationRollbackStore = agentquality.NewInMemoryRollbackStore()
	s.optimizationEvalDiffStore = newInMemoryEvalDiffStore()
	s.memoryEmbeddingBacklog = memory.NewInMemoryEmbeddingBacklog()
	writableStore := agentquality.NewInMemoryOptimizationWritableStore()
	s.toolDescriptionStore = writableStore
	s.memoryGovernancePolicyStore = writableStore
	s.workbenchGroupingRuleStore = qualityworkbench.NewMemoryGroupingRuleStore(time.Now)
	s.workbenchReplayStore = qualityworkbench.NewMemoryReplayJobStore(time.Now)
	s.workbenchBatchEvalStore = qualityworkbench.NewMemoryBatchEvalRunStore(time.Now)
	s.workbenchReportStore = qualityworkbench.NewMemoryWeeklyReportStore(time.Now)
	if pgStore, ok := db.(*store.PostgresStore); ok && pgStore.Pool() != nil {
		s.workbenchReplayStore = qualityworkbench.NewPGReplayJobStore(pgStore.Pool())
		s.workbenchGroupingRuleStore = qualityworkbench.NewPGGroupingRuleStore(pgStore.Pool())
		s.workbenchBatchEvalStore = qualityworkbench.NewPGBatchEvalRunStore(pgStore.Pool())
		s.workbenchReportStore = qualityworkbench.NewPGWeeklyReportStore(pgStore.Pool())
		s.memoryEmbeddingBacklog = memory.NewInstrumentedEmbeddingBacklog(
			memory.NewPGEmbeddingBacklog(pgStore.Pool()),
			memory.NewExternalMetricRecorder(memoryobs.NewWriter(observability.NewPgMetricsWriter(pgStore.Pool(), logger))),
		)
		pgWritableStore := agentquality.NewPGOptimizationWritableStore(pgStore.Pool())
		s.toolDescriptionStore = pgWritableStore
		s.memoryGovernancePolicyStore = pgWritableStore
		s.optimizationStore = agentquality.NewPGOptimizationSuggestionStore(pgStore.Pool(), logger)
		s.optimizationRolloutStore = agentquality.NewPGOptimizationRolloutStore(pgStore.Pool())
		s.optimizationApprovalStore = agentquality.NewPGApprovalStore(pgStore.Pool())
		s.optimizationRollbackStore = agentquality.NewPGRollbackStore(pgStore.Pool())
		s.optimizationEvalDiffStore = agentquality.NewPGEvalDiffStore(pgStore.Pool())
		s.sessionTodoOpsReader = sessiontodo.NewPGOpsReader(pgStore.Pool())
		s.traceReader = observability.NewPgTracer(pgStore.Pool(), logger)
		s.memoryInjectionExplainReader = &pgMemoryInjectionExplainReader{pool: pgStore.Pool()}
		s.memoryProductionMetricsReader = memoryobs.NewPGMetricsReader(pgStore.Pool())
	}
	if s.master != nil {
		s.master.SetTrajectoryStore(s.getTrajectoryStore())
	}
	if fullCfg != nil && fullCfg.Channel.Feishu.Push.Enabled {
		s.pushService = push.NewService(channelRouter, push.Config{
			Enabled:          true,
			PerChatPerMinute: fullCfg.Channel.Feishu.PushPerChatPerMinuteResolved(),
			IdempotencyTTL:   fullCfg.Channel.Feishu.PushIdempotencyTTLResolved(),
		}, logger)
	}
	s.feishuAuditSink = feishu.NewJSONLAuditSink("")
	if fullCfg != nil {
		s.feishuIngressMode = fullCfg.Channel.Feishu.ResolvedIngressMode()
	} else {
		s.feishuIngressMode = config.FeishuIngressModeWebhook
	}
	s.feishuWebhookGate = s.feishuIngressMode

	// 配置 WebSocket 认证
	if hitlCfg.WebSocketToken != "" {
		s.wsHandler.SetAuthToken(hitlCfg.WebSocketToken)
		logger.Info("WebSocket 认证已启用")
	}
	if authEngine != nil {
		s.wsHandler.SetAuthEngine(authEngine)
		logger.Info("WebSocket JWT 认证已启用")
	}
	if hitlCfg.WebSocketMaxConnPerIP > 0 {
		s.wsHandler.SetMaxConnectionsPerIP(hitlCfg.WebSocketMaxConnPerIP)
	}

	mux := http.NewServeMux()
	s.mux = mux
	s.registerRoutes(mux)

	handler := s.applyMiddleware(mux)

	s.httpServer = &http.Server{
		Addr:         fmt.Sprintf("%s:%d", cfg.Host, cfg.Port),
		Handler:      handler,
		ReadTimeout:  0, // WebSocket/SSE 长连接不设置读超时
		WriteTimeout: 0, // WebSocket/SSE 长连接不设置写超时
		IdleTimeout:  60 * time.Second,
	}

	return s
}

// Start 开始监听 HTTP 请求
func (s *Server) Start() error {
	s.logger.Info("启动 HTTP 服务器", zap.String("addr", s.httpServer.Addr))
	return s.httpServer.ListenAndServe()
}

// Shutdown 优雅关闭服务器
func (s *Server) Shutdown(ctx context.Context) error {
	s.logger.Info("正在关闭 HTTP 服务器")
	return s.httpServer.Shutdown(ctx)
}

// SetChannelRouter 注入 Channel 路由器，注册 webhook 路由
func (s *Server) SetChannelRouter(r *channel.Router) {
	if r == nil {
		return
	}
	s.mux.HandleFunc("POST /api/v1/channel/dingtalk/webhook", r.WebhookHandler(channel.PlatformDingTalk))
	s.mux.Handle("POST /api/v1/channel/feishu/webhook", NewFeishuIngressGateHandler(
		s.GetFeishuWebhookGateMode,
		func() http.Handler {
			return r.WebhookHandler(channel.PlatformFeishu)
		},
		s.logger,
	))
	s.mux.HandleFunc("GET /api/v1/channel/wecom/webhook", r.WebhookHandler(channel.PlatformWeCom))
	s.mux.HandleFunc("POST /api/v1/channel/wecom/webhook", r.WebhookHandler(channel.PlatformWeCom))
	s.mux.HandleFunc("POST /api/v1/channel/wechat/webhook", r.WebhookHandler(channel.PlatformWeChat))
	s.logger.Info("IM Channel webhook 路由已注册")
}

// SetGateway 注入 Gateway 网关，注册 RPC 和 WebSocket 路由
func (s *Server) SetGateway(gw *gateway.Gateway) {
	if gw == nil {
		return
	}
	s.mux.HandleFunc("POST /api/v1/rpc", gw.HandleHTTP)
	s.mux.HandleFunc("/api/v1/rpc/ws", gw.HandleWebSocket)
	s.logger.Info("Gateway RPC/WebSocket 路由已注册")
}

// SetWSPingInterval 设置 WebSocket ping 间隔
func (s *Server) SetWSPingInterval(d time.Duration) {
	if s.wsHandler != nil && d > 0 {
		s.wsHandler.SetPingInterval(d)
	}
}

// SetReloadProtocolFunc 设置协议热加载回调函数
func (s *Server) SetReloadProtocolFunc(fn func(protocol string) error) {
	s.reloadProtocolFunc = fn
}

// SetCostTracker 注入成本追踪器（Admin 用量统计 API 使用）
func (s *Server) SetCostTracker(ct accounting.CostTracker) {
	s.costTracker = ct
}

// SetPromptStore 注入 Prompt CRUD 存储（可选，nil 时 prompt 管理 API 返回 503）
func (s *Server) SetPromptStore(ps promptStoreInterface) {
	s.promptStore = ps
}

// SetPromptLoader 注入 PromptLoader（可选，用于 Upsert/Delete 后立即失效缓存）
func (s *Server) SetPromptLoader(pl promptLoaderInterface) {
	s.promptLoader = pl
}

// SetAIRouter 注入 AI 路由器（LLM Provider/Model CRUD 后触发热重载，使图片生成等适配器立即感知新配置）
func (s *Server) SetAIRouter(r *airouter.Router) {
	s.aiRouter = r
}

func (s *Server) SetPushService(service *push.Service) {
	s.pushService = service
}

func (s *Server) SetFeishuAuditSink(sink feishu.AuditSink) {
	s.feishuAuditSink = sink
}

// SetSkillStore 注入 Skill CRUD 存储（可选，nil 时 skill 管理 API 返回 503）
func (s *Server) SetSkillStore(ss skillStoreInterface) {
	s.skillStore = ss
}

func (s *Server) SetQualityCandidateStore(store qualityCandidateStore) {
	s.qualityCandidateStore = store
}

func (s *Server) SetQualityEvalRunner(runner agentquality.EvalRunner) {
	s.qualityEvalRunner = runner
}

func (s *Server) SetOptimizationSuggestionStore(store agentquality.OptimizationSuggestionStore) {
	if store != nil {
		s.optimizationStore = store
	}
}

func (s *Server) SetQualityWorkbenchStores(replay qualityworkbench.ReplayJobStore, batchEval qualityworkbench.BatchEvalRunStore, reports qualityworkbench.WeeklyReportStore) {
	if replay != nil {
		s.workbenchReplayStore = replay
	}
	if batchEval != nil {
		s.workbenchBatchEvalStore = batchEval
	}
	if reports != nil {
		s.workbenchReportStore = reports
	}
}

func (s *Server) SetQualityWorkbenchGroupingRuleStore(store qualityworkbench.GroupingRuleStore) {
	if store != nil {
		s.workbenchGroupingRuleStore = store
	}
}

func (s *Server) SetOptimizationWritableStores(toolStore toolDescriptionStore, memoryPolicyStore memoryGovernancePolicyStore) {
	if toolStore != nil {
		s.toolDescriptionStore = toolStore
	}
	if memoryPolicyStore != nil {
		s.memoryGovernancePolicyStore = memoryPolicyStore
	}
}

func (s *Server) SetMemoryStore(store memory.MemoryStore) {
	s.memoryStore = store
}

func (s *Server) SetFeishuHealthClient(client *feishu.Client) {
	s.feishuHealthClient = client
}

// Mux 返回 server 当前 HTTP handler，供跨包集成测试复用已注册路由。
func (s *Server) Mux() http.Handler {
	if s == nil {
		return nil
	}
	return s.mux
}

// GetFeishuIngressMode 返回飞书已提交运行时入口模式。
func (s *Server) GetFeishuIngressMode() config.FeishuIngressMode {
	if s == nil {
		return ""
	}
	s.configMu.RLock()
	defer s.configMu.RUnlock()
	return s.feishuIngressMode
}

// SetFeishuIngressMode 更新飞书已提交运行时入口模式。
func (s *Server) SetFeishuIngressMode(mode config.FeishuIngressMode) {
	if s == nil {
		return
	}
	s.configMu.Lock()
	defer s.configMu.Unlock()
	s.feishuIngressMode = mode
}

// GetFeishuWebhookGateMode 返回飞书 webhook gate 当前放行模式。
func (s *Server) GetFeishuWebhookGateMode() config.FeishuIngressMode {
	if s == nil {
		return ""
	}
	s.configMu.RLock()
	defer s.configMu.RUnlock()
	return s.feishuWebhookGate
}

// SetFeishuWebhookGateMode 更新飞书 webhook gate 当前放行模式。
func (s *Server) SetFeishuWebhookGateMode(mode config.FeishuIngressMode) {
	if s == nil {
		return
	}
	s.configMu.Lock()
	defer s.configMu.Unlock()
	s.feishuWebhookGate = mode
}
