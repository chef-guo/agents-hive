package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"go.uber.org/zap"

	"github.com/chef-guo/agents-hive/internal/mcphost"
)

// mockTool 模拟工具执行器
func mockTool(shouldFail bool, result string) mcphost.ToolExecutor {
	return func(ctx context.Context, input json.RawMessage) (*mcphost.ToolResult, error) {
		if shouldFail {
			return &mcphost.ToolResult{
				Content: jsonText("模拟工具失败"),
				IsError: true,
			}, nil
		}
		return &mcphost.ToolResult{
			Content: jsonText(result),
			IsError: false,
		}, nil
	}
}

func TestBatch(t *testing.T) {
	logger := zap.NewNop()
	host := mcphost.NewHost(logger)

	// 注册模拟工具
	host.RegisterTool(
		mcphost.ToolDefinition{
			Name:              "test_success",
			Description:       "测试成功工具",
			InputSchema:       json.RawMessage(`{"type":"object"}`),
			IsConcurrencySafe: true,
		},
		mockTool(false, "success result"),
	)

	host.RegisterTool(
		mcphost.ToolDefinition{
			Name:        "test_fail",
			Description: "测试失败工具",
			InputSchema: json.RawMessage(`{"type":"object"}`),
		},
		mockTool(true, ""),
	)

	// 注册 batch 工具
	registerBatch(host, logger)

	tests := []struct {
		name           string
		input          batchInput
		expectError    bool
		expectedTotal  int
		expectedSucces int
		expectedFailed int
	}{
		{
			name: "单个成功操作",
			input: batchInput{
				Operations: []batchOperation{
					{
						Tool:  "test_success",
						Input: json.RawMessage(`{}`),
					},
				},
			},
			expectError:    false,
			expectedTotal:  1,
			expectedSucces: 1,
			expectedFailed: 0,
		},
		{
			name: "多个成功操作（串行）",
			input: batchInput{
				Operations: []batchOperation{
					{Tool: "test_success", Input: json.RawMessage(`{}`)},
					{Tool: "test_success", Input: json.RawMessage(`{}`)},
					{Tool: "test_success", Input: json.RawMessage(`{}`)},
				},
				Parallel: false,
			},
			expectError:    false,
			expectedTotal:  3,
			expectedSucces: 3,
			expectedFailed: 0,
		},
		{
			name: "多个成功操作（并行）",
			input: batchInput{
				Operations: []batchOperation{
					{Tool: "test_success", Input: json.RawMessage(`{}`)},
					{Tool: "test_success", Input: json.RawMessage(`{}`)},
					{Tool: "test_success", Input: json.RawMessage(`{}`)},
				},
				Parallel: true,
			},
			expectError:    false,
			expectedTotal:  3,
			expectedSucces: 3,
			expectedFailed: 0,
		},
		{
			name: "部分失败",
			input: batchInput{
				Operations: []batchOperation{
					{Tool: "test_success", Input: json.RawMessage(`{}`)},
					{Tool: "test_fail", Input: json.RawMessage(`{}`)},
					{Tool: "test_success", Input: json.RawMessage(`{}`)},
				},
			},
			expectError:    true, // 有失败的操作
			expectedTotal:  3,
			expectedSucces: 2,
			expectedFailed: 1,
		},
		{
			name: "工具不存在",
			input: batchInput{
				Operations: []batchOperation{
					{Tool: "test_success", Input: json.RawMessage(`{}`)},
					{Tool: "nonexistent", Input: json.RawMessage(`{}`)},
				},
			},
			expectError:    true,
			expectedTotal:  2,
			expectedSucces: 1,
			expectedFailed: 1,
		},
		{
			name: "全部失败",
			input: batchInput{
				Operations: []batchOperation{
					{Tool: "test_fail", Input: json.RawMessage(`{}`)},
					{Tool: "test_fail", Input: json.RawMessage(`{}`)},
				},
			},
			expectError:    true,
			expectedTotal:  2,
			expectedSucces: 0,
			expectedFailed: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			inputJSON, err := json.Marshal(tt.input)
			if err != nil {
				t.Fatalf("序列化输入失败: %v", err)
			}

			result, err := host.ExecuteTool(context.Background(), "batch", inputJSON)
			if err != nil {
				t.Fatalf("ExecuteTool 失败: %v", err)
			}

			// 检查错误状态
			if result.IsError != tt.expectError {
				t.Errorf("期望 IsError=%v, 实际 IsError=%v", tt.expectError, result.IsError)
			}

			// 解析输出
			var output batchOutput
			if err := json.Unmarshal(result.Content, &output); err != nil {
				t.Fatalf("解析输出失败: %v", err)
			}

			// 验证统计信息
			if output.Total != tt.expectedTotal {
				t.Errorf("期望 Total=%d, 实际 Total=%d", tt.expectedTotal, output.Total)
			}
			if output.Successful != tt.expectedSucces {
				t.Errorf("期望 Successful=%d, 实际 Successful=%d", tt.expectedSucces, output.Successful)
			}
			if output.Failed != tt.expectedFailed {
				t.Errorf("期望 Failed=%d, 实际 Failed=%d", tt.expectedFailed, output.Failed)
			}

			// 验证结果数量
			if len(output.Results) != tt.expectedTotal {
				t.Errorf("期望 %d 个结果, 实际 %d 个", tt.expectedTotal, len(output.Results))
			}
		})
	}
}

