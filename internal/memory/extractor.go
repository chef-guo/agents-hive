package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"go.uber.org/zap"
)

// 编译期接口合规检查
var _ MemoryExtractor = (*Extractor)(nil)

// Extractor 从压缩摘要中自动提取记忆
type Extractor struct {
	store        MemoryStore
	structured   StructuredExtractor
	maxInputRune int
	logger       *zap.Logger
}

// NewExtractor 创建记忆提取器
func NewExtractor(store MemoryStore, logger *zap.Logger) *Extractor {
	return &Extractor{
		store:        store,
		maxInputRune: 12000,
		logger:       logger,
	}
}

// NewExtractorWithStructured 创建带结构化提取路径的记忆提取器。
func NewExtractorWithStructured(store MemoryStore, structured StructuredExtractor, logger *zap.Logger) *Extractor {
	ext := NewExtractor(store, logger)
	ext.structured = structured
	return ext
}

// ExtractFromSummary 从压缩摘要文本中提取并保存记忆
// summaryText 是 compaction 生成的 LLM 摘要
// sessionID 是来源会话
// userID 是记忆归属用户
func (e *Extractor) ExtractFromSummary(ctx context.Context, summaryText string, sessionID string, userID string, opts ...ExtractorOption) error {
	if summaryText == "" {
		return nil
	}
	cfg := defaultExtractorConfig()
	for _, opt := range opts {
		opt(&cfg)
	}

	facts := e.extractFacts(ctx, summaryText)
	if len(facts) == 0 {
		e.logger.Debug("摘要中未提取到记忆", zap.String("session_id", sessionID))
		return nil
	}

	autoTags := []string{"auto-extracted", "compaction"}
	saved := 0

	for _, fact := range facts {
		// 检查是否已存在相似记忆（去重，限定在同一用户范围内）
		if e.isDuplicate(ctx, fact.content, userID) {
			e.logger.Debug("跳过重复记忆", zap.String("content", fact.content))
			continue
		}

		record := &MemoryRecord{
			Type:      fact.memType,
			Content:   fact.content,
			Tags:      autoTags,
			SessionID: sessionID,
			UserID:    userID,
			Metadata: EncodeGovernance(nil, Governance{
				Source:         "compaction_summary",
				Confidence:     cfg.confidence,
				ExpiresAt:      cfg.expiresAt(),
				ExtractedBy:    cfg.extractorVersion,
				SourceMessage:  cfg.sourceMessage,
				SourceUserID:   userID,
				SourceTenantID: cfg.sourceTenantID,
				RunID:          cfg.runID,
			}),
		}

		if _, err := e.store.Save(ctx, record); err != nil {
			e.logger.Warn("保存提取的记忆失败",
				zap.String("content", fact.content),
				zap.Error(err),
			)
			continue
		}
		saved++
	}

	e.logger.Info("从摘要中提取记忆完成",
		zap.Int("extracted", len(facts)),
		zap.Int("saved", saved),
		zap.String("session_id", sessionID),
	)
	return nil
}

