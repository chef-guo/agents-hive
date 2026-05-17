package master

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zaptest"

	"github.com/chef-guo/agents-hive/internal/errs"
	"github.com/chef-guo/agents-hive/internal/kb"
	"github.com/chef-guo/agents-hive/internal/llm"
	"github.com/chef-guo/agents-hive/internal/store"
)

// ─────────────────────────────────────────────────────────────────────────────
// 测试辅助函数
// ─────────────────────────────────────────────────────────────────────────────

// newTestSessionManager 创建一个测试专用的 SessionManager（stopCh 永不关闭）
func newTestSessionManager(t *testing.T) *SessionManager {
	t.Helper()
	logger := zaptest.NewLogger(t)
	stopCh := make(chan struct{})
	t.Cleanup(func() { close(stopCh) })
	return NewSessionManager(stopCh, logger)
}

type fakeKBActiveBindingHintReader struct {
	domainID string
	ok       bool
	err      error
	input    kb.ActiveBindingHintInput
}

func (f *fakeKBActiveBindingHintReader) ActiveBindingHint(_ context.Context, input kb.ActiveBindingHintInput) (string, bool, error) {
	f.input = input
	return f.domainID, f.ok, f.err
}

func TestSaveSessionRestoresKBDomainFromActiveBinding(t *testing.T) {
	sm := newTestSessionManager(t)
	hints := &fakeKBActiveBindingHintReader{domainID: "generic", ok: true}
	sm.kbBindingHints = hints
	st := store.NewMemoryStore()
	now := time.Now()
	session := &SessionState{
		ID:           "session-kb-restore",
		Name:         "kb restore",
		UserID:       "user-1",
		Messages:     []llm.MessageWithTools{},
		Created:      now,
		LastAccessed: now,
		Tags:         []string{},
	}
	sm.SetSession(session)

	require.NoError(t, sm.SaveSession(context.Background(), st, session))

	record, err := st.LoadSession(context.Background(), session.ID)
	require.NoError(t, err)
	require.Equal(t, "generic", record.KBDomainID)
	require.Equal(t, "generic", session.KBDomainIDSnapshot())
	require.Equal(t, kb.BindingTypeSession, hints.input.BindingType)
	require.Equal(t, session.ID, hints.input.BindingTarget)
	require.Equal(t, "user-1", hints.input.OwnerID)
}

// mustSendResponse 在后台 goroutine 中向 SessionManager 发送响应，用于解除
// HandleSessionCommand 中 SendResponse 对通用 responseCh 的阻塞
func mustDrainResponseCh(sm *SessionManager) chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		select {
		case <-sm.responseCh:
		case <-time.After(2 * time.Second):
		}
	}()
	return done
}

// registerAndDrainResponse 在调用 HandleSessionCommand 之前注册 per-request channel，
// 返回该 channel 以便测试验证响应内容。
func registerAndDrainResponse(sm *SessionManager, responseID uint64) chan TaskResponse {
	ch := make(chan TaskResponse, 1)
	sm.responseMu.Lock()
	sm.pendingResponses[responseID] = ch
	sm.responseMu.Unlock()
	return ch
}

// ─────────────────────────────────────────────────────────────────────────────
// NewSessionManager / 基础属性测试
// ─────────────────────────────────────────────────────────────────────────────

// TestNewSessionManager 验证构造函数初始化正确
func TestNewSessionManager(t *testing.T) {
	sm := newTestSessionManager(t)

	// 初始时无活跃会话
	assert.Empty(t, sm.GetActiveSessionID(), "初始活跃会话 ID 应为空")
	// requestCh / responseCh 必须可写可读（非 nil）
	assert.NotNil(t, sm.RequestCh(), "RequestCh 不应为 nil")
	assert.NotNil(t, sm.ResponseCh(), "ResponseCh 不应为 nil")
}

// ─────────────────────────────────────────────────────────────────────────────
// GetActiveSessionID / SetActiveSessionID
// ─────────────────────────────────────────────────────────────────────────────

// TestSetAndGetActiveSessionID 验证活跃会话 ID 的读写线程安全
func TestSetAndGetActiveSessionID(t *testing.T) {
	sm := newTestSessionManager(t)

	sm.SetActiveSessionID("sess-abc")
	assert.Equal(t, "sess-abc", sm.GetActiveSessionID())

	sm.SetActiveSessionID("sess-xyz")
	assert.Equal(t, "sess-xyz", sm.GetActiveSessionID())
}

