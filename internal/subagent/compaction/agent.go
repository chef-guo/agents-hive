package compaction

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/chef-guo/agents-hive/internal/config"
	"github.com/chef-guo/agents-hive/internal/errs"
	"github.com/chef-guo/agents-hive/internal/llm"
	"github.com/chef-guo/agents-hive/internal/memory"
	"github.com/chef-guo/agents-hive/internal/subagent"
)

// Agent 异步后台上下文压缩代理
type Agent struct {
	*subagent.BaseAgent
	llm           *llm.Client
	llmResolver   subagent.LLMClientResolver // 动态获取 LLM client（优先于静态 llm）
	cfg           config.CompactionConfig
	tokenCounter  *llm.TokenCounter
	memExtractor  memory.MemoryExtractor       // 记忆提取器（可选，从压缩摘要中自动提取记忆）
	llmCompleteFn subagent.LLMCompleteCallback // LLM 用量回调（可选）
	promptLoader  any                          // PromptLoader（可选，用于 prompt 外部化）
	sessionID     string                       // 当前任务的会话 ID（由 handleTask 从 TaskRequest 中提取）
	userID        string                       // 用户 ID（由 handleTask 从 TaskRequest 中提取）
	logger        *zap.Logger
}

// SetPromptLoader 设置 Prompt 外部化加载器（可选，nil 时使用硬编码默认值）
func (a *Agent) SetPromptLoader(loader any) {
	a.promptLoader = loader
}

// loadPrompt 从 PromptLoader 加载 prompt，nil 时返回 defaultVal
func (a *Agent) loadPrompt(key, defaultVal string) string {
	if a.promptLoader == nil {
		return defaultVal
	}
	type loader interface {
		LoadOrDefault(string, string) string
	}
	if l, ok := a.promptLoader.(loader); ok {
		return l.LoadOrDefault(key, defaultVal)
	}
	return defaultVal
}

// New 创建一个新的 Compaction Agent
// callbacks 可选：传入 AgentCallbacks 以启用 LLM 用量统计回调
func New(llmClient *llm.Client, compactionCfg config.CompactionConfig, logger *zap.Logger, callbacks ...subagent.AgentCallbacks) *Agent {
	card := subagent.AgentCard{
		ID:          "compaction",
		Name:        "Compaction Agent",
		Description: "异步后台上下文压缩代理",
	}

	agent := &Agent{
		llm:    llmClient,
		cfg:    compactionCfg,
		logger: logger,
	}
	if len(callbacks) > 0 && callbacks[0].LLMCompleteFn != nil {
		agent.llmCompleteFn = callbacks[0].LLMCompleteFn
	}

	// 初始化 TokenCounter（失败则为 nil，降级到启发式估算）
	if compactionCfg.UseTiktoken {
		tc, err := llm.NewTokenCounter()
		if err != nil {
			logger.Warn("tiktoken 初始化失败，降级到启发式估算", zap.Error(err))
		} else {
			agent.tokenCounter = tc
		}
	}

	agent.BaseAgent = subagent.NewBaseAgent(card, agent.handleTask, nil, logger)
	return agent
}

// NewWithResolver 创建使用动态 LLM 路由的 Compaction Agent。
func NewWithResolver(resolver subagent.LLMClientResolver, compactionCfg config.CompactionConfig, logger *zap.Logger, callbacks ...subagent.AgentCallbacks) *Agent {
	card := subagent.AgentCard{
		ID:          "compaction",
		Name:        "Compaction Agent",
		Description: "异步后台上下文压缩代理",
	}

	agent := &Agent{
		llmResolver: resolver,
		cfg:         compactionCfg,
		logger:      logger,
	}
	if len(callbacks) > 0 && callbacks[0].LLMCompleteFn != nil {
		agent.llmCompleteFn = callbacks[0].LLMCompleteFn
	}

	if compactionCfg.UseTiktoken {
		tc, err := llm.NewTokenCounter()
		if err != nil {
			logger.Warn("tiktoken 初始化失败，降级到启发式估算", zap.Error(err))
		} else {
			agent.tokenCounter = tc
		}
	}

	agent.BaseAgent = subagent.NewBaseAgent(card, agent.handleTask, nil, logger)
	return agent
}

// resolveLLM 获取当前 LLM client（优先 resolver，fallback 到静态 client）
func (a *Agent) resolveLLM() *llm.Client {
	if a.llmResolver != nil {
		if c := a.llmResolver(); c != nil {
			return c
		}
	}
	return a.llm
}