// ExtractFeedback 从用户纠错、reflection 或 evaluator verdict 中提取 feedback 记忆。
func (e *Extractor) ExtractFeedback(ctx context.Context, input FeedbackInput, opts ...ExtractorOption) ([]MemoryRecord, error) {
	if strings.TrimSpace(input.Text) == "" && len(input.Feedback) == 0 {
		return nil, nil
	}
	cfg := defaultExtractorConfig()
	for _, opt := range opts {
		opt(&cfg)
	}
	if input.Confidence > 0 && input.Confidence <= 1 {
		cfg.confidence = input.Confidence
	}
	source := strings.TrimSpace(input.Source)
	if source == "" {
		source = "feedback"
	}
	facts := e.extractFeedbackFacts(ctx, input)
	records := make([]MemoryRecord, 0, len(facts))
	for _, fact := range facts {
		if strings.TrimSpace(fact.content) == "" {
			continue
		}
		if e.isDuplicate(ctx, fact.content, input.UserID) {
			continue
		}
		meta := EncodeGovernance(nil, Governance{
			Source:         source,
			Evidence:       strings.TrimSpace(fact.evidence),
			Confidence:     cfg.confidence,
			ExpiresAt:      cfg.expiresAt(),
			ExtractedBy:    cfg.extractorVersion,
			SourceMessage:  firstNonEmpty(cfg.sourceMessage, input.SourceMessage),
			SourceUserID:   input.UserID,
			SourceTenantID: cfg.sourceTenantID,
			RunID:          firstNonEmpty(cfg.runID, input.RunID),
		})
		meta = encodeRuntimeMetadata(meta, RuntimeContext{
			UserID:    input.UserID,
			SessionID: input.SessionID,
			AgentName: input.AgentName,
			SkillName: input.SkillName,
			TaskType:  input.TaskType,
		})
		record := MemoryRecord{
			Type:      MemoryTypeFeedback,
			Content:   fact.content,
			Tags:      []string{"auto-extracted", "feedback"},
			SessionID: input.SessionID,
			UserID:    input.UserID,
			Metadata:  meta,
		}
		id, err := e.store.Save(ctx, &record)
		if err != nil {
			return records, err
		}
		record.ID = id
		records = append(records, record)
	}
	return records, nil
}

type FeedbackInput struct {
	Text          string
	Feedback      []string
	SessionID     string
	UserID        string
	AgentName     string
	SkillName     string
	TaskType      string
	Source        string
	SourceMessage string
	RunID         string
	Confidence    float64
}

func encodeRuntimeMetadata(raw json.RawMessage, rc RuntimeContext) json.RawMessage {
	var m map[string]any
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &m)
	}
	if m == nil {
		m = map[string]any{}
	}
	runtime := map[string]any{}
	if rc.UserID != "" {
		runtime["user_id"] = rc.UserID
	}
	if rc.TenantID != "" {
		runtime["tenant_id"] = rc.TenantID
	}
	if rc.WorkspaceID != "" {
		runtime["workspace_id"] = rc.WorkspaceID
	}
	if rc.ProjectID != "" {
		runtime["project_id"] = rc.ProjectID
	}
	if rc.RepoID != "" {
		runtime["repo_id"] = rc.RepoID
	}
	if rc.SessionID != "" {
		runtime["session_id"] = rc.SessionID
	}
	if rc.AgentName != "" {
		runtime["agent_name"] = rc.AgentName
	}
	if rc.SkillName != "" {
		runtime["skill_name"] = rc.SkillName
	}
	if rc.TaskType != "" {
		runtime["task_type"] = rc.TaskType
	}
	if len(rc.CurrentFiles) > 0 {
		runtime["current_files"] = append([]string(nil), rc.CurrentFiles...)
	}
	if rc.ToolIntent != "" {
		runtime["tool_intent"] = rc.ToolIntent
	}
	if len(runtime) > 0 {
		m["runtime"] = runtime
	}
	encoded, _ := json.Marshal(m)
	return encoded
}

type StructuredExtractor interface {
	ExtractMemoryFacts(ctx context.Context, input StructuredExtractInput) ([]StructuredMemoryFact, error)
}

type FeedbackExtractor interface {
	ExtractFeedback(ctx context.Context, input FeedbackInput, opts ...ExtractorOption) ([]MemoryRecord, error)
}

type StructuredExtractInput struct {
	Text         string
	AllowedTypes []MemoryType
	MaxRunes     int
}

type StructuredMemoryFact struct {
	Type       MemoryType
	Content    string
	Confidence float64
	Evidence   string
}

type JSONStructuredExtractor struct {
	Generate func(context.Context, string) (string, error)
	MaxRunes int
}

