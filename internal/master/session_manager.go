package master

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/chef-guo/agents-hive/internal/auth"
	"github.com/chef-guo/agents-hive/internal/errs"
	"github.com/chef-guo/agents-hive/internal/journal"
	"github.com/chef-guo/agents-hive/internal/llm"
	"github.com/chef-guo/agents-hive/internal/plugin"
	"github.com/chef-guo/agents-hive/internal/store"
)

// SessionManager 管理所有会话状态和会话级操作
type SessionManager struct {
	requestCh        chan SessionRequest
	responseCh       chan TaskResponse
	sessions         map[string]*SessionState
	activeSessionID  string
	sessionMu        sync.RWMutex
	pendingResponses map[uint64]chan TaskResponse
	responseCounter  uint64
	responseMu       sync.Mutex
	counter          uint64
	stopCh           chan struct{}
	logger           *zap.Logger
	journal          journal.Journal // 开发日志（可选，删除会话时级联清理）
	pluginMgr        *plugin.Manager // 插件管理器（可选，删除会话时触发 SessionEnd hook）
}

// NewSessionManager 创建新的会话管理器
func NewSessionManager(stopCh chan struct{}, logger *zap.Logger) *SessionManager {
	return &SessionManager{
		requestCh:        make(chan SessionRequest, 16),
		responseCh:       make(chan TaskResponse, 16),
		sessions:         make(map[string]*SessionState),
		pendingResponses: make(map[uint64]chan TaskResponse),
		stopCh:           stopCh,
		logger:           logger,
	}
}

// RequestCh 返回 SessionLoop 的请求通道
func (sm *SessionManager) RequestCh() chan<- SessionRequest {
	return sm.requestCh
}

// ResponseCh 返回 SessionLoop 的响应通道
func (sm *SessionManager) ResponseCh() <-chan TaskResponse {
	return sm.responseCh
}

// GetActiveSessionID 返回当前活跃会话 ID（线程安全）
func (sm *SessionManager) GetActiveSessionID() string {
	sm.sessionMu.RLock()
	defer sm.sessionMu.RUnlock()
	return sm.activeSessionID
}

// SetActiveSessionID 设置当前活跃会话 ID（线程安全）
func (sm *SessionManager) SetActiveSessionID(id string) {
	sm.sessionMu.Lock()
	defer sm.sessionMu.Unlock()
	sm.activeSessionID = id
}

// ListActiveSessions 返回所有活跃会话的 ID 列表（线程安全）
func (sm *SessionManager) ListActiveSessions() []string {
	sm.sessionMu.RLock()
	defer sm.sessionMu.RUnlock()
	ids := make([]string, 0, len(sm.sessions))
	for id := range sm.sessions {
		ids = append(ids, id)
	}
	return ids
}

// GetSession 获取指定 ID 的会话（线程安全）
func (sm *SessionManager) GetSession(sessionID string) *SessionState {
	sm.sessionMu.RLock()
	defer sm.sessionMu.RUnlock()
	return sm.sessions[sessionID]
}

// SetSession 注册会话到管理器（线程安全）
func (sm *SessionManager) SetSession(session *SessionState) {
	sm.sessionMu.Lock()
	defer sm.sessionMu.Unlock()
	sm.sessions[session.ID] = session
}

// GetOrCreateSession 获取或创建指定 ID 的会话
//
// 并发安全设计：
//   - 在 sessionMu 写锁内完成 map 读写，确保 check-then-act 的原子性
//   - logger.Info 调用移到锁外，避免 IO 操作在持锁期间阻塞其他并发访问
//
// 返回值 isNew 为 true 表示本次调用新建了会话，false 表示已存在。
func (sm *SessionManager) GetOrCreateSession(sessionID string) (*SessionState, bool) {
	sm.sessionMu.Lock()

	if session, ok := sm.sessions[sessionID]; ok {
		// 使用 session.mu 写锁保护字段更新，避免与并发读操作竞态
		session.mu.Lock()
		session.LastAccessed = time.Now()
		session.mu.Unlock()
		sm.sessionMu.Unlock()
		return session, false
	}

	sessionName := "新会话"

	session := &SessionState{
		ID:           sessionID,
		Name:         sessionName,
		Messages:     []llm.MessageWithTools{},
		Metadata:     make(map[string]any),
		Tags:         []string{},
		Created:      time.Now(),
		LastAccessed: time.Now(),
		Stats:        SessionStats{},
	}

	sm.sessions[sessionID] = session
	// 先解锁，再执行 IO 操作（logger 可能涉及文件/网络 IO）
	// 避免在持有 sessionMu 期间阻塞其他并发读写
	sm.sessionMu.Unlock()

	sm.logger.Info("创建新会话",
		zap.String("session_id", sessionID),
		zap.String("session_name", session.Name))

	return session, true
}

