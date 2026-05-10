package mcphost

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/chef-guo/agents-hive/internal/errs"
)

// ToolDefinition defines an MCP tool.
type ToolDefinition struct {
	Name              string          `json:"name"`
	Description       string          `json:"description"`
	InputSchema       json.RawMessage `json:"inputSchema"`
	OutputSchema      json.RawMessage `json:"outputSchema,omitempty"`
	Core              bool            `json:"core,omitempty"`                // Core tools are shown in system prompt; all tools remain callable
	IsConcurrencySafe bool            `json:"is_concurrency_safe,omitempty"` // true = 可并发执行（只读无副作用工具）
}

// ToolResult is the result of a tool execution.
type ToolResult struct {
	Content              json.RawMessage `json:"content"`
	IsError              bool            `json:"isError,omitempty"`
	FailureType          string          `json:"failureType,omitempty"`
	RequiresUserApproval bool            `json:"requiresUserApproval,omitempty"`
	SuggestedAction      string          `json:"suggestedAction,omitempty"`
}

// DecodeContent 解码 ToolResult.Content 为纯文本。
// 支持三种格式：
// 1. JSON 字符串: "hello" -> hello
// 2. MCP 格式: [{"type":"text","text":"hello"}] -> hello
// 3. 原始字节: 直接返回
func (r *ToolResult) DecodeContent() string {
	if r == nil || len(r.Content) == 0 {
		return ""
	}
	return DecodeToolContent(r.Content)
}

// DecodeToolContent 解码 json.RawMessage 格式的工具输出为纯文本。
// 支持三种格式：
// 1. JSON 字符串: "hello" -> hello
// 2. MCP 格式: [{"type":"text","text":"hello"}] -> 所有 text 块拼接（换行分隔）
// 3. 原始字节: 直接返回
func DecodeToolContent(raw json.RawMessage) string {
	// 尝试作为 JSON 字符串解析
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return text
	}

	// 尝试作为 MCP 格式解析，拼接所有 text 块
	var mcpContent []map[string]any
	if err := json.Unmarshal(raw, &mcpContent); err == nil {
		var parts []string
		for _, item := range mcpContent {
			if item["type"] == "text" {
				if t, ok := item["text"].(string); ok {
					parts = append(parts, t)
				}
			}
		}
		if len(parts) > 0 {
			return strings.Join(parts, "\n")
		}
	}

	// 降级：返回原始字符串
	return string(raw)
}

// ToolExecutor is a function that executes a tool.
type ToolExecutor func(ctx context.Context, input json.RawMessage) (*ToolResult, error)

// Host manages MCP tool discovery and execution.
type Host struct {
	mu           sync.RWMutex
	tools        map[string]*registeredTool
	resources    map[string]*resourceEntry
	prompts      map[string]*promptEntry
	toolChangeCh chan struct{}
	logger       *zap.Logger

	// hitlEmitter 为 nil 时 EmitInputRequest 返回 ErrHITLEmitterNotConfigured。
	// 在 bootstrap.InitServer 构造 Master 后通过 SetHITLEmitter 注入。详见 internal/mcphost/hitl.go。
	hitlEmitter HITLEmitter
}

type registeredTool struct {
	definition ToolDefinition
	executor   ToolExecutor
}

// NewHost creates a new MCP Host.
func NewHost(logger *zap.Logger) *Host {
	return &Host{
		tools:        make(map[string]*registeredTool),
		resources:    make(map[string]*resourceEntry),
		prompts:      make(map[string]*promptEntry),
		toolChangeCh: make(chan struct{}, 8),
		logger:       logger,
	}
}

// RegisterTool registers a tool with its executor.
func (h *Host) RegisterTool(def ToolDefinition, exec ToolExecutor) {
	h.mu.Lock()
	h.tools[def.Name] = &registeredTool{definition: def, executor: exec}
	h.mu.Unlock()
	h.logger.Info("已注册 MCP 工具", zap.String("name", def.Name))
	h.notifyToolChange()
}

// UnregisterTool removes a tool.
func (h *Host) UnregisterTool(name string) error {
	h.mu.Lock()
	if _, ok := h.tools[name]; !ok {
		h.mu.Unlock()
		return errs.New(errs.CodeMCPToolNotFound, fmt.Sprintf("tool %q not found", name))
	}
	delete(h.tools, name)
	h.mu.Unlock()
	h.logger.Info("已注销 MCP 工具", zap.String("name", name))
	h.notifyToolChange()
	return nil
}

// ExecuteTool runs a registered tool by name.
func (h *Host) ExecuteTool(ctx context.Context, name string, input json.RawMessage) (*ToolResult, error) {
	h.mu.RLock()
	tool, ok := h.tools[name]
	h.mu.RUnlock()

	if !ok {
		h.logger.Warn("工具未注册", zap.String("tool", name))
		return nil, errs.New(errs.CodeMCPToolNotFound, fmt.Sprintf("tool %q not found", name))
	}

	h.logger.Info("执行工具", zap.String("tool", name))
	start := time.Now()
	result, err := tool.executor(ctx, input)
	duration := time.Since(start)
	if err != nil {
		h.logger.Error("工具执行失败",
			zap.String("tool", name),
			zap.Duration("duration", duration),
			zap.Error(err),
		)
		return nil, errs.Wrap(errs.CodeMCPToolExecFailed, fmt.Sprintf("tool %q execution failed", name), err)
	}
	resultBytes := 0
	if result != nil {
		resultBytes = len(result.Content)
	}
	h.logger.Info("工具执行完成",
		zap.String("tool", name),
		zap.Duration("duration", duration),
		zap.Int("result_bytes", resultBytes),
	)
	return result, nil
}

