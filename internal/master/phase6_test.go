package master

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chef-guo/agents-hive/internal/accounting"
	"github.com/chef-guo/agents-hive/internal/config"
	"github.com/chef-guo/agents-hive/internal/errs"
	"github.com/chef-guo/agents-hive/internal/llm"
	"github.com/chef-guo/agents-hive/internal/mcphost"
	"github.com/chef-guo/agents-hive/internal/runtimepolicy"
	"github.com/chef-guo/agents-hive/internal/sessiontodo"
	"github.com/chef-guo/agents-hive/internal/skills"
	"github.com/chef-guo/agents-hive/internal/store"
	"github.com/chef-guo/agents-hive/internal/subagent"
	"github.com/chef-guo/agents-hive/internal/toolctx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest"
)

// --- helpers ---

// jsonTestText 将字符串编码为 JSON 格式的 RawMessage（与 tools.jsonText 等价）
func jsonTestText(text string) json.RawMessage {
	data, _ := json.Marshal(text)
	return data
}

// newPhase6Master 创建一个用于 Phase 6 测试的最小 Master 实例
// 使用 zaptest.NewLogger 将日志输出到 testing.T（与 master_test.go 中的 newTestMaster 使用 zap.NewNop 不同，
// 因为 Phase 6 测试需要观察日志输出以验证 warn/error 路径）
func newPhase6Master(t *testing.T) *Master {
	t.Helper()
	logger := zaptest.NewLogger(t)
	skillReg := skills.NewRegistry(logger)
	st := store.NewMemoryStore()
	registry := subagent.NewRegistry(logger)
	cfg := Config{}
	hitlCfg := config.HITLConfig{Enabled: false}
	return NewMaster(cfg, hitlCfg, registry, skillReg, st, logger)
}

// newPhase6MasterWithMCPHost 创建带 mcpHost 的 Master（用于 executeTool 测试）
func newPhase6MasterWithMCPHost(t *testing.T) *Master {
	t.Helper()
	m := newPhase6Master(t)
	m.mcpHost = mcphost.NewHost(zaptest.NewLogger(t))
	return m
}

// newTestSession 创建一个测试用的 SessionState
func newTestSession(id string) *SessionState {
	return &SessionState{
		ID:           id,
		Name:         "test",
		Messages:     []llm.MessageWithTools{},
		Metadata:     make(map[string]any),
		Tags:         []string{},
		Created:      time.Now(),
		LastAccessed: time.Now(),
		Stats:        SessionStats{},
	}
}

// mockCostTracker 用于测试的成本追踪器
type mockCostTracker struct {
	mu          sync.Mutex
	sessionCost map[string]float64
}

func newMockCostTracker() *mockCostTracker {
	return &mockCostTracker{sessionCost: make(map[string]float64)}
}

func (m *mockCostTracker) Record(_ context.Context, entry accounting.UsageEntry) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sessionCost[entry.SessionID] += entry.CostUSD
	return nil
}

func (m *mockCostTracker) GetSessionCost(_ context.Context, sessionID string) (*accounting.CostSummary, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cost := m.sessionCost[sessionID]
	return &accounting.CostSummary{TotalCostUSD: cost}, nil
}

func (m *mockCostTracker) GetTotalCost(_ context.Context, _ accounting.CostFilter) (*accounting.CostSummary, error) {
	return &accounting.CostSummary{}, nil
}

func (m *mockCostTracker) Cleanup(_ context.Context, _ int) (int64, error) {
	return 0, nil
}
func (m *mockCostTracker) GetCostByUser(_ context.Context) ([]accounting.UserCost, error) {
	return nil, nil
}

func (m *mockCostTracker) GetQualityCost(_ context.Context) (*accounting.QualityCostSummary, error) {
	return &accounting.QualityCostSummary{}, nil
}

func (m *mockCostTracker) SetSessionCost(sessionID string, cost float64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sessionCost[sessionID] = cost
}

type failingSetStatusTodoStore struct {
	*sessiontodo.MemoryStore
	err error
}

func (s failingSetStatusTodoStore) SetPlanStatusWithMeta(ctx context.Context, sessionID string, status sessiontodo.PlanStatus, meta sessiontodo.SnapshotMeta) (sessiontodo.Snapshot, error) {
	if s.err != nil {
		return sessiontodo.Snapshot{}, s.err
	}
	return s.MemoryStore.SetPlanStatusWithMeta(ctx, sessionID, status, meta)
}

// --- Phase 6 Tests ---