// SendResponse 发送响应到 per-request channel 或通用 responseCh
func (sm *SessionManager) SendResponse(responseID uint64, resp TaskResponse) {
	resp = NormalizeTaskResponse(resp)
	if responseID > 0 {
		sm.responseMu.Lock()
		ch, ok := sm.pendingResponses[responseID]
		sm.responseMu.Unlock()
		if ok {
			select {
			case ch <- resp:
			default:
				sm.logger.Warn("per-request 响应通道已满，丢弃响应",
					zap.Uint64("response_id", responseID))
			}
			return
		}
	}
	// 回退到通用 responseCh（CLI 等场景），非阻塞避免死锁
	select {
	case sm.responseCh <- resp:
	default:
		sm.logger.Warn("通用响应通道已满，丢弃响应")
	}
}

// ProcessRequestWithResponse 统一的 per-request 响应模式
func (sm *SessionManager) ProcessRequestWithResponse(ctx context.Context, req SessionRequest) (TaskResponse, error) {
	// 分配唯一请求 ID
	reqID := atomic.AddUint64(&sm.responseCounter, 1)
	respCh := make(chan TaskResponse, 1)

	// 注册 per-request response channel
	sm.responseMu.Lock()
	sm.pendingResponses[reqID] = respCh
	sm.responseMu.Unlock()

	defer func() {
		sm.responseMu.Lock()
		delete(sm.pendingResponses, reqID)
		sm.responseMu.Unlock()
	}()

	// 设置 ResponseID 和请求上下文
	req.ResponseID = reqID
	req.Ctx = ctx

	// 发送请求到 SessionLoop
	select {
	case sm.requestCh <- req:
	case <-ctx.Done():
		return TaskResponse{}, ctx.Err()
	case <-sm.stopCh:
		return TaskResponse{}, errs.New(errs.CodeCanceled, "master stopped")
	}

	// 等待响应
	select {
	case resp := <-respCh:
		return resp, nil
	case <-ctx.Done():
		return TaskResponse{}, ctx.Err()
	case <-sm.stopCh:
		return TaskResponse{}, errs.New(errs.CodeCanceled, "master stopped")
	}
}

// GetCurrentSessionInfo 获取当前活跃会话的信息
func (sm *SessionManager) GetCurrentSessionInfo() (sessionID, sessionName string) {
	sm.sessionMu.RLock()
	defer sm.sessionMu.RUnlock()

	if sm.activeSessionID == "" {
		return "", ""
	}

	session, ok := sm.sessions[sm.activeSessionID]
	if !ok {
		return sm.activeSessionID, "unknown"
	}

	return sm.activeSessionID, session.Name
}

