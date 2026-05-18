package llm

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/openai/openai-go"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

func TestNormalizeBaseURL(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "空字符串",
			input:    "",
			expected: "",
		},
		{
			name:     "仅域名（HTTPS）",
			input:    "https://gmini.xyz",
			expected: "https://gmini.xyz/v1",
		},
		{
			name:     "仅域名（HTTP）",
			input:    "http://api.example.com",
			expected: "http://api.example.com/v1",
		},
		{
			name:     "域名带尾部斜杠",
			input:    "https://gmini.xyz/",
			expected: "https://gmini.xyz/v1",
		},
		{
			name:     "已有 /v1 后缀",
			input:    "https://gmini.xyz/v1",
			expected: "https://gmini.xyz/v1",
		},
		{
			name:     "已有 /v1/ 后缀（带尾部斜杠）",
			input:    "https://gmini.xyz/v1/",
			expected: "https://gmini.xyz/v1/",
		},
		{
			name:     "大小写混合的 /V1",
			input:    "https://gmini.xyz/V1",
			expected: "https://gmini.xyz/V1",
		},
		{
			name:     "localhost 带端口号",
			input:    "http://localhost:18080",
			expected: "http://localhost:18080/v1",
		},
		{
			name:     "localhost 带端口号和尾部斜杠",
			input:    "http://localhost:18080/",
			expected: "http://localhost:18080/v1",
		},
		{
			name:     "自定义路径（复杂路径保持不变）",
			input:    "https://api.custom.com/custom/endpoint",
			expected: "https://api.custom.com/custom/endpoint",
		},
		{
			name:     "简单路径（单段）",
			input:    "https://api.custom.com/api",
			expected: "https://api.custom.com/api/v1",
		},
		{
			name:     "自定义路径已有 /v1",
			input:    "https://api.custom.com/custom/v1",
			expected: "https://api.custom.com/custom/v1",
		},
		{
			name:     "无效 URL（保持原样）",
			input:    "not a valid url",
			expected: "not a valid url",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := normalizeBaseURL(tt.input)
			if result != tt.expected {
				t.Errorf("normalizeBaseURL(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

func TestNewClient_NormalizesBaseURL(t *testing.T) {
	tests := []struct {
		name    string
		baseURL string
	}{
		{
			name:    "空 baseURL",
			baseURL: "",
		},
		{
			name:    "需要规范化的 baseURL",
			baseURL: "https://gmini.xyz",
		},
		{
			name:    "已规范化的 baseURL",
			baseURL: "http://45.205.26.177:9999",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logger := zap.NewNop()
			client := NewClient(ClientConfig{APIKey: "test-api-key", BaseURL: tt.baseURL, Model: "gpt-4", DisableJSONMode: false}, logger)

			if client == nil {
				t.Error("NewClient returned nil")
			}
			if client.model != "gpt-4" {
				t.Errorf("model = %q, want %q", client.model, "gpt-4")
			}
		})
	}
}

func TestClient_LogAPIError(t *testing.T) {
	// 创建一个可观察的 logger（仅 Error 级别，避免触发 DumpResponse）
	core, observed := observer.New(zapcore.ErrorLevel)
	logger := zap.New(core)

	client := &Client{
		logger: logger,
	}

	// 创建一个模拟的 OpenAI API 错误
	apiErr := &openai.Error{
		StatusCode: 401,
		Type:       "invalid_request_error",
		Code:       "invalid_api_key",
		Message:    "Incorrect API key provided",
	}

	// 包装错误以便可以使用 errors.As
	err := errors.Join(apiErr)

	// 调用 logAPIError
	client.logAPIError(err, "test_context")

	// 验证日志记录
	logs := observed.All()
	if len(logs) == 0 {
		t.Fatal("Expected at least one log entry, got none")
	}

	errorLog := logs[0]
	if errorLog.Level != zapcore.ErrorLevel {
		t.Errorf("Expected Error level, got %v", errorLog.Level)
	}

	// 验证日志字段
	fields := make(map[string]interface{})
	for _, field := range errorLog.Context {
		fields[field.Key] = field.String
		if field.Key == "status_code" {
			fields[field.Key] = field.Integer
		}
	}

	if fields["context"] != "test_context" {
		t.Errorf("Expected context='test_context', got %v", fields["context"])
	}
	if fields["status_code"] != int64(401) {
		t.Errorf("Expected status_code=401, got %v", fields["status_code"])
	}
	if fields["error_type"] != "invalid_request_error" {
		t.Errorf("Expected error_type='invalid_request_error', got %v", fields["error_type"])
	}
}

func TestClient_LogAPIError_NonAPIError(t *testing.T) {
	// 创建一个可观察的 logger
	core, observed := observer.New(zapcore.DebugLevel)
	logger := zap.New(core)

	client := &Client{
		logger: logger,
	}

	// 使用非 OpenAI API 错误
	err := errors.New("some other error")

	// 调用 logAPIError
	client.logAPIError(err, "test_context")

	// 验证没有日志记录（因为不是 API 错误）
	logs := observed.All()
	if len(logs) != 0 {
		t.Errorf("Expected no log entries for non-API error, got %d", len(logs))
	}
}

func TestClient_ChatJSON_Integration(t *testing.T) {
	// 这是一个集成测试示例，验证 ChatJSON 调用流程
	// 注意：这需要有效的 API key 或 mock，这里只是展示结构

	t.Skip("需要有效的 API key 或 mock server")

	logger := zap.NewNop()
	client := NewClient(ClientConfig{APIKey: "test-api-key", BaseURL: "http://45.205.26.177:9999", Model: "gpt-4", DisableJSONMode: false}, logger)

	type Response struct {
		Answer string `json:"answer"`
	}

	var resp Response
	err := client.ChatJSON(context.Background(), ChatRequest{
		SystemPrompt: "You are a helpful assistant.",
		Messages: []Message{
			{Role: "user", Content: NewTextContent("What is 2+2? Reply in JSON with 'answer' field.")},
		},
		Temperature: 0.7,
		MaxTokens:   100,
	}, &resp)

	if err != nil {
		t.Fatalf("ChatJSON failed: %v", err)
	}

	if resp.Answer == "" {
		t.Error("Expected non-empty answer")
	}
}

// TestIsResponseFormatError 测试错误检测逻辑
func TestIsResponseFormatError(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected bool
	}{
		{
			name: "400 错误 - response_format 关键词",
			err: &openai.Error{
				StatusCode: 400,
				Message:    "Invalid parameter: response_format",
			},
			expected: true,
		},
		{
			name: "400 错误 - 中文关键词",
			err: &openai.Error{
				StatusCode: 400,
				Message:    "不合法的response_format（traceid: xxx）",
			},
			expected: true,
		},
		{
			name: "400 错误 - unsupported 关键词",
			err: &openai.Error{
				StatusCode: 400,
				Message:    "The response_format parameter is unsupported",
			},
			expected: true,
		},
		{
			name: "400 错误 - invalid 关键词",
			err: &openai.Error{
				StatusCode: 400,
				Message:    "invalid response_format value",
			},
			expected: true,
		},
		{
			name: "400 错误 - 无关键词",
			err: &openai.Error{
				StatusCode: 400,
				Message:    "Invalid model parameter",
			},
			expected: false,
		},
		{
			name: "401 错误 - 有关键词但状态码不对",
			err: &openai.Error{
				StatusCode: 401,
				Message:    "Invalid API key: response_format",
			},
			expected: false,
		},
		{
			name: "500 错误",
			err: &openai.Error{
				StatusCode: 500,
				Message:    "Internal server error",
			},
			expected: false,
		},
		{
			name:     "非 API 错误",
			err:      fmt.Errorf("network timeout"),
			expected: false,
		},
		{
			name:     "nil 错误",
			err:      nil,
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isResponseFormatError(tt.err)
			if result != tt.expected {
				t.Errorf("isResponseFormatError() = %v, want %v", result, tt.expected)
			}
		})
	}
}

