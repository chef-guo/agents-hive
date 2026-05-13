package mcphost

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHTTPTransport_Connect(t *testing.T) {
	tests := []struct {
		name    string
		handler http.HandlerFunc
		wantErr bool
		errMsg  string
	}{
		{
			name: "正常连接_JSON响应",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				fmt.Fprint(w, `{"jsonrpc":"2.0","id":0,"result":{"capabilities":{}}}`)
			},
			wantErr: false,
		},
		{
			name: "正常连接_SSE降级响应",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				w.WriteHeader(http.StatusOK)
				fmt.Fprint(w, "data: {\"jsonrpc\":\"2.0\",\"id\":0,\"result\":{}}\n\n")
			},
			wantErr: false,
		},
		{
			name: "服务端返回错误状态码",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusBadGateway)
				fmt.Fprint(w, "bad gateway")
			},
			wantErr: true,
			errMsg:  "502",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(tt.handler)
			defer srv.Close()

			transport := NewHTTPTransport(HTTPTransportConfig{
				URL:     srv.URL,
				Timeout: 5 * time.Second,
			}, testLogger())
			defer transport.Close()

			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()

			err := transport.Connect(ctx)
			if tt.wantErr {
				require.Error(t, err)
				if tt.errMsg != "" {
					assert.Contains(t, err.Error(), tt.errMsg)
				}
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestHTTPTransport_ConnectSendsInitializeParams(t *testing.T) {
	var reqBody struct {
		JSONRPC string `json:"jsonrpc"`
		ID      int    `json:"id"`
		Method  string `json:"method"`
		Params  struct {
			ProtocolVersion string         `json:"protocolVersion"`
			Capabilities    map[string]any `json:"capabilities"`
			ClientInfo      struct {
				Name    string `json:"name"`
				Version string `json:"version"`
			} `json:"clientInfo"`
		} `json:"params"`
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, json.NewDecoder(r.Body).Decode(&reqBody))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{"jsonrpc":"2.0","id":0,"result":{"protocolVersion":"2024-11-05","capabilities":{}}}`)
	}))
	defer srv.Close()

	transport := NewHTTPTransport(HTTPTransportConfig{
		URL:     srv.URL,
		Timeout: 5 * time.Second,
	}, testLogger())
	defer transport.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	require.NoError(t, transport.Connect(ctx))
	assert.Equal(t, "2.0", reqBody.JSONRPC)
	assert.Equal(t, 0, reqBody.ID)
	assert.Equal(t, "initialize", reqBody.Method)
	assert.Equal(t, "2024-11-05", reqBody.Params.ProtocolVersion)
	assert.NotNil(t, reqBody.Params.Capabilities)
	assert.Equal(t, "agents-hive", reqBody.Params.ClientInfo.Name)
	assert.Equal(t, "1.0", reqBody.Params.ClientInfo.Version)
}

func TestHTTPTransport_SendReceive(t *testing.T) {
	tests := []struct {
		name        string
		contentType string
		body        string
		wantResult  string
	}{
		{
			name:        "JSON响应",
			contentType: "application/json",
			body:        `{"jsonrpc":"2.0","id":1,"result":{"tools":[]}}`,
			wantResult:  `"result"`,
		},
		{
			name:        "SSE降级响应",
			contentType: "text/event-stream",
			body:        "data: {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"name\":\"test\"}}\n\n",
			wantResult:  `"name"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			callCount := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				callCount++
				if callCount == 1 {
					// Connect 的初始化请求返回 JSON
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusOK)
					fmt.Fprint(w, `{"jsonrpc":"2.0","id":0,"result":{}}`)
					return
				}
				w.Header().Set("Content-Type", tt.contentType)
				w.WriteHeader(http.StatusOK)
				fmt.Fprint(w, tt.body)
			}))
			defer srv.Close()

			transport := NewHTTPTransport(HTTPTransportConfig{
				URL:     srv.URL,
				Timeout: 5 * time.Second,
			}, testLogger())
			defer transport.Close()

			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()

			// 连接（消费初始化响应）
			err := transport.Connect(ctx)
			require.NoError(t, err)

			// 消费 Connect 产生的消息
			_, err = transport.Receive(ctx)
			require.NoError(t, err)

			// 发送请求
			msg := json.RawMessage(`{"jsonrpc":"2.0","method":"tools/list","id":1}`)
			err = transport.Send(ctx, msg)
			require.NoError(t, err)

			// 接收响应
			resp, err := transport.Receive(ctx)
			require.NoError(t, err)
			assert.Contains(t, string(resp), tt.wantResult)
		})
	}
}

