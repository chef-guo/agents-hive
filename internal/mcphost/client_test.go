package mcphost

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"go.uber.org/zap"
)

// mockTransport 模拟 MCP Transport 用于测试
type mockTransport struct {
	mu        sync.Mutex
	connected bool
	closed    bool
	sent      []json.RawMessage
	recvQueue []json.RawMessage
	recvIdx   int
}

func (m *mockTransport) Connect(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.connected = true
	return nil
}

func (m *mockTransport) Send(ctx context.Context, msg json.RawMessage) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sent = append(m.sent, msg)
	return nil
}

func (m *mockTransport) Receive(ctx context.Context) (json.RawMessage, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.recvIdx >= len(m.recvQueue) {
		// 阻塞直到 context 取消
		m.mu.Unlock()
		<-ctx.Done()
		m.mu.Lock()
		return nil, ctx.Err()
	}
	msg := m.recvQueue[m.recvIdx]
	m.recvIdx++
	return msg, nil
}

func (m *mockTransport) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed = true
	return nil
}

func TestConnectRemoteMCP(t *testing.T) {
	logger := zap.NewNop()
	host := NewHost(logger)

	// 模拟服务端响应序列：
	// 1. initialize 响应
	// 2. initialized 通知的 "空响应"（通知无响应，但 Send 不阻塞）
	// 3. tools/list 响应
	transport := &mockTransport{
		recvQueue: []json.RawMessage{
			// initialize 响应
			json.RawMessage(`{"jsonrpc":"2.0","id":0,"result":{"protocolVersion":"2024-11-05","serverInfo":{"name":"test-server","version":"1.0"},"capabilities":{"tools":{}}}}`),
			// tools/list 响应
			json.RawMessage(`{"jsonrpc":"2.0","id":1,"result":{"tools":[{"name":"echo","description":"回显输入","inputSchema":{"type":"object","properties":{"text":{"type":"string"}}}}]}}`),
			// resources/list 响应（不支持）
			json.RawMessage(`{"jsonrpc":"2.0","id":2,"error":{"code":-32601,"message":"Method not found"}}`),
			// prompts/list 响应（不支持）
			json.RawMessage(`{"jsonrpc":"2.0","id":3,"error":{"code":-32601,"message":"Method not found"}}`),
		},
	}

	client, err := ConnectRemoteMCP(context.Background(), transport, host, "test-server", logger)
	if err != nil {
		t.Fatalf("ConnectRemoteMCP 失败: %v", err)
	}
	defer client.Close()

	// 验证传输层已连接
	if !transport.connected {
		t.Error("传输层未连接")
	}

	// 验证远程工具已注册到 Host（带服务端前缀）
	tools := host.ListTools()
	if len(tools) != 1 {
		t.Fatalf("期望注册 1 个工具，实际 %d", len(tools))
	}
	if tools[0].Name != "test-server__echo" {
		t.Errorf("工具名期望 %q，实际 %q", "test-server__echo", tools[0].Name)
	}
	expectedDesc := "[test-server] 回显输入"
	if tools[0].Description != expectedDesc {
		t.Errorf("工具描述期望 %q，实际 %q", expectedDesc, tools[0].Description)
	}
	if len(tools[0].OutputSchema) != 0 {
		t.Errorf("未提供 outputSchema 时应为空，实际 %s", tools[0].OutputSchema)
	}
}

