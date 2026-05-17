package master

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/chef-guo/agents-hive/internal/llm"
	"github.com/chef-guo/agents-hive/internal/memory"
	"github.com/chef-guo/agents-hive/internal/router"
)

// setupTestMaster 使用 deadlock_test.go 中的统一实现
// 为保持接口兼容，这里提供一个简化的包装函数
func setupTestMasterSimple(t *testing.T) *Master {
	return setupTestMaster(t, nil)
}

func TestQualityMemoryInjectionConsumedOnce(t *testing.T) {
	session := &SessionState{ID: "s1"}
	session.SetQualityMemoryInjection(memory.InjectionResult{
		Text:            "## 相关记忆\n\n- [user] 可信记忆\n",
		DomainID:        "generic",
		SourceKind:      "master",
		SourceName:      "react",
		OwnerScope:      memory.TargetScopeUser,
		OwnerID:         "user-1",
		Memories:        []memory.InjectedMemory{{ID: 11, Type: memory.MemoryTypeUser}},
		EstimatedTokens: 9,
	})

	first := session.ConsumeQualityMemoryInjection()
	second := session.ConsumeQualityMemoryInjection()

	require.Len(t, first.Memories, 1)
	assert.Equal(t, int64(11), first.Memories[0].ID)
	assert.Equal(t, "generic", first.DomainID)
	assert.Equal(t, "master", first.SourceKind)
	assert.Equal(t, "react", first.SourceName)
	assert.Equal(t, memory.TargetScopeUser, first.OwnerScope)
	assert.Equal(t, "user-1", first.OwnerID)
	assert.Empty(t, second.Memories)
	assert.Empty(t, second.Text)
}

func TestContaminationStatus(t *testing.T) {
	assert.Equal(t, "none", contaminationStatus(memory.InjectionResult{}))
	assert.Equal(t, "clean", contaminationStatus(memory.InjectionResult{
		Memories: []memory.InjectedMemory{{ID: 1, Type: memory.MemoryTypeUser}},
	}))
	assert.Equal(t, "filtered", contaminationStatus(memory.InjectionResult{SkippedExpired: 1}))
	assert.Equal(t, "filtered", contaminationStatus(memory.InjectionResult{SkippedLowTrust: 1}))
	assert.Equal(t, "filtered", contaminationStatus(memory.InjectionResult{SkippedCrossUser: 1}))
	assert.Equal(t, "filtered", contaminationStatus(memory.InjectionResult{SkippedLowScore: 1}))
}

func TestShouldRecordMemoryInjectionIncludesFilteredOnly(t *testing.T) {
	assert.False(t, shouldRecordMemoryInjection(memory.InjectionResult{}))
	assert.True(t, shouldRecordMemoryInjection(memory.InjectionResult{
		Text:     "## 相关记忆\n",
		Memories: []memory.InjectedMemory{{ID: 1, Type: memory.MemoryTypeUser}},
	}))
	assert.True(t, shouldRecordMemoryInjection(memory.InjectionResult{
		SkippedExpired:   1,
		SkippedMemoryIDs: []int64{2},
	}))
}

func TestMemoryRuntimeContextUsesSessionKBDomainAndOwner(t *testing.T) {
	session := &SessionState{ID: "s-memory-domain", UserID: "session-user"}
	session.SetKBDomainID("support")

	ctx := (&Master{}).memoryRuntimeContext(context.Background(), session, "react")
	rctx := memory.RuntimeContextFromContext(ctx)

	assert.Equal(t, "session-user", rctx.UserID)
	assert.Equal(t, "s-memory-domain", rctx.SessionID)
	assert.Equal(t, "support", rctx.DomainID)
	assert.Equal(t, "master", rctx.AgentName)
	assert.Equal(t, "react", rctx.SourceName)
}

func TestMemoryRuntimeContextFallsBackToRouteDomain(t *testing.T) {
	session := &SessionState{ID: "s-memory-route", UserID: "session-user"}
	session.SetRouteDecision(router.RouteDecision{
		Intent: router.IntentFrame{DomainID: "customer_service"},
	})

	ctx := (&Master{}).memoryRuntimeContext(context.Background(), session, "react")
	rctx := memory.RuntimeContextFromContext(ctx)

	assert.Equal(t, "session-user", rctx.UserID)
	assert.Equal(t, "customer_service", rctx.DomainID)
}