// TestClient_ShouldSkipJSONMode 测试 JSON mode 跳过逻辑
func TestClient_ShouldSkipJSONMode(t *testing.T) {
	logger := zap.NewNop()

	t.Run("显式禁用 - 应该跳过", func(t *testing.T) {
		client := NewClient(ClientConfig{APIKey: "test-key", BaseURL: "https://api.example.com", Model: "gpt-4", DisableJSONMode: true}, logger)
		if !client.shouldSkipJSONMode() {
			t.Error("shouldSkipJSONMode() = false, want true (explicit disable)")
		}
	})

	t.Run("未禁用且无缓存 - 不应该跳过", func(t *testing.T) {
		// 清理缓存
		unsupportedJSONModeCache = sync.Map{}

		client := NewClient(ClientConfig{APIKey: "test-key", BaseURL: "https://api.fresh.com/v1", Model: "gpt-4", DisableJSONMode: false}, logger)
		if client.shouldSkipJSONMode() {
			t.Error("shouldSkipJSONMode() = true, want false (no cache, not disabled)")
		}
	})

	t.Run("缓存命中 - 应该跳过", func(t *testing.T) {
		// 清理缓存
		unsupportedJSONModeCache = sync.Map{}

		baseURL := "https://api.cached.com/v1"
		client := NewClient(ClientConfig{APIKey: "test-key", BaseURL: baseURL, Model: "gpt-4", DisableJSONMode: false}, logger)

		// 标记为不支持
		client.markJSONModeUnsupported()

		// 创建新客户端使用相同 baseURL
		client2 := NewClient(ClientConfig{APIKey: "test-key", BaseURL: baseURL, Model: "gpt-4", DisableJSONMode: false}, logger)
		if !client2.shouldSkipJSONMode() {
			t.Error("shouldSkipJSONMode() = false, want true (cache hit)")
		}
	})

	t.Run("空 baseURL - 不应该跳过", func(t *testing.T) {
		client := NewClient(ClientConfig{APIKey: "test-key", BaseURL: "", Model: "gpt-4", DisableJSONMode: false}, logger)
		if client.shouldSkipJSONMode() {
			t.Error("shouldSkipJSONMode() = true, want false (empty baseURL)")
		}
	})
}