func TestHTTPTransport_SendsSessionIDAfterInitialize(t *testing.T) {
	const sessionID = "sess-http-123"
	var listSessionHeader string
	var protocolVersionHeader string

	callCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		callCount++
		if callCount == 1 {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Mcp-Session-Id", sessionID)
			w.WriteHeader(http.StatusOK)
			fmt.Fprint(w, `{"jsonrpc":"2.0","id":0,"result":{"protocolVersion":"2024-11-05","capabilities":{}}}`)
			return
		}
		listSessionHeader = r.Header.Get("Mcp-Session-Id")
		protocolVersionHeader = r.Header.Get("MCP-Protocol-Version")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"result":{"tools":[]}}`)
	}))
	defer srv.Close()

	transport := NewHTTPTransport(HTTPTransportConfig{
		URL:     srv.URL,
		Timeout: 5 * time.Second,
	}, testLogger())
	defer transport.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	require.NoError(t, transport.Connect(ctx))
	_, err := transport.Receive(ctx)
	require.NoError(t, err)

	msg := json.RawMessage(`{"jsonrpc":"2.0","method":"tools/list","id":1}`)
	require.NoError(t, transport.Send(ctx, msg))
	assert.Equal(t, sessionID, listSessionHeader)
	assert.Equal(t, "2024-11-05", protocolVersionHeader)
}

func TestHTTPTransport_CloseDeletesSession(t *testing.T) {
	const sessionID = "sess-delete-123"
	var deleteSessionHeader string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Mcp-Session-Id", sessionID)
			w.WriteHeader(http.StatusOK)
			fmt.Fprint(w, `{"jsonrpc":"2.0","id":0,"result":{"protocolVersion":"2024-11-05","capabilities":{}}}`)
		case http.MethodGet:
			w.WriteHeader(http.StatusMethodNotAllowed)
		case http.MethodDelete:
			deleteSessionHeader = r.Header.Get("Mcp-Session-Id")
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	defer srv.Close()

	transport := NewHTTPTransport(HTTPTransportConfig{
		URL:     srv.URL,
		Timeout: 5 * time.Second,
	}, testLogger())

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	require.NoError(t, transport.Connect(ctx))
	require.NoError(t, transport.Close())
	assert.Equal(t, sessionID, deleteSessionHeader)
}

func TestHTTPTransport_ReinitializesExpiredSession(t *testing.T) {
	const oldSessionID = "sess-old"
	const newSessionID = "sess-new"
	var seenSessionHeaders []string
	posts := 0

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		posts++
		switch posts {
		case 1:
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Mcp-Session-Id", oldSessionID)
			w.WriteHeader(http.StatusOK)
			fmt.Fprint(w, `{"jsonrpc":"2.0","id":0,"result":{"protocolVersion":"2024-11-05","capabilities":{}}}`)
		case 2:
			seenSessionHeaders = append(seenSessionHeaders, r.Header.Get("Mcp-Session-Id"))
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `expired`)
		case 3:
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Mcp-Session-Id", newSessionID)
			w.WriteHeader(http.StatusOK)
			fmt.Fprint(w, `{"jsonrpc":"2.0","id":0,"result":{"protocolVersion":"2024-11-05","capabilities":{}}}`)
		default:
			seenSessionHeaders = append(seenSessionHeaders, r.Header.Get("Mcp-Session-Id"))
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"result":{"tools":[]}}`)
		}
	}))
	defer srv.Close()

	transport := NewHTTPTransport(HTTPTransportConfig{
		URL:     srv.URL,
		Timeout: 5 * time.Second,
	}, testLogger())
	defer transport.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	require.NoError(t, transport.Connect(ctx))
	_, err := transport.Receive(ctx)
	require.NoError(t, err)

	msg := json.RawMessage(`{"jsonrpc":"2.0","method":"tools/list","id":1}`)
	require.NoError(t, transport.Send(ctx, msg))
	assert.Equal(t, []string{oldSessionID, newSessionID}, seenSessionHeaders)
}

