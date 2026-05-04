package master

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/chef-guo/agents-hive/internal/auth"
	"github.com/chef-guo/agents-hive/internal/llm"
	"github.com/chef-guo/agents-hive/internal/observability"
	"github.com/chef-guo/agents-hive/internal/plugin"
	"github.com/chef-guo/agents-hive/internal/sessiontodo"
	"github.com/chef-guo/agents-hive/internal/store"
	"github.com/chef-guo/agents-hive/internal/subagent"
)

// SessionLoop 运行持续的会话循环直到退出命令
func (m *Master) SessionLoop(ctx context.Context) error {
	var session *SessionState
	if m.store != nil {
		if err := m.sessionMgr.LoadLastActiveSession(ctx, m.store); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				m.logger.Debug("未找到历史会话，将创建新会话")
			} else if errors.Is(err, store.ErrCorrupted) {
				m.logger.Error("会话数据损坏，将创建新会话", zap.Error(err))
				m.eventBus.BroadcastGenericMessage("error", map[string]any{
					"message": "会话数据损坏，已创建新会话",
					"error":   err.Error(),
				})
			} else {
				m.logger.Warn("加载会话失败，创建新会话", zap.Error(err))
			}
		} else {
			activeID := m.sessionMgr.GetActiveSessionID()
			if activeID != "" {
				session = m.sessionMgr.GetSession(activeID)
			}
		}
	}

	if session == nil {
		session = &SessionState{
			ID:           uuid.New().String(),
			Name:         "main",
			Messages:     []llm.MessageWithTools{},
			Metadata:     make(map[string]any),
			Tags:         []string{},
			Created:      time.Now(),
			LastAccessed: time.Now(),
			Stats:        SessionStats{},
		}

		m.sessionMgr.SetSession(session)
		m.sessionMgr.SetActiveSessionID(session.ID)

		// auth 启用时不持久化无主会话，避免所有用户的会话列表都看到它。
		// 用户通过 API 创建会话时会携带 user_id，届时再持久化。
		if m.store != nil && !auth.IsAuthEnabled(ctx) {
			if err := m.sessionMgr.SaveSession(ctx, m.store, session); err != nil {
				m.logger.Error("保存新会话失败", zap.String("session_id", session.ID), zap.Error(err))
			}
		}

		m.logger.Info("启动会话循环",
			zap.String("session_id", session.ID),
			zap.String("session_name", session.Name))

		// 记录会话开始
		if m.journal != nil {
			if err := m.journal.StartSession(ctx, session.ID, session.Name); err != nil {
				m.logger.Warn("Journal StartSession 失败", zap.Error(err))
			}
		}
	} else {
		m.logger.Info("启动会话循环（已恢复）",
			zap.String("session_id", session.ID),
			zap.String("session_name", session.Name),
			zap.Int("messages", len(session.Messages)))
		// 恢复的会话也记录 journal（幂等，不会覆盖 started_at）
		if m.journal != nil {
			if err := m.journal.StartSession(ctx, session.ID, session.Name); err != nil {
				m.logger.Warn("Journal StartSession 失败", zap.Error(err))
			}
		}
	}

	// 启动异步 journal 写入 worker（#8）
	// 仅通过 workerCancel 触发退出，不再调用 StopJournalWorker（避免 double-close）
	workerCtx, workerCancel := context.WithCancel(context.Background())
	m.StartJournalWorker(workerCtx)
	m.StartObsWorker(workerCtx)
	defer func() {
		workerCancel()
		// 等待 journal worker 退出（drain 完成）
		if m.journalDone != nil {
			<-m.journalDone
		}
		// 等待 observability worker 退出（drain 完成，确保 DB 关闭前写完）
		if m.obsDone != nil {
			<-m.obsDone
		}
	}()

	if m.store != nil {
		go m.startBackgroundSync(ctx)
	}

	// Worker Pool
	workerCount := m.config.RuntimePolicy.GlobalWorkers
	if workerCount <= 0 {
		workerCount = m.config.MaxConcurrentTasks
	}
	if workerCount <= 0 {
		workerCount = 50
	}
	taskCh := make(chan sessionTask, workerCount)
	var taskWg sync.WaitGroup

	for i := 0; i < workerCount; i++ {
		taskWg.Add(1)
		go m.taskWorker(ctx, taskCh, &taskWg)
	}
	m.logger.Info("Worker Pool 已启动", zap.Int("workers", workerCount))

	// Dispatcher 主循环
	var loopErr error
