package master

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/chef-guo/agents-hive/internal/accounting"
	"github.com/chef-guo/agents-hive/internal/agentquality"
	"github.com/chef-guo/agents-hive/internal/airouter"
	"github.com/chef-guo/agents-hive/internal/auth"
	"github.com/chef-guo/agents-hive/internal/errs"
	"github.com/chef-guo/agents-hive/internal/imctx"
	"github.com/chef-guo/agents-hive/internal/journal"
	"github.com/chef-guo/agents-hive/internal/kb"
	"github.com/chef-guo/agents-hive/internal/llm"
	"github.com/chef-guo/agents-hive/internal/master/assistantcap"
	"github.com/chef-guo/agents-hive/internal/mcphost"
	"github.com/chef-guo/agents-hive/internal/memory"
	"github.com/chef-guo/agents-hive/internal/observability"
	"github.com/chef-guo/agents-hive/internal/plugin"
	"github.com/chef-guo/agents-hive/internal/router"
	"github.com/chef-guo/agents-hive/internal/sessiontodo"
	"github.com/chef-guo/agents-hive/internal/skills"
	"github.com/chef-guo/agents-hive/internal/toolctx"
	"github.com/chef-guo/agents-hive/internal/toolruntime"
	"github.com/chef-guo/agents-hive/internal/tools"
	"github.com/chef-guo/agents-hive/internal/trajectory"
)

// directExecParams 直接执行路径的前置准备参数
type directExecParams struct {
	sessionLLM      *llm.Client
	availableTools  []mcphost.ToolDefinition
	systemPrompt    string
	promptVersions  []string
	reasoningEffort string
	imContext       *imctx.IMMessageContext
}

// prepareDirectExecParams 准备直接执行路径的共享参数。
func (m *Master) prepareDirectExecParams(ctx context.Context, session *SessionState) (*directExecParams, error) {
	sessionLLM := m.getSessionLLM(session)
	if sessionLLM == nil {
		// error 终态由 processTask defer 统一广播
		return nil, errs.New(errs.CodeInvalidInput, "LLM client not configured (API key required)")
	}

	// 获取可用工具
	var availableTools []mcphost.ToolDefinition
	if m.toolBridge != nil {
		allTools := m.toolBridge.AvailableTools(nil)
		availableTools = m.toolBridge.AvailableTools(m.masterFilter)
		if len(availableTools) != len(allTools) {
			m.logger.Debug("工具策略过滤生效",
				zap.Int("total_tools", len(allTools)),
				zap.Int("filtered_tools", len(availableTools)))
		}
	} else if m.mcpHost != nil {
		availableTools = m.mcpHost.ListTools()
	}
	modelVisibleTools := modelVisibleToolsForSession(session, availableTools)
	promptBuild := m.buildSystemPromptWithMeta(modelVisibleTools)
	systemPrompt := promptBuild.Content
	promptVersions := promptBuild.Versions()

	// 记忆注入
	if m.memoryInjector != nil {
		lastUserMsg := ""
		session.mu.RLock()
		for i := len(session.Messages) - 1; i >= 0; i-- {
			if session.Messages[i].Role == "user" {
				lastUserMsg = session.Messages[i].Content.Text()
				break
			}
		}
		session.mu.RUnlock()
		if lastUserMsg != "" {
			userID := ownerIDForSessionRuntime(session, auth.UserIDFrom(ctx))
			domainID := domainIDForSessionRuntime(session)
			memCtx := m.memoryRuntimeContext(ctx, session, "react")
			memResult, err := m.memoryInjector.InjectContextWithTarget(memCtx, memory.InjectionRequest{
				Query:     lastUserMsg,
				SessionID: session.ID,
				UserID:    userID,
				Target: memory.MemoryTarget{
					Scope:      memory.TargetScopeUser,
					UserID:     userID,
					SessionID:  session.ID,
					AgentName:  "master",
					DomainID:   domainID,
					SourceKind: "master",
					SourceName: "react",
				},
			})
			if err != nil {
				m.logger.Warn("记忆注入失败", zap.Error(err))
			} else {
				if memResult.Text != "" {
					systemPrompt += "\n" + memResult.Text
				}
				if shouldRecordMemoryInjection(memResult) {
					session.SetQualityMemoryInjection(memResult)
				}
			}
		}
	}

	// 插件 ChatMessageBefore hook
	if m.pluginMgr != nil {
		session.mu.RLock()
		msgSnapshot := make([]llm.MessageWithTools, len(session.Messages))
		copy(msgSnapshot, session.Messages)
		session.mu.RUnlock()

		chatInput := &plugin.ChatMessageInput{
			SessionID:    session.ID,
			SystemPrompt: systemPrompt,
			Messages:     msgSnapshot,
			Agent:        "default",
		}
		if err := m.pluginMgr.TriggerChatBefore(ctx, chatInput); err != nil {
			m.logger.Warn("插件 ChatMessageBefore hook 失败", zap.Error(err))
		} else {
			systemPrompt = chatInput.SystemPrompt
		}
	}

	var pendingIMCtx *imctx.IMMessageContext
	// M1：在 plugin hook 之后注入 IM 上下文 prefix，防止被插件覆盖
	if imCtx := session.ConsumePendingIMContext(); imCtx != nil {
		pendingIMCtx = imCtx
		if imCtx.SystemPromptPrefix != "" {
			systemPrompt = imCtx.SystemPromptPrefix + "\n\n" + systemPrompt
		}
	}

	// 读取推理努力级别和模型覆盖
	_, reasoningEffort, modelOverride := session.GetPendingData()

	// 模型覆盖
	if modelOverride != "" && modelOverride != sessionLLM.Model() {
		overrideClient := m.getModelOverrideLLM(modelOverride)
		if overrideClient != nil {
			sessionLLM = overrideClient
			m.logger.Info("使用 skill 模型覆盖",
				zap.String("model", modelOverride),
				zap.String("session_id", session.ID),
			)
		} else {
			m.logger.Warn("模型覆盖失败：llmPool 返回 nil，回退到默认模型",
				zap.String("requested_model", modelOverride),
				zap.String("fallback_model", sessionLLM.Model()),
				zap.String("session_id", session.ID),
			)
			m.eventBus.BroadcastSessionMessage(session.ID, BroadcastMessage{
				Type: EventTypeAgentStatus,
				Payload: map[string]interface{}{
					"status":     "warning",
					"session_id": session.ID,
					"warning":    fmt.Sprintf("模型 %q 不可用，已回退到 %s", modelOverride, sessionLLM.Model()),
				},
			})
		}
	}

	return &directExecParams{
		sessionLLM:      sessionLLM,
		availableTools:  availableTools,
		systemPrompt:    systemPrompt,
		promptVersions:  promptVersions,
		reasoningEffort: reasoningEffort,
		imContext:       pendingIMCtx,
	}, nil
}

func (m *Master) getModelOverrideLLM(modelOverride string) *llm.Client {
	modelOverride = strings.TrimSpace(modelOverride)
	if modelOverride == "" {
		return nil
	}
	if m.router != nil {
		if !m.router.HasModel(modelOverride) {
			return nil
		}
		return m.router.GetLLMClientForModel(airouter.TaskChat, modelOverride)
	}

	m.llmMu.RLock()
	baseCfg := m.config
	m.llmMu.RUnlock()
	provDef := llm.LookupProvider(baseCfg.Provider)
	provDef.APIFormat = baseCfg.APIFormat
	return m.llmPool.Get(llm.ClientConfig{
		APIKey:          baseCfg.APIKey,
		BaseURL:         baseCfg.BaseURL,
		Model:           modelOverride,
		DisableJSONMode: baseCfg.DisableJSONMode,
		Provider:        provDef,
		ReasoningEffort: baseCfg.ReasoningEffort,
		StorePrivacy:    baseCfg.StorePrivacy,
		PromptCacheKey:  baseCfg.PromptCacheKey,
		ServiceTier:     baseCfg.ServiceTier,
	})
}

func (m *Master) memoryRuntimeContext(ctx context.Context, session *SessionState, taskType string) context.Context {
	rc := memory.RuntimeContext{
		UserID:     ownerIDForSessionRuntime(session, auth.UserIDFrom(ctx)),
		TaskType:   taskType,
		AgentName:  "master",
		DomainID:   domainIDForSessionRuntime(session),
		SourceKind: "master",
		SourceName: taskType,
	}
	if session != nil {
		rc.SessionID = session.ID
		if allowed := session.AllowedToolInputsSnapshot(); len(allowed) > 0 {
			if skillArgs := allowed["skill"]; len(skillArgs) > 0 {
				rc.SkillName = strings.TrimSpace(skillArgs["name"])
			}
		}
	}
	return memory.WithRuntimeContext(ctx, rc)
}

func ownerIDForSessionRuntime(session *SessionState, userID string) string {
	ownerID := strings.TrimSpace(userID)
	if session == nil {
		return ownerID
	}
	session.mu.RLock()
	defer session.mu.RUnlock()
	if strings.TrimSpace(session.UserID) != "" {
		ownerID = strings.TrimSpace(session.UserID)
	}
	return ownerID
}

func domainIDForSessionRuntime(session *SessionState) string {
	domainID := "generic"
	if session == nil {
		return domainID
	}
	session.mu.RLock()
	defer session.mu.RUnlock()
	if d := strings.TrimSpace(session.pendingKBDomainID); d != "" {
		return d
	}
	if d := strings.TrimSpace(session.KBDomainID); d != "" {
		return d
	}
	if session.routeDecisionSet {
		if d := strings.TrimSpace(session.routeDecision.Intent.DomainID); d != "" {
			return d
		}
	}
	return domainID
}

// processTaskDirectExec 使用 ReAct Tool-Use 循环处理任务。
// Master 拥有所有常用工具（master_direct profile），直接执行用户任务。
func (m *Master) processTaskDirectExec(ctx context.Context, request string, session *SessionState, responseID uint64, sessionTraceID, sessionSpanID string, skipUserMsg bool) error {
	if m.agentFactory != nil {
		defer m.agentFactory.CleanupBySession(session.ID)
	}
	ctx = m.memoryRuntimeContext(ctx, session, "react")

	// 读取附件并构建用户消息
	pendingAttachments, _, _ := session.GetPendingData()
	userContent := m.buildUserContent(ctx, request, pendingAttachments)

	if !skipUserMsg {
		userCreatedAt := time.Now().Format(time.RFC3339)
		m.appendSessionMessage(session, llm.MessageWithTools{
			Role:      "user",
			Content:   userContent,
			CreatedAt: userCreatedAt,
			Metadata:  attachmentMetadata(pendingAttachments),
		})

		// 广播用户消息确认
		// X-1: 使用 BroadcastSessionMessage 填充 top-level SessionID，
		// 防止同一飞书群聊内两个 session 互相看到对方的 token。
		m.eventBus.BroadcastSessionMessage(session.ID, BroadcastMessage{
			Type: EventTypeMessage,
			Payload: map[string]any{
				"role":       "user",
				"content":    userContent.Text(),
				"timestamp":  userCreatedAt,
				"session_id": session.ID,
			},
		})
	}

	// 图片附件自动路由到 Vision 模型
	if hasImageAttachments(pendingAttachments) && m.router != nil {
		if visionLLM := m.router.GetLLMClient(airouter.TaskVision); visionLLM != nil {
			m.logger.Info("检测到图片附件，路由到 Vision 模型",
				zap.String("vision_model", visionLLM.Model()),
				zap.String("session_id", session.ID),
			)
			// 临时覆盖 session LLM（prepareDirectExecParams 会重新获取，这里提前设置）
			session.mu.Lock()
			session.activeModel = visionLLM.Model()
			session.mu.Unlock()
		}
	}

	params, err := m.prepareDirectExecParams(ctx, session)
	if err != nil {
		return err
	}

	return m.runReActLoop(ctx, session, responseID, params.sessionLLM, params.availableTools, params.systemPrompt, params.promptVersions, params.reasoningEffort, params.imContext, sessionTraceID, sessionSpanID)
}

