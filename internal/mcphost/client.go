package mcphost

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"go.uber.org/zap"

	"github.com/chef-guo/agents-hive/internal/errs"
)

// mcpOperationTimeout MCP 操作统一超时，与 mcpOperationTimeout 保持一致
// 注意：因 import cycle 无法直接引用 config 包，修改时需同步更新
const mcpOperationTimeout = 30 * time.Second

// jsonRPCRequest JSON-RPC 2.0 请求
// ID 用指针：nil → omitempty 省略（通知），非 nil → 请求
type jsonRPCRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      *int64 `json:"id,omitempty"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

// jsonRPCResponse JSON-RPC 2.0 响应
type jsonRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int64           `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *jsonRPCError   `json:"error,omitempty"`
}

type jsonRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// toolsListResult tools/list 响应结构
type toolsListResult struct {
	Tools []remoteTool `json:"tools"`
}

type remoteTool struct {
	Name         string          `json:"name"`
	Description  string          `json:"description"`
	InputSchema  json.RawMessage `json:"inputSchema"`
	OutputSchema json.RawMessage `json:"outputSchema,omitempty"`
	Annotations  json.RawMessage `json:"annotations,omitempty"`
}

// RemoteMCPClient 远程 MCP 服务端客户端
// 负责通过 Transport 与远程 MCP 服务端通信，发现工具并注册到 Host
// 注意：Transport 的 Send+Receive 必须原子执行，transportMu 保护并发安全
type RemoteMCPClient struct {
	transport   Transport
	transportMu sync.Mutex // 保护 Send+Receive 原子性
	host        *Host
	serverName  string
	logger      *zap.Logger
	nextID      atomic.Int64
}

// NewRemoteMCPClient 创建远程 MCP 客户端
func NewRemoteMCPClient(transport Transport, host *Host, serverName string, logger *zap.Logger) *RemoteMCPClient {
	return &RemoteMCPClient{
		transport:  transport,
		host:       host,
		serverName: serverName,
		logger:     logger,
	}
}

// Name 返回服务端名称
func (c *RemoteMCPClient) Name() string {
	return c.serverName
}

// Connect 连接远程 MCP 服务端，发现工具并注册到 Host
// 完整流程：Connect → 读取 initialize 响应 → tools/list → 注册工具
func (c *RemoteMCPClient) Connect(ctx context.Context) error {
	// 1. 建立连接（Transport.Connect 内部会发送 initialize 请求）
	connectCtx, cancel := context.WithTimeout(ctx, mcpOperationTimeout)
	defer cancel()

	if err := c.transport.Connect(connectCtx); err != nil {
		return errs.Wrap(errs.CodeMCPTransportFailed, fmt.Sprintf("连接远程 MCP 服务端 %q 失败", c.serverName), err)
	}

	// 2. 读取 initialize 响应，再发送 initialized 通知；两步合为一个锁区间，避免并发交错
	c.transportMu.Lock()
	initResp, err := c.transport.Receive(connectCtx)
	if err != nil {
		c.transportMu.Unlock()
		return errs.Wrap(errs.CodeMCPTransportFailed, fmt.Sprintf("读取 %q 初始化响应失败", c.serverName), err)
	}
	c.logger.Debug("收到 MCP 初始化响应",
		zap.String("服务端", c.serverName),
		zap.String("响应", string(initResp)),
	)

	// 3. 发送 initialized 通知（MCP 协议要求，无响应）
	notif := jsonRPCRequest{
		JSONRPC: "2.0",
		Method:  "notifications/initialized",
	}
	notifBytes, err := json.Marshal(notif)
	if err != nil {
		c.logger.Warn("序列化 initialized 通知失败",
			zap.String("服务端", c.serverName),
			zap.Error(err),
		)
		notifBytes = []byte("{}")
	}
	if err := c.transport.Send(ctx, notifBytes); err != nil {
		c.logger.Warn("发送 initialized 通知失败（非致命）",
			zap.String("服务端", c.serverName),
			zap.Error(err),
		)
	}
	c.transportMu.Unlock()

	// 4. 发现并注册工具
	if err := c.discoverTools(ctx); err != nil {
		return err
	}

	// 5. 发现并注册资源（可选，服务端不支持时静默跳过）
	c.discoverResources(ctx)

	// 6. 发现并注册提示
	c.discoverPrompts(ctx)

	c.logger.Info("远程 MCP 服务端连接完成", zap.String("服务端", c.serverName))
	return nil
}