loop:
	for {
		select {
		case <-ctx.Done():
			m.logger.Info("上下文已取消，退出 Dispatcher")
			loopErr = ctx.Err()
			break loop

		case <-m.stopCh:
			m.logger.Info("收到停止信号，退出 Dispatcher")
			break loop

		case req := <-m.sessionMgr.requestCh:
			responseID := req.ResponseID

			// --- Command：同步处理（轻量）---
			if req.Command != SessionCommandNone {
				// revert 等命令需要 session 在内存中，进程重启后 session 只在 DB 里
				// 先用 GetOrCreateSession 确保 session 存在，新建时从 DB 回源历史消息
				if req.SessionID != "" {
					m.sessionMgr.sessionMu.RLock()
					_, exists := m.sessionMgr.sessions[req.SessionID]
					m.sessionMgr.sessionMu.RUnlock()
					if !exists {
						sess, isNew := m.sessionMgr.GetOrCreateSession(req.SessionID)
						if isNew && m.store != nil {
							m.restoreSessionFromStore(ctx, sess)
						}
					}
				}
				if err := m.sessionMgr.HandleSessionCommand(req, m.store); err != nil {
					resp := TaskResponse{
						Error:     err.Error(),
						Completed: true,
					}
					m.sessionMgr.SendResponse(responseID, resp)
				}
				continue
			}

			// --- Task：前置处理 + 投递到 worker ---
			targetID := req.SessionID
			if targetID == "" {
				targetID = m.sessionMgr.GetActiveSessionID()
			}

			targetSession, isNewSession := m.sessionMgr.GetOrCreateSession(targetID)

			// P2 修复：GetOrCreateSession 不接受 ctx，新建 session 后补设 UserID
			if isNewSession && req.Ctx != nil {
				userID := auth.UserIDFrom(req.Ctx)
				if userID != "" {
					targetSession.mu.Lock()
					targetSession.UserID = userID
					targetSession.mu.Unlock()
				}
			}

			// 触发 SessionStart hook（仅新建会话时）
			if isNewSession && m.pluginMgr != nil {
				hookCtx, cancel := context.WithTimeout(ctx, plugin.HookCallTimeout)
				_ = m.pluginMgr.TriggerSessionStart(hookCtx, &plugin.SessionStartInput{
					SessionID: targetSession.ID,
				})
				cancel()
			}

			// 确保目标会话在 journal 中有父行
			m.ensureJournalSession(ctx, targetSession)

			handled, exit := m.handleLegacyCommand(req.Input, targetSession, responseID)
			if exit {
				m.logger.Info("收到退出命令，结束会话")
				break loop
			}
			if handled {
				continue
			}

			// per-session 串行化：同一 session 同时只允许一个任务执行
			// 在 goroutine 中等待信号量，避免阻塞 Dispatcher 处理其他 session 的请求
			go func(req SessionRequest, targetSession *SessionState, responseID uint64, isNew bool) {
				sem := m.getSessionSem(targetSession.ID)
				select {
				case sem <- struct{}{}:
					// 获取信号量成功
				case <-ctx.Done():
					return
				}

				// 关键修复：新建的 session 可能在 DB 中已有历史消息（进程重启/多会话切换后内存丢失）
				// 放在 goroutine 中执行，避免阻塞 Dispatcher 处理其他 session 的请求
				// 必须在 maybeSetSessionTitle 之前完成，否则会话名会被误覆盖
				if isNew && m.store != nil {
					m.restoreSessionFromStore(ctx, targetSession)
				}

				// 设置临时字段（附件、推理努力级别、模型覆盖和 IM 上下文）到 Session
				targetSession.SetPendingData(req.Attachments, req.ReasoningEffort, req.ModelOverride, req.IMContext)
				m.maybeSetSessionTitle(ctx, targetSession, req.Input)

				// 投递到 worker pool
				// 成功投递：worker 负责释放信号量
				// 未投递（ctx/stopCh）：此处释放信号量
				select {
				case taskCh <- sessionTask{req: req, session: targetSession, responseID: responseID, semToken: sem}:
					// 所有权转移给 worker，不释放
				case <-ctx.Done():
					<-sem // 释放信号量
				case <-m.stopCh:
					<-sem // 释放信号量
				}
			}(req, targetSession, responseID, isNewSession)
		}
	}

	// === 优雅关闭 ===
	close(taskCh) // 通知所有 worker 退出
	taskWg.Wait() // 等待所有 worker 完成当前任务

	if m.store != nil {
		saveCtx, saveCancel := context.WithTimeout(context.Background(), 10*time.Second)
		if err := m.sessionMgr.SaveAllSessions(saveCtx, m.store); err != nil {
			m.logger.Error("退出时保存会话失败", zap.Error(err))
		}
		saveCancel()
	}
	// 结束所有活跃会话的 journal
	m.endAllSessionJournals()
	return loopErr
}