// runReActLoop 执行核心 ReAct 迭代循环（LLM 调用 → 工具执行 → 广播）。
// runReActLoop 是 Master 的核心 ReAct 循环。
func (m *Master) runReActLoop(
	ctx context.Context,
	session *SessionState,
	responseID uint64,
	sessionLLM *llm.Client,
	availableTools []mcphost.ToolDefinition,
	systemPrompt string,
	promptVersions []string,
	reasoningEffort string,
	imCtx *imctx.IMMessageContext,
	sessionTraceID, sessionSpanID string,
) error {
	const temperature float64 = 0.3 // P0-3: 从 0.7 降到 0.3，减少随机性，提升代码生成确定性
	const maxTokens int64 = 0

	// Task 4.4（harden-spec-driven-phase2）：runReActLoop 入口 atomic 读 specCtx 打诊断日志。
	// 零锁（atomic.Pointer.Load）、零业务影响——仅 Debug 日志，生产 grep 排查
	// "为什么这个 session 没走 spec" 时可快速定位 plumbing 是否抵达 react 层。
	logSpecCtxAtReactEntry(m.logger, session)

	// P0-3 Phase 5: 循环检测器，滑动窗口 20 轮
	detector := newLoopDetector(20)

	// 终端错误缓存：key = "toolName:args"，value = 错误内容
	// 同一 tool+args 组合遇到不可重试错误后，后续相同调用直接返回缓存错误，不再执行
	terminalCache := make(map[string]string)

	// 审批缓存：key = "toolName:args"，记录同任务内已审批通过的 tool+args 组合
	// 避免 LLM 重试相同调用时重复弹出审批卡片
	approvedCalls := make(map[string]bool)

	// ── Phase 5B：配额检查（只在入口检查一次）──
	userID := auth.UserIDFrom(ctx)
	if userID != "" && m.authEngine != nil {
		if err := m.authEngine.CheckQuota(ctx, userID); err != nil {
			return err
		}
	}

	// P0-A C2：required+0-tool-call 兜底计数器。
	// 跨迭代累计：本任务内允许 1 次追责重试，第 2 次仍 required+0 就结构化失败。
	var requiredBreachCount int
	var intentFulfillmentRetryCount int
	var turnIntentQuery string
	var turnIntent router.IntentFrame
	var turnIntentResult router.IntentClassificationResult
	var turnContract IntentContract
	var hasTurnContract bool
	imRefsRead := false

	for i := 0; ; i++ {
		preModelStart := time.Now()
		sessionHasActiveKBBinding := m.recoverSessionKBDomainFromBinding(ctx, session, userID)

		// P0-3 Phase 5.3: per-session 成本预算检查（每轮检查，防止 fan-out turn 耗尽预算后无感知）
		if costErr := m.checkCostBudget(ctx, session.ID); costErr != nil {
			decision := m.handleCostBudgetExceeded(ctx, session, responseID, costErr, sessionTraceID, sessionSpanID)
			if decision.Graceful {
				return nil
			}
			return decision.Err
		}

		// 广播当前轮次进度，用于前端活动面板的 Turn 进度条
		// X-1: 填充 top-level SessionID 防止跨 session 泄漏。
		m.eventBus.BroadcastSessionMessage(session.ID, BroadcastMessage{
			Type: EventTypeAgentProgress,
			Payload: map[string]any{
				"turn":         i + 1,
				"turn_id":      sessionTraceID,
				"max_turns":    0,
				"status":       "turn_start",
				"session_id":   session.ID,
				"agent_id":     "master",
				"agent_type":   "master",
				"route_reason": "direct",
			},
		})

		// 每轮迭代开始时重置为 thinking，避免上一轮 tool_calling 状态粘住前端指示器。
		// i==0 首轮由 processTask 入口广播 thinking，此处从 i>=1 开始补发。
		if i > 0 {
			m.eventBus.BroadcastSessionMessage(session.ID, BroadcastMessage{
				Type: EventTypeAgentStatus,
				Payload: map[string]interface{}{
					"status":     "thinking",
					"session_id": session.ID,
				},
			})
		}

		// 读取消息快照，在锁外执行耗时的修复和压缩操作
		repairStart := time.Now()
		session.mu.Lock()
		repairedMessages, patchedMsgs := repairOrphanedToolCalls(session.Messages, m.logger)
		session.Messages = repairedMessages
		msgsCopy := make([]llm.MessageWithTools, len(session.Messages))
		copy(msgsCopy, session.Messages)
		session.mu.Unlock()
		repairToolCallsMs := time.Since(repairStart).Milliseconds()
		// 将修复插入的假 tool result 持久化到 DB，避免重启后重复警告
		for _, pm := range patchedMsgs {
			m.appendSessionMessage(session, pm)
		}

		m.logger.Info("ReAct 迭代开始",
			zap.Int("iteration", i+1),
			zap.String("max", "unlimited"),
			zap.Int("messages", len(msgsCopy)),
		)

		// 使用新的上下文压缩机制（在锁外执行，避免长时间持锁）
		contextCompressionStart := time.Now()
		preparedMessages := m.prepareMessagesWithCompression(ctx, session, msgsCopy)
		preparedMessages = removeStaleKBNoBindingResults(preparedMessages, sessionHasActiveKBBinding, m.logger)
		// 移除孤立的 tool result（压缩可能导致 assistant tool_call 被截断而 tool result 保留）
		preparedMessages = removeOrphanedToolResults(preparedMessages, m.logger)
		contextCompressionMs := time.Since(contextCompressionStart).Milliseconds()
		latestQuery := extractLatestUserQuery(preparedMessages)
		intentClassifyStart := time.Now()
		intentClassifySkipped := true
		if latestQuery != turnIntentQuery {
			intentClassifySkipped = false
			turnIntentQuery = latestQuery
			if m.config.FirstToken.FastPathEnabled {
				turnIntentResult = fastPathTurnIntentResult(session, latestQuery)
			} else {
				turnIntentResult = router.NewIntentClassifier(
					router.WithIntentLLMClassifier(m.intentLLMClassifier(sessionLLM)),
					router.WithIntentClassifierTimeout(intentClassifierTimeout),
				).Classify(ctx, session.ID, latestQuery)
				turnIntentResult.Intent = resolveTurnIntent(session, latestQuery, turnIntentResult.Intent)
			}
			turnIntent = turnIntentResult.Intent
			turnContract, hasTurnContract = NewIntentContract(turnIntent)
			intentFulfillmentRetryCount = 0
		}
		intentClassifyMs := time.Since(intentClassifyStart).Milliseconds()
		qualityContextStart := time.Now()
		pendingAttachments, _, _ := session.GetPendingData()
		memoryInjection := session.ConsumeQualityMemoryInjection()
		m.emitQualityEvent(sessionTraceID, sessionSpanID, session.ID, agentquality.Event{
			Name:        agentquality.EventContextBuild,
			Route:       routeFromSession(session),
			FailureType: agentquality.FailureNone,
			FinalStatus: agentquality.StatusPass,
			ContextBuild: agentquality.ContextBuild{
				MessageCount:          len(preparedMessages),
				Compressed:            len(preparedMessages) < len(msgsCopy),
				MemoryInjected:        memoryInjection.Text != "",
				MemoryIDs:             memoryInjection.MemoryIDs(),
				SkippedMemoryIDs:      append([]int64(nil), memoryInjection.SkippedMemoryIDs...),
				SkippedExpired:        memoryInjection.SkippedExpired,
				SkippedLowTrust:       memoryInjection.SkippedLowTrust,
				SkippedCrossUser:      memoryInjection.SkippedCrossUser,
				SkippedScope:          memoryInjection.SkippedScope,
				SkippedLowScore:       memoryInjection.SkippedLowScore,
				SkippedTokenBudget:    memoryInjection.SkippedTokenBudget,
				SkippedFeedbackBudget: memoryInjection.SkippedFeedbackBudget,
				SkippedRegularBudget:  memoryInjection.SkippedRegularBudget,
				SkippedMemoryTotal:    memoryInjection.SkippedTotal(),
				FeedbackMemoryCount:   memoryInjection.FeedbackCount,
				RegularMemoryCount:    memoryInjection.RegularCount,
				MemoryDomainID:        memoryInjection.DomainID,
				MemorySourceKind:      memoryInjection.SourceKind,
				MemorySourceName:      memoryInjection.SourceName,
				MemoryOwnerScope:      string(memoryInjection.OwnerScope),
				MemoryOwnerID:         memoryInjection.OwnerID,
				AttachmentCount:       len(pendingAttachments),
				PromptVersions:        promptVersions,
				EstimatedTokens:       memoryInjection.EstimatedTokens,
				ContaminationCheck:    contaminationStatus(memoryInjection),
			},
		})
		qualityContextMs := time.Since(qualityContextStart).Milliseconds()

		toolVisibilityStart := time.Now()
		skillMetas := m.skillMetasForModel(userID)
		modelVisibleTools, toolRecallObs := modelVisibleToolsForSessionWithRecallObservationAndSkillsAndIntentWithOptions(session, availableTools, skillMetas, latestQuery, m.config.ToolRecall, turnIntent, toolVisibilityOptions{
			FastPath:             m.config.FirstToken.FastPathEnabled,
			MaxModelVisibleTools: m.config.MaxModelVisibleTools,
			FilesystemEnabled:    m.config.Tools.FilesystemEnabled,
		})
		toolVisibilityMs := time.Since(toolVisibilityStart).Milliseconds()

		llmRequestBuildStart := time.Now()
		// P0-A：根据 feature flag 决定本轮 ToolChoice 策略。
		// 但 IM 上下文里若已有文档引用，必须强制 required，否则模型会绕过工具直接口头分析。
		var toolChoice string
		refs := refsForToolChoice(imCtx, imRefsRead)
		if shouldEvaluateToolChoiceForTurn(latestQuery, refs, m.config.QualityGuards, turnIntent) {
			// H3：注入真实 skillsIndex —— 让 "X 是什么 / 怎么用" 且 X 已知时回落到 auto，
			// 避免对自家 skill 的闲聊式介绍也强制 required。nil 时 detectToolChoice
			// 退化为保守立场（只要命中 whatIs 模式就 required）。
			skillsIndex := m.buildSkillsIndex(userID)
			toolChoice = detectToolChoiceWithIntentAndMessages(latestQuery, skillsIndex, refs, turnIntent, preparedMessages)
			toolChoice = toolChoiceWithDiscoveryFallback(toolChoice, modelVisibleTools, turnIntent, preparedMessages)
			m.logger.Info("[quality-guards] P0-A tool_choice 决策",
				zap.String("session_id", session.ID),
				zap.Int("iteration", i+1),
				zap.String("query_preview", truncateForLog(latestQuery, 80)),
				zap.String("tool_choice", toolChoice),
				zap.Int("im_refs_count", len(refs)),
				zap.String("intent_kind", string(turnIntent.Kind)),
				zap.String("intent_source", turnIntentResult.Source),
				zap.Bool("intent_degraded", turnIntentResult.Degraded),
			)
			trigger := toolChoiceRequiredTriggerWithMessages(latestQuery, skillsIndex, refs, turnIntent, preparedMessages)
			if turnIntent.Kind != "" {
				m.enqueueMetric(observability.Metric{
					Name:   "route_intent_kind_total",
					Value:  1,
					Labels: map[string]any{"kind": string(turnIntent.Kind)},
				})
			}
			if isForcedToolChoice(toolChoice) {
				m.enqueueMetric(observability.Metric{
					Name:   "tool_choice_required_total",
					Value:  1,
					Labels: map[string]any{"trigger": trigger},
				})
				if trigger == "external_send" && !hasRequiredIntentCallableTool(modelVisibleTools, turnIntent) {
					m.enqueueMetric(observability.Metric{
						Name:  "tool_choice_required_but_no_tool_total",
						Value: 1,
					})
				}
			}
		}

		llmReq := llm.ChatWithToolsRequest{
			SystemPrompt:    systemPrompt,
			Messages:        preparedMessages,
			Tools:           modelVisibleTools,
			Temperature:     temperature,
			MaxTokens:       maxTokens,
			ReasoningEffort: m.resolveModelReasoningEffort(reasoningEffort, latestQuery, sessionLLM.Model()),
			UserID:          userID,
			PromptVersions:  promptVersions,
			ToolChoice:      toolChoice,
		}
		agentState := &AgentState{
			SessionID:    session.ID,
			UserID:       userID,
			SystemPrompt: systemPrompt,
			Messages:     preparedMessages,
			Request:      &llmReq,
			Evidence:     BuildToolEvidence(preparedMessages),
		}
		llmRequestBuildMs := time.Since(llmRequestBuildStart).Milliseconds()
		// 保持原有 LLM 耗时口径：从请求构造开始计，便于和既有流式诊断对齐。
		llmCallStart := llmRequestBuildStart
		middlewareBeforeModelStart := time.Now()
		beforeModelErr := m.middlewarePipeline.BeforeModel(ctx, agentState)
		middlewareBeforeModelMs := time.Since(middlewareBeforeModelStart).Milliseconds()
		beforeModelMs := time.Since(preModelStart).Milliseconds()
		m.logger.Info("[pre-model-diag] ReAct 调用前耗时",
			zap.String("session_id", session.ID),
			zap.Int("iteration", i+1),
			zap.Int64("repair_tool_calls_ms", repairToolCallsMs),
			zap.Int64("context_compression_ms", contextCompressionMs),
			zap.Int64("intent_classify_ms", intentClassifyMs),
			zap.Bool("intent_classify_skipped", intentClassifySkipped),
			zap.Int64("quality_context_ms", qualityContextMs),
			zap.Int64("tool_visibility_ms", toolVisibilityMs),
			zap.Int64("llm_request_build_ms", llmRequestBuildMs),
			zap.Int64("middleware_before_model_ms", middlewareBeforeModelMs),
			zap.Int64("before_model_ms", beforeModelMs),
			zap.Int("messages_before", len(msgsCopy)),
			zap.Int("messages_prepared", len(preparedMessages)),
			zap.Int("tools_available", len(modelVisibleTools)),
			zap.Int("tools_trimmed", toolRecallObs.VisibleTrimmedCount),
			zap.Int("max_visible_tools", toolRecallObs.MaxVisibleTools),
		)
		if beforeModelErr != nil {
			m.emitQualityEvent(sessionTraceID, sessionSpanID, session.ID, agentquality.Event{
				Name:        agentquality.EventAgentTurn,
				Route:       routeFromSession(session),
				FailureType: agentquality.FailureRuntime,
				RetryReason: "middleware_before_model",
				FinalStatus: agentquality.StatusFail,
			})
			return errs.Wrap(errs.CodePlanExecFailed, "BeforeModel middleware failed", beforeModelErr)
		}
		m.logger.Info("发起 LLM 调用",
			zap.String("session_id", session.ID),
			zap.String("model", sessionLLM.Model()),
			zap.Int("iteration", i+1),
			zap.Int("tools_available", len(modelVisibleTools)),
		)
		llmSpanID := observability.NewSpanID()
		lastStreamBroadcast := time.Time{}
		const streamThrottleInterval = 50 * time.Millisecond
		// 诊断计数：chunk 到达节奏（排查"假流式"：provider 是否按 SSE 逐步吐 token）
		var streamEventCount int
		var textChunkCount int
		var toolChunkCount int
		var finalToolCallCount int
		var firstChunkAt time.Time
		var lastContentLen int
		var lastToolPreviewFingerprint string

		resp, err := sessionLLM.ChatWithToolsStream(ctx, llmReq, func(chunk llm.StreamChunk) error {
			chunkClass := classifyStreamChunk(chunk)
			if chunk.Done {
				if chunkClass.HasToolCalls {
					finalToolCallCount = len(chunk.ToolCalls)
				}
				return nil
			}
			// 诊断：记录首个 chunk 抵达耗时 + 前 3 个 chunk 的节奏
			if chunkClass.CountsAsStreamEvent {
				streamEventCount++
				if chunkClass.HasText {
					textChunkCount++
				}
				if chunkClass.HasToolCalls {
					toolChunkCount++
				}
				if streamEventCount == 1 {
					firstChunkAt = time.Now()
					m.logger.Info("[stream-diag] 首个 LLM chunk 抵达",
						zap.String("session_id", session.ID),
						zap.Int64("ttfb_ms", time.Since(llmCallStart).Milliseconds()),
						zap.Bool("has_text", chunkClass.HasText),
						zap.Bool("has_tool_calls", chunkClass.HasToolCalls),
						zap.Int("tool_calls", len(chunk.ToolCalls)),
						zap.Int("content_len", len(chunk.ContentSoFar)),
					)
				} else if streamEventCount <= 3 || streamEventCount%20 == 0 {
					m.logger.Info("[stream-diag] chunk 抵达",
						zap.String("session_id", session.ID),
						zap.Int("n", streamEventCount),
						zap.Int64("elapsed_since_start_ms", time.Since(llmCallStart).Milliseconds()),
						zap.Int64("elapsed_since_first_ms", time.Since(firstChunkAt).Milliseconds()),
						zap.Bool("has_text", chunkClass.HasText),
						zap.Bool("has_tool_calls", chunkClass.HasToolCalls),
						zap.Int("tool_calls", len(chunk.ToolCalls)),
						zap.Int("content_len", len(chunk.ContentSoFar)),
						zap.Int("content_delta_len", len(chunk.ContentSoFar)-lastContentLen),
					)
				}
			}
			lastContentLen = len(chunk.ContentSoFar)
			if chunkClass.HasToolCalls {
				fingerprint := toolCallPreviewFingerprint(chunk.ToolCalls)
				if fingerprint != "" && fingerprint != lastToolPreviewFingerprint {
					lastToolPreviewFingerprint = fingerprint
					if payload, ok := buildToolCallPreviewPayload(session.ID, chunk, time.Now()); ok {
						m.eventBus.BroadcastSessionMessage(session.ID, BroadcastMessage{
							Type:    EventTypeMessage,
							Payload: payload,
						})
					}
				}
			}
			// 有可见内容或推理内容时才推送；tool_calls-only chunk 已通过预览事件广播，不执行工具。
			if !chunkClass.HasText {
				return nil
			}
			// P0-A/C3 round-4：tool_choice=required 时抑制流式 partial assistant 广播。
			// 反例（修复前）：required + 模型硬抗不调工具时，回调会把凭记忆胡编的 token
			// 一帧一帧推给前端 UI，用户已经看见坏回答；等 guard 在终态 retry/fail 时，
			// 坏内容已经 render。屏蔽 partial 后：
			//   - 模型最终调了工具（pass）→ 由 L644 终态 broadcast 一次性出完整内容
			//   - 模型硬抗（retry/fail）→ 坏回答从未触达前端
			// 代价：required 模式失去流式体感。但 required 场景文本价值本来就低（真回答靠工具）。
			if shouldSuppressStreamPartial(toolChoice) {
				return nil
			}
			// 限速：最多 20fps，避免高速流式输出时撑爆 EventBus 订阅者通道
			now := time.Now()
			if now.Sub(lastStreamBroadcast) < streamThrottleInterval {
				return nil
			}
			lastStreamBroadcast = now
			// P0-A structural lock: tool_choice != required 时颁发 stream cap，覆盖流 partial。
			// required 模式上面已经 return nil，到此 GrantStream 必然成功。
			streamCap, ok := assistantcap.GrantStream(toolChoice, "required")
			if !ok {
				return nil
			}
			payload := map[string]any{
				"content":    chunk.ContentSoFar,
				"session_id": session.ID,
				"partial":    true,
			}
			if chunk.ReasoningContent != "" {
				payload["reasoning_content"] = chunk.ReasoningContent
			}
			m.broadcastAssistant(streamCap, session.ID, payload)
			return nil
		})
		if streamEventCount > 0 {
			m.logger.Info("[stream-diag] LLM 流结束汇总",
				zap.String("session_id", session.ID),
				zap.Int("total_chunks", streamEventCount),
				zap.Int("text_chunks", textChunkCount),
				zap.Int("tool_chunks", toolChunkCount),
				zap.Int("final_tool_calls", finalToolCallCount),
				zap.Int64("total_ms", time.Since(llmCallStart).Milliseconds()),
				zap.Int64("ttfb_ms", firstChunkAt.Sub(llmCallStart).Milliseconds()),
				zap.Int64("stream_span_ms", time.Since(firstChunkAt).Milliseconds()),
			)
		} else {
			m.logger.Warn("[stream-diag] LLM 调用未收到非终态流式 chunk（provider 可能未真正 SSE 输出）",
				zap.String("session_id", session.ID),
				zap.Int("final_tool_calls", finalToolCallCount),
				zap.Int64("total_ms", time.Since(llmCallStart).Milliseconds()),
			)
		}
		llmDurationMs := time.Since(llmCallStart).Milliseconds()

		if err != nil {
			// LLM 调用失败时写入 error trace
			m.enqueueSpan(observability.Span{
				TraceID:      sessionTraceID,
				SpanID:       llmSpanID,
				ParentSpanID: sessionSpanID,
				Operation:    "llm.call",
				Service:      "master",
				SessionID:    session.ID,
				UserID:       userID,
				DurationMs:   int(llmDurationMs),
				Status:       "error",
				Attributes:   map[string]any{"model": sessionLLM.Model(), "error": err.Error()},
				Ts:           llmCallStart,
			})
			// error 终态由 processTask defer 统一广播，此处不重复
			return errs.Wrap(errs.CodePlanExecFailed, "LLM 调用失败", err)
		}

		// nil response 防御：LLM 返回 nil 但无 error（不应发生，但防御性处理）
		// 加 1 次重试：底层 resilience.Do 不会触发（因为无 error），所以这里手动重试一次
		if err := validateLLMResponse(resp); err != nil {
			m.logger.Warn("LLM 返回 nil response（无 error），尝试重试 1 次",
				zap.String("session_id", session.ID),
				zap.String("model", sessionLLM.Model()),
				zap.Int("iteration", i+1),
			)
			// 重试 1 次（复用首发完整请求参数，包括 ToolChoice — 否则 P0-A 在最该兜底的路径失效）
			// 注意：这里必须用 "=" 而非 ":="，否则 resp 被 shadow，外层拿不到重试成功的响应。
			retryStart := time.Now()
			var retryErr error
			resp, retryErr = sessionLLM.ChatWithToolsStream(ctx, llmReq, func(chunk llm.StreamChunk) error { return nil })
			retryDuration := time.Since(retryStart).Milliseconds()
			if retryErr != nil || validateLLMResponse(resp) != nil {
				finalErr := err
				if retryErr != nil {
					finalErr = retryErr
				}
				m.logger.Error("LLM nil response 重试仍失败",
					zap.String("session_id", session.ID),
					zap.Int("iteration", i+1),
				)
				m.enqueueSpan(observability.Span{
					TraceID:      sessionTraceID,
					SpanID:       llmSpanID,
					ParentSpanID: sessionSpanID,
					Operation:    "llm.call",
					Service:      "master",
					SessionID:    session.ID,
					UserID:       userID,
					DurationMs:   int(llmDurationMs + retryDuration),
					Status:       "error",
					Attributes:   map[string]any{"model": sessionLLM.Model(), "error": "nil response after retry"},
					Ts:           llmCallStart,
				})
				// error 终态由 processTask defer 统一广播，此处不重复
				return errs.Wrap(errs.CodeLLMError, "LLM nil response 重试仍失败", finalErr)
			}
			m.logger.Info("LLM nil response 重试成功",
				zap.String("session_id", session.ID),
				zap.Int("iteration", i+1),
			)
			llmDurationMs += retryDuration
		}
		agentState.Response = resp
		m.emitQualityEvent(sessionTraceID, sessionSpanID, session.ID, agentquality.Event{
			Name:        agentquality.EventAgentTurn,
			Route:       routeFromSession(session),
			FailureType: agentquality.FailureNone,
			FinalStatus: agentquality.StatusPass,
			Attributes: agentTurnAttributes(
				sessionTraceID,
				i+1,
				i+1,
				len(resp.ToolCalls),
				len(preparedMessages),
				len(modelVisibleTools),
				len(preparedMessages) < len(msgsCopy),
			),
		})
		selectedRecallTool, usedRecallTool := selectedRecalledTool(resp.ToolCalls, toolRecallObs.RecalledToolNames)
		m.recordRouteDecision(sessionTraceID, sessionSpanID, session, toolRecallObs.toRouteDecisionEvent())
		m.recordRouteDecisionSpan(sessionTraceID, sessionSpanID, session, toolRecallObs.toDecisionSpan(sessionTraceID, qualitySessionHash(session.ID)))
		m.recordToolRecall(sessionTraceID, sessionSpanID, session, toolRecallObs.toEvent(sessionTraceID, sessionTraceID, selectedRecallTool, usedRecallTool))

		// LLM 调用成功：写入 trace + metrics
		m.enqueueSpan(observability.Span{
			TraceID:      sessionTraceID,
			SpanID:       llmSpanID,
			ParentSpanID: sessionSpanID,
			Operation:    "llm.call",
			Service:      "master",
			SessionID:    session.ID,
			UserID:       userID,
			DurationMs:   int(llmDurationMs),
			Status:       "ok",
			Attributes: map[string]any{
				"model":             sessionLLM.Model(),
				"prompt_tokens":     resp.Usage.PromptTokens,
				"completion_tokens": resp.Usage.CompletionTokens,
				"finish_reason":     resp.FinishReason,
			},
			Ts: llmCallStart,
		})
		m.enqueueMetric(observability.Metric{
			Name:   "hive.llm.duration_ms",
			Value:  float64(llmDurationMs),
			Labels: map[string]any{"model": sessionLLM.Model(), "session_id": session.ID, "user_id": userID},
		})
		if resp.Usage.PromptTokens > 0 {
			m.enqueueMetric(observability.Metric{
				Name:   "hive.llm.tokens.input",
				Value:  float64(resp.Usage.PromptTokens),
				Labels: map[string]any{"model": sessionLLM.Model(), "session_id": session.ID, "user_id": userID},
			})
		}
		if resp.Usage.CompletionTokens > 0 {
			m.enqueueMetric(observability.Metric{
				Name:   "hive.llm.tokens.output",
				Value:  float64(resp.Usage.CompletionTokens),
				Labels: map[string]any{"model": sessionLLM.Model(), "session_id": session.ID, "user_id": userID},
			})
		}

		// ---- 观测 / 记账（与 guard 决策无关，API 已消耗必须记） ----
		// 累加 completion tokens（不累加 prompt tokens，避免上下文重叠膨胀）
		if resp.Usage.CompletionTokens > 0 {
			session.mu.Lock()
			session.Stats.TotalTokens += int(resp.Usage.CompletionTokens)
			session.mu.Unlock()
		}

		// 成本追踪：通过 AsyncRecorder 异步记录本次 LLM 调用的用量和成本（nil 安全）
		if m.asyncRecorder != nil {
			qv := agentquality.FromContext(ctx)
			promptVersion := strings.Join(promptVersions, ",")
			if qv.PromptVersion != "" {
				promptVersion = qv.PromptVersion
			}
			m.asyncRecorder.RecordUsageWithMeta(session.ID, userID, sessionLLM.Model(), resp.Usage, accounting.UsageMeta{
				TaskType:      "react",
				QualityCaseID: qv.CaseID,
				PromptVersion: promptVersion,
				FailureType:   string(qv.FailureType),
				FinalStatus:   string(qv.FinalStatus),
			})
		}

		m.logger.Info("LLM 调用完成",
			zap.String("session_id", session.ID),
			zap.Int("iteration", i+1),
			zap.Duration("duration", time.Duration(llmDurationMs)*time.Millisecond),
			zap.String("finish_reason", resp.FinishReason),
			zap.Int("tool_calls", len(resp.ToolCalls)),
			zap.Int64("prompt_tokens", resp.Usage.PromptTokens),
			zap.Int64("completion_tokens", resp.Usage.CompletionTokens),
		)

		// P0-A C2-HIGH-B + C3（round-5）：required+0 兜底必须先于
		//   1) ChatMessageAfter plugin hook（round-5 Codex 指认：坏 content 泄露给插件）
		//   2) appendSessionMessage（污染 session.Messages，进入下次 LLM prompt）
		//   3) BroadcastSessionMessage（泄露到前端 UI）
		// 反例（修复前）：plugin hook 在 L537、persist 在 L574、broadcast 在 L643，guard 在 L649，
		// 等于三条泄露路径都先于 guard；round-3 修了 persist/broadcast 的顺序，round-5 补上
		// 流式 partial 和 plugin hook。
		// 现在的完整策略：
		//   - retry/fail → 跳过 plugin hook、跳过 persist、跳过 broadcast（本函数早退）
		//                    流 chunk 回调里也已通过 shouldSuppressStreamPartial 抑制（L391）
		//   - pass → plugin hook → persist → broadcast → 继续终态/tool-exec 分支
		// 观测/记账（logger.Info / asyncRecorder / session stats）本身不含 resp.Content，允许先记。
		action, nextBreach := evaluateRequiredGuard(toolChoice, len(resp.ToolCalls), requiredBreachCount)
		requiredBreachCount = nextBreach
		// P0-A structural lock: gate-pass 后才能颁发 cap。
		// retry/fail 分支拿到的 cap 是零值 + ok=false，下面 persistAssistant/broadcastAssistant
		// 不会被调用（emitAssistantMessage 提前 return）。但 cap 本身的颁发条件就锁死了 pass 语义。
		passCap, passCapOk := assistantcap.GrantPass(int(action), int(requiredGuardPass))
		if !emitAssistantMessage(action) {
			m.emitQualityEvent(sessionTraceID, sessionSpanID, session.ID, agentquality.Event{
				Name:        agentquality.EventAgentTurn,
				Route:       routeFromSession(session),
				FailureType: agentquality.FailureTool,
				RetryReason: "required_zero_tool",
				FinalStatus: agentquality.StatusFail,
			})
			switch action {
			case requiredGuardFail:
				m.logger.Warn("[quality-guards] P0-A required 连续 2 轮未出工具调用，结构化失败退出（assistant 消息 + plugin hook 均已跳过）",
					zap.String("session_id", session.ID),
					zap.Int("iteration", i+1),
					zap.String("finish_reason", resp.FinishReason),
					zap.Int("dropped_content_len", len(resp.Content)),
				)
				m.recordReflection(sessionTraceID, sessionSpanID, session, reflectionNoteInput{
					Trigger:  "guard_failure",
					Severity: "hard_stop",
				})
				return errs.New(errs.CodePlanExecFailed,
					"tool_choice=required 但 LLM 连续两轮未产出工具调用，疑似 provider 吞字段或模型硬抗")
			case requiredGuardRetry:
				m.logger.Warn("[quality-guards] P0-A required 未满足，追加追责消息后重试（assistant 消息 + plugin hook 均已跳过）",
					zap.String("session_id", session.ID),
					zap.Int("iteration", i+1),
					zap.String("finish_reason", resp.FinishReason),
					zap.Int("dropped_content_len", len(resp.Content)),
				)
				m.recordReflection(sessionTraceID, sessionSpanID, session, reflectionNoteInput{
					Trigger:  "guard_failure",
					Severity: "warn",
				})
				continue
			}
		}

		if err := m.middlewarePipeline.AfterModel(ctx, agentState); err != nil {
			m.emitQualityEvent(sessionTraceID, sessionSpanID, session.ID, agentquality.Event{
				Name:        agentquality.EventAgentTurn,
				Route:       routeFromSession(session),
				FailureType: agentquality.FailureModel,
				RetryReason: "grounding_validation",
				FinalStatus: agentquality.StatusFail,
				Attributes:  map[string]any{"error": err.Error()},
			})
			m.logger.Warn("[quality-guards] PostValidation 阻断未证实模型输出",
				zap.String("session_id", session.ID),
				zap.Int("iteration", i+1),
				zap.Error(err),
			)
			m.recordReflection(sessionTraceID, sessionSpanID, session, reflectionNoteInput{
				Trigger:  "validation_failure",
				Severity: "hard_stop",
				Detail:   err.Error(),
			})
			return errs.Wrap(errs.CodePlanExecFailed, "post validation failed", err)
		}

		var fulfillmentDecision intentFulfillmentGateDecision
		if hasTurnContract {
			contractEval := turnContract.Evaluate(preparedMessages, *resp)
			fulfillmentDecision = (IntentFulfillmentGate{}).Decide(intentFulfillmentGateInput{
				Evaluation: contractEval,
				Response:   *resp,
				RetryCount: intentFulfillmentRetryCount,
			})
			switch fulfillmentDecision.Action {
			case IntentFulfillmentPass:
				session.ClearPendingExternalSendIntent()
			case IntentFulfillmentSuppressAndRetry:
				session.RememberPendingExternalSendIntent(turnIntent)
				m.emitQualityEvent(sessionTraceID, sessionSpanID, session.ID, agentquality.Event{
					Name:        agentquality.EventAgentTurn,
					Route:       routeFromSession(session),
					FailureType: agentquality.FailureModel,
					RetryReason: "intent_fulfillment",
					FinalStatus: agentquality.StatusFail,
					Attributes: map[string]any{
						"reason": fulfillmentDecision.Reason,
					},
				})
				m.logger.Warn("[quality-guards] Intent fulfillment 阻断未完成用户意图的回复",
					zap.String("session_id", session.ID),
					zap.Int("iteration", i+1),
					zap.String("reason", fulfillmentDecision.Reason),
					zap.Int("dropped_content_len", len(resp.Content)),
				)
				m.recordReflection(sessionTraceID, sessionSpanID, session, fulfillmentDecision.Reflection)
				intentFulfillmentRetryCount++
				continue
			case IntentFulfillmentPause:
				session.RememberPendingExternalSendIntent(turnIntent)
			case IntentFulfillmentFail:
				session.ClearPendingExternalSendIntent()
				if !fulfillmentDecision.AllowAssistant {
					m.recordReflection(sessionTraceID, sessionSpanID, session, fulfillmentDecision.Reflection)
					return errs.New(errs.CodePlanExecFailed, "intent fulfillment failed after retry")
				}
			}
		}

		// ---- 以下路径仅在 guard pass 时执行 ----

		// 插件 ChatMessageAfter hook（pass 后才触发，避免把坏 content 泄给插件）
		if m.pluginMgr != nil {
			session.mu.RLock()
			msgSnapshot := make([]llm.MessageWithTools, len(session.Messages))
			copy(msgSnapshot, session.Messages)
			session.mu.RUnlock()

			chatOutput := &plugin.ChatMessageOutput{
				Content: resp.Content,
				Model:   session.activeModel,
			}
			if afterErr := m.pluginMgr.TriggerChatAfter(ctx, plugin.ChatMessageInput{
				SessionID:    session.ID,
				SystemPrompt: systemPrompt,
				Messages:     msgSnapshot,
				Agent:        "default",
			}, chatOutput); afterErr != nil {
				m.logger.Warn("插件 ChatMessageAfter hook 失败", zap.Error(afterErr))
			} else {
				// 验证 plugin 改写后 artifact 标签完整性
				originalContent := resp.Content
				resp.Content = chatOutput.Content
				if openCount := strings.Count(resp.Content, "<artifact"); openCount > 0 {
					closeCount := strings.Count(resp.Content, "</artifact>")
					if openCount != closeCount {
						m.logger.Warn("plugin 改写破坏了 artifact 标签完整性，回退到原始内容",
							zap.Int("open_tags", openCount),
							zap.Int("close_tags", closeCount),
							zap.String("session_id", session.ID),
						)
						resp.Content = originalContent
					}
				}
			}
		}

		// 统一时间戳：创建时生成，贯穿 WS 广播和 DB 存储
		assistantCreatedAt := time.Now().Format(time.RFC3339)

		msgMeta := map[string]string{"agent_id": "master"}
		// 将 token 用量写入消息元数据，确保持久化到 DB（刷新后可恢复）
		if resp.Usage.PromptTokens > 0 {
			msgMeta["input_tokens"] = fmt.Sprintf("%d", resp.Usage.PromptTokens)
		}
		if resp.Usage.CompletionTokens > 0 {
			msgMeta["output_tokens"] = fmt.Sprintf("%d", resp.Usage.CompletionTokens)
		}
		if citations := m.kbCitationsForCurrentTurn(ctx, session, userID, sessionTraceID); citations != "" {
			msgMeta["citations"] = citations
		}
		if artifactsJSON := m.persistAssistantArtifacts(ctx, session, userID, resp.Content, assistantCreatedAt); artifactsJSON != "" {
			msgMeta["artifacts"] = artifactsJSON
		}

		// pass 分支：persist assistant 消息到 session.Messages（DB 持久化）
		// P0-A structural lock: 走 persistAssistant + cap 而非 appendSessionMessage。
		if !passCapOk {
			panic("[P0-A structural lock] passCap should be valid in gate-pass branch — invariant violation")
		}
		m.persistAssistant(passCap, session, resp.Content, resp.ReasoningContent, resp.ToolCalls, msgMeta, assistantCreatedAt)

		// 广播 LLM 响应（中间状态或最终响应）
		if !session.IsTerminated() && (resp.Content != "" || resp.ReasoningContent != "" || len(resp.ToolCalls) > 0) {
			// partial=false 仅当本次响应是真正终态时才置：无 tool_calls 且 finish_reason 终态。
			// 反例：finish_reason="tool_calls" + ToolCalls>0 是中间状态（HITL question 等用户输入时尤其明显），
			// 若按"非 stop/end_turn 即 partial"的旧公式会误算 partial=false，渲染端切 CardStatusDone，
			// 导致工具还在跑、用户还在被等回答，飞书卡片标题已经显"✅ 完成"。
			payload := map[string]any{
				"content":    resp.Content,
				"session_id": session.ID,
				"partial":    len(resp.ToolCalls) > 0 || !shouldExitTask(resp.FinishReason),
			}
			if resp.ReasoningContent != "" {
				payload["reasoning_content"] = resp.ReasoningContent
			}
			if len(resp.ToolCalls) > 0 {
				// 将 tool_calls 转为前端期望的格式
				tcs := make([]map[string]any, len(resp.ToolCalls))
				for i, tc := range resp.ToolCalls {
					tcs[i] = map[string]any{
						"id":        tc.ID,
						"name":      tc.Name,
						"arguments": string(tc.Arguments),
					}
				}
				payload["tool_calls"] = tcs
			}
			// 携带 token 用量（非流式中间状态时）
			if resp.Usage.PromptTokens > 0 || resp.Usage.CompletionTokens > 0 {
				payload["usage"] = map[string]any{
					"input_tokens":  resp.Usage.PromptTokens,
					"output_tokens": resp.Usage.CompletionTokens,
				}
			}
			if citationsJSON := msgMeta["citations"]; citationsJSON != "" {
				var citations []kb.EvidenceRef
				if err := json.Unmarshal([]byte(citationsJSON), &citations); err == nil {
					payload["citations"] = citations
				}
			}
			if artifactsJSON := msgMeta["artifacts"]; artifactsJSON != "" {
				var artifacts []AssistantArtifactManifest
				if err := json.Unmarshal([]byte(artifactsJSON), &artifacts); err == nil {
					payload["artifacts"] = artifacts
				}
			}
			// 携带 LLM 请求耗时
			payload["llm_duration"] = llmDurationMs
			// 使用与 session.Messages 相同的时间戳，确保 WS 和 DB 一致
			payload["timestamp"] = assistantCreatedAt
			// P0-A structural lock: 走 broadcastAssistant + cap 而非裸 BroadcastSessionMessage。
			m.broadcastAssistant(passCap, session.ID, payload)
		}

		// 关键：tool_calls 优先于 finish_reason。
		// 部分模型（如 GPT-5.4）会同时返回 finish_reason="stop" 和 tool_calls，
		// 必须先执行工具，否则工具调用被静默跳过（如公众号发布永远不会执行）。
		if len(resp.ToolCalls) == 0 && shouldExitTask(resp.FinishReason) {
			// completed 终态由 processTask defer 统一广播
			m.logDecisionIfFinal(ctx, session)
			m.saveTrajectorySnapshot(ctx, session, sessionTraceID, sessionSpanID, i+1)
			m.recordLongArtifactEvaluation(ctx, session.ID, sessionTraceID, sessionSpanID, resp.Content)
			if !session.IsTerminated() {
				decision := CompletionDecision{Status: TaskStatusCompleted, Completed: true}
				if fulfillmentDecision.TaskStatus == TaskStatusPaused || fulfillmentDecision.TaskStatus == TaskStatusFailed {
					decision = CompletionDecision{
						Status:    fulfillmentDecision.TaskStatus,
						Completed: fulfillmentDecision.TaskStatus == TaskStatusCompleted,
					}
				}
				if (fulfillmentDecision.TaskStatus == "" || fulfillmentDecision.TaskStatus == TaskStatusCompleted) && m.planRuntimeGuard() != nil {
					guard := m.planRuntimeGuard()
					var guardErr error
					decision, guardErr = guard.DecideTurnCompletion(ctx, session, resp.Content, sessionTraceID, sessionSpanID, sessionTraceID)
					if guardErr != nil {
						return guardErr
					}
				}
				m.sessionMgr.SendResponse(responseID, decision.TaskResponse(resp.Content))
			}
			return nil
		}

		if len(resp.ToolCalls) > 0 {
			// P0-A C2-HIGH-A：计数器已在 evaluateRequiredGuard 里按 tool_calls>0 统一归零。
			// 这里不再重复赋值，避免两处同时维护同一状态。

			// stop + tool_calls 场景：仅记录日志，不提前退出。
			// 工具结果必须回传 LLM 才能生成最终回复。
			if shouldExitTask(resp.FinishReason) {
				m.logger.Info("LLM 返回 stop + tool_calls，执行工具后将继续下一轮 LLM 调用",
					zap.String("session_id", session.ID),
					zap.Int("tool_calls", len(resp.ToolCalls)),
					zap.String("finish_reason", resp.FinishReason),
				)
			}
			// P0-3 Phase 5.1: 循环检测
			loopWarned := false
			loopCheckResult := detector.check(resp.ToolCalls)
			toolNames := make([]string, len(resp.ToolCalls))
			for i, tc := range resp.ToolCalls {
				toolNames[i] = tc.Name
			}
			switch loopCheckResult {
			case "hard_stop":
				m.logger.Warn("循环检测：相同工具组合连续出现 5 次，强制终止",
					zap.String("session_id", session.ID),
					zap.Int("iteration", i+1),
					zap.Strings("tools", toolNames),
					zap.Int("consecutive_same", detector.consecutiveSame),
				)
				m.recordReflection(sessionTraceID, sessionSpanID, session, reflectionNoteInput{
					Trigger:     "batch_loop",
					Severity:    "hard_stop",
					Consecutive: detector.consecutiveSame,
				})
				// error 终态由 processTask defer 统一广播
				return errs.New(errs.CodePlanExecFailed, "loop detected: same tool combination repeated 5 times")
			case "warn":
				m.logger.Warn("循环检测：相同工具组合连续出现 3 次，注入警告",
					zap.String("session_id", session.ID),
					zap.Int("iteration", i+1),
					zap.Strings("tools", toolNames),
					zap.Int("consecutive_same", detector.consecutiveSame),
				)
				loopWarned = true // 延迟到工具执行后注入，避免破坏 assistant(tool_calls) → tool(results) 消息顺序
			}

			// 广播工具调用状态
			m.logger.Info("开始执行工具调用",
				zap.String("session_id", session.ID),
				zap.Int("iteration", i+1),
				zap.Int("count", len(resp.ToolCalls)),
				zap.Strings("tools", toolNames),
			)
			m.eventBus.BroadcastSessionMessage(session.ID, BroadcastMessage{
				Type: EventTypeAgentStatus,
				Payload: map[string]interface{}{
					"status":     "tool_calling",
					"session_id": session.ID,
					"tools":      toolNames,
				},
			})

			// P0-3 Phase 5.2: 子代理并发限制（单轮最多 3 个 spawn_agent）
			const maxConcurrentSpawn = 3
			spawnFilter := m.filterSpawnAgentCalls(resp.ToolCalls, maxConcurrentSpawn)

			// 构建 rejected ID set，用于单循环中快速判断
			rejectedIDs := make(map[string]bool, len(spawnFilter.Rejected))
			for _, r := range spawnFilter.Rejected {
				rejectedIDs[r.ID] = true
			}

			// 按原始顺序遍历，保持 tool result 消息顺序与 assistant tool_calls 一致
			var terminalFailures []string // 收集本轮终端错误的工具名
			var callFailureReflections []reflectionNoteInput

			if m.EnableStreamingExecutor {
				// 并发路径：safe 工具并发执行，unsafe 工具串行
				m.executeToolsConcurrent(ctx, session, userID, resp.ToolCalls,
					rejectedIDs, terminalCache, approvedCalls, &terminalFailures,
					&callFailureReflections, &imRefsRead, sessionTraceID, sessionSpanID, detector)
			} else {
				for _, toolCall := range resp.ToolCalls {
					if rejectedIDs[toolCall.ID] {
						// spawn_agent 超限：写入错误消息
						m.logger.Warn("spawn_agent 并发限制：单轮超过限制，拒绝执行",
							zap.String("session_id", session.ID),
							zap.String("call_id", toolCall.ID),
						)
						errContent := fmt.Sprintf("错误：单轮最多 %d 个 spawn_agent 调用，请减少并发或分批执行", maxConcurrentSpawn)
						toolCreatedAt := time.Now().Format(time.RFC3339)
						m.appendSessionMessage(session, llm.MessageWithTools{
							Role:       "tool",
							ToolCallID: toolCall.ID,
							Content:    llm.NewTextContent(errContent),
							IsError:    true,
							ToolName:   toolCall.Name,
							CreatedAt:  toolCreatedAt,
							Metadata:   map[string]string{"agent_id": "master"},
						})
						m.broadcastToolMessage(session.ID, toolCall.ID, toolCall.Name, errContent, toolCreatedAt, toolResult{IsError: true, Terminal: true})
						continue
					}

					// 检查是否有缓存的终端失败（同一 tool+args 已经失败过，不再重试）
					callFP := canonicalFingerprint(toolCall.Name, toolCall.Arguments)
					if cachedErr, hit := terminalCache[callFP]; hit {
						m.logger.Info("跳过已知终端失败的工具调用",
							zap.String("tool", toolCall.Name),
							zap.String("session_id", session.ID),
						)
						toolCreatedAt := time.Now().Format(time.RFC3339)
						m.appendSessionMessage(session, llm.MessageWithTools{
							Role:       "tool",
							ToolCallID: toolCall.ID,
							Content:    llm.NewTextContent(cachedErr),
							IsError:    true,
							ToolName:   toolCall.Name,
							CreatedAt:  toolCreatedAt,
							Metadata:   map[string]string{"agent_id": "master"},
						})
						m.broadcastToolMessage(session.ID, toolCall.ID, toolCall.Name, cachedErr, toolCreatedAt, toolResult{IsError: true, Terminal: true})
						terminalFailures = append(terminalFailures, toolCall.Name)
						continue
					}

					// 如果同任务内已审批过相同 tool+args，跳过权限检查避免重复弹卡
					toolCtx := ctx
					if approvedCalls[callFP] {
						toolCtx = toolctx.WithSkipPermission(ctx)
					}

					tr := m.executeTool(toolCtx, session, userID, toolCall, sessionTraceID, sessionSpanID)
					// 统一时间戳：创建时生成，贯穿 WS 广播和 DB 存储
					toolCreatedAt := time.Now().Format(time.RFC3339)
					m.appendSessionMessage(session, llm.MessageWithTools{
						Role:       "tool",
						ToolCallID: toolCall.ID,
						Content:    llm.NewTextContent(tr.Content),
						IsError:    tr.IsError,
						ToolName:   toolCall.Name,
						CreatedAt:  toolCreatedAt,
						Metadata:   toolResultMessageMetadata(tr),
					})
					// 广播工具执行结果给前端
					m.broadcastToolMessage(session.ID, toolCall.ID, toolCall.Name, tr.Content, toolCreatedAt, tr)

					// 审批缓存：仅在工具通过权限检查并成功执行时记录
					// 权限拒绝和终端错误不缓存，避免绕过安全检查
					if !tr.IsError && !tr.Terminal {
						approvedCalls[callFP] = true
					}
					if isSuccessfulIMReferenceRead(toolCall, tr.IsError) {
						imRefsRead = true
					}

					// 终端错误缓存：记录不可重试的失败，后续相同调用直接跳过
					if tr.Terminal {
						terminalCache[callFP] = tr.Content
						terminalFailures = append(terminalFailures, toolCall.Name)
						if kind := reflectionFailureKindFromToolError(tr.Content); kind != "" {
							callFailureReflections = append(callFailureReflections, reflectionNoteInput{
								Trigger:     "call_failure",
								Severity:    "warn",
								ToolName:    toolCall.Name,
								Consecutive: 1,
								Detail:      tr.Content,
								FailureKind: kind,
							})
						}
						m.logger.Warn("工具返回终端错误，已缓存，后续相同调用将跳过",
							zap.String("tool", toolCall.Name),
							zap.String("session_id", session.ID),
						)
					}

					// 调用级循环检测：同一 tool+args 连续失败 2 次，标记为终端失败
					if tr.IsError && !tr.Terminal && !tr.Recoverable {
						if detector.recordCallResult(callFP, true) {
							terminalCache[callFP] = tr.Content
							terminalFailures = append(terminalFailures, toolCall.Name)
							callFailureReflections = append(callFailureReflections, reflectionNoteInput{
								Trigger:     "call_failure",
								Severity:    "warn",
								ToolName:    toolCall.Name,
								Consecutive: 2,
								Detail:      tr.Content,
							})
							m.logger.Warn("同一工具+参数连续失败 2 次，标记为终端失败",
								zap.String("tool", toolCall.Name),
								zap.String("session_id", session.ID),
							)
						}
					} else if !tr.IsError || tr.Recoverable {
						detector.recordCallResult(callFP, false)
					}
				}
			} // end else serial path

			// P0-3 Phase 5.1: 循环检测 warn 消息延迟注入
			// 必须在所有 tool results 写入之后，避免破坏 assistant(tool_calls) → tool(results) 消息顺序
			if loopWarned {
				m.recordReflection(sessionTraceID, sessionSpanID, session, reflectionNoteInput{
					Trigger:     "batch_loop",
					Severity:    "warn",
					Consecutive: detector.consecutiveSame,
				})
			}
			for _, reflection := range callFailureReflections {
				m.recordReflection(sessionTraceID, sessionSpanID, session, reflection)
			}

			// 终端错误强制提示：当本轮有工具返回不可重试错误时，注入明确指令让 LLM 停止重试
			if len(terminalFailures) > 0 {
				hint := fmt.Sprintf("[系统指令] 以下工具遇到不可重试的配置/权限错误，请勿再次调用: %s。请直接告知用户错误原因和解决方法。",
					strings.Join(terminalFailures, ", "))
				m.appendSessionMessage(session, llm.MessageWithTools{
					Role:      "system",
					Content:   llm.NewTextContent(hint),
					CreatedAt: time.Now().Format(time.RFC3339),
					Metadata:  map[string]string{"agent_id": "master"},
				})
			}

			// finish_reason=stop + tool_calls 场景：不再提前退出。
			// 工具结果已写入 session messages，继续下一轮 LLM 调用，
			// 让 LLM 基于工具结果生成最终回复。
			// 之前的实现会直接 return nil，导致 ls/bash 等信息收集类工具执行后
			// LLM 没机会看到结果，用户收到空回复。
			m.saveTrajectorySnapshot(ctx, session, sessionTraceID, sessionSpanID, i+1)
		}
	}

}