// TestSetGetActiveSessionID_Concurrent 并发读写活跃会话 ID
func TestSetGetActiveSessionID_Concurrent(t *testing.T) {
	sm := newTestSessionManager(t)

	var wg sync.WaitGroup
	n := 200
	wg.Add(n * 2)

	for i := 0; i < n; i++ {
		id := fmt.Sprintf("sess-%d", i)
		// 写线程
		go func(sid string) {
			defer wg.Done()
			sm.SetActiveSessionID(sid)
		}(id)
		// 读线程（只验证不 panic、不 race）
		go func() {
			defer wg.Done()
			_ = sm.GetActiveSessionID()
		}()
	}

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("并发读写 activeSessionID 超时，可能存在死锁或数据竞争")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// GetSession / SetSession
// ─────────────────────────────────────────────────────────────────────────────

// TestSetAndGetSession 验证注册后可正确取回会话
func TestSetAndGetSession(t *testing.T) {
	sm := newTestSessionManager(t)

	sess := &SessionState{
		ID:       "test-id",
		Name:     "my-session",
		Messages: []llm.MessageWithTools{},

		Metadata:     make(map[string]any),
		Created:      time.Now(),
		LastAccessed: time.Now(),
	}

	sm.SetSession(sess)
	got := sm.GetSession("test-id")
	require.NotNil(t, got, "SetSession 后应能取回会话")
	assert.Equal(t, "test-id", got.ID)
	assert.Equal(t, "my-session", got.Name)
}

// TestGetSession_NotFound 获取不存在的会话应返回 nil
func TestGetSession_NotFound(t *testing.T) {
	sm := newTestSessionManager(t)
	got := sm.GetSession("nonexistent-id")
	assert.Nil(t, got, "未注册的 ID 应返回 nil")
}

// ─────────────────────────────────────────────────────────────────────────────
// GetOrCreateSession
// ─────────────────────────────────────────────────────────────────────────────

// TestGetOrCreateSession_NewSession 首次调用应创建新会话
func TestGetOrCreateSession_NewSession(t *testing.T) {
	sm := newTestSessionManager(t)

	sess, _ := sm.GetOrCreateSession("new-sess-1")

	require.NotNil(t, sess)
	assert.Equal(t, "new-sess-1", sess.ID)
	// 默认名称为"新会话"
	assert.Equal(t, "新会话", sess.Name)
	assert.NotNil(t, sess.Metadata)
	assert.NotNil(t, sess.Tags)
}

// TestGetOrCreateSession_ExistingSession 二次调用应返回同一实例并更新 LastAccessed
func TestGetOrCreateSession_ExistingSession(t *testing.T) {
	sm := newTestSessionManager(t)

	first, _ := sm.GetOrCreateSession("reuse-sess")
	require.NotNil(t, first)
	firstAccessed := first.LastAccessed

	// 轻微等待确保时间有差异
	time.Sleep(2 * time.Millisecond)

	second, _ := sm.GetOrCreateSession("reuse-sess")
	require.NotNil(t, second)

	// 必须是同一个指针
	assert.Same(t, first, second, "二次调用必须返回相同会话实例")
	// LastAccessed 应该被更新
	assert.True(t, second.LastAccessed.After(firstAccessed) || second.LastAccessed.Equal(firstAccessed),
		"LastAccessed 应被更新为更新时间")
}

// TestGetOrCreateSession_Concurrent 并发对同一 ID 调用只创建一个会话
func TestGetOrCreateSession_Concurrent(t *testing.T) {
	sm := newTestSessionManager(t)

	sessionID := "concurrent-sess"

	const goroutines = 50
	results := make([]*SessionState, goroutines)
	var wg sync.WaitGroup
	wg.Add(goroutines)

	for i := 0; i < goroutines; i++ {
		go func(idx int) {
			defer wg.Done()
			results[idx], _ = sm.GetOrCreateSession(sessionID)
		}(i)
	}

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("并发创建同一会话超时")
	}

	// 所有返回值必须是同一指针（只创建了一次）
	for i := 1; i < goroutines; i++ {
		assert.Same(t, results[0], results[i],
			"所有并发调用应返回同一 SessionState 实例，goroutine %d 不同", i)
	}
}