// sessionTask 是投递给 worker 的任务单元
type sessionTask struct {
	req        SessionRequest
	session    *SessionState
	responseID uint64
	// semToken 是 worker 释放信号量的凭证（goroutine 从 semaphore 通道接收的值）
	// worker 从 channel 接收时获得，defer 中用此值释放
	semToken chan struct{}
}

// taskWorker 是 Worker Pool 中的单个 worker，从 taskCh 取任务执行
func (m *Master) taskWorker(ctx context.Context, taskCh <-chan sessionTask, wg *sync.WaitGroup) {
	defer wg.Done()
	for task := range taskCh {
		m.executeSessionTask(ctx, task)
	}
}

// executeSessionTask 执行单个 session 任务
func (m *Master) executeSessionTask(ctx context.Context, task sessionTask) {
	session := task.session
	responseID := task.responseID

	// 任务完成后释放 per-session 信号量，允许同一 session 的下一个任务执行
	defer func() { <-task.semToken }()

	taskCtx, taskCancel := context.WithCancel(ctx)
	// C3 fix: 将请求 context 中的 user 信息和 auth 状态注入 taskCtx
	// 否则 react_processor 的 CheckQuota/RecordUsage 拿不到 userID
	if task.req.Ctx != nil {
		if u := auth.UserFrom(task.req.Ctx); u != nil {
			taskCtx = auth.WithUser(taskCtx, u)
		}
		if auth.IsAuthEnabled(task.req.Ctx) {
			taskCtx = auth.WithAuthEnabled(taskCtx)
		}
	}
	m.setTaskCancel(session.ID, taskCancel)

	m.logger.Info("开始处理任务",
		zap.String("session_id", session.ID),
		zap.Int("content_len", len(task.req.Input)),
	)
	taskStart := time.Now()
	sessionTraceID := task.req.TurnID
	if sessionTraceID == "" {
		sessionTraceID = observability.NewTraceID()
	}
	sessionSpanID := observability.NewSpanID()
	taskRoute := routeFromSession(session)
	m.enqueueMetric(observability.Metric{
		Name:   "hive.task.started",
		Value:  1,
		Labels: map[string]any{"route": taskRoute},
	})
	m.enqueueMetric(observability.Metric{
		Name:   "hive.sessions.active",
		Value:  1,
		Labels: map[string]any{"session_id": session.ID},
	})

	taskErr := m.processTask(taskCtx, task.req.Input, session, responseID, sessionTraceID, sessionSpanID, task.req.SkipUserMessage)
	taskCancel()
	taskDurationMs := time.Since(taskStart).Milliseconds()
	m.clearTaskCancel(session.ID)

	m.logger.Info("任务处理完成",
		zap.String("session_id", session.ID),
		zap.Duration("duration", time.Duration(taskDurationMs)*time.Millisecond),
		zap.Bool("error", taskErr != nil),
	)
	spanStatus := "ok"
	if taskErr != nil {
		spanStatus = "error"
	}
	m.enqueueMetric(observability.Metric{
		Name:   "hive.task.finished",
		Value:  1,
		Labels: map[string]any{"route": taskRoute, "status": spanStatus},
	})
	m.enqueueSpan(observability.Span{
		TraceID:    sessionTraceID,
		SpanID:     sessionSpanID,
		Operation:  "session.process",
		Service:    "master",
		SessionID:  session.ID,
		UserID:     auth.UserIDFrom(taskCtx),
		DurationMs: int(taskDurationMs),
		Status:     spanStatus,
		Attributes: map[string]any{"input_len": len(task.req.Input)},
		Ts:         taskStart,
	})
	m.enqueueMetric(observability.Metric{
		Name:   "hive.sessions.total",
		Value:  1,
		Labels: map[string]any{"session_id": session.ID, "status": spanStatus},
	})

	if taskErr != nil {
		m.logger.Error("任务处理失败", zap.Error(taskErr))
		if !session.IsTerminated() {
			m.sessionMgr.SendResponse(responseID, TaskResponse{
				Error:     taskErr.Error(),
				Completed: true,
			})
		}
	}

	// 清理临时字段
	session.ClearPendingData()

	if m.store != nil && !session.IsTerminated() {
		if err := m.sessionMgr.SaveSession(context.Background(), m.store, session); err != nil {
			m.logger.Error("任务完成后保存会话失败",
				zap.String("session_id", session.ID), zap.Error(err))
		}
	}

	// 首次对话完成后异步生成会话标题
	if !session.IsTerminated() {
		m.maybeGenerateTitle(ctx, session)
	}
}