func (m *Master) saveTrajectorySnapshot(ctx context.Context, session *SessionState, traceID, spanID string, iteration int) {
	if m == nil || m.trajectoryStore == nil || session == nil {
		return
	}
	session.mu.RLock()
	messages := make([]llm.MessageWithTools, len(session.Messages))
	copy(messages, session.Messages)
	sessionID := session.ID
	session.mu.RUnlock()
	messagesJSON, err := json.Marshal(messages)
	if err != nil {
		m.logger.Warn("trajectory snapshot 序列化失败", zap.String("session_id", sessionID), zap.Error(err))
		return
	}
	if err := m.trajectoryStore.Save(ctx, trajectory.Snapshot{
		SessionID:    sessionID,
		TraceID:      traceID,
		SpanID:       spanID,
		Iteration:    iteration,
		MessageCount: len(messages),
		Messages:     messagesJSON,
	}); err != nil {
		m.logger.Warn("trajectory snapshot 保存失败", zap.String("session_id", sessionID), zap.Error(err))
	}
}

func selectedRecalledTool(toolCalls []llm.ToolCall, recalled map[string]bool) (string, bool) {
	if len(toolCalls) == 0 || len(recalled) == 0 {
		return "", false
	}
	for _, toolCall := range toolCalls {
		if recalled[toolCall.Name] {
			return toolCall.Name, true
		}
	}
	return "", false
}

