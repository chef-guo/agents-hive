package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"go.uber.org/zap"
	"gopkg.in/yaml.v3"

	"github.com/chef-guo/agents-hive/internal/config"
	"github.com/chef-guo/agents-hive/internal/mcphost"
	"github.com/chef-guo/agents-hive/internal/router"
)

// ApprovalBridge 连接到 HITL 审批机制（解耦 master 包依赖）
type ApprovalBridge interface {
	// RequestApproval 请求用户审批，返回是否批准
	RequestApproval(ctx context.Context, toolName, description string, details map[string]string) (bool, error)
}

// createToolInput create_tool 工具的输入参数
type createToolInput struct {
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Type        string            `json:"type"`              // "shell" | "http"
	Command     string            `json:"command,omitempty"` // shell 类型
	URL         string            `json:"url,omitempty"`     // http 类型
	Method      string            `json:"method,omitempty"`  // http 类型
	Headers     map[string]string `json:"headers,omitempty"` // http 类型
	Parameters  []ToolParameter   `json:"parameters,omitempty"`
	AllowWrite  bool              `json:"allow_write"`
	Timeout     int               `json:"timeout,omitempty"`
}

// removeToolInput remove_tool 工具的输入参数
type removeToolInput struct {
	Name string `json:"name"`
}

// persistCustomTool 将自定义工具保存到磁盘
func persistCustomTool(toolsDir string, tool CustomTool, logger *zap.Logger) {
	if toolsDir == "" {
		return
	}

	// 确保目录存在
	if err := os.MkdirAll(toolsDir, 0755); err != nil {
		logger.Warn("创建自定义工具目录失败，工具仅存在于内存中",
			zap.String("dir", toolsDir),
			zap.Error(err))
		return
	}

	data, err := yaml.Marshal(&tool)
	if err != nil {
		logger.Warn("序列化工具定义失败，工具仅存在于内存中",
			zap.String("name", tool.Name),
			zap.Error(err))
		return
	}

	filePath := filepath.Join(toolsDir, tool.Name+".yaml")
	if err := os.WriteFile(filePath, data, 0644); err != nil {
		logger.Warn("保存工具文件失败，工具仅存在于内存中",
			zap.String("path", filePath),
			zap.Error(err))
		return
	}

	logger.Info("自定义工具已持久化到磁盘",
		zap.String("name", tool.Name),
		zap.String("path", filePath))
}

// removeCustomToolFile 从磁盘删除自定义工具文件
func removeCustomToolFile(toolsDir string, toolName string, logger *zap.Logger) {
	if toolsDir == "" {
		return
	}

	filePath := filepath.Join(toolsDir, toolName+".yaml")
	if err := os.Remove(filePath); err != nil {
		if !os.IsNotExist(err) {
			logger.Warn("删除工具文件失败",
				zap.String("path", filePath),
				zap.Error(err))
		}
		// 也尝试 .yml 后缀
		ymlPath := filepath.Join(toolsDir, toolName+".yml")
		if err2 := os.Remove(ymlPath); err2 != nil && !os.IsNotExist(err2) {
			logger.Warn("删除工具文件失败",
				zap.String("path", ymlPath),
				zap.Error(err2))
		}
		return
	}

	logger.Info("已从磁盘删除自定义工具文件",
		zap.String("name", toolName),
		zap.String("path", filePath))
}