func TestConnectRemoteMCPPreservesOutputSchema(t *testing.T) {
	logger := zap.NewNop()
	host := NewHost(logger)

	transport := &mockTransport{
		recvQueue: []json.RawMessage{
			json.RawMessage(`{"jsonrpc":"2.0","id":0,"result":{"protocolVersion":"2024-11-05","serverInfo":{"name":"test-server","version":"1.0"},"capabilities":{"tools":{}}}}`),
			json.RawMessage(`{"jsonrpc":"2.0","id":1,"result":{"tools":[{"name":"echo","description":"回显输入","inputSchema":{"type":"object","properties":{"text":{"type":"string"}}},"outputSchema":{"type":"object","required":["content"]}}]}}`),
			json.RawMessage(`{"jsonrpc":"2.0","id":2,"error":{"code":-32601,"message":"Method not found"}}`),
			json.RawMessage(`{"jsonrpc":"2.0","id":3,"error":{"code":-32601,"message":"Method not found"}}`),
		},
	}

	client, err := ConnectRemoteMCP(context.Background(), transport, host, "test-server", logger)
	if err != nil {
		t.Fatalf("ConnectRemoteMCP 失败: %v", err)
	}
	defer client.Close()

	def, err := host.GetTool("test-server__echo")
	if err != nil {
		t.Fatalf("GetTool 失败: %v", err)
	}
	if string(def.InputSchema) != `{"type":"object","properties":{"text":{"type":"string"}}}` {
		t.Fatalf("inputSchema 期望保留，实际 %s", def.InputSchema)
	}
	if string(def.OutputSchema) != `{"type":"object","required":["content"]}` {
		t.Fatalf("outputSchema 期望保留，实际 %s", def.OutputSchema)
	}
}

func TestRemoteToolExecution(t *testing.T) {
	logger := zap.NewNop()
	host := NewHost(logger)

	transport := &mockTransport{
		recvQueue: []json.RawMessage{
			// initialize 响应
			json.RawMessage(`{"jsonrpc":"2.0","id":0,"result":{"protocolVersion":"2024-11-05","serverInfo":{"name":"test"}}}`),
			// tools/list 响应
			json.RawMessage(`{"jsonrpc":"2.0","id":1,"result":{"tools":[{"name":"greet","description":"打招呼","inputSchema":{"type":"object"}}]}}`),
			// resources/list 响应（不支持）
			json.RawMessage(`{"jsonrpc":"2.0","id":2,"error":{"code":-32601,"message":"Method not found"}}`),
			// prompts/list 响应（不支持）
			json.RawMessage(`{"jsonrpc":"2.0","id":3,"error":{"code":-32601,"message":"Method not found"}}`),
		},
	}

	client, err := ConnectRemoteMCP(context.Background(), transport, host, "remote", logger)
	if err != nil {
		t.Fatalf("连接失败: %v", err)
	}
	defer client.Close()

	// 添加 tools/call 响应到队列
	transport.mu.Lock()
	transport.recvQueue = append(transport.recvQueue,
		json.RawMessage(`{"jsonrpc":"2.0","id":4,"result":{"content":[{"type":"text","text":"Hello, World!"}]}}`),
	)
	transport.mu.Unlock()

	// 通过 Host 执行远程工具
	result, err := host.ExecuteTool(context.Background(), "remote__greet", json.RawMessage(`{"name":"World"}`))
	if err != nil {
		t.Fatalf("执行远程工具失败: %v", err)
	}

	if result.IsError {
		t.Error("工具执行返回错误")
	}
}

func TestConnectRemoteMCPToolsListError(t *testing.T) {
	logger := zap.NewNop()
	host := NewHost(logger)

	transport := &mockTransport{
		recvQueue: []json.RawMessage{
			// initialize 响应
			json.RawMessage(`{"jsonrpc":"2.0","id":0,"result":{"protocolVersion":"2024-11-05"}}`),
			// tools/list 错误响应
			json.RawMessage(`{"jsonrpc":"2.0","id":1,"error":{"code":-32601,"message":"Method not found"}}`),
		},
	}

	_, err := ConnectRemoteMCP(context.Background(), transport, host, "bad-server", logger)
	if err == nil {
		t.Fatal("期望连接失败")
	}

	// 验证没有工具被注册
	if len(host.ListTools()) != 0 {
		t.Error("错误时不应注册任何工具")
	}
}