// HandleSessionCommand 处理会话管理命令（new/switch/list/delete等）
func (sm *SessionManager) HandleSessionCommand(req SessionRequest, st ...store.SessionStore) error {
	var sessionStore store.SessionStore
	if len(st) > 0 {
		sessionStore = st[0]
	}
	switch req.Command {
	case SessionCommandNew:
		sessionID := uuid.New().String()
		sessionName := "新会话"
		if len(req.Args) > 0 {
			sessionName = req.Args[0]
		}

		session := &SessionState{
			ID:           sessionID,
			Name:         sessionName,
			Messages:     []llm.MessageWithTools{},
			Metadata:     make(map[string]any),
			Tags:         []string{},
			Created:      time.Now(),
			LastAccessed: time.Now(),
			Stats:        SessionStats{},
		}
		// C2 修复：auth 启用但无 user → 拒绝创建，防止静默写入空 user_id
		if req.Ctx != nil {
			if auth.IsAuthEnabled(req.Ctx) {
				userID := auth.UserIDFrom(req.Ctx)
				if userID == "" {
					return errs.New(errs.CodePermissionDenied, "未登录，无法创建会话")
				}
				session.UserID = userID
			}
		}

		sm.sessionMu.Lock()
		sm.sessions[sessionID] = session
		sm.activeSessionID = sessionID
		sm.sessionMu.Unlock()

		// 立即持久化新会话
		if sessionStore != nil {
			if err := sm.SaveSession(context.Background(), sessionStore, session); err != nil {
				sm.logger.Warn("持久化新会话失败", zap.String("session_id", sessionID), zap.Error(err))
			}
		}

		sm.SendResponse(req.ResponseID, TaskResponse{
			Message:   fmt.Sprintf("已创建会话: %s (%s)", sessionName, sessionID),
			Completed: true,
		})

		sm.logger.Info("创建新会话",
			zap.String("session_id", sessionID),
			zap.String("session_name", sessionName))

	case SessionCommandSwitch:
		if len(req.Args) == 0 {
			return errs.New(errs.CodeInvalidInput, "switch command requires session_id argument")
		}

		targetID := req.Args[0]
		var sessionName string
		// 在同一个 sessionMu 写锁内完成：验证存在、设置 activeSessionID、读取 Name
		// 避免解锁后再次读取 session 的 TOCTOU 竞态窗口
		if err := func() error {
			sm.sessionMu.Lock()
			defer sm.sessionMu.Unlock()
			session, ok := sm.sessions[targetID]
			if !ok {
				return errs.New(errs.CodeTaskNotFound, "session not found: "+targetID)
			}
			sm.activeSessionID = targetID
			// 使用 session.mu 写锁保护字段更新，避免与并发读操作竞态
			session.mu.Lock()
			session.LastAccessed = time.Now()
			// 在持有 session.mu 的期间同时读取 Name，省去锁外的第二次读取
			sessionName = session.Name
			session.mu.Unlock()
			return nil
		}(); err != nil {
			return err
		}

		sm.SendResponse(req.ResponseID, TaskResponse{
			Message:   fmt.Sprintf("切换到会话: %s (%s)", sessionName, targetID),
			Completed: true,
		})

	case SessionCommandList:
		sm.sessionMu.RLock()
		// 先收集所有会话指针（持有 sessionMu 读锁防止 map 修改）
		type sessionEntry struct {
			id       string
			isActive bool
			s        *SessionState
		}
		entries := make([]sessionEntry, 0, len(sm.sessions))
		for id, s := range sm.sessions {
			entries = append(entries, sessionEntry{id: id, isActive: id == sm.activeSessionID, s: s})
		}
		sm.sessionMu.RUnlock()

		// 在 sessionMu 锁外，逐个持有 session.mu 读锁读取字段
		infos := make([]SessionInfo, 0, len(entries))
		for _, e := range entries {
			e.s.mu.RLock()
			infos = append(infos, SessionInfo{
				ID:           e.id,
				Name:         e.s.Name,
				MessageCount: len(e.s.Messages),
				LastAccessed: e.s.LastAccessed,
				Tags:         e.s.Tags,
				IsActive:     e.isActive,
			})
			e.s.mu.RUnlock()
		}

		var msg strings.Builder
		msg.WriteString("活跃会话:\n")
		for _, info := range infos {
			activeMarker := "  "
			if info.IsActive {
				activeMarker = "* "
			}
			msg.WriteString(fmt.Sprintf("%s%s  %-20s  %d messages  (%.1f min ago)\n",
				activeMarker,
				info.ID,
				info.Name,
				info.MessageCount,
				time.Since(info.LastAccessed).Minutes()))
		}

		sm.SendResponse(req.ResponseID, TaskResponse{
			Message:   msg.String(),
			Completed: true,
		})

	case SessionCommandDelete:
		if len(req.Args) == 0 {
			return errs.New(errs.CodeInvalidInput, "delete command requires session_id argument")
		}

		targetID := req.Args[0]

		sm.sessionMu.Lock()
		_, inMemory := sm.sessions[targetID]
		delete(sm.sessions, targetID)
		if sm.activeSessionID == targetID {
			sm.activeSessionID = ""
		}
		sm.sessionMu.Unlock()

		// 持久化删除
		if sessionStore != nil {
			delCtx, delCancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer delCancel()
			if err := sessionStore.DeleteSession(delCtx, targetID); err != nil {
				if !inMemory {
					// 内存和存储都没有，确实不存在
					return errs.New(errs.CodeTaskNotFound, "session not found: "+targetID)
				}
				sm.logger.Warn("删除会话持久化失败",
					zap.String("session_id", targetID), zap.Error(err))
			}
		} else if !inMemory {
			return errs.New(errs.CodeTaskNotFound, "session not found: "+targetID)
		}

		sm.SendResponse(req.ResponseID, TaskResponse{
			Message:   fmt.Sprintf("已删除会话: %s", targetID),
			Completed: true,
		})

		// 级联清理 journal 数据（#6）
		if sm.journal != nil {
			jCtx, jCancel := context.WithTimeout(context.Background(), 5*time.Second)
			if err := sm.journal.DeleteSession(jCtx, targetID); err != nil {
				sm.logger.Warn("删除会话 journal 数据失败",
					zap.String("session_id", targetID), zap.Error(err))
			}
			jCancel()
		}

		// 触发 SessionEnd hook（仅当会话确实存在于内存中）
		if inMemory && sm.pluginMgr != nil {
			hookCtx, cancel := context.WithTimeout(context.Background(), plugin.HookCallTimeout)
			_ = sm.pluginMgr.TriggerSessionEnd(hookCtx, &plugin.SessionEndInput{
				SessionID: targetID,
			})
			cancel()
		}

	case SessionCommandRename:
		if len(req.Args) == 0 {
			return errs.New(errs.CodeInvalidInput, "rename command requires name argument")
		}

		newName := req.Args[0]
		var oldName string
		var renamedSession *SessionState
		if err := func() error {
			sm.sessionMu.RLock()
			// H1 fix: 优先按 req.SessionID 查找，fallback 到 activeSessionID
			targetID := req.SessionID
			if targetID == "" {
				targetID = sm.activeSessionID
			}
			session := sm.sessions[targetID]
			sm.sessionMu.RUnlock()
			if session == nil {
				return errs.New(errs.CodeTaskNotFound, "当前无活跃会话")
			}
			// 使用 session.mu 写锁保护字段更新，避免与并发读操作竞态
			session.mu.Lock()
			oldName = session.Name
			session.Name = newName
			session.mu.Unlock()
			renamedSession = session
			return nil
		}(); err != nil {
			return err
		}

		// 持久化重命名到数据库
		if sessionStore != nil && renamedSession != nil {
			if err := sm.SaveSession(context.Background(), sessionStore, renamedSession); err != nil {
				sm.logger.Warn("持久化会话重命名失败", zap.String("session_id", renamedSession.ID), zap.Error(err))
			}
		}

		sm.SendResponse(req.ResponseID, TaskResponse{
			Message:   fmt.Sprintf("session renamed: %s → %s", oldName, newName),
			Completed: true,
		})

	case SessionCommandInfo:
		sm.sessionMu.RLock()
		session := sm.sessions[sm.activeSessionID]
		sm.sessionMu.RUnlock()

		if session == nil {
			return errs.New(errs.CodeTaskNotFound, "当前无活跃会话")
		}

		// 持有 session.mu 读锁读取字段，避免与并发写操作竞态
		session.mu.RLock()
		info := fmt.Sprintf("会话信息:\n"+
			"  ID: %s\n"+
			"  名称: %s\n"+
			"  消息数: %d\n"+
			"  创建时间: %s\n"+
			"  最后访问: %s\n",
			session.ID,
			session.Name,
			len(session.Messages),
			session.Created.Format(time.RFC3339),
			session.LastAccessed.Format(time.RFC3339))
		session.mu.RUnlock()

		sm.SendResponse(req.ResponseID, TaskResponse{
			Message:   info,
			Completed: true,
		})

	case SessionCommandExport:
		sm.sessionMu.RLock()
		session := sm.sessions[sm.activeSessionID]
		sm.sessionMu.RUnlock()

		if session == nil {
			return errs.New(errs.CodeTaskNotFound, "当前无活跃会话")
		}

		// 持有 session.mu 读锁读取字段，避免与并发写操作竞态
		session.mu.RLock()
		exportData := map[string]interface{}{
			"id":            session.ID,
			"name":          session.Name,
			"created":       session.Created.Format(time.RFC3339),
			"last_accessed": session.LastAccessed.Format(time.RFC3339),
			"tags":          session.Tags,
			"message_count": len(session.Messages),
			"messages":      session.Messages,
			"stats":         session.Stats,
		}
		session.mu.RUnlock()

		jsonData, err := json.MarshalIndent(exportData, "", "  ")
		if err != nil {
			return errs.Wrap(errs.CodeInternal, "序列化会话失败", err)
		}

		sm.SendResponse(req.ResponseID, TaskResponse{
			Content:   string(jsonData),
			Completed: true,
		})

	case SessionCommandFork:
		sm.sessionMu.RLock()
		parent := sm.sessions[sm.activeSessionID]
		sm.sessionMu.RUnlock()

		if parent == nil {
			return errs.New(errs.CodeTaskNotFound, "当前无活跃会话")
		}

		// 持有 session.mu 读锁快照父会话数据，避免与并发写操作竞态
		parent.mu.RLock()
		forkName := fmt.Sprintf("%s-fork", parent.Name)
		if len(req.Args) > 0 && req.Args[0] != "" {
			forkName = req.Args[0]
		}

		forkPoint := len(parent.Messages)
		if len(req.Args) > 1 {
			var idx int
			if _, err := fmt.Sscanf(req.Args[1], "%d", &idx); err == nil {
				if idx >= 0 && idx <= len(parent.Messages) {
					forkPoint = idx
				}
			}
		}

		forkID := uuid.New().String()
		fork := &SessionState{
			ID:           forkID,
			Name:         forkName,
			Created:      time.Now(),
			LastAccessed: time.Now(),
			Messages:     make([]llm.MessageWithTools, forkPoint),
			Metadata:     copyMap(parent.Metadata),
			Tags:         append([]string{}, parent.Tags...),
			Stats:        SessionStats{},
		}

		copy(fork.Messages, parent.Messages[:forkPoint])
		// P3 修复：fork 出的 session 归当前操作用户，不继承源 session
		// admin fork 别人的 session 后，新 session 归 admin 所有
		if req.Ctx != nil {
			fork.UserID = auth.UserIDFrom(req.Ctx)
		}
		if fork.UserID == "" {
			// fallback：ctx 无 user（auth 未启用）时继承父 session，防止变成无主
			fork.UserID = parent.UserID
		}
		parent.mu.RUnlock()

		sm.sessionMu.Lock()
		sm.sessions[forkID] = fork
		oldActiveID := sm.activeSessionID
		sm.activeSessionID = forkID
		sm.sessionMu.Unlock()

		sm.logger.Info("会话已 Fork",
			zap.String("parent_id", oldActiveID),
			zap.String("fork_id", forkID),
			zap.String("fork_name", forkName),
			zap.Int("fork_point", forkPoint))

		sm.SendResponse(req.ResponseID, TaskResponse{
			Message:   fmt.Sprintf("已创建分支会话: %s (%s)\nFork 点: %d 条消息", forkName, forkID, forkPoint),
			Completed: true,
		})

	case SessionCommandRevert:
		if len(req.Args) == 0 {
			return errs.New(errs.CodeInvalidInput, "revert 命令需要目标消息索引参数")
		}

		var targetIndex int
		if _, err := fmt.Sscanf(req.Args[0], "%d", &targetIndex); err != nil {
			return errs.New(errs.CodeInvalidInput, "无效的消息索引: "+req.Args[0])
		}

		sm.sessionMu.RLock()
		targetID := req.SessionID
		if targetID == "" {
			targetID = sm.activeSessionID
		}
		session := sm.sessions[targetID]
		sm.sessionMu.RUnlock()

		if session == nil {
			return errs.New(errs.CodeTaskNotFound, "当前无活跃会话")
		}

		// 持有 session.mu 写锁修改 Messages 字段
		session.mu.Lock()
		if targetIndex < 0 || targetIndex > len(session.Messages) {
			msgLen := len(session.Messages)
			session.mu.Unlock()
			return errs.New(errs.CodeInvalidInput, fmt.Sprintf("消息索引超出范围: %d (总消息数: %d)", targetIndex, msgLen))
		}

		removedCount := len(session.Messages) - targetIndex
		session.Messages = session.Messages[:targetIndex]
		session.LastAccessed = time.Now()
		session.mu.Unlock()

		sm.logger.Info("会话已回滚",
			zap.String("session_id", session.ID),
			zap.Int("revert_to", targetIndex),
			zap.Int("removed", removedCount))

		sm.SendResponse(req.ResponseID, TaskResponse{
			Message:   fmt.Sprintf("会话已回滚到消息 #%d，移除了 %d 条消息", targetIndex, removedCount),
			Completed: true,
		})

	default:
		return errs.New(errs.CodeInvalidInput, "unknown session command: "+string(req.Command))
	}

	return nil
}