// maybeSetSessionTitle 首条消息自动设置会话标题（从 SessionLoop 提取）
func (m *Master) maybeSetSessionTitle(ctx context.Context, session *SessionState, input string) {
	session.mu.RLock()
	sessName := session.Name
	msgCount := len(session.Messages)
	sessID := session.ID
	session.mu.RUnlock()

	if isDefaultSessionName(sessName) && msgCount == 0 {
		title := []rune(input)
		if len(title) > 30 {
			title = append(title[:30], []rune("...")...)
		}
		newTitle := string(title)
		session.mu.Lock()
		session.Name = newTitle
		session.mu.Unlock()
		if m.store != nil {
			_ = m.sessionMgr.SaveSession(ctx, m.store, session)
		}
		m.eventBus.BroadcastGenericMessage("session_title", map[string]any{
			"session_id": sessID,
			"title":      newTitle,
		})
		m.updateJournalTask(sessID, newTitle)
	}
}

// endAllSessionJournals 结束所有内存中会话的 journal（#2 修复）
func (m *Master) endAllSessionJournals() {
	if m.journal == nil {
		return
	}
	m.sessionMgr.sessionMu.RLock()
	ids := make([]string, 0, len(m.sessionMgr.sessions))
	for id := range m.sessionMgr.sessions {
		ids = append(ids, id)
	}
	m.sessionMgr.sessionMu.RUnlock()

	endCtx, endCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer endCancel()
	for _, id := range ids {
		if err := m.journal.EndSession(endCtx, id, ""); err != nil {
			m.logger.Warn("Journal EndSession 失败", zap.String("session_id", id), zap.Error(err))
		}
	}
}

// ensureJournalSession 确保目标会话在 journal 中有父行（#1 修复）
func (m *Master) ensureJournalSession(ctx context.Context, session *SessionState) {
	if m.journal == nil {
		return
	}
	session.mu.RLock()
	sessID := session.ID
	sessName := session.Name
	session.mu.RUnlock()

	jCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := m.journal.StartSession(jCtx, sessID, sessName); err != nil {
		m.logger.Warn("Journal ensureJournalSession 失败",
			zap.String("session_id", sessID), zap.Error(err))
	}
}