func TestDiscoverResourcesFromRemote(t *testing.T) {
	logger := zap.NewNop()
	host := NewHost(logger)

	transport := &mockTransport{
		recvQueue: []json.RawMessage{
			// initialize 响应
			json.RawMessage(`{"jsonrpc":"2.0","id":0,"result":{"protocolVersion":"2024-11-05","serverInfo":{"name":"res-server"}}}`),
			// tools/list 响应（空工具列表）
			json.RawMessage(`{"jsonrpc":"2.0","id":1,"result":{"tools":[]}}`),
			// resources/list 响应（包含两个资源）
			json.RawMessage(`{"jsonrpc":"2.0","id":2,"result":{"resources":[{"uri":"file:///docs/readme.md","name":"README","description":"项目说明文档","mimeType":"text/markdown"},{"uri":"config://app","name":"应用配置","description":"应用运行时配置"}]}}`),
			// prompts/list 响应（不支持）
			json.RawMessage(`{"jsonrpc":"2.0","id":3,"error":{"code":-32601,"message":"Method not found"}}`),
		},
	}

	client, err := ConnectRemoteMCP(context.Background(), transport, host, "res-server", logger)
	if err != nil {
		t.Fatalf("ConnectRemoteMCP 失败: %v", err)
	}
	defer client.Close()

	// 验证远程资源已注册到 Host（带服务端前缀）
	resources := host.ListResources()
	if len(resources) != 2 {
		t.Fatalf("期望注册 2 个资源，实际 %d", len(resources))
	}

	// 按 URI 查找资源（map 顺序不确定）
	resourceMap := make(map[string]ResourceDefinition)
	for _, r := range resources {
		resourceMap[r.URI] = r
	}

	readmeRes, ok := resourceMap["res-server://file:///docs/readme.md"]
	if !ok {
		t.Fatal("未找到 res-server://file:///docs/readme.md 资源")
	}
	if readmeRes.Name != "README" {
		t.Errorf("资源名期望 %q，实际 %q", "README", readmeRes.Name)
	}
	if readmeRes.Description != "项目说明文档" {
		t.Errorf("资源描述期望 %q，实际 %q", "项目说明文档", readmeRes.Description)
	}
	if readmeRes.MimeType != "text/markdown" {
		t.Errorf("资源 MIME 类型期望 %q，实际 %q", "text/markdown", readmeRes.MimeType)
	}

	configRes, ok := resourceMap["res-server://config://app"]
	if !ok {
		t.Fatal("未找到 res-server://config://app 资源")
	}
	if configRes.Name != "应用配置" {
		t.Errorf("资源名期望 %q，实际 %q", "应用配置", configRes.Name)
	}
}

func TestDiscoverResourcesMethodNotFound(t *testing.T) {
	logger := zap.NewNop()
	host := NewHost(logger)

	transport := &mockTransport{
		recvQueue: []json.RawMessage{
			// initialize 响应
			json.RawMessage(`{"jsonrpc":"2.0","id":0,"result":{"protocolVersion":"2024-11-05"}}`),
			// tools/list 响应（空工具列表）
			json.RawMessage(`{"jsonrpc":"2.0","id":1,"result":{"tools":[]}}`),
			// resources/list 响应（不支持）
			json.RawMessage(`{"jsonrpc":"2.0","id":2,"error":{"code":-32601,"message":"Method not found"}}`),
			// prompts/list 响应（不支持）
			json.RawMessage(`{"jsonrpc":"2.0","id":3,"error":{"code":-32601,"message":"Method not found"}}`),
		},
	}

	client, err := ConnectRemoteMCP(context.Background(), transport, host, "no-res-server", logger)
	if err != nil {
		t.Fatalf("ConnectRemoteMCP 失败（不应因资源不支持而失败）: %v", err)
	}
	defer client.Close()

	// 验证没有资源被注册
	if len(host.ListResources()) != 0 {
		t.Error("服务端不支持 resources 时不应注册任何资源")
	}
}