// SetMemoryExtractor 设置记忆提取器，使压缩完成后自动提取记忆
func (a *Agent) SetMemoryExtractor(ext memory.MemoryExtractor) {
	a.memExtractor = ext
}

// handleTask 处理压缩任务
func (a *Agent) handleTask(ctx context.Context, req subagent.TaskRequest) subagent.TaskResponse {
	a.sessionID = req.SessionID
	a.userID = req.UserID
	payload, _ := subagent.ExtractPayload(req)

	var compReq CompactionRequest
	if err := json.Unmarshal(payload, &compReq); err != nil {
		return subagent.TaskResponse{
			Status: "failed",
			Error:  fmt.Sprintf("解析压缩请求失败: %v", err),
		}
	}

	// 反序列化 messages
	var messages []llm.MessageWithTools
	if err := json.Unmarshal(compReq.Messages, &messages); err != nil {
		return subagent.TaskResponse{
			Status: "failed",
			Error:  fmt.Sprintf("反序列化消息失败: %v", err),
		}
	}

	// 执行压缩
	result, stats := a.compact(ctx, messages)

	// 自动提取记忆（LLM 摘要成功且有压缩时）
	if a.memExtractor != nil && stats != nil && stats.Compressed > 0 && stats.Strategy == string(config.StrategyLLMSummary) {
		// 从压缩结果的第一条 system 消息中提取摘要文本
		if len(result) > 0 && result[0].Role == "system" {
			summaryText := result[0].Content.Text()
			sessionID := compReq.SessionID
			memCtx := memory.WithRuntimeContext(ctx, memory.RuntimeContext{
				UserID:    a.userID,
				SessionID: sessionID,
				AgentName: a.ID(),
				TaskType:  "compaction",
			})
			if err := a.memExtractor.ExtractFromSummary(memCtx, summaryText, sessionID, a.userID); err != nil {
				a.logger.Warn("自动提取记忆失败", zap.Error(err))
			}
		}
	}

	// 序列化压缩后的消息
	resultJSON, err := json.Marshal(result)
	if err != nil {
		return subagent.TaskResponse{
			Status: "failed",
			Error:  fmt.Sprintf("序列化压缩结果失败: %v", err),
		}
	}

	compResult := CompactionResult{
		Messages: resultJSON,
		Stats:    stats,
	}

	respJSON, err := json.Marshal(compResult)
	if err != nil {
		return subagent.TaskResponse{
			Status: "failed",
			Error:  fmt.Sprintf("序列化压缩响应失败: %v", err),
		}
	}
	return subagent.TaskResponse{
		Status: "completed",
		Result: respJSON,
	}
}

// compact 执行核心压缩逻辑（独立实现，不 import master 包）
func (a *Agent) compact(ctx context.Context, messages []llm.MessageWithTools) ([]llm.MessageWithTools, *Stats) {
	if len(messages) == 0 {
		return messages, &Stats{}
	}

	cfg := a.cfg
	if !cfg.Enabled {
		return messages, &Stats{
			Original:  len(messages),
			Remaining: len(messages),
		}
	}

	// 1. 计算总 token 数
	totalTokens := a.countTokens(messages)

	// 2. 懒惰模式：检查是否需要压缩
	if cfg.LazyMode {
		if totalTokens <= cfg.LazyThreshold {
			return messages, &Stats{
				Original:       len(messages),
				Remaining:      len(messages),
				OriginalToken:  totalTokens,
				RemainingToken: totalTokens,
				LazySkipped:    true,
			}
		}
	}

	// 3. 如果未超过 MaxTokens，直接返回
	if totalTokens <= cfg.MaxTokens {
		return messages, &Stats{
			Original:       len(messages),
			Remaining:      len(messages),
			OriginalToken:  totalTokens,
			RemainingToken: totalTokens,
		}
	}

	// 4. 超限：根据策略压缩
	if cfg.Strategy == config.StrategyLLMSummary {
		client := a.resolveLLM()
		if client != nil {
			result, stats, err := a.compactLLMSummary(ctx, client, messages)
			if err != nil {
				a.logger.Warn("LLM 摘要失败，降级到简单截断",
					zap.Error(err),
					zap.String("strategy", "llm_summary"),
				)
				return a.compactTruncate(messages)
			}
			return result, stats
		}
	}

	return a.compactTruncate(messages)
}