func (e JSONStructuredExtractor) ExtractMemoryFacts(ctx context.Context, input StructuredExtractInput) ([]StructuredMemoryFact, error) {
	if e.Generate == nil {
		return nil, fmt.Errorf("structured extractor generator missing")
	}
	maxRunes := e.MaxRunes
	if input.MaxRunes > 0 && (maxRunes <= 0 || input.MaxRunes < maxRunes) {
		maxRunes = input.MaxRunes
	}
	prompt := truncateRunes(input.Text, maxRunes)
	raw, err := e.Generate(ctx, prompt)
	if err != nil {
		return nil, err
	}
	var decoded []struct {
		Type       string  `json:"type"`
		Content    string  `json:"content"`
		Confidence float64 `json:"confidence"`
		Evidence   string  `json:"evidence"`
	}
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		return nil, err
	}
	allowed := allowedMemoryTypeSet(input.AllowedTypes)
	out := make([]StructuredMemoryFact, 0, len(decoded))
	for _, item := range decoded {
		mt := MemoryType(strings.TrimSpace(item.Type))
		if !ValidMemoryTypes[mt] || !allowed[mt] {
			return nil, fmt.Errorf("invalid memory type %q", item.Type)
		}
		content := strings.TrimSpace(item.Content)
		if content == "" {
			return nil, fmt.Errorf("memory content must not be empty")
		}
		if item.Confidence < 0 || item.Confidence > 1 {
			return nil, fmt.Errorf("memory confidence must be between 0 and 1")
		}
		out = append(out, StructuredMemoryFact{
			Type:       mt,
			Content:    content,
			Confidence: item.Confidence,
			Evidence:   strings.TrimSpace(item.Evidence),
		})
	}
	return out, nil
}

type extractedFact struct {
	content  string
	memType  MemoryType
	evidence string
}

func (e *Extractor) extractFacts(ctx context.Context, text string) []extractedFact {
	if e.structured != nil {
		facts, err := e.structured.ExtractMemoryFacts(ctx, StructuredExtractInput{
			Text:         truncateRunes(text, e.maxInputRune),
			AllowedTypes: []MemoryType{MemoryTypeUser, MemoryTypeProject, MemoryTypeReference, MemoryTypeFeedback},
			MaxRunes:     e.maxInputRune,
		})
		if err == nil {
			return structuredToExtractedFacts(facts)
		}
		e.logger.Warn("结构化记忆提取失败，降级为规则提取", zap.Error(err))
	}
	return e.parseFacts(text)
}

func (e *Extractor) extractFeedbackFacts(ctx context.Context, input FeedbackInput) []extractedFact {
	text := input.Text
	if len(input.Feedback) > 0 {
		text += "\n" + strings.Join(input.Feedback, "\n")
	}
	if e.structured != nil {
		facts, err := e.structured.ExtractMemoryFacts(ctx, StructuredExtractInput{
			Text:         truncateRunes(text, e.maxInputRune),
			AllowedTypes: []MemoryType{MemoryTypeFeedback},
			MaxRunes:     e.maxInputRune,
		})
		if err == nil {
			return structuredToExtractedFacts(facts)
		}
		e.logger.Warn("结构化 feedback 提取失败，降级为规则提取", zap.Error(err))
	}
	return extractRuleFeedbackFacts(text, input.Feedback)
}

type extractorConfig struct {
	extractorVersion string
	sourceMessage    string
	sourceTenantID   string
	runID            string
	retentionDays    int
	confidence       float64
	now              func() time.Time
}

type ExtractorOption func(*extractorConfig)

func defaultExtractorConfig() extractorConfig {
	return extractorConfig{
		extractorVersion: "v1",
		retentionDays:    90,
		confidence:       0.8,
		now:              time.Now,
	}
}

func (c extractorConfig) expiresAt() time.Time {
	if c.retentionDays <= 0 {
		return time.Time{}
	}
	return c.now().Add(time.Duration(c.retentionDays) * 24 * time.Hour)
}

func WithExtractorVersion(v string) ExtractorOption {
	return func(cfg *extractorConfig) {
		if strings.TrimSpace(v) != "" {
			cfg.extractorVersion = strings.TrimSpace(v)
		}
	}
}

func WithSourceMessage(id string) ExtractorOption {
	return func(cfg *extractorConfig) {
		cfg.sourceMessage = strings.TrimSpace(id)
	}
}

