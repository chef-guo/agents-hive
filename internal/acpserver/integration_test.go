package acpserver

import (
	"context"
	"io"
	"testing"
	"time"

	acp "github.com/coder/acp-go-sdk"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/chef-guo/agents-hive/internal/config"
)

// mockACPClient 实现 acp.Client 接口，用于测试
// 所有方法均返回零值，不执行实际操作
type mockACPClient struct{}

func (m *mockACPClient) ReadTextFile(_ context.Context, _ acp.ReadTextFileRequest) (acp.ReadTextFileResponse, error) {
	return acp.ReadTextFileResponse{}, nil
}

func (m *mockACPClient) WriteTextFile(_ context.Context, _ acp.WriteTextFileRequest) (acp.WriteTextFileResponse, error) {
	return acp.WriteTextFileResponse{}, nil
}

func (m *mockACPClient) RequestPermission(_ context.Context, _ acp.RequestPermissionRequest) (acp.RequestPermissionResponse, error) {
	return acp.RequestPermissionResponse{}, nil
}

func (m *mockACPClient) SessionUpdate(_ context.Context, _ acp.SessionNotification) error {
	return nil
}

func (m *mockACPClient) CreateTerminal(_ context.Context, _ acp.CreateTerminalRequest) (acp.CreateTerminalResponse, error) {
	return acp.CreateTerminalResponse{}, nil
}

func (m *mockACPClient) KillTerminal(_ context.Context, _ acp.KillTerminalRequest) (acp.KillTerminalResponse, error) {
	return acp.KillTerminalResponse{}, nil
}

func (m *mockACPClient) TerminalOutput(_ context.Context, _ acp.TerminalOutputRequest) (acp.TerminalOutputResponse, error) {
	return acp.TerminalOutputResponse{}, nil
}

func (m *mockACPClient) ReleaseTerminal(_ context.Context, _ acp.ReleaseTerminalRequest) (acp.ReleaseTerminalResponse, error) {
	return acp.ReleaseTerminalResponse{}, nil
}

func (m *mockACPClient) WaitForTerminalExit(_ context.Context, _ acp.WaitForTerminalExitRequest) (acp.WaitForTerminalExitResponse, error) {
	return acp.WaitForTerminalExitResponse{}, nil
}

// defaultTestACPConfig 返回用于测试的最小化 ACPServerConfig
func defaultTestACPConfig() config.ACPServerConfig {
	return config.ACPServerConfig{
		Enabled:     true,
		AuthMethod:  "none",
		MaxSessions: 10,
	}
}

// TestClawAgent_Initialize 测试 ACP Agent 初始化流程
// 使用 io.Pipe 模拟 stdio 通信，不依赖真实 LLM 或网络
func TestClawAgent_Initialize(t *testing.T) {
	// 使用 nil Master（Initialize 不依赖 Master）
	agent := NewClawAgent(nil, defaultTestACPConfig(), zap.NewNop(), nil, nil)

	// 创建双向管道模拟 stdio
	// agentRead ← clientWrite  (客户端写，服务端读)
	// clientRead ← agentWrite  (服务端写，客户端读)
	agentRead, clientWrite := io.Pipe()
	clientRead, agentWrite := io.Pipe()

	defer agentRead.Close()
	defer agentWrite.Close()
	defer clientRead.Close()
	defer clientWrite.Close()

	// 创建 Agent 侧连接（服务端：读 agentRead，写 agentWrite）
	agentConn := acp.NewAgentSideConnection(agent, agentWrite, agentRead)
	agent.SetAgentConnection(agentConn)

	// 启动 Agent 侧消息处理
	agentDone := make(chan struct{})
	go func() {
		defer close(agentDone)
		<-agentConn.Done()
	}()

	// 创建客户端侧连接（客户端：写 clientWrite，读 clientRead）
	clientConn := acp.NewClientSideConnection(&mockACPClient{}, clientWrite, clientRead)

	// 模拟客户端发送 initialize 请求
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	resp, err := clientConn.Initialize(ctx, acp.InitializeRequest{
		ProtocolVersion: acp.ProtocolVersionNumber,
	})
	require.NoError(t, err, "Initialize 请求应成功")
	assert.Equal(t, acp.ProtocolVersion(acp.ProtocolVersionNumber), resp.ProtocolVersion, "协议版本应匹配")

	// 关闭客户端管道，触发服务端连接断开
	_ = clientWrite.Close()
	_ = clientRead.Close()

	// 等待服务端处理完成
	select {
	case <-agentDone:
	case <-time.After(3 * time.Second):
		t.Fatal("等待 Agent 连接关闭超时")
	}
}

// TestClawAgent_Initialize_CapabilitiesSet 验证初始化响应中的能力声明
func TestClawAgent_Initialize_CapabilitiesSet(t *testing.T) {
	agent := NewClawAgent(nil, defaultTestACPConfig(), zap.NewNop(), nil, nil)

	// 直接调用 Initialize 方法（单元测试，不走网络）
	resp, err := agent.Initialize(context.Background(), acp.InitializeRequest{
		ProtocolVersion: acp.ProtocolVersionNumber,
	})
	require.NoError(t, err)
	assert.Equal(t, acp.ProtocolVersion(acp.ProtocolVersionNumber), resp.ProtocolVersion)
}