// discoverTools 发送 tools/list 请求，解析响应并注册工具到 Host
func (c *RemoteMCPClient) discoverTools(ctx context.Context) error {
	reqID := c.nextID.Add(1)
	req := jsonRPCRequest{
		JSONRPC: "2.0",
		ID:      &reqID,
		Method:  "tools/list",
	}
	reqBytes, err := json.Marshal(req)
	if err != nil {
		c.logger.Warn("序列化 tools/list 请求失败",
			zap.String("服务端", c.serverName),
			zap.Error(err),
		)
		reqBytes = []byte("{}")
	}

	// 统一使用 DefaultMCPTimeout，避免与 Connect 超时不一致
	listCtx, cancel := context.WithTimeout(ctx, mcpOperationTimeout)
	defer cancel()

	// 锁定 Transport 保证 Send+Receive 原子性
	c.transportMu.Lock()
	defer c.transportMu.Unlock()

	if err := c.transport.Send(listCtx, reqBytes); err != nil {
		return errs.Wrap(errs.CodeMCPTransportFailed, fmt.Sprintf("发送 tools/list 到 %q 失败", c.serverName), err)
	}

	respBytes, err := c.transport.Receive(listCtx)
	if err != nil {
		return errs.Wrap(errs.CodeMCPTransportFailed, fmt.Sprintf("读取 %q tools/list 响应失败", c.serverName), err)
	}

	var resp jsonRPCResponse
	if err := json.Unmarshal(respBytes, &resp); err != nil {
		return errs.Wrap(errs.CodeMCPResponseInvalid, fmt.Sprintf("解析 %q tools/list 响应失败", c.serverName), err)
	}
	if resp.Error != nil {
		return errs.New(errs.CodeMCPTransportFailed, fmt.Sprintf("远程 MCP 服务端 %q tools/list 错误: %s", c.serverName, resp.Error.Message))
	}

	var result toolsListResult
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		return errs.Wrap(errs.CodeMCPResponseInvalid, fmt.Sprintf("解析 %q 工具列表失败", c.serverName), err)
	}

	// 注册远程工具到 Host（工具名加服务端前缀，避免冲突）
	for _, tool := range result.Tools {
		toolName := fmt.Sprintf("%s__%s", c.serverName, tool.Name)
		def := ToolDefinition{
			Name:         toolName,
			Description:  fmt.Sprintf("[%s] %s", c.serverName, tool.Description),
			InputSchema:  tool.InputSchema,
			OutputSchema: tool.OutputSchema,
			SourceServer: c.serverName,
			Trusted:      true,
			Annotations:  tool.Annotations,
		}

		// 创建远程执行器闭包
		executor := c.makeRemoteExecutor(tool.Name)
		c.host.RegisterTool(def, executor)

		c.logger.Debug("已注册远程工具",
			zap.String("服务端", c.serverName),
			zap.String("工具", toolName),
		)
	}

	c.logger.Info("已从远程 MCP 服务端发现并注册工具",
		zap.String("服务端", c.serverName),
		zap.Int("工具数量", len(result.Tools)),
	)
	return nil
}

// resourcesListResult resources/list 响应结构
type resourcesListResult struct {
	Resources []remoteResource `json:"resources"`
}