func WithSourceTenantID(id string) ExtractorOption {
	return func(cfg *extractorConfig) {
		cfg.sourceTenantID = strings.TrimSpace(id)
	}
}

func WithRunID(id string) ExtractorOption {
	return func(cfg *extractorConfig) {
		cfg.runID = strings.TrimSpace(id)
	}
}

func WithRetentionDays(days int) ExtractorOption {
	return func(cfg *extractorConfig) {
		cfg.retentionDays = days
	}
}

func WithConfidence(confidence float64) ExtractorOption {
	return func(cfg *extractorConfig) {
		if confidence > 0 && confidence <= 1 {
			cfg.confidence = confidence
		}
	}
}

func WithNow(now func() time.Time) ExtractorOption {
	return func(cfg *extractorConfig) {
		if now != nil {
			cfg.now = now
		}
	}
}

// 目标/决策相关关键词
var projectKeywords = []string{
	"目标", "决策", "计划", "架构", "设计", "方案", "策略",
	"实现", "完成", "修复", "重构", "优化", "部署",
	"goal", "decision", "plan", "architecture", "design",
}

// 用户偏好相关关键词
var userKeywords = []string{
	"偏好", "喜欢", "习惯", "风格", "用户",
	"prefer", "like", "style", "user",
}

var feedbackStrongKeywords = []string{
	"纠正", "指出", "不要", "下次", "以后", "不允许", "别再", "不能只", "反馈", "否定",
	"do not", "don't", "next time", "feedback", "correction",
}

var feedbackInstructionKeywords = []string{
	"必须", "要求", "先", "直接", "must", "should",
}

var feedbackSubjectKeywords = []string{
	"用户", "你", "你们", "agent", "assistant",
}

// 文件/文档引用相关关键词
var referenceKeywords = []string{
	"文件", "路径", "文档", "链接", "配置",
	"file", "path", "doc", "config", ".go", ".ts", ".json", ".yaml", ".yml",
}

// parseFacts 从摘要文本中解析事实条目
func (e *Extractor) parseFacts(text string) []extractedFact {
	var facts []extractedFact

	lines := strings.Split(text, "\n")
	currentSection := ""

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}

		// 检测章节标题
		if strings.HasPrefix(trimmed, "#") || strings.HasSuffix(trimmed, ":") || strings.HasSuffix(trimmed, "：") {
			currentSection = strings.ToLower(trimmed)
			continue
		}

		// 提取要点行（以 - 或 * 或数字序号开头）
		content := extractBulletContent(trimmed)
		if content == "" {
			continue
		}

		// 过滤太短的内容
		if len(content) < 5 {
			continue
		}

		memType := classifyFact(content, currentSection)
		facts = append(facts, extractedFact{
			content: content,
			memType: memType,
		})
	}

	return facts
}

// extractBulletContent 提取要点行的内容
// 支持格式：- 内容、* 内容、1. 内容、1) 内容
func extractBulletContent(line string) string {
	// Markdown 无序列表
	if strings.HasPrefix(line, "- ") {
		return strings.TrimSpace(line[2:])
	}
	if strings.HasPrefix(line, "* ") {
		return strings.TrimSpace(line[2:])
	}

	// 有序列表：1. 内容 或 1) 内容
	for i, ch := range line {
		if ch >= '0' && ch <= '9' {
			continue
		}
		if i > 0 && (ch == '.' || ch == ')') && i+1 < len(line) && line[i+1] == ' ' {
			return strings.TrimSpace(line[i+2:])
		}
		break
	}

	return ""
}

// classifyFact 基于内容和所在章节分类事实类型
func classifyFact(content string, section string) MemoryType {
	lower := strings.ToLower(content)
	sectionLower := strings.ToLower(section)

	if looksLikeFeedback(content) || containsAnyKeyword(sectionLower, feedbackStrongKeywords) {
		return MemoryTypeFeedback
	}

	// 优先检查文件引用
	for _, kw := range referenceKeywords {
		if strings.Contains(lower, kw) || strings.Contains(sectionLower, kw) {
			return MemoryTypeReference
		}
	}

	// 检查用户偏好
	for _, kw := range userKeywords {
		if strings.Contains(lower, kw) || strings.Contains(sectionLower, kw) {
			return MemoryTypeUser
		}
	}

	// 检查项目目标/决策
	for _, kw := range projectKeywords {
		if strings.Contains(lower, kw) || strings.Contains(sectionLower, kw) {
			return MemoryTypeProject
		}
	}

	// 默认归类为项目记忆
	return MemoryTypeProject
}

