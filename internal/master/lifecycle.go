package master

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/chef-guo/agents-hive/internal/agentquality"
	"github.com/chef-guo/agents-hive/internal/errs"
	"github.com/chef-guo/agents-hive/internal/observability"
	"github.com/chef-guo/agents-hive/internal/plugin"
	"github.com/chef-guo/agents-hive/internal/router"
	"github.com/chef-guo/agents-hive/internal/security"
	"github.com/chef-guo/agents-hive/internal/skills"
	"github.com/chef-guo/agents-hive/internal/store"
	"github.com/chef-guo/agents-hive/internal/toolctx"
)

// isShellFamilyTool 返回 toolName 是否属于 shell 家族。
func (m *Master) isShellFamilyTool(toolName string) bool {
	return router.IsShellCommandTool(toolName)
}

// extractShellCommand 从 shell 家族工具的 Input JSON 中提取 command 字段。
// 成功返回 cmd 字符串；失败返回 ""（调用方按 safe-deny 处理）。
func extractShellCommand(input json.RawMessage) (string, bool) {
	if len(input) == 0 {
		return "", false
	}
	var payload struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal(input, &payload); err != nil {
		return "", false
	}
	if payload.Command == "" {
		return "", false
	}
	return payload.Command, true
}

// Start 初始化并启动所有已注册的 sub-agents
func (m *Master) Start(ctx context.Context) {
	m.registry.StartAll(ctx)
	m.logger.Info("启动 master agent")

	// 加载外部资源配置到缓存
	m.loadExternalResources()

	// 注册配置变更监听，当外部资源配置变更时自动刷新缓存
	if fullStore, ok := m.store.(store.Store); ok {
		fullStore.OnConfigChange(func(key string) {
			if strings.HasPrefix(key, "resource:") {
				m.loadExternalResources()
			}
			// 触发 ConfigChange hook（非阻塞，防止慢插件阻塞 Store 通知链）
			if m.pluginMgr != nil {
				go func() {
					hookCtx, cancel := context.WithTimeout(context.Background(), plugin.HookCallTimeout)
					defer cancel()
					_ = m.pluginMgr.TriggerConfigChange(hookCtx, &plugin.ConfigChangeInput{
						Key: key,
					})
				}()
			}
		})
	}

	// 监听 MCP 工具列表变更
	if m.mcpHost != nil {
		go m.watchToolChanges(ctx)
	}
}

// Stop 关闭所有 sub-agents
func (m *Master) Stop() {
	// 1. 先关闭 stopCh，触发 SessionLoop 退出
	m.stopOnce.Do(func() {
		close(m.stopCh)
		m.logger.Info("已发送停止信号")
	})

	// 2. 等待一小段时间让 SessionLoop 完成退出
	// 这避免了在 SessionLoop 仍在使用 channel 时就关闭它们
	time.Sleep(100 * time.Millisecond)

	// 3. 然后关闭会话通道
	m.closeOnce.Do(func() {
		m.sessionMgr.CloseChannels()
	})

	// 4. 关闭前保存所有会话数据
	if m.store != nil {
		m.logger.Info("关闭前保存所有会话")
		if err := m.sessionMgr.SaveAllSessions(context.Background(), m.store); err != nil {
			m.logger.Warn("保存会话失败", zap.Error(err))
		}
	}

	// 清理动态 Agent（在 StopAll 之前执行，确保从 registry 正确注销）
	if m.agentFactory != nil {
		m.agentFactory.CleanupAll()
	}

	// 触发所有活跃会话的 SessionEnd hook（在数据完全写入后触发，确保插件看到完整状态）
	if m.pluginMgr != nil {
		sessions := m.sessionMgr.ListActiveSessions()
		for _, sid := range sessions {
			hookCtx, cancel := context.WithTimeout(context.Background(), plugin.HookCallTimeout)
			_ = m.pluginMgr.TriggerSessionEnd(hookCtx, &plugin.SessionEndInput{
				SessionID: sid,
			})
			cancel()
		}
	}

	m.registry.StopAll()

	// 等待成本追踪 worker 写完所有飞行中的条目，再关闭 DB 连接池
	// 必须在 StopAll() 之后执行：agent 停止后不再有新的 Submit() 调用
	if m.asyncRecorder != nil {
		m.asyncRecorder.Stop()
	}

	// 5. 关停事件总线：等待所有后台重试 goroutine 结束，释放订阅者资源
	if m.eventBus != nil {
		m.eventBus.Close()
	}

	m.logger.Info("停止 master agent")
}

