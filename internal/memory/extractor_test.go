package memory

import (
	"context"
	"fmt"
	"testing"
	"time"

	"go.uber.org/zap"
)

// 确保 Extractor 实现 MemoryExtractor 接口
var _ MemoryExtractor = (*Extractor)(nil)

func TestNewExtractor(t *testing.T) {
	store := &mockMemoryStore{}
	logger := zap.NewNop()
	ext := NewExtractor(store, logger)
	if ext == nil {
		t.Fatal("NewExtractor 返回 nil")
	}
	if ext.store != store {
		t.Error("store 未正确赋值")
	}
}

func TestExtractor_ExtractFromSummary(t *testing.T) {
	logger := zap.NewNop()

	tests := []struct {
		name         string
		summaryText  string
		sessionID    string
		searchResult *SearchResult
		searchErr    error
		saveErr      error
		wantSaved    int
		wantErr      bool
	}{
		{
			name:        "空摘要不提取",
			summaryText: "",
			sessionID:   "s1",
			wantSaved:   0,
		},
		{
			name: "从要点列表中提取记忆",
			summaryText: `## 摘要

- 用户偏好使用 Go 语言
- 项目采用 Plan-and-Execute 架构
- 修改了 internal/master/executor.go 文件
`,
			sessionID:    "s1",
			searchResult: &SearchResult{Memories: nil, Total: 0},
			wantSaved:    3,
		},
		{
			name: "跳过重复记忆",
			summaryText: `## 摘要

- 用户偏好使用 Go 语言
`,
			sessionID: "s1",
			searchResult: &SearchResult{
				Memories: []MemoryRecord{
					{Content: "用户偏好使用 Go 语言"},
				},
				Total: 1,
			},
			wantSaved: 0,
		},
		{
			name: "跳过过短的内容",
			summaryText: `## 摘要

- 短
- ab
- 这是一段足够长的有效内容
`,
			sessionID:    "s1",
			searchResult: &SearchResult{Memories: nil, Total: 0},
			wantSaved:    1,
		},
		{
			name: "有序列表也能提取",
			summaryText: `## 决策

1. 采用微服务架构设计方案
2. 使用 PostgreSQL 作为数据库
`,
			sessionID:    "s1",
			searchResult: &SearchResult{Memories: nil, Total: 0},
			wantSaved:    2,
		},
		{
			name: "保存失败仍继续处理",
			summaryText: `## 摘要

- 第一条有效记忆内容
- 第二条有效记忆内容
`,
			sessionID:    "s1",
			searchResult: &SearchResult{Memories: nil, Total: 0},
			saveErr:      fmt.Errorf("模拟保存失败"),
			wantSaved:    0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &mockMemoryStore{
				searchResult: tt.searchResult,
				searchErr:    tt.searchErr,
				saveErr:      tt.saveErr,
			}

			ext := NewExtractor(store, logger)
			err := ext.ExtractFromSummary(context.Background(), tt.summaryText, tt.sessionID, "")

			if tt.wantErr {
				if err == nil {
					t.Error("期望返回错误，但实际无错误")
				}
				return
			}
			if err != nil {
				t.Fatalf("不期望的错误: %v", err)
			}

			if len(store.savedRecords) != tt.wantSaved {
				t.Errorf("保存记忆数 = %d, want %d", len(store.savedRecords), tt.wantSaved)
			}

			// 验证自动标签
			for _, rec := range store.savedRecords {
				hasAutoExtracted := false
				hasCompaction := false
				for _, tag := range rec.Tags {
					if tag == "auto-extracted" {
						hasAutoExtracted = true
					}
					if tag == "compaction" {
						hasCompaction = true
					}
				}
				if !hasAutoExtracted {
					t.Errorf("记忆缺少 'auto-extracted' 标签: %+v", rec)
				}
				if !hasCompaction {
					t.Errorf("记忆缺少 'compaction' 标签: %+v", rec)
				}
			}
		})
	}
}