type testNestedToolGate struct {
	denyTool string
	called   int
}

func (g *testNestedToolGate) CheckNestedToolAllowed(ctx context.Context, toolName string) error {
	g.called++
	if toolName == g.denyTool {
		return fmt.Errorf("plan mode gate denied %s", toolName)
	}
	return nil
}

func TestBatchNestedToolGateRejectsBeforeExecution(t *testing.T) {
	logger := zap.NewNop()
	host := mcphost.NewHost(logger)

	executed := false
	host.RegisterTool(
		mcphost.ToolDefinition{
			Name:        "write_file",
			Description: "测试写工具",
			InputSchema: json.RawMessage(`{"type":"object"}`),
		},
		func(ctx context.Context, input json.RawMessage) (*mcphost.ToolResult, error) {
			executed = true
			return textResult("不应该执行"), nil
		},
	)

	gate := &testNestedToolGate{denyTool: "write_file"}
	registerBatch(host, logger, gate)

	inputJSON, err := json.Marshal(batchInput{
		Operations: []batchOperation{
			{Tool: "write_file", Input: json.RawMessage(`{}`)},
		},
	})
	if err != nil {
		t.Fatalf("序列化输入失败: %v", err)
	}

	result, err := host.ExecuteTool(context.Background(), "batch", inputJSON)
	if err != nil {
		t.Fatalf("ExecuteTool 失败: %v", err)
	}
	if !result.IsError {
		t.Fatal("期望 batch 返回错误")
	}
	if executed {
		t.Fatal("nested gate 拒绝后不应执行子工具")
	}
	if gate.called != 1 {
		t.Fatalf("期望 gate 调用 1 次，实际 %d", gate.called)
	}

	var output batchOutput
	if err := json.Unmarshal(result.Content, &output); err != nil {
		t.Fatalf("解析输出失败: %v", err)
	}
	if output.Failed != 1 || output.Results[0].Success {
		t.Fatalf("期望 1 个失败结果: %+v", output)
	}
	if !strings.Contains(output.Results[0].Error, "plan mode gate denied") {
		t.Fatalf("错误消息未包含 gate 原因: %q", output.Results[0].Error)
	}
}