// countTokens 计算消息列表的 token 数
func (a *Agent) countTokens(messages []llm.MessageWithTools) int {
	if a.cfg.UseTiktoken && a.tokenCounter != nil {
		return a.tokenCounter.CountMessages(messages)
	}
	return llm.EstimateMessagesTokens(messages)
}

// countMessageTokens 计算单条消息的 token 数
func (a *Agent) countMessageTokens(msg llm.MessageWithTools) int {
	if a.cfg.UseTiktoken && a.tokenCounter != nil {
		return a.tokenCounter.CountMessage(msg)
	}
	return llm.EstimateMessageTokens(msg)
}

// compactTruncate 使用简单截断策略压缩
func (a *Agent) compactTruncate(messages []llm.MessageWithTools) ([]llm.MessageWithTools, *Stats) {
	// 先计算原始总 token 数
	originalTokens := a.countTokens(messages)

	// 从后往前累计 token，确定截断边界
	totalTokens := 0
	cutoffIndex := 0

	for i := len(messages) - 1; i >= 0; i-- {
		msgTokens := a.countMessageTokens(messages[i])
		if totalTokens+msgTokens > a.cfg.MaxTokens {
			cutoffIndex = i + 1
			break
		}
		totalTokens += msgTokens
	}

	if cutoffIndex == 0 {
		return messages, &Stats{
			Original:       len(messages),
			Remaining:      len(messages),
			OriginalToken:  originalTokens,
			RemainingToken: originalTokens,
		}
	}

	// 生成简单摘要 + 保留最近消息
	olderMessages := messages[:cutoffIndex]
	recentMessages := messages[cutoffIndex:]

	summary := generateSimpleSummary(olderMessages)
	summaryMessage := llm.MessageWithTools{
		Role:    "system",
		Content: llm.NewTextContent(summary),
	}

	result := append([]llm.MessageWithTools{summaryMessage}, recentMessages...)

	summaryTokens := a.countMessageTokens(summaryMessage)

	return result, &Stats{
		Original:       len(messages),
		Remaining:      len(result),
		Compressed:     cutoffIndex,
		Strategy:       string(config.StrategyTruncate),
		OriginalToken:  originalTokens,
		RemainingToken: totalTokens + summaryTokens,
	}
}

// compactLLMSummary 使用 LLM 生成智能摘要
func (a *Agent) compactLLMSummary(ctx context.Context, client *llm.Client, messages []llm.MessageWithTools) ([]llm.MessageWithTools, *Stats, error) {
	// 先计算原始总 token 数
	originalTokens := a.countTokens(messages)

	// 找到需要压缩的消息边界
	totalTokens := 0
	cutoffIndex := 0

	for i := len(messages) - 1; i >= 0; i-- {
		msgTokens := a.countMessageTokens(messages[i])
		if totalTokens+msgTokens > a.cfg.MaxTokens {
			cutoffIndex = i + 1
			break
		}
		totalTokens += msgTokens
	}

	if cutoffIndex == 0 {
		return messages, &Stats{
			Original:       len(messages),
			Remaining:      len(messages),
			OriginalToken:  originalTokens,
			RemainingToken: originalTokens,
		}, nil
	}

	// 调用 LLM 生成摘要
	olderMessages := messages[:cutoffIndex]
	recentMessages := messages[cutoffIndex:]

	summaryText, err := a.generateLLMSummary(ctx, client, olderMessages)
	if err != nil {
		return nil, nil, err
	}

	summaryMessage := llm.MessageWithTools{
		Role:    "system",
		Content: llm.NewTextContent(summaryText),
	}

	result := append([]llm.MessageWithTools{summaryMessage}, recentMessages...)

	summaryTokens := a.countMessageTokens(summaryMessage)

	return result, &Stats{
		Original:       len(messages),
		Remaining:      len(result),
		Compressed:     cutoffIndex,
		Strategy:       string(config.StrategyLLMSummary),
		OriginalToken:  originalTokens,
		RemainingToken: totalTokens + summaryTokens,
	}, nil
}