func TestMemoryRuntimeContextPrefersExplicitKBDomainOverRouteDomain(t *testing.T) {
	session := &SessionState{ID: "s-memory-explicit", UserID: "session-user"}
	session.SetRouteDecision(router.RouteDecision{
		Intent: router.IntentFrame{DomainID: "generic"},
	})
	session.SetKBDomainID("support")

	ctx := (&Master{}).memoryRuntimeContext(context.Background(), session, "react")
	rctx := memory.RuntimeContextFromContext(ctx)

	assert.Equal(t, "session-user", rctx.UserID)
	assert.Equal(t, "support", rctx.DomainID)
}

func TestSessionReflectionBlockLRUAndFailureKindFilter(t *testing.T) {
	session := &SessionState{ID: "s-reflection"}

	assert.False(t, session.AddReflectionBlock(router.ReflectionBlock{
		ToolName:    "slow_tool",
		Mode:        "exec",
		Reason:      "timeout",
		FailureKind: "timeout",
	}))
	assert.False(t, session.AddReflectionBlock(router.ReflectionBlock{
		ToolName:    "net_tool",
		Mode:        "exec",
		Reason:      "network",
		FailureKind: "network",
	}))

	for i := 0; i < maxSessionReflectionBlocks+2; i++ {
		ok := session.AddReflectionBlock(router.ReflectionBlock{
			ToolName:    fmt.Sprintf("tool-%c", 'a'+i),
			Mode:        "exec",
			Reason:      "bad schema",
			FailureKind: "schema_invalid",
		})
		assert.True(t, ok)
	}

	blocks := session.ListReflectionBlocks()
	require.Len(t, blocks, maxSessionReflectionBlocks)
	assert.Equal(t, "tool-c", blocks[0].ToolName)
	assert.Equal(t, "tool-l", blocks[len(blocks)-1].ToolName)
	assert.False(t, blocks[0].CreatedAt.IsZero())

	blocks[0].ToolName = "mutated"
	assert.Equal(t, "tool-c", session.ListReflectionBlocks()[0].ToolName)
}

func TestSessionReflectionBlockClear(t *testing.T) {
	session := &SessionState{ID: "s-reflection"}
	require.True(t, session.AddReflectionBlock(router.ReflectionBlock{
		ToolName:    "skill",
		FailureKind: "permission_denied",
	}))

	session.ClearReflectionBlocks()

	assert.Empty(t, session.ListReflectionBlocks())
}

// TestSessionLoop_ExitCommand 测试退出命令
func TestSessionLoop_ExitCommand(t *testing.T) {
	m := setupTestMasterSimple(t)
	defer m.Stop()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// 启动 SessionLoop
	m.Start(ctx)
	errCh := make(chan error, 1)
	go func() {
		errCh <- m.SessionLoop(ctx)
	}()

	// 发送退出命令
	select {
	case m.RequestCh() <- SessionRequest{Input: "exit"}:
	case <-time.After(time.Second):
		t.Fatal("超时:无法发送请求")
	}

	// 等待退出响应
	select {
	case resp := <-m.ResponseCh():
		assert.True(t, resp.Exit, "应该返回 Exit=true")
	case <-time.After(time.Second):
		t.Fatal("超时:没有收到响应")
	}

	// SessionLoop 应该正常退出
	select {
	case err := <-errCh:
		assert.NoError(t, err, "SessionLoop 应该正常退出")
	case <-time.After(time.Second):
		t.Fatal("超时:SessionLoop 未退出")
	}
}

