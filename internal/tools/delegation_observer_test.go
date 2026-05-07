package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"

	"go.uber.org/zap"

	"github.com/chef-guo/agents-hive/internal/mcphost"
)

type recordingDelegationObserver struct {
	mu     sync.Mutex
	events []DelegationEvent
}

func (o *recordingDelegationObserver) RecordDelegation(_ context.Context, ev DelegationEvent) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.events = append(o.events, ev)
}

func (o *recordingDelegationObserver) snapshot() []DelegationEvent {
	o.mu.Lock()
	defer o.mu.Unlock()
	out := make([]DelegationEvent, len(o.events))
	copy(out, o.events)
	return out
}

func TestTaskDelegationObserverRecordsFailure(t *testing.T) {
	logger := zap.NewNop()
	host := mcphost.NewHost(logger)
	observer := &recordingDelegationObserver{}
	executor := &mockTaskExecutor{
		executeFn: func(context.Context, string, string, map[string]interface{}) (string, error) {
			return "", fmt.Errorf("agent crashed")
		},
	}
	registerTask(host, executor, logger, observer, 0)

	ctx := WithToolContext(context.Background(), &ToolContext{
		CallerType: CallerMaster,
		CallerName: "master",
		TraceID:    "trace-parent",
		SpanID:     "span-tool",
	})
	input, _ := json.Marshal(taskInput{AgentID: "research", Instruction: "调查"})
	result, err := host.ExecuteTool(ctx, "task", input)
	if err != nil {
		t.Fatalf("调用工具失败: %v", err)
	}
	if !result.IsError {
		t.Fatal("期望 task 执行失败")
	}

	events := observer.snapshot()
	if len(events) != 1 {
		t.Fatalf("期望 1 个委派事件，实际 %d", len(events))
	}
	if events[0].AgentID != "research" || events[0].AgentType != "subagent" {
		t.Fatalf("委派目标错误: %+v", events[0])
	}
	if events[0].Status != "failed" || events[0].FailureType != "runtime" {
		t.Fatalf("失败状态错误: %+v", events[0])
	}
	if !strings.Contains(events[0].Error, "agent crashed") {
		t.Fatalf("错误信息未记录: %+v", events[0])
	}
}

func TestSpawnAgentDelegationObserverRecordsWhitelistAndFailure(t *testing.T) {
	logger := zap.NewNop()
	host := mcphost.NewHost(logger)
	observer := &recordingDelegationObserver{}
	spawner := &mockAgentSpawner{}
	executor := &mockTaskExecutor{
		executeFn: func(context.Context, string, string, map[string]interface{}) (string, error) {
			return "", fmt.Errorf("timeout")
		},
	}
	registerSpawnAgent(host, executor, spawner, logger, observer, 0)

	ctx := WithToolContext(context.Background(), &ToolContext{
		CallerType: CallerMaster,
		CallerName: "master",
		TraceID:    "trace-parent",
		SpanID:     "span-tool",
	})
	input, _ := json.Marshal(spawnAgentInput{
		Name:         "worker",
		SystemPrompt: "你负责执行任务",
		Tools:        []string{"read_file", "grep"},
		Instruction:  "执行",
	})
	result, err := host.ExecuteTool(ctx, "spawn_agent", input)
	if err != nil {
		t.Fatalf("调用工具失败: %v", err)
	}
	if !result.IsError {
		t.Fatal("期望 spawn_agent 执行失败")
	}

	events := observer.snapshot()
	if len(events) != 1 {
		t.Fatalf("期望 1 个委派事件，实际 %d", len(events))
	}
	if events[0].AgentID != "dyn-mock" || events[0].AgentType != "subagent" {
		t.Fatalf("委派目标错误: %+v", events[0])
	}
	if len(events[0].ToolWhitelist) != 2 || events[0].ToolWhitelist[0] != "read_file" || events[0].ToolWhitelist[1] != "grep" {
		t.Fatalf("工具白名单未记录: %+v", events[0])
	}
	if events[0].Status != "failed" || events[0].FailureType != "runtime" {
		t.Fatalf("失败状态错误: %+v", events[0])
	}
	if events[0].ParentTraceID != "trace-parent" || events[0].ChildTraceID != "trace-parent:dyn-mock" {
		t.Fatalf("trace 边未记录: %+v", events[0])
	}
}

func TestParallelDispatchDelegationObserverRecordsPartialFailure(t *testing.T) {
	logger := zap.NewNop()
	host := mcphost.NewHost(logger)
	observer := &recordingDelegationObserver{}
	executor := &mockTaskExecutor{
		executeFn: func(_ context.Context, agentID string, _ string, _ map[string]interface{}) (string, error) {
			if agentID == "broken" {
				return "", fmt.Errorf("not found")
			}
			return "ok", nil
		},
	}
	registerParallelDispatch(host, executor, nil, logger, observer)

	ctx := WithToolContext(context.Background(), &ToolContext{
		CallerType: CallerMaster,
		CallerName: "master",
		TraceID:    "trace-parent",
		SpanID:     "span-tool",
	})
	input, _ := json.Marshal(parallelDispatchInput{Tasks: []parallelTaskItem{
		{ID: "ok", AgentID: "research", Instruction: "成功"},
		{ID: "bad", AgentID: "broken", Instruction: "失败"},
	}})
	result, err := host.ExecuteTool(ctx, "parallel_dispatch", input)
	if err != nil {
		t.Fatalf("调用工具失败: %v", err)
	}
	if result.IsError {
		t.Fatalf("parallel_dispatch 不应因部分失败返回工具错误: %s", string(result.Content))
	}

	events := observer.snapshot()
	if len(events) < 3 {
		t.Fatalf("期望至少 3 个委派事件（2 个任务 + 1 个组事件），实际 %d", len(events))
	}
	var failedTask, failedGroup *DelegationEvent
	for i := range events {
		ev := &events[i]
		if ev.AgentID == "broken" && ev.Status == "failed" {
			failedTask = ev
		}
		if ev.AgentType == "subagent_group" && ev.Status == "failed" {
			failedGroup = ev
		}
	}
	if failedTask == nil {
		t.Fatalf("未记录失败任务事件: %+v", events)
	}
	if failedTask.GroupID == "" || failedTask.FailureType != "runtime" {
		t.Fatalf("失败任务事件缺少 group/failure: %+v", *failedTask)
	}
	if failedTask.ParentTraceID != "trace-parent" || failedTask.ChildTraceID != "trace-parent:broken" {
		t.Fatalf("失败任务 trace 边未记录: %+v", *failedTask)
	}
	if failedGroup == nil {
		t.Fatalf("未记录部分失败组事件: %+v", events)
	}
	if failedGroup.GroupID != failedTask.GroupID {
		t.Fatalf("组事件和任务事件 group_id 不一致: task=%s group=%s", failedTask.GroupID, failedGroup.GroupID)
	}
	if failedGroup.ParentTraceID != "trace-parent" || failedGroup.ChildTraceID != "trace-parent:"+failedGroup.GroupID {
		t.Fatalf("组事件 trace 边未记录: %+v", *failedGroup)
	}
}
