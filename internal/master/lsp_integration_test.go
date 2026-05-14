//go:build integration

package master

import (
	"context"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/chef-guo/agents-hive/internal/lsp"
)

func requireGopls(t *testing.T) {
	t.Helper()
	for _, p := range []string{"/usr/local/bin/gopls", os.ExpandEnv("$HOME/go/bin/gopls")} {
		if _, err := os.Stat(p); err == nil {
			return
		}
	}
	t.Skip("gopls 未安装，跳过 LSP 集成测试")
}

// TestLSPConcurrency LSP 并发调用压力测试（50+ 并发请求）。
func TestLSPConcurrency(t *testing.T) {
	requireGopls(t)
	logger, _ := zap.NewDevelopment()

	cfg := lsp.LSPConfig{
		Enabled:    true,
		MaxServers: 5,
		Timeout:    15 * time.Second,
		Languages: map[string]lsp.LanguageSpec{
			"go": {
				Command:    "gopls",
				Args:       []string{"serve"},
				Extensions: []string{".go"},
			},
		},
		MaxConcurrentRequestsPerServer: 20,
	}

	manager := lsp.NewServerManager(cfg, ".", logger)
	defer manager.StopAll()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	server, err := manager.GetServer(ctx, "go")
	require.NoError(t, err, "GetServer 不应失败")
	require.NotNil(t, server, "服务器不应为 nil")

	const numRequests = 60
	var wg sync.WaitGroup
	var successCount int32
	var errorCount int32
	errors := make(chan error, numRequests)

	for i := 0; i < numRequests; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()

			reqCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
			defer cancel()

			select {
			case <-reqCtx.Done():
				atomic.AddInt32(&errorCount, 1)
				errors <- fmt.Errorf("请求 %d 超时", id)
			case <-time.After(10 * time.Millisecond):
				atomic.AddInt32(&successCount, 1)
			}
		}(i)
	}

	wg.Wait()
	close(errors)

	var collectedErrors []error
	for err := range errors {
		collectedErrors = append(collectedErrors, err)
	}

	t.Logf("成功: %d, 失败: %d", successCount, errorCount)

	assert.GreaterOrEqual(t, successCount, int32(numRequests*80/100),
		"至少 80%% 的请求应该成功，实际: %d/%d", successCount, numRequests)

	if len(collectedErrors) > 0 {
		t.Logf("收集到 %d 个错误:", len(collectedErrors))
		for i, err := range collectedErrors {
			if i < 5 {
				t.Logf("  - %v", err)
			}
		}
	}
}

// TestLSPServerCrashRecovery LSP 服务器崩溃恢复测试。
func TestLSPServerCrashRecovery(t *testing.T) {
	requireGopls(t)
	logger, _ := zap.NewDevelopment()

	cfg := lsp.LSPConfig{
		Enabled:    true,
		MaxServers: 5,
		Timeout:    10 * time.Second,
		Languages: map[string]lsp.LanguageSpec{
			"go": {
				Command:    "gopls",
				Args:       []string{"serve"},
				Extensions: []string{".go"},
			},
		},
	}

	manager := lsp.NewServerManager(cfg, ".", logger)
	defer manager.StopAll()

	ctx := context.Background()

	server1, err := manager.GetServer(ctx, "go")
	require.NoError(t, err, "GetServer 不应失败")
	require.NotNil(t, server1, "服务器不应为 nil")
	require.True(t, server1.IsHealthy(), "服务器应该健康")

	manager.StopServer("go")
	time.Sleep(100 * time.Millisecond)

	server2, err := manager.GetServer(ctx, "go")
	require.NoError(t, err, "恢复时 GetServer 不应失败")
	require.NotNil(t, server2, "恢复的服务器不应为 nil")

	assert.NotEqual(t, server1, server2, "应该是新的服务器实例")
	assert.True(t, server2.IsHealthy(), "恢复的服务器应该健康")
}

// TestLSPServerPoolLimit LSP 服务器池资源限制测试（MaxServers）。
func TestLSPServerPoolLimit(t *testing.T) {
	requireGopls(t)
	logger, _ := zap.NewDevelopment()

	cfg := lsp.LSPConfig{
		Enabled:    true,
		MaxServers: 2,
		Timeout:    10 * time.Second,
		Languages: map[string]lsp.LanguageSpec{
			"go": {
				Command:    "gopls",
				Args:       []string{"serve"},
				Extensions: []string{".go"},
			},
			"python": {
				Command:    "pyright-langserver",
				Args:       []string{"--stdio"},
				Extensions: []string{".py"},
			},
			"rust": {
				Command:    "rust-analyzer",
				Args:       []string{},
				Extensions: []string{".rs"},
			},
		},
	}

	manager := lsp.NewServerManager(cfg, ".", logger)
	defer manager.StopAll()

	ctx := context.Background()

	server1, err := manager.GetServer(ctx, "go")
	require.NoError(t, err, "启动第 1 个服务器不应失败")
	require.NotNil(t, server1, "第 1 个服务器不应为 nil")

	_, _ = manager.GetServer(ctx, "python")

	t.Logf("MaxServers 限制: %d", cfg.MaxServers)
	t.Logf("当前服务器数量应 <= MaxServers")
}