// TestClient_MarkJSONModeUnsupported 测试缓存标记
func TestClient_MarkJSONModeUnsupported(t *testing.T) {
	logger := zap.NewNop()

	// 清理缓存
	unsupportedJSONModeCache = sync.Map{}

	baseURL := "https://api.test.com/v1"
	client := NewClient(ClientConfig{APIKey: "test-key", BaseURL: baseURL, Model: "gpt-4", DisableJSONMode: false}, logger)

	// 标记前应该不跳过
	if client.shouldSkipJSONMode() {
		t.Error("shouldSkipJSONMode() = true before marking, want false")
	}

	// 标记
	client.markJSONModeUnsupported()

	// 标记后应该跳过
	if !client.shouldSkipJSONMode() {
		t.Error("shouldSkipJSONMode() = false after marking, want true")
	}

	// 验证缓存中有这个 baseURL
	_, exists := unsupportedJSONModeCache.Load(baseURL)
	if !exists {
		t.Error("baseURL not found in cache after marking")
	}
}

// TestClient_MarkJSONModeUnsupported_EmptyBaseURL 测试空 baseURL 不会写入缓存
func TestClient_MarkJSONModeUnsupported_EmptyBaseURL(t *testing.T) {
	logger := zap.NewNop()

	// 清理缓存
	unsupportedJSONModeCache = sync.Map{}

	client := NewClient(ClientConfig{APIKey: "test-key", BaseURL: "", Model: "gpt-4", DisableJSONMode: false}, logger)
	client.markJSONModeUnsupported()

	// 验证缓存中没有空字符串
	_, exists := unsupportedJSONModeCache.Load("")
	if exists {
		t.Error("empty baseURL should not be stored in cache")
	}
}

func TestStripThinkTags(t *testing.T) {
	cases := []struct {
		name           string
		input          string
		wantCleaned    string
		wantReasoning  string
	}{
		{
			name:          "无 think 标签",
			input:         "正常回答",
			wantCleaned:   "正常回答",
			wantReasoning: "",
		},
		{
			name:          "单个 think 块在前",
			input:         "<think>推理过程</think>实际回答",
			wantCleaned:   "实际回答",
			wantReasoning: "推理过程",
		},
		{
			name:          "think 块在中间",
			input:         "前缀<think>推理</think>后缀",
			wantCleaned:   "前缀后缀",
			wantReasoning: "推理",
		},
		{
			name:          "多个 think 块",
			input:         "<think>推理1</think>回答<think>推理2</think>结尾",
			wantCleaned:   "回答结尾",
			wantReasoning: "推理1\n推理2",
		},
		{
			name:          "仅有 think 块无实际内容",
			input:         "<think>只有推理</think>",
			wantCleaned:   "",
			wantReasoning: "只有推理",
		},
		{
			name:          "未闭合的 think 标签",
			input:         "<think>未闭合的推理",
			wantCleaned:   "",
			wantReasoning: "未闭合的推理",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cleaned, reasoning := stripThinkTags(tc.input)
			if cleaned != tc.wantCleaned {
				t.Errorf("cleaned = %q，期望 %q", cleaned, tc.wantCleaned)
			}
			if reasoning != tc.wantReasoning {
				t.Errorf("reasoning = %q，期望 %q", reasoning, tc.wantReasoning)
			}
		})
	}
}