// TestProcessTask_DirectExec 验证所有消息直接走 Master ReAct（processTaskDirectExec）
func TestProcessTask_DirectExec(t *testing.T) {
	m := newPhase6Master(t)

	// processTask 应直接调用 processTaskDirectExec，不经过任何路由
	// 由于 processTaskDirectExec 需要 LLM client，这里验证在无 LLM 时返回预期错误
	session := newTestSession("test-direct-exec")
	m.sessionMgr.SetSession(session)
	m.sessionMgr.SetActiveSessionID(session.ID)

	ctx := context.Background()
	err := m.processTask(ctx, "hello", session, 1, "trace-1", "span-1", false)

	// 无 LLM client 时应返回错误（证明走了 processTaskDirectExec 路径）
	require.Error(t, err)
	assert.Contains(t, err.Error(), "LLM client not configured")
}

// TestProcessTask_NoRouting 验证不再有路由决策
func TestProcessTask_NoRouting(t *testing.T) {
	m := newPhase6Master(t)

	// 验证 Master 结构体中不再有 llmRouter 字段（编译期验证）
	// 验证 processTask 中不再有路由分支——直接调用 processTaskDirectExec
	// 通过反射验证 Master 没有 llmRouter 字段
	// （编译期已保证：如果 llmRouter 字段存在，删除它的 Phase 3+4 代码不会编译通过）

	// 验证 buildSystemPrompt 不引用固定 Agent 路由
	prompt := m.buildSystemPrompt(nil)
	routingKeywords := []string{
		"RouteTask",
		"RouteInput",
		"RouteDecision",
		"llmRouter",
		"router_llm",
		"LLM 分类器",
		"4-way 分类",
	}
	for _, kw := range routingKeywords {
		assert.False(t, strings.Contains(prompt, kw),
			"system prompt 不应包含路由相关引用: %q", kw)
	}

	// 验证 session_loop.go 中 processTask 直接调用 processTaskDirectExec
	// 通过实际调用验证：无 LLM 时返回 "LLM client not configured"（而非路由错误）
	session := newTestSession("test-no-routing")
	m.sessionMgr.SetSession(session)
	err := m.processTask(context.Background(), "test", session, 1, "", "", false)
	require.Error(t, err)
	// 如果还有路由层，错误信息会不同（如 "routing failed" 或 "agent not found"）
	assert.Contains(t, err.Error(), "LLM client not configured")
}

// TestSpawnAgent_ConcurrencyLimit 验证子代理并发限制（单轮最多 3 个 spawn_agent）
// 通过调用提取的 filterSpawnAgentCalls helper 测试实际生产代码路径
func TestSpawnAgent_ConcurrencyLimit(t *testing.T) {
	m := newPhase6MasterWithMCPHost(t)

	// 注册 mock spawn_agent 工具用于验证实际执行数
	callCount := 0
	m.mcpHost.RegisterTool(
		mcphost.ToolDefinition{Name: "spawn_agent", Description: "test"},
		func(ctx context.Context, input json.RawMessage) (*mcphost.ToolResult, error) {
			callCount++
			return &mcphost.ToolResult{Content: jsonTestText("ok")}, nil
		},
	)

	session := newTestSession("test-concurrency")
	m.sessionMgr.SetSession(session)

	// 构造 5 个 spawn_agent 调用（超过限制 3）
	toolCalls := make([]llm.ToolCall, 5)
	for i := range toolCalls {
		toolCalls[i] = llm.ToolCall{
			ID:        fmt.Sprintf("call-%d", i),
			Name:      "spawn_agent",
			Arguments: json.RawMessage(`{}`),
		}
	}

	// 调用生产代码中提取的 filterSpawnAgentCalls
	filter := m.filterSpawnAgentCalls(toolCalls, 3)

	assert.Equal(t, 3, len(filter.ToExecute), "应允许 3 个 spawn_agent 执行")
	assert.Equal(t, 2, len(filter.Rejected), "应拒绝 2 个 spawn_agent")

	// 验证实际执行 ToExecute 列表中的工具
	for _, tc := range filter.ToExecute {
		m.executeTool(context.Background(), session, "", tc, "", "")
	}
	assert.Equal(t, 3, callCount, "mock spawn_agent 应被调用 3 次")
}