func TestRemoteResourceRead(t *testing.T) {
	logger := zap.NewNop()
	host := NewHost(logger)

	transport := &mockTransport{
		recvQueue: []json.RawMessage{
			// initialize 响应
			json.RawMessage(`{"jsonrpc":"2.0","id":0,"result":{"protocolVersion":"2024-11-05"}}`),
			// tools/list 响应（空工具列表）
			json.RawMessage(`{"jsonrpc":"2.0","id":1,"result":{"tools":[]}}`),
			// resources/list 响应
			json.RawMessage(`{"jsonrpc":"2.0","id":2,"result":{"resources":[{"uri":"data://test","name":"测试数据","mimeType":"application/json"}]}}`),
			// prompts/list 响应（不支持）
			json.RawMessage(`{"jsonrpc":"2.0","id":3,"error":{"code":-32601,"message":"Method not found"}}`),
		},
	}

	client, err := ConnectRemoteMCP(context.Background(), transport, host, "data-server", logger)
	if err != nil {
		t.Fatalf("ConnectRemoteMCP 失败: %v", err)
	}
	defer client.Close()

	// 添加 resources/read 响应到队列
	transport.mu.Lock()
	transport.recvQueue = append(transport.recvQueue,
		json.RawMessage(`{"jsonrpc":"2.0","id":4,"result":{"contents":[{"uri":"data://test","mimeType":"application/json","text":"{\"key\":\"value\"}"}]}}`),
	)
	transport.mu.Unlock()

	// 通过 Host 读取远程资源
	content, err := host.ReadResource(context.Background(), "data-server://data://test")
	if err != nil {
		t.Fatalf("读取远程资源失败: %v", err)
	}

	if content.MimeType != "application/json" {
		t.Errorf("MIME 类型期望 %q，实际 %q", "application/json", content.MimeType)
	}
	if content.Text != `{"key":"value"}` {
		t.Errorf("资源内容期望 %q，实际 %q", `{"key":"value"}`, content.Text)
	}
}

func TestDiscoverPrompts(t *testing.T) {
	logger := zap.NewNop()
	host := NewHost(logger)

	// 模拟服务端响应序列：支持 prompts
	transport := &mockTransport{
		recvQueue: []json.RawMessage{
			// initialize 响应
			json.RawMessage(`{"jsonrpc":"2.0","id":0,"result":{"protocolVersion":"2024-11-05","serverInfo":{"name":"prompt-server"}}}`),
			// tools/list 响应（无工具）
			json.RawMessage(`{"jsonrpc":"2.0","id":1,"result":{"tools":[]}}`),
			// resources/list 响应（不支持）
			json.RawMessage(`{"jsonrpc":"2.0","id":2,"error":{"code":-32601,"message":"Method not found"}}`),
			// prompts/list 响应（有两个提示）
			json.RawMessage(`{"jsonrpc":"2.0","id":3,"result":{"prompts":[{"name":"summarize","description":"总结文本","arguments":[{"name":"text","description":"要总结的文本","required":true}]},{"name":"translate","description":"翻译文本","arguments":[{"name":"text","required":true},{"name":"language","required":true}]}]}}`),
		},
	}

	client, err := ConnectRemoteMCP(context.Background(), transport, host, "prompt-server", logger)
	if err != nil {
		t.Fatalf("ConnectRemoteMCP 失败: %v", err)
	}
	defer client.Close()

	// 验证远程提示已注册到 Host（带服务端前缀）
	prompts := host.ListPrompts()
	if len(prompts) != 2 {
		t.Fatalf("期望注册 2 个提示，实际 %d", len(prompts))
	}

	// 检查提示名称（顺序可能不固定，使用 map 验证）
	promptMap := make(map[string]PromptDefinition)
	for _, p := range prompts {
		promptMap[p.Name] = p
	}

	summarize, ok := promptMap["prompt-server__summarize"]
	if !ok {
		t.Fatal("未找到提示 prompt-server__summarize")
	}
	if summarize.Description != "总结文本" {
		t.Errorf("提示描述期望 %q，实际 %q", "总结文本", summarize.Description)
	}
	if len(summarize.Arguments) != 1 {
		t.Errorf("期望 1 个参数，实际 %d", len(summarize.Arguments))
	}

	translate, ok := promptMap["prompt-server__translate"]
	if !ok {
		t.Fatal("未找到提示 prompt-server__translate")
	}
	if len(translate.Arguments) != 2 {
		t.Errorf("期望 2 个参数，实际 %d", len(translate.Arguments))
	}
}

