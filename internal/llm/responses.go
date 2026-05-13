package llm

import (
	"context"
	"encoding/json"

	"github.com/openai/openai-go/packages/param"
	"github.com/openai/openai-go/responses"
	"github.com/openai/openai-go/shared"
	"go.uber.org/zap"

	"github.com/chef-guo/agents-hive/internal/errs"
	"github.com/chef-guo/agents-hive/internal/resilience"
)

// ---------------------------------------------------------------------------
// Responses API 实现
// ---------------------------------------------------------------------------

// chatViaResponses 通过 Responses API 实现 Chat 调用。
func (c *Client) chatViaResponses(ctx context.Context, req ChatRequest) (*ChatResponse, error) {
	snapModel, _, _ := c.snapshot()

	// JSON mode fallback prompt
	const jsonFallbackSuffix = "\n\nCRITICAL: 你必须只返回有效的 JSON。不要在 JSON 对象之前或之后包含任何文本。格式示例: {\"key\": \"value\", ...}"

	systemPrompt := req.SystemPrompt
	shouldSkip := req.JSONMode && c.shouldSkipJSONMode()
	if shouldSkip {
		systemPrompt += jsonFallbackSuffix
	}

	// 构建 input items
	input := buildResponsesInput(req.Messages)

	params := responses.ResponseNewParams{
		Model: snapModel,
		Input: responses.ResponseNewParamsInputUnion{
			OfInputItemList: input,
		},
	}

	// 系统提示 → Instructions
	if systemPrompt != "" {
		params.Instructions = param.NewOpt(systemPrompt)
	}

	if req.Temperature > 0 {
		params.Temperature = param.NewOpt(req.Temperature)
	}
	if req.MaxTokens > 0 {
		params.MaxOutputTokens = param.NewOpt(req.MaxTokens)
	}

	// JSON mode → text.format
	if req.JSONMode && !shouldSkip {
		params.Text = responses.ResponseTextConfigParam{
			Format: responses.ResponseFormatTextConfigUnionParam{
				OfJSONObject: &shared.ResponseFormatJSONObjectParam{},
			},
		}
	}

	// 客户端级别 reasoning effort
	if c.reasoningEffort != "" {
		params.Reasoning = shared.ReasoningParam{
			Effort: shared.ReasoningEffort(c.reasoningEffort),
		}
	}

	// 用流式请求替代非流式，避免中转站对非流式请求的限流
	content, finishReason, usage, err := c.responsesStreamCollect(ctx, params)
	if err != nil {
		// 自动回退：如果 JSON mode 失败，去掉 JSON format 重试
		if req.JSONMode && !shouldSkip && isResponseFormatError(err) {
			c.markJSONModeUnsupported()
			params.Text = responses.ResponseTextConfigParam{}
			if req.SystemPrompt != "" {
				params.Instructions = param.NewOpt(req.SystemPrompt + jsonFallbackSuffix)
			}
			content, finishReason, usage, err = c.responsesStreamCollect(ctx, params)
		}
		if err != nil {
			c.logAPIError(err, "responses_chat")
			return nil, errs.Wrap(errs.CodeLLMError, "Responses API 调用失败", err)
		}
	}

	return &ChatResponse{
		Content:      content,
		FinishReason: finishReason,
		Usage:        usage,
	}, nil
}

// responsesStreamCollect 用 NewStreaming 发送请求并把 SSE 事件累积成完整响应。
// 这样所有 Responses API 调用都走流式端点，避免中转站对非流式请求的限流（429）。
func (c *Client) responsesStreamCollect(ctx context.Context, params responses.ResponseNewParams) (content, finishReason string, usage Usage, err error) {
	doAttempt := func() error {
		stream := c.client.Responses.NewStreaming(ctx, params)
		defer stream.Close()

		// 每次尝试前重置所有输出变量，防止重试时混入上次部分写入的脏数据
		var localContent, localFinishReason string
		var localUsage Usage
		for stream.Next() {
			event := stream.Current()
			switch variant := event.AsAny().(type) {
			case responses.ResponseTextDeltaEvent:
				localContent += variant.Delta
			case responses.ResponseCompletedEvent:
				localFinishReason = extractResponsesFinishReason(&variant.Response)
				localUsage = convertResponsesUsage(&variant.Response)
			}
		}
		if err := stream.Err(); err != nil {
			return err
		}
		// 仅在成功时写入外部变量
		content = localContent
		finishReason = localFinishReason
		usage = localUsage
		return nil
	}

	_, err = resilience.Do(ctx, llmRetryPolicy, c.logger, "responses_stream_collect", func() (struct{}, error) {
		return struct{}{}, doAttempt()
	})
	return
}

// chatWithToolsViaResponses 通过 Responses API 实现带工具调用的 Chat。
func (c *Client) chatWithToolsViaResponses(ctx context.Context, req ChatWithToolsRequest) (*ChatWithToolsResponse, error) {
	snapModel, _, snapProvider := c.snapshot()

	// 构建 input items
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

	// 调用 API（带重试）
	resp, err := retryableAPICall(ctx, c.logger, "responses_chat_with_tools", func() (*responses.Response, error) {
		return c.client.Responses.New(ctx, params)
	})
	if err != nil {
		c.logAPIError(err, "responses_chat_with_tools")
		return nil, errs.Wrap(errs.CodeLLMError, "Responses API 调用失败", err)
	}

	// 解析响应
	result := &ChatWithToolsResponse{
		Content:      resp.OutputText(),
		FinishReason: extractResponsesFinishReason(resp),
		Usage:        convertResponsesUsage(resp),
	}

	// 提取工具调用
	for _, item := range resp.Output {
		if item.Type == "function_call" {
			result.ToolCalls = append(result.ToolCalls, ToolCall{
				ID:        item.CallID,
				Name:      item.Name,
				Arguments: json.RawMessage(item.Arguments),
			})
		}
	}

	// 提取 reasoning content
	for _, item := range resp.Output {
		if item.Type == "reasoning" {
			for _, s := range item.Summary {
				if s.Text != "" {
					if result.ReasoningContent != "" {
						result.ReasoningContent += "\n"
					}
					result.ReasoningContent += s.Text
				}
			}
		}
	}

	c.logger.Debug("Responses API 调用完成",
		zap.String("model", snapModel),
		zap.Int("tool_calls", len(result.ToolCalls)),
		zap.Int64("prompt_tokens", result.Usage.PromptTokens),
		zap.Int64("completion_tokens", result.Usage.CompletionTokens),
	)

	return result, nil
}

