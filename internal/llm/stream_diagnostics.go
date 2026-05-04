package llm

import (
	"github.com/openai/openai-go"
	"github.com/openai/openai-go/responses"
)

type chatCompletionRawChunkSummary struct {
	ChunkID            string
	Model              string
	ChoiceCount        int
	ContentDeltaLen    int
	ToolCallDeltaCount int
	HasText            bool
	HasToolCalls       bool
	HasUsage           bool
	FinishReason       string
}

func summarizeChatCompletionRawChunk(chunk openai.ChatCompletionChunk) chatCompletionRawChunkSummary {
	summary := chatCompletionRawChunkSummary{
		ChunkID:     chunk.ID,
		Model:       chunk.Model,
		ChoiceCount: len(chunk.Choices),
		HasUsage:    chunk.Usage.TotalTokens > 0,
	}
	if len(chunk.Choices) == 0 {
		return summary
	}

	choice := chunk.Choices[0]
	summary.ContentDeltaLen = len(choice.Delta.Content)
	summary.ToolCallDeltaCount = len(choice.Delta.ToolCalls)
	summary.HasText = choice.Delta.Content != ""
	summary.HasToolCalls = len(choice.Delta.ToolCalls) > 0
	summary.FinishReason = choice.FinishReason
	return summary
}

type responsesRawEventSummary struct {
	EventType   string
	Sequence    int64
	ItemID      string
	DeltaLen    int
	HasText     bool
	HasToolArgs bool
	HasUsage    bool
}

func summarizeResponsesRawEvent(event responses.ResponseStreamEventUnion) responsesRawEventSummary {
	summary := responsesRawEventSummary{
		EventType: event.Type,
		Sequence:  event.SequenceNumber,
		ItemID:    event.ItemID,
	}

	switch event.Type {
	case "response.output_text.delta", "response.reasoning_summary_text.delta":
		summary.DeltaLen = len(event.Delta.OfString)
		summary.HasText = event.Delta.OfString != ""
	case "response.function_call_arguments.delta":
		summary.DeltaLen = len(event.Delta.OfString)
		summary.HasToolArgs = event.Delta.OfString != ""
	case "response.function_call_arguments.done":
		summary.DeltaLen = len(event.Arguments)
		summary.HasToolArgs = event.Arguments != ""
	case "response.completed":
		summary.HasUsage = event.Response.Usage.TotalTokens > 0
	}

	return summary
}
