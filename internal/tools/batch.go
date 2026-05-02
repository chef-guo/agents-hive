package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"go.uber.org/zap"

	"github.com/chef-guo/agents-hive/internal/mcphost"
)

// NestedToolGate 是 batch 等工具执行子工具前调用的统一执行层 gate。
type NestedToolGate interface {
	CheckNestedToolAllowed(ctx context.Context, toolName string) error
}

// batchInput 定义批量操作的输入结构
type batchInput struct {
	Operations []batchOperation `json:"operations"`
	Parallel   bool             `json:"parallel,omitempty"` // 是否并行执行（可选）
}

// batchOperation 定义单个批量操作
type batchOperation struct {
	Tool  string          `json:"tool"`
	Input json.RawMessage `json:"input"`
}

// batchResult 定义单个操作的结果
type batchResult struct {
	Tool    string          `json:"tool"`
	Success bool            `json:"success"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   string          `json:"error,omitempty"`
}

// batchOutput 定义批量操作的输出结构
type batchOutput struct {
	Total      int           `json:"total"`
	Successful int           `json:"successful"`
	Failed     int           `json:"failed"`
	Results    []batchResult `json:"results"`
}

const (
	maxBatchOperations = 25 // 最大批量操作数
)

// registerBatch 注册 batch 工具
func registerBatch(host *mcphost.Host, logger *zap.Logger, gates ...NestedToolGate) {
	var gate NestedToolGate
	for _, candidate := range gates {
		if candidate != nil {
			gate = candidate
			break
		}
	}

	schema, _ := json.Marshal(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"operations": map[string]any{
				"type":        "array",
				"description": "要批量执行的操作列表（最多25个）",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"tool": map[string]any{
							"type":        "string",
							"description": "要调用的工具名称",
						},
						"input": map[string]any{
							"type":        "object",
							"description": "工具的输入参数",
						},
					},
					"required": []string{"tool", "input"},
				},
				"maxItems": maxBatchOperations,
				"minItems": 1,
			},
			"parallel": map[string]any{
				"type":        "boolean",
				"description": "是否并行执行操作（默认 false）",
			},
		},
		"required": []string{"operations"},
	})

	host.RegisterTool(
		mcphost.ToolDefinition{
			Name:        "batch",
			Description: "批量执行多个工具调用（最多25个操作），支持串行或并行执行",
			InputSchema: schema,
		},
		func(ctx context.Context, input json.RawMessage) (*mcphost.ToolResult, error) {
			var params batchInput
			if err := json.Unmarshal(input, &params); err != nil {
				return errorResult("输入无效: " + err.Error()), nil
			}

			// 验证操作数量
			if len(params.Operations) == 0 {
				return errorResult("至少需要一个操作"), nil
			}
			if len(params.Operations) > maxBatchOperations {
				return errorResult(fmt.Sprintf("操作数量超过限制（最多 %d 个）", maxBatchOperations)), nil
			}

			// 检查递归调用（防止在 batch 中调用 batch）
			for i, op := range params.Operations {
				if op.Tool == "batch" {
					return errorResult(fmt.Sprintf("操作 #%d: 不允许在 batch 中调用 batch（防止递归）", i+1)), nil
				}
			}

			logger.Info("开始批量执行工具",
				zap.Int("count", len(params.Operations)),
				zap.Bool("parallel", params.Parallel))

			// 执行批量操作
			var output batchOutput
			if params.Parallel {
				if err := validateBatchParallelSafety(host, params.Operations); err != nil {
					return errorResult(err.Error()), nil
				}
				output = executeBatchParallel(ctx, host, logger, params.Operations, gate)
			} else {
				output = executeBatchSerial(ctx, host, logger, params.Operations, gate)
			}

			// 格式化输出
			resultJSON, err := json.Marshal(output)
			if err != nil {
				return errorResult("序列化结果失败: " + err.Error()), nil
			}

			// 如果有失败的操作，标记为错误
			isError := output.Failed > 0

			logger.Info("批量执行完成",
				zap.Int("total", output.Total),
				zap.Int("successful", output.Successful),
				zap.Int("failed", output.Failed))

			return &mcphost.ToolResult{
				Content: resultJSON,
				IsError: isError,
			}, nil
		},
	)
}

func validateBatchParallelSafety(host *mcphost.Host, operations []batchOperation) error {
	for i, op := range operations {
		def, err := host.GetTool(op.Tool)
		if err != nil {
			return fmt.Errorf("操作 #%d: 工具不存在: %s", i+1, op.Tool)
		}
		if !def.IsConcurrencySafe {
			return fmt.Errorf("操作 #%d: 工具 %q 不允许并发执行非只读工具，请改用串行 batch", i+1, op.Tool)
		}
	}
	return nil
}

// executeBatchSerial 串行执行批量操作
func executeBatchSerial(ctx context.Context, host *mcphost.Host, logger *zap.Logger, operations []batchOperation, gate NestedToolGate) batchOutput {
	output := batchOutput{
		Total:   len(operations),
		Results: make([]batchResult, len(operations)),
	}

	for i, op := range operations {
		// 先检查上下文是否已取消，避免执行不必要的操作
		if ctx.Err() != nil {
			logger.Warn("批量执行被取消",
				zap.Int("completed", i),
				zap.Int("total", len(operations)))
			break
		}

		result := executeOperation(ctx, host, logger, op, i+1, gate)
		output.Results[i] = result

		if result.Success {
			output.Successful++
		} else {
			output.Failed++
		}
	}

	return output
}

// executeBatchParallel 并行执行批量操作
func executeBatchParallel(ctx context.Context, host *mcphost.Host, logger *zap.Logger, operations []batchOperation, gate NestedToolGate) batchOutput {
	output := batchOutput{
		Total:   len(operations),
		Results: make([]batchResult, len(operations)),
	}

	var wg sync.WaitGroup
	var mu sync.Mutex

	for i, op := range operations {
		wg.Add(1)
		go func(index int, operation batchOperation) {
			defer wg.Done()

			result := executeOperation(ctx, host, logger, operation, index+1, gate)

			mu.Lock()
			output.Results[index] = result
			if result.Success {
				output.Successful++
			} else {
				output.Failed++
			}
			mu.Unlock()
		}(i, op)
	}

	wg.Wait()
	return output
}

// executeOperation 执行单个操作
func executeOperation(ctx context.Context, host *mcphost.Host, logger *zap.Logger, op batchOperation, index int, gate NestedToolGate) batchResult {
	logger.Debug("执行批量操作",
		zap.Int("index", index),
		zap.String("tool", op.Tool))

	result := batchResult{
		Tool: op.Tool,
	}

	// 检查工具是否存在
	_, err := host.GetTool(op.Tool)
	if err != nil {
		result.Success = false
		result.Error = fmt.Sprintf("工具不存在: %s", op.Tool)
		logger.Warn("批量操作失败：工具不存在",
			zap.Int("index", index),
			zap.String("tool", op.Tool))
		return result
	}

	if gate != nil {
		if err := gate.CheckNestedToolAllowed(ctx, op.Tool); err != nil {
			result.Success = false
			result.Error = err.Error()
			logger.Warn("批量操作失败：执行层 gate 拒绝",
				zap.Int("index", index),
				zap.String("tool", op.Tool),
				zap.Error(err))
			return result
		}
	}

	// 执行工具
	toolResult, err := host.ExecuteTool(ctx, op.Tool, op.Input)
	if err != nil {
		result.Success = false
		result.Error = err.Error()
		logger.Warn("批量操作失败：执行错误",
			zap.Int("index", index),
			zap.String("tool", op.Tool),
			zap.Error(err))
		return result
	}

	// 检查工具执行结果
	if toolResult.IsError {
		result.Success = false
		// 提取错误消息
		var errMsg string
		if err := json.Unmarshal(toolResult.Content, &errMsg); err == nil {
			result.Error = errMsg
		} else {
			result.Error = "工具执行返回错误"
		}
		logger.Warn("批量操作失败：工具返回错误",
			zap.Int("index", index),
			zap.String("tool", op.Tool),
			zap.String("error", result.Error))
		return result
	}

	// 执行成功
	result.Success = true
	result.Result = toolResult.Content
	logger.Debug("批量操作成功",
		zap.Int("index", index),
		zap.String("tool", op.Tool))

	return result
}

// formatBatchOutput 格式化批量操作输出（用于人类可读）
func formatBatchOutput(output batchOutput) string {
	var sb strings.Builder

	sb.WriteString(fmt.Sprintf("批量执行完成: 总共 %d 个操作，成功 %d 个，失败 %d 个\n\n",
		output.Total, output.Successful, output.Failed))

	for i, result := range output.Results {
		sb.WriteString(fmt.Sprintf("操作 #%d [%s]:\n", i+1, result.Tool))
		if result.Success {
			sb.WriteString("  状态: ✓ 成功\n")
			if result.Result != nil {
				var resultStr string
				if err := json.Unmarshal(result.Result, &resultStr); err == nil {
					// 限制输出长度
					if len(resultStr) > 200 {
						resultStr = resultStr[:200] + "..."
					}
					sb.WriteString(fmt.Sprintf("  结果: %s\n", resultStr))
				}
			}
		} else {
			sb.WriteString("  状态: ✗ 失败\n")
			sb.WriteString(fmt.Sprintf("  错误: %s\n", result.Error))
		}
		sb.WriteString("\n")
	}

	return sb.String()
}