func TestToolTimeout_UsesRuntimePolicy(t *testing.T) {
	m := newPhase6MasterWithMCPHost(t)
	m.config.RuntimePolicy = runtimepolicy.Policy{ToolTimeout: 50 * time.Millisecond}.WithDefaults()

	// 注册一个会阻塞的工具
	m.mcpHost.RegisterTool(
		mcphost.ToolDefinition{Name: "slow_tool", Description: "test"},
		func(ctx context.Context, input json.RawMessage) (*mcphost.ToolResult, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		},
	)

	session := newTestSession("test-timeout")
	m.sessionMgr.SetSession(session)

	tc := llm.ToolCall{ID: "timeout-1", Name: "slow_tool", Arguments: json.RawMessage(`{}`)}
	start := time.Now()
	result := m.executeTool(context.Background(), session, "", tc, "", "")

	assert.True(t, result.IsError, "阻塞工具应因超时而返回错误")
	assert.Contains(t, result.Content, "失败")
	assert.Less(t, time.Since(start), 500*time.Millisecond)
}

// TestToolTimeout_QuestionExempt 验证 question 工具豁免 2 分钟超时
// 豁免列表见 react_processor.go:727-733 的 switch 分支：
//
//	question, parallel_dispatch, task, spawn_agent, skill, create_tool
func TestToolTimeout_QuestionExempt(t *testing.T) {
	// 验证 executeTool 中 question 工具不加额外超时
	// 通过检查代码中的 switch 分支验证

	m := newPhase6MasterWithMCPHost(t)

	// 注册 question 工具，验证它不会被 2 分钟超时截断
	questionCalled := false
	m.mcpHost.RegisterTool(
		mcphost.ToolDefinition{Name: "question", Description: "test"},
		func(ctx context.Context, input json.RawMessage) (*mcphost.ToolResult, error) {
			questionCalled = true
			// 验证 context 没有被 executeTool 加上 2 分钟超时
			// 如果有 2 分钟超时，deadline 应该在 ~2 分钟内
			// 如果没有额外超时，deadline 应该来自 parent context（10 分钟）或无 deadline
			deadline, hasDeadline := ctx.Deadline()
			if hasDeadline {
				remaining := time.Until(deadline)
				// 如果被加了 2 分钟超时，remaining 应该 <= 2 分钟
				// question 豁免后，remaining 应该 > 2 分钟（来自 parent 的 10 分钟）
				assert.True(t, remaining > 2*time.Minute,
					"question 工具不应有 2 分钟超时限制，剩余时间: %v", remaining)
			}
			// 无 deadline 也是正确的（说明没有加超时）
			return &mcphost.ToolResult{Content: jsonTestText("answered")}, nil
		},
	)

	session := newTestSession("test-question-exempt")
	m.sessionMgr.SetSession(session)

	// 使用 10 分钟的 parent context
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	tc := llm.ToolCall{ID: "q-1", Name: "question", Arguments: json.RawMessage(`{}`)}
	result := m.executeTool(ctx, session, "", tc, "", "")

	assert.True(t, questionCalled, "question 工具应被调用")
	assert.False(t, result.IsError, "question 工具不应返回错误")
}

// TestCostBudget_SessionLimit 验证 per-session 成本预算
// 通过调用提取的 checkCostBudget helper 测试实际生产代码路径
func TestCostBudget_SessionLimit(t *testing.T) {
	m := newPhase6Master(t)

	// 设置成本预算
	m.config.MaxSessionCost = 1.0 // 1 USD

	// 创建 mock cost tracker
	tracker := newMockCostTracker()
	recorder := accounting.NewAsyncRecorder(tracker, zap.NewNop())
	defer recorder.Stop()
	m.asyncRecorder = recorder

	ctx := context.Background()

	// 场景 1: 成本超预算 → checkCostBudget 返回 error
	tracker.SetSessionCost("test-cost", 1.5)
	err := m.checkCostBudget(ctx, "test-cost")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "session cost budget exceeded")
	assert.Contains(t, err.Error(), "1.50")

	// 场景 2: 成本未超预算 → checkCostBudget 返回 nil
	tracker.SetSessionCost("test-cost-ok", 0.5)
	err = m.checkCostBudget(ctx, "test-cost-ok")
	assert.NoError(t, err)

	// 场景 3: asyncRecorder 为 nil → 不检查，返回 nil
	m.asyncRecorder = nil
	err = m.checkCostBudget(ctx, "test-cost")
	assert.NoError(t, err)

	// 场景 4: MaxSessionCost <= 0 → 不检查，返回 nil
	m.asyncRecorder = recorder
	m.config.MaxSessionCost = 0
	err = m.checkCostBudget(ctx, "test-cost")
	assert.NoError(t, err)
}

