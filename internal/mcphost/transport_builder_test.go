package mcphost

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildTransport_SSE(t *testing.T) {
	tests := []struct {
		name    string
		spec    MCPServerSpec
		wantErr bool
		errMsg  string
	}{
		{
			name: "SSE_基本配置",
			spec: MCPServerSpec{
				Name:      "test-sse",
				Transport: "sse",
				URL:       "http://localhost:18080/sse",
			},
			wantErr: false,
		},
		{
			name: "SSE_带自定义头和超时",
			spec: MCPServerSpec{
				Name:      "test-sse-headers",
				Transport: "sse",
				URL:       "http://localhost:18080/sse",
				Headers:   map[string]string{"X-Custom": "value"},
				Timeout:   10 * time.Second,
			},
			wantErr: false,
		},
		{
			name: "SSE_带OAuth配置",
			spec: MCPServerSpec{
				Name:      "test-sse-oauth",
				Transport: "sse",
				URL:       "http://localhost:18080/sse",
				OAuth: &OAuthConfig{
					ClientID: "my-client",
					AuthURL:  "http://auth.example.com/authorize",
					TokenURL: "http://auth.example.com/token",
					Scopes:   []string{"read", "write"},
				},
			},
			wantErr: false,
		},
		{
			name: "SSE_缺少URL",
			spec: MCPServerSpec{
				Name:      "test-sse-no-url",
				Transport: "sse",
			},
			wantErr: true,
			errMsg:  "SSE 传输需要指定 URL",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			transport, err := BuildTransport(tt.spec, nil, testLogger())
			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errMsg)
				assert.Nil(t, transport)
			} else {
				require.NoError(t, err)
				assert.NotNil(t, transport)
				_ = transport.Close()
			}
		})
	}
}

func TestBuildTransport_HTTP(t *testing.T) {
	tests := []struct {
		name    string
		spec    MCPServerSpec
		wantErr bool
		errMsg  string
	}{
		{
			name: "HTTP_基本配置",
			spec: MCPServerSpec{
				Name:      "test-http",
				Transport: "http",
				URL:       "http://localhost:18080/mcp",
			},
			wantErr: false,
		},
		{
			name: "HTTP_带OAuth配置",
			spec: MCPServerSpec{
				Name:      "test-http-oauth",
				Transport: "http",
				URL:       "http://localhost:18080/mcp",
				OAuth: &OAuthConfig{
					ClientID:     "my-client",
					ClientSecret: "my-secret",
					AuthURL:      "http://auth.example.com/authorize",
					TokenURL:     "http://auth.example.com/token",
				},
			},
			wantErr: false,
		},
		{
			name: "HTTP_缺少URL",
			spec: MCPServerSpec{
				Name:      "test-http-no-url",
				Transport: "http",
			},
			wantErr: true,
			errMsg:  "HTTP 传输需要指定 URL",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			transport, err := BuildTransport(tt.spec, nil, testLogger())
			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errMsg)
				assert.Nil(t, transport)
			} else {
				require.NoError(t, err)
				assert.NotNil(t, transport)
				_ = transport.Close()
			}
		})
	}
}

func TestBuildTransport_不支持的传输类型(t *testing.T) {
	tests := []struct {
		name      string
		transport string
		errMsg    string
	}{
		{
			name:      "stdio传输缺少command",
			transport: "stdio",
			errMsg:    "stdio 传输需要指定 command",
		},
		{
			name:      "空传输类型视为stdio缺少command",
			transport: "",
			errMsg:    "stdio 传输需要指定 command",
		},
		{
			name:      "未知传输类型",
			transport: "grpc",
			errMsg:    "不支持的 MCP 传输类型",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec := MCPServerSpec{
				Name:      "test",
				Transport: tt.transport,
				URL:       "http://localhost",
			}
			transport, err := BuildTransport(spec, nil, testLogger())
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.errMsg)
			assert.Nil(t, transport)
		})
	}
}

func TestBuildTransport_SSE_OAuth_AuthProvider已设置(t *testing.T) {
	// 验证带 OAuth 的 SSE 传输确实创建了 AuthProvider
	spec := MCPServerSpec{
		Name:      "oauth-test",
		Transport: "sse",
		URL:       "http://localhost:18080/sse",
		OAuth: &OAuthConfig{
			ClientID: "test-client",
			AuthURL:  "http://auth.example.com/authorize",
			TokenURL: "http://auth.example.com/token",
		},
	}

	mockStore := newMockTokenStore()
	transport, err := BuildTransport(spec, mockStore, testLogger())
	require.NoError(t, err)
	defer transport.Close()

	// SSETransport 的 AuthProvider 应已设置
	sseTransport, ok := transport.(*SSETransport)
	require.True(t, ok, "应返回 SSETransport 类型")
	assert.NotNil(t, sseTransport.cfg.AuthProvider, "OAuth 配置后 AuthProvider 不应为 nil")
}

func TestBuildTransport_HTTP_OAuth_AuthProvider已设置(t *testing.T) {
	// 验证带 OAuth 的 HTTP 传输确实创建了 AuthProvider
	spec := MCPServerSpec{
		Name:      "oauth-test",
		Transport: "http",
		URL:       "http://localhost:18080/mcp",
		OAuth: &OAuthConfig{
			ClientID: "test-client",
			AuthURL:  "http://auth.example.com/authorize",
			TokenURL: "http://auth.example.com/token",
		},
	}

	mockStore := newMockTokenStore()
	transport, err := BuildTransport(spec, mockStore, testLogger())
	require.NoError(t, err)
	defer transport.Close()

	// HTTPTransport 的 AuthProvider 应已设置
	httpTransport, ok := transport.(*HTTPTransport)
	require.True(t, ok, "应返回 HTTPTransport 类型")
	assert.NotNil(t, httpTransport.cfg.AuthProvider, "OAuth 配置后 AuthProvider 不应为 nil")
}
