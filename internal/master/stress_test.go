package master

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/chef-guo/agents-hive/internal/config"
	"github.com/chef-guo/agents-hive/internal/skills"
	"github.com/chef-guo/agents-hive/internal/store"
	"github.com/chef-guo/agents-hive/internal/subagent"
)

// TestStress_HITL_ConcurrentInputRequests 压力测试：50 个并发 HITL 输入请求与响应
func TestStress_HITL_ConcurrentInputRequests(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	skillReg := skills.NewRegistry(logger)
	agentReg := subagent.NewRegistry(logger)

	m := NewMaster(Config{}, config.HITLConfig{
		Enabled:      true,
		InputTimeout: 10 * time.Second,
	}, agentReg, skillReg, nil, logger)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.Start(ctx)
	defer m.Stop()

	const numRequests = 50
	var wg sync.WaitGroup

	// 创建 50 个并发请求，每个都有匹配的响应
	wg.Add(numRequests * 2) // 每个请求 = 1 个 waitForInput + 1 个 SubmitInput
	results := make(chan string, numRequests)
	errors := make(chan error, numRequests)

	for i := 0; i < numRequests; i++ {
		taskID := fmt.Sprintf("stress-task-%d", i)
		req := m.hitlBroker.RequestInput(taskID, "step-1", InputApproval, fmt.Sprintf("请求 %d", i), nil)

		// 等待响应的 goroutine
		go func(taskID string, req *InputRequest, id int) {
			defer wg.Done()
			resp, err := m.hitlBroker.WaitForInput(ctx, taskID, req)
			if err != nil {
				errors <- fmt.Errorf("waitForInput %d 失败: %w", id, err)
				return
			}
			results <- resp.Value
		}(taskID, req, i)

		// 提交响应的 goroutine
		go func(reqID, taskID string, id int) {
			defer wg.Done()
			time.Sleep(10 * time.Millisecond) // 短暂延迟确保 waitForInput 先就绪
			err := m.SubmitInput(InputResponse{
				RequestID: reqID,
				TaskID:    taskID,
				Action:    "approve",
				Value:     fmt.Sprintf("响应-%d", id),
			})
			if err != nil {
				errors <- fmt.Errorf("SubmitInput %d 失败: %w", id, err)
			}
		}(req.ID, taskID, i)
	}

	wg.Wait()
	close(results)
	close(errors)

	// 检查错误
	for err := range errors {
		t.Error(err)
	}

	// 验证所有响应都已收到
	count := 0
	for range results {
		count++
	}
	assert.Equal(t, numRequests, count, "应收到 %d 个响应", numRequests)
}

// TestStress_HITL_ResponseIsolation 压力测试：验证 per-request channel 不会混淆响应
func TestStress_HITL_ResponseIsolation(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	skillReg := skills.NewRegistry(logger)
	agentReg := subagent.NewRegistry(logger)

	m := NewMaster(Config{}, config.HITLConfig{
		Enabled:      true,
		InputTimeout: 10 * time.Second,
	}, agentReg, skillReg, nil, logger)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.Start(ctx)
	defer m.Stop()

	const numPairs = 30
	var wg sync.WaitGroup
	wg.Add(numPairs)

	for i := 0; i < numPairs; i++ {
		go func(id int) {
			defer wg.Done()
			taskID := fmt.Sprintf("isolation-task-%d", id)
			expectedValue := fmt.Sprintf("隔离值-%d", id)

			req := m.hitlBroker.RequestInput(taskID, "step-1", InputApproval, "测试隔离", nil)

			// 立即提交匹配的响应
			go func() {
				time.Sleep(5 * time.Millisecond)
				err := m.SubmitInput(InputResponse{
					RequestID: req.ID,
					TaskID:    taskID,
					Action:    "approve",
					Value:     expectedValue,
				})
				if err != nil {
					t.Errorf("SubmitInput 失败: %v", err)
				}
			}()

			resp, err := m.hitlBroker.WaitForInput(ctx, taskID, req)
			require.NoError(t, err, "waitForInput 不应失败")
			assert.Equal(t, expectedValue, resp.Value,
				"响应值应与请求匹配，任务 %s", taskID)
		}(i)
	}

	wg.Wait()
}