// restoreSessionFromStore 从持久化存储回源加载会话元数据和历史消息
// 当 GetOrCreateSession 新建了空 session（内存中不存在），但 DB 中可能有历史数据时调用
func (m *Master) restoreSessionFromStore(ctx context.Context, session *SessionState) {
	loadCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	record, err := m.store.LoadSession(loadCtx, session.ID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			// DB 中确实没有这个 session，全新会话，无需回源
			m.logger.Debug("会话在 DB 中不存在，跳过回源",
				zap.String("session_id", session.ID))
			return
		}
		// DB 查询失败（超时/连接断开等），会话可能存在但无法加载
		// 标记 Name 为非默认值，防止 maybeSetSessionTitle 误覆盖已有会话名
		m.logger.Warn("回源加载会话元数据失败，标记为已存在会话防止误命名",
			zap.String("session_id", session.ID), zap.Error(err))
		session.mu.Lock()
		name := session.ID
		if len(name) > 8 {
			name = name[:8]
		}
		session.Name = name // 非默认值即可，防止 maybeSetSessionTitle 误覆盖
		session.mu.Unlock()
		return
	}

	// 回填会话元数据
	session.mu.Lock()
	if record.Name != "" {
		session.Name = record.Name
	}
	if record.UserID != "" {
		session.UserID = record.UserID
	}
	session.Tags = record.Tags
	session.Stats.MessageCount = record.MessageCount
	session.Stats.TotalTokens = record.TotalTokens
	session.mu.Unlock()

	// 加载历史消息
	messages, err := m.store.GetMessages(loadCtx, session.ID, 0)
	if err != nil {
		m.logger.Warn("回源加载消息失败",
			zap.String("session_id", session.ID), zap.Error(err))
		return
	}

	if len(messages) == 0 {
		return
	}

	var restored []llm.MessageWithTools
	for _, msg := range messages {
		mwt := llm.MessageWithTools{
			Role:      msg.Role,
			Content:   llm.NewTextContent(msg.Content),
			CreatedAt: msg.CreatedAt.Format(time.RFC3339),
		}
		if msg.Metadata != nil {
			var meta map[string]any
			if err := json.Unmarshal(msg.Metadata, &meta); err == nil {
				if tcStr, ok := meta["tool_calls"].(string); ok {
					var tcs []llm.ToolCall
					if err := json.Unmarshal([]byte(tcStr), &tcs); err == nil {
						mwt.ToolCalls = tcs
					}
				}
				if tcID, ok := meta["tool_call_id"].(string); ok {
					mwt.ToolCallID = tcID
				}
				if rc, ok := meta["reasoning_content"].(string); ok {
					mwt.ReasoningContent = rc
				}
				if ie, ok := meta["is_error"].(bool); ok {
					mwt.IsError = ie
				}
				if tn, ok := meta["tool_name"].(string); ok {
					mwt.ToolName = tn
				}
				if cpStr, ok := meta["content_parts"].(string); ok {
					var parts []llm.ContentPart
					if err := json.Unmarshal([]byte(cpStr), &parts); err == nil {
						mwt.Content = llm.NewMultiContent(parts...)
					}
				}
			}
		}
		restored = append(restored, mwt)
	}

	session.mu.Lock()
	session.Messages = restored
	session.lastSavedIndex = len(restored) // 标记为已保存，防止重复写入
	session.mu.Unlock()

	m.logger.Info("会话已从 DB 回源恢复",
		zap.String("session_id", session.ID),
		zap.String("session_name", record.Name),
		zap.Int("message_count", len(restored)))
}

// updateJournalTask 更新 journal 中的 task 字段（#11 修复）
func (m *Master) updateJournalTask(sessionID, task string) {
	if m.journal == nil {
		return
	}
	jCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := m.journal.StartSession(jCtx, sessionID, task); err != nil {
		m.logger.Warn("Journal updateJournalTask 失败",
			zap.String("session_id", sessionID), zap.Error(err))
	}
}