// ---------------------------------------------------------------------------
// 转换辅助函数
// ---------------------------------------------------------------------------

// buildResponsesInput 将 []Message 转换为 Responses API 的 input items。
func buildResponsesInput(msgs []Message) responses.ResponseInputParam {
	items := make(responses.ResponseInputParam, 0, len(msgs))
	for _, msg := range msgs {
		switch msg.Role {
		case "user":
			items = append(items, responses.ResponseInputItemUnionParam{
				OfMessage: &responses.EasyInputMessageParam{
					Role: responses.EasyInputMessageRoleUser,
					Content: responses.EasyInputMessageContentUnionParam{
						OfString: param.NewOpt(msg.Content.Text()),
					},
				},
			})
		case "assistant":
			items = append(items, responses.ResponseInputItemUnionParam{
				OfMessage: &responses.EasyInputMessageParam{
					Role: responses.EasyInputMessageRoleAssistant,
					Content: responses.EasyInputMessageContentUnionParam{
						OfString: param.NewOpt(msg.Content.Text()),
					},
				},
			})
		case "system":
			items = append(items, responses.ResponseInputItemUnionParam{
				OfMessage: &responses.EasyInputMessageParam{
					Role: responses.EasyInputMessageRoleSystem,
					Content: responses.EasyInputMessageContentUnionParam{
						OfString: param.NewOpt(msg.Content.Text()),
					},
				},
			})
		}
	}
	return items
}

// buildResponsesInputFromToolMessages 将 []MessageWithTools 转换为 Responses API 的 input items。
// 处理包含 tool_calls 的 assistant 消息和 tool 结果消息。
func buildResponsesInputFromToolMessages(msgs []MessageWithTools) responses.ResponseInputParam {
	items := make(responses.ResponseInputParam, 0, len(msgs)*2)

	for _, msg := range msgs {
		switch msg.Role {
		case "user":
			items = append(items, responses.ResponseInputItemUnionParam{
				OfMessage: &responses.EasyInputMessageParam{
					Role: responses.EasyInputMessageRoleUser,
					Content: responses.EasyInputMessageContentUnionParam{
						OfString: param.NewOpt(msg.Content.Text()),
					},
				},
			})

		case "assistant":
			// 如果有文本内容，作为 assistant message
			if text := msg.Content.Text(); text != "" {
				items = append(items, responses.ResponseInputItemUnionParam{
					OfMessage: &responses.EasyInputMessageParam{
						Role: responses.EasyInputMessageRoleAssistant,
						Content: responses.EasyInputMessageContentUnionParam{
							OfString: param.NewOpt(text),
						},
					},
				})
			}

			// 每个 tool call 作为独立的 function_call input item
			for _, tc := range msg.ToolCalls {
				items = append(items, responses.ResponseInputItemUnionParam{
					OfFunctionCall: &responses.ResponseFunctionToolCallParam{
						CallID:    tc.ID,
						Name:      tc.Name,
						Arguments: string(tc.Arguments),
					},
				})
			}

		case "tool":
			// tool 结果 → function_call_output
			output := msg.Content.Text()
			status := "completed"
			if msg.IsError {
				output = "[TOOL_ERROR] " + output
				status = "incomplete"
			}
			items = append(items, responses.ResponseInputItemUnionParam{
				OfFunctionCallOutput: &responses.ResponseInputItemFunctionCallOutputParam{
					CallID: msg.ToolCallID,
					Output: output,
					Status: status,
				},
			})

		case "system":
			items = append(items, responses.ResponseInputItemUnionParam{
				OfMessage: &responses.EasyInputMessageParam{
					Role: responses.EasyInputMessageRoleSystem,
					Content: responses.EasyInputMessageContentUnionParam{
						OfString: param.NewOpt(msg.Content.Text()),
					},
				},
			})
		}
	}
	return items
}

// convertResponsesUsage 将 Responses API 的 usage 转换为内部 Usage 类型。
func convertResponsesUsage(resp *responses.Response) Usage {
	return Usage{
		PromptTokens:     resp.Usage.InputTokens,
		CompletionTokens: resp.Usage.OutputTokens,
		TotalTokens:      resp.Usage.TotalTokens,
	}
}

// extractResponsesFinishReason 从 Responses API 响应中提取结束原因。
func extractResponsesFinishReason(resp *responses.Response) string {
	// Responses API 的 status 字段: "completed", "failed", "incomplete", "in_progress"
	// 如果有 incomplete_details，返回具体原因
	if resp.IncompleteDetails.Reason != "" {
		return resp.IncompleteDetails.Reason
	}

	// 检查是否有工具调用（类似 Chat Completions 的 "tool_calls" finish reason）
	for _, item := range resp.Output {
		if item.Type == "function_call" {
			return "tool_calls"
		}
	}

	return "stop"
}