// TestStress_ConcurrentSessionCreation 压力测试：并发创建会话（通过 ProcessCommand）
func TestStress_ConcurrentSessionCreation(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	skillReg := skills.NewRegistry(logger)
	agentReg := subagent.NewRegistry(logger)

	st := store.NewMemoryStore()

	m := NewMaster(Config{Model: "test"}, config.HITLConfig{}, agentReg, skillReg, st, logger)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	m.Start(ctx)
	defer m.Stop()

	sessionDone := make(chan struct{})
	go func() {
		defer close(sessionDone)
		m.SessionLoop(ctx)
	}()

	time.Sleep(50 * time.Millisecond) // 等待 SessionLoop 就绪

	const numSessions = 20
	var wg sync.WaitGroup
	var successCount int32

	// 串行创建会话（因为 SessionLoop 是单通道处理）
	for i := 0; i < numSessions; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			select {
			case m.RequestCh() <- SessionRequest{
				Command: SessionCommandNew,
				Args:    []string{fmt.Sprintf("压力会话-%d", id)},
			}:
				select {
				case <-m.ResponseCh():
					successCount++
				case <-time.After(5 * time.Second):
					t.Errorf("等待会话 %d 响应超时", id)
				}
			case <-time.After(5 * time.Second):
				t.Errorf("发送会话 %d 请求超时", id)
			}
		}(i)
		// SessionLoop 是单线程的，需要串行化请求
		wg.Wait()
	}

	// 验证最终会话数
	m.sessionMgr.sessionMu.RLock()
	sessionCount := len(m.sessionMgr.sessions)
	m.sessionMgr.sessionMu.RUnlock()

	// 应有 numSessions + 1（初始会话）
	assert.Equal(t, numSessions+1, sessionCount, "应有 %d 个会话", numSessions+1)
	t.Logf("成功创建 %d 个会话", sessionCount)

	cancel()
	select {
	case <-sessionDone:
	case <-time.After(5 * time.Second):
	}
}

// TestStress_PendingInputsConsistency 压力测试：大量创建/清理 pendingInput 的一致性
func TestStress_PendingInputsConsistency(t *testing.T) {
	logger := zap.NewNop()
	skillReg := skills.NewRegistry(logger)
	agentReg := subagent.NewRegistry(logger)

	m := NewMaster(Config{}, config.HITLConfig{
		Enabled:      true,
		InputTimeout: 500 * time.Millisecond,
	}, agentReg, skillReg, nil, logger)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.Start(ctx)
	defer m.Stop()

	const numRequests = 100
	var wg sync.WaitGroup
	wg.Add(numRequests)

	// 创建大量请求，一半响应、一半超时
	for i := 0; i < numRequests; i++ {
		go func(id int) {
			defer wg.Done()
			taskID := fmt.Sprintf("consistency-task-%d", id)
			req := m.hitlBroker.RequestInput(taskID, "step-1", InputApproval, "一致性测试", nil)

			if id%2 == 0 {
				// 偶数：提交响应
				go func() {
					time.Sleep(10 * time.Millisecond)
					m.SubmitInput(InputResponse{
						RequestID: req.ID,
						TaskID:    taskID,
						Action:    "approve",
						Value:     "ok",
					})
				}()
			}
			// 奇数：让它超时

			// 等待结果（成功或超时）
			m.hitlBroker.WaitForInput(ctx, taskID, req)
		}(i)
	}

	wg.Wait()

	// 验证所有 pendingInput 都已清理
	m.hitlBroker.inputMu.Lock()
	remaining := len(m.hitlBroker.pendingInput)
	remainingChans := len(m.hitlBroker.pendingInputChans)
	m.hitlBroker.inputMu.Unlock()

	assert.Equal(t, 0, remaining, "pendingInput 应全部清理")
	assert.Equal(t, 0, remainingChans, "pendingInputChans 应全部清理")
}