func agentTurnAttributes(turnID string, turnIndex, llmCallCount, toolCallCount, preparedMessageCount, visibleToolCount int, compactionTriggered bool) map[string]any {
	return map[string]any{
		"turn_id":                turnID,
		"turn_index":             turnIndex,
		"llm_call_count":         llmCallCount,
		"tool_call_count":        toolCallCount,
		"prepared_message_count": preparedMessageCount,
		"visible_tool_count":     visibleToolCount,
		"compaction_triggered":   compactionTriggered,
	}
}

// toolResult 工具执行结果
type toolResult struct {
	Content     string
	IsError     bool
	Terminal    bool // 终端错误：配置/权限/白名单等不可重试错误，LLM 不应重试相同调用
	Recoverable bool
	ErrorKind   string
}

func toolResultMessageMetadata(tr toolResult) map[string]string {
	meta := map[string]string{"agent_id": "master"}
	if tr.Recoverable {
		meta["recoverable"] = "true"
	}
	if tr.Terminal {
		meta["terminal"] = "true"
	}
	if tr.ErrorKind != "" {
		meta["error_kind"] = tr.ErrorKind
	}
	return meta
}

// terminalErrorPatterns 已知的不可重试错误模式（配置类、权限类、白名单类）
// 匹配工具返回内容中的关键词，命中即标记为 terminal
// 注意：瞬态错误（rate limit、timeout）不在此列表中，它们可以重试
var terminalErrorPatterns = []string{
	"not in whitelist",
	"ip whitelist",
	"白名单",
	"invalid ip",
	"unauthorized",
	"403 forbidden",
	"permission denied",
	"access denied",
	"invalid credentials",
	"account suspended",
}

// isTerminalError 检查工具执行结果是否为不可重试的终端错误
func isTerminalError(content string) bool {
	if isRecoverableToolCallError(content) {
		return false
	}
	lower := strings.ToLower(content)
	for _, pattern := range terminalErrorPatterns {
		if strings.Contains(lower, pattern) {
			return true
		}
	}
	return false
}

func reflectionFailureKindFromToolError(content string) string {
	lower := strings.ToLower(content)
	switch {
	case strings.Contains(lower, "permission denied"), strings.Contains(lower, "access denied"), strings.Contains(lower, "工具策略拒绝"), strings.Contains(lower, "不在当前 profile 的允许列表中"):
		return "permission_denied"
	case strings.Contains(lower, "unauthorized"), strings.Contains(lower, "invalid credentials"), strings.Contains(lower, "account suspended"):
		return "auth"
	case strings.Contains(lower, "403 forbidden"):
		return "4xx"
	case strings.Contains(lower, "schema"), strings.Contains(lower, "invalid input"), strings.Contains(lower, "invalid argument"):
		return "schema_invalid"
	default:
		return ""
	}
}

func commandFailureMetadata(result *mcphost.ToolResult) (failureType string, requiresApproval bool, suggestedAction string) {
	if result == nil {
		return "", false, ""
	}
	return result.FailureType, result.RequiresUserApproval, result.SuggestedAction
}

// canonicalFingerprint 生成工具调用的规范化 fingerprint（tool + 规范化 args）
// 通过 unmarshal/marshal 消除 JSON key order 和 whitespace 差异
func canonicalFingerprint(toolName string, args json.RawMessage) string {
	var normalized json.RawMessage
	var obj map[string]interface{}
	if json.Unmarshal(args, &obj) == nil {
		if b, err := json.Marshal(obj); err == nil {
			normalized = b
		}
	}
	if normalized == nil {
		normalized = args
	}
	return toolName + ":" + string(normalized)
}

func toolInputName(args json.RawMessage) (string, bool) {
	return toolInputString(args, "name")
}

func toolInputString(args json.RawMessage, key string) (string, bool) {
	key = strings.TrimSpace(key)
	if key == "" {
		return "", false
	}
	var payload map[string]any
	if err := json.Unmarshal(args, &payload); err != nil {
		return "", false
	}
	value, ok := payload[key].(string)
	if !ok {
		if key == "name" {
			if _, exists := payload[key]; !exists {
				return "", true
			}
		}
		return "", false
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return "", false
	}
	return value, true
}

func toolInputStrings(args json.RawMessage, key string) ([]string, bool) {
	key = strings.TrimSpace(key)
	if key == "" {
		return nil, false
	}
	if strings.HasSuffix(key, "[].action") {
		arrayKey := strings.TrimSuffix(key, "[].action")
		return toolInputArrayStringField(args, arrayKey, "action")
	}
	if value, ok := toolInputString(args, key); ok {
		return []string{value}, true
	}
	return nil, false
}

func toolInputArrayStringField(args json.RawMessage, arrayKey, field string) ([]string, bool) {
	arrayKey = strings.TrimSpace(arrayKey)
	field = strings.TrimSpace(field)
	if arrayKey == "" || field == "" {
		return nil, false
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(args, &payload); err != nil {
		return nil, false
	}
	raw, ok := payload[arrayKey]
	if !ok {
		return nil, false
	}
	var items []map[string]any
	if err := json.Unmarshal(raw, &items); err != nil || len(items) == 0 {
		return nil, false
	}
	values := make([]string, 0, len(items))
	for _, item := range items {
		value, ok := item[field].(string)
		if !ok {
			return nil, false
		}
		value = strings.TrimSpace(value)
		if value == "" {
			return nil, false
		}
		values = append(values, value)
	}
	return values, true
}

func routeInputDenyReason(toolName string, args json.RawMessage, allowed map[string]string) (string, map[string]string, bool) {
	actuals := make(map[string]string, len(allowed))
	for key, allowedValues := range allowed {
		key = strings.TrimSpace(key)
		allowedValues = strings.TrimSpace(allowedValues)
		if key == "" || allowedValues == "" {
			continue
		}
		actualValues, ok := toolInputStrings(args, key)
		actual := strings.Join(actualValues, "|")
		actuals[key] = actual
		if !ok || !matchesAllowedToolInputValues(actualValues, allowedValues) {
			reason := fmt.Sprintf("route decision denied %s %s %q; allowed %s is %q", toolName, key, actual, key, allowedValues)
			return reason, actuals, true
		}
	}
	return "", actuals, false
}

func (m *Master) recordRouteInputDeniedMetric(toolName string, args json.RawMessage, source string) {
	m.enqueueMetric(observability.Metric{
		Name:  "hive_route_input_denied_total",
		Value: 1,
		Labels: map[string]any{
			"tool_name": emptyAsUnknown(toolName),
			"action":    normalizedToolActionForMetric(toolName, args),
			"reason":    "route_input_denied",
			"source":    emptyAsUnknown(source),
		},
		Ts: time.Now(),
	})
}

func (m *Master) recordToolCallCounter(toolName string, args json.RawMessage, status string) {
	m.enqueueMetric(observability.Metric{
		Name:  "hive_tool_call_total",
		Value: 1,
		Labels: map[string]any{
			"tool_name": emptyAsUnknown(toolName),
			"action":    normalizedToolActionForMetric(toolName, args),
			"status":    normalizedToolStatusForMetric(status),
		},
		Ts: time.Now(),
	})
}

func (m *Master) recordToolErrorCounter(toolName string, args json.RawMessage, reason string) {
	m.enqueueMetric(observability.Metric{
		Name:  "hive_tool_error_total",
		Value: 1,
		Labels: map[string]any{
			"tool_name": emptyAsUnknown(toolName),
			"action":    normalizedToolActionForMetric(toolName, args),
			"reason":    normalizedToolReasonForMetric(reason),
		},
		Ts: time.Now(),
	})
}

func (m *Master) recordFilesystemActionObservation(ctx context.Context, sessionID, toolName string, args json.RawMessage, status, reason string, duration time.Duration) {
	if toolName != "filesystem" {
		return
	}
	action := normalizedToolActionForMetric(toolName, args)
	status = normalizedToolStatusForMetric(status)
	reason = normalizedToolReasonForMetric(reason)
	m.enqueueMetric(observability.Metric{
		Name:  "hive_filesystem_action_total",
		Value: 1,
		Labels: map[string]any{
			"tool_name": "filesystem",
			"action":    action,
			"status":    status,
			"reason":    reason,
		},
		Ts: time.Now(),
	})
	attrs := map[string]any{
		"tool_name":   "filesystem",
		"action":      action,
		"status":      status,
		"reason":      reason,
		"args_hash":   hashToolArgs(args),
		"duration_ms": duration.Milliseconds(),
	}
	if tc := toolctx.GetToolContext(ctx); tc != nil {
		if tc.ToolCallID != "" {
			attrs["tool_call_id"] = tc.ToolCallID
		}
		if turnID := tc.TurnIDOrTraceID(); turnID != "" {
			attrs["turn_id"] = turnID
		}
	}
	m.enqueueLog(observability.LogEntry{
		Level:      "info",
		Message:    "filesystem action audit",
		Attributes: attrs,
		Ts:         time.Now(),
	})
}

func normalizedToolActionForMetric(toolName string, args json.RawMessage) string {
	switch toolName {
	case "filesystem", "im_api":
		var payload struct {
			Action string `json:"action"`
		}
		if err := json.Unmarshal(args, &payload); err == nil {
			action := strings.ToLower(strings.TrimSpace(payload.Action))
			if isKnownToolActionForMetric(toolName, action) {
				return action
			}
			if action != "" {
				return "other"
			}
		}
	case "send_im_message":
		return "send_message"
	}
	return "unknown"
}

func normalizedToolErrorReason(toolName string, _ json.RawMessage, content string, fallback string) string {
	if toolName == "filesystem" {
		if code := filesystemErrorCodeFromContent(content); code != "" {
			return code
		}
	}
	return fallback
}

func filesystemErrorCodeFromContent(content string) string {
	content = strings.TrimSpace(content)
	if !strings.HasPrefix(content, "filesystem.") {
		return ""
	}
	parts := strings.SplitN(content, ":", 3)
	if len(parts) < 2 {
		return ""
	}
	code := strings.TrimSpace(parts[1])
	switch code {
	case "invalid_input", "missing_field", "unknown_action", "execution_failed":
		return code
	default:
		return ""
	}
}

func isKnownToolActionForMetric(toolName, action string) bool {
	switch toolName {
	case "filesystem":
		switch action {
		case "list", "glob", "grep", "read", "write", "edit", "multiedit":
			return true
		}
	case "im_api":
		switch action {
		case "search_recipients", "list_recent_conversations", "resolve_recipient", "send_message":
			return true
		}
	}
	return false
}

func normalizedToolStatusForMetric(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "success", "ok":
		return "success"
	case "error", "failed", "fail":
		return "error"
	case "denied", "blocked":
		return "denied"
	default:
		return "unknown"
	}
}

func normalizedToolReasonForMetric(reason string) string {
	switch strings.ToLower(strings.TrimSpace(reason)) {
	case "":
		return "none"
	case "route_input_denied", "executor_error", "tool_error", "nil_result",
		"invalid_input", "missing_field", "unknown_action", "execution_failed",
		"route_input_outside_allowed_values", "route_tool_not_allowed":
		return strings.ToLower(strings.TrimSpace(reason))
	default:
		return "other"
	}
}

func textToolResult(content string, isError bool) *mcphost.ToolResult {
	encoded, _ := json.Marshal(content)
	return &mcphost.ToolResult{Content: encoded, IsError: isError}
}

func (m *Master) availableToolDefinitions() []mcphost.ToolDefinition {
	if m == nil {
		return nil
	}
	if m.toolBridge != nil {
		return m.toolBridge.AvailableTools(nil)
	}
	if m.mcpHost != nil {
		return m.mcpHost.ListTools()
	}
	return nil
}

const recoverableToolCallErrorMarker = toolruntime.RecoverableToolCallErrorMarker

func recoverableToolCallErrorContent(kind, detail string) string {
	return toolruntime.RecoverableToolCallErrorContent(kind, detail)
}

func recoverableToolCallErrorContentWithHint(hint toolruntime.RecoverableToolCallError) string {
	return toolruntime.RecoverableToolCallErrorContentWithHint(hint)
}

func recoverableToolCallErrorHint(session *SessionState, toolName, kind, detail, repairAction string) toolruntime.RecoverableToolCallError {
	hint := toolruntime.RecoverableToolCallError{
		Kind:         kind,
		Detail:       detail,
		RepairAction: repairAction,
		ToolName:     strings.TrimSpace(toolName),
	}
	if session != nil {
		hint.AllowedTools = session.AllowedToolsSnapshot()
		hint.AllowedInputs = session.AllowedToolInputsSnapshot()
		hint.ActionCapabilities = recoverableActionCapabilitiesFromSession(session)
		if query := recommendedToolSearchQueryFromSession(session, toolName); query != "" {
			hint.RecommendedToolSearchQuery = query
		}
	}
	return hint
}

func recoverableActionCapabilitiesFromSession(session *SessionState) []toolruntime.RecoverableActionCapability {
	if session == nil {
		return nil
	}
	decision, ok := session.RouteDecisionSnapshot()
	if !ok {
		return nil
	}
	allowedInputs := session.AllowedToolInputsSnapshot()
	var out []toolruntime.RecoverableActionCapability
	for _, rule := range router.ActionCapabilityRulesForIntent(decision.Intent) {
		actionField := router.MixedActionField(rule.ToolName)
		if actionField == "" || !recoverableCapabilityAllowedByRoute(rule, actionField, allowedInputs) {
			continue
		}
		out = append(out, toolruntime.RecoverableActionCapability{
			ToolName:           rule.ToolName,
			Action:             rule.Action,
			ActionField:        actionField,
			CapabilityID:       rule.CapabilityID,
			RequiredFields:     append([]string(nil), rule.RequiredFields...),
			PreparatoryActions: append([]string(nil), rule.PreparatoryActions...),
			ExampleToolCall:    recoverableExampleToolCall(rule, actionField),
			InvocationHint:     recoverableInvocationHint(rule.ToolName, actionField, rule.Action),
			RepairHint:         rule.RepairHint,
		})
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func recoverableCapabilityAllowedByRoute(rule router.ActionCapabilityRule, actionField string, allowedInputs map[string]map[string]string) bool {
	if strings.TrimSpace(rule.ToolName) == "" || strings.TrimSpace(rule.Action) == "" {
		return false
	}
	allowed := allowedInputs[rule.ToolName]
	if len(allowed) == 0 {
		return false
	}
	allowedActions := allowed[actionField]
	return matchesAllowedToolInputValue(rule.Action, allowedActions)
}

func recoverableExampleToolCall(rule router.ActionCapabilityRule, actionField string) map[string]any {
	toolName := strings.TrimSpace(rule.ToolName)
	if toolName == "" {
		return nil
	}
	args := make(map[string]any, len(rule.ExampleArgs)+1)
	for key, value := range rule.ExampleArgs {
		args[key] = value
	}
	if actionField != "" && strings.TrimSpace(rule.Action) != "" {
		if _, ok := args[actionField]; !ok {
			args[actionField] = rule.Action
		}
	}
	return map[string]any{
		"name":      toolName,
		"arguments": args,
	}
}

func recoverableInvocationHint(toolName, actionField, action string) string {
	toolName = strings.TrimSpace(toolName)
	actionField = strings.TrimSpace(actionField)
	action = strings.TrimSpace(action)
	if toolName == "" || action == "" {
		return ""
	}
	if actionField == "" {
		return fmt.Sprintf("调用工具 %s；不要把 action capability 当作独立工具名。", toolName)
	}
	return fmt.Sprintf("调用工具 %s，并设置 arguments.%s=%s；不要调用 %s.%s。", toolName, actionField, action, toolName, action)
}

func recommendedToolSearchQueryFromSession(session *SessionState, fallbackTool string) string {
	if session != nil {
		if decision, ok := session.RouteDecisionSnapshot(); ok {
			for _, rule := range router.ActionCapabilityRulesForIntent(decision.Intent) {
				parts := []string{rule.Action, rule.CapabilityID, rule.Resource, rule.Operation}
				if len(rule.Platforms) > 0 {
					parts = append(parts, rule.Platforms...)
				}
				return strings.TrimSpace(strings.Join(nonEmptyStrings(parts...), " "))
			}
			if len(decision.Intent.AllowedDomainsHint) > 0 {
				return strings.Join(decision.Intent.AllowedDomainsHint, " ")
			}
		}
	}
	return strings.TrimSpace(fallbackTool)
}

func nonEmptyStrings(values ...string) []string {
	var out []string
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			out = append(out, value)
		}
	}
	return out
}