// TestGetOrCreateSession_DifferentIDs 对不同 ID 各创建独立会话
func TestGetOrCreateSession_DifferentIDs(t *testing.T) {
	sm := newTestSessionManager(t)

	ids := []string{"id-a", "id-b", "id-c"}
	sessions := make(map[string]*SessionState)
	for _, id := range ids {
		sessions[id], _ = sm.GetOrCreateSession(id)
	}

	for _, id := range ids {
		got := sm.GetSession(id)
		require.NotNil(t, got)
		assert.Same(t, sessions[id], got, "ID %s 的会话指针应一致", id)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// GetCurrentSessionInfo
// ─────────────────────────────────────────────────────────────────────────────

// TestGetCurrentSessionInfo_NoActive 无活跃会话时返回空字符串
func TestGetCurrentSessionInfo_NoActive(t *testing.T) {
	sm := newTestSessionManager(t)
	id, name := sm.GetCurrentSessionInfo()
	assert.Empty(t, id)
	assert.Empty(t, name)
}

// TestGetCurrentSessionInfo_ActiveExists 有活跃会话时返回正确信息
func TestGetCurrentSessionInfo_ActiveExists(t *testing.T) {
	sm := newTestSessionManager(t)

	sess := &SessionState{
		ID:       "active-sess",
		Name:     "工作会话",
		Messages: []llm.MessageWithTools{},

		Metadata:     make(map[string]any),
		Created:      time.Now(),
		LastAccessed: time.Now(),
	}
	sm.SetSession(sess)
	sm.SetActiveSessionID("active-sess")

	id, name := sm.GetCurrentSessionInfo()
	assert.Equal(t, "active-sess", id)
	assert.Equal(t, "工作会话", name)
}

// TestGetCurrentSessionInfo_ActiveIDMissing 活跃 ID 存在但会话不在 map 中
func TestGetCurrentSessionInfo_ActiveIDMissing(t *testing.T) {
	sm := newTestSessionManager(t)
	sm.SetActiveSessionID("ghost-sess")

	id, name := sm.GetCurrentSessionInfo()
	assert.Equal(t, "ghost-sess", id)
	assert.Equal(t, "unknown", name)
}

// ─────────────────────────────────────────────────────────────────────────────
// GetSessionByID（内存路径；store=nil 时的行为）
// ─────────────────────────────────────────────────────────────────────────────

// TestGetSessionByID_InMemory 从内存中取已有会话
func TestGetSessionByID_InMemory(t *testing.T) {
	sm := newTestSessionManager(t)
	ctx := context.Background()

	sess := &SessionState{
		ID:       "mem-sess",
		Name:     "内存会话",
		Messages: []llm.MessageWithTools{},

		Metadata:     make(map[string]any),
		Tags:         []string{"tag1"},
		Created:      time.Now(),
		LastAccessed: time.Now(),
		Stats:        SessionStats{TotalTokens: 42},
	}
	sm.SetSession(sess)

	record, err := sm.GetSessionByID(ctx, "mem-sess", nil)
	require.NoError(t, err)
	require.NotNil(t, record)
	assert.Equal(t, "mem-sess", record.ID)
	assert.Equal(t, "内存会话", record.Name)
	assert.Equal(t, 42, record.TotalTokens)
}

// TestGetSessionByID_NotFound_NoStore 既不在内存中也无 store 时应返回错误
func TestGetSessionByID_NotFound_NoStore(t *testing.T) {
	sm := newTestSessionManager(t)
	ctx := context.Background()

	record, err := sm.GetSessionByID(ctx, "no-such-id", nil)
	assert.Nil(t, record)
	require.Error(t, err)

	// 验证是 errs 类型且包含 session not found 信息
	assert.Contains(t, err.Error(), "no-such-id")
}

// ─────────────────────────────────────────────────────────────────────────────
// SaveSession（store=nil 时短路返回 nil）
// ─────────────────────────────────────────────────────────────────────────────

// TestSaveSession_NilStore store 为 nil 时直接返回 nil，不 panic
func TestSaveSession_NilStore(t *testing.T) {
	sm := newTestSessionManager(t)
	ctx := context.Background()

	sess := &SessionState{
		ID:       "save-sess",
		Name:     "保存测试",
		Messages: []llm.MessageWithTools{},

		Metadata:     make(map[string]any),
		Created:      time.Now(),
		LastAccessed: time.Now(),
	}

	err := sm.SaveSession(ctx, nil, sess)
	assert.NoError(t, err, "store=nil 时 SaveSession 应返回 nil")
}

// TestSaveAllSessions_NilStore store 为 nil 时直接返回，不 panic
func TestSaveAllSessions_NilStore(t *testing.T) {
	sm := newTestSessionManager(t)
	ctx := context.Background()

	// 注册几个会话
	for i := 0; i < 3; i++ {
		sm.SetSession(&SessionState{
			ID:       fmt.Sprintf("sess-%d", i),
			Name:     fmt.Sprintf("会话%d", i),
			Messages: []llm.MessageWithTools{},

			Metadata:     make(map[string]any),
			Created:      time.Now(),
			LastAccessed: time.Now(),
		})
	}

	// 不应 panic
	assert.NotPanics(t, func() {
		sm.SaveAllSessions(ctx, nil)
	})
}

// TestSaveSession_Concurrent 并发保存（store=nil）不产生竞争
func TestSaveSession_Concurrent(t *testing.T) {
	sm := newTestSessionManager(t)
	ctx := context.Background()

	sess := &SessionState{
		ID:       "concurrent-save",
		Name:     "并发保存测试",
		Messages: make([]llm.MessageWithTools, 0),

		Metadata:     make(map[string]any),
		Created:      time.Now(),
		LastAccessed: time.Now(),
	}
	sm.SetSession(sess)

	var wg sync.WaitGroup
	const n = 50
	wg.Add(n)

	for i := 0; i < n; i++ {
		go func(idx int) {
			defer wg.Done()
			// 并发追加消息后保存（nil store 只验证锁正确性）
			sess.mu.Lock()
			sess.Messages = append(sess.Messages, llm.MessageWithTools{
				Role:    "user",
				Content: llm.NewTextContent(fmt.Sprintf("msg-%d", idx)),
			})
			sess.mu.Unlock()
			_ = sm.SaveSession(ctx, nil, sess)
		}(i)
	}

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("并发 SaveSession 超时，可能存在死锁")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// SendResponse
// ─────────────────────────────────────────────────────────────────────────────

// TestSendResponse_PerRequest 注册了 per-request channel 时走专用通道
func TestSendResponse_PerRequest(t *testing.T) {
	sm := newTestSessionManager(t)

	const reqID = uint64(42)
	ch := registerAndDrainResponse(sm, reqID)

	resp := TaskResponse{Message: "hello", Completed: true}
	sm.SendResponse(reqID, resp)

	select {
	case got := <-ch:
		assert.Equal(t, "hello", got.Message)
		assert.True(t, got.Completed)
	case <-time.After(time.Second):
		t.Fatal("未在超时内收到 per-request 响应")
	}
}

// TestSendResponse_Fallback 未注册 per-request channel 时走通用 responseCh
func TestSendResponse_Fallback(t *testing.T) {
	sm := newTestSessionManager(t)

	resp := TaskResponse{Message: "fallback", Completed: true}
	// 在后台读取，避免阻塞
	go func() {
		sm.SendResponse(0, resp)
	}()

	select {
	case got := <-sm.responseCh:
		assert.Equal(t, "fallback", got.Message)
	case <-time.After(time.Second):
		t.Fatal("未在超时内收到通用 responseCh 响应")
	}
}

// TestSendResponse_Concurrent 并发 SendResponse 不产生竞争
func TestSendResponse_Concurrent(t *testing.T) {
	sm := newTestSessionManager(t)

	const n = 20
	// 注册 n 个 per-request 通道
	channels := make([]chan TaskResponse, n)
	for i := 0; i < n; i++ {
		ch := make(chan TaskResponse, 1)
		reqID := uint64(i + 1)
		sm.responseMu.Lock()
		sm.pendingResponses[reqID] = ch
		sm.responseMu.Unlock()
		channels[i] = ch
	}

	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(idx int) {
			defer wg.Done()
			sm.SendResponse(uint64(idx+1), TaskResponse{
				Message:   fmt.Sprintf("resp-%d", idx),
				Completed: true,
			})
		}(i)
	}

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("并发 SendResponse 超时")
	}

	// 验证每个通道都收到了恰好一个响应
	for i, ch := range channels {
		select {
		case got := <-ch:
			assert.Equal(t, fmt.Sprintf("resp-%d", i), got.Message)
		default:
			t.Errorf("通道 %d 未收到响应", i)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// HandleSessionCommand — SessionCommandNew
// ─────────────────────────────────────────────────────────────────────────────

// TestHandleSessionCommand_New_DefaultName 无参数时使用默认名称 "main"
func TestHandleSessionCommand_New_DefaultName(t *testing.T) {
	sm := newTestSessionManager(t)

	const reqID = uint64(1)
	ch := registerAndDrainResponse(sm, reqID)

	req := SessionRequest{
		Command:    SessionCommandNew,
		ResponseID: reqID,
	}
	err := sm.HandleSessionCommand(req)
	require.NoError(t, err)

	resp := <-ch
	assert.True(t, resp.Completed)
	assert.Contains(t, resp.Message, "新会话")

	// activeSessionID 应已切换到新会话
	activeID := sm.GetActiveSessionID()
	assert.NotEmpty(t, activeID)
	assert.Regexp(t, `^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`, activeID)
}

// TestHandleSessionCommand_New_CustomName 有参数时使用自定义名称
func TestHandleSessionCommand_New_CustomName(t *testing.T) {
	sm := newTestSessionManager(t)

	const reqID = uint64(2)
	ch := registerAndDrainResponse(sm, reqID)

	req := SessionRequest{
		Command:    SessionCommandNew,
		Args:       []string{"我的会话"},
		ResponseID: reqID,
	}
	err := sm.HandleSessionCommand(req)
	require.NoError(t, err)

	resp := <-ch
	assert.Contains(t, resp.Message, "我的会话")

	// 活跃会话的 Name 应为 "我的会话"
	activeID := sm.GetActiveSessionID()
	sess := sm.GetSession(activeID)
	require.NotNil(t, sess)
	assert.Equal(t, "我的会话", sess.Name)
}

// ─────────────────────────────────────────────────────────────────────────────
// HandleSessionCommand — SessionCommandSwitch
// ─────────────────────────────────────────────────────────────────────────────

// TestHandleSessionCommand_Switch_Success 切换到已存在的会话
func TestHandleSessionCommand_Switch_Success(t *testing.T) {
	sm := newTestSessionManager(t)

	// 创建两个会话
	_, _ = sm.GetOrCreateSession("sess-A")
	_, _ = sm.GetOrCreateSession("sess-B")
	sm.SetActiveSessionID("sess-A")

	const reqID = uint64(10)
	ch := registerAndDrainResponse(sm, reqID)

	req := SessionRequest{
		Command:    SessionCommandSwitch,
		Args:       []string{"sess-B"},
		ResponseID: reqID,
	}
	err := sm.HandleSessionCommand(req)
	require.NoError(t, err)

	resp := <-ch
	assert.True(t, resp.Completed)
	assert.Contains(t, resp.Message, "sess-B")
	assert.Equal(t, "sess-B", sm.GetActiveSessionID())
}

// TestHandleSessionCommand_Switch_NotFound 切换到不存在的会话应返回错误
func TestHandleSessionCommand_Switch_NotFound(t *testing.T) {
	sm := newTestSessionManager(t)

	req := SessionRequest{
		Command: SessionCommandSwitch,
		Args:    []string{"ghost-sess"},
	}
	err := sm.HandleSessionCommand(req)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ghost-sess")
}

// TestHandleSessionCommand_Switch_NoArgs 缺少参数应返回错误
func TestHandleSessionCommand_Switch_NoArgs(t *testing.T) {
	sm := newTestSessionManager(t)

	req := SessionRequest{Command: SessionCommandSwitch}
	err := sm.HandleSessionCommand(req)
	require.Error(t, err)
}

// ─────────────────────────────────────────────────────────────────────────────
// HandleSessionCommand — SessionCommandList
// ─────────────────────────────────────────────────────────────────────────────

// TestHandleSessionCommand_List 列出所有会话，活跃会话带 "*" 标记
func TestHandleSessionCommand_List(t *testing.T) {
	sm := newTestSessionManager(t)

	_, _ = sm.GetOrCreateSession("list-A")
	_, _ = sm.GetOrCreateSession("list-B")
	sm.SetActiveSessionID("list-A")

	const reqID = uint64(20)
	ch := registerAndDrainResponse(sm, reqID)

	req := SessionRequest{
		Command:    SessionCommandList,
		ResponseID: reqID,
	}
	err := sm.HandleSessionCommand(req)
	require.NoError(t, err)

	resp := <-ch
	assert.True(t, resp.Completed)
	assert.Contains(t, resp.Message, "活跃会话")
	assert.Contains(t, resp.Message, "list-A")
	assert.Contains(t, resp.Message, "list-B")
	// 活跃会话应带 "*" 标记
	assert.Contains(t, resp.Message, "* ")
}

// TestHandleSessionCommand_List_Empty 无会话时也能正常返回
func TestHandleSessionCommand_List_Empty(t *testing.T) {
	sm := newTestSessionManager(t)

	const reqID = uint64(21)
	ch := registerAndDrainResponse(sm, reqID)

	req := SessionRequest{
		Command:    SessionCommandList,
		ResponseID: reqID,
	}
	err := sm.HandleSessionCommand(req)
	require.NoError(t, err)

	resp := <-ch
	assert.True(t, resp.Completed)
	assert.Contains(t, resp.Message, "活跃会话")
}

// ─────────────────────────────────────────────────────────────────────────────
// HandleSessionCommand — SessionCommandInfo
// ─────────────────────────────────────────────────────────────────────────────

// TestHandleSessionCommand_Info_Success 有活跃会话时返回详细信息
func TestHandleSessionCommand_Info_Success(t *testing.T) {
	sm := newTestSessionManager(t)

	_, _ = sm.GetOrCreateSession("info-sess")
	sm.SetActiveSessionID("info-sess")

	const reqID = uint64(30)
	ch := registerAndDrainResponse(sm, reqID)

	req := SessionRequest{
		Command:    SessionCommandInfo,
		ResponseID: reqID,
	}
	err := sm.HandleSessionCommand(req)
	require.NoError(t, err)

	resp := <-ch
	assert.True(t, resp.Completed)
	assert.Contains(t, resp.Message, "info-sess")
}

// TestHandleSessionCommand_Info_NoActive 无活跃会话时返回错误
func TestHandleSessionCommand_Info_NoActive(t *testing.T) {
	sm := newTestSessionManager(t)

	req := SessionRequest{Command: SessionCommandInfo}
	err := sm.HandleSessionCommand(req)
	require.Error(t, err)
}

// ─────────────────────────────────────────────────────────────────────────────
// HandleSessionCommand — SessionCommandRename
// ─────────────────────────────────────────────────────────────────────────────

// TestHandleSessionCommand_Rename_Success 正确重命名活跃会话
func TestHandleSessionCommand_Rename_Success(t *testing.T) {
	sm := newTestSessionManager(t)

	_, _ = sm.GetOrCreateSession("rename-sess")
	sm.SetActiveSessionID("rename-sess")

	const reqID = uint64(40)
	ch := registerAndDrainResponse(sm, reqID)

	req := SessionRequest{
		Command:    SessionCommandRename,
		Args:       []string{"新名称"},
		ResponseID: reqID,
	}
	err := sm.HandleSessionCommand(req)
	require.NoError(t, err)

	resp := <-ch
	assert.True(t, resp.Completed)
	assert.Contains(t, resp.Message, "新名称")

	// 实际 Name 应已更新
	sess := sm.GetSession("rename-sess")
	require.NotNil(t, sess)
	assert.Equal(t, "新名称", sess.Name)
}

// TestHandleSessionCommand_Rename_NoArgs 缺少参数应返回错误
func TestHandleSessionCommand_Rename_NoArgs(t *testing.T) {
	sm := newTestSessionManager(t)

	_, _ = sm.GetOrCreateSession("rename-sess2")
	sm.SetActiveSessionID("rename-sess2")

	req := SessionRequest{Command: SessionCommandRename}
	err := sm.HandleSessionCommand(req)
	require.Error(t, err)
}

// TestHandleSessionCommand_Rename_NoActive 无活跃会话时返回错误
func TestHandleSessionCommand_Rename_NoActive(t *testing.T) {
	sm := newTestSessionManager(t)

	req := SessionRequest{
		Command: SessionCommandRename,
		Args:    []string{"新名称"},
	}
	err := sm.HandleSessionCommand(req)
	require.Error(t, err)
}

// ─────────────────────────────────────────────────────────────────────────────
// HandleSessionCommand — SessionCommandDelete
// ─────────────────────────────────────────────────────────────────────────────

// TestHandleSessionCommand_Delete_Success 删除非活跃会话
func TestHandleSessionCommand_Delete_Success(t *testing.T) {
	sm := newTestSessionManager(t)

	_, _ = sm.GetOrCreateSession("del-A")
	_, _ = sm.GetOrCreateSession("del-B")
	sm.SetActiveSessionID("del-A") // del-B 非活跃，可删除

	const reqID = uint64(50)
	ch := registerAndDrainResponse(sm, reqID)

	req := SessionRequest{
		Command:    SessionCommandDelete,
		Args:       []string{"del-B"},
		ResponseID: reqID,
	}
	err := sm.HandleSessionCommand(req)
	require.NoError(t, err)

	resp := <-ch
	assert.True(t, resp.Completed)
	assert.Contains(t, resp.Message, "del-B")

	// del-B 应已从 map 中删除
	assert.Nil(t, sm.GetSession("del-B"))
}

// TestHandleSessionCommand_Delete_ActiveSession 删除活跃会话应返回错误
func TestHandleSessionCommand_Delete_ActiveSession(t *testing.T) {
	sm := newTestSessionManager(t)

	_, _ = sm.GetOrCreateSession("active-del")
	sm.SetActiveSessionID("active-del")

	req := SessionRequest{
		Command: SessionCommandDelete,
		Args:    []string{"active-del"},
	}
	err := sm.HandleSessionCommand(req)
	// 删除活跃会话现在是允许的（会清空 activeSessionID）
	require.NoError(t, err)

	// 验证 activeSessionID 已清空
	sm.sessionMu.RLock()
	assert.Empty(t, sm.activeSessionID, "activeSessionID should be cleared")
	sm.sessionMu.RUnlock()
}

// TestHandleSessionCommand_Delete_NotFound 删除不存在的会话应返回错误
func TestHandleSessionCommand_Delete_NotFound(t *testing.T) {
	sm := newTestSessionManager(t)

	req := SessionRequest{
		Command: SessionCommandDelete,
		Args:    []string{"no-such-sess"},
	}
	err := sm.HandleSessionCommand(req)
	require.Error(t, err)
}

// TestHandleSessionCommand_Delete_NoArgs 缺少参数应返回错误
func TestHandleSessionCommand_Delete_NoArgs(t *testing.T) {
	sm := newTestSessionManager(t)

	req := SessionRequest{Command: SessionCommandDelete}
	err := sm.HandleSessionCommand(req)
	require.Error(t, err)
}

// ─────────────────────────────────────────────────────────────────────────────
// HandleSessionCommand — SessionCommandExport
// ─────────────────────────────────────────────────────────────────────────────

// TestHandleSessionCommand_Export_Success 导出会话生成合法 JSON
func TestHandleSessionCommand_Export_Success(t *testing.T) {
	sm := newTestSessionManager(t)

	sess, _ := sm.GetOrCreateSession("export-sess")
	// 添加一条消息
	sess.mu.Lock()
	sess.Messages = append(sess.Messages, llm.MessageWithTools{
		Role:    "user",
		Content: llm.NewTextContent("hello export"),
	})
	sess.mu.Unlock()
	sm.SetActiveSessionID("export-sess")

	const reqID = uint64(60)
	ch := registerAndDrainResponse(sm, reqID)

	req := SessionRequest{
		Command:    SessionCommandExport,
		ResponseID: reqID,
	}
	err := sm.HandleSessionCommand(req)
	require.NoError(t, err)

	resp := <-ch
	assert.True(t, resp.Completed)

	// Content 应为合法 JSON
	var data map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(resp.Content), &data),
		"Export 应返回合法 JSON，实际内容: %s", resp.Content)
	assert.Equal(t, "export-sess", data["id"])
	assert.EqualValues(t, 1, data["message_count"])
}

// TestHandleSessionCommand_Export_NoActive 无活跃会话时返回错误
func TestHandleSessionCommand_Export_NoActive(t *testing.T) {
	sm := newTestSessionManager(t)

	req := SessionRequest{Command: SessionCommandExport}
	err := sm.HandleSessionCommand(req)
	require.Error(t, err)
}

// ─────────────────────────────────────────────────────────────────────────────
// HandleSessionCommand — SessionCommandFork
// ─────────────────────────────────────────────────────────────────────────────

// TestHandleSessionCommand_Fork_DefaultName Fork 后自动命名
func TestHandleSessionCommand_Fork_DefaultName(t *testing.T) {
	sm := newTestSessionManager(t)

	parent, _ := sm.GetOrCreateSession("fork-parent")
	parent.mu.Lock()
	parent.Name = "parent-session"
	parent.Messages = []llm.MessageWithTools{
		{Role: "user", Content: llm.NewTextContent("msg1")},
		{Role: "assistant", Content: llm.NewTextContent("reply1")},
	}
	parent.mu.Unlock()
	sm.SetActiveSessionID("fork-parent")

	const reqID = uint64(70)
	ch := registerAndDrainResponse(sm, reqID)

	req := SessionRequest{
		Command:    SessionCommandFork,
		ResponseID: reqID,
	}
	err := sm.HandleSessionCommand(req)
	require.NoError(t, err)

	resp := <-ch
	assert.True(t, resp.Completed)
	assert.Contains(t, resp.Message, "fork")

	// activeSessionID 应已切换到 fork 会话
	forkID := sm.GetActiveSessionID()
	assert.NotEqual(t, "fork-parent", forkID)

	// Fork 会话的消息应与父会话相同
	forkSess := sm.GetSession(forkID)
	require.NotNil(t, forkSess)
	assert.Len(t, forkSess.Messages, 2)
	assert.Contains(t, forkSess.Name, "fork")
}

// TestHandleSessionCommand_Fork_CustomName 指定自定义名称和截断点
func TestHandleSessionCommand_Fork_CustomName(t *testing.T) {
	sm := newTestSessionManager(t)

	parent, _ := sm.GetOrCreateSession("fork-parent2")
	parent.mu.Lock()
	parent.Messages = []llm.MessageWithTools{
		{Role: "user", Content: llm.NewTextContent("msg1")},
		{Role: "assistant", Content: llm.NewTextContent("reply1")},
		{Role: "user", Content: llm.NewTextContent("msg2")},
	}
	parent.mu.Unlock()
	sm.SetActiveSessionID("fork-parent2")

	const reqID = uint64(71)
	ch := registerAndDrainResponse(sm, reqID)

	req := SessionRequest{
		Command:    SessionCommandFork,
		Args:       []string{"my-fork", "1"}, // 截断到第 1 条消息
		ResponseID: reqID,
	}
	err := sm.HandleSessionCommand(req)
	require.NoError(t, err)

	resp := <-ch
	assert.Contains(t, resp.Message, "my-fork")
	assert.Contains(t, resp.Message, "1 条消息")

	forkID := sm.GetActiveSessionID()
	forkSess := sm.GetSession(forkID)
	require.NotNil(t, forkSess)
	assert.Equal(t, "my-fork", forkSess.Name)
	assert.Len(t, forkSess.Messages, 1, "截断到第 1 条消息，fork 会话应只有 1 条")
}

// TestHandleSessionCommand_Fork_NoActive 无活跃会话时返回错误
func TestHandleSessionCommand_Fork_NoActive(t *testing.T) {
	sm := newTestSessionManager(t)

	req := SessionRequest{Command: SessionCommandFork}
	err := sm.HandleSessionCommand(req)
	require.Error(t, err)
}

// ─────────────────────────────────────────────────────────────────────────────
// HandleSessionCommand — SessionCommandRevert
// ─────────────────────────────────────────────────────────────────────────────

// TestHandleSessionCommand_Revert_Success 回退到指定消息索引
func TestHandleSessionCommand_Revert_Success(t *testing.T) {
	sm := newTestSessionManager(t)

	sess, _ := sm.GetOrCreateSession("revert-sess")
	sess.mu.Lock()
	sess.Messages = []llm.MessageWithTools{
		{Role: "user", Content: llm.NewTextContent("m0")},
		{Role: "assistant", Content: llm.NewTextContent("m1")},
		{Role: "user", Content: llm.NewTextContent("m2")},
		{Role: "assistant", Content: llm.NewTextContent("m3")},
	}
	sess.mu.Unlock()
	sm.SetActiveSessionID("revert-sess")

	const reqID = uint64(80)
	ch := registerAndDrainResponse(sm, reqID)

	req := SessionRequest{
		Command:    SessionCommandRevert,
		Args:       []string{"2"}, // 保留前 2 条（索引 0, 1）
		ResponseID: reqID,
	}
	err := sm.HandleSessionCommand(req)
	require.NoError(t, err)

	resp := <-ch
	assert.True(t, resp.Completed)
	assert.Contains(t, resp.Message, "移除了 2 条消息")

	sess.mu.RLock()
	msgCount := len(sess.Messages)
	sess.mu.RUnlock()
	assert.Equal(t, 2, msgCount, "回退后应只剩 2 条消息")
}

// TestHandleSessionCommand_Revert_IndexZero 回退到 0 清空所有消息
func TestHandleSessionCommand_Revert_IndexZero(t *testing.T) {
	sm := newTestSessionManager(t)

	sess, _ := sm.GetOrCreateSession("revert-zero")
	sess.mu.Lock()
	sess.Messages = []llm.MessageWithTools{
		{Role: "user", Content: llm.NewTextContent("m0")},
		{Role: "user", Content: llm.NewTextContent("m1")},
	}
	sess.mu.Unlock()
	sm.SetActiveSessionID("revert-zero")

	const reqID = uint64(81)
	ch := registerAndDrainResponse(sm, reqID)

	req := SessionRequest{
		Command:    SessionCommandRevert,
		Args:       []string{"0"},
		ResponseID: reqID,
	}
	err := sm.HandleSessionCommand(req)
	require.NoError(t, err)

	resp := <-ch
	assert.Contains(t, resp.Message, "移除了 2 条消息")

	sess.mu.RLock()
	msgCount := len(sess.Messages)
	sess.mu.RUnlock()
	assert.Equal(t, 0, msgCount)
}

// TestHandleSessionCommand_Revert_OutOfRange 索引越界应返回错误
func TestHandleSessionCommand_Revert_OutOfRange(t *testing.T) {
	sm := newTestSessionManager(t)

	sess, _ := sm.GetOrCreateSession("revert-oob")
	sess.mu.Lock()
	sess.Messages = []llm.MessageWithTools{
		{Role: "user", Content: llm.NewTextContent("m0")},
	}
	sess.mu.Unlock()
	sm.SetActiveSessionID("revert-oob")

	req := SessionRequest{
		Command: SessionCommandRevert,
		Args:    []string{"99"},
	}
	err := sm.HandleSessionCommand(req)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "超出范围")
}

// TestHandleSessionCommand_Revert_InvalidIndex 非数字索引应返回错误
func TestHandleSessionCommand_Revert_InvalidIndex(t *testing.T) {
	sm := newTestSessionManager(t)

	_, _ = sm.GetOrCreateSession("revert-inv")
	sm.SetActiveSessionID("revert-inv")

	req := SessionRequest{
		Command: SessionCommandRevert,
		Args:    []string{"abc"},
	}
	err := sm.HandleSessionCommand(req)
	require.Error(t, err)
}

// TestHandleSessionCommand_Revert_NoArgs 缺少参数应返回错误
func TestHandleSessionCommand_Revert_NoArgs(t *testing.T) {
	sm := newTestSessionManager(t)

	req := SessionRequest{Command: SessionCommandRevert}
	err := sm.HandleSessionCommand(req)
	require.Error(t, err)
}

// TestHandleSessionCommand_Revert_NoActive 无活跃会话时返回错误
func TestHandleSessionCommand_Revert_NoActive(t *testing.T) {
	sm := newTestSessionManager(t)

	req := SessionRequest{
		Command: SessionCommandRevert,
		Args:    []string{"1"},
	}
	err := sm.HandleSessionCommand(req)
	require.Error(t, err)
}

// ─────────────────────────────────────────────────────────────────────────────
// HandleSessionCommand — Unknown Command
// ─────────────────────────────────────────────────────────────────────────────

// TestHandleSessionCommand_Unknown 未知命令应返回错误
func TestHandleSessionCommand_Unknown(t *testing.T) {
	sm := newTestSessionManager(t)

	req := SessionRequest{Command: SessionCommand("totally-unknown")}
	err := sm.HandleSessionCommand(req)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown session command")
}

// ─────────────────────────────────────────────────────────────────────────────
// 表驱动：HandleSessionCommand 各命令在无活跃会话下的错误行为
// ─────────────────────────────────────────────────────────────────────────────

// TestHandleSessionCommand_RequireActiveSession 需要活跃会话的命令在无会话时均应报错
func TestHandleSessionCommand_RequireActiveSession(t *testing.T) {
	tests := []struct {
		name string
		req  SessionRequest
	}{
		{
			name: "Info 无活跃会话",
			req:  SessionRequest{Command: SessionCommandInfo},
		},
		{
			name: "Export 无活跃会话",
			req:  SessionRequest{Command: SessionCommandExport},
		},
		{
			name: "Fork 无活跃会话",
			req:  SessionRequest{Command: SessionCommandFork},
		},
		{
			name: "Rename 无活跃会话（有参数）",
			req:  SessionRequest{Command: SessionCommandRename, Args: []string{"new"}},
		},
		{
			name: "Revert 无活跃会话（有参数）",
			req:  SessionRequest{Command: SessionCommandRevert, Args: []string{"0"}},
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			sm := newTestSessionManager(t)

			err := sm.HandleSessionCommand(tc.req)
			assert.Error(t, err, "命令 %s 在无活跃会话时应报错", tc.req.Command)
		})
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// 表驱动：HandleSessionCommand 各命令缺少必要参数
// ─────────────────────────────────────────────────────────────────────────────

// TestHandleSessionCommand_MissingArgs 各命令在缺少必要参数时应报错
func TestHandleSessionCommand_MissingArgs(t *testing.T) {
	tests := []struct {
		name    string
		command SessionCommand
	}{
		{"Switch 缺少 session_id", SessionCommandSwitch},
		{"Delete 缺少 session_id", SessionCommandDelete},
		{"Rename 缺少 name", SessionCommandRename},
		{"Revert 缺少 index", SessionCommandRevert},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			sm := newTestSessionManager(t)

			// 对于 Rename/Revert 需要预设活跃会话
			if tc.command == SessionCommandRename || tc.command == SessionCommandRevert {
				_, _ = sm.GetOrCreateSession("placeholder")
				sm.SetActiveSessionID("placeholder")
			}

			req := SessionRequest{Command: tc.command, Args: []string{}}
			err := sm.HandleSessionCommand(req)
			assert.Error(t, err, "命令 %s 缺少参数时应报错", tc.command)
		})
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// 并发安全：多 goroutine 同时操作不同会话
// ─────────────────────────────────────────────────────────────────────────────

// TestConcurrentDifferentSessions 并发对不同会话进行读写操作
func TestConcurrentDifferentSessions(t *testing.T) {
	sm := newTestSessionManager(t)

	const numSessions = 50
	const numOps = 10

	// 预先创建所有会话
	for i := 0; i < numSessions; i++ {
		_, _ = sm.GetOrCreateSession(fmt.Sprintf("conc-sess-%d", i))
	}

	var wg sync.WaitGroup
	wg.Add(numSessions * numOps)

	for i := 0; i < numSessions; i++ {
		for j := 0; j < numOps; j++ {
			sessID := fmt.Sprintf("conc-sess-%d", i)
			op := j
			go func(sid string, opIdx int) {
				defer wg.Done()
				sess := sm.GetSession(sid)
				if sess == nil {
					return
				}
				if opIdx%2 == 0 {
					// 读操作：持有读锁读取消息数
					sess.mu.RLock()
					_ = len(sess.Messages)
					sess.mu.RUnlock()
				} else {
					// 写操作：持有写锁追加消息
					sess.mu.Lock()
					sess.Messages = append(sess.Messages, llm.MessageWithTools{
						Role:    "user",
						Content: llm.NewTextContent(fmt.Sprintf("op-%d", opIdx)),
					})
					sess.mu.Unlock()
				}
			}(sessID, op)
		}
	}

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		t.Log("不同会话并发操作完成，无死锁")
	case <-time.After(10 * time.Second):
		t.Fatal("不同会话并发操作超时，可能存在死锁")
	}
}

// TestConcurrentSameSession 并发对同一会话进行读写
func TestConcurrentSameSession(t *testing.T) {
	sm := newTestSessionManager(t)

	sess, _ := sm.GetOrCreateSession("shared-sess")

	const readers = 30
	const writers = 30
	var wg sync.WaitGroup
	wg.Add(readers + writers)

	// 读线程
	for i := 0; i < readers; i++ {
		go func(idx int) {
			defer wg.Done()
			for j := 0; j < 5; j++ {
				sess.mu.RLock()
				_ = len(sess.Messages)
				_ = sess.Name
				sess.mu.RUnlock()
				time.Sleep(time.Millisecond)
			}
		}(i)
	}

	// 写线程
	for i := 0; i < writers; i++ {
		go func(idx int) {
			defer wg.Done()
			for j := 0; j < 5; j++ {
				sess.mu.Lock()
				sess.Messages = append(sess.Messages, llm.MessageWithTools{
					Role:    "user",
					Content: llm.NewTextContent(fmt.Sprintf("w%d-j%d", idx, j)),
				})
				sess.mu.Unlock()
				time.Sleep(time.Millisecond)
			}
		}(i)
	}

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		t.Log("同一会话并发读写完成，无死锁")
	case <-time.After(10 * time.Second):
		t.Fatal("同一会话并发读写超时，可能存在死锁")
	}
}