// generateLLMSummary 调用 LLM 生成智能摘要
func (a *Agent) generateLLMSummary(ctx context.Context, client *llm.Client, messages []llm.MessageWithTools) (string, error) {
	// 构建对话历史文本
	var historyBuilder strings.Builder
	historyBuilder.WriteString("对话历史：\n\n")

	for i, msg := range messages {
		historyBuilder.WriteString(strconv.Itoa(i + 1))
		historyBuilder.WriteString(". [")
		historyBuilder.WriteString(msg.Role)
		historyBuilder.WriteString("]: ")
		historyBuilder.WriteString(stripArtifactTags(msg.Content.Text()))
		historyBuilder.WriteString("\n\n")
	}

	systemPrompt := a.loadPrompt("subagents/compaction",
		`你是一个对话历史压缩助手。你的任务是将长对话历史压缩为简洁的结构化摘要。

请仔细阅读对话历史，提取以下关键信息：
1. 用户的核心目标和需求
2. 已完成的关键操作和决策
3. 重要的文件变更和代码修改
4. 待解决的问题或待办事项

输出格式必须是有效的 JSON，结构如下：
{
  "goal": "用户的核心目标（1-2句话）",
  "completed": ["已完成项1", "已完成项2", ...],
  "file_changes": ["文件1", "文件2", ...],
  "pending": ["待办项1", "待办项2", ...],
  "message_count": 压缩的消息数量
}

注意：
- 保持简洁，每项不超过 50 字
- 只提取最重要的信息
- completed 和 pending 数组各不超过 5 项
- file_changes 只列出修改过的文件名（不包括路径）
- 如果某个字段无内容，使用空数组或空字符串`)

	userPrompt := historyBuilder.String()

	// 设置超时
	timeout := a.cfg.CompactTimeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}

	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	resp, err := client.Chat(callCtx, llm.ChatRequest{
		SystemPrompt: systemPrompt,
		Messages: []llm.Message{
			{Role: "user", Content: llm.NewTextContent(userPrompt)},
		},
		Temperature: 0.3,
		MaxTokens:   500,
		JSONMode:    true,
	})
	if err != nil {
		return "", errs.Wrap(errs.CodeLLMError, "LLM 生成摘要失败", err)
	}
	if a.llmCompleteFn != nil {
		a.llmCompleteFn("compaction", a.sessionID, a.userID, client.Model(), resp.Usage)
	}

	// 解析 JSON 摘要
	summary, err := llm.ParseSummaryJSON(resp.Content)
	if err != nil {
		a.logger.Warn("解析 LLM 摘要 JSON 失败，使用原始文本",
			zap.Error(err),
			zap.String("content", resp.Content),
		)
		return "# 会话摘要\n\n" + resp.Content, nil
	}

	summary.MessageCount = len(messages)
	return llm.FormatSummary(summary), nil
}

// generateSimpleSummary 为早期消息生成简单摘要（不使用 LLM）
func generateSimpleSummary(messages []llm.MessageWithTools) string {
	if len(messages) == 0 {
		return "[会话摘要] 无早期消息"
	}

	var summary strings.Builder
	summary.WriteString("[会话摘要]\n")
	summary.WriteString("已压缩 ")
	summary.WriteString(strconv.Itoa(len(messages)))
	summary.WriteString(" 条早期消息，以下是简要内容：\n\n")

	for i, msg := range messages {
		if i >= 10 {
			summary.WriteString("...（还有 ")
			summary.WriteString(strconv.Itoa(len(messages) - 10))
			summary.WriteString(" 条消息已省略）\n")
			break
		}

		summary.WriteString(strconv.Itoa(i + 1))
		summary.WriteString(". [")
		summary.WriteString(msg.Role)
		summary.WriteString("]: ")
		summary.WriteString(truncateContent(msg.Content.Text(), 100))
		summary.WriteString("\n")
	}

	return summary.String()
}

// truncateContent 截断内容到指定 rune 长度（UTF-8 安全）
func truncateContent(content string, maxLen int) string {
	runes := []rune(content)
	if len(runes) <= maxLen {
		return content
	}
	return string(runes[:maxLen]) + "..."
}

// stripArtifactTags 移除 <artifact>...</artifact> 标签及其内容，防止泄漏到 LLM 上下文
func stripArtifactTags(s string) string {
	result := s
	for {
		start := strings.Index(result, "<artifact")
		if start == -1 {
			break
		}
		end := strings.Index(result[start:], "</artifact>")
		if end == -1 {
			result = result[:start]
			break
		}
		result = result[:start] + result[start+end+len("</artifact>"):]
	}
	return strings.TrimSpace(result)
}