func TestBatchParallelRejectsUnsafeTools(t *testing.T) {
	logger := zap.NewNop()
	host := mcphost.NewHost(logger)

	executed := false
	host.RegisterTool(
		mcphost.ToolDefinition{
			Name:        "todo_write",
			Description: "测试状态工具",
			InputSchema: json.RawMessage(`{"type":"object"}`),
		},
		func(ctx context.Context, input json.RawMessage) (*mcphost.ToolResult, error) {
			executed = true
			return textResult("不应该执行"), nil
		},
	)
	registerBatch(host, logger)

	inputJSON, err := json.Marshal(batchInput{
		Parallel: true,
		Operations: []batchOperation{
			{Tool: "todo_write", Input: json.RawMessage(`{}`)},
		},
	})
	if err != nil {
		t.Fatalf("序列化输入失败: %v", err)
	}

	result, err := host.ExecuteTool(context.Background(), "batch", inputJSON)
	if err != nil {
		t.Fatalf("ExecuteTool 失败: %v", err)
	}
	if !result.IsError {
		t.Fatal("期望 batch.parallel 拒绝非并发安全工具")
	}
	if executed {
		t.Fatal("batch.parallel 拒绝后不应执行子工具")
	}

	var errMsg string
	if err := json.Unmarshal(result.Content, &errMsg); err != nil {
		t.Fatalf("解析错误消息失败: %v", err)
	}
	if !strings.Contains(errMsg, "不允许并发执行非只读工具") {
		t.Fatalf("错误消息不符合预期: %q", errMsg)
	}
}

func TestBatchValidation(t *testing.T) {
	logger := zap.NewNop()
	host := mcphost.NewHost(logger)

	// 注册 batch 工具
	registerBatch(host, logger)

	tests := []struct {
		name        string
		input       batchInput
		expectError bool
		errorMsg    string
	}{
		{
			name: "空操作列表",
			input: batchInput{
				Operations: []batchOperation{},
			},
			expectError: true,
			errorMsg:    "至少需要一个操作",
		},
		{
			name: "超过最大操作数",
			input: batchInput{
				Operations: make([]batchOperation, maxBatchOperations+1),
			},
			expectError: true,
			errorMsg:    "操作数量超过限制",
		},
		{
			name: "防止递归调用",
			input: batchInput{
				Operations: []batchOperation{
					{
						Tool:  "batch",
						Input: json.RawMessage(`{}`),
					},
				},
			},
			expectError: true,
			errorMsg:    "不允许在 batch 中调用 batch",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			inputJSON, err := json.Marshal(tt.input)
			if err != nil {
				t.Fatalf("序列化输入失败: %v", err)
			}

			result, err := host.ExecuteTool(context.Background(), "batch", inputJSON)
			if err != nil {
				t.Fatalf("ExecuteTool 失败: %v", err)
			}

			if !result.IsError {
				t.Error("期望返回错误，但没有")
			}

			// 检查错误消息
			var errMsg string
			if err := json.Unmarshal(result.Content, &errMsg); err != nil {
				t.Fatalf("解析错误消息失败: %v", err)
			}

			if errMsg == "" {
				t.Error("错误消息为空")
			}
		})
	}
}

func TestBatchCancellation(t *testing.T) {
	logger := zap.NewNop()
	host := mcphost.NewHost(logger)

	// 注册会阻塞的模拟工具
	host.RegisterTool(
		mcphost.ToolDefinition{
			Name:        "test_blocking",
			Description: "测试阻塞工具",
			InputSchema: json.RawMessage(`{"type":"object"}`),
		},
		func(ctx context.Context, input json.RawMessage) (*mcphost.ToolResult, error) {
			// 检查上下文是否已取消
			select {
			case <-ctx.Done():
				return &mcphost.ToolResult{
					Content: jsonText("已取消"),
					IsError: true,
				}, nil
			default:
				return &mcphost.ToolResult{
					Content: jsonText("完成"),
					IsError: false,
				}, nil
			}
		},
	)

	registerBatch(host, logger)

	// 创建可取消的上下文
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 立即取消

	input := batchInput{
		Operations: []batchOperation{
			{Tool: "test_blocking", Input: json.RawMessage(`{}`)},
			{Tool: "test_blocking", Input: json.RawMessage(`{}`)},
		},
	}

	inputJSON, err := json.Marshal(input)
	if err != nil {
		t.Fatalf("序列化输入失败: %v", err)
	}

	result, err := host.ExecuteTool(ctx, "batch", inputJSON)
	if err != nil {
		t.Fatalf("ExecuteTool 失败: %v", err)
	}

	// 解析输出
	var output batchOutput
	if err := json.Unmarshal(result.Content, &output); err != nil {
		t.Fatalf("解析输出失败: %v", err)
	}

	// 验证至少有一些结果（可能不是全部，因为被取消了）
	if len(output.Results) == 0 {
		t.Error("期望至少有一些结果")
	}
}