func isRecoverableToolCallError(content string) bool {
	return toolruntime.IsRecoverableToolCallError(content)
}

func recoverableToolCallErrorKind(content string) string {
	return toolruntime.RecoverableToolCallErrorKind(content)
}

func (m *Master) recoverableToolCallErrorResult(ctx context.Context, sessionID, toolCallID, toolName string, args json.RawMessage, turnID, content, errText string) toolResult {
	errKind := recoverableToolCallErrorKind(content)
	m.logger.Info("工具调用未执行，交回模型自动修复",
		zap.String("tool", toolName),
		zap.String("reason", errText),
	)
	m.recordToolCallCounter(toolName, args, "error")
	m.recordToolErrorCounter(toolName, args, errKind)
	m.emitToolCallEvent(sessionID, ToolCallEvent{
		ToolCallID:  toolCallID,
		ToolName:    toolName,
		TurnID:      turnID,
		Status:      "error",
		Error:       errText,
		Recoverable: true,
		Terminal:    false,
		ErrorKind:   errKind,
		SessionID:   sessionID,
	})
	m.logToolCall(ctx, sessionID, llm.ToolCall{ID: toolCallID, Name: toolName, Arguments: args}, string(args), content, true, 0)
	return toolResult{Content: content, IsError: true, Terminal: false, Recoverable: true, ErrorKind: errKind}
}

func (m *Master) enforceToolExecutionGate(ctx context.Context, session *SessionState, sessionID, toolCallID, toolName string, args json.RawMessage, sessionTraceID, sessionSpanID string) (toolResult, bool) {
	if m.masterFilter != nil && !m.masterFilter.IsAllowed(toolName) {
		reason := fmt.Sprintf("tool %q is not allowed by tool policy", toolName)
		allowedTools := []string{}
		if m.masterFilter != nil {
			for _, tool := range m.masterFilter.FilterTools(m.availableToolDefinitions()) {
				allowedTools = append(allowedTools, tool.Name)
			}
		}
		hint := recoverableToolCallErrorHint(session, toolName, "tool_policy_not_allowed",
			fmt.Sprintf("工具 %q 不在当前 profile 允许列表中，当前调用未执行。当前 profile 可用工具: %s。请改用可用工具，或先调用 tool_search/question 获取正确工具与参数。", toolName, strings.Join(allowedTools, "|")),
			"reselect_tool")
		hint.AllowedTools = allowedTools
		content := recoverableToolCallErrorContentWithHint(hint)
		return m.recoverableToolCallErrorResult(ctx, sessionID, toolCallID, toolName, args, sessionTraceID, content, reason), false
	}

	if session != nil && session.HasAllowedToolsDecision() && !session.IsAllowedTool(toolName) {
		allowedTools := session.AllowedToolsSnapshot()
		reason := fmt.Sprintf("route decision denied tool %q; allowed tools are %q", toolName, strings.Join(allowedTools, "|"))
		content := recoverableToolCallErrorContentWithHint(recoverableToolCallErrorHint(session, toolName, "route_tool_not_allowed",
			fmt.Sprintf("工具 %q 不在本轮 RouteDecision 允许列表中，当前调用未执行。本轮允许工具: %s。请重新选择允许工具，或先调用 tool_search/question 澄清后再继续。", toolName, strings.Join(allowedTools, "|")),
			"reselect_tool"))
		m.logger.Info("工具不在本轮 RouteDecision 范围内，交回模型自动修复",
			zap.String("tool", toolName),
			zap.Strings("allowed_tools", allowedTools),
		)
		m.emitToolCallEvent(sessionID, ToolCallEvent{
			ToolCallID:  toolCallID,
			ToolName:    toolName,
			TurnID:      sessionTraceID,
			Status:      "error",
			Error:       reason,
			Recoverable: true,
			Terminal:    false,
			ErrorKind:   "route_tool_not_allowed",
			SessionID:   sessionID,
		})
		m.emitQualityEvent(sessionTraceID, sessionSpanID, sessionID, agentquality.Event{
			Name:        agentquality.EventToolDecision,
			Route:       routeFromSession(session),
			FailureType: agentquality.FailureTool,
			FinalStatus: agentquality.StatusFail,
			ToolDecision: agentquality.ToolDecision{
				Actual:   toolName,
				Decision: agentquality.DecisionUnexpected,
				ArgsHash: hashToolArgs(args),
			},
			Attributes: map[string]any{
				"recoverable":   true,
				"repair_action": "reselect_tool",
				"reason":        reason,
				"allowed_tools": allowedTools,
			},
		})
		m.recordToolCallCounter(toolName, args, "error")
		m.recordToolErrorCounter(toolName, args, "route_tool_not_allowed")
		m.logToolCall(ctx, sessionID, llm.ToolCall{ID: toolCallID, Name: toolName, Arguments: args}, string(args), content, true, 0)
		return toolResult{Content: content, IsError: true, Terminal: false, Recoverable: true, ErrorKind: "route_tool_not_allowed"}, false
	}

	if session != nil {
		if allowedInputs := session.AllowedToolInputsSnapshot()[toolName]; len(allowedInputs) > 0 {
			if reason, actuals, denied := routeInputDenyReason(toolName, args, allowedInputs); denied {
				content := recoverableToolCallErrorContentWithHint(recoverableToolCallErrorHint(session, toolName, "route_input_outside_allowed_values",
					fmt.Sprintf("%s。当前调用未执行。请按 allowed_inputs 重构参数；如果用户目标需要其他动作，请重新规划到正确工具/意图，不要重复相同工具和参数。", reason),
					"rebuild_arguments"))
				m.logger.Info("工具输入超出 RouteDecision 参数约束，交回模型自动修复",
					zap.String("tool", toolName),
					zap.Any("actual_inputs", actuals),
					zap.Any("allowed_inputs", allowedInputs),
				)
				m.emitToolCallEvent(sessionID, ToolCallEvent{
					ToolCallID:  toolCallID,
					ToolName:    toolName,
					TurnID:      sessionTraceID,
					Status:      "error",
					Error:       reason,
					Recoverable: true,
					Terminal:    false,
					ErrorKind:   "route_input_outside_allowed_values",
					SessionID:   sessionID,
				})
				m.emitQualityEvent(sessionTraceID, sessionSpanID, sessionID, agentquality.Event{
					Name:        agentquality.EventToolDecision,
					Route:       routeFromSession(session),
					FailureType: agentquality.FailureTool,
					FinalStatus: agentquality.StatusFail,
					ToolDecision: agentquality.ToolDecision{
						Actual:   toolName,
						Decision: agentquality.DecisionUnexpected,
						ArgsHash: hashToolArgs(args),
					},
					Attributes: map[string]any{
						"recoverable":    true,
						"repair_action":  "rebuild_arguments",
						"reason":         reason,
						"allowed_inputs": allowedInputs,
						"actual_inputs":  actuals,
					},
				})
				m.recordRouteInputDeniedMetric(toolName, args, "direct")
				m.recordToolCallCounter(toolName, args, "error")
				m.recordToolErrorCounter(toolName, args, "route_input_outside_allowed_values")
				m.recordFilesystemActionObservation(ctx, sessionID, toolName, args, "denied", "route_input_denied", 0)
				m.logToolCall(ctx, sessionID, llm.ToolCall{ID: toolCallID, Name: toolName, Arguments: args}, string(args), content, true, 0)
				return toolResult{Content: content, IsError: true, Terminal: false, Recoverable: true, ErrorKind: "route_input_outside_allowed_values"}, false
			}
		}
	}

	if m.permMgr != nil && m.hitlBroker != nil && m.hitlBroker.Enabled() && !toolctx.ShouldSkipPermission(ctx) {
		ctxWithSession := toolctx.WithSessionID(ctx, sessionID)
		if err := m.permMgr.CheckPermission(ctxWithSession, toolName, args); err != nil {
			if isRecoverableToolCallError(err.Error()) {
				content := err.Error()
				errKind := recoverableToolCallErrorKind(content)
				m.logger.Info("工具权限检查返回可恢复错误，交回模型自动修复",
					zap.String("tool", toolName),
					zap.Error(err))
				m.emitToolCallEvent(sessionID, ToolCallEvent{
					ToolCallID:  toolCallID,
					ToolName:    toolName,
					TurnID:      sessionTraceID,
					Status:      "error",
					Error:       err.Error(),
					Recoverable: true,
					Terminal:    false,
					ErrorKind:   errKind,
					SessionID:   sessionID,
				})
				m.logToolCall(ctx, sessionID, llm.ToolCall{ID: toolCallID, Name: toolName, Arguments: args}, string(args), content, true, 0)
				return toolResult{Content: content, IsError: true, Terminal: false, Recoverable: true, ErrorKind: errKind}, false
			}
			content := fmt.Sprintf("[权限拒绝: %v]", err)
			m.logger.Info("工具执行被拒绝",
				zap.String("tool", toolName),
				zap.Error(err))
			m.emitToolCallEvent(sessionID, ToolCallEvent{
				ToolCallID: toolCallID,
				ToolName:   toolName,
				TurnID:     sessionTraceID,
				Status:     "error",
				Error:      err.Error(),
				Terminal:   true,
				SessionID:  sessionID,
			})
			m.logToolCall(ctx, sessionID, llm.ToolCall{ID: toolCallID, Name: toolName, Arguments: args}, "", content, true, 0)
			return toolResult{Content: content, IsError: true, Terminal: true}, false
		}
	}
	return toolResult{}, true
}

func matchesAllowedToolInputValue(actual, allowedValues string) bool {
	return matchesAllowedToolInputValues([]string{actual}, allowedValues)
}

func matchesAllowedToolInputValues(actuals []string, allowedValues string) bool {
	if len(actuals) == 0 {
		return false
	}
	for _, actual := range actuals {
		if !matchesSingleAllowedToolInputValue(actual, allowedValues) {
			return false
		}
	}
	return true
}

func matchesSingleAllowedToolInputValue(actual, allowedValues string) bool {
	actual = strings.TrimSpace(actual)
	for _, allowed := range strings.Split(allowedValues, "|") {
		allowed = strings.TrimSpace(allowed)
		if allowed == routeEmptyInputValue && actual == "" {
			return true
		}
		if actual == allowed {
			return true
		}
	}
	return false
}

// executeTool 执行单个工具调用，返回结果
func (m *Master) executeTool(ctx context.Context, session *SessionState, userID string, toolCall llm.ToolCall, sessionTraceID, sessionSpanID string) toolResult {
	sessionID := ""
	if session != nil {
		sessionID = session.ID
	}
	toolSpanID := observability.NewSpanID()
	// 统一注入 sessionID，确保下游工具（如 spawn_agent）能从 ctx 中提取
	ctx = toolctx.WithSessionID(ctx, sessionID)
	// Master-local bridge: 后续 toolctx worker 合并后，应改为 toolctx.ToolContext 字段。
	ctx = WithPlanToolTrace(ctx, PlanToolTraceContext{
		TraceID:      sessionTraceID,
		SpanID:       toolSpanID,
		ParentSpanID: sessionSpanID,
		TurnID:       sessionTraceID,
		ToolCallID:   toolCall.ID,
	})
	ctx = toolctx.WithTraceContext(ctx, sessionTraceID, toolSpanID, sessionSpanID, toolCall.ID)
	ctx = tools.WithKBRuntimeContext(ctx, kbRuntimeContextForTool(session, userID))

	if decision := m.evaluatePlanToolGate(ctx, session, toolCall.Name); !decision.Allowed {
		content := fmt.Sprintf("[plan mode gate denied: %s]", decision.Reason)
		m.emitToolCallEvent(sessionID, ToolCallEvent{
			ToolCallID: toolCall.ID,
			ToolName:   toolCall.Name,
			TurnID:     sessionTraceID,
			Status:     "error",
			Error:      decision.Reason,
			SessionID:  sessionID,
		})
		m.enqueueSpan(observability.Span{
			TraceID:      sessionTraceID,
			SpanID:       toolSpanID,
			ParentSpanID: sessionSpanID,
			Operation:    "tool.execute",
			Service:      "master",
			SessionID:    sessionID,
			UserID:       userID,
			Status:       "error",
			Attributes: map[string]any{
				"tool_name":   toolCall.Name,
				"caller_type": string(decision.CallerType),
				"error":       decision.Reason,
			},
			Ts: time.Now(),
		})
		return toolResult{Content: content, IsError: true, Terminal: true}
	}

	// 检查 HITL 批准时是否有参数覆盖（如用户在审批卡片中更改了 theme_id）
	args := toolCall.Arguments
	overrideKey := sessionID + ":" + toolCall.Name
	if override, ok := m.toolArgOverrides.LoadAndDelete(overrideKey); ok {
		args = override.(json.RawMessage)
	}

	// wenyan publish_article 防御：LLM 经常忘记在 content 中加 frontmatter title，
	// 导致 MCP 报 "未能找到文章标题"。自动检测并注入。
	if toolCall.Name == "wenyan__publish_article" {
		args = ensureWenyanTitle(args, m.logger)
	}

	if len(args) > 0 && !json.Valid(args) {
		content := recoverableToolCallErrorContentWithHint(recoverableToolCallErrorHint(session, toolCall.Name, "invalid_tool_arguments_json",
			"工具参数不是合法 JSON，当前调用未执行。请重新构造为合法 JSON 对象；不要把自然语言或半截 JSON 放入 arguments。",
			"rebuild_arguments_json"))
		return m.recoverableToolCallErrorResult(ctx, sessionID, toolCall.ID, toolCall.Name, args, sessionTraceID, content, "invalid_tool_arguments_json")
	}

	actionGuardApproved := make(map[string]bool)
	if tr, ok := m.guardToolExecution(ctx, session, sessionID, userID, toolCall.ID, toolCall.Name, args, sessionTraceID, sessionSpanID, actionGuardApproved); !ok {
		return tr
	}

	// 广播工具调用开始事件仅在 guard 放行后发出，避免被拦截工具显示为已开始执行。
	m.emitToolCallEvent(sessionID, ToolCallEvent{
		ToolCallID: toolCall.ID,
		ToolName:   toolCall.Name,
		TurnID:     sessionTraceID,
		Status:     "start",
		SessionID:  sessionID,
	})

	m.emitQualityEvent(sessionTraceID, sessionSpanID, sessionID, agentquality.Event{
		Name:        agentquality.EventToolDecision,
		Route:       routeFromSession(session),
		FailureType: agentquality.FailureNone,
		FinalStatus: agentquality.StatusPass,
		ToolDecision: agentquality.ToolDecision{
			Actual:   toolCall.Name,
			Decision: agentquality.DecisionAllowed,
			ArgsHash: hashToolArgs(args),
		},
	})

	start := time.Now()
	// 长时间运行的工具有自己的超时逻辑，不加额外 2 分钟超时：
	//   question: 默认 5 分钟，最大 60 分钟（等待用户回答）
	//   parallel_dispatch: 内部 per-task 超时，最大 30 分钟
	//   task: 委派子 agent 执行，时间不可预测
	//   spawn_agent: 委派子 agent，时间不可预测
	//   skill: fork 模式下 600 秒 shell executor
	//   create_tool: 5 分钟审批等待
	// 其他工具统一 2 分钟超时，防止单工具阻塞整个 ReAct 循环
	toolCtx := ctx
	var toolCancel context.CancelFunc
	switch toolCall.Name {
	case "question", "parallel_dispatch", "task", "spawn_agent", "skill", "create_tool":
		// 这些工具有自己的超时机制，不加额外超时
	default:
		toolTimeout := m.config.RuntimePolicy.ToolTimeout
		if toolTimeout <= 0 {
			toolTimeout = 2 * time.Minute
		}
		toolCtx, toolCancel = context.WithTimeout(ctx, toolTimeout)
		defer toolCancel()
	}
	// 优先走 ToolBridge（补齐插件 hooks、read_file 缓存、指标、tool-not-found 友好提示），
	// fallback 到 mcpHost 直接执行（向后兼容测试场景）。实际执行闭包再交给
	// middleware 包裹，使工具级质量治理可以观察、改写或阻断调用。
	call := &ToolCall{Name: toolCall.Name, Arguments: args}
	wrappedResult, err := m.middlewarePipeline.WrapToolCall(toolCtx, call, func(runCtx context.Context, runCall *ToolCall) (*ToolResult, error) {
		if runCall == nil {
			return nil, fmt.Errorf("middleware passed nil tool call")
		}
		if tr, ok := m.guardToolExecution(runCtx, session, sessionID, userID, toolCall.ID, runCall.Name, runCall.Arguments, sessionTraceID, sessionSpanID, actionGuardApproved); !ok {
			return &ToolResult{Result: textToolResult(tr.Content, true)}, nil
		}
		if m.toolBridge != nil {
			result, execErr := m.toolBridge.ExecuteDirect(runCtx, runCall.Name, runCall.Arguments, skills.WithDirectExecutionGate(func(gateCtx context.Context, gateToolName string, gateInput json.RawMessage) error {
				if tr, ok := m.guardToolExecution(gateCtx, session, sessionID, userID, toolCall.ID, gateToolName, gateInput, sessionTraceID, sessionSpanID, actionGuardApproved); !ok {
					return errs.New(errs.CodePermissionDenied, tr.Content)
				}
				return nil
			}))
			return &ToolResult{Result: result}, execErr
		}
		if m.mcpHost != nil {
			result, execErr := toolruntime.InvokeHostTool(runCtx, m.mcpHost, runCall.Name, runCall.Arguments)
			return &ToolResult{Result: result}, execErr
		}
		return nil, fmt.Errorf("no tool execution backend available")
	})
	var result *mcphost.ToolResult
	if wrappedResult != nil {
		result = wrappedResult.Result
	}
	executedToolCall := toolCall
	executedToolCall.Name = call.Name
	executedArgs := call.Arguments
	duration := time.Since(start)

	// 统一工具执行结束日志：记录原始返回状态，方便排查工具调用链路问题
	{
		var contentPreview string
		if result != nil {
			contentPreview = mcphost.DecodeToolContent(result.Content)
			if len(contentPreview) > 200 {
				contentPreview = contentPreview[:200] + "..."
			}
		}
		m.logger.Info("工具执行结束",
			zap.String("tool", executedToolCall.Name),
			zap.String("call_id", toolCall.ID),
			zap.Int64("duration_ms", duration.Milliseconds()),
			zap.Bool("has_error", err != nil),
			zap.Bool("is_error", result != nil && result.IsError),
			zap.String("content_preview", contentPreview),
		)
	}

	if err != nil {
		m.logger.Warn("工具执行失败",
			zap.String("tool", executedToolCall.Name),
			zap.Error(err))
		failureType, requiresApproval, suggestedAction := commandFailureMetadata(result)
		errMsg := fmt.Sprintf("[工具执行失败: %v]", err)
		recoverable := isRecoverableToolCallError(errMsg)
		errKind := recoverableToolCallErrorKind(errMsg)
		terminal := isTerminalError(errMsg)
		// 广播工具调用失败事件
		m.emitToolCallEvent(sessionID, ToolCallEvent{
			ToolCallID:           toolCall.ID,
			ToolName:             executedToolCall.Name,
			TurnID:               sessionTraceID,
			Status:               "error",
			Duration:             duration.Milliseconds(),
			Error:                err.Error(),
			Recoverable:          recoverable,
			Terminal:             terminal,
			ErrorKind:            errKind,
			FailureType:          failureType,
			RequiresUserApproval: requiresApproval,
			SuggestedAction:      suggestedAction,
			SessionID:            sessionID,
		})
		m.enqueueSpan(observability.Span{
			TraceID:      sessionTraceID,
			SpanID:       toolSpanID,
			ParentSpanID: sessionSpanID,
			Operation:    "tool.execute",
			Service:      "master",
			SessionID:    sessionID,
			UserID:       userID,
			DurationMs:   int(duration.Milliseconds()),
			Status:       "error",
			Attributes:   map[string]any{"tool_name": executedToolCall.Name, "error": err.Error()},
			Ts:           start,
		})
		m.enqueueMetric(observability.Metric{
			Name:   "hive.tool.errors",
			Value:  1,
			Labels: map[string]any{"tool_name": executedToolCall.Name, "session_id": sessionID},
		})
		m.recordToolCallCounter(executedToolCall.Name, executedArgs, "error")
		m.recordToolErrorCounter(executedToolCall.Name, executedArgs, "executor_error")
		m.recordFilesystemActionObservation(ctx, sessionID, executedToolCall.Name, executedArgs, "error", "executor_error", duration)
		m.logToolCall(ctx, sessionID, executedToolCall, string(executedArgs), err.Error(), true, duration)
		return toolResult{Content: errMsg, IsError: true, Terminal: terminal, Recoverable: recoverable, ErrorKind: errKind}
	}

	// 检查工具返回的 IsError 标记（工具执行成功但业务逻辑报错）
	if result != nil && result.IsError {
		decoded := mcphost.DecodeToolContent(result.Content)
		failureType, requiresApproval, suggestedAction := commandFailureMetadata(result)
		recoverable := isRecoverableToolCallError(decoded)
		errKind := recoverableToolCallErrorKind(decoded)
		terminal := isTerminalError(decoded)
		m.emitToolCallEvent(sessionID, ToolCallEvent{
			ToolCallID:           toolCall.ID,
			ToolName:             executedToolCall.Name,
			TurnID:               sessionTraceID,
			Status:               "error",
			Duration:             duration.Milliseconds(),
			Error:                decoded,
			Recoverable:          recoverable,
			Terminal:             terminal,
			ErrorKind:            errKind,
			FailureType:          failureType,
			RequiresUserApproval: requiresApproval,
			SuggestedAction:      suggestedAction,
			SessionID:            sessionID,
		})
		m.enqueueSpan(observability.Span{
			TraceID:      sessionTraceID,
			SpanID:       toolSpanID,
			ParentSpanID: sessionSpanID,
			Operation:    "tool.execute",
			Service:      "master",
			SessionID:    sessionID,
			UserID:       userID,
			DurationMs:   int(duration.Milliseconds()),
			Status:       "error",
			Attributes:   map[string]any{"tool_name": executedToolCall.Name, "is_error": true},
			Ts:           start,
		})
		errorReason := normalizedToolErrorReason(executedToolCall.Name, executedArgs, decoded, "tool_error")
		m.recordToolCallCounter(executedToolCall.Name, executedArgs, "error")
		m.recordToolErrorCounter(executedToolCall.Name, executedArgs, errorReason)
		m.recordFilesystemActionObservation(ctx, sessionID, executedToolCall.Name, executedArgs, "error", errorReason, duration)
		m.logToolCall(ctx, sessionID, executedToolCall, string(executedArgs), decoded, true, duration)
		return toolResult{Content: decoded, IsError: true, Terminal: terminal, Recoverable: recoverable, ErrorKind: errKind}
	}

	// nil result 防御：先检查再记录，避免 broadcast success + span ok 与 IsError:true 矛盾
	if result == nil {
		m.logger.Warn("工具执行返回 nil result（无 error），标记为异常",
			zap.String("tool", executedToolCall.Name),
			zap.String("call_id", toolCall.ID),
		)
		m.emitToolCallEvent(sessionID, ToolCallEvent{
			ToolCallID: toolCall.ID,
			ToolName:   executedToolCall.Name,
			TurnID:     sessionTraceID,
			Status:     "error",
			Duration:   duration.Milliseconds(),
			SessionID:  sessionID,
		})
		m.enqueueSpan(observability.Span{
			TraceID:      sessionTraceID,
			SpanID:       toolSpanID,
			ParentSpanID: sessionSpanID,
			Operation:    "tool.execute",
			Service:      "master",
			SessionID:    sessionID,
			UserID:       userID,
			DurationMs:   int(duration.Milliseconds()),
			Status:       "error",
			Attributes:   map[string]any{"tool_name": executedToolCall.Name, "error": "nil result"},
			Ts:           start,
		})
		m.recordToolCallCounter(executedToolCall.Name, executedArgs, "error")
		m.recordToolErrorCounter(executedToolCall.Name, executedArgs, "nil_result")
		m.recordFilesystemActionObservation(ctx, sessionID, executedToolCall.Name, executedArgs, "error", "nil_result", duration)
		m.logToolCall(ctx, sessionID, executedToolCall, string(executedArgs), "[工具返回空结果]", true, duration)
		return toolResult{Content: "[工具返回空结果]", IsError: true}
	}

	// 广播工具调用成功事件
	m.emitToolCallEvent(sessionID, ToolCallEvent{
		ToolCallID: toolCall.ID,
		ToolName:   executedToolCall.Name,
		TurnID:     sessionTraceID,
		Status:     "success",
		Duration:   duration.Milliseconds(),
		SessionID:  sessionID,
	})

	content := mcphost.DecodeToolContent(result.Content)
	m.logger.Info("工具执行成功",
		zap.String("tool", executedToolCall.Name),
		zap.String("call_id", toolCall.ID),
		zap.Int64("duration_ms", duration.Milliseconds()),
		zap.Int("result_bytes", len(result.Content)),
	)
	// 工具执行成功 trace + metrics
	m.enqueueSpan(observability.Span{
		TraceID:      sessionTraceID,
		SpanID:       toolSpanID,
		ParentSpanID: sessionSpanID,
		Operation:    "tool.execute",
		Service:      "master",
		SessionID:    sessionID,
		UserID:       userID,
		DurationMs:   int(duration.Milliseconds()),
		Status:       "ok",
		Attributes:   map[string]any{"tool_name": executedToolCall.Name},
		Ts:           start,
	})
	m.enqueueMetric(observability.Metric{
		Name:   "hive.tool.duration_ms",
		Value:  float64(duration.Milliseconds()),
		Labels: map[string]any{"tool_name": executedToolCall.Name, "session_id": sessionID},
	})
	m.recordToolCallCounter(executedToolCall.Name, executedArgs, "success")
	m.recordFilesystemActionObservation(ctx, sessionID, executedToolCall.Name, executedArgs, "success", "", duration)
	m.logToolCall(ctx, sessionID, executedToolCall, string(executedArgs), content, false, duration)
	m.logFileChangeIfNeeded(ctx, sessionID, executedToolCall.Name, executedArgs, content)
	m.runTestDrivenShadowForToolChange(ctx, sessionID, sessionTraceID, toolSpanID, executedToolCall.Name, executedArgs)
	recordToolDiscoveryFromResult(session, executedToolCall, content, false)
	m.applyPlanToolStateAfterSuccess(session, executedToolCall.Name, toolCall.ID, sessionTraceID)
	m.recordToolOutputSchemaDiagnostic(sessionTraceID, toolSpanID, session, executedToolCall.Name, result)

	return toolResult{Content: content}
}