type remoteResource struct {
	URI         string `json:"uri"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	MimeType    string `json:"mimeType,omitempty"`
}

// resourceReadResult resources/read 响应结构
type resourceReadResult struct {
	Contents []ResourceContent `json:"contents"`
}

// discoverResources 发送 resources/list 请求，解析响应并注册资源到 Host
// 如果远程服务端不支持 resources（返回 method not found），静默跳过
func (c *RemoteMCPClient) discoverResources(ctx context.Context) {
	reqID := c.nextID.Add(1)
	req := jsonRPCRequest{
		JSONRPC: "2.0",
		ID:      &reqID,
		Method:  "resources/list",
	}
	reqBytes, err := json.Marshal(req)
	if err != nil {
		c.logger.Warn("序列化 resources/list 请求失败",
			zap.String("服务端", c.serverName),
			zap.Error(err),
		)
		reqBytes = []byte("{}")
	}

	// 统一使用 DefaultMCPTimeout，避免与 Connect 超时不一致
	listCtx, cancel := context.WithTimeout(ctx, mcpOperationTimeout)
	defer cancel()

	// 锁定 Transport 保证 Send+Receive 原子性
	c.transportMu.Lock()
	defer c.transportMu.Unlock()

	if err := c.transport.Send(listCtx, reqBytes); err != nil {
		c.logger.Warn("发送 resources/list 失败，跳过资源发现",
			zap.String("服务端", c.serverName),
			zap.Error(err),
		)
		return
	}

	respBytes, err := c.transport.Receive(listCtx)
	if err != nil {
		c.logger.Warn("读取 resources/list 响应失败，跳过资源发现",
			zap.String("服务端", c.serverName),
			zap.Error(err),
		)
		return
	}

	var resp jsonRPCResponse
	if err := json.Unmarshal(respBytes, &resp); err != nil {
		c.logger.Warn("解析 resources/list 响应失败，跳过资源发现",
			zap.String("服务端", c.serverName),
			zap.Error(err),
		)
		return
	}

	// 如果服务端不支持 resources（-32601 = Method not found），静默跳过
	if resp.Error != nil {
		if resp.Error.Code == -32601 {
			c.logger.Debug("远程服务端不支持 resources，跳过",
				zap.String("服务端", c.serverName),
			)
		} else {
			c.logger.Warn("远程服务端 resources/list 返回错误，跳过资源发现",
				zap.String("服务端", c.serverName),
				zap.String("错误", resp.Error.Message),
			)
		}
		return
	}

	var result resourcesListResult
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		c.logger.Warn("解析资源列表失败，跳过资源发现",
			zap.String("服务端", c.serverName),
			zap.Error(err),
		)
		return
	}

	// 注册远程资源到 Host（资源 URI 加服务端前缀，避免冲突）
	for _, res := range result.Resources {
		prefixedURI := fmt.Sprintf("%s://%s", c.serverName, res.URI)
		def := ResourceDefinition{
			URI:         prefixedURI,
			Name:        res.Name,
			Description: res.Description,
			MimeType:    res.MimeType,
		}

		// 创建远程资源 provider 闭包
		originalURI := res.URI
		provider := func(ctx context.Context, uri string) (*ResourceContent, error) {
			return c.readRemoteResource(ctx, originalURI)
		}
		c.host.RegisterResource(def, provider)

		c.logger.Debug("已注册远程资源",
			zap.String("服务端", c.serverName),
			zap.String("资源", prefixedURI),
		)
	}

	c.logger.Info("已从远程 MCP 服务端发现并注册资源",
		zap.String("服务端", c.serverName),
		zap.Int("资源数量", len(result.Resources)),
	)
}

// readRemoteResource 发送 resources/read JSON-RPC 请求到远程服务端
func (c *RemoteMCPClient) readRemoteResource(ctx context.Context, uri string) (*ResourceContent, error) {
	reqID := c.nextID.Add(1)
	req := jsonRPCRequest{
		JSONRPC: "2.0",
		ID:      &reqID,
		Method:  "resources/read",
		Params: map[string]any{
			"uri": uri,
		},
	}
	reqBytes, err := json.Marshal(req)
	if err != nil {
		return nil, errs.Wrap(errs.CodeMCPResponseInvalid, fmt.Sprintf("序列化 resources/read 请求到 %q 失败", c.serverName), err)
	}

	// 锁定 Transport 保证 Send+Receive 原子性
	c.transportMu.Lock()
	defer c.transportMu.Unlock()

	if err := c.transport.Send(ctx, reqBytes); err != nil {
		return nil, errs.Wrap(errs.CodeMCPTransportFailed, fmt.Sprintf("发送 resources/read 到 %q 失败", c.serverName), err)
	}

	respBytes, err := c.transport.Receive(ctx)
	if err != nil {
		return nil, errs.Wrap(errs.CodeMCPTransportFailed, fmt.Sprintf("读取 %q resources/read 响应失败", c.serverName), err)
	}

	var resp jsonRPCResponse
	if err := json.Unmarshal(respBytes, &resp); err != nil {
		return nil, errs.Wrap(errs.CodeMCPResponseInvalid, "解析 resources/read 响应失败", err)
	}
	if resp.Error != nil {
		return nil, errs.New(errs.CodeMCPResourceNotFound, fmt.Sprintf("远程 MCP 服务端 %q resources/read 错误: %s", c.serverName, resp.Error.Message))
	}

	var result resourceReadResult
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		return nil, errs.Wrap(errs.CodeMCPResponseInvalid, fmt.Sprintf("解析 %q 资源内容失败", c.serverName), err)
	}
	if len(result.Contents) == 0 {
		return nil, errs.New(errs.CodeMCPResourceNotFound, fmt.Sprintf("远程资源 %q 返回空内容", uri))
	}
	return &result.Contents[0], nil
}

// promptsListResult prompts/list 响应结构
type promptsListResult struct {
	Prompts []remotePrompt `json:"prompts"`
}

type remotePrompt struct {
	Name        string           `json:"name"`
	Description string           `json:"description"`
	Arguments   []PromptArgument `json:"arguments"`
}

// promptsGetResult prompts/get 响应结构
type promptsGetResult struct {
	Messages []PromptMessage `json:"messages"`
}

// discoverPrompts 发送 prompts/list 请求，解析响应并注册提示到 Host
// 如果远程服务端不支持 prompts（返回 method not found），静默跳过
func (c *RemoteMCPClient) discoverPrompts(ctx context.Context) {
	reqID := c.nextID.Add(1)
	req := jsonRPCRequest{
		JSONRPC: "2.0",
		ID:      &reqID,
		Method:  "prompts/list",
	}
	reqBytes, err := json.Marshal(req)
	if err != nil {
		c.logger.Warn("序列化 prompts/list 请求失败",
			zap.String("服务端", c.serverName),
			zap.Error(err),
		)
		reqBytes = []byte("{}")
	}

	// 统一使用 DefaultMCPTimeout，避免与 Connect 超时不一致
	listCtx, cancel := context.WithTimeout(ctx, mcpOperationTimeout)
	defer cancel()

	// 锁定 Transport 保证 Send+Receive 原子性
	c.transportMu.Lock()
	defer c.transportMu.Unlock()

	if err := c.transport.Send(listCtx, reqBytes); err != nil {
		c.logger.Warn("发送 prompts/list 失败，跳过提示发现",
			zap.String("服务端", c.serverName),
			zap.Error(err),
		)
		return
	}

	respBytes, err := c.transport.Receive(listCtx)
	if err != nil {
		c.logger.Warn("读取 prompts/list 响应失败，跳过提示发现",
			zap.String("服务端", c.serverName),
			zap.Error(err),
		)
		return
	}

	var resp jsonRPCResponse
	if err := json.Unmarshal(respBytes, &resp); err != nil {
		c.logger.Warn("解析 prompts/list 响应失败，跳过提示发现",
			zap.String("服务端", c.serverName),
			zap.Error(err),
		)
		return
	}

	// 如果服务端不支持 prompts（-32601 = Method not found），静默跳过
	if resp.Error != nil {
		if resp.Error.Code == -32601 {
			c.logger.Debug("远程服务端不支持 prompts，跳过",
				zap.String("服务端", c.serverName),
			)
		} else {
			c.logger.Warn("远程服务端 prompts/list 返回错误，跳过提示发现",
				zap.String("服务端", c.serverName),
				zap.String("错误", resp.Error.Message),
			)
		}
		return
	}

	var result promptsListResult
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		c.logger.Warn("解析提示列表失败，跳过提示发现",
			zap.String("服务端", c.serverName),
			zap.Error(err),
		)
		return
	}

	// 注册远程提示到 Host（提示名加服务端前缀，避免冲突）
	for _, prompt := range result.Prompts {
		promptName := fmt.Sprintf("%s__%s", c.serverName, prompt.Name)
		def := PromptDefinition{
			Name:        promptName,
			Description: prompt.Description,
			Arguments:   prompt.Arguments,
		}

		// 创建远程提示执行器闭包
		executor := c.makeRemotePromptExecutor(prompt.Name)
		c.host.RegisterPrompt(def, executor)

		c.logger.Debug("已注册远程提示",
			zap.String("服务端", c.serverName),
			zap.String("提示", promptName),
		)
	}

	c.logger.Info("已从远程 MCP 服务端发现并注册提示",
		zap.String("服务端", c.serverName),
		zap.Int("提示数量", len(result.Prompts)),
	)
}

// makeRemotePromptExecutor 创建远程提示执行器
func (c *RemoteMCPClient) makeRemotePromptExecutor(promptName string) PromptExecutor {
	return func(ctx context.Context, args map[string]string) ([]PromptMessage, error) {
		reqID := c.nextID.Add(1)
		req := jsonRPCRequest{
			JSONRPC: "2.0",
			ID:      &reqID,
			Method:  "prompts/get",
			Params: map[string]any{
				"name":      promptName,
				"arguments": args,
			},
		}
		reqBytes, err := json.Marshal(req)
		if err != nil {
			return nil, errs.Wrap(errs.CodeMCPResponseInvalid, fmt.Sprintf("序列化 prompts/get 请求到 %q 失败", c.serverName), err)
		}

		// 锁定 Transport 保证 Send+Receive 原子性
		c.transportMu.Lock()
		defer c.transportMu.Unlock()

		if err := c.transport.Send(ctx, reqBytes); err != nil {
			return nil, errs.Wrap(errs.CodeMCPTransportFailed, fmt.Sprintf("发送 prompts/get 到 %q 失败", c.serverName), err)
		}

		respBytes, err := c.transport.Receive(ctx)
		if err != nil {
			return nil, errs.Wrap(errs.CodeMCPTransportFailed, fmt.Sprintf("读取 %q prompts/get 响应失败", c.serverName), err)
		}

		var resp jsonRPCResponse
		if err := json.Unmarshal(respBytes, &resp); err != nil {
			return nil, errs.Wrap(errs.CodeMCPResponseInvalid, "解析 prompts/get 响应失败", err)
		}
		if resp.Error != nil {
			return nil, errs.New(errs.CodeMCPPromptNotFound, fmt.Sprintf("远程 MCP 服务端 %q prompts/get 错误: %s", c.serverName, resp.Error.Message))
		}

		var result promptsGetResult
		if err := json.Unmarshal(resp.Result, &result); err != nil {
			return nil, errs.Wrap(errs.CodeMCPResponseInvalid, fmt.Sprintf("解析 %q 提示消息失败", c.serverName), err)
		}
		return result.Messages, nil
	}
}

// makeRemoteExecutor 创建远程工具执行器
func (c *RemoteMCPClient) makeRemoteExecutor(toolName string) ToolExecutor {
	return func(ctx context.Context, input json.RawMessage) (*ToolResult, error) {
		reqID := c.nextID.Add(1)
		req := jsonRPCRequest{
			JSONRPC: "2.0",
			ID:      &reqID,
			Method:  "tools/call",
			Params: map[string]any{
				"name":      toolName,
				"arguments": input,
			},
		}
		reqBytes, err := json.Marshal(req)
		if err != nil {
			return nil, errs.Wrap(errs.CodeMCPResponseInvalid, fmt.Sprintf("序列化 tools/call 请求到 %q 失败", c.serverName), err)
		}

		// 锁定 Transport 保证 Send+Receive 原子性
		c.transportMu.Lock()
		defer c.transportMu.Unlock()

		if err := c.transport.Send(ctx, reqBytes); err != nil {
			return nil, errs.Wrap(errs.CodeMCPToolExecFailed, fmt.Sprintf("发送 tools/call 到 %q 失败", c.serverName), err)
		}

		respBytes, err := c.transport.Receive(ctx)
		if err != nil {
			return nil, errs.Wrap(errs.CodeMCPToolExecFailed, fmt.Sprintf("读取 %q tools/call 响应失败", c.serverName), err)
		}

		var resp jsonRPCResponse
		if err := json.Unmarshal(respBytes, &resp); err != nil {
			return nil, errs.Wrap(errs.CodeMCPResponseInvalid, "解析 tools/call 响应失败", err)
		}
		if resp.Error != nil {
			// 安全编码错误消息，防止 JSON 注入
			errText, _ := json.Marshal(resp.Error.Message)
			return &ToolResult{
				Content: json.RawMessage(fmt.Sprintf(`[{"type":"text","text":%s}]`, errText)),
				IsError: true,
			}, nil
		}

		// MCP tools/call 响应的 result 已经是 ToolResult 格式
		var toolResult ToolResult
		if err := json.Unmarshal(resp.Result, &toolResult); err != nil {
			// 如果不符合 ToolResult 格式，把 result 直接包装为 content
			toolResult = ToolResult{
				Content: resp.Result,
			}
		}
		return &toolResult, nil
	}
}

// Close 关闭远程连接
func (c *RemoteMCPClient) Close() error {
	return c.transport.Close()
}

// ConnectRemoteMCP 便捷函数：创建客户端并连接远程 MCP 服务端
// 返回的 RemoteMCPClient 应在不再需要时调用 Close()
func ConnectRemoteMCP(ctx context.Context, transport Transport, host *Host, serverName string, logger *zap.Logger) (*RemoteMCPClient, error) {
	client := NewRemoteMCPClient(transport, host, serverName, logger)
	if err := client.Connect(ctx); err != nil {
		_ = transport.Close()
		return nil, err
	}
	return client, nil
}