// TestSessionLoop_ClearCommand 测试清空命令
func TestSessionLoop_ClearCommand(t *testing.T) {
	m := setupTestMasterSimple(t)
	defer m.Stop()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	m.Start(ctx)
	go m.SessionLoop(ctx)

	// 发送清空命令
	select {
	case m.RequestCh() <- SessionRequest{Input: "clear"}:
	case <-time.After(time.Second):
		t.Fatal("超时:无法发送请求")
	}

	// 等待响应
	select {
	case resp := <-m.ResponseCh():
		assert.False(t, resp.Exit, "clear 不应该退出会话")
		assert.True(t, resp.Completed, "clear 应该标记为完成")
		assert.Contains(t, resp.Message, "已清空", "应该返回清空消息")
	case <-time.After(time.Second):
		t.Fatal("超时:没有收到响应")
	}

	// 验证会话历史已清空
	m.sessionMgr.sessionMu.RLock()
	session := m.sessionMgr.sessions[m.sessionMgr.activeSessionID]
	m.sessionMgr.sessionMu.RUnlock()

	require.NotNil(t, session)
	assert.Empty(t, session.Messages, "会话历史应该为空")
}

// TestSessionLoop_ResetCommand 测试重置命令
func TestSessionLoop_ResetCommand(t *testing.T) {
	m := setupTestMasterSimple(t)
	defer m.Stop()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	m.Start(ctx)
	go m.SessionLoop(ctx)

	// 发送重置命令
	select {
	case m.RequestCh() <- SessionRequest{Input: "reset"}:
	case <-time.After(time.Second):
		t.Fatal("超时:无法发送请求")
	}

	// 等待响应
	select {
	case resp := <-m.ResponseCh():
		assert.False(t, resp.Exit, "reset 不应该退出会话")
		assert.True(t, resp.Completed, "reset 应该标记为完成")
		assert.Contains(t, resp.Message, "已重置", "应该返回重置消息")
	case <-time.After(time.Second):
		t.Fatal("超时:没有收到响应")
	}

	// 验证会话已重置
	m.sessionMgr.sessionMu.RLock()
	session := m.sessionMgr.sessions[m.sessionMgr.activeSessionID]
	m.sessionMgr.sessionMu.RUnlock()

	require.NotNil(t, session)
	assert.Empty(t, session.Messages, "会话历史应该为空")
	assert.Empty(t, session.Messages, "会话消息应该为空")
}

// TestSessionLoop_ContextCancellation 测试上下文取消
func TestSessionLoop_ContextCancellation(t *testing.T) {
	m := setupTestMasterSimple(t)
	defer m.Stop()

	ctx, cancel := context.WithCancel(context.Background())

	m.Start(ctx)
	errCh := make(chan error, 1)
	go func() {
		errCh <- m.SessionLoop(ctx)
	}()

	// 取消上下文
	cancel()

	// SessionLoop 应该因上下文取消而退出
	select {
	case err := <-errCh:
		assert.ErrorIs(t, err, context.Canceled, "应该返回 context.Canceled 错误")
	case <-time.After(time.Second):
		t.Fatal("超时:SessionLoop 未响应上下文取消")
	}
}

// TestHandleSessionCommand 测试会话命令处理
func TestHandleSessionCommand(t *testing.T) {
	m := setupTestMasterSimple(t)
	defer m.Stop()

	session := &SessionState{
		ID:       "test-session",
		Messages: []llm.MessageWithTools{},
	}

	tests := []struct {
		name        string
		command     string
		wantHandled bool
		wantExit    bool
		wantClear   bool
	}{
		{"exit", "exit", true, true, false},
		{"quit", "quit", true, true, false},
		{"退出", "退出", true, true, false},
		{"clear", "clear", true, false, true},
		{"清空", "清空", true, false, true},
		{"reset", "reset", true, false, true},
		{"normal", "hello", false, false, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handled, exit := m.handleLegacyCommand(tt.command, session, 0)
			assert.Equal(t, tt.wantHandled, handled, "handled flag 不匹配")
			assert.Equal(t, tt.wantExit, exit, "exit flag 不匹配")

			if tt.wantClear {
				// clear/reset 命令会发送响应,需要从 channel 中读取
				select {
				case resp := <-m.ResponseCh():
					assert.True(t, resp.Completed)
				case <-time.After(100 * time.Millisecond):
					t.Fatal("超时:没有收到响应")
				}
			}

			if tt.wantExit {
				// exit 命令会发送响应
				select {
				case resp := <-m.ResponseCh():
					assert.True(t, resp.Exit)
				case <-time.After(100 * time.Millisecond):
					t.Fatal("超时:没有收到响应")
				}
			}
		})
	}
}