func TestHTTPTransport_SSEListenerReceivesMessagesAndLastEventID(t *testing.T) {
	const sessionID = "sess-sse-listener"
	ready := make(chan struct{})
	var getLastEventID string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Mcp-Session-Id", sessionID)
			w.WriteHeader(http.StatusOK)
			fmt.Fprint(w, `{"jsonrpc":"2.0","id":0,"result":{"protocolVersion":"2024-11-05","capabilities":{}}}`)
		case http.MethodGet:
			getLastEventID = r.Header.Get("Last-Event-ID")
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			if f, ok := w.(http.Flusher); ok {
				fmt.Fprint(w, "id: evt-1\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/test\"}\n\n")
				f.Flush()
			}
			close(ready)
		case http.MethodDelete:
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	defer srv.Close()

	transport := NewHTTPTransport(HTTPTransportConfig{
		URL:     srv.URL,
		Timeout: 5 * time.Second,
	}, testLogger())
	defer transport.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	require.NoError(t, transport.Connect(ctx))
	_, err := transport.Receive(ctx)
	require.NoError(t, err)

	select {
	case <-ready:
	case <-ctx.Done():
		t.Fatal("SSE listener did not connect")
	}
	msg, err := transport.Receive(ctx)
	require.NoError(t, err)
	assert.Contains(t, string(msg), "notifications/test")
	assert.Equal(t, "evt-1", transport.lastEventID)
	assert.Empty(t, getLastEventID)
}

func TestHTTPTransport_Close(t *testing.T) {
	transport := NewHTTPTransport(HTTPTransportConfig{
		URL: "http://localhost:9999",
	}, testLogger())

	// 关闭
	err := transport.Close()
	require.NoError(t, err)

	// 关闭后发送应报错
	ctx := context.Background()
	err = transport.Send(ctx, json.RawMessage(`{}`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "已关闭")

	// 关闭后连接应报错
	err = transport.Connect(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "已关闭")

	// 重复关闭不报错
	err = transport.Close()
	require.NoError(t, err)
}

func TestHTTPTransport_CustomHeaders(t *testing.T) {
	var receivedHeaders http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedHeaders = r.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{"jsonrpc":"2.0","id":0,"result":{}}`)
	}))
	defer srv.Close()

	transport := NewHTTPTransport(HTTPTransportConfig{
		URL: srv.URL,
		Headers: map[string]string{
			"Authorization": "Bearer my-secret-token",
			"X-Custom":      "test-value",
		},
		Timeout: 5 * time.Second,
	}, testLogger())
	defer transport.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	err := transport.Connect(ctx)
	require.NoError(t, err)

	assert.Equal(t, "Bearer my-secret-token", receivedHeaders.Get("Authorization"))
	assert.Equal(t, "test-value", receivedHeaders.Get("X-Custom"))
}

func TestHTTPTransport_DefaultConfig(t *testing.T) {
	transport := NewHTTPTransport(HTTPTransportConfig{
		URL: "http://localhost:9999",
	}, testLogger())

	assert.Equal(t, 30*time.Second, transport.cfg.Timeout, "默认超时应为 30s")
}

func TestHTTPTransport_InvalidJSON(t *testing.T) {
	callCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		if callCount == 1 {
			fmt.Fprint(w, `{"jsonrpc":"2.0","id":0,"result":{}}`)
			return
		}
		// 返回无效 JSON
		fmt.Fprint(w, `{invalid json`)
	}))
	defer srv.Close()

	transport := NewHTTPTransport(HTTPTransportConfig{
		URL:     srv.URL,
		Timeout: 5 * time.Second,
	}, testLogger())
	defer transport.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	err := transport.Connect(ctx)
	require.NoError(t, err)

	// 消费 Connect 的消息
	_, _ = transport.Receive(ctx)

	// 发送请求（服务端返回无效 JSON）
	msg := json.RawMessage(`{"jsonrpc":"2.0","method":"test","id":1}`)
	err = transport.Send(ctx, msg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "不是有效的 JSON")
}

func TestHTTPTransport_EmptyResponse(t *testing.T) {
	callCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.Header().Set("Content-Type", "application/json")
		if callCount == 1 {
			fmt.Fprint(w, `{"jsonrpc":"2.0","id":0,"result":{}}`)
			return
		}
		// 返回空响应
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	transport := NewHTTPTransport(HTTPTransportConfig{
		URL:     srv.URL,
		Timeout: 5 * time.Second,
	}, testLogger())
	defer transport.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	err := transport.Connect(ctx)
	require.NoError(t, err)

	// 消费 Connect 的消息
	_, _ = transport.Receive(ctx)

	// 发送请求（服务端返回空响应）
	msg := json.RawMessage(`{"jsonrpc":"2.0","method":"test","id":2}`)
	err = transport.Send(ctx, msg)
	require.NoError(t, err, "空响应不应报错")
}

func TestIsSSEContentType(t *testing.T) {
	tests := []struct {
		name        string
		contentType string
		want        bool
	}{
		{"标准SSE类型", "text/event-stream", true},
		{"带charset的SSE", "text/event-stream; charset=utf-8", true},
		{"JSON类型", "application/json", false},
		{"空类型", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, isSSEContentType(tt.contentType))
		})
	}
}
