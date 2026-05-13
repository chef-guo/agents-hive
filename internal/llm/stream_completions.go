package llm

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/openai/openai-go"
	"go.uber.org/zap"

	"github.com/chef-guo/agents-hive/internal/errs"
)

// pendingToolCall 用于累积流式工具调用增量
type pendingToolCall struct {
	ID        string
	Name      string
	Arguments strings.Builder
}

// chatWithToolsStreamViaCompletions 通过 Chat Completions API 实现流式带工具调用的聊天补全。
func (c *Client) chatWithToolsStreamViaCompletions(ctx context.Context, req ChatWithToolsRequest, onChunk StreamCallback) (*ChatWithToolsResponse, error) {
	// 获取当前 model 快照（线程安全）
	snapModel, _, snapProvider := c.snapshot()

	// 检查模型是否支持 tool use，不支持时记录警告日志
	if meta := GetModelMeta(snapModel); meta != nil && !meta.Capabilities.ToolUse {
		c.logger.Warn("当前模型不支持工具调用，请求可能失败",
			zap.String("model", snapModel),
		)
	}

	// 验证多模态内容是否被 provider 支持
	for _, msg := range req.Messages {
		if msg.Role == "user" {
			if err := c.validateContent(msg.Content, snapProvider); err != nil {
				return nil, err
			}
		}
	}

	// 0. Provider 感知的消息转换（处理不同 Provider 的差异）
	transformedMessages := req.Messages
	if c.transformer != nil {
		providerName := snapProvider.Name
		if providerName == "" {
			providerName = DetectProvider(c.baseURL)
		}
		transformedMessages = c.transformer.Transform(transformedMessages, providerName)
	}

	// 1. 转换 mcphost.ToolDefinition → openai.ChatCompletionTool
	tools, err := convertToolsForChatCompletions(req.Tools)
	if err != nil {
		return nil, err
	}

	// 2. 转换 Messages → openai messages
	var messages []openai.ChatCompletionMessageParamUnion
	if req.SystemPrompt != "" {
		messages = append(messages, openai.SystemMessage(req.SystemPrompt))
	}

	for mi, msg := range transformedMessages {
		switch msg.Role {
		case "system":
			messages = append(messages, openai.SystemMessage(msg.Content.Text()))
		case "user":
			if msg.Content.IsMultimodal() {
				c.logger.Info("[DEBUG-UPLOAD] 发送多模态 user 消息到 LLM",
					zap.Int("msg_index", mi),
					zap.Int("parts", len(msg.Content.Parts())),
				)
			}
			messages = append(messages, toSDKUserMessage(msg.Content))
		case "assistant":
			if len(msg.ToolCalls) > 0 {
				var toolCalls []openai.ChatCompletionMessageToolCallParam
				for _, tc := range msg.ToolCalls {
					tcID := tc.ID
					if len(tcID) > MaxToolCallIDLength {
						tcID = shortenToolCallID(tcID)
						c.logger.Info("截断超长 tool_call_id (assistant)",
							zap.Int("original_length", len(tc.ID)),
							zap.String("shortened", tcID),
							zap.String("tool_name", tc.Name),
						)
					}
					toolCalls = append(toolCalls, openai.ChatCompletionMessageToolCallParam{
						ID: tcID,
						Function: openai.ChatCompletionMessageToolCallFunctionParam{
							Name:      tc.Name,
							Arguments: string(tc.Arguments),
						},
					})
				}

				assistantMsg := openai.ChatCompletionAssistantMessageParam{
					ToolCalls: toolCalls,
				}
				if text := msg.Content.Text(); text != "" {
					assistantMsg.Content = openai.ChatCompletionAssistantMessageParamContentUnion{
						OfString: openai.String(text),
					}
				}
				messages = append(messages, openai.ChatCompletionMessageParamUnion{
					OfAssistant: &assistantMsg,
				})
			} else {
				messages = append(messages, openai.AssistantMessage(msg.Content.Text()))
			}
		case "tool":
			toolCallID := msg.ToolCallID
			if len(toolCallID) > MaxToolCallIDLength {
				toolCallID = shortenToolCallID(toolCallID)
				c.logger.Info("截断超长 tool_call_id (tool)",
					zap.Int("original_length", len(msg.ToolCallID)),
					zap.String("shortened", toolCallID),
				)
			}
			content := msg.Content.Text()
			if msg.IsError {
				content = "[TOOL_ERROR] " + content
			}
			messages = append(messages, openai.ToolMessage(content, toolCallID))
		}
	}

	// 3. 构建请求参数
	params := openai.ChatCompletionNewParams{
		Model:    snapModel,
		Messages: messages,
	}

	if len(tools) > 0 {
		params.Tools = tools
	}
	// P0-A：ToolChoice 透传（空字符串时跳过，保持旧 auto 行为）
	if tc, ok := buildChatCompletionsToolChoice(req.ToolChoice); ok {
		params.ToolChoice = tc
	}
	if req.Temperature > 0 {
		params.Temperature = openai.Float(req.Temperature)
	}
	if req.MaxTokens > 0 {
		params.MaxTokens = openai.Int(req.MaxTokens)
	}

	// 设置流式选项，以便在最后一个 chunk 获取 usage 信息
	params.StreamOptions = openai.ChatCompletionStreamOptionsParam{
		IncludeUsage: openai.Bool(true),
	}

	// Info level（原 Debug）：诊断"模型不调工具"问题必须拿到这条数据。
	// tool_count=0 表示 tools 在某处被丢了（transformer / provider config）；
	// tool_count=N 但 finish_reason=stop tool_calls=0 → 代理没转发 or 模型决策。
	toolNames := make([]string, 0, len(tools))
	for i, t := range tools {
		if i >= 5 {
			break
		}
		toolNames = append(toolNames, t.Function.Name)
	}
	c.logger.Info("发送流式带工具的聊天补全请求",
		zap.String("model", snapModel),
		zap.Int("message_count", len(messages)),
		zap.Int("tool_count", len(tools)),
		zap.Strings("tool_sample", toolNames),
	)

	// 3.5 请求级转换（缓存控制、推理参数等）
	providerName := snapProvider.Name
	if providerName == "" {
		providerName = DetectProvider(c.baseURL)
	}
	if c.requestTransformer != nil {
		c.requestTransformer.TransformRequest(&RequestTransformContext{
			Provider: providerName,
			Model:    snapModel,
			Messages: messages,
			Params:   &params,
			CacheKey: stablePromptCacheKey(snapModel, req.UserID, req.PromptVersions, req.Tools),
		})
		params.Messages = messages
	}

	// 3.6 单次请求推理努力级别覆盖
	if req.ReasoningEffort != "" {
		overrideTransformer := NewReasoningVariantsTransformer(req.ReasoningEffort, c.logger)
		overrideTransformer.TransformRequest(&RequestTransformContext{
			Provider: providerName,
			Model:    snapModel,
			Messages: messages,
			Params:   &params,
		})
		c.logger.Debug("单次请求推理努力级别覆盖",
			zap.String("effort", req.ReasoningEffort),
		)
	}

	// 4. 发送流式请求（不使用 retryableAPICall）
	streamStart := time.Now()
	stream := c.client.Chat.Completions.NewStreaming(ctx, params)
	c.logger.Info("[stream-diag] Chat Completions 流式请求已创建",
		zap.String("model", snapModel),
		zap.String("provider", providerName),
		zap.Int("message_count", len(messages)),
		zap.Int("tool_count", len(tools)),
		zap.String("tool_choice", req.ToolChoice),
		zap.String("reasoning_effort", req.ReasoningEffort),
		zap.Int64("create_stream_ms", time.Since(streamStart).Milliseconds()),
	)
	defer stream.Close()

	// 累积器
	var contentBuf strings.Builder   // 原始内容（含 <think> 标签）
	var visibleBuf strings.Builder   // 过滤 <think> 后的可见内容
	var reasoningBuf strings.Builder // <think> 内的推理内容
	var pendingCalls []pendingToolCall
	var finishReason string
	var usage Usage
	insideThink := false       // 是否正在 <think> 块内
	var tagBuf strings.Builder // 用于检测 <think> 和 </think> 标签的缓冲区
	var rawChunkCount int
	var firstRawChunkAt time.Time

	for stream.Next() {
		chunk := stream.Current()
		rawChunkCount++
		rawSummary := summarizeChatCompletionRawChunk(chunk)
		if rawChunkCount == 1 {
			firstRawChunkAt = time.Now()
			c.logger.Info("[stream-diag] 首个 SDK 原始 chunk 抵达",
				zap.String("model", snapModel),
				zap.String("provider", providerName),
				zap.Int64("ttfb_ms", firstRawChunkAt.Sub(streamStart).Milliseconds()),
				zap.String("chunk_id", rawSummary.ChunkID),
				zap.String("chunk_model", rawSummary.Model),
				zap.Int("choice_count", rawSummary.ChoiceCount),
				zap.Bool("has_text", rawSummary.HasText),
				zap.Bool("has_tool_calls", rawSummary.HasToolCalls),
				zap.Bool("has_usage", rawSummary.HasUsage),
				zap.Int("content_delta_len", rawSummary.ContentDeltaLen),
				zap.Int("tool_call_delta_count", rawSummary.ToolCallDeltaCount),
				zap.String("finish_reason", rawSummary.FinishReason),
			)
		} else if rawChunkCount <= 3 || rawChunkCount%20 == 0 {
			c.logger.Debug("[stream-diag] SDK 原始 chunk 抵达",
				zap.String("model", snapModel),
				zap.String("provider", providerName),
				zap.Int("n", rawChunkCount),
				zap.Int64("elapsed_since_start_ms", time.Since(streamStart).Milliseconds()),
				zap.Int64("elapsed_since_first_ms", time.Since(firstRawChunkAt).Milliseconds()),
				zap.Int("choice_count", rawSummary.ChoiceCount),
				zap.Bool("has_text", rawSummary.HasText),
				zap.Bool("has_tool_calls", rawSummary.HasToolCalls),
				zap.Bool("has_usage", rawSummary.HasUsage),
				zap.Int("content_delta_len", rawSummary.ContentDeltaLen),
				zap.Int("tool_call_delta_count", rawSummary.ToolCallDeltaCount),
				zap.String("finish_reason", rawSummary.FinishReason),
			)
		}

		// 处理 usage（最后一个 chunk，choices 可能为空）
		if chunk.Usage.TotalTokens > 0 {
			usage = Usage{
				PromptTokens:     chunk.Usage.PromptTokens,
				CompletionTokens: chunk.Usage.CompletionTokens,
				TotalTokens:      chunk.Usage.TotalTokens,
			}
		}

		if len(chunk.Choices) == 0 {
			continue
		}

		choice := chunk.Choices[0]
		delta := choice.Delta

		// 累积文本内容
		contentDelta := delta.Content
		if contentDelta != "" {
			contentBuf.WriteString(contentDelta)

			// 实时过滤 <think> 标签：逐字符处理以应对标签跨 chunk 的情况
			for _, ch := range contentDelta {
				if insideThink {
					tagBuf.WriteRune(ch)
					candidate := tagBuf.String()
					if strings.HasSuffix(candidate, "</think>") {
						// 闭合标签完成，将 tagBuf 中除去 </think> 的部分写入推理
						reasoning := candidate[:len(candidate)-len("</think>")]
						reasoningBuf.WriteString(reasoning)
						tagBuf.Reset()
						insideThink = false
					} else if len(candidate) >= 8 && !strings.HasPrefix("</think>", candidate[len(candidate)-min(len(candidate), 8):]) {
						// 不可能是 </think> 前缀了，刷出累积内容到推理
						reasoningBuf.WriteString(candidate)
						tagBuf.Reset()
					}
				} else {
					tagBuf.WriteRune(ch)
					candidate := tagBuf.String()
					if strings.HasSuffix(candidate, "<think>") {
						// 开始标签完成，将 tagBuf 中除去 <think> 的部分写入可见内容
						visible := candidate[:len(candidate)-len("<think>")]
						visibleBuf.WriteString(visible)
						tagBuf.Reset()
						insideThink = true
					} else if len(candidate) >= 7 && !strings.HasPrefix("<think>", candidate[len(candidate)-min(len(candidate), 7):]) {
						// 不可能是 <think> 前缀了，刷出累积内容到可见
						visibleBuf.WriteString(candidate)
						tagBuf.Reset()
					}
				}
			}
		}

		// 累积工具调用增量
		for _, tc := range delta.ToolCalls {
			idx := int(tc.Index)
			// 扩展 pendingCalls 切片以容纳新索引
			for len(pendingCalls) <= idx {
				pendingCalls = append(pendingCalls, pendingToolCall{})
			}
			if tc.ID != "" {
				pendingCalls[idx].ID = tc.ID
			}
			if tc.Function.Name != "" {
				pendingCalls[idx].Name = tc.Function.Name
			}
			if tc.Function.Arguments != "" {
				pendingCalls[idx].Arguments.WriteString(tc.Function.Arguments)
			}
		}

		// 流中 emit tool calls：累积的 pendingCalls 立即通知上层做诊断/预览。
		// 注意：此处参数可能还是部分 JSON，上层不能在非 Done chunk 上执行工具。
		if len(pendingCalls) > 0 && onChunk != nil {
			// 构建当前累积的 tool calls 数组
			curToolCalls := make([]ToolCall, 0, len(pendingCalls))
			for _, pc := range pendingCalls {
				if pc.ID != "" {
					curToolCalls = append(curToolCalls, ToolCall{
						ID:        pc.ID,
						Name:      pc.Name,
						Arguments: json.RawMessage(pc.Arguments.String()),
					})
				}
			}
			// 通知回调（ToolCalls 累积内容，Done=false 表示非最终）
			if err := onChunk(StreamChunk{
				ContentSoFar: visibleBuf.String(),
				ToolCalls:    curToolCalls,
				Done:         false,
			}); err != nil {
				stream.Close()
				return nil, errs.Wrap(errs.CodePlanGenFailed, "流式回调中断", err)
			}
		}

		// 记录 finish reason
		if choice.FinishReason != "" {
			finishReason = choice.FinishReason
		}

		// 回调通知（非最终 chunk）— 推送过滤后的可见内容和推理内容
		if contentDelta != "" && onChunk != nil {
			visibleContent := visibleBuf.String()
			reasonContent := reasoningBuf.String()
			if visibleContent != "" || reasonContent != "" {
				if err := onChunk(StreamChunk{
					ContentDelta:     contentDelta,
					ContentSoFar:     visibleContent,
					ReasoningContent: reasonContent,
				}); err != nil {
					stream.Close()
					return nil, errs.Wrap(errs.CodePlanGenFailed, "流式回调中断", err)
				}
			}
		}
	}

	// 流结束后，刷出 tagBuf 中的残留内容（未闭合的标签片段）
	if tagBuf.Len() > 0 {
		if insideThink {
			reasoningBuf.WriteString(tagBuf.String())
		} else {
			visibleBuf.WriteString(tagBuf.String())
		}
	}

	if err := stream.Err(); err != nil {
		c.logAPIError(err, "chat_completion_stream_with_tools")
		return nil, errs.Wrap(errs.CodePlanGenFailed, "流式聊天补全失败", err)
	}
	if rawChunkCount > 0 {
		c.logger.Info("[stream-diag] SDK 原始流结束汇总",
			zap.String("model", snapModel),
			zap.String("provider", providerName),
			zap.Int("raw_chunks", rawChunkCount),
			zap.Int64("total_ms", time.Since(streamStart).Milliseconds()),
			zap.Int64("sdk_ttfb_ms", firstRawChunkAt.Sub(streamStart).Milliseconds()),
			zap.Int64("sdk_stream_span_ms", time.Since(firstRawChunkAt).Milliseconds()),
		)
	} else {
		c.logger.Warn("[stream-diag] SDK 原始流未收到任何 chunk",
			zap.String("model", snapModel),
			zap.String("provider", providerName),
			zap.Int64("total_ms", time.Since(streamStart).Milliseconds()),
		)
	}

	// 处理累积的内容：提取推理内容
	rawContent := contentBuf.String()
	cleanedContent, reasoning := stripThinkTags(rawContent)

	// 构建最终的工具调用列表
	var toolCalls []ToolCall
	for _, pc := range pendingCalls {
		if pc.ID != "" {
			if len(pc.ID) > MaxToolCallIDLength {
				c.logger.Warn("收到超长 tool_call_id，将在发送时截断",
					zap.Int("id_length", len(pc.ID)),
					zap.String("tool_name", pc.Name),
					zap.String("id_preview", pc.ID[:64]+"..."),
				)
			}
			toolCalls = append(toolCalls, ToolCall{
				ID:        pc.ID,
				Name:      pc.Name,
				Arguments: json.RawMessage(pc.Arguments.String()),
			})
		}
	}

	// 构建最终响应
	result := &ChatWithToolsResponse{
		Content:          cleanedContent,
		ReasoningContent: reasoning,
		FinishReason:     finishReason,
		Usage:            usage,
		ToolCalls:        toolCalls,
	}

	// 发送最终 chunk
	if onChunk != nil {
		if err := onChunk(StreamChunk{
			ContentDelta:     "",
			ContentSoFar:     cleanedContent,
			ReasoningContent: reasoning,
			ToolCalls:        toolCalls,
			FinishReason:     finishReason,
			Usage:            usage,
			Done:             true,
		}); err != nil {
			return nil, errs.Wrap(errs.CodePlanGenFailed, "流式最终回调失败", err)
		}
	}

	c.logger.Debug("流式聊天补全完成",
		zap.Int64("prompt_tokens", result.Usage.PromptTokens),
		zap.Int64("completion_tokens", result.Usage.CompletionTokens),
		zap.Int("tool_calls", len(result.ToolCalls)),
	)

	return result, nil
}