func TestCostBudget_GracefulYieldPausesOpenTodos(t *testing.T) {
	m := newPhase6Master(t)
	m.obsCh = make(chan observabilityEntry, 8)
	todoStore := sessiontodo.NewMemoryStore()
	m.SetSessionTodoStore(todoStore)

	session := newTestSession("budget-graceful")
	session.PlanMode = true
	session.PlanStatus = sessiontodo.PlanStatusExecuting
	session.Messages = append(session.Messages,
		llm.MessageWithTools{Role: "user", Content: llm.NewTextContent("实现长任务预算 graceful yield")},
		llm.MessageWithTools{Role: "tool", ToolName: "go_test", Content: llm.NewTextContent("单测失败: missing paused response"), IsError: true},
	)
	respCh := make(chan TaskResponse, 1)
	m.sessionMgr.responseMu.Lock()
	m.sessionMgr.pendingResponses[42] = respCh
	m.sessionMgr.responseMu.Unlock()

	snap, err := todoStore.SetPlanStatus(context.Background(), session.ID, sessiontodo.PlanStatusExecuting)
	require.NoError(t, err)
	_, err = todoStore.Replace(context.Background(), session.ID, snap.PlanVersion, []sessiontodo.TodoInput{
		{ID: "read", Content: "阅读现有预算检查路径", Status: sessiontodo.TodoStatusCompleted},
		{ID: "yield", Content: "实现预算触顶交接暂停", Status: sessiontodo.TodoStatusInProgress},
		{ID: "test", Content: "补 graceful yield 回归测试", Status: sessiontodo.TodoStatusPending},
	})
	require.NoError(t, err)

	budgetErr := errs.New(errs.CodePlanExecFailed, "session cost budget exceeded: 1.50 USD > 1.00 USD")
	decision := m.handleCostBudgetExceeded(context.Background(), session, 42, budgetErr, "trace-budget", "span-budget")

	require.True(t, decision.Graceful)
	require.NoError(t, decision.Err)
	resp := <-respCh
	assert.Equal(t, string(TaskStatusPaused), resp.Status)
	assert.False(t, resp.Completed)
	for _, title := range []string{"目标", "已完成", "未完成", "最近失败", "下一步建议"} {
		assert.Contains(t, resp.Content, title)
	}
	assert.Contains(t, resp.Content, "实现长任务预算 graceful yield")
	assert.Contains(t, resp.Content, "阅读现有预算检查路径")
	assert.Contains(t, resp.Content, "实现预算触顶交接暂停")
	assert.Contains(t, resp.Content, "单测失败")

	paused, err := todoStore.Snapshot(context.Background(), session.ID)
	require.NoError(t, err)
	assert.Equal(t, sessiontodo.PlanStatusPaused, paused.PlanStatus)
	assert.Equal(t, sessiontodo.PlanStatusPaused, session.PlanStatus)
	require.NotEmpty(t, session.Messages)
	handoff := session.Messages[len(session.Messages)-1]
	assert.Equal(t, "system", handoff.Role)
	assert.Equal(t, "graceful_yield", handoff.Metadata["budget_exit_mode"])

	raw := requireQualityEventRaw(t, m, "quality.budget_exit")
	assert.Contains(t, string(raw), `"name":"quality.budget_exit"`)
	assert.Contains(t, string(raw), `"budget_exit_mode":"graceful_yield"`)
	assert.Contains(t, string(raw), `"final_status":"needs_user"`)
}

func TestCostBudget_GracefulYieldDoesNotWriteHandoffOnPauseFailure(t *testing.T) {
	m := newPhase6Master(t)
	m.obsCh = make(chan observabilityEntry, 8)
	backingStore := sessiontodo.NewMemoryStore()
	m.sessionTodoStore = failingSetStatusTodoStore{
		MemoryStore: backingStore,
		err:         errs.New(errs.CodePlanExecFailed, "pause failed"),
	}

	session := newTestSession("budget-graceful-fail")
	session.PlanMode = true
	session.PlanStatus = sessiontodo.PlanStatusExecuting
	session.Messages = append(session.Messages, llm.MessageWithTools{Role: "user", Content: llm.NewTextContent("预算触顶时不能留下误导性 handoff")})
	snap, err := backingStore.SetPlanStatus(context.Background(), session.ID, sessiontodo.PlanStatusExecuting)
	require.NoError(t, err)
	_, err = backingStore.Replace(context.Background(), session.ID, snap.PlanVersion, []sessiontodo.TodoInput{
		{ID: "next", Content: "触发 pause 失败", Status: sessiontodo.TodoStatusPending},
	})
	require.NoError(t, err)

	budgetErr := errs.New(errs.CodePlanExecFailed, "session cost budget exceeded: 3.00 USD > 1.00 USD")
	decision := m.handleCostBudgetExceeded(context.Background(), session, 0, budgetErr, "trace-fail", "span-fail")

	require.False(t, decision.Graceful)
	require.Error(t, decision.Err)
	assert.Contains(t, decision.Err.Error(), "pause failed")
	require.NotEmpty(t, session.Messages)
	last := session.Messages[len(session.Messages)-1]
	assert.NotContains(t, last.Content.Text(), "Budget Graceful Yield Handoff")
	assert.Equal(t, "hard_stop", last.Metadata["budget_exit_mode"])
	assert.Equal(t, sessiontodo.PlanStatusPaused, session.PlanStatus)

	raw := requireQualityEventRaw(t, m, "quality.budget_exit")
	assert.Contains(t, string(raw), `"budget_exit_mode":"hard_stop"`)
	assert.Contains(t, string(raw), `"final_status":"fail"`)
}