// TestMultiSession_NewCommand 测试创建新会话
func TestMultiSession_NewCommand(t *testing.T) {
	m := setupTestMasterSimple(t)
	defer m.Stop()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	m.Start(ctx)
	go m.SessionLoop(ctx)

	// 发送创建会话命令
	select {
	case m.RequestCh() <- SessionRequest{Command: SessionCommandNew, Args: []string{"test-session"}}:
	case <-time.After(time.Second):
		t.Fatal("超时:无法发送请求")
	}

	// 等待响应
	select {
	case resp := <-m.ResponseCh():
		assert.True(t, resp.Completed)
		assert.Contains(t, resp.Message, "test-session")
	case <-time.After(time.Second):
		t.Fatal("超时:没有收到响应")
	}

	// 验证会话已创建
	m.sessionMgr.sessionMu.RLock()
	sessionCount := len(m.sessionMgr.sessions)
	m.sessionMgr.sessionMu.RUnlock()

	assert.Equal(t, 2, sessionCount, "应该有 2 个会话（初始 + 新创建）")
}

// TestMultiSession_ListCommand 测试列出会话
func TestMultiSession_ListCommand(t *testing.T) {
	m := setupTestMasterSimple(t)
	defer m.Stop()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	m.Start(ctx)
	go m.SessionLoop(ctx)

	// 创建第二个会话
	select {
	case m.RequestCh() <- SessionRequest{Command: SessionCommandNew, Args: []string{"second"}}:
	case <-time.After(time.Second):
		t.Fatal("超时:无法发送创建请求")
	}
	<-m.ResponseCh() // 等待创建完成

	// 列出会话
	select {
	case m.RequestCh() <- SessionRequest{Command: SessionCommandList}:
	case <-time.After(time.Second):
		t.Fatal("超时:无法发送列表请求")
	}

	// 等待响应
	select {
	case resp := <-m.ResponseCh():
		assert.True(t, resp.Completed)
		assert.Contains(t, resp.Message, "活跃会话")
	case <-time.After(time.Second):
		t.Fatal("超时:没有收到响应")
	}
}

// TestMultiSession_SwitchCommand 测试切换会话
func TestMultiSession_SwitchCommand(t *testing.T) {
	m := setupTestMasterSimple(t)
	defer m.Stop()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	m.Start(ctx)
	go m.SessionLoop(ctx)

	// 等待初始会话创建（SessionLoop 启动时会自动创建）
	time.Sleep(50 * time.Millisecond)

	// 记录初始会话 ID
	m.sessionMgr.sessionMu.RLock()
	initialID := m.sessionMgr.activeSessionID
	m.sessionMgr.sessionMu.RUnlock()

	require.NotEmpty(t, initialID, "初始会话 ID 不应为空")

	// 创建第二个会话
	select {
	case m.RequestCh() <- SessionRequest{Command: SessionCommandNew, Args: []string{"second"}}:
	case <-time.After(time.Second):
		t.Fatal("超时:无法发送创建请求")
	}

	// 等待创建完成
	select {
	case resp := <-m.ResponseCh():
		require.True(t, resp.Completed, "创建会话应该成功")
	case <-time.After(time.Second):
		t.Fatal("超时:没有收到创建响应")
	}

	// 切换回初始会话
	select {
	case m.RequestCh() <- SessionRequest{Command: SessionCommandSwitch, Args: []string{initialID}}:
	case <-time.After(time.Second):
		t.Fatal("超时:无法发送切换请求")
	}

	// 等待响应
	select {
	case resp := <-m.ResponseCh():
		assert.True(t, resp.Completed)
		assert.Contains(t, resp.Message, "切换到会话")
	case <-time.After(time.Second):
		t.Fatal("超时:没有收到响应")
	}

	// 验证当前活跃会话
	m.sessionMgr.sessionMu.RLock()
	currentID := m.sessionMgr.activeSessionID
	m.sessionMgr.sessionMu.RUnlock()

	assert.Equal(t, initialID, currentID, "应该切换回初始会话")
}