// TestConcurrentActiveSessionSwitch 并发切换活跃会话
func TestConcurrentActiveSessionSwitch(t *testing.T) {
	sm := newTestSessionManager(t)

	// 创建 10 个会话
	ids := make([]string, 10)
	for i := 0; i < 10; i++ {
		id := fmt.Sprintf("switch-sess-%d", i)
		_, _ = sm.GetOrCreateSession(id)
		ids[i] = id
	}

	var wg sync.WaitGroup
	const goroutines = 100
	wg.Add(goroutines)

	for i := 0; i < goroutines; i++ {
		go func(idx int) {
			defer wg.Done()
			targetID := ids[idx%len(ids)]
			req := SessionRequest{
				Command:    SessionCommandSwitch,
				Args:       []string{targetID},
				ResponseID: uint64(idx + 1000),
			}
			ch := make(chan TaskResponse, 1)
			sm.responseMu.Lock()
			sm.pendingResponses[req.ResponseID] = ch
			sm.responseMu.Unlock()

			_ = sm.HandleSessionCommand(req)

			sm.responseMu.Lock()
			delete(sm.pendingResponses, req.ResponseID)
			sm.responseMu.Unlock()
		}(i)
	}

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		t.Log("并发切换活跃会话完成，无死锁")
	case <-time.After(10 * time.Second):
		t.Fatal("并发切换会话超时，可能存在死锁")
	}

	// 最终 activeSessionID 必须是有效的会话之一
	finalActive := sm.GetActiveSessionID()
	found := false
	for _, id := range ids {
		if id == finalActive {
			found = true
			break
		}
	}
	assert.True(t, found, "最终活跃会话 ID 应为已知会话，实际为: %s", finalActive)
}