func TestCostBudget_HardStopKeepsReasonAndPausedState(t *testing.T) {
	m := newPhase6Master(t)
	m.obsCh = make(chan observabilityEntry, 8)
	session := newTestSession("budget-hard-stop")
	session.PlanMode = true
	session.PlanStatus = sessiontodo.PlanStatusExecuting
	budgetErr := errs.New(errs.CodePlanExecFailed, "session cost budget exceeded: 2.00 USD > 1.00 USD")

	decision := m.handleCostBudgetExceeded(context.Background(), session, 0, budgetErr, "trace-hard", "span-hard")

	require.False(t, decision.Graceful)
	require.Error(t, decision.Err)
	assert.Contains(t, decision.Err.Error(), "session cost budget exceeded")
	assert.Equal(t, sessiontodo.PlanStatusPaused, session.PlanStatus)
	require.NotEmpty(t, session.Messages)
	last := session.Messages[len(session.Messages)-1]
	assert.Contains(t, last.Content.Text(), "session cost budget exceeded")
	assert.Equal(t, "hard_stop", last.Metadata["budget_exit_mode"])

	raw := requireQualityEventRaw(t, m, "quality.budget_exit")
	assert.Contains(t, string(raw), `"budget_exit_mode":"hard_stop"`)
	assert.Contains(t, string(raw), `session cost budget exceeded`)
}

func requireQualityEventRaw(t *testing.T, m *Master, eventName string) json.RawMessage {
	t.Helper()
	deadline := time.After(200 * time.Millisecond)
	for {
		select {
		case entry := <-m.obsCh:
			if entry.log == nil || entry.log.Attributes == nil {
				continue
			}
			raw, ok := entry.log.Attributes["quality_event"].(json.RawMessage)
			if !ok || !strings.Contains(string(raw), fmt.Sprintf(`"name":"%s"`, eventName)) {
				continue
			}
			return raw
		case <-deadline:
			t.Fatalf("quality event %s not found", eventName)
			return nil
		}
	}
}

// TestExecuteTool_SessionIDInjection 验证 executeTool 注入 sessionID 到 ctx
func TestExecuteTool_SessionIDInjection(t *testing.T) {
	m := newPhase6MasterWithMCPHost(t)

	var capturedSessionID string
	m.mcpHost.RegisterTool(
		mcphost.ToolDefinition{Name: "check_session", Description: "test"},
		func(ctx context.Context, input json.RawMessage) (*mcphost.ToolResult, error) {
			capturedSessionID = toolctx.GetSessionID(ctx)
			return &mcphost.ToolResult{Content: jsonTestText("ok")}, nil
		},
	)

	session := newTestSession("session-inject-test")
	m.sessionMgr.SetSession(session)

	tc := llm.ToolCall{ID: "inject-1", Name: "check_session", Arguments: json.RawMessage(`{}`)}
	result := m.executeTool(context.Background(), session, "", tc, "", "")

	assert.False(t, result.IsError, "工具不应返回错误")
	assert.Equal(t, "session-inject-test", capturedSessionID,
		"executeTool 应将 sessionID 注入到 ctx 中")
}

// TestLLMNilResponse_Handled 验证 LLM 返回 nil response 时的防御处理
// 通过调用提取的 validateLLMResponse helper 测试实际生产代码路径
func TestLLMNilResponse_Handled(t *testing.T) {
	// 场景 1: nil response → 返回 error
	err := validateLLMResponse(nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "LLM returned nil response")

	// 场景 2: 非 nil response → 返回 nil
	validResp := &llm.ChatWithToolsResponse{
		Content:      "hello",
		FinishReason: "stop",
	}
	err = validateLLMResponse(validResp)
	assert.NoError(t, err)
}