// TestMultiSession_DeleteCommand 测试删除会话
func TestMultiSession_DeleteCommand(t *testing.T) {
	m := setupTestMasterSimple(t)
	defer m.Stop()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	m.Start(ctx)
	go m.SessionLoop(ctx)

	// 创建第二个会话
	select {
	case m.RequestCh() <- SessionRequest{Command: SessionCommandNew, Args: []string{"to-delete"}}:
	case <-time.After(time.Second):
		t.Fatal("超时:无法发送创建请求")
	}
	<-m.ResponseCh() // 等待创建完成

	m.sessionMgr.sessionMu.RLock()
	secondID := m.sessionMgr.activeSessionID
	initialCount := len(m.sessionMgr.sessions)
	m.sessionMgr.sessionMu.RUnlock()

	// 切换回初始会话
	m.sessionMgr.sessionMu.RLock()
	firstID := ""
	for id := range m.sessionMgr.sessions {
		if id != secondID {
			firstID = id
			break
		}
	}
	m.sessionMgr.sessionMu.RUnlock()

	select {
	case m.RequestCh() <- SessionRequest{Command: SessionCommandSwitch, Args: []string{firstID}}:
	case <-time.After(time.Second):
		t.Fatal("超时:无法发送切换请求")
	}
	<-m.ResponseCh() // 等待切换完成

	// 删除第二个会话
	select {
	case m.RequestCh() <- SessionRequest{Command: SessionCommandDelete, Args: []string{secondID}}:
	case <-time.After(time.Second):
		t.Fatal("超时:无法发送删除请求")
	}

	// 等待响应
	select {
	case resp := <-m.ResponseCh():
		assert.True(t, resp.Completed)
		assert.Contains(t, resp.Message, "已删除")
	case <-time.After(time.Second):
		t.Fatal("超时:没有收到响应")
	}

	// 验证会话已删除
	m.sessionMgr.sessionMu.RLock()
	currentCount := len(m.sessionMgr.sessions)
	m.sessionMgr.sessionMu.RUnlock()

	assert.Equal(t, initialCount-1, currentCount, "会话数应该减少 1")
}

// TestGetOrCreateSession 测试获取或创建会话
func TestGetOrCreateSession(t *testing.T) {
	m := setupTestMasterSimple(t)
	defer m.Stop()

	// 第一次调用创建新会话
	session1, _ := m.sessionMgr.GetOrCreateSession("test-id")
	assert.NotNil(t, session1)
	assert.Equal(t, "test-id", session1.ID)

	// 第二次调用返回已存在的会话
	session2, _ := m.sessionMgr.GetOrCreateSession("test-id")
	assert.Equal(t, session1, session2, "应该返回同一个会话实例")

	// 验证会话已存在
	m.sessionMgr.sessionMu.RLock()
	_, exists := m.sessionMgr.sessions["test-id"]
	m.sessionMgr.sessionMu.RUnlock()
	assert.True(t, exists, "会话应该存在于 sessions map 中")
}

// TestMultiSession_RenameCommand 测试重命名会话
func TestMultiSession_RenameCommand(t *testing.T) {
	m := setupTestMasterSimple(t)
	defer m.Stop()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	m.Start(ctx)
	go m.SessionLoop(ctx)

	// 等待初始会话创建
	time.Sleep(50 * time.Millisecond)

	// 发送重命名命令
	select {
	case m.RequestCh() <- SessionRequest{Command: SessionCommandRename, Args: []string{"新名称"}}:
	case <-time.After(time.Second):
		t.Fatal("超时:无法发送重命名请求")
	}

	// 等待响应
	select {
	case resp := <-m.ResponseCh():
		assert.True(t, resp.Completed)
		assert.Contains(t, resp.Message, "新名称")
	case <-time.After(time.Second):
		t.Fatal("超时:没有收到响应")
	}

	// 验证会话名称已更新
	m.sessionMgr.sessionMu.RLock()
	session := m.sessionMgr.sessions[m.sessionMgr.activeSessionID]
	m.sessionMgr.sessionMu.RUnlock()

	assert.NotNil(t, session)
	assert.Equal(t, "新名称", session.Name)
}