// maybeGenerateTitle 在首次对话完成后异步生成会话标题
// 触发条件：会话名称仍为默认值 "main" 且至少有 2 条消息（一问一答）
func (m *Master) maybeGenerateTitle(ctx context.Context, session *SessionState) {
	session.mu.RLock()
	name := session.Name
	msgCount := len(session.Messages)
	session.mu.RUnlock()

	// 仅在首次对话且名称为默认值时触发
	if !isDefaultSessionName(name) || msgCount < 2 {
		return
	}

	titleAgent, err := m.registry.Get("title-agent")
	if err != nil {
		return // title agent 未注册（无 LLM 时不注册）
	}

	// 取前几条消息用于生成标题
	session.mu.RLock()
	msgs := make([]llm.MessageWithTools, 0, 5)
	n := msgCount
	if n > 5 {
		n = 5
	}
	msgs = append(msgs, session.Messages[:n]...)
	sessionID := session.ID
	userID := session.UserID
	session.mu.RUnlock()

	// 异步执行，不阻塞主循环
	go func() {
		// 使用可取消的 context，同时监听 stopCh 以响应服务关闭
		titleCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		// 监听 stopCh，关闭时立即取消 context
		go func() {
			select {
			case <-m.stopCh:
				cancel()
			case <-titleCtx.Done():
			}
		}()

		payload, err := json.Marshal(map[string]any{
			"messages": msgs,
		})
		if err != nil {
			m.logger.Warn("序列化标题请求失败", zap.Error(err))
			return
		}

		resp, err := titleAgent.SendTask(titleCtx, subagent.TaskRequest{
			ID:        "title-" + sessionID,
			Type:      "title",
			SessionID: sessionID,
			UserID:    userID,
			Payload:   payload,
		})
		if err != nil || resp.Status != "completed" {
			m.logger.Debug("自动生成标题失败", zap.Error(err))
			return
		}

		var result struct {
			Title string `json:"title"`
		}
		if err := json.Unmarshal(resp.Result, &result); err != nil || result.Title == "" {
			return
		}

		// 更新会话名称
		session.mu.Lock()
		session.Name = result.Title
		session.mu.Unlock()

		// 持久化
		if m.store != nil {
			if err := m.sessionMgr.SaveSession(titleCtx, m.store, session); err != nil {
				m.logger.Warn("保存会话标题失败", zap.String("session_id", sessionID), zap.Error(err))
			}
		}

		// 同步更新 journal task 字段（#11）
		m.updateJournalTask(sessionID, result.Title)

		// 广播标题变更事件给前端
		m.eventBus.BroadcastGenericMessage("session_title", map[string]any{
			"session_id": sessionID,
			"title":      result.Title,
		})

		m.logger.Info("会话标题已自动生成",
			zap.String("session_id", sessionID),
			zap.String("title", result.Title))
	}()
}

// handleLegacyCommand 处理旧的简单文本命令（exit/clear/reset）
func (m *Master) handleLegacyCommand(request string, session *SessionState, responseID uint64) (bool, bool) {
	cmd := strings.TrimSpace(strings.ToLower(request))

	switch cmd {
	case "exit", "quit", "退出", "再见":
		m.sessionMgr.SendResponse(responseID, TaskResponse{Exit: true})
		return true, true

	case "clear", "清空", "清空历史":
		session.mu.Lock()
		session.Messages = []llm.MessageWithTools{}
		session.lastSavedIndex = 0
		session.mu.Unlock()

		// 同步清空数据库中的消息记录
		clearMsg := "对话历史已清空"
		if m.store != nil {
			revertCtx, revertCancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer revertCancel()
			if err := m.store.RevertSession(revertCtx, session.ID, 0); err != nil {
				m.logger.Error("清空数据库消息失败", zap.String("session_id", session.ID), zap.Error(err))
				clearMsg = fmt.Sprintf("内存已清空，但数据库清空失败: %v", err)
			}
		}

		m.sessionMgr.SendResponse(responseID, TaskResponse{
			Message:   clearMsg,
			Completed: true,
		})
		return true, false

	case "reset", "重置", "重新开始":
		session.mu.Lock()
		session.Messages = []llm.MessageWithTools{}
		session.lastSavedIndex = 0
		session.mu.Unlock()

		// 同步清空数据库中的消息记录
		resetMsg := "会话已重置"
		if m.store != nil {
			revertCtx, revertCancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer revertCancel()
			if err := m.store.RevertSession(revertCtx, session.ID, 0); err != nil {
				m.logger.Error("重置时清空数据库消息失败", zap.String("session_id", session.ID), zap.Error(err))
				resetMsg = fmt.Sprintf("内存已重置，但数据库清空失败: %v", err)
			}
		}

		m.sessionMgr.SendResponse(responseID, TaskResponse{
			Message:   resetMsg,
			Completed: true,
		})
		return true, false

	default:
		return false, false
	}
}