// ListTools returns all registered tool definitions.
func (h *Host) ListTools() []ToolDefinition {
	h.mu.RLock()
	defer h.mu.RUnlock()
	defs := make([]ToolDefinition, 0, len(h.tools))
	for _, t := range h.tools {
		defs = append(defs, t.definition)
	}
	return defs
}

// ListCoreTools returns only tool definitions with Core: true.
func (h *Host) ListCoreTools() []ToolDefinition {
	h.mu.RLock()
	defer h.mu.RUnlock()
	defs := make([]ToolDefinition, 0)
	for _, t := range h.tools {
		if t.definition.Core {
			defs = append(defs, t.definition)
		}
	}
	return defs
}

// GetTool returns a tool definition by name.
func (h *Host) GetTool(name string) (*ToolDefinition, error) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	tool, ok := h.tools[name]
	if !ok {
		return nil, errs.New(errs.CodeMCPToolNotFound, fmt.Sprintf("tool %q not found", name))
	}
	return &tool.definition, nil
}

// UpdateToolDefinition 更新已注册工具的定义（描述和 schema），保留执行器不变
func (h *Host) UpdateToolDefinition(name string, description string, inputSchema json.RawMessage) error {
	h.mu.Lock()
	tool, ok := h.tools[name]
	if !ok {
		h.mu.Unlock()
		return errs.New(errs.CodeMCPToolNotFound, fmt.Sprintf("tool %q not found", name))
	}
	if description != "" {
		tool.definition.Description = description
	}
	if inputSchema != nil {
		tool.definition.InputSchema = inputSchema
	}
	h.mu.Unlock()
	h.notifyToolChange()
	return nil
}

// ExecuteToolFiltered runs a tool only if it passes the allowed-tools check.
// The allowedTools map contains tool names that are permitted. If empty, all tools are allowed.
func (h *Host) ExecuteToolFiltered(ctx context.Context, name string, input json.RawMessage, allowedTools map[string]bool) (*ToolResult, error) {
	if len(allowedTools) > 0 && !allowedTools[name] {
		return nil, errs.New(errs.CodeMCPToolNotFound, fmt.Sprintf("tool %q is not in allowed-tools list", name))
	}
	return h.ExecuteTool(ctx, name, input)
}

// notifyToolChange 发送工具列表变更通知（非阻塞）
func (h *Host) notifyToolChange() {
	select {
	case h.toolChangeCh <- struct{}{}:
	default:
	}
}

// OnToolListChanged 返回工具列表变更通知 channel
func (h *Host) OnToolListChanged() <-chan struct{} {
	return h.toolChangeCh
}

// RegisterResource 注册 MCP 资源
func (h *Host) RegisterResource(def ResourceDefinition, provider ResourceProvider) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.resources[def.URI] = &resourceEntry{def: def, provider: provider}
	h.logger.Info("已注册 MCP 资源", zap.String("uri", def.URI), zap.String("name", def.Name))
}

// ReadResource 读取 MCP 资源
func (h *Host) ReadResource(ctx context.Context, uri string) (*ResourceContent, error) {
	h.mu.RLock()
	entry, ok := h.resources[uri]
	h.mu.RUnlock()

	if !ok {
		return nil, errs.New(errs.CodeMCPResourceNotFound, fmt.Sprintf("资源 %q 未找到", uri))
	}

	content, err := entry.provider(ctx, uri)
	if err != nil {
		return nil, errs.Wrap(errs.CodeMCPResourceNotFound, fmt.Sprintf("读取资源 %q 失败", uri), err)
	}
	return content, nil
}

// ListResources 列出所有已注册的 MCP 资源
func (h *Host) ListResources() []ResourceDefinition {
	h.mu.RLock()
	defer h.mu.RUnlock()
	defs := make([]ResourceDefinition, 0, len(h.resources))
	for _, r := range h.resources {
		defs = append(defs, r.def)
	}
	return defs
}

// RegisterPrompt 注册 MCP 提示模板
func (h *Host) RegisterPrompt(def PromptDefinition, executor PromptExecutor) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.prompts[def.Name] = &promptEntry{def: def, executor: executor}
	h.logger.Info("已注册 MCP 提示", zap.String("name", def.Name))
}

// GetPrompt 获取并执行 MCP 提示
func (h *Host) GetPrompt(ctx context.Context, name string, args map[string]string) ([]PromptMessage, error) {
	h.mu.RLock()
	entry, ok := h.prompts[name]
	h.mu.RUnlock()

	if !ok {
		return nil, errs.New(errs.CodeMCPPromptNotFound, fmt.Sprintf("提示 %q 未找到", name))
	}

	// 验证必需参数
	for _, arg := range entry.def.Arguments {
		if arg.Required {
			if _, exists := args[arg.Name]; !exists {
				return nil, errs.New(errs.CodeInvalidInput, fmt.Sprintf("提示 %q 缺少必需参数 %q", name, arg.Name))
			}
		}
	}

	messages, err := entry.executor(ctx, args)
	if err != nil {
		return nil, errs.Wrap(errs.CodeMCPPromptNotFound, fmt.Sprintf("执行提示 %q 失败", name), err)
	}
	return messages, nil
}

// ListPrompts 列出所有已注册的 MCP 提示
func (h *Host) ListPrompts() []PromptDefinition {
	h.mu.RLock()
	defer h.mu.RUnlock()
	defs := make([]PromptDefinition, 0, len(h.prompts))
	for _, p := range h.prompts {
		defs = append(defs, p.def)
	}
	return defs
}