// TestMultiSession_InfoCommand 测试获取会话信息
func TestMultiSession_InfoCommand(t *testing.T) {
	m := setupTestMasterSimple(t)
	defer m.Stop()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	m.Start(ctx)
	go m.SessionLoop(ctx)

	// 等待初始会话创建
	time.Sleep(50 * time.Millisecond)

	// 发送信息命令
	select {
	case m.RequestCh() <- SessionRequest{Command: SessionCommandInfo}:
	case <-time.After(time.Second):
		t.Fatal("超时:无法发送信息请求")
	}

	// 等待响应
	select {
	case resp := <-m.ResponseCh():
		assert.True(t, resp.Completed)
		assert.Contains(t, resp.Message, "会话")
	case <-time.After(time.Second):
		t.Fatal("超时:没有收到响应")
	}
}

// TestMultiSession_ExportCommand 测试导出会话
func TestMultiSession_ExportCommand(t *testing.T) {
	m := setupTestMasterSimple(t)
	defer m.Stop()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	m.Start(ctx)
	go m.SessionLoop(ctx)

	// 等待初始会话创建
	time.Sleep(50 * time.Millisecond)

	// 发送导出命令
	select {
	case m.RequestCh() <- SessionRequest{Command: SessionCommandExport}:
	case <-time.After(time.Second):
		t.Fatal("超时:无法发送导出请求")
	}

	// 等待响应
	select {
	case resp := <-m.ResponseCh():
		assert.True(t, resp.Completed)
		// 导出命令应该返回会话数据或文件路径
	case <-time.After(time.Second):
		t.Fatal("超时:没有收到响应")
	}
}

// TestMultiSession_ConcurrentAccess 测试并发访问会话
func TestMultiSession_ConcurrentAccess(t *testing.T) {
	m := setupTestMasterSimple(t)
	defer m.Stop()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	m.Start(ctx)
	go m.SessionLoop(ctx)

	// 等待初始会话创建
	time.Sleep(50 * time.Millisecond)

	const numGoroutines = 10
	const opsPerGoroutine = 20

	errCh := make(chan error, numGoroutines)

	// 并发执行多个操作
	for i := 0; i < numGoroutines; i++ {
		go func(id int) {
			for j := 0; j < opsPerGoroutine; j++ {
				// 随机执行不同操作
				switch j % 4 {
				case 0:
					// 创建新会话
					select {
					case m.RequestCh() <- SessionRequest{
						Command: SessionCommandNew,
						Args:    []string{time.Now().Format("session-20060102-150405.000")},
					}:
						<-m.ResponseCh() // 等待响应
					case <-ctx.Done():
						errCh <- ctx.Err()
						return
					}

				case 1:
					// 列出会话
					select {
					case m.RequestCh() <- SessionRequest{Command: SessionCommandList}:
						<-m.ResponseCh() // 等待响应
					case <-ctx.Done():
						errCh <- ctx.Err()
						return
					}

				case 2:
					// 读取当前会话信息
					m.sessionMgr.sessionMu.RLock()
					_ = m.sessionMgr.activeSessionID
					_ = len(m.sessionMgr.sessions)
					m.sessionMgr.sessionMu.RUnlock()

				case 3:
					// 获取会话信息
					select {
					case m.RequestCh() <- SessionRequest{Command: SessionCommandInfo}:
						<-m.ResponseCh() // 等待响应
					case <-ctx.Done():
						errCh <- ctx.Err()
						return
					}
				}

				// 短暂休眠避免过度竞争
				time.Sleep(time.Millisecond)
			}
			errCh <- nil
		}(i)
	}

	// 等待所有 goroutine 完成
	for i := 0; i < numGoroutines; i++ {
		select {
		case err := <-errCh:
			assert.NoError(t, err, "并发操作不应出错")
		case <-time.After(15 * time.Second):
			t.Fatal("超时:并发测试未完成")
		}
	}

	// 验证最终状态一致性
	m.sessionMgr.sessionMu.RLock()
	sessionCount := len(m.sessionMgr.sessions)
	m.sessionMgr.sessionMu.RUnlock()

	assert.Greater(t, sessionCount, 0, "应该至少有一个会话")
	t.Logf("并发测试完成，共创建 %d 个会话", sessionCount)
}