// LoadLastActiveSession 从 Store 加载最后的活跃会话
func (sm *SessionManager) LoadLastActiveSession(ctx context.Context, st store.SessionStore) error {
	if st == nil {
		return errs.New(errs.CodeInternal, "存储未初始化")
	}

	record, err := st.GetLastActiveSession(ctx)
	if err != nil {
		return fmt.Errorf("获取最后活跃会话失败: %w", err)
	}
	if record == nil {
		return fmt.Errorf("未找到活跃会话: %w", store.ErrNotFound)
	}

	session := &SessionState{
		ID:           record.ID,
		Name:         record.Name,
		Messages:     []llm.MessageWithTools{},
		Metadata:     make(map[string]any),
		Tags:         record.Tags,
		UserID:       record.UserID,
		Created:      parseTime(record.CreatedAt),
		LastAccessed: parseTime(record.LastAccessedAt),
		Stats: SessionStats{
			MessageCount: record.MessageCount,
			TotalTokens:  record.TotalTokens,
		},
	}

	messages, err := st.GetMessages(ctx, record.ID, 0)
	if err != nil {
		return fmt.Errorf("加载消息失败: %w", err)
	} else {
		for _, msg := range messages {
			m := llm.MessageWithTools{
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
							m.ToolCalls = tcs
						}
					}
					if tcID, ok := meta["tool_call_id"].(string); ok {
						m.ToolCallID = tcID
					}
					if rc, ok := meta["reasoning_content"].(string); ok {
						m.ReasoningContent = rc
					}
					if ie, ok := meta["is_error"].(bool); ok {
						m.IsError = ie
					}
					if tn, ok := meta["tool_name"].(string); ok {
						m.ToolName = tn
					}
					// 恢复多模态内容
					if cpStr, ok := meta["content_parts"].(string); ok {
						var parts []llm.ContentPart
						if err := json.Unmarshal([]byte(cpStr), &parts); err == nil {
							m.Content = llm.NewMultiContent(parts...)
						}
					}
				}
			}
			session.Messages = append(session.Messages, m)
		}
		// 关键修复：标记已加载的消息为已保存，防止重复追加
		session.lastSavedIndex = len(session.Messages)
	}

	sm.sessionMu.Lock()
	sm.sessions[session.ID] = session
	sm.activeSessionID = session.ID
	sm.sessionMu.Unlock()

	sm.logger.Info("会话已恢复",
		zap.String("session_id", session.ID),
		zap.String("session_name", session.Name),
		zap.Int("message_count", len(session.Messages)))

	return nil
}