func (m *Master) recordToolOutputSchemaDiagnostic(traceID, spanID string, session *SessionState, toolName string, result *mcphost.ToolResult) {
	def, ok := m.lookupToolDefinition(toolName)
	if !ok || len(def.OutputSchema) == 0 {
		return
	}
	diagnostic, err := mcphost.ValidateToolResult(def, result)
	if err != nil {
		m.emitQualityEvent(traceID, spanID, session.ID, agentquality.Event{
			Name:        agentquality.EventReflection,
			Route:       routeFromSession(session),
			FailureType: agentquality.FailureRuntime,
			FinalStatus: agentquality.StatusNeedsUser,
			Reflection: agentquality.Reflection{
				Trigger:  "tool_output_schema",
				Severity: "warn",
				ToolName: toolName,
				Summary:  "工具 outputSchema 配置无效",
			},
			Attributes: map[string]any{"error": err.Error()},
		})
		return
	}
	if diagnostic == nil {
		return
	}
	m.emitQualityEvent(traceID, spanID, session.ID, agentquality.Event{
		Name:        agentquality.EventReflection,
		Route:       routeFromSession(session),
		FailureType: agentquality.FailureTool,
		FinalStatus: agentquality.StatusNeedsUser,
		Reflection: agentquality.Reflection{
			Trigger:  "tool_output_schema",
			Severity: "warn",
			ToolName: toolName,
			Summary:  diagnostic.Message,
		},
		Attributes: map[string]any{
			"schema_keyword": diagnostic.Keyword,
		},
	})
}

func (m *Master) runTestDrivenShadowForToolChange(ctx context.Context, sessionID, traceID, spanID, toolName string, args json.RawMessage) {
	if m == nil || !m.config.Reflection.TestDrivenShadow.Enabled || m.validationExec == nil {
		return
	}
	changed := changedFilesFromToolCall(toolName, args)
	if len(changed) == 0 {
		return
	}
	commands := agentquality.BuildValidationCommands(agentquality.ChangedFileSet{Files: changed})
	if len(commands) == 0 {
		return
	}
	runner := validationFeedbackRunner{
		enabled:  true,
		executor: m.validationExec,
		record: func(ev agentquality.Event) {
			m.emitQualityEvent(traceID, spanID, sessionID, ev)
			if ev.Reflection.Severity == "warn" {
				m.recordReflectionEvaluationShadow(ctx, sessionID, traceID, spanID, agentquality.EvaluationInput{
					Trigger:          "test_failed",
					ValidationOutput: fmt.Sprint(ev.Attributes["stderr"]),
				})
			}
		},
	}
	runner.Run(ctx, sessionID, traceID, commands)
}

func changedFilesFromToolCall(toolName string, args json.RawMessage) []string {
	paths := map[string]struct{}{}
	addPath := func(path string) {
		path = strings.TrimSpace(path)
		if path != "" && path != "/dev/null" {
			paths[path] = struct{}{}
		}
	}
	switch toolName {
	case "write_file", "edit":
		var p struct {
			Path string `json:"path"`
		}
		if err := json.Unmarshal(args, &p); err == nil {
			addPath(p.Path)
		}
	case "filesystem":
		var p struct {
			Action string `json:"action"`
			Path   string `json:"path"`
			Edits  []struct {
				Path string `json:"path"`
			} `json:"edits"`
		}
		if err := json.Unmarshal(args, &p); err == nil {
			switch strings.ToLower(strings.TrimSpace(p.Action)) {
			case "write", "edit":
				addPath(p.Path)
			case "multiedit":
				for _, edit := range p.Edits {
					addPath(edit.Path)
				}
			}
		}
	case "multiedit", "multi_edit":
		var p struct {
			Path  string `json:"path"`
			Edits []struct {
				Path string `json:"path"`
			} `json:"edits"`
		}
		if err := json.Unmarshal(args, &p); err == nil {
			addPath(p.Path)
			for _, edit := range p.Edits {
				addPath(edit.Path)
			}
		}
	case "apply_patch":
		var p struct {
			Patch string `json:"patch"`
		}
		if err := json.Unmarshal(args, &p); err == nil && p.Patch != "" {
			if parsed, parseErr := tools.ParsePatch(p.Patch); parseErr == nil {
				for _, fp := range parsed.Files {
					addPath(fp.NewPath)
					if fp.NewPath == "" {
						addPath(fp.OldPath)
					}
				}
			}
		}
	default:
		return nil
	}
	out := make([]string, 0, len(paths))
	for path := range paths {
		out = append(out, path)
	}
	sort.Strings(out)
	return out
}

func (m *Master) recordLongArtifactEvaluation(ctx context.Context, sessionID, traceID, spanID, content string) {
	if len(content) <= 1200 || !strings.Contains(content, "```") {
		return
	}
	m.recordReflectionEvaluationShadow(ctx, sessionID, traceID, spanID, agentquality.EvaluationInput{
		Trigger:         "long_artifact",
		AssistantOutput: content,
	})
}

func (m *Master) lookupToolDefinition(toolName string) (mcphost.ToolDefinition, bool) {
	if m == nil {
		return mcphost.ToolDefinition{}, false
	}
	if m.toolBridge != nil {
		for _, def := range m.toolBridge.AvailableTools(nil) {
			if def.Name == toolName {
				return def, true
			}
		}
	}
	if m.mcpHost != nil {
		def, err := m.mcpHost.GetTool(toolName)
		if err == nil && def != nil {
			return *def, true
		}
	}
	return mcphost.ToolDefinition{}, false
}

// logToolCall 将工具调用异步记录到 Journal（nil 安全，非阻塞）
// entry 在本函数内构建，避免 channel 传递大对象引用问题
// 队列满时旧条目被丢弃（背压保护）
func (m *Master) logToolCall(_ context.Context, sessionID string, toolCall llm.ToolCall, args, result string, isError bool, duration time.Duration) {
	if m.journal == nil {
		return
	}
	entry := journal.ToolCallEntry{
		SessionID:  sessionID,
		ToolName:   toolCall.Name,
		ToolCallID: toolCall.ID,
		Arguments:  args,
		Result:     result,
		IsError:    isError,
		Duration:   duration,
		Timestamp:  time.Now(),
	}
	// 尝试非阻塞写入队列；队列满时跳过（不阻塞请求路径）
	select {
	case m.journalCh <- journalEntry{toolCall: &entry}:
	default:
		// 队列已满，跳过此条目
	}
}

// logFileChangeIfNeeded 对文件写入类工具调用异步记录 FileChange journal（nil 安全，非阻塞）
func (m *Master) logFileChangeIfNeeded(_ context.Context, sessionID, toolName string, args json.RawMessage, _ string) {
	if m.journal == nil && m.pluginMgr == nil {
		return
	}
	// 只记录有实际文件副作用的工具
	var filePaths []string
	switch toolName {
	case "write_file", "edit", "multiedit":
		filePaths = changedFilesFromToolCall(toolName, args)
	case "filesystem":
		filePaths = changedFilesFromToolCall(toolName, args)
	case "apply_patch":
		// apply_patch 的文件路径藏在 patch payload 里，需要解析提取
		filePaths = changedFilesFromToolCall(toolName, args)
	default:
		return
	}
	if len(filePaths) == 0 {
		return
	}
	for _, filePath := range filePaths {
		fp := filePath // capture for goroutine
		if m.journal != nil {
			entry := journal.FileChangeEntry{
				SessionID: sessionID,
				FilePath:  fp,
				Action:    toolName,
				Timestamp: time.Now(),
			}
			select {
			case m.journalCh <- journalEntry{fileChange: &entry}:
			default:
			}
		}
		// 触发 FileChanged hook（非阻塞）
		if m.pluginMgr != nil {
			go func() {
				hookCtx, cancel := context.WithTimeout(context.Background(), plugin.HookCallTimeout)
				defer cancel()
				_ = m.pluginMgr.TriggerFileChanged(hookCtx, &plugin.FileChangedInput{
					Path:      fp,
					Operation: toolName,
					SessionID: sessionID,
				})
			}()
		}
	}
}

// logDecisionIfFinal 在 LLM 完成任务时异步记录决策日志（nil 安全，非阻塞）
// 从 session 最后一条 user 消息提取任务描述，从最后一条 assistant 消息提取决策结果
func (m *Master) logDecisionIfFinal(_ context.Context, session *SessionState) {
	if m.journal == nil {
		return
	}
	session.mu.RLock()
	var lastUser, lastAssistant string
	for i := len(session.Messages) - 1; i >= 0; i-- {
		msg := session.Messages[i]
		if lastAssistant == "" && msg.Role == "assistant" {
			lastAssistant = msg.Content.Text()
		}
		if lastUser == "" && msg.Role == "user" {
			lastUser = msg.Content.Text()
		}
		if lastUser != "" && lastAssistant != "" {
			break
		}
	}
	sessionID := session.ID
	session.mu.RUnlock()

	if lastUser == "" {
		return
	}
	entry := journal.DecisionEntry{
		SessionID: sessionID,
		Decision:  lastUser,
		Reason:    lastAssistant,
		AgentID:   "master",
		Timestamp: time.Now(),
	}
	select {
	case m.journalCh <- journalEntry{decision: &entry}:
	default:
	}
}

// loopDetector 检测 ReAct 循环中的重复工具调用模式。
// 两层检测：
//  1. 批次级：对每轮所有 tool_call 的 tool+args 排序后 hash，连续 >=3 warn，连续 >=5 hard_stop。
//  2. 调用级：per-call 的 tool+args 连续失败计数，同一 fingerprint 连续失败 >=2 次触发 early_stop。
//
// 批次级必须看参数且必须按连续计数，否则正常的连续 read_file/grep 探索会被误判成循环。
type loopDetector struct {
	hashes          []string
	window          int            // 滑动窗口大小，仅保留诊断历史
	lastHash        string         // 最近一轮批次指纹
	consecutiveSame int            // 相同批次指纹连续出现次数
	callFailures    map[string]int // key = tool+args fingerprint, value = 连续失败次数
}

func newLoopDetector(window int) *loopDetector {
	return &loopDetector{window: window, callFailures: make(map[string]int)}
}

// check 返回 "ok"、"warn" 或 "hard_stop"（批次级检测）。
// count 包含当前这次：先更新连续计数，确保 warn@3, hard_stop@5 精确匹配。
func (d *loopDetector) check(toolCalls []llm.ToolCall) string {
	hash := computeToolCallHash(toolCalls)
	d.hashes = append(d.hashes, hash)
	if len(d.hashes) > d.window {
		d.hashes = d.hashes[1:]
	}
	if hash == d.lastHash {
		d.consecutiveSame++
	} else {
		d.lastHash = hash
		d.consecutiveSame = 1
	}
	if d.consecutiveSame >= 5 {
		return "hard_stop"
	}
	if d.consecutiveSame >= 3 {
		return "warn"
	}
	return "ok"
}

// recordCallResult 记录单个工具调用的执行结果（调用级检测）。
// 失败时递增连续失败计数，成功时重置。
// 返回 true 表示该 fingerprint 连续失败次数已达阈值，应触发 early stop。
func (d *loopDetector) recordCallResult(fingerprint string, isError bool) bool {
	if !isError {
		delete(d.callFailures, fingerprint)
		return false
	}
	d.callFailures[fingerprint]++
	return d.callFailures[fingerprint] >= 2
}

// computeToolCallHash 对工具名 + 规范化参数排序后计算 SHA-256 hash。
// 只有同一组工具以同一参数重复出现时才算批次级循环。
func computeToolCallHash(toolCalls []llm.ToolCall) string {
	fingerprints := make([]string, len(toolCalls))
	for i, tc := range toolCalls {
		fingerprints[i] = canonicalFingerprint(tc.Name, tc.Arguments)
	}
	sort.Strings(fingerprints)
	joined := strings.Join(fingerprints, ",")
	h := sha256.Sum256([]byte(joined))
	return hex.EncodeToString(h[:8]) // 前 8 字节足够区分
}

func shouldExitTask(finishReason string) bool {
	return finishReason == "stop" || finishReason == "end_turn"
}

// repairOrphanedToolCalls 修复缺少 tool result 的 assistant 消息。
// 当权限审批超时或用户取消时，assistant 消息中的 tool_calls 可能没有对应的 tool result，
// 导致下次 LLM 调用时因消息不一致而返回 400 错误。
// 返回值：修复后的消息列表 + 本次新插入的补丁消息（供调用方持久化到 DB）。
func repairOrphanedToolCalls(messages []llm.MessageWithTools, logger *zap.Logger) ([]llm.MessageWithTools, []llm.MessageWithTools) {
	// 收集所有已有 tool result 的 call ID
	answeredIDs := map[string]bool{}
	for _, m := range messages {
		if m.Role == "tool" && m.ToolCallID != "" {
			answeredIDs[m.ToolCallID] = true
		}
	}

	// 遍历消息，在 assistant 消息后就地插入缺失的 tool result
	var result []llm.MessageWithTools
	var patched []llm.MessageWithTools
	for _, m := range messages {
		result = append(result, m)
		if m.Role == "assistant" && len(m.ToolCalls) > 0 {
			for _, tc := range m.ToolCalls {
				if !answeredIDs[tc.ID] {
					fakeResult := llm.MessageWithTools{
						Role:       "tool",
						ToolCallID: tc.ID,
						ToolName:   tc.Name,
						Content:    llm.NewTextContent("[工具调用被中断：权限超时或请求取消]"),
						CreatedAt:  time.Now().Format(time.RFC3339),
						Metadata:   map[string]string{"agent_id": "master", "repair": "orphaned_tool_call"},
					}
					result = append(result, fakeResult)
					patched = append(patched, fakeResult)
					answeredIDs[tc.ID] = true
					logger.Warn("修复孤立的 tool_call",
						zap.String("call_id", tc.ID),
						zap.String("tool", tc.Name))
				}
			}
		}
	}
	if len(patched) > 0 {
		return result, patched
	}
	return messages, nil
}