func TestExtractBulletContent(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "无序列表 dash", input: "- 这是内容", want: "这是内容"},
		{name: "无序列表 star", input: "* 这是内容", want: "这是内容"},
		{name: "有序列表 dot", input: "1. 这是内容", want: "这是内容"},
		{name: "有序列表 paren", input: "2) 这是内容", want: "这是内容"},
		{name: "多位数序号", input: "12. 这是内容", want: "这是内容"},
		{name: "非列表行", input: "这是普通文本", want: ""},
		{name: "空行", input: "", want: ""},
		{name: "仅 dash", input: "-", want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractBulletContent(tt.input)
			if got != tt.want {
				t.Errorf("extractBulletContent(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestClassifyFact(t *testing.T) {
	tests := []struct {
		name    string
		content string
		section string
		want    MemoryType
	}{
		{
			name:    "文件引用",
			content: "修改了 internal/master/executor.go 文件",
			section: "",
			want:    MemoryTypeReference,
		},
		{
			name:    "路径引用",
			content: "配置文件在 config.json 路径下",
			section: "",
			want:    MemoryTypeReference,
		},
		{
			name:    "用户偏好",
			content: "用户偏好使用 Vim 编辑器",
			section: "",
			want:    MemoryTypeUser,
		},
		{
			name:    "章节为用户相关",
			content: "使用暗色主题",
			section: "## 用户设置",
			want:    MemoryTypeUser,
		},
		{
			name:    "项目目标",
			content: "完成 Memory 系统的设计和实现",
			section: "",
			want:    MemoryTypeProject,
		},
		{
			name:    "项目决策",
			content: "决策采用 SQLite 作为存储后端",
			section: "",
			want:    MemoryTypeProject,
		},
		{
			name:    "章节为目标相关",
			content: "一些内容条目",
			section: "## 目标",
			want:    MemoryTypeProject,
		},
		{
			name:    "默认归类为 project",
			content: "一些无法明确分类的描述信息",
			section: "",
			want:    MemoryTypeProject,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := classifyFact(tt.content, tt.section)
			if got != tt.want {
				t.Errorf("classifyFact(%q, %q) = %q, want %q", tt.content, tt.section, got, tt.want)
			}
		})
	}
}

func TestExtractor_isDuplicate(t *testing.T) {
	logger := zap.NewNop()

	tests := []struct {
		name         string
		content      string
		searchResult *SearchResult
		searchErr    error
		want         bool
	}{
		{
			name:         "无搜索结果不重复",
			content:      "新内容",
			searchResult: &SearchResult{Memories: nil},
			want:         false,
		},
		{
			name:    "完全匹配为重复",
			content: "相同的内容",
			searchResult: &SearchResult{
				Memories: []MemoryRecord{{Content: "相同的内容"}},
			},
			want: true,
		},
		{
			name:    "子串包含为重复",
			content: "用户偏好 Go 语言",
			searchResult: &SearchResult{
				Memories: []MemoryRecord{{Content: "用户偏好 Go 语言和 Python"}},
			},
			want: true,
		},
		{
			name:    "不相似非重复",
			content: "完全不同的内容",
			searchResult: &SearchResult{
				Memories: []MemoryRecord{{Content: "另一条记忆"}},
			},
			want: false,
		},
		{
			name:      "搜索出错视为非重复",
			content:   "内容",
			searchErr: fmt.Errorf("搜索错误"),
			want:      false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &mockMemoryStore{
				searchResult: tt.searchResult,
				searchErr:    tt.searchErr,
			}
			ext := NewExtractor(store, logger)
			got := ext.isDuplicate(context.Background(), tt.content, "")
			if got != tt.want {
				t.Errorf("isDuplicate(%q) = %v, want %v", tt.content, got, tt.want)
			}
		})
	}
}

func TestExtractor_ExtractFromSummaryWritesGovernanceDefaults(t *testing.T) {
	store := &mockMemoryStore{searchResult: &SearchResult{}}
	ext := NewExtractor(store, zap.NewNop())
	now := time.Now()

	err := ext.ExtractFromSummary(context.Background(), "- 用户偏好使用 Go 语言", "session-1", "user-1",
		WithExtractorVersion("extractor-v2"),
		WithSourceMessage("msg-7"),
		WithRunID("run-9"),
		WithRetentionDays(30),
		WithNow(func() time.Time { return now }),
	)

	if err != nil {
		t.Fatalf("ExtractFromSummary: %v", err)
	}
	if len(store.savedRecords) != 1 {
		t.Fatalf("saved records = %d, want 1", len(store.savedRecords))
	}
	g := DecodeGovernance(store.savedRecords[0].Metadata)
	if g.Source != "compaction_summary" {
		t.Fatalf("governance source = %q, want compaction_summary", g.Source)
	}
	if g.ExtractedBy != "extractor-v2" {
		t.Fatalf("extracted_by = %q, want extractor-v2", g.ExtractedBy)
	}
	if g.SourceMessage != "msg-7" {
		t.Fatalf("source_message = %q, want msg-7", g.SourceMessage)
	}
	if g.SourceUserID != "user-1" {
		t.Fatalf("source_user_id = %q, want user-1", g.SourceUserID)
	}
	if g.RunID != "run-9" {
		t.Fatalf("run_id = %q, want run-9", g.RunID)
	}
	if g.Confidence != 0.8 {
		t.Fatalf("confidence = %v, want 0.8", g.Confidence)
	}
	if !g.ExpiresAt.Equal(now.Add(30 * 24 * time.Hour)) {
		t.Fatalf("expires_at = %s, want %s", g.ExpiresAt, now.Add(30*24*time.Hour))
	}
}