// watchToolChanges 监听 MCP 工具列表变更，触发工具描述刷新
func (m *Master) watchToolChanges(ctx context.Context) {
	ch := m.mcpHost.OnToolListChanged()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ch:
			tools := m.mcpHost.ListTools()
			m.logger.Info("检测到工具列表变更，已刷新",
				zap.Int("当前工具数", len(tools)),
			)
			// 通知可能的监听者（如 WebSocket 客户端）
			// no session scope by design — tool catalog 是全局共享视图
			m.eventBus.BroadcastGenericMessage(EventTypeToolListChanged, map[string]interface{}{
				"tool_count": len(tools),
			})
		}
	}
}

// startBackgroundSync 启动后台自动同步
func (m *Master) startBackgroundSync(ctx context.Context) {
	interval := m.syncInterval
	if interval == 0 {
		interval = 5 * time.Minute
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	m.logger.Info("后台自动同步已启动", zap.Duration("interval", interval))

	for {
		select {
		case <-ctx.Done():
			// 退出前做最终一次保存，使用独立 context 防止已取消的 ctx 导致保存失败
			m.logger.Info("后台自动同步正在退出，执行最终保存")
			finalCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			if err := m.sessionMgr.SaveAllSessions(finalCtx, m.store); err != nil {
				m.logger.Warn("退出时最终保存失败", zap.Error(err))
			}
			cancel()
			m.logger.Info("后台自动同步已停止")
			return

		case <-ticker.C:
			m.logger.Debug("执行后台会话同步")
			if err := m.sessionMgr.SaveAllSessions(ctx, m.store); err != nil {
				m.logger.Warn("后台同步保存会话失败", zap.Error(err))
			}
		}
	}
}

// createPermissionPromptFn 创建权限请求提示函数。
//
// 权限极简架构（v2, add-spec-driven-cognition Phase 1）：
//  1. 非 shell 家族工具 → 直接 Granted:true（Input 是结构化 JSON，不是 shell 文本）
//  2. shell 家族工具 → 先调 SafeExecutor.MatchPolicy
//     - PolicyDeny → Granted:false（绝对禁止）+ audit log + metric
//     - PolicyAsk → 走 HITL 审批（仅高风险/破坏性命令会命中这档）
//     - PolicyAllow / 默认 / 命令解析失败（safe-deny）→ 对应分支
//  3. strict 模式（PermissionMode=="strict"）兜底：跳过 default-allow，全部走 HITL
//
// 设计 invariant：常规工具和安全命令默认放行；真正高风险命令才进入审批。
func (m *Master) createPermissionPromptFn() func(context.Context, skills.PermissionRequest) (skills.PermissionResponse, error) {
	return func(ctx context.Context, req skills.PermissionRequest) (skills.PermissionResponse, error) {
		// 从 context 读取 sessionID（由 executeTool / AgentLoop 通过 toolctx.WithSessionID 注入）
		sessionID := toolctx.GetSessionID(ctx)
		strictMode := m.config.SecurityPermissionMode == "strict"

		// 非 shell 家族工具：Input 是结构化 JSON，按工具自己的 action/operation 做细分。
		// minimal 模式只拦外部发送、删除、工具变更等危险副作用；普通文件编辑/计划/读取直接放行。
		if !m.isShellFamilyTool(req.ToolName) {
			if !strictMode {
				if router.StructuredDangerousOperation(req.ToolName, req.Input) {
					m.emitQualityEvent("", "", sessionID, agentquality.Event{
						Name:        agentquality.EventPermissionDecision,
						Route:       routeFromSessionID(sessionID),
						FailureType: agentquality.FailurePermission,
						FinalStatus: agentquality.StatusNeedsUser,
						Attributes: map[string]any{
							"tool_name": req.ToolName,
							"policy":    "structured_ask",
						},
					})
					m.enqueueMetric(observability.Metric{
						Name:  "security.structured_dangerous_operation_ask_total",
						Value: 1,
						Labels: map[string]any{
							"tool_name": req.ToolName,
							"route":     routeFromSessionID(sessionID),
						},
					})
					return m.requestHITLPermission(ctx, req, sessionID)
				}
				return skills.PermissionResponse{Granted: true}, nil
			}
			// strict 模式：非 shell 工具也走审批（一键回滚路径）
			return m.requestHITLPermission(ctx, req, sessionID)
		}

		// shell 家族：先提取 command，再走 MatchPolicy
		cmd, ok := extractShellCommand(req.Input)
		if !ok {
			// 命令解析失败 → safe-deny + warn + metric（避免把未知 payload 当成安全命令放行）
			m.logger.Warn("shell 家族工具 Input 解析失败，按安全拒绝",
				zap.String("session_id", sessionID),
				zap.String("tool", req.ToolName),
			)
			m.enqueueMetric(observability.Metric{
				Name:  "security.shell_input_malformed_total",
				Value: 1,
				Labels: map[string]any{
					"tool_name": req.ToolName,
					"reason":    "malformed_input",
				},
			})
			return skills.PermissionResponse{Granted: false}, nil
		}

		// SafeExecutor.MatchPolicy 必须早于 im- 前缀检查（设计 invariant）
		executor := m.safeExecutor.Load()
		if executor == nil {
			// Master 未完成安全初始化（理论不可达）—— 失效则 fail-closed，防止 rm -rf / 从这个窗口走
			m.logger.Error("safeExecutor 未初始化，shell 命令一律拒绝",
				zap.String("session_id", sessionID),
				zap.String("tool", req.ToolName),
				zap.String("command", cmd),
			)
			m.enqueueMetric(observability.Metric{
				Name:  "security.safe_executor_missing_total",
				Value: 1,
				Labels: map[string]any{
					"tool_name": req.ToolName,
					"reason":    "safe_executor_missing",
				},
			})
			return skills.PermissionResponse{Granted: false}, nil
		}
		policy, pattern := executor.MatchPolicyWithRule(cmd)

		switch policy {
		case security.PolicyDeny:
			m.logger.Warn("命令被安全策略拒绝",
				zap.String("session_id", sessionID),
				zap.String("tool", req.ToolName),
				zap.String("command", cmd),
				zap.String("pattern", pattern),
			)
			m.emitQualityEvent("", "", sessionID, agentquality.Event{
				Name:        agentquality.EventPermissionDecision,
				Route:       routeFromSessionID(sessionID),
				FailureType: agentquality.FailurePermission,
				FinalStatus: agentquality.StatusBlocked,
				Attributes: map[string]any{
					"tool_name": req.ToolName,
					"policy":    "deny",
					"pattern":   pattern,
				},
			})
			m.enqueueMetric(observability.Metric{
				Name:  "security.policy_deny_total",
				Value: 1,
				Labels: map[string]any{
					"tool_name": req.ToolName,
					"policy":    "deny",
					"route":     routeFromSessionID(sessionID),
				},
			})
			return skills.PermissionResponse{Granted: false}, nil

		case security.PolicyAsk:
			// PolicyAsk 一律走 HITL 审批。飞书 IM 已具备卡片审批回路，
			// 再保留 IM auto-allow 会导致"根本收不到审批卡片"。
			m.emitQualityEvent("", "", sessionID, agentquality.Event{
				Name:        agentquality.EventPermissionDecision,
				Route:       routeFromSessionID(sessionID),
				FailureType: agentquality.FailurePermission,
				FinalStatus: agentquality.StatusNeedsUser,
				Attributes: map[string]any{
					"tool_name": req.ToolName,
					"policy":    "ask",
					"pattern":   pattern,
				},
			})
			m.enqueueMetric(observability.Metric{
				Name:   "security.dangerous_operation_ask_total",
				Value:  1,
				Labels: map[string]any{"tool_name": req.ToolName, "policy": "ask", "route": routeFromSessionID(sessionID)},
			})
			return m.requestHITLPermission(ctx, req, sessionID)

		default:
			// PolicyAllow / 未匹配（minimal 模式默认放行）
			if strictMode {
				return m.requestHITLPermission(ctx, req, sessionID)
			}
			return skills.PermissionResponse{Granted: true}, nil
		}
	}
}

// requestHITLPermission 走 legacy HITL 审批流程。
// 从旧 createPermissionPromptFn 抽出，保持行为完全一致，仅供两种场景调用：
//  1. PolicyAsk + 非 IM 会话
//  2. PermissionMode=="strict" 模式（一键回滚路径）
func (m *Master) requestHITLPermission(ctx context.Context, req skills.PermissionRequest, sessionID string) (skills.PermissionResponse, error) {
	inputReq := &InputRequest{
		ID:          m.hitlBroker.NextInputID("perm"),
		TaskID:      sessionID,
		SessionID:   sessionID,
		Type:        InputPermission,
		Prompt:      req.Description,
		Options:     []string{"approve", "deny"},
		ToolName:    req.ToolName,
		Data:        req.Input,
		Timeout:     60 * time.Minute,
		Fingerprint: permFingerprint(req.ToolName, req.Input),
		CreatedAt:   time.Now(),
	}

	// 使用 per-request channel，避免通道饥饿
	respCh := make(chan InputResponse, 1)
	isNew := m.hitlBroker.RegisterPendingInput(inputReq, respCh)

	// 仅首次注册时广播，去重请求不重复广播审批卡片
	if isNew {
		m.eventBus.BroadcastInputRequest(inputReq)
	}

	m.logger.Info("请求权限",
		zap.String("request_id", inputReq.ID),
		zap.String("tool", req.ToolName))

	timeout := time.NewTimer(60 * time.Minute)
	defer timeout.Stop()

	// 确保退出时清理
	defer m.hitlBroker.UnregisterPendingInput(inputReq.ID)

	select {
	case resp := <-respCh:
		granted := resp.Action == "approve"
		// 如果用户批准且提供了参数覆盖（如更改了 theme_id），merge 后存储
		// executeTool 会在执行前通过 LoadAndDelete 读取并使用
		if granted && resp.Value != "" {
			merged := mergeToolArgs(req.Input, []byte(resp.Value))
			overrideKey := sessionID + ":" + req.ToolName
			m.toolArgOverrides.Store(overrideKey, merged)
		}
		return skills.PermissionResponse{
			Granted:  granted,
			Remember: resp.Remember,
		}, nil

	case <-timeout.C:
		return skills.PermissionResponse{Granted: false}, errs.New(errs.CodeInputTimeout, "permission request timed out")

	case <-ctx.Done():
		return skills.PermissionResponse{Granted: false}, ctx.Err()

	case <-m.stopCh:
		return skills.PermissionResponse{Granted: false}, errs.New(errs.CodeCanceled, "master stopped")
	}
}

// mergeToolArgs 将 override（JSON object）的键值合并到 base（JSON object）中
// override 的键优先，base 中不存在的键保留
func mergeToolArgs(base, override json.RawMessage) json.RawMessage {
	var baseMap, overrideMap map[string]json.RawMessage
	if err := json.Unmarshal(base, &baseMap); err != nil {
		return override
	}
	if err := json.Unmarshal(override, &overrideMap); err != nil {
		return base
	}
	for k, v := range overrideMap {
		baseMap[k] = v
	}
	merged, err := json.Marshal(baseMap)
	if err != nil {
		return base
	}
	return merged
}

// GetPermissionManager 返回权限管理器
func (m *Master) GetPermissionManager() *skills.PermissionManager {
	return m.permMgr
}

// UpdatePermissionRules 热更新权限规则，同步到运行时 PermissionManager
func (m *Master) UpdatePermissionRules(rules []skills.PermissionRule) {
	if m.permMgr != nil {
		m.permMgr.SetRules(rules)
	}
}