// SaveSession 保存单个会话的元数据到 Store。
//
// 注意：消息持久化已由 appendSessionMessage 增量完成，此方法不再写消息。
// 这消除了两个写入者竞争 lastSavedIndex 导致的重复写入和指针回退问题。
func (sm *SessionManager) SaveSession(ctx context.Context, st store.SessionStore, session *SessionState) error {
	if st == nil {
		return nil
	}

	// 持有 session.mu 读锁快照所有需要的字段，避免在 IO 期间持锁太久
	session.mu.RLock()
	record := &store.SessionRecord{
		ID:             session.ID,
		Name:           session.Name,
		CreatedAt:      session.Created.Format(time.RFC3339),
		UpdatedAt:      time.Now().Format(time.RFC3339),
		LastAccessedAt: session.LastAccessed.Format(time.RFC3339),
		MessageCount:   len(session.Messages),
		TotalTokens:    session.Stats.TotalTokens,
		Deleted:        false,
		Tags:           session.Tags,
		UserID:         session.UserID,
	}
	session.mu.RUnlock()

	if err := st.SaveSession(ctx, record); err != nil {
		return errs.Wrap(errs.CodeStoreWriteFailed, "保存会话记录失败", err)
	}

	return nil
}

// SaveAllSessions 保存所有会话
func (sm *SessionManager) SaveAllSessions(ctx context.Context, st store.SessionStore) error {
	if st == nil {
		return nil
	}

	sm.sessionMu.RLock()
	sessions := make([]*SessionState, 0, len(sm.sessions))
	for _, session := range sm.sessions {
		sessions = append(sessions, session)
	}
	sm.sessionMu.RUnlock()

	var failedCount int
	for _, session := range sessions {
		if err := sm.SaveSession(ctx, st, session); err != nil {
			sm.logger.Warn("保存会话失败",
				zap.String("session_id", session.ID),
				zap.Error(err))
			failedCount++
		}
	}

	sm.logger.Info("已保存所有会话", zap.Int("count", len(sessions)))

	if failedCount > 0 {
		return store.ErrPartialSave
	}
	return nil
}

