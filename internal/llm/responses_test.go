package llm

import (
	"testing"

	"github.com/openai/openai-go/responses"

	"github.com/chef-guo/agents-hive/internal/mcphost"
)

func TestBuildResponsesInputFromToolMessages_IsError(t *testing.T) {
	tests := []struct {
		name       string
		msgs       []MessageWithTools
		wantOutput string
		wantStatus string
	}{
		{
			name: "正常工具结果：status=completed，无前缀",
			msgs: []MessageWithTools{
				{
					Role:       "tool",
					ToolCallID: "call_123",
					Content:    NewTextContent("文件内容"),
					IsError:    false,
				},
			},
			wantOutput: "文件内容",
			wantStatus: "completed",
		},
		{
			name: "错误工具结果：status=incomplete，添加 [TOOL_ERROR] 前缀",
			msgs: []MessageWithTools{
				{
					Role:       "tool",
					ToolCallID: "call_456",
					Content:    NewTextContent("文件不存在"),
					IsError:    true,
				},
			},
			wantOutput: "[TOOL_ERROR] 文件不存在",
			wantStatus: "incomplete",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			items := buildResponsesInputFromToolMessages(tt.msgs)
			if len(items) != 1 {
				t.Fatalf("期望 1 个 item，得到 %d", len(items))
			}
			item := items[0]
			if item.OfFunctionCallOutput == nil {
				t.Fatal("期望 OfFunctionCallOutput 不为 nil")
			}
			if item.OfFunctionCallOutput.Output != tt.wantOutput {
				t.Errorf("Output = %q，期望 %q", item.OfFunctionCallOutput.Output, tt.wantOutput)
			}
			if item.OfFunctionCallOutput.Status != tt.wantStatus {
				t.Errorf("Status = %q，期望 %q", item.OfFunctionCallOutput.Status, tt.wantStatus)
			}
		})
	}
}

func TestBuildResponsesInputFromToolMessages_MixedMessages(t *testing.T) {
	msgs := []MessageWithTools{
		{
			Role:    "user",
			Content: NewTextContent("请读取文件"),
		},
		{
			Role:    "assistant",
			Content: NewTextContent("好的，我来读取"),
			ToolCalls: []ToolCall{
				{ID: "call_1", Name: "read_file", Arguments: []byte(`{"path":"test.go"}`)},
			},
		},
		{
			Role:       "tool",
			ToolCallID: "call_1",
			Content:    NewTextContent("文件不存在: test.go"),
			IsError:    true,
		},
	}

	items := buildResponsesInputFromToolMessages(msgs)

	// user(1) + assistant text(1) + function_call(1) + function_call_output(1) = 4
	if len(items) != 4 {
		t.Fatalf("期望 4 个 items，得到 %d", len(items))
	}

	// 最后一个是 tool result
	toolItem := items[3]
	if toolItem.OfFunctionCallOutput == nil {
		t.Fatal("最后一个 item 应该是 OfFunctionCallOutput")
	}
	if toolItem.OfFunctionCallOutput.Status != "incomplete" {
		t.Errorf("错误工具结果的 Status = %q，期望 %q", toolItem.OfFunctionCallOutput.Status, "incomplete")
	}
	if toolItem.OfFunctionCallOutput.Output != "[TOOL_ERROR] 文件不存在: test.go" {
		t.Errorf("错误工具结果的 Output = %q，期望包含 [TOOL_ERROR] 前缀", toolItem.OfFunctionCallOutput.Output)
	}
}

func TestApplyResponsesRequestOptimizations(t *testing.T) {
	params := responses.ResponseNewParams{}
	applyResponsesRequestOptimizations(&params, responsesRequestOptions{
		Provider:        "openai",
		Model:           "gpt-5.4",
		UserID:          "user-123",
		PromptVersions:  []string{"b", "a"},
		Tools:           []mcphost.ToolDefinition{{Name: "tool_search"}, {Name: "memory"}},
		CacheKeyEnabled: true,
		ServiceTier:     "priority",
	})

	if !params.PromptCacheKey.Valid() {
		t.Fatal("Responses prompt_cache_key should be set")
	}
	if got := params.PromptCacheKey.Value; got == "" || got == "user-123" {
		t.Fatalf("Responses prompt_cache_key should be hashed/stable, got %q", got)
	}
	if params.ServiceTier != responses.ResponseNewParamsServiceTierPriority {
		t.Fatalf("Responses service tier = %q, want priority", params.ServiceTier)
	}
}

func TestApplyResponsesRequestOptimizationsSkipsDisabledCacheKey(t *testing.T) {
	params := responses.ResponseNewParams{}
	applyResponsesRequestOptimizations(&params, responsesRequestOptions{
		Provider:        "openai",
		Model:           "gpt-5.4",
		UserID:          "user-123",
		CacheKeyEnabled: false,
		ServiceTier:     "unknown",
	})

	if params.PromptCacheKey.Valid() {
		t.Fatalf("Responses prompt_cache_key should be omitted when disabled, got %q", params.PromptCacheKey.Value)
	}
	if params.ServiceTier != "" {
		t.Fatalf("invalid service tier should be omitted, got %q", params.ServiceTier)
	}
}