func contaminationStatus(result memory.InjectionResult) string {
	if result.SkippedTotal() > 0 {
		return "filtered"
	}
	if len(result.Memories) > 0 {
		return "clean"
	}
	return "none"
}

func shouldRecordMemoryInjection(result memory.InjectionResult) bool {
	return result.HasSignal()
}

// removeOrphanedToolResults 移除孤立的 tool result 消息。
// 压缩/截断可能导致 assistant 消息被移除，但其对应的 tool result 消息保留下来，
// 这会导致 API 返回 "No tool call found for function call output" 错误。
func removeOrphanedToolResults(messages []llm.MessageWithTools, logger *zap.Logger) []llm.MessageWithTools {
	// 收集所有 assistant 消息中的 tool_call ID
	validCallIDs := map[string]bool{}
	for _, m := range messages {
		if m.Role == "assistant" {
			for _, tc := range m.ToolCalls {
				validCallIDs[tc.ID] = true
			}
		}
	}

	// 检查是否有孤立的 tool result
	var result []llm.MessageWithTools
	removed := 0
	for _, m := range messages {
		if m.Role == "tool" && m.ToolCallID != "" && !validCallIDs[m.ToolCallID] {
			removed++
			if logger != nil {
				logger.Debug("移除孤立的 tool result（无匹配的 assistant tool_call）",
					zap.String("call_id", m.ToolCallID),
				)
			}
			continue
		}
		result = append(result, m)
	}

	if removed > 0 {
		return result
	}
	return messages
}

func removeStaleKBNoBindingResults(messages []llm.MessageWithTools, hasActiveKBBinding bool, logger *zap.Logger) []llm.MessageWithTools {
	if !hasActiveKBBinding || len(messages) == 0 {
		return messages
	}
	staleCalls := map[string]struct{}{}
	for _, msg := range messages {
		if msg.Role != "tool" || msg.ToolName != "kb.doc.meta" || msg.ToolCallID == "" {
			continue
		}
		if msg.Metadata["error_kind"] != "kb_unavailable_or_not_bound" && !strings.Contains(msg.Content.Text(), "kb_unavailable_or_not_bound") {
			continue
		}
		staleCalls[msg.ToolCallID] = struct{}{}
	}
	if len(staleCalls) == 0 {
		return messages
	}
	out := make([]llm.MessageWithTools, 0, len(messages))
	removed := 0
	for _, msg := range messages {
		if msg.Role == "tool" {
			if _, ok := staleCalls[msg.ToolCallID]; ok {
				removed++
				continue
			}
		}
		if msg.Role == "assistant" && len(msg.ToolCalls) > 0 {
			filtered := make([]llm.ToolCall, 0, len(msg.ToolCalls))
			for _, call := range msg.ToolCalls {
				if _, ok := staleCalls[call.ID]; ok && call.Name == "kb.doc.meta" {
					removed++
					continue
				}
				filtered = append(filtered, call)
			}
			msg.ToolCalls = filtered
			if msg.Content.Text() == "" && msg.ReasoningContent == "" && len(msg.ToolCalls) == 0 {
				continue
			}
		}
		out = append(out, msg)
	}
	if removed > 0 && logger != nil {
		logger.Info("已移除历史 KB 未绑定工具结果，当前会话已有 active KB binding",
			zap.Int("removed", removed))
	}
	return out
}

// hasImageAttachments 检查附件列表中是否包含图片
func hasImageAttachments(attachments []FileAttachment) bool {
	for _, a := range attachments {
		if len(a.MimeType) > 6 && a.MimeType[:6] == "image/" {
			return true
		}
	}
	return false
}

// --- 提取的可测试 helper（P0-3 Phase 6 Eng Review GAP 修复） ---

// spawnCallFilter 记录一轮 spawn_agent 过滤结果
type spawnCallFilter struct {
	ToExecute []llm.ToolCall // 允许执行的调用
	Rejected  []llm.ToolCall // 因超限被拒绝的调用
}

// filterSpawnAgentCalls 对工具调用列表应用 spawn_agent 并发限制。
// 超过 maxSpawn 的 spawn_agent 标记为 rejected。
// 此方法仅做过滤决策，不执行工具也不写消息。
func (m *Master) filterSpawnAgentCalls(toolCalls []llm.ToolCall, maxSpawn int) spawnCallFilter {
	spawnCount := 0
	filter := spawnCallFilter{
		ToExecute: make([]llm.ToolCall, 0, len(toolCalls)),
		Rejected:  make([]llm.ToolCall, 0),
	}
	for _, tc := range toolCalls {
		if tc.Name == "spawn_agent" {
			spawnCount++
			if spawnCount > maxSpawn {
				filter.Rejected = append(filter.Rejected, tc)
				continue
			}
		}
		filter.ToExecute = append(filter.ToExecute, tc)
	}
	return filter
}

// checkCostBudget 检查 session 成本是否超过预算。
// 返回 non-nil error 表示已超预算，调用方应终止循环。
func (m *Master) checkCostBudget(ctx context.Context, sessionID string) error {
	if m.asyncRecorder == nil || m.config.MaxSessionCost <= 0 {
		return nil
	}
	summary, err := m.asyncRecorder.GetSessionCost(ctx, sessionID)
	if err != nil {
		return nil // 查询失败不阻塞主流程
	}
	if summary.TotalCostUSD > m.config.MaxSessionCost {
		return errs.New(errs.CodePlanExecFailed, fmt.Sprintf(
			"session cost budget exceeded: %.2f USD > %.2f USD",
			summary.TotalCostUSD, m.config.MaxSessionCost))
	}
	return nil
}

type costBudgetDecision struct {
	Graceful bool
	Err      error
}

// handleCostBudgetExceeded 在预算触顶时优先给未完成 plan 生成交接摘要并暂停。
// 交接失败才硬停，避免仍有 open todos 时被外层兜底误广播 completed。
func (m *Master) handleCostBudgetExceeded(ctx context.Context, session *SessionState, responseID uint64, budgetErr error, traceID, spanID string) costBudgetDecision {
	sessionID := ""
	if session != nil {
		sessionID = session.ID
	}
	if m != nil && m.logger != nil {
		m.logger.Warn("会话成本已达上限",
			zap.String("session_id", sessionID),
			zap.Error(budgetErr),
		)
	}

	if paused, handoff, err := m.tryGracefulBudgetYield(ctx, session, budgetErr, traceID, spanID); err == nil && paused {
		if m != nil && m.sessionMgr != nil {
			m.sessionMgr.SendResponse(responseID, NewTaskResponse(handoff, TaskStatusPaused))
		}
		return costBudgetDecision{Graceful: true}
	} else if err != nil {
		hardErr := errs.Wrap(errs.CodePlanExecFailed, "budget graceful yield failed", err)
		m.markSessionBudgetPaused(session)
		m.recordBudgetExit(ctx, session, "hard_stop", hardErr, traceID, spanID)
		m.appendBudgetHardStopMessage(session, fmt.Sprintf("%v; original budget error: %v", hardErr, budgetErr))
		return costBudgetDecision{Err: hardErr}
	}

	m.markSessionBudgetPaused(session)
	m.recordBudgetExit(ctx, session, "hard_stop", budgetErr, traceID, spanID)
	m.appendBudgetHardStopMessage(session, budgetErr.Error())
	return costBudgetDecision{Err: budgetErr}
}

func (m *Master) markSessionBudgetPaused(session *SessionState) {
	if session == nil {
		return
	}
	session.mu.Lock()
	session.PlanStatus = sessiontodo.PlanStatusPaused
	session.PlanMode = true
	session.mu.Unlock()
}

func (m *Master) tryGracefulBudgetYield(ctx context.Context, session *SessionState, budgetErr error, traceID, spanID string) (bool, string, error) {
	if m == nil || session == nil {
		return false, "", nil
	}
	if m.sessionTodoStore == nil {
		return false, "", nil
	}
	snapshot, err := m.sessionTodoStore.Snapshot(ctx, session.ID)
	if err != nil {
		return false, "", err
	}
	if len(pendingTodosForResume(snapshot.Todos)) == 0 {
		return false, "", nil
	}

	handoff := buildBudgetHandoffSummary(session, snapshot, budgetErr)
	if _, err := m.pausePlanForBudgetYield(ctx, session, snapshot, traceID, spanID); err != nil {
		return false, "", err
	}
	m.appendSessionMessage(session, llm.MessageWithTools{
		Role:      "system",
		Content:   llm.NewTextContent(handoff),
		CreatedAt: time.Now().Format(time.RFC3339),
		Metadata: map[string]string{
			"agent_id":         "master",
			"handoff":          "budget_graceful_yield",
			"budget_exit_mode": "graceful_yield",
		},
	})
	m.recordBudgetExit(ctx, session, "graceful_yield", budgetErr, traceID, spanID)
	return true, handoff, nil
}

func (m *Master) appendBudgetHardStopMessage(session *SessionState, reason string) {
	if m == nil || session == nil {
		return
	}
	m.appendSessionMessage(session, llm.MessageWithTools{
		Role:      "system",
		Content:   llm.NewTextContent(reason),
		CreatedAt: time.Now().Format(time.RFC3339),
		Metadata: map[string]string{
			"agent_id":         "master",
			"budget_exit_mode": "hard_stop",
		},
	})
}

func buildBudgetHandoffSummary(session *SessionState, snapshot sessiontodo.Snapshot, budgetErr error) string {
	goal := latestUserGoal(session)
	completed := todosByOpenState(snapshot.Todos, false)
	open := todosByOpenState(snapshot.Todos, true)
	recentFailure := latestFailureSummary(session)
	if recentFailure == "" && budgetErr != nil {
		recentFailure = budgetErr.Error()
	}

	var b strings.Builder
	b.WriteString("# Budget Graceful Yield Handoff\n\n")
	writeHandoffField(&b, "目标", goal, "未记录明确目标")
	writeHandoffList(&b, "已完成", completed, "暂无已完成事项")
	writeHandoffList(&b, "未完成", open, "暂无未完成事项")
	writeHandoffField(&b, "最近失败", recentFailure, "未记录最近失败")
	writeHandoffList(&b, "下一步建议", []string{
		"补充预算或切换更低成本模型后继续执行未完成 todos",
		"从 in_progress todo 开始恢复，必要时先验证最近一次失败的前置条件",
	}, "")
	return strings.TrimRight(b.String(), "\n")
}

func writeHandoffField(b *strings.Builder, title, value, fallback string) {
	value = strings.TrimSpace(value)
	if value == "" {
		value = fallback
	}
	fmt.Fprintf(b, "## %s\n%s\n\n", title, value)
}

func writeHandoffList(b *strings.Builder, title string, items []string, fallback string) {
	fmt.Fprintf(b, "## %s\n", title)
	if len(items) == 0 {
		if fallback != "" {
			fmt.Fprintf(b, "- %s\n", fallback)
		}
		b.WriteString("\n")
		return
	}
	for _, item := range items {
		fmt.Fprintf(b, "- %s\n", item)
	}
	b.WriteString("\n")
}

func latestUserGoal(session *SessionState) string {
	if session == nil {
		return ""
	}
	session.mu.RLock()
	defer session.mu.RUnlock()
	for i := len(session.Messages) - 1; i >= 0; i-- {
		if session.Messages[i].Role == "user" {
			return truncateForLog(strings.TrimSpace(session.Messages[i].Content.Text()), 240)
		}
	}
	return ""
}

func todosByOpenState(todos []sessiontodo.Todo, wantOpen bool) []string {
	out := make([]string, 0)
	for _, todo := range todos {
		open := todo.Status == sessiontodo.TodoStatusPending || todo.Status == sessiontodo.TodoStatusInProgress
		if open != wantOpen {
			continue
		}
		status := string(todo.Status)
		content := strings.TrimSpace(todo.Content)
		if content == "" {
			content = todo.ID
		}
		if todo.ID != "" {
			out = append(out, fmt.Sprintf("[%s] %s: %s", status, todo.ID, content))
		} else {
			out = append(out, fmt.Sprintf("[%s] %s", status, content))
		}
	}
	return out
}

func latestFailureSummary(session *SessionState) string {
	if session == nil {
		return ""
	}
	session.mu.RLock()
	defer session.mu.RUnlock()
	for i := len(session.Messages) - 1; i >= 0; i-- {
		msg := session.Messages[i]
		if !msg.IsError && msg.Metadata["reflection_severity"] != "hard_stop" {
			continue
		}
		content := strings.TrimSpace(msg.Content.Text())
		if content == "" {
			continue
		}
		if msg.ToolName != "" {
			return truncateForLog(fmt.Sprintf("%s: %s", msg.ToolName, content), 240)
		}
		return truncateForLog(content, 240)
	}
	return ""
}

// validateLLMResponse 检查 LLM 响应是否为 nil（防御性处理）。
// 返回 non-nil error 表示 LLM 返回了 nil response。
func validateLLMResponse(resp *llm.ChatWithToolsResponse) error {
	if resp == nil {
		return errs.New(errs.CodePlanExecFailed, "LLM returned nil response")
	}
	return nil
}

// appendSessionMessage 将消息追加到 session.Messages 并立即增量持久化到 DB。
//
// 架构变更：从"任务完成后批量写入"改为"每条消息产生时立即写入"。
// 好处：(1) 进程崩溃时最多丢 1 条消息而非全部 (2) 时间戳天然准确，无需 metadata hack
// (3) 前端刷新后能看到进行中的消息。
//
// 同步写入：ReAct 循环本身是串行的，appendSessionMessage 调用天然有序。
// 单条 INSERT 在本地 PostgreSQL 上 1-3ms，不会成为瓶颈。
// appendSessionMessage 是唯一的消息写入者（SaveSession 只更新会话元数据），
// 消除了两个写入者竞争 lastSavedIndex 导致的重复写入和指针回退问题。
//
// 防空洞机制：一旦某条消息写入失败（2 次重试后），设置 persistFailed 标记，
// 后续消息不再尝试写 DB，避免 DB 中出现不连续的消息序列。
//
// buildSkillsIndex 把 skill 注册表快照成 lowercase name → true 的查找表，
// 用于 detectToolChoice 判断 "X 是什么 / 怎么用" 里的 X 是否为已知 skill。
// nil-safe：skillReg 未注入时返回 nil，detectToolChoice 会退化为保守立场。
// userID 空串表示 public scope；非空则合并 personal + public（personal 优先）。
func (m *Master) buildSkillsIndex(userID string) map[string]bool {
	if m.skillReg == nil {
		return nil
	}
	var list []skills.SkillMetadata
	if userID == "" {
		list = m.skillReg.ListForModel()
	} else {
		list = m.skillReg.ListForModel(userID)
	}
	if len(list) == 0 {
		return nil
	}
	idx := make(map[string]bool, len(list))
	for _, sm := range list {
		if sm.Name == "" {
			continue
		}
		idx[strings.ToLower(sm.Name)] = true
	}
	return idx
}

func (m *Master) skillMetasForModel(userID string) []skills.SkillMetadata {
	if m == nil || m.skillReg == nil {
		return nil
	}
	if userID == "" {
		return m.skillReg.ListForModel()
	}
	return m.skillReg.ListForModel(userID)
}

// persistAssistant 是 P0-A structural lock 的 assistant 消息持久化唯一入口。
// 接受 assistantcap.Capability 作为编译期 + 运行时双层授权证明。
// 全包内 Role:"assistant" 字面量仅在此函数体出现一处。
func (m *Master) persistAssistant(_ assistantcap.Capability, s *SessionState, content, reasoning string,
	toolCalls []llm.ToolCall, meta map[string]string, ts string) {
	m.appendSessionMessageRaw(s, llm.MessageWithTools{
		Role:             "assistant",
		Content:          llm.NewTextContent(content),
		ToolCalls:        toolCalls,
		ReasoningContent: reasoning,
		CreatedAt:        ts,
		Metadata:         meta,
	})
}

// broadcastAssistant 是 P0-A structural lock 的 assistant 广播唯一入口。
// 接受 assistantcap.Capability 作为编译期 + 运行时双层授权证明。
// 全包内 payload["role"]="assistant" 字面量仅在此函数体出现一处。
//
// 不能走 BroadcastSessionMessage → Broadcast，因为公开 Broadcast 入口的
// assertNoAssistantPayload 会把自己合法的 role:"assistant" payload 当 forbidden
// 处理而 panic。改走 EventBus.broadcastWithAssistantCap：cap 既是 wrapper 入口
// proof，也是 sink 跳过 assert 的 key，把 capability 一路下沉到分发口。
func (m *Master) broadcastAssistant(cap assistantcap.Capability, sessionID string, payload map[string]any) {
	if session := m.sessionMgr.GetSession(sessionID); session != nil && session.IsTerminated() {
		return
	}
	payload["role"] = "assistant"
	m.eventBus.broadcastWithAssistantCap(cap, BroadcastMessage{
		Type:      EventTypeMessage,
		SessionID: sessionID,
		Payload:   payload,
	})
}

// appendSessionMessage 是 non-assistant 消息（user/system/tool）的持久化入口。
// 写入 Role:"assistant" 时 panic — assistant 必须走 persistAssistant + assistantcap.Capability。
func (m *Master) appendSessionMessage(session *SessionState, msg llm.MessageWithTools) {
	if msg.Role == "assistant" {
		panic("[P0-A structural lock] assistant messages must use Master.persistAssistant + assistantcap.Capability — direct appendSessionMessage with Role:\"assistant\" is forbidden")
	}
	m.appendSessionMessageRaw(session, msg)
}

// appendSessionMessageRaw 是底层持久化实现，不做 role 校验。
// 仅 persistAssistant 和 appendSessionMessage 应该调用。AST 测试规则强制其他 caller 报红。
func (m *Master) appendSessionMessageRaw(session *SessionState, msg llm.MessageWithTools) {
	if session.IsTerminated() {
		return
	}
	session.mu.Lock()
	session.Messages = append(session.Messages, msg)
	msgIndex := len(session.Messages) - 1
	sessionID := session.ID
	failed := session.persistFailed
	session.mu.Unlock()

	// 增量持久化：每条消息立即写入 DB
	if m.store == nil || failed {
		return
	}

	meta := buildMessageMeta(msg)

	// 最多尝试 2 次（首次 + 1 次重试），避免瞬时 DB 抖动丢消息
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		lastErr = m.store.AddMessage(ctx, sessionID, msg.Role, msg.Content.Text(), meta)
		cancel()
		if lastErr == nil {
			break
		}
		m.logger.Warn("增量持久化消息失败，重试中",
			zap.String("session_id", sessionID),
			zap.String("role", msg.Role),
			zap.Int("attempt", attempt+1),
			zap.Error(lastErr))
	}

	if lastErr != nil {
		// 2 次都失败：标记 persistFailed，阻止后续消息写入 DB（防止空洞）
		session.mu.Lock()
		session.persistFailed = true
		session.mu.Unlock()
		m.logger.Error("增量持久化消息最终失败，本任务后续消息将不再写入 DB",
			zap.String("session_id", sessionID),
			zap.String("role", msg.Role),
			zap.Int("msg_index", msgIndex),
			zap.Error(lastErr))
		return
	}

	// 写入成功，推进 lastSavedIndex
	session.mu.Lock()
	session.lastSavedIndex = msgIndex + 1
	session.mu.Unlock()
}