// processTask 处理单个任务（Master ReAct 直接执行）
func (m *Master) processTask(ctx context.Context, request string, session *SessionState, responseID uint64, sessionTraceID, sessionSpanID string, skipUserMsg bool) (err error) {
	m.logger.Debug("处理任务", zap.String("request", request))

	m.eventBus.BroadcastSessionMessage(session.ID, BroadcastMessage{
		Type: EventTypeAgentStatus,
		Payload: map[string]interface{}{
			"status":     "thinking",
			"session_id": session.ID,
		},
	})

	// 兜底：无论 processTaskDirectExec 内部是否已广播 completed/error，
	// defer 确保任务结束时前端一定能收到终态事件，防止活动栏卡在"思考中"。
	// runReActLoop 正常完成时已广播 completed，此处 defer 会再发一次——
	// 前端 onAgentStatus 对 completed 是幂等的（重复收到不会出错）。
	completedBroadcasted := false
	defer func() {
		if r := recover(); r != nil {
			m.logger.Error("processTask panic recovered",
				zap.String("session_id", session.ID),
				zap.Any("panic", r),
				zap.String("stack", string(debug.Stack())),
			)
			m.eventBus.BroadcastSessionMessage(session.ID, BroadcastMessage{
				Type: EventTypeAgentStatus,
				Payload: map[string]interface{}{
					"status":     "error",
					"session_id": session.ID,
					"error":      fmt.Sprintf("internal panic: %v", r),
				},
			})
			completedBroadcasted = true
			err = fmt.Errorf("panic in processTask: %v", r)
		}
		if !completedBroadcasted {
			session.mu.RLock()
			planStatus := session.PlanStatus
			session.mu.RUnlock()
			status := "completed"
			payload := map[string]interface{}{
				"status":     status,
				"session_id": session.ID,
			}
			if planStatus == sessiontodo.PlanStatusPaused {
				status = string(TaskStatusPaused)
				payload["status"] = status
				payload["message"] = "Send a message to continue"
			}
			m.eventBus.BroadcastSessionMessage(session.ID, BroadcastMessage{
				Type:    EventTypeAgentStatus,
				Payload: payload,
			})
		}
	}()

	// Spec-driven Phase 2 intake hook（TG10 10.1–10.4，openspec/changes/harden-spec-driven-phase2）。
	// 设计契约：
	//   - mode=legacy（默认）完全 short-circuit，零 LLM / DB 开销，legacy 行为不变。
	//   - mode=dual/spec 当前 runner 为 Sprint 3.3.a MinimalRunner → 返回
	//     planner.ErrPlannerSchemaInvalid → intake.DowngradeOnError downshift 到 legacy，
	//     metric 记为 `legacy_downshift_planner_schema`；Sprint 3.3.b 接 LLM 后自然上线。
	//   - specCtx 仅在此处 Store/Clear——react_processor 读侧用 atomic.Pointer 零锁读。
	//
	// Round 5 G2 修复：之前 `_ = m.applySpecDrivenIntake(...)` 把 intake.Path 扔掉
	// 是字面"路由结果丢了"，导致 dual/spec mode 下 operators 无法验证路由真生效。
	// 现在捕获 path + emit `specdriven.execution_path_total{path}` counter 让 Stage 2
	// promotion 准入有直接证据。
	//
	// 注意（Phase 2 范围边界，写入 design.md）：今天 path=spec/dual 与 legacy 在
	// processTaskDirectExec 行为上**没有差异**——spec runner 产出的 specCtx 已挂到
	// session（react_processor 入口可读），但 ReAct 主循环并未消费它生成 ToolCall。
	// "spec 为 primary 执行" 是 Phase 3 的工程范围（替换 ReAct 为 plan-driven executor）。
	// Phase 2 在此 ingress 给出 path 并埋观测，确保 Phase 3 接入时 routing 已就绪。
	specPath := m.applySpecDrivenIntake(session, request)
	m.emitExecutionPath(specPath)

	err = m.processTaskDirectExec(ctx, request, session, responseID, sessionTraceID, sessionSpanID, skipUserMsg)
	if err != nil {
		completedBroadcasted = true // error 分支已广播终态，defer 不再重复
		m.eventBus.BroadcastSessionMessage(session.ID, BroadcastMessage{
			Type: EventTypeAgentStatus,
			Payload: map[string]interface{}{
				"status":     "error",
				"session_id": session.ID,
				"error":      err.Error(),
			},
		})
	}
	return err
}

// isDefaultSessionName 判断是否为系统默认会话名（需要自动用首条消息内容更新标题）
func isDefaultSessionName(name string) bool {
	if name == "main" || name == "" || name == "新会话" {
		return true
	}
	// 匹配前端生成的 "Session N" 格式
	if strings.HasPrefix(name, "Session ") {
		suffix := strings.TrimPrefix(name, "Session ")
		if len(suffix) == 0 {
			return false
		}
		for _, c := range suffix {
			if c < '0' || c > '9' {
				return false
			}
		}
		return true
	}
	return false
}