// registerCreateTool 注册 create_tool 到 MCP host
func registerCreateTool(host *mcphost.Host, logger *zap.Logger, customToolsDir string, cfg *config.Config, approvalBridge ApprovalBridge) {
	schema, _ := json.Marshal(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"name": map[string]any{
				"type":        "string",
				"description": "工具名称（英文、下划线，不能与内置工具重名）",
			},
			"description": map[string]any{
				"type":        "string",
				"description": "工具的功能描述",
			},
			"type": map[string]any{
				"type":        "string",
				"enum":        []string{"shell", "http"},
				"description": "工具类型：shell（执行命令）或 http（调用 API）",
			},
			"command": map[string]any{
				"type":        "string",
				"description": "shell 类型：要执行的命令（支持 {{.param}} 模板变量）",
			},
			"url": map[string]any{
				"type":        "string",
				"description": "http 类型：请求 URL（支持 {{.param}} 模板变量）",
			},
			"method": map[string]any{
				"type":        "string",
				"description": "http 类型：HTTP 方法（GET/POST/PUT/DELETE），默认 GET",
			},
			"headers": map[string]any{
				"type":        "object",
				"description": "http 类型：请求头（键值对）",
			},
			"parameters": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"name":        map[string]any{"type": "string"},
						"type":        map[string]any{"type": "string", "enum": []string{"string", "integer", "boolean", "array"}},
						"description": map[string]any{"type": "string"},
						"required":    map[string]any{"type": "boolean"},
						"default":     map[string]any{},
					},
				},
				"description": "工具参数列表",
			},
			"allow_write": map[string]any{
				"type":        "boolean",
				"description": "是否允许写操作（shell 类型），默认 false",
			},
			"timeout": map[string]any{
				"type":        "integer",
				"description": "执行超时秒数，默认 30",
			},
		},
		"required": []string{"name", "description", "type"},
	})

	host.RegisterTool(
		mcphost.ToolDefinition{
			Name:        "create_tool",
			Description: "运行时动态创建自定义工具。支持 shell（执行命令）和 http（调用 API）两种类型。创建后立即可用，并自动持久化到磁盘。",
			InputSchema: schema,
		},
		func(ctx context.Context, input json.RawMessage) (*mcphost.ToolResult, error) {
			var params createToolInput
			if err := json.Unmarshal(input, &params); err != nil {
				return errorResult("参数解析失败: " + err.Error()), nil
			}

			// 校验
			if params.Name == "" {
				return errorResult("工具名称不能为空"), nil
			}
			if strings.ContainsAny(params.Name, " \t\n./\\") {
				return errorResult("工具名称不能包含空格、点、斜杠等特殊字符"), nil
			}
			if strings.Contains(params.Name, "__") {
				return errorResult("工具名称不能包含 \"__\"（双下划线为外部 MCP 工具保留前缀）"), nil
			}
			if router.IsKnownHostTool(params.Name) {
				return errorResult(fmt.Sprintf("不能覆盖内置工具: %s", params.Name)), nil
			}
			if params.Type != "shell" && params.Type != "http" {
				return errorResult("工具类型必须是 shell 或 http"), nil
			}
			if params.Type == "shell" && params.Command == "" {
				return errorResult("shell 类型工具必须提供 command"), nil
			}
			if params.Type == "http" && params.URL == "" {
				return errorResult("http 类型工具必须提供 url"), nil
			}

			// HITL 审批：如果配置要求审批，需要用户确认
			if cfg != nil && cfg.Tools.CreateRequiresApproval {
				if approvalBridge == nil {
					// CLI 模式下无审批通道：shell 类型直接拒绝（安全风险高），http 类型放行（已有 SSRF 防护）
					if params.Type == "shell" {
						return errorResult("CLI 模式下禁止动态创建 shell 类型工具（无 HITL 审批通道）"), nil
					}
				} else {
					details := map[string]string{
						"name": params.Name,
						"type": params.Type,
					}
					if params.Type == "shell" {
						details["command"] = params.Command
					} else {
						details["url"] = params.URL
						details["method"] = params.Method
					}
					approvalCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
					defer cancel()
					approved, err := approvalBridge.RequestApproval(approvalCtx, params.Name,
						fmt.Sprintf("创建 %s 类型工具 [%s]", params.Type, params.Name), details)
					if err != nil {
						return errorResult("审批请求失败: " + err.Error()), nil
					}
					if !approved {
						return errorResult(fmt.Sprintf("用户拒绝创建工具: %s", params.Name)), nil
					}
				}
			}

			// 构建 CustomTool
			tool := CustomTool{
				Name:        params.Name,
				Description: params.Description,
				Type:        params.Type,
				Command:     params.Command,
				URL:         params.URL,
				Method:      params.Method,
				Headers:     params.Headers,
				Parameters:  params.Parameters,
				AllowWrite:  params.AllowWrite,
				Timeout:     params.Timeout,
			}
			if tool.Timeout <= 0 {
				tool.Timeout = 30
			}

			// 注册（复用已有的 RegisterCustomTool）
			if err := RegisterCustomTool(host, logger, tool); err != nil {
				return errorResult("注册工具失败: " + err.Error()), nil
			}

			// 持久化到磁盘（失败不影响内存中的工具）
			persistCustomTool(customToolsDir, tool, logger)

			logger.Info("运行时创建工具",
				zap.String("name", tool.Name),
				zap.String("type", tool.Type),
			)

			return textResult(fmt.Sprintf("工具 %s 已创建并立即可用（类型: %s）", tool.Name, tool.Type)), nil
		},
	)
}

// registerRemoveTool 注册 remove_tool 到 MCP host
func registerRemoveTool(host *mcphost.Host, logger *zap.Logger, customToolsDir string) {
	schema, _ := json.Marshal(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"name": map[string]any{
				"type":        "string",
				"description": "要删除的工具名称",
			},
		},
		"required": []string{"name"},
	})

	host.RegisterTool(
		mcphost.ToolDefinition{
			Name:        "remove_tool",
			Description: "删除运行时动态创建的自定义工具。不能删除内置工具。",
			InputSchema: schema,
		},
		func(ctx context.Context, input json.RawMessage) (*mcphost.ToolResult, error) {
			var params removeToolInput
			if err := json.Unmarshal(input, &params); err != nil {
				return errorResult("参数解析失败: " + err.Error()), nil
			}

			if params.Name == "" {
				return errorResult("工具名称不能为空"), nil
			}
			if router.IsKnownHostTool(params.Name) {
				return errorResult(fmt.Sprintf("不能删除内置工具: %s", params.Name)), nil
			}

			if err := host.UnregisterTool(params.Name); err != nil {
				return errorResult("删除工具失败: " + err.Error()), nil
			}

			// 从磁盘删除持久化文件（失败不影响内存中的删除）
			removeCustomToolFile(customToolsDir, params.Name, logger)

			logger.Info("运行时删除工具", zap.String("name", params.Name))
			return textResult(fmt.Sprintf("工具 %s 已删除", params.Name)), nil
		},
	)
}