// buildMessageMeta 从 MessageWithTools 构建持久化用的 metadata map。
// 提取自 SaveSession，供增量写入和批量兜底共用。
func buildMessageMeta(msg llm.MessageWithTools) map[string]any {
	var meta map[string]any
	if len(msg.ToolCalls) > 0 {
		tcJSON, _ := json.Marshal(msg.ToolCalls)
		meta = map[string]any{"tool_calls": string(tcJSON)}
	}
	if msg.ToolCallID != "" {
		if meta == nil {
			meta = map[string]any{}
		}
		meta["tool_call_id"] = msg.ToolCallID
	}
	if msg.ReasoningContent != "" {
		if meta == nil {
			meta = map[string]any{}
		}
		meta["reasoning_content"] = msg.ReasoningContent
	}
	if msg.IsError {
		if meta == nil {
			meta = map[string]any{}
		}
		meta["is_error"] = true
	}
	if msg.ToolName != "" {
		if meta == nil {
			meta = map[string]any{}
		}
		meta["tool_name"] = msg.ToolName
	}
	if msg.CreatedAt != "" {
		if meta == nil {
			meta = map[string]any{}
		}
		meta["created_at"] = msg.CreatedAt
	}
	if msg.Metadata != nil {
		if v, ok := msg.Metadata["attachments"]; ok && v != "" {
			if meta == nil {
				meta = map[string]any{}
			}
			meta["attachments"] = v
		}
		if v, ok := msg.Metadata["input_tokens"]; ok && v != "" {
			if meta == nil {
				meta = map[string]any{}
			}
			meta["input_tokens"] = v
		}
		if v, ok := msg.Metadata["output_tokens"]; ok && v != "" {
			if meta == nil {
				meta = map[string]any{}
			}
			meta["output_tokens"] = v
		}
		if v, ok := msg.Metadata["recoverable"]; ok && v != "" {
			if meta == nil {
				meta = map[string]any{}
			}
			meta["recoverable"] = v == "true"
		}
		if v, ok := msg.Metadata["terminal"]; ok && v != "" {
			if meta == nil {
				meta = map[string]any{}
			}
			meta["terminal"] = v == "true"
		}
		if v, ok := msg.Metadata["error_kind"]; ok && v != "" {
			if meta == nil {
				meta = map[string]any{}
			}
			meta["error_kind"] = v
		}
		if v, ok := msg.Metadata["citations"]; ok && v != "" {
			if meta == nil {
				meta = map[string]any{}
			}
			meta["citations"] = v
		}
		if v, ok := msg.Metadata["artifacts"]; ok && v != "" {
			if meta == nil {
				meta = map[string]any{}
			}
			meta["artifacts"] = v
		}
	}
	if msg.Content.IsMultimodal() && !hasAttachmentManifest(msg.Metadata) {
		if meta == nil {
			meta = map[string]any{}
		}
		if partsJSON, err := json.Marshal(msg.Content.Parts()); err == nil {
			meta["content_parts"] = string(partsJSON)
		}
	}
	return meta
}

// emitToolCallEvent 广播工具调用事件，并把 SessionID 填到 top-level。
// X-1 修复：直接用 BroadcastGenericMessage 会丢失 top-level SessionID，
// 导致同一飞书群聊内两个 session 的 renderer 互相收到对方的 tool_call。
func (m *Master) emitToolCallEvent(sessionID string, ev ToolCallEvent) {
	m.eventBus.BroadcastSessionMessage(sessionID, BroadcastMessage{
		Type:    EventTypeToolCall,
		Payload: ev,
	})
}

// broadcastToolMessage 广播 tool 消息给前端。
func (m *Master) broadcastToolMessage(sessionID, toolCallID, toolName, content, createdAt string, tr toolResult) {
	payload := map[string]interface{}{
		"role":         "tool",
		"content":      content,
		"session_id":   sessionID,
		"partial":      false,
		"tool_call_id": toolCallID,
		"tool_name":    toolName,
		"timestamp":    createdAt,
	}
	if tr.IsError {
		payload["is_error"] = true
	}
	if tr.Recoverable {
		payload["recoverable"] = true
	}
	if tr.Terminal {
		payload["terminal"] = true
	}
	if tr.ErrorKind != "" {
		payload["error_kind"] = tr.ErrorKind
	}
	// X-1: 填充 top-level SessionID 防止跨 session 泄漏。
	m.eventBus.BroadcastSessionMessage(sessionID, BroadcastMessage{
		Type:    EventTypeMessage,
		Payload: payload,
	})
}

// ensureWenyanTitle 检查 wenyan__publish_article 的 content 参数，
// 如果缺少 frontmatter title 则自动注入，避免 MCP 报 "未能找到文章标题"。
func ensureWenyanTitle(args json.RawMessage, logger *zap.Logger) json.RawMessage {
	var m map[string]any
	if err := json.Unmarshal(args, &m); err != nil {
		logger.Warn("ensureWenyanTitle: 参数解析失败", zap.Error(err))
		return args
	}
	contentRaw, ok := m["content"]
	if !ok {
		logger.Warn("ensureWenyanTitle: 参数中无 content 字段")
		return args
	}
	content, ok := contentRaw.(string)
	if !ok || content == "" {
		logger.Warn("ensureWenyanTitle: content 为空或非字符串", zap.Bool("is_string", ok))
		return args
	}

	// 检查是否已有 frontmatter
	hasFrontmatter := false
	fmEndPos := -1 // content 中第二个 "---" 的起始位置（绝对偏移）
	if strings.HasPrefix(content, "---") {
		idx := strings.Index(content[3:], "\n---")
		if idx >= 0 {
			fmEndPos = idx + 3 // "\n---" 中 '\n' 的位置
			fm := content[3:fmEndPos]
			hasFrontmatter = true
			if strings.Contains(fm, "\ntitle:") || strings.HasPrefix(fm, "title:") {
				return args // 已有 title，不需要注入
			}
		}
	}

	// 从正文提取标题：优先取 # 标题行，其次取前 30 个非空字符
	// 如果有 frontmatter，只在 frontmatter 之后的正文中搜索
	bodyForSearch := content
	if hasFrontmatter {
		// 跳过 frontmatter block（包括结尾的 "---\n"）
		afterFM := fmEndPos + 4 // 跳过 "\n---"
		if afterFM < len(content) {
			bodyForSearch = content[afterFM:]
		}
	}
	var title string
	for _, line := range strings.Split(bodyForSearch, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || line == "---" {
			continue
		}
		if strings.HasPrefix(line, "# ") {
			title = strings.TrimSpace(line[2:])
			break
		}
		// 非标题行，用作兜底标题
		title = line
		break
	}

	if title == "" {
		logger.Warn("ensureWenyanTitle: 无法从正文提取标题",
			zap.Int("content_len", len(content)),
			zap.Bool("has_frontmatter", hasFrontmatter))
		return args
	}

	// 注入 title
	var newContent string
	if hasFrontmatter {
		// 已有 frontmatter 但缺 title：在 frontmatter 内部插入 title 行
		// content = "---\n...existing fields...\n---\n正文"
		// 在第一个 "---\n" 之后插入 "title: xxx\n"
		newContent = "---\ntitle: " + title + "\n" + content[4:] // content[4:] 跳过开头的 "---\n"
	} else {
		// 无 frontmatter：在前面加新的 frontmatter block
		newContent = fmt.Sprintf("---\ntitle: %s\n---\n\n%s", title, content)
	}
	m["content"] = newContent
	out, err := json.Marshal(m)
	if err != nil {
		logger.Warn("ensureWenyanTitle: 序列化失败，保留原始参数",
			zap.Error(err))
		return args
	}
	logger.Info("ensureWenyanTitle: 自动注入 frontmatter title",
		zap.String("title", title))
	return out
}

// executeToolsConcurrent 使用 StreamingExecutor 并发执行工具。
// safe 工具（IsConcurrencySafe=true）并发执行，unsafe 工具按原始顺序串行。
// 维护与串行路径相同的状态：terminalCache, approvedCalls, terminalFailures。
func (m *Master) executeToolsConcurrent(
	ctx context.Context,
	session *SessionState,
	userID string,
	toolCalls []llm.ToolCall,
	rejectedIDs map[string]bool,
	terminalCache map[string]string,
	approvedCalls map[string]bool,
	terminalFailures *[]string,
	callFailureReflections *[]reflectionNoteInput,
	imRefsRead *bool,
	sessionTraceID, sessionSpanID string,
	detector *loopDetector,
) {
	// 1. 过滤 rejected 和 terminalCache，与串行路径逻辑一致
	type pendingCall struct {
		tc      llm.ToolCall
		toolCtx context.Context
	}
	var toExecute []pendingCall

	for _, toolCall := range toolCalls {
		if rejectedIDs[toolCall.ID] {
			errContent := fmt.Sprintf("错误：单轮最多 3 个 spawn_agent 调用，请减少并发或分批执行")
			toolCreatedAt := time.Now().Format(time.RFC3339)
			m.appendSessionMessage(session, llm.MessageWithTools{
				Role:       "tool",
				ToolCallID: toolCall.ID,
				Content:    llm.NewTextContent(errContent),
				IsError:    true,
				ToolName:   toolCall.Name,
				CreatedAt:  toolCreatedAt,
				Metadata:   toolResultMessageMetadata(toolResult{IsError: true, Terminal: true}),
			})
			m.broadcastToolMessage(session.ID, toolCall.ID, toolCall.Name, errContent, toolCreatedAt, toolResult{IsError: true, Terminal: true})
			continue
		}

		callFP := canonicalFingerprint(toolCall.Name, toolCall.Arguments)
		if cachedErr, hit := terminalCache[callFP]; hit {
			toolCreatedAt := time.Now().Format(time.RFC3339)
			m.appendSessionMessage(session, llm.MessageWithTools{
				Role:       "tool",
				ToolCallID: toolCall.ID,
				Content:    llm.NewTextContent(cachedErr),
				IsError:    true,
				ToolName:   toolCall.Name,
				CreatedAt:  toolCreatedAt,
				Metadata:   toolResultMessageMetadata(toolResult{IsError: true, Terminal: true}),
			})
			m.broadcastToolMessage(session.ID, toolCall.ID, toolCall.Name, cachedErr, toolCreatedAt, toolResult{IsError: true, Terminal: true})
			*terminalFailures = append(*terminalFailures, toolCall.Name)
			continue
		}

		toolCtx := ctx
		if approvedCalls[callFP] {
			toolCtx = toolctx.WithSkipPermission(ctx)
		}
		toExecute = append(toExecute, pendingCall{tc: toolCall, toolCtx: toolCtx})
	}

	if len(toExecute) == 0 {
		return
	}

	// 2. 构建 id -> pendingCall 映射，供 executor 闭包按 ID 回查
	callByID := make(map[string]pendingCall, len(toExecute))
	for _, p := range toExecute {
		callByID[p.tc.ID] = p
	}

	// 3. 获取工具定义列表（与 prepareDirectExecParams 路径一致）
	var availableTools []mcphost.ToolDefinition
	if m.toolBridge != nil {
		availableTools = m.toolBridge.AvailableTools(m.masterFilter)
	} else if m.mcpHost != nil {
		availableTools = m.mcpHost.ListTools()
	}

	// 4. 创建 batch 级可取消 context（SiblingAbortController：任一工具失败时取消其余工具）
	batchCtx, batchCancel := context.WithCancel(ctx)
	defer batchCancel()

	// 4b. 创建 StreamingExecutor
	// executor 通过 tool call ID（即 AddTool 的第一个参数）回查 pendingCall。
	// 由于 ToolExecutorFunc 签名为 (ctx, name, input)，不携带 ID，
	// 我们利用 StreamingExecutor 传入的 id 字段在 TrackedTool 中存储，
	// 但 executor 函数无法直接获取 ID。
	// 解决方案：用 canonicalFingerprint(name, input) 作为查找 key，
	// 因为同一轮 LLM 调用中相同 name+args 的 toolCtx 一致（均经过 approvedCalls 检查）。
	se := NewStreamingExecutor(
		availableTools,
		func(execCtx context.Context, name string, input json.RawMessage) (*mcphost.ToolResult, error) {
			fp := canonicalFingerprint(name, input)
			// 按 fingerprint 找到最先匹配的 pendingCall（顺序无关，toolCtx 一致）
			var resolvedCtx context.Context
			for _, p := range callByID {
				if canonicalFingerprint(p.tc.Name, p.tc.Arguments) == fp {
					resolvedCtx = p.toolCtx
					break
				}
			}
			if resolvedCtx == nil {
				resolvedCtx = execCtx
			}

			// 构建 ToolCall（executeTool 需要完整字段）
			tc := llm.ToolCall{Name: name, Arguments: input}
			for _, p := range callByID {
				if canonicalFingerprint(p.tc.Name, p.tc.Arguments) == fp {
					tc.ID = p.tc.ID
					break
				}
			}
			tr := m.executeTool(resolvedCtx, session, userID, tc, sessionTraceID, sessionSpanID)

			// 将 string content 编码为 json.RawMessage
			contentBytes, _ := json.Marshal(tr.Content)
			return &mcphost.ToolResult{Content: contentBytes, IsError: tr.IsError}, nil
		},
	)

	// 4c. 注册 emitFunc：当任一工具失败时取消 batchCtx，终止其余正在执行的工具
	se.SetEmitFunc(func(result *mcphost.ToolResult) {
		if result != nil && result.IsError {
			batchCancel()
		}
	})

	// 5. 按原始顺序添加工具（传入 batchCtx，失败时可通过 batchCancel 传播取消）
	for _, p := range toExecute {
		se.AddTool(batchCtx, p.tc.ID, p.tc.Name, p.tc.Arguments)
	}

	// 6. 等待所有工具完成，获取以 ID 为键的结果 map（Bug 2 fix: 避免 index 对齐错误）
	resultsByID := se.GetResultsByID()

	// 7. 按原始顺序处理结果，通过工具 ID 精确查找对应结果
	for _, p := range toExecute {
		toolCall := p.tc
		r, ok := resultsByID[toolCall.ID]
		if !ok {
			// 工具被 abort 或未产生结果，跳过
			continue
		}

		// 解码结果内容（executor 闭包中用 json.Marshal(string) 编码）
		contentStr := r.DecodeContent()
		isError := r.IsError

		tr := toolResult{
			Content:     contentStr,
			IsError:     isError,
			Terminal:    isError && isTerminalError(contentStr),
			Recoverable: isError && isRecoverableToolCallError(contentStr),
			ErrorKind:   recoverableToolCallErrorKind(contentStr),
		}
		toolCreatedAt := time.Now().Format(time.RFC3339)
		m.appendSessionMessage(session, llm.MessageWithTools{
			Role:       "tool",
			ToolCallID: toolCall.ID,
			Content:    llm.NewTextContent(contentStr),
			IsError:    isError,
			ToolName:   toolCall.Name,
			CreatedAt:  toolCreatedAt,
			Metadata:   toolResultMessageMetadata(tr),
		})
		m.broadcastToolMessage(session.ID, toolCall.ID, toolCall.Name, contentStr, toolCreatedAt, tr)

		callFP := canonicalFingerprint(toolCall.Name, toolCall.Arguments)
		if isError {
			// 与串行路径对齐：检测终端错误（配置/权限/白名单等不可重试错误），写入 terminalCache
			if isTerminalError(contentStr) {
				terminalCache[callFP] = contentStr
				*terminalFailures = append(*terminalFailures, toolCall.Name)
				if kind := reflectionFailureKindFromToolError(contentStr); kind != "" {
					*callFailureReflections = append(*callFailureReflections, reflectionNoteInput{
						Trigger:     "call_failure",
						Severity:    "warn",
						ToolName:    toolCall.Name,
						Consecutive: 1,
						Detail:      contentStr,
						FailureKind: kind,
					})
				}
			} else if tr.Recoverable {
				if detector != nil {
					detector.recordCallResult(callFP, false)
				}
			} else if detector != nil && detector.recordCallResult(callFP, true) {
				terminalCache[callFP] = contentStr
				*terminalFailures = append(*terminalFailures, toolCall.Name)
				*callFailureReflections = append(*callFailureReflections, reflectionNoteInput{
					Trigger:     "call_failure",
					Severity:    "warn",
					ToolName:    toolCall.Name,
					Consecutive: 2,
					Detail:      contentStr,
				})
			}
		} else {
			if detector != nil {
				detector.recordCallResult(callFP, false)
			}
			recordToolDiscoveryFromResult(session, toolCall, contentStr, false)
			approvedCalls[callFP] = true
			if imRefsRead != nil && isSuccessfulIMReferenceRead(toolCall, false) {
				*imRefsRead = true
			}
		}
	}
}

func kbRuntimeContextForTool(session *SessionState, userID string) tools.KBRuntimeContext {
	ownerID := ownerIDForSessionRuntime(session, userID)
	domainID := domainIDForSessionRuntime(session)
	sessionID := ""
	if session != nil {
		session.mu.RLock()
		sessionID = strings.TrimSpace(session.ID)
		session.mu.RUnlock()
	}
	return tools.KBRuntimeContext{
		DomainID:   domainID,
		OwnerScope: kb.OwnerScopeUser,
		OwnerID:    ownerID,
		AgentID:    "master",
		SessionID:  sessionID,
	}
}

func (m *Master) kbCitationsForCurrentTurn(ctx context.Context, session *SessionState, userID, traceID string) string {
	if m == nil || session == nil {
		return ""
	}
	if m.kbEvidenceReader == nil {
		m.recordKBCitationMissing(session, userID, traceID, "reader_unavailable", "")
		return ""
	}
	runtime := kbRuntimeContextForTool(session, userID)
	scope := kb.EvidenceScope{
		SessionID:  session.ID,
		TurnID:     traceID,
		TraceID:    traceID,
		DomainID:   runtime.DomainID,
		OwnerScope: runtime.OwnerScope,
		OwnerID:    runtime.OwnerID,
		Now:        time.Now(),
	}
	refs, err := m.kbEvidenceReader.CurrentTurnEvidence(ctx, scope)
	if err != nil {
		m.recordKBCitationMissing(session, userID, traceID, "read_error", err.Error())
		return ""
	}
	if len(refs) == 0 {
		m.recordKBCitationMissing(session, userID, traceID, "empty_current_turn_evidence", "")
		return ""
	}
	data, err := json.Marshal(refs)
	if err != nil {
		m.recordKBCitationMissing(session, userID, traceID, "marshal_error", err.Error())
		return ""
	}
	return string(data)
}

func (m *Master) recoverSessionKBDomainFromBinding(ctx context.Context, session *SessionState, userID string) bool {
	if m == nil || session == nil || m.sessionMgr == nil || m.sessionMgr.kbBindingHints == nil {
		return false
	}
	if strings.TrimSpace(session.KBDomainIDSnapshot()) != "" {
		return true
	}
	ownerID := ownerIDForSessionRuntime(session, userID)
	sessionID := strings.TrimSpace(session.ID)
	if ownerID == "" || sessionID == "" {
		return false
	}
	domainID, ok, err := m.sessionMgr.kbBindingHints.ActiveBindingHint(ctx, kb.ActiveBindingHintInput{
		OwnerScope:    kb.OwnerScopeUser,
		OwnerID:       ownerID,
		BindingType:   kb.BindingTypeSession,
		BindingTarget: sessionID,
		Now:           time.Now(),
	})
	if err != nil {
		m.logger.Warn("恢复会话 KB domain 失败",
			zap.String("session_id", sessionID),
			zap.String("owner_id", ownerID),
			zap.Error(err))
		return false
	}
	if !ok || strings.TrimSpace(domainID) == "" {
		return false
	}
	session.SetKBDomainID(domainID)
	return true
}

func (m *Master) recordKBCitationMissing(session *SessionState, userID, traceID, reason, errText string) {
	if m == nil || session == nil {
		return
	}
	runtime := kbRuntimeContextForTool(session, userID)
	m.emitQualityEvent(traceID, "", session.ID, agentquality.NewKBQualityEvent(agentquality.KBEventInput{
		Name:       agentquality.EventKBEvidence,
		TurnID:     traceID,
		TraceID:    traceID,
		DomainID:   runtime.DomainID,
		OwnerScope: agentquality.OwnerScope(runtime.OwnerScope),
		OwnerID:    runtime.OwnerID,
		UserID:     userID,
		Route:      routeFromSession(session),
		ToolName:   "kb.citations",
		Status:     agentquality.StatusFail,
		Failure:    agentquality.FailureKBEvidence,
		Reason:     agentquality.KBFailureCitationMissing,
		Error:      errText,
		Attributes: map[string]any{
			"operation":       "kb.citations",
			"missing_reason":  reason,
			"session_user_id": userID,
		},
	}))
}