// GetSessionByID 获取指定会话的详细信息
func (sm *SessionManager) GetSessionByID(ctx context.Context, sessionID string, st store.SessionStore) (*store.SessionRecord, error) {
	// 先检查内存中的会话
	sm.sessionMu.RLock()
	session, ok := sm.sessions[sessionID]
	sm.sessionMu.RUnlock()

	if ok {
		// 持有 session.mu 读锁读取会话字段，避免与并发写操作产生竞态
		session.mu.RLock()
		record := &store.SessionRecord{
			ID:             session.ID,
			Name:           session.Name,
			CreatedAt:      session.Created.Format(time.RFC3339),
			UpdatedAt:      time.Now().Format(time.RFC3339),
			LastAccessedAt: session.LastAccessed.Format(time.RFC3339),
			MessageCount:   len(session.Messages),
			TotalTokens:    session.Stats.TotalTokens,
			Tags:           session.Tags,
			UserID:         session.UserID,
		}
		session.mu.RUnlock()
		return record, nil
	}

	// 再从持久化存储查找
	if st == nil {
		return nil, errs.New(errs.CodeTaskNotFound, "session not found: "+sessionID)
	}
	return st.LoadSession(ctx, sessionID)
}

// CloseChannels 关闭会话通道
func (sm *SessionManager) CloseChannels() {
	sm.logger.Info("关闭会话通道")
	close(sm.requestCh)
	close(sm.responseCh)
	sm.logger.Debug("会话通道已关闭",
		zap.String("requestCh", "closed"),
		zap.String("responseCh", "closed"))
}

// SyncSessionTags 同步标签到内存中的会话状态
func (sm *SessionManager) SyncSessionTags(sessionID string, tags []string) {
	sm.sessionMu.RLock()
	session, ok := sm.sessions[sessionID]
	sm.sessionMu.RUnlock()
	if !ok {
		return
	}
	session.mu.Lock()
	session.Tags = tags
	session.mu.Unlock()
}
