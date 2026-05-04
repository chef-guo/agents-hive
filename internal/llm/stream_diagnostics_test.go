package llm

import (
	"testing"

	"github.com/openai/openai-go"
	"github.com/openai/openai-go/responses"
)

func TestSummarizeChatCompletionRawChunk_ToolDelta(t *testing.T) {
	got := summarizeChatCompletionRawChunk(openai.ChatCompletionChunk{
		ID:    "chatcmpl-test",
		Model: "gpt-5.2",
		Choices: []openai.ChatCompletionChunkChoice{
			{
				Delta: openai.ChatCompletionChunkChoiceDelta{
					ToolCalls: []openai.ChatCompletionChunkChoiceDeltaToolCall{
						{
							ID: "call_1",
							Function: openai.ChatCompletionChunkChoiceDeltaToolCallFunction{
								Name:      "glob",
								Arguments: `{"pattern":"*.go"`,
							},
						},
					},
				},
				FinishReason: "tool_calls",
			},
		},
	})

	if got.ChunkID != "chatcmpl-test" {
		t.Fatalf("ChunkID = %q, want chatcmpl-test", got.ChunkID)
	}
	if got.Model != "gpt-5.2" {
		t.Fatalf("Model = %q, want gpt-5.2", got.Model)
	}
	if got.ChoiceCount != 1 {
		t.Fatalf("ChoiceCount = %d, want 1", got.ChoiceCount)
	}
	if got.ContentDeltaLen != 0 {
		t.Fatalf("ContentDeltaLen = %d, want 0", got.ContentDeltaLen)
	}
	if got.ToolCallDeltaCount != 1 {
		t.Fatalf("ToolCallDeltaCount = %d, want 1", got.ToolCallDeltaCount)
	}
	if !got.HasToolCalls {
		t.Fatal("HasToolCalls = false, want true")
	}
	if got.HasText {
		t.Fatal("HasText = true, want false")
	}
	if got.FinishReason != "tool_calls" {
		t.Fatalf("FinishReason = %q, want tool_calls", got.FinishReason)
	}
}

func TestSummarizeChatCompletionRawChunk_UsageOnly(t *testing.T) {
	got := summarizeChatCompletionRawChunk(openai.ChatCompletionChunk{
		Usage: openai.CompletionUsage{TotalTokens: 42},
	})

	if got.ChoiceCount != 0 {
		t.Fatalf("ChoiceCount = %d, want 0", got.ChoiceCount)
	}
	if !got.HasUsage {
		t.Fatal("HasUsage = false, want true")
	}
	if got.HasText || got.HasToolCalls {
		t.Fatalf("usage-only chunk should not be text/tool: %+v", got)
	}
}

func TestSummarizeResponsesRawEvent_TextDelta(t *testing.T) {
	got := summarizeResponsesRawEvent(responses.ResponseStreamEventUnion{
		Type:           "response.output_text.delta",
		SequenceNumber: 7,
		ItemID:         "msg_1",
		Delta: responses.ResponseStreamEventUnionDelta{
			OfString: "你好",
		},
	})

	if got.EventType != "response.output_text.delta" {
		t.Fatalf("EventType = %q, want response.output_text.delta", got.EventType)
	}
	if got.Sequence != 7 {
		t.Fatalf("Sequence = %d, want 7", got.Sequence)
	}
	if got.ItemID != "msg_1" {
		t.Fatalf("ItemID = %q, want msg_1", got.ItemID)
	}
	if got.DeltaLen != len("你好") {
		t.Fatalf("DeltaLen = %d, want %d", got.DeltaLen, len("你好"))
	}
	if !got.HasText {
		t.Fatal("HasText = false, want true")
	}
	if got.HasToolArgs {
		t.Fatal("HasToolArgs = true, want false")
	}
}

func TestSummarizeResponsesRawEvent_ToolArgumentsDelta(t *testing.T) {
	got := summarizeResponsesRawEvent(responses.ResponseStreamEventUnion{
		Type:           "response.function_call_arguments.delta",
		SequenceNumber: 8,
		ItemID:         "fc_1",
		Delta: responses.ResponseStreamEventUnionDelta{
			OfString: `{"path":"README.md"`,
		},
	})

	if got.EventType != "response.function_call_arguments.delta" {
		t.Fatalf("EventType = %q, want response.function_call_arguments.delta", got.EventType)
	}
	if got.Sequence != 8 {
		t.Fatalf("Sequence = %d, want 8", got.Sequence)
	}
	if got.ItemID != "fc_1" {
		t.Fatalf("ItemID = %q, want fc_1", got.ItemID)
	}
	if !got.HasToolArgs {
		t.Fatal("HasToolArgs = false, want true")
	}
	if got.HasText {
		t.Fatal("HasText = true, want false")
	}
}