func TestExtractor_ExtractFeedbackWritesFeedbackMemory(t *testing.T) {
	store := &mockMemoryStore{searchResult: &SearchResult{}}
	ext := NewExtractor(store, zap.NewNop())
	now := time.Now()

	records, err := ext.ExtractFeedback(context.Background(), FeedbackInput{
		Text:          "用户指出不要只说应该可以，必须跑测试后再声明完成。",
		SessionID:     "session-1",
		UserID:        "user-1",
		Source:        "user_correction",
		SourceMessage: "msg-9",
		RunID:         "run-1",
		Confidence:    0.9,
	}, WithNow(func() time.Time { return now }))

	if err != nil {
		t.Fatalf("ExtractFeedback returned error: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("records = %d, want 1", len(records))
	}
	if len(store.savedRecords) != 1 {
		t.Fatalf("saved records = %d, want 1", len(store.savedRecords))
	}
	rec := store.savedRecords[0]
	if rec.Type != MemoryTypeFeedback {
		t.Fatalf("type = %q, want feedback", rec.Type)
	}
	if rec.UserID != "user-1" || rec.SessionID != "session-1" {
		t.Fatalf("record ownership = user %q session %q", rec.UserID, rec.SessionID)
	}
	g := DecodeGovernance(rec.Metadata)
	if g.Source != "user_correction" || g.SourceUserID != "user-1" || g.SourceMessage != "msg-9" || g.RunID != "run-1" {
		t.Fatalf("governance = %+v", g)
	}
	if g.Confidence != 0.9 {
		t.Fatalf("confidence = %v, want 0.9", g.Confidence)
	}
}

func TestJSONStructuredExtractorValidatesAndParsesFacts(t *testing.T) {
	extractor := JSONStructuredExtractor{
		Generate: func(context.Context, string) (string, error) {
			return `[{"type":"feedback","content":"用户要求完成声明必须附带测试输出。","confidence":0.85,"evidence":"必须跑测试"}]`, nil
		},
	}

	facts, err := extractor.ExtractMemoryFacts(context.Background(), StructuredExtractInput{
		Text:         "source",
		AllowedTypes: []MemoryType{MemoryTypeFeedback},
	})

	if err != nil {
		t.Fatalf("ExtractMemoryFacts returned error: %v", err)
	}
	if len(facts) != 1 || facts[0].Type != MemoryTypeFeedback || facts[0].Confidence != 0.85 {
		t.Fatalf("facts = %+v", facts)
	}
}

func TestExtractor_StructuredFailureFallsBackToRules(t *testing.T) {
	store := &mockMemoryStore{searchResult: &SearchResult{}}
	ext := NewExtractorWithStructured(store, JSONStructuredExtractor{
		Generate: func(context.Context, string) (string, error) {
			return `not-json`, nil
		},
	}, zap.NewNop())

	err := ext.ExtractFromSummary(context.Background(), "- 用户要求以后直接改文件，不要先讲方案", "session-1", "user-1")

	if err != nil {
		t.Fatalf("ExtractFromSummary returned error: %v", err)
	}
	if len(store.savedRecords) != 1 {
		t.Fatalf("saved records = %d, want fallback rule extraction", len(store.savedRecords))
	}
	if store.savedRecords[0].Type != MemoryTypeFeedback {
		t.Fatalf("type = %q, want feedback", store.savedRecords[0].Type)
	}
}

func TestExtractor_NonBulletFeedbackCanBeExtracted(t *testing.T) {
	store := &mockMemoryStore{searchResult: &SearchResult{}}
	ext := NewExtractor(store, zap.NewNop())

	records, err := ext.ExtractFeedback(context.Background(), FeedbackInput{
		Text:      "用户说以后不要反复问确认，能直接安全执行就直接改。",
		UserID:    "user-1",
		SessionID: "session-1",
	})

	if err != nil {
		t.Fatalf("ExtractFeedback returned error: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("records = %d, want 1", len(records))
	}
}

func TestExtractor_ProjectRequirementIsNotFeedback(t *testing.T) {
	store := &mockMemoryStore{searchResult: &SearchResult{}}
	ext := NewExtractor(store, zap.NewNop())

	err := ext.ExtractFromSummary(context.Background(), "- 项目必须支持多租户和独立权限模型", "session-1", "user-1")

	if err != nil {
		t.Fatalf("ExtractFromSummary returned error: %v", err)
	}
	if len(store.savedRecords) != 1 {
		t.Fatalf("saved records = %d, want 1", len(store.savedRecords))
	}
	if store.savedRecords[0].Type != MemoryTypeProject {
		t.Fatalf("type = %q, want project", store.savedRecords[0].Type)
	}
}
