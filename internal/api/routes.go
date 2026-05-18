package api

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/chef-guo/agents-hive/internal/auth"
	"github.com/chef-guo/agents-hive/internal/webui"
)

func (s *Server) registerRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/agents", s.handleListAgents)
	mux.HandleFunc("GET /api/v1/skills", s.handleListSkills)
	mux.HandleFunc("GET /api/v1/metrics/skills", s.handleSkillMetrics)
	mux.HandleFunc("GET /api/v1/health", s.handleHealth)
	mux.HandleFunc("GET /api/v1/health/feishu", s.handleFeishuHealth)
	mux.HandleFunc("GET /api/v1/assets/resolve", s.handleResolveAsset)
	mux.HandleFunc("GET /api/v1/assets/proxy", s.handleProxyAsset)
	mux.HandleFunc("GET /api/v1/kb/namespaces", s.handleKBListNamespaces)
	mux.HandleFunc("POST /api/v1/kb/namespaces", s.handleKBCreateNamespace)
	mux.HandleFunc("GET /api/v1/kb/namespaces/{id}/documents", s.handleKBListDocuments)
	mux.HandleFunc("POST /api/v1/kb/namespaces/{id}/documents:preview-markdown", s.handleKBPreviewMarkdown)
	mux.HandleFunc("POST /api/v1/kb/namespaces/{id}/documents:ingest-markdown", s.handleKBIngestMarkdown)
	mux.HandleFunc("GET /api/v1/kb/documents/{id}/tree", s.handleKBGetDocumentTree)
	mux.HandleFunc("GET /api/v1/kb/documents/{id}/nodes/{node_id}", s.handleKBGetDocumentNode)
	mux.HandleFunc("POST /api/v1/kb/documents/{id...}", s.handleKBArchiveDocument)
	mux.HandleFunc("GET /api/v1/kb/evidence", s.handleKBListEvidence)
	mux.HandleFunc("GET /api/v1/kb/bindings", s.handleKBListBindings)
	mux.HandleFunc("POST /api/v1/kb/bindings", s.handleKBCreateBinding)
	mux.HandleFunc("PATCH /api/v1/kb/bindings/{id}", s.handleKBUpdateBinding)
	mux.HandleFunc("DELETE /api/v1/kb/bindings/{id}", s.handleKBDeleteBinding)
	mux.HandleFunc("GET /api/v1/kb/effective-bindings", s.handleKBEffectiveBindings)
	mux.HandleFunc("POST /api/v1/cs/sessions", s.handleCSCreateSession)
	mux.HandleFunc("GET /api/v1/cs/sessions/{id}/state", s.handleCSGetSessionState)
	mux.HandleFunc("POST /api/v1/cs/sessions/{id}/escalate/cancel", s.handleCSCancelSessionEscalation)
	mux.HandleFunc("POST /api/v1/cs/webhooks/subscriptions", s.handleCSCreateWebhookSubscription)
	mux.HandleFunc("GET /api/v1/cs/webhooks/subscriptions", s.handleCSListWebhookSubscriptions)
	mux.HandleFunc("GET /api/v1/cs/webhooks/dlq", s.handleCSListDLQ)
	mux.HandleFunc("POST /api/v1/cs/webhooks/dlq/{id}/retry", s.handleCSRetryDLQ)
	mux.HandleFunc("POST /api/v1/channels/push", s.handleChannelPush)
	mux.HandleFunc("POST /api/v1/channels/push/schedules", s.handleCreatePushSchedule)
	mux.HandleFunc("GET /api/v1/channels/push/schedules", s.handleListPushSchedules)
	mux.HandleFunc("DELETE /api/v1/channels/push/schedules/{id}", s.handleDeletePushSchedule)
	mux.HandleFunc("POST /api/v1/scheduled-tasks", s.handleCreateScheduledTask)
	mux.HandleFunc("GET /api/v1/scheduled-tasks", s.handleListScheduledTasks)
	mux.HandleFunc("GET /api/v1/scheduled-tasks/{id}", s.handleGetScheduledTask)
	mux.HandleFunc("PUT /api/v1/scheduled-tasks/{id}", s.handleUpdateScheduledTask)
	mux.HandleFunc("DELETE /api/v1/scheduled-tasks/{id}", s.handleDeleteScheduledTask)
	mux.HandleFunc("POST /api/v1/scheduled-tasks/{id}/toggle", s.handleToggleScheduledTask)
	mux.HandleFunc("POST /api/v1/scheduled-tasks/{id}/run-now", s.handleRunScheduledTaskNow)
	mux.HandleFunc("GET /api/v1/scheduled-tasks/{id}/runs", s.handleListScheduledTaskRuns)
	mux.HandleFunc("GET /api/v1/auth/status", s.handleAuthStatus)
	mux.HandleFunc("GET /api/v1/capabilities", s.handleListCapabilities)
	mux.HandleFunc("GET /api/v1/wechat/status", s.handleWeChatStatus)
	mux.HandleFunc("POST /api/v1/wechat/login", s.handleWeChatLogin)
	mux.HandleFunc("POST /api/v1/wechat/relogin", s.handleWeChatRelogin)
	mux.HandleFunc("POST /api/v1/wechat/logout", s.handleWeChatLogout)
	mux.HandleFunc("GET /api/v1/wechat/events", s.handleWeChatEvents)
	mux.HandleFunc("GET /api/v1/wechat/conversations", s.handleWeChatConversations)

	// HITL endpoints
	mux.HandleFunc("POST /api/v1/tasks/{id}/input", s.handleSubmitInput)
	mux.HandleFunc("POST /api/v1/tasks/{id}/command", s.handleSendCommand)
	mux.HandleFunc("GET /api/v1/tasks/{id}/pending-input", s.handleGetPendingInput)

	// Session endpoints
	mux.HandleFunc("POST /api/v1/sessions", s.handleCreateSession)
	mux.HandleFunc("GET /api/v1/sessions", s.handleListSessions)
	mux.HandleFunc("GET /api/v1/sessions/{id}", s.handleGetSession)
	mux.HandleFunc("PATCH /api/v1/sessions/{id}", s.handleUpdateSession)
	mux.HandleFunc("DELETE /api/v1/sessions/{id}", s.handleDeleteSession)
	mux.HandleFunc("POST /api/v1/sessions/{id}/messages", s.handleSendMessage)
	mux.HandleFunc("POST /api/v1/sessions/{id}/clear", s.handleClearSession)
	mux.HandleFunc("GET /api/v1/sessions/{id}/messages", s.handleGetMessages)
	mux.HandleFunc("GET /api/v1/sessions/{id}/todos", s.handleGetSessionTodos)
	mux.HandleFunc("GET /api/v1/sessions/{id}/trace", s.handleGetSessionTrace)
	mux.HandleFunc("GET /api/v1/sessions/{id}/trajectory/{step}", s.handleGetSessionTrajectory)
	mux.HandleFunc("POST /api/v1/sessions/{id}/todos/resume", s.handlePlanSessionTodoResume)
	mux.HandleFunc("POST /api/v1/sessions/{id}/fork", s.handleForkSession)
	mux.HandleFunc("POST /api/v1/sessions/{id}/fork-from-step", s.handleForkFromStep)
	mux.HandleFunc("POST /api/v1/sessions/{id}/revert", s.handleRevertSession)
	mux.HandleFunc("POST /api/v1/sessions/{id}/regenerate", s.handleRegenerateMessage)
	mux.HandleFunc("POST /api/v1/sessions/{id}/stop", s.handleStopSession)
	mux.HandleFunc("PATCH /api/v1/sessions/{id}/star", s.handleStarSession)
	mux.HandleFunc("PATCH /api/v1/sessions/{id}/tags", s.handleUpdateTags)

	// Journal endpoints（回放剧场）
	mux.HandleFunc("GET /api/v1/sessions/{id}/journal", s.handleGetSessionJournal)
	mux.HandleFunc("GET /api/v1/journal/stats", s.handleGetJournalStats)

	// Model 端点
	mux.HandleFunc("GET /api/v1/models", s.handleListModels)
	mux.HandleFunc("PUT /api/v1/model", s.handleSwitchModel)

	// Tools 端点（白名单工具直接调用，用于预览等无副作用操作）
	mux.HandleFunc("POST /api/v1/tools/invoke", s.handleInvokeTool)

	// Config endpoints
	mux.HandleFunc("POST /api/v1/config/save", s.handleSaveConfig)
	// Phase 5 缺口 13 修复:补飞书热重载入口,触发 unregister + rebuild + ReloadFromConfig 链路。
	mux.HandleFunc("POST /api/v1/config/channels/feishu/reload", s.handleReloadFeishu)

	// WebSocket 端点（条件启用）
	if s.hitlConfig.WebSocketEnabled {
		mux.HandleFunc("GET /api/v1/ws", s.handleWebSocket)
	}

	// Auth 登录/OAuth/JWT 端点（仅认证引擎初始化后注册）
	if s.authEngine != nil {
		mux.HandleFunc("GET /api/v1/auth/providers", s.handleListAuthProviders)
		mux.HandleFunc("GET /api/v1/auth/login", s.handleAuthLogin)
		mux.HandleFunc("GET /api/v1/auth/callback", s.handleAuthCallback)
		mux.HandleFunc("POST /api/v1/auth/login", s.handleLDAPLogin)
		mux.HandleFunc("GET /api/v1/auth/me", s.handleGetCurrentUser)
		mux.HandleFunc("POST /api/v1/auth/refresh", s.handleRefreshToken)
		mux.HandleFunc("POST /api/v1/auth/register", s.handleAuthRegister)
	}

	// Admin 端点：启用认证时校验 admin JWT；未启用时不校验（内网/开发，勿暴露公网）
	adminOnly := func(h http.HandlerFunc) http.HandlerFunc {
		if s.authEngine != nil {
			return auth.AdminOnly(h)
		}
		return h
	}

	mux.HandleFunc("GET /api/v1/admin/scheduled-tasks", adminOnly(s.handleAdminListScheduledTasks))

	// 用户管理 / 认证 Provider（依赖 auth 存储，仅在认证引擎初始化后注册）
	if s.authEngine != nil {
		mux.HandleFunc("GET /api/v1/admin/users", adminOnly(s.handleListUsers))
		mux.HandleFunc("GET /api/v1/admin/users/{id}", adminOnly(s.handleGetUser))
		mux.HandleFunc("PATCH /api/v1/admin/users/{id}", adminOnly(s.handleUpdateUser))
		mux.HandleFunc("PATCH /api/v1/admin/users/{id}/quota", adminOnly(s.handleUpdateQuota))
		mux.HandleFunc("GET /api/v1/admin/users/{id}/logins", adminOnly(s.handleUserLogins))
		mux.HandleFunc("DELETE /api/v1/admin/users/{id}", adminOnly(s.handleDeleteUser))

		mux.HandleFunc("GET /api/v1/admin/auth/invite-codes", adminOnly(s.handleListInviteCodes))
		mux.HandleFunc("POST /api/v1/admin/auth/invite-codes", adminOnly(s.handleCreateInviteCode))
		mux.HandleFunc("PATCH /api/v1/admin/auth/invite-codes/{id}", adminOnly(s.handleUpdateInviteCode))
		mux.HandleFunc("DELETE /api/v1/admin/auth/invite-codes/{id}", adminOnly(s.handleDeleteInviteCode))

		mux.HandleFunc("POST /api/v1/admin/assets/gc", adminOnly(s.handleAdminAssetGC))

		mux.HandleFunc("GET /api/v1/admin/auth/providers", adminOnly(s.handleAdminListProviders))
		mux.HandleFunc("POST /api/v1/admin/auth/providers", adminOnly(s.handleCreateProvider))
		mux.HandleFunc("PATCH /api/v1/admin/auth/providers/{name}", adminOnly(s.handleUpdateProvider))
		mux.HandleFunc("DELETE /api/v1/admin/auth/providers/{name}", adminOnly(s.handleDeleteProvider))
	}

	// 用量统计
	mux.HandleFunc("GET /api/v1/admin/usage/summary", adminOnly(s.handleUsageSummary))
	mux.HandleFunc("GET /api/v1/admin/usage/by-user", adminOnly(s.handleUsageByUser))
	mux.HandleFunc("GET /api/v1/admin/usage/by-model", adminOnly(s.handleUsageByModel))
	mux.HandleFunc("GET /api/v1/admin/usage/quality", adminOnly(s.handleUsageQuality))

	// Prompt 管理（DB 覆盖，运行时热更新）
	mux.HandleFunc("GET /api/v1/admin/prompts", adminOnly(s.handleListPrompts))
	mux.HandleFunc("GET /api/v1/admin/prompts/{key...}", adminOnly(s.handleGetPrompt))
	mux.HandleFunc("PUT /api/v1/admin/prompts/{key...}", adminOnly(s.handleUpsertPrompt))
	mux.HandleFunc("DELETE /api/v1/admin/prompts/{key...}", adminOnly(s.handleDeletePrompt))

	// Skill 管理（DB 覆盖，运行时热更新）
	mux.HandleFunc("GET /api/v1/admin/skills", adminOnly(s.handleListAdminSkills))
	mux.HandleFunc("GET /api/v1/admin/skills/db", adminOnly(s.handleListDBSkills))
	mux.HandleFunc("GET /api/v1/admin/skills/{name}", adminOnly(s.handleGetAdminSkill))
	mux.HandleFunc("PUT /api/v1/admin/skills/{name}", adminOnly(s.handleUpsertAdminSkill))
	mux.HandleFunc("DELETE /api/v1/admin/skills/{name}", adminOnly(s.handleDeleteAdminSkill))

	// LLM Provider 管理
	mux.HandleFunc("GET /api/v1/admin/llm/providers", adminOnly(s.handleAdminListLLMProviders))
	mux.HandleFunc("POST /api/v1/admin/llm/providers", adminOnly(s.handleAdminCreateLLMProvider))
	mux.HandleFunc("PATCH /api/v1/admin/llm/providers/{name}", adminOnly(s.handleAdminUpdateLLMProvider))
	mux.HandleFunc("DELETE /api/v1/admin/llm/providers/{name}", adminOnly(s.handleAdminDeleteLLMProvider))

	// LLM Model 管理
	mux.HandleFunc("GET /api/v1/admin/llm/models", adminOnly(s.handleAdminListLLMModels))
	mux.HandleFunc("POST /api/v1/admin/llm/models", adminOnly(s.handleAdminCreateLLMModel))
	mux.HandleFunc("PATCH /api/v1/admin/llm/models/{name}", adminOnly(s.handleAdminUpdateLLMModel))
	mux.HandleFunc("DELETE /api/v1/admin/llm/models/{name}", adminOnly(s.handleAdminDeleteLLMModel))

	// Agent Quality 候选用例池
	mux.HandleFunc("GET /api/v1/admin/quality/cases", adminOnly(s.handleAdminQualityListCases))
	mux.HandleFunc("POST /api/v1/admin/quality/prompt-smoke", adminOnly(s.handleAdminQualityPromptSmoke))
	mux.HandleFunc("GET /api/v1/admin/quality/candidates", adminOnly(s.handleAdminQualityListCandidates))
	mux.HandleFunc("POST /api/v1/admin/quality/candidates", adminOnly(s.handleAdminQualityCreateCandidate))
	mux.HandleFunc("PATCH /api/v1/admin/quality/candidates/{id}", adminOnly(s.handleAdminQualityUpdateCandidate))
	mux.HandleFunc("GET /api/v1/admin/quality/candidates/{id}/golden-case", adminOnly(s.handleAdminQualityExportCandidate))
	mux.HandleFunc("GET /api/v1/admin/quality-workbench/clusters", adminOnly(s.handleAdminQualityWorkbenchClusters))
	mux.HandleFunc("POST /api/v1/admin/quality-workbench/clusters/recompute", adminOnly(s.handleAdminQualityWorkbenchRecomputeClusters))
	mux.HandleFunc("POST /api/v1/admin/quality-workbench/replays", adminOnly(s.handleAdminQualityWorkbenchCreateReplays))
	mux.HandleFunc("GET /api/v1/admin/quality-workbench/replays", adminOnly(s.handleAdminQualityWorkbenchListReplays))
	mux.HandleFunc("GET /api/v1/admin/quality-workbench/replays/{id}", adminOnly(s.handleAdminQualityWorkbenchGetReplay))
	mux.HandleFunc("POST /api/v1/admin/quality-workbench/replays/{id}/run", adminOnly(s.handleAdminQualityWorkbenchRunReplay))
	mux.HandleFunc("POST /api/v1/admin/quality-workbench/replays/{id}/cancel", adminOnly(s.handleAdminQualityWorkbenchCancelReplay))
	mux.HandleFunc("POST /api/v1/admin/quality-workbench/grouping-rules/preview", adminOnly(s.handleAdminQualityWorkbenchPreviewGroupingRules))
	mux.HandleFunc("GET /api/v1/admin/quality-workbench/grouping-rules", adminOnly(s.handleAdminQualityWorkbenchListGroupingRules))
	mux.HandleFunc("PUT /api/v1/admin/quality-workbench/grouping-rules/{id}", adminOnly(s.handleAdminQualityWorkbenchUpsertGroupingRule))
	mux.HandleFunc("DELETE /api/v1/admin/quality-workbench/grouping-rules/{id}", adminOnly(s.handleAdminQualityWorkbenchDeleteGroupingRule))
	mux.HandleFunc("POST /api/v1/admin/quality-workbench/replays/fanout", adminOnly(s.handleAdminQualityWorkbenchReplayFanout))
	mux.HandleFunc("POST /api/v1/admin/quality-workbench/version-diff", adminOnly(s.handleAdminQualityWorkbenchVersionDiff))
	mux.HandleFunc("POST /api/v1/admin/quality-workbench/batch-evals", adminOnly(s.handleAdminQualityWorkbenchCreateBatchEval))
	mux.HandleFunc("GET /api/v1/admin/quality-workbench/batch-evals", adminOnly(s.handleAdminQualityWorkbenchListBatchEvals))
	mux.HandleFunc("GET /api/v1/admin/quality-workbench/batch-evals/{id}", adminOnly(s.handleAdminQualityWorkbenchGetBatchEval))
	mux.HandleFunc("GET /api/v1/admin/quality-workbench/dashboard/snapshot", adminOnly(s.handleAdminQualityWorkbenchDashboardSnapshot))
	mux.HandleFunc("GET /api/v1/admin/quality-workbench/dashboard/series", adminOnly(s.handleAdminQualityWorkbenchDashboardSeries))
	mux.HandleFunc("GET /api/v1/admin/quality-workbench/reports", adminOnly(s.handleAdminQualityWorkbenchListReports))
	mux.HandleFunc("GET /api/v1/admin/quality-workbench/reports/{id}", adminOnly(s.handleAdminQualityWorkbenchGetReport))
	mux.HandleFunc("POST /api/v1/admin/quality-workbench/reports/generate", adminOnly(s.handleAdminQualityWorkbenchGenerateReport))
	mux.HandleFunc("GET /api/v1/admin/quality-workbench/reports/{id}/download", adminOnly(s.handleAdminQualityWorkbenchDownloadReport))
	mux.HandleFunc("GET /api/v1/admin/sessiontodo/ops/snapshot", adminOnly(s.handleAdminSessionTodoOpsSnapshot))
	mux.HandleFunc("GET /api/v1/admin/memory/governance", adminOnly(s.handleAdminMemoryGovernance))
	mux.HandleFunc("POST /api/v1/admin/memory/prune", adminOnly(s.handleAdminMemoryPrune))
	mux.HandleFunc("GET /api/v1/admin/memory/export", adminOnly(s.handleAdminMemoryExport))
	mux.HandleFunc("POST /api/v1/admin/memory/import", adminOnly(s.handleAdminMemoryImport))
	mux.HandleFunc("GET /api/v1/admin/memory/injection/explain", adminOnly(s.handleAdminMemoryInjectionExplain))
	mux.HandleFunc("GET /api/v1/admin/memory/promotions/candidates", adminOnly(s.handleAdminMemoryPromotionCandidates))
	mux.HandleFunc("POST /api/v1/admin/memory/promotions/apply", adminOnly(s.handleAdminMemoryPromotionApply))
	mux.HandleFunc("POST /api/v1/admin/memory/vector-space/plan", adminOnly(s.handleAdminMemoryVectorSpacePlan))
	mux.HandleFunc("GET /api/v1/admin/memory/backlog/stats", adminOnly(s.handleAdminMemoryBacklogStats))
	mux.HandleFunc("GET /api/v1/admin/memory/metrics", adminOnly(s.handleAdminMemoryProductionMetrics))
	mux.HandleFunc("GET /api/v1/admin/optimization/suggestions", adminOnly(s.handleAdminOptimizationListSuggestions))
	mux.HandleFunc("POST /api/v1/admin/optimization/suggestions", adminOnly(s.handleAdminOptimizationGenerateSuggestions))
	mux.HandleFunc("POST /api/v1/admin/optimization/suggestions/{id}/approve", adminOnly(s.handleAdminOptimizationApproveSuggestion))
	mux.HandleFunc("POST /api/v1/admin/optimization/suggestions/{id}/reject", adminOnly(s.handleAdminOptimizationRejectSuggestion))
	mux.HandleFunc("POST /api/v1/admin/optimization/suggestions/{id}/apply", adminOnly(s.handleAdminOptimizationApplySuggestion))
	mux.HandleFunc("POST /api/v1/admin/optimization/suggestions/{id}/rollback", adminOnly(s.handleAdminOptimizationRollbackSuggestion))
	mux.HandleFunc("POST /api/v1/admin/optimization/eval-diffs", adminOnly(s.handleAdminOptimizationComputeEvalDiff))
	mux.HandleFunc("GET /api/v1/admin/optimization/eval-diffs", adminOnly(s.handleAdminOptimizationListEvalDiffs))
	mux.HandleFunc("GET /api/v1/admin/optimization/eval-diffs/{id}", adminOnly(s.handleAdminOptimizationGetEvalDiff))
	mux.HandleFunc("POST /api/v1/admin/optimization/eval-diffs/suggestions", adminOnly(s.handleAdminOptimizationGenerateEvalDiffSuggestions))
	mux.HandleFunc("POST /api/v1/admin/optimization/eval-diffs/{id}/report", adminOnly(s.handleAdminOptimizationABReport))
	mux.HandleFunc("GET /api/v1/admin/optimization/approvals", adminOnly(s.handleAdminOptimizationListApprovals))
	mux.HandleFunc("POST /api/v1/admin/optimization/approvals", adminOnly(s.handleAdminOptimizationCreateApproval))
	mux.HandleFunc("POST /api/v1/admin/optimization/rollback-alerts/evaluate", adminOnly(s.handleAdminOptimizationEvaluateRollbackAlert))
	mux.HandleFunc("GET /api/v1/admin/optimization/rollback-alerts", adminOnly(s.handleAdminOptimizationListRollbackAlerts))
	mux.HandleFunc("GET /api/v1/admin/optimization/rollbacks", adminOnly(s.handleAdminOptimizationListRollbacks))

	// Runtime Policy 只读查看
	mux.HandleFunc("GET /api/v1/admin/runtime/policy", adminOnly(s.handleAdminRuntimePolicy))

	// 图片临时文件服务（Gemini inlineData 生成的图片，通过 /api/images/<filename> 访问）
	mux.HandleFunc("/api/images/", handleServeImage)

	// WebUI 前端静态资源（SPA fallback）
	if s.webuiEnabled {
		mux.Handle("/", webui.Handler())
	}
}

// handleServeImage 提供临时图片文件服务。
// 安全约束：文件名不得包含路径分隔符或 ".."，防止路径穿越攻击。
func handleServeImage(w http.ResponseWriter, r *http.Request) {
	filename := strings.TrimPrefix(r.URL.Path, "/api/images/")
	// 安全检查：防止路径穿越
	if filename == "" || strings.Contains(filename, "..") || strings.ContainsAny(filename, "/\\") {
		http.Error(w, "invalid filename", http.StatusBadRequest)
		return
	}
	imgPath := filepath.Join(os.TempDir(), "hive-images", filename)
	http.ServeFile(w, r, imgPath)
}