func TestBatchMaxOperations(t *testing.T) {
	logger := zap.NewNop()
	host := mcphost.NewHost(logger)

	// 注册成功工具
	host.RegisterTool(
		mcphost.ToolDefinition{
			Name:        "test_success",
			Description: "测试成功工具",
			InputSchema: json.RawMessage(`{"type":"object"}`),
		},
		mockTool(false, "success"),
	)

	registerBatch(host, logger)

	// 测试最大操作数（正好10个）
	operations := make([]batchOperation, maxBatchOperations)
	for i := 0; i < maxBatchOperations; i++ {
		operations[i] = batchOperation{
			Tool:  "test_success",
			Input: json.RawMessage(`{}`),
		}
	}

	input := batchInput{
		Operations: operations,
	}

	inputJSON, err := json.Marshal(input)
	if err != nil {
		t.Fatalf("序列化输入失败: %v", err)
	}

	result, err := host.ExecuteTool(context.Background(), "batch", inputJSON)
	if err != nil {
		t.Fatalf("ExecuteTool 失败: %v", err)
	}

	if result.IsError {
		t.Error("不应返回错误")
	}

	var output batchOutput
	if err := json.Unmarshal(result.Content, &output); err != nil {
		t.Fatalf("解析输出失败: %v", err)
	}

	if output.Total != maxBatchOperations {
		t.Errorf("期望 Total=%d, 实际 Total=%d", maxBatchOperations, output.Total)
	}
	if output.Successful != maxBatchOperations {
		t.Errorf("期望 Successful=%d, 实际 Successful=%d", maxBatchOperations, output.Successful)
	}
}

func TestFormatBatchOutput(t *testing.T) {
	output := batchOutput{
		Total:      3,
		Successful: 2,
		Failed:     1,
		Results: []batchResult{
			{
				Tool:    "tool1",
				Success: true,
				Result:  jsonText("result1"),
			},
			{
				Tool:    "tool2",
				Success: false,
				Error:   "error message",
			},
			{
				Tool:    "tool3",
				Success: true,
				Result:  jsonText("result3"),
			},
		},
	}

	formatted := formatBatchOutput(output)

	// 验证格式化输出包含关键信息
	if formatted == "" {
		t.Error("格式化输出为空")
	}

	// 检查是否包含统计信息
	expectedStats := []string{"总共 3 个操作", "成功 2 个", "失败 1 个"}
	for _, stat := range expectedStats {
		if !contains(formatted, stat) {
			t.Errorf("格式化输出缺少统计信息: %s", stat)
		}
	}

	// 检查是否包含工具名称
	expectedTools := []string{"tool1", "tool2", "tool3"}
	for _, tool := range expectedTools {
		if !contains(formatted, tool) {
			t.Errorf("格式化输出缺少工具名称: %s", tool)
		}
	}

	// 检查是否包含状态标记
	expectedMarkers := []string{"✓ 成功", "✗ 失败"}
	for _, marker := range expectedMarkers {
		if !contains(formatted, marker) {
			t.Errorf("格式化输出缺少状态标记: %s", marker)
		}
	}

	// 测试长结果被截断
	outputWithLongResult := batchOutput{
		Total:      1,
		Successful: 1,
		Failed:     0,
		Results: []batchResult{
			{
				Tool:    "long_tool",
				Success: true,
				Result:  jsonText(strings.Repeat("x", 300)), // 超过 200 字符
			},
		},
	}

	formatted2 := formatBatchOutput(outputWithLongResult)
	if !contains(formatted2, "...") {
		t.Error("长结果应该被截断并显示 ...")
	}
}

// contains 检查字符串是否包含子串
func contains(s, substr string) bool {
	return strings.Contains(s, substr)
}