// ─────────────────────────────────────────────────────────────────────────────
// ProcessRequestWithResponse
// ─────────────────────────────────────────────────────────────────────────────

// TestProcessRequestWithResponse_CtxCancel context 取消时应返回 ctx.Err()
func TestProcessRequestWithResponse_CtxCancel(t *testing.T) {
	sm := newTestSessionManager(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 立刻取消

	req := SessionRequest{Input: "任意请求"}
	_, err := sm.ProcessRequestWithResponse(ctx, req)
	require.Error(t, err)
	// 错误应是 context 相关
	assert.True(t, err == context.Canceled || strings.Contains(err.Error(), "cancel"),
		"应返回 context 取消错误，实际: %v", err)
}

// TestProcessRequestWithResponse_StopCh stopCh 关闭时应返回 master stopped 错误
func TestProcessRequestWithResponse_StopCh(t *testing.T) {
	logger := zaptest.NewLogger(t)
	stopCh := make(chan struct{})
	sm := NewSessionManager(stopCh, logger)

	// 立刻关闭 stopCh
	close(stopCh)

	ctx := context.Background()
	req := SessionRequest{Input: "任意请求"}
	_, err := sm.ProcessRequestWithResponse(ctx, req)
	require.Error(t, err)

	// 验证错误类型为 errs 包的错误，且包含 "master stopped"
	var appErr *errs.Error
	if assert.ErrorAs(t, err, &appErr) {
		assert.Equal(t, errs.CodeCanceled, appErr.Code)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// LoadLastActiveSession（store=nil 时）
// ─────────────────────────────────────────────────────────────────────────────

// TestLoadLastActiveSession_NilStore store 为 nil 时应返回错误
func TestLoadLastActiveSession_NilStore(t *testing.T) {
	sm := newTestSessionManager(t)
	ctx := context.Background()
	err := sm.LoadLastActiveSession(ctx, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "存储未初始化")
}

// ─────────────────────────────────────────────────────────────────────────────
// 边界场景
// ─────────────────────────────────────────────────────────────────────────────

// TestGetOrCreateSession_VeryShortID 极短 sessionID（< 8 个字符）不应 panic
func TestGetOrCreateSession_VeryShortID(t *testing.T) {
	sm := newTestSessionManager(t)

	tests := []struct {
		id string
	}{
		{"a"},
		{"ab"},
		{"abc"},
		{"abcdefg"},  // 恰好 7 个字符
		{"abcdefgh"}, // 恰好 8 个字符
	}

	for _, tc := range tests {
		tc := tc
		t.Run("id="+tc.id, func(t *testing.T) {
			assert.NotPanics(t, func() {
				sess, _ := sm.GetOrCreateSession(tc.id)
				require.NotNil(t, sess)
				assert.Equal(t, tc.id, sess.ID)
			})
		})
	}
}

// TestSessionManager_MultipleCommandsSequential 顺序执行多个命令验证状态一致性
func TestSessionManager_MultipleCommandsSequential(t *testing.T) {
	sm := newTestSessionManager(t)

	nextID := uint64(100)
	mkCh := func() (uint64, chan TaskResponse) {
		id := nextID
		nextID++
		ch := make(chan TaskResponse, 1)
		sm.responseMu.Lock()
		sm.pendingResponses[id] = ch
		sm.responseMu.Unlock()
		return id, ch
	}

	// Step 1：创建会话 A
	id, ch := mkCh()
	require.NoError(t, sm.HandleSessionCommand(SessionRequest{
		Command: SessionCommandNew, Args: []string{"session-A"}, ResponseID: id,
	}))
	<-ch
	sessAID := sm.GetActiveSessionID()
	assert.NotEmpty(t, sessAID)

	// Step 2：创建会话 B
	id, ch = mkCh()
	require.NoError(t, sm.HandleSessionCommand(SessionRequest{
		Command: SessionCommandNew, Args: []string{"session-B"}, ResponseID: id,
	}))
	<-ch
	sessBID := sm.GetActiveSessionID()
	assert.NotEmpty(t, sessBID)
	assert.NotEqual(t, sessAID, sessBID, "两次 New 应产生不同 ID")

	// Step 3：切回会话 A
	id, ch = mkCh()
	require.NoError(t, sm.HandleSessionCommand(SessionRequest{
		Command: SessionCommandSwitch, Args: []string{sessAID}, ResponseID: id,
	}))
	<-ch
	assert.Equal(t, sessAID, sm.GetActiveSessionID())

	// Step 4：重命名会话 A
	id, ch = mkCh()
	require.NoError(t, sm.HandleSessionCommand(SessionRequest{
		Command: SessionCommandRename, Args: []string{"renamed-A"}, ResponseID: id,
	}))
	<-ch
	sessA := sm.GetSession(sessAID)
	require.NotNil(t, sessA)
	assert.Equal(t, "renamed-A", sessA.Name)

	// Step 5：导出会话 A（验证 JSON 合法）
	id, ch = mkCh()
	require.NoError(t, sm.HandleSessionCommand(SessionRequest{
		Command: SessionCommandExport, ResponseID: id,
	}))
	resp := <-ch
	var exportData map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(resp.Content), &exportData))
	assert.Equal(t, "renamed-A", exportData["name"])

	// Step 6：Fork 会话 A
	id, ch = mkCh()
	require.NoError(t, sm.HandleSessionCommand(SessionRequest{
		Command: SessionCommandFork, ResponseID: id,
	}))
	<-ch
	forkID := sm.GetActiveSessionID()
	assert.NotEqual(t, sessAID, forkID)

	// Step 7：删除会话 B（非活跃）
	id, ch = mkCh()
	require.NoError(t, sm.HandleSessionCommand(SessionRequest{
		Command: SessionCommandDelete, Args: []string{sessBID}, ResponseID: id,
	}))
	<-ch
	assert.Nil(t, sm.GetSession(sessBID), "会话 B 应已删除")
}