func TestDiscoverPromptsMethodNotFound(t *testing.T) {
	logger := zap.NewNop()
	host := NewHost(logger)

	// 模拟服务端不支持 prompts
	transport := &mockTransport{
		recvQueue: []json.RawMessage{
			// initialize 响应
			json.RawMessage(`{"jsonrpc":"2.0","id":0,"result":{"protocolVersion":"2024-11-05"}}`),
			// tools/list 响应
			json.RawMessage(`{"jsonrpc":"2.0","id":1,"result":{"tools":[]}}`),
			// resources/list 响应（不支持）
			json.RawMessage(`{"jsonrpc":"2.0","id":2,"error":{"code":-32601,"message":"Method not found"}}`),
			// prompts/list 响应（不支持）
			json.RawMessage(`{"jsonrpc":"2.0","id":3,"error":{"code":-32601,"message":"Method not found"}}`),
		},
	}

	client, err := ConnectRemoteMCP(context.Background(), transport, host, "no-prompts", logger)
	if err != nil {
		t.Fatalf("ConnectRemoteMCP 不应失败: %v", err)
	}
	defer client.Close()

	// 验证没有提示被注册（服务端不支持时静默跳过）
	prompts := host.ListPrompts()
	if len(prompts) != 0 {
		t.Errorf("服务端不支持 prompts 时不应注册任何提示，实际 %d", len(prompts))
	}
}

func TestRemotePromptExecution(t *testing.T) {
	logger := zap.NewNop()
	host := NewHost(logger)

	transport := &mockTransport{
		recvQueue: []json.RawMessage{
			// initialize 响应
			json.RawMessage(`{"jsonrpc":"2.0","id":0,"result":{"protocolVersion":"2024-11-05"}}`),
			// tools/list 响应
			json.RawMessage(`{"jsonrpc":"2.0","id":1,"result":{"tools":[]}}`),
			// resources/list 响应（不支持）
			json.RawMessage(`{"jsonrpc":"2.0","id":2,"error":{"code":-32601,"message":"Method not found"}}`),
			// prompts/list 响应
			json.RawMessage(`{"jsonrpc":"2.0","id":3,"result":{"prompts":[{"name":"greet","description":"生成问候语","arguments":[{"name":"name","required":true}]}]}}`),
		},
	}

	client, err := ConnectRemoteMCP(context.Background(), transport, host, "remote", logger)
	if err != nil {
		t.Fatalf("连接失败: %v", err)
	}
	defer client.Close()

	// 添加 prompts/get 响应到队列
	transport.mu.Lock()
	transport.recvQueue = append(transport.recvQueue,
		json.RawMessage(`{"jsonrpc":"2.0","id":4,"result":{"messages":[{"role":"user","content":"你好，张三！"}]}}`),
	)
	transport.mu.Unlock()

	// 通过 Host 执行远程提示
	messages, err := host.GetPrompt(context.Background(), "remote__greet", map[string]string{"name": "张三"})
	if err != nil {
		t.Fatalf("执行远程提示失败: %v", err)
	}

	if len(messages) != 1 {
		t.Fatalf("期望 1 条消息，实际 %d", len(messages))
	}
	if messages[0].Role != "user" {
		t.Errorf("消息角色期望 %q，实际 %q", "user", messages[0].Role)
	}
	if messages[0].Content != "你好，张三！" {
		t.Errorf("消息内容期望 %q，实际 %q", "你好，张三！", messages[0].Content)
	}

	// 验证发送的 prompts/get 请求格式正确
	transport.mu.Lock()
	defer transport.mu.Unlock()

	lastSent := transport.sent[len(transport.sent)-1]
	var req jsonRPCRequest
	if err := json.Unmarshal(lastSent, &req); err != nil {
		t.Fatalf("解析发送的请求失败: %v", err)
	}
	if req.Method != "prompts/get" {
		t.Errorf("请求方法期望 %q，实际 %q", "prompts/get", req.Method)
	}
}

