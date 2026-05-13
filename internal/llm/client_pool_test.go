package llm

import (
	"testing"

	"go.uber.org/zap"
)

func TestClientPool(t *testing.T) {
	logger := zap.NewNop()
	pool := NewClientPool(logger)

	// 测试基本的 Get
	cfg1 := ClientConfig{
		APIKey:   "test-key",
		BaseURL:  "https://www.gmini.xyz/v1",
		Model:    "gpt-4",
		Provider: LookupProvider("openai"),
	}

	client1 := pool.Get(cfg1)
	if client1 == nil {
		t.Fatal("client1 should not be nil")
	}

	// 再次获取相同配置，应该返回缓存的 client
	client2 := pool.Get(cfg1)
	if client2 != client1 {
		t.Error("expected cached client, got new instance")
	}

	// 测试不同配置
	cfg2 := ClientConfig{
		APIKey:   "test-key",
		BaseURL:  "https://www.gmini.xyz/v1",
		Model:    "gpt-3.5-turbo",
		Provider: LookupProvider("openai"),
	}

	client3 := pool.Get(cfg2)
	if client3 == client1 {
		t.Error("expected different client for different model")
	}

	// 验证池大小
	if pool.Size() != 2 {
		t.Errorf("expected pool size 2, got %d", pool.Size())
	}
}

func TestClientPool_MaxSize(t *testing.T) {
	logger := zap.NewNop()
	pool := NewClientPool(logger)
	pool.maxSize = 3

	provider := LookupProvider("openai")

	// 创建 3 个不同配置的 client
	for i := 0; i < 3; i++ {
		cfg := ClientConfig{
			APIKey:   "test-key",
			BaseURL:  "https://www.gmini.xyz/v1",
			Model:    string(rune('A' + i)), // "A", "B", "C"
			Provider: provider,
		}
		pool.Get(cfg)
	}

	if pool.Size() != 3 {
		t.Errorf("expected pool size 3, got %d", pool.Size())
	}

	// 尝试创建第 4 个，应该返回临时 client（不缓存）
	cfg4 := ClientConfig{
		APIKey:   "test-key",
		BaseURL:  "https://www.gmini.xyz/v1",
		Model:    "D",
		Provider: provider,
	}
	client4 := pool.Get(cfg4)
	if client4 == nil {
		t.Fatal("client4 should not be nil")
	}

	// 池大小应该保持 3
	if pool.Size() != 3 {
		t.Errorf("expected pool size 3, got %d", pool.Size())
	}
}

func TestClientPool_Clear(t *testing.T) {
	logger := zap.NewNop()
	pool := NewClientPool(logger)

	cfg := ClientConfig{
		APIKey:   "test-key",
		BaseURL:  "https://www.gmini.xyz/v1",
		Model:    "gpt-4",
		Provider: LookupProvider("openai"),
	}

	pool.Get(cfg)
	if pool.Size() != 1 {
		t.Errorf("expected pool size 1, got %d", pool.Size())
	}

	pool.Clear()
	if pool.Size() != 0 {
		t.Errorf("expected pool size 0, got %d", pool.Size())
	}
}

func TestBuildCacheKey(t *testing.T) {
	tests := []struct {
		name     string
		cfg      ClientConfig
		expected string
	}{
		{
			name: "完整配置",
			cfg: ClientConfig{
				Provider:       LookupProvider("openai"),
				Model:          "gpt-4",
				BaseURL:        "https://www.gmini.xyz/v1",
				PromptCacheKey: true,
				ServiceTier:    "priority",
				StorePrivacy:   true,
			},
			expected: "openai:gpt-4:https://www.gmini.xyz/v1:chat:cache=true:tier=priority:privacy=true",
		},
		{
			name: "空 BaseURL",
			cfg: ClientConfig{
				Provider: LookupProvider("openai"),
				Model:    "gpt-4",
				BaseURL:  "",
			},
			expected: "openai:gpt-4:https://www.gmini.xyz:chat:cache=false:tier=:privacy=false",
		},
		{
			name: "DeepSeek Provider",
			cfg: ClientConfig{
				Provider: LookupProvider("deepseek"),
				Model:    "deepseek-chat",
				BaseURL:  "",
			},
			expected: "deepseek:deepseek-chat:https://api.deepseek.com:chat:cache=false:tier=:privacy=false",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			key := buildCacheKey(tt.cfg)
			if key != tt.expected {
				t.Errorf("expected key %q, got %q", tt.expected, key)
			}
		})
	}
}

func TestClientPool_Concurrent(t *testing.T) {
	logger := zap.NewNop()
	pool := NewClientPool(logger)

	cfg := ClientConfig{
		APIKey:   "test-key",
		BaseURL:  "https://www.gmini.xyz/v1",
		Model:    "gpt-4",
		Provider: LookupProvider("openai"),
	}

	// 并发获取相同配置，应该只创建一个 client
	const goroutines = 100
	clients := make([]*Client, goroutines)
	done := make(chan bool)

	for i := 0; i < goroutines; i++ {
		go func(idx int) {
			clients[idx] = pool.Get(cfg)
			done <- true
		}(i)
	}

	for i := 0; i < goroutines; i++ {
		<-done
	}

	// 验证所有 client 都是同一个实例
	firstClient := clients[0]
	for i := 1; i < goroutines; i++ {
		if clients[i] != firstClient {
			t.Errorf("concurrent Get() created multiple instances")
			break
		}
	}

	// 验证池大小为 1
	if pool.Size() != 1 {
		t.Errorf("expected pool size 1, got %d", pool.Size())
	}
}