func extractRuleFeedbackFacts(text string, explicit []string) []extractedFact {
	var facts []extractedFact
	for _, item := range explicit {
		content := normalizeFeedbackContent(item)
		if content != "" {
			facts = append(facts, extractedFact{content: content, memType: MemoryTypeFeedback, evidence: item})
		}
	}
	lines := strings.Split(text, "\n")
	for _, line := range lines {
		content := strings.TrimSpace(line)
		if bullet := extractBulletContent(content); bullet != "" {
			content = bullet
		}
		if !looksLikeFeedback(content) {
			continue
		}
		normalized := normalizeFeedbackContent(content)
		if normalized == "" {
			continue
		}
		facts = append(facts, extractedFact{content: normalized, memType: MemoryTypeFeedback, evidence: content})
	}
	return dedupeExtractedFacts(facts)
}

func normalizeFeedbackContent(raw string) string {
	content := strings.TrimSpace(raw)
	content = strings.Trim(content, "-* 　\t")
	if len([]rune(content)) < 6 {
		return ""
	}
	return content
}

func looksLikeFeedback(content string) bool {
	lower := strings.ToLower(strings.TrimSpace(content))
	if lower == "" {
		return false
	}
	if containsAnyKeyword(lower, feedbackStrongKeywords) {
		return true
	}
	return containsAnyKeyword(lower, feedbackInstructionKeywords) && containsAnyKeyword(lower, feedbackSubjectKeywords)
}

func containsAnyKeyword(s string, keywords []string) bool {
	for _, kw := range keywords {
		if strings.Contains(s, kw) {
			return true
		}
	}
	return false
}

func structuredToExtractedFacts(facts []StructuredMemoryFact) []extractedFact {
	out := make([]extractedFact, 0, len(facts))
	for _, fact := range facts {
		out = append(out, extractedFact{
			content:  strings.TrimSpace(fact.Content),
			memType:  fact.Type,
			evidence: strings.TrimSpace(fact.Evidence),
		})
	}
	return dedupeExtractedFacts(out)
}

func dedupeExtractedFacts(facts []extractedFact) []extractedFact {
	seen := map[string]bool{}
	out := make([]extractedFact, 0, len(facts))
	for _, fact := range facts {
		key := string(fact.memType) + "\x00" + strings.TrimSpace(fact.content)
		if fact.content == "" || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, fact)
	}
	return out
}

func allowedMemoryTypeSet(types []MemoryType) map[MemoryType]bool {
	if len(types) == 0 {
		return ValidMemoryTypes
	}
	out := map[MemoryType]bool{}
	for _, mt := range types {
		out[mt] = true
	}
	return out
}

func truncateRunes(text string, maxRunes int) string {
	if maxRunes <= 0 {
		return text
	}
	rs := []rune(text)
	if len(rs) <= maxRunes {
		return text
	}
	return string(rs[:maxRunes])
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

// isDuplicate 检查是否已存在相似内容的记忆（限定在同一用户范围内）
func (e *Extractor) isDuplicate(ctx context.Context, content string, userID string) bool {
	result, err := e.store.Search(ctx, SearchOptions{
		Query:  content,
		Limit:  1,
		UserID: userID,
	})
	if err != nil {
		return false
	}

	if result == nil || len(result.Memories) == 0 {
		return false
	}

	// 使用简单的内容相似度检查：完全匹配或子串包含
	existing := result.Memories[0].Content
	if existing == content {
		return true
	}
	if strings.Contains(existing, content) || strings.Contains(content, existing) {
		return true
	}

	return false
}