func TestBuiltinPrompts(t *testing.T) {
	logger := zap.NewNop()
	host := NewHost(logger)

	RegisterBuiltinPrompts(host, logger)

	// 验证内置提示已注册
	prompts := host.ListPrompts()
	if len(prompts) != 3 {
		t.Fatalf("期望注册 3 个内置提示，实际 %d", len(prompts))
	}

	promptMap := make(map[string]PromptDefinition)
	for _, p := range prompts {
		promptMap[p.Name] = p
	}

	// 验证 summarize
	if _, ok := promptMap["summarize"]; !ok {
		t.Error("未找到内置提示 summarize")
	}

	// 验证 translate
	if _, ok := promptMap["translate"]; !ok {
		t.Error("未找到内置提示 translate")
	}

	// 验证 code-review
	if _, ok := promptMap["code-review"]; !ok {
		t.Error("未找到内置提示 code-review")
	}

	// 测试 summarize 执行
	msgs, err := host.GetPrompt(context.Background(), "summarize", map[string]string{"text": "这是一段测试文本"})
	if err != nil {
		t.Fatalf("执行 summarize 提示失败: %v", err)
	}
	if len(msgs) != 1 || msgs[0].Role != "user" {
		t.Error("summarize 返回格式不正确")
	}

	// 测试 translate 执行
	msgs, err = host.GetPrompt(context.Background(), "translate", map[string]string{"text": "Hello", "language": "中文"})
	if err != nil {
		t.Fatalf("执行 translate 提示失败: %v", err)
	}
	if len(msgs) != 1 || msgs[0].Role != "user" {
		t.Error("translate 返回格式不正确")
	}

	// 测试 translate 缺少参数
	_, err = host.GetPrompt(context.Background(), "translate", map[string]string{"text": "Hello"})
	if err == nil {
		t.Error("translate 缺少 language 参数应返回错误")
	}

	// 测试 code-review 执行（带语言参数）
	msgs, err = host.GetPrompt(context.Background(), "code-review", map[string]string{"code": "fmt.Println()", "language": "Go"})
	if err != nil {
		t.Fatalf("执行 code-review 提示失败: %v", err)
	}
	if len(msgs) != 1 || msgs[0].Role != "user" {
		t.Error("code-review 返回格式不正确")
	}

	// 测试 code-review 执行（不带语言参数）
	msgs, err = host.GetPrompt(context.Background(), "code-review", map[string]string{"code": "print('hello')"})
	if err != nil {
		t.Fatalf("执行 code-review 提示（无语言）失败: %v", err)
	}
	if len(msgs) != 1 || msgs[0].Role != "user" {
		t.Error("code-review（无语言）返回格式不正确")
	}
}

