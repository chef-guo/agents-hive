package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/openai/openai-go/packages/param"
	"github.com/openai/openai-go/responses"
	"github.com/openai/openai-go/shared"
	"go.uber.org/zap"

	"github.com/chef-guo/agents-hive/internal/errs"
)

// ---------------------------------------------------------------------------
// Responses API 流式实现
// ---------------------------------------------------------------------------

// responsesPendingToolCall 跟踪 Responses API 流式过程中正在构建的工具调用。
type responsesPendingToolCall struct {
	CallID    string
	Name      string
	Arguments string
}

func responsesRequestPayloadBytes(params responses.ResponseNewParams) (int, bool) {
	// NewStreaming 通过 SDK request option 注入 stream=true；这里只在副本上补齐用于诊断。
	params.SetExtraFields(map[string]any{"stream": true})
	payload, err := json.Marshal(params)
	if err != nil {
		return 0, false
	}
	return len(payload), true
}

func responsesAPIFormatForDiag(provider ProviderDef) string {
	if provider.APIFormat == "" {
		return "unset"
	}
	return provider.APIFormat
}

func responsesServiceTierForDiag(params responses.ResponseNewParams) string {
	if params.ServiceTier == "" {
		return "unset"
	}
	return string(params.ServiceTier)
}

// chatWithToolsStreamViaResponses 通过 Responses API 流式实现带工具调用的 Chat。
func (c *Client) chatWithToolsStreamViaResponses(ctx context.Context, req ChatWithToolsRequest, onChunk StreamCallback) (*ChatWithToolsResponse, error) {
	snapModel, snapBaseURL, snapProvider := c.snapshot()

	// 构建 input items（与 chatWithToolsViaResponses 相同）
	input := buildResponsesInputFromToolMessages(req.Messages)

	// 构建工具定义
	tools, err := convertToolsForResponses(req.Tools)
	if err != nil {
		return nil, err
	}

	params := responses.ResponseNewParams{
		Model: snapModel,
		Input: responses.ResponseNewParamsInputUnion{
			OfInputItemList: input,
		},
	}
	if len(tools) > 0 {
		params.Tools = tools
	}

	// 系统提示 → Instructions
	if req.SystemPrompt != "" {
		params.Instructions = param.NewOpt(req.SystemPrompt)
	}

	if req.Temperature > 0 {
		params.Temperature = param.NewOpt(req.Temperature)
	}
	if req.MaxTokens > 0 {
		params.MaxOutputTokens = param.NewOpt(req.MaxTokens)
	}
	applyResponsesRequestOptimizations(&params, responsesRequestOptions{
		Provider:        snapProvider.Name,
		Model:           snapModel,
		UserID:          req.UserID,
		PromptVersions:  req.PromptVersions,
		Tools:           req.Tools,
		CacheKeyEnabled: c.promptCacheKey,
		ServiceTier:     c.serviceTier,
	})

	// P0-A：ToolChoice 透传（空字符串时跳过，保持旧 auto 行为）
	if tc, ok := buildResponsesToolChoice(req.ToolChoice); ok {
		params.ToolChoice = tc
	}

	// 推理努力级别：单次请求覆盖 > 客户端默认值
	effort := c.reasoningEffort
	if req.ReasoningEffort != "" {
		effort = req.ReasoningEffort
	}
	if effort != "" {
		params.Reasoning = shared.ReasoningParam{
			Effort: shared.ReasoningEffort(effort),
		}
	}

	requestPayloadBytes, hasRequestPayloadBytes := responsesRequestPayloadBytes(params)

	// 启动流式请求
	streamStart := time.Now()
	stream := c.client.Responses.NewStreaming(ctx, params)
	diagFields := []zap.Field{
		zap.String("model", snapModel),
		zap.String("base_url", snapBaseURL),
		zap.String("api_format", responsesAPIFormatForDiag(snapProvider)),
		zap.Int("input_count", len(input)),
		zap.Int("tool_count", len(tools)),
		zap.String("tool_choice", req.ToolChoice),
		zap.String("reasoning_effort", effort),
		zap.String("service_tier", responsesServiceTierForDiag(params)),
		zap.Bool("prompt_cache_key_present", params.PromptCacheKey.Valid()),
		zap.Int64("create_stream_ms", time.Since(streamStart).Milliseconds()),
	}
	if hasRequestPayloadBytes {
		diagFields = append(diagFields, zap.Int("request_payload_bytes", requestPayloadBytes))
	}
	c.logger.Info("[stream-diag] Responses 流式请求已创建", diagFields...)
	defer stream.Close()

	// 累积状态
	var (
		contentSoFar     string
		reasoningContent string
		finishReason     string
		usage            Usage
		toolCalls        []ToolCall
		// pendingCalls 按 ItemID 索引正在构建的工具调用
		pendingCalls    = make(map[string]*responsesPendingToolCall)
		rawEventCount   int
		firstRawEventAt time.Time
	)

	for stream.Next() {
		event := stream.Current()
		rawEventCount++
		rawSummary := summarizeResponsesRawEvent(event)
		if rawEventCount == 1 {
			firstRawEventAt = time.Now()
			c.logger.Info("[stream-diag] 首个 Responses 原始 event 抵达",
				zap.String("model", snapModel),
				zap.Int64("ttfb_ms", firstRawEventAt.Sub(streamStart).Milliseconds()),
				zap.String("event_type", rawSummary.EventType),
				zap.Int64("sequence", rawSummary.Sequence),
				zap.String("item_id", rawSummary.ItemID),
				zap.Bool("has_text", rawSummary.HasText),
				zap.Bool("has_tool_args", rawSummary.HasToolArgs),
				zap.Bool("has_usage", rawSummary.HasUsage),
				zap.Int("delta_len", rawSummary.DeltaLen),
			)
		} else if rawEventCount <= 3 || rawEventCount%20 == 0 {
			c.logger.Debug("[stream-diag] Responses 原始 event 抵达",
				zap.String("model", snapModel),
				zap.Int("n", rawEventCount),
				zap.Int64("elapsed_since_start_ms", time.Since(streamStart).Milliseconds()),
				zap.Int64("elapsed_since_first_ms", time.Since(firstRawEventAt).Milliseconds()),
				zap.String("event_type", rawSummary.EventType),
				zap.Int64("sequence", rawSummary.Sequence),
				zap.String("item_id", rawSummary.ItemID),
				zap.Bool("has_text", rawSummary.HasText),
				zap.Bool("has_tool_args", rawSummary.HasToolArgs),
				zap.Bool("has_usage", rawSummary.HasUsage),
				zap.Int("delta_len", rawSummary.DeltaLen),
			)
		}

		switch variant := event.AsAny().(type) {
		case responses.ResponseTextDeltaEvent:
			// 文本增量
			contentSoFar += variant.Delta
			if onChunk != nil {
				if err := onChunk(StreamChunk{
					ContentDelta: variant.Delta,
					ContentSoFar: contentSoFar,
				}); err != nil {
					return nil, errs.Wrap(errs.CodeLLMError, "流式回调中断", err)
				}
			}

		case responses.ResponseOutputItemAddedEvent:
			// 新的输出项被添加（可能是 function_call）
			if variant.Item.Type == "function_call" {
				pendingCalls[variant.Item.ID] = &responsesPendingToolCall{
					CallID: variant.Item.CallID,
					Name:   variant.Item.Name,
				}
			}

		case responses.ResponseFunctionCallArgumentsDeltaEvent:
			// 工具调用参数增量
			if pc, ok := pendingCalls[variant.ItemID]; ok {
				pc.Arguments += variant.Delta
			}

		case responses.ResponseFunctionCallArgumentsDoneEvent:
			// 工具调用参数完成
			if pc, ok := pendingCalls[variant.ItemID]; ok {
				tc := ToolCall{
					ID:        pc.CallID,
					Name:      pc.Name,
					Arguments: json.RawMessage(variant.Arguments),
				}
				toolCalls = append(toolCalls, tc)
				delete(pendingCalls, variant.ItemID)

				// 通知回调有新的工具调用
				if onChunk != nil {
					if err := onChunk(StreamChunk{
						ContentSoFar: contentSoFar,
						ToolCalls:    toolCalls,
					}); err != nil {
						return nil, errs.Wrap(errs.CodeLLMError, "流式回调中断", err)
					}
				}
			}

		case responses.ResponseReasoningSummaryTextDeltaEvent:
			// 推理摘要文本增量
			reasoningContent += variant.Delta
			if onChunk != nil {
				if err := onChunk(StreamChunk{
					ContentSoFar:     contentSoFar,
					ReasoningContent: reasoningContent,
				}); err != nil {
					return nil, errs.Wrap(errs.CodeLLMError, "流式回调中断", err)
				}
			}

		case responses.ResponseCompletedEvent:
			// 流完成，提取 usage 和 finish reason
			resp := &variant.Response
			usage = convertResponsesUsage(resp)
			finishReason = extractResponsesFinishReason(resp)

			// 发送最终 Done 块
			if onChunk != nil {
				if err := onChunk(StreamChunk{
					ContentSoFar:     contentSoFar,
					ReasoningContent: reasoningContent,
					ToolCalls:        toolCalls,
					FinishReason:     finishReason,
					Usage:            usage,
					Done:             true,
				}); err != nil {
					return nil, errs.Wrap(errs.CodeLLMError, "流式回调中断", err)
				}
			}

		case responses.ResponseErrorEvent:
			// 流内错误事件
			return nil, errs.New(errs.CodeLLMError, fmt.Sprintf("Responses API 流式错误: %s", event.RawJSON()))

		default:
			// 忽略其他事件类型（response.created, response.in_progress, etc.）
		}
	}

	// 检查流错误
	if err := stream.Err(); err != nil {
		c.logAPIError(err, "responses_stream_chat_with_tools")
		return nil, errs.Wrap(errs.CodeLLMError, "Responses API 流式调用失败", err)
	}
	if rawEventCount > 0 {
		c.logger.Info("[stream-diag] Responses 原始流结束汇总",
			zap.String("model", snapModel),
			zap.Int("raw_events", rawEventCount),
			zap.Int64("total_ms", time.Since(streamStart).Milliseconds()),
			zap.Int64("responses_ttfb_ms", firstRawEventAt.Sub(streamStart).Milliseconds()),
			zap.Int64("responses_stream_span_ms", time.Since(firstRawEventAt).Milliseconds()),
		)
	} else {
		c.logger.Warn("[stream-diag] Responses 原始流未收到任何 event",
			zap.String("model", snapModel),
			zap.Int64("total_ms", time.Since(streamStart).Milliseconds()),
		)
	}

	c.logger.Debug("Responses API 流式调用完成",
		zap.String("model", snapModel),
		zap.Int("tool_calls", len(toolCalls)),
		zap.Int64("prompt_tokens", usage.PromptTokens),
		zap.Int64("completion_tokens", usage.CompletionTokens),
	)

	return &ChatWithToolsResponse{
		Content:          contentSoFar,
		ReasoningContent: reasoningContent,
		ToolCalls:        toolCalls,
		FinishReason:     finishReason,
		Usage:            usage,
	}, nil
}