func TestRemoteToolExecution_ErrorResponse(t *testing.T) {
	logger := zap.NewNop()
	host := NewHost(logger)

	transport := &mockTransport{
		recvQueue: []json.RawMessage{
			// initialize 响应
			json.RawMessage(`{"jsonrpc":"2.0","id":0,"result":{"protocolVersion":"2024-11-05","serverInfo":{"name":"test"}}}`),
			// tools/list 响应
			json.RawMessage(`{"jsonrpc":"2.0","id":1,"result":{"tools":[{"name":"fail_tool","description":"会失败的工具","inputSchema":{"type":"object"}}]}}`),
			// resources/list 响应（不支持）
			json.RawMessage(`{"jsonrpc":"2.0","id":2,"error":{"code":-32601,"message":"Method not found"}}`),
			// prompts/list 响应（不支持）
			json.RawMessage(`{"jsonrpc":"2.0","id":3,"error":{"code":-32601,"message":"Method not found"}}`),
		},
	}

	client, err := ConnectRemoteMCP(context.Background(), transport, host, "remote", logger)
	if err != nil {
		t.Fatalf("连接失败: %v", err)
	}
	defer client.Close()

	// 添加 tools/call 错误响应
	transport.mu.Lock()
	transport.recvQueue = append(transport.recvQueue,
		json.RawMessage(`{"jsonrpc":"2.0","id":4,"error":{"code":-32000,"message":"工具执行失败: 权限不足"}}`),
	)
	transport.mu.Unlock()

	result, err := host.ExecuteTool(context.Background(), "remote__fail_tool", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("不应返回 Go error，期望 ToolResult.IsError=true: %v", err)
	}

	if !result.IsError {
		t.Error("期望 IsError=true")
	}

	// 验证 Content 是合法 JSON
	decoded := DecodeToolContent(result.Content)
	if decoded == "" {
		t.Error("期望非空的错误消息")
	}
	if decoded != "工具执行失败: 权限不足" {
		t.Errorf("错误消息 = %q, 期望 %q", decoded, "工具执行失败: 权限不足")
	}
}

func TestRemoteToolExecution_ErrorWithSpecialChars(t *testing.T) {
	logger := zap.NewNop()
	host := NewHost(logger)

	transport := &mockTransport{
		recvQueue: []json.RawMessage{
			json.RawMessage(`{"jsonrpc":"2.0","id":0,"result":{"protocolVersion":"2024-11-05","serverInfo":{"name":"test"}}}`),
			json.RawMessage(`{"jsonrpc":"2.0","id":1,"result":{"tools":[{"name":"inject_tool","description":"测试","inputSchema":{"type":"object"}}]}}`),
			json.RawMessage(`{"jsonrpc":"2.0","id":2,"error":{"code":-32601,"message":"Method not found"}}`),
			json.RawMessage(`{"jsonrpc":"2.0","id":3,"error":{"code":-32601,"message":"Method not found"}}`),
		},
	}

	client, err := ConnectRemoteMCP(context.Background(), transport, host, "remote", logger)
	if err != nil {
		t.Fatalf("连接失败: %v", err)
	}
	defer client.Close()

	// 错误消息包含 JSON 特殊字符（引号、反斜杠、换行）— 这是 JSON 注入攻击向量
	transport.mu.Lock()
	transport.recvQueue = append(transport.recvQueue,
		json.RawMessage(`{"jsonrpc":"2.0","id":4,"error":{"code":-32000,"message":"path \"C:\\Users\\test\" not found\ndetails: {\"inject\": true}"}}`),
	)
	transport.mu.Unlock()

	result, err := host.ExecuteTool(context.Background(), "remote__inject_tool", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("不应返回 Go error: %v", err)
	}

	if !result.IsError {
		t.Error("期望 IsError=true")
	}

	// 关键验证：Content 必须是合法 JSON，不能因特殊字符而破坏
	if !json.Valid(result.Content) {
		t.Fatalf("Content 不是合法 JSON: %s", result.Content)
	}

	// 验证解码后包含原始错误信息
	decoded := DecodeToolContent(result.Content)
	if decoded == "" {
		t.Error("期望非空的错误消息")
	}
	// 验证特殊字符被正确保留（不是被截断或丢失）
	expectedSubstrings := []string{`C:\Users\test`, "not found", "inject"}
	for _, sub := range expectedSubstrings {
		found := false
		for i := 0; i <= len(decoded)-len(sub); i++ {
			if decoded[i:i+len(sub)] == sub {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("解码后的错误消息 %q 应包含 %q", decoded, sub)
		}
	}
}
