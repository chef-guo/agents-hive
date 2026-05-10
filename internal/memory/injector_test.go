package memory

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"
)

// mockMemoryStore 测试用的 MemoryStore 模拟实现
type mockMemoryStore struct {
	searchResult        *SearchResult
	searchResultsByType map[MemoryType]*SearchResult
	searchOptions       []SearchOptions
	batchRecords        []MemoryRecord
	batchIDs            []int64
	getCalls            int
	batchGetErr         error
	searchErr           error
	savedRecords        []*MemoryRecord
	saveErr             error
}

func (m *mockMemoryStore) Save(ctx context.Context, record *MemoryRecord) (int64, error) {
	if m.saveErr != nil {
		return 0, m.saveErr
	}
	m.savedRecords = append(m.savedRecords, record)
	return int64(len(m.savedRecords)), nil
}

func (m *mockMemoryStore) Get(_ context.Context, _ int64) (*MemoryRecord, error) {
	m.getCalls++
	return nil, nil
}

func (m *mockMemoryStore) BatchGet(_ context.Context, ids []int64) ([]MemoryRecord, error) {
	if m.batchGetErr != nil {
		return nil, m.batchGetErr
	}
	m.batchIDs = append([]int64(nil), ids...)
	return append([]MemoryRecord(nil), m.batchRecords...), nil
}

func (m *mockMemoryStore) Update(_ context.Context, _ *MemoryRecord) error {
	return nil
}

func (m *mockMemoryStore) Delete(_ context.Context, _ int64) error {
	return nil
}

func (m *mockMemoryStore) Search(_ context.Context, opts SearchOptions) (*SearchResult, error) {
	if m.searchErr != nil {
		return nil, m.searchErr
	}
	m.searchOptions = append(m.searchOptions, opts)
	if m.searchResultsByType != nil {
		if result, ok := m.searchResultsByType[opts.Type]; ok {
			return result, nil
		}
	}
	return m.searchResult, nil
}

func (m *mockMemoryStore) List(_ context.Context, _ SearchOptions) (*SearchResult, error) {
	return m.searchResult, nil
}

func (m *mockMemoryStore) Stats(_ context.Context) (*MemoryStats, error) {
	return &MemoryStats{}, nil
}

func (m *mockMemoryStore) SetEmbedding(_ EmbeddingProvider, _ VectorStore) {}

func (m *mockMemoryStore) Close() error {
	return nil
}

func TestNewInjector(t *testing.T) {
	logger := zap.NewNop()
	store := &mockMemoryStore{}

	tests := []struct {
		name          string
		maxTokens     int
		topK          int
		wantMaxTokens int
		wantTopK      int
	}{
		{
			name:          "使用自定义参数",
			maxTokens:     1000,
			topK:          5,
			wantMaxTokens: 1000,
			wantTopK:      5,
		},
		{
			name:          "零值使用默认值",
			maxTokens:     0,
			topK:          0,
			wantMaxTokens: 2000,
			wantTopK:      10,
		},
		{
			name:          "负值使用默认值",
			maxTokens:     -1,
			topK:          -1,
			wantMaxTokens: 2000,
			wantTopK:      10,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			inj := NewInjector(store, tt.maxTokens, tt.topK, logger)
			if inj.maxTokens != tt.wantMaxTokens {
				t.Errorf("maxTokens = %d, want %d", inj.maxTokens, tt.wantMaxTokens)
			}
			if inj.topK != tt.wantTopK {
				t.Errorf("topK = %d, want %d", inj.topK, tt.wantTopK)
			}
		})
	}
}

func TestNewInjectorWithConfigNormalizesIndependentBudgets(t *testing.T) {
	inj := NewInjectorWithConfig(&mockMemoryStore{}, InjectionConfig{
		MinConfidence:     2,
		MinScore:          -1,
		FeedbackTopK:      -1,
		MemoryTopK:        100,
		FeedbackMaxTokens: -1,
		MemoryMaxTokens:   20000,
	}, zap.NewNop())

	if inj.minConfidence != 0.5 {
		t.Fatalf("minConfidence = %v, want 0.5", inj.minConfidence)
	}
	if inj.minScore != 0 {
		t.Fatalf("minScore = %v, want 0", inj.minScore)
	}
	if inj.feedbackTopK != 3 || inj.memoryTopK != 50 {
		t.Fatalf("topK = feedback %d memory %d", inj.feedbackTopK, inj.memoryTopK)
	}
	if inj.feedbackMaxTokens != 600 || inj.memoryMaxTokens != 12000 {
		t.Fatalf("tokens = feedback %d memory %d", inj.feedbackMaxTokens, inj.memoryMaxTokens)
	}
}

func TestInjector_InjectContext(t *testing.T) {
	logger := zap.NewNop()

	tests := []struct {
		name                string
		userMessage         string
		sessionID           string
		searchResult        *SearchResult
		searchResultsByType map[MemoryType]*SearchResult
		searchErr           error
		maxTokens           int
		topK                int
		wantEmpty           bool
		wantContains        []string
		wantErr             bool
	}{
		{
			name:        "空消息返回空字符串",
			userMessage: "",
			sessionID:   "s1",
			wantEmpty:   true,
		},
		{
			name:         "无搜索结果返回空字符串",
			userMessage:  "测试查询",
			sessionID:    "s1",
			searchResult: &SearchResult{Memories: nil, Total: 0},
			maxTokens:    2000,
			topK:         10,
			wantEmpty:    true,
		},
		{
			name:        "搜索结果为 nil 返回空字符串",
			userMessage: "测试查询",
			sessionID:   "s1",
			maxTokens:   2000,
			topK:        10,
			wantEmpty:   true,
		},
		{
			name:        "搜索出错返回错误",
			userMessage: "测试查询",
			sessionID:   "s1",
			searchErr:   context.DeadlineExceeded,
			maxTokens:   2000,
			topK:        10,
			wantErr:     true,
		},
		{
			name:        "正常注入记忆",
			userMessage: "Go 语言开发",
			sessionID:   "s1",
			searchResultsByType: map[MemoryType]*SearchResult{
				MemoryTypeFeedback: {Memories: nil, Total: 0},
				"": {
					Memories: []MemoryRecord{
						{Type: MemoryTypeUser, Content: "用户偏好 Go 语言"},
						{Type: MemoryTypeProject, Content: "项目采用 Plan-and-Execute 架构"},
					},
					Total: 2,
				},
			},
			maxTokens: 2000,
			topK:      10,
			wantContains: []string{
				"## 相关记忆",
				"[user] 用户偏好 Go 语言",
				"[project] 项目采用 Plan-and-Execute 架构",
			},
		},
		{
			name:        "token 上限截断",
			userMessage: "查询",
			sessionID:   "s1",
			searchResultsByType: map[MemoryType]*SearchResult{
				MemoryTypeFeedback: {Memories: nil, Total: 0},
				"": {
					Memories: []MemoryRecord{
						{Type: MemoryTypeUser, Content: "短记忆"},
						{Type: MemoryTypeProject, Content: strings.Repeat("很长的记忆内容", 500)},
					},
					Total: 2,
				},
			},
			maxTokens:    50,
			topK:         10,
			wantContains: []string{"[user] 短记忆"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &mockMemoryStore{
				searchResult:        tt.searchResult,
				searchResultsByType: tt.searchResultsByType,
				searchErr:           tt.searchErr,
			}

			maxTokens := tt.maxTokens
			if maxTokens == 0 {
				maxTokens = 2000
			}
			topK := tt.topK
			if topK == 0 {
				topK = 10
			}

			inj := NewInjector(store, maxTokens, topK, logger)
			result, err := inj.InjectContext(context.Background(), tt.userMessage, tt.sessionID, "")

			if tt.wantErr {
				if err == nil {
					t.Error("期望返回错误，但实际无错误")
				}
				return
			}
			if err != nil {
				t.Fatalf("不期望的错误: %v", err)
			}

			if tt.wantEmpty {
				if result != "" {
					t.Errorf("期望空字符串，得到: %q", result)
				}
				return
			}

			for _, want := range tt.wantContains {
				if !strings.Contains(result, want) {
					t.Errorf("结果中未包含 %q\n实际结果: %s", want, result)
				}
			}
		})
	}
}

func TestInjector_InjectContextDetailedGovernance(t *testing.T) {
	logger := zap.NewNop()
	now := time.Now()
	store := &mockMemoryStore{
		searchResultsByType: map[MemoryType]*SearchResult{
			MemoryTypeFeedback: {
				Memories: []MemoryRecord{
					{
						ID:       3,
						UserID:   "user-1",
						Type:     MemoryTypeFeedback,
						Content:  "低置信建议",
						Metadata: mustGovernance(t, Governance{Confidence: 0.2}),
					},
				},
			},
			"": {
				Memories: []MemoryRecord{
					{
						ID:       1,
						UserID:   "user-1",
						Type:     MemoryTypeUser,
						Content:  "用户偏好 Go",
						Metadata: mustGovernance(t, Governance{Source: "summary", Confidence: 0.9}),
						Score:    0.95,
					},
					{
						ID:       2,
						UserID:   "user-1",
						Type:     MemoryTypeProject,
						Content:  "过期项目背景",
						Metadata: mustGovernance(t, Governance{Confidence: 0.9, ExpiresAt: now.Add(-time.Hour)}),
					},
					{
						ID:       4,
						UserID:   "other-user",
						Type:     MemoryTypeReference,
						Content:  "其他用户记忆",
						Metadata: mustGovernance(t, Governance{Confidence: 0.9}),
					},
				},
				Total: 3,
			},
		},
	}

	inj := NewInjector(store, 2000, 10, logger)
	got, err := inj.InjectContextDetailed(context.Background(), "Go 语言", "s1", "user-1")
	if err != nil {
		t.Fatalf("InjectContextDetailed returned error: %v", err)
	}

	if !strings.Contains(got.Text, "[user] 用户偏好 Go") {
		t.Fatalf("Text did not include trusted memory: %s", got.Text)
	}
	if strings.Contains(got.Text, "过期项目背景") || strings.Contains(got.Text, "低置信建议") || strings.Contains(got.Text, "其他用户记忆") {
		t.Fatalf("Text included filtered memories: %s", got.Text)
	}
	if len(got.Memories) != 1 || got.Memories[0].ID != 1 {
		t.Fatalf("Memories = %+v, want only memory 1", got.Memories)
	}
	if got.Memories[0].Confidence != 0.9 || got.Memories[0].Source != "summary" {
		t.Fatalf("Injected metadata = %+v, want confidence/source", got.Memories[0])
	}
	if got.SkippedExpired != 1 {
		t.Fatalf("SkippedExpired = %d, want 1", got.SkippedExpired)
	}
	if got.SkippedLowTrust != 1 {
		t.Fatalf("SkippedLowTrust = %d, want 1", got.SkippedLowTrust)
	}
	if got.SkippedCrossUser != 1 {
		t.Fatalf("SkippedCrossUser = %d, want 1", got.SkippedCrossUser)
	}
	if got.EstimatedTokens <= 0 {
		t.Fatalf("EstimatedTokens = %d, want > 0", got.EstimatedTokens)
	}
}

func TestInjector_InjectContextDetailedRecordsTokenBudgetSkips(t *testing.T) {
	store := &mockMemoryStore{
		searchResultsByType: map[MemoryType]*SearchResult{
			MemoryTypeFeedback: {
				Memories: []MemoryRecord{
					{ID: 3, UserID: "user-1", Type: MemoryTypeFeedback, Content: strings.Repeat("另一个很长的记忆内容", 200)},
				},
			},
			"": {
				Memories: []MemoryRecord{
					{ID: 1, UserID: "user-1", Type: MemoryTypeUser, Content: "短记忆"},
					{ID: 2, UserID: "user-1", Type: MemoryTypeProject, Content: strings.Repeat("很长的记忆内容", 200)},
				},
				Total: 2,
			},
		},
	}

	inj := NewInjector(store, 20, 10, zap.NewNop())
	got, err := inj.InjectContextDetailed(context.Background(), "查询", "s1", "user-1")
	if err != nil {
		t.Fatalf("InjectContextDetailed returned error: %v", err)
	}

	if got.SkippedTokenBudget != 2 {
		t.Fatalf("SkippedTokenBudget = %d, want 2", got.SkippedTokenBudget)
	}
	if got.SkippedTotal() != 2 {
		t.Fatalf("SkippedTotal() = %d, want 2", got.SkippedTotal())
	}
	if len(got.SkippedMemoryIDs) != 2 || got.SkippedMemoryIDs[0] != 3 || got.SkippedMemoryIDs[1] != 2 {
		t.Fatalf("SkippedMemoryIDs = %#v, want [3 2]", got.SkippedMemoryIDs)
	}
}

func TestInjector_FeedbackIsInjectedBeforeRegularMemory(t *testing.T) {
	store := &mockMemoryStore{
		searchResultsByType: map[MemoryType]*SearchResult{
			MemoryTypeFeedback: {
				Memories: []MemoryRecord{
					{ID: 1, UserID: "user-1", Type: MemoryTypeFeedback, Content: "完成声明必须带测试输出", Metadata: mustGovernance(t, Governance{Confidence: 0.9})},
				},
			},
			"": {
				Memories: []MemoryRecord{
					{ID: 2, UserID: "user-1", Type: MemoryTypeProject, Content: "项目使用 Go", Metadata: mustGovernance(t, Governance{Confidence: 0.9})},
				},
			},
		},
	}
	inj := NewInjectorWithConfig(store, InjectionConfig{
		MinConfidence:     0.5,
		FeedbackTopK:      3,
		MemoryTopK:        8,
		FeedbackMaxTokens: 200,
		MemoryMaxTokens:   500,
	}, zap.NewNop())

	got, err := inj.InjectContextDetailed(context.Background(), "完成任务", "s1", "user-1")

	if err != nil {
		t.Fatalf("InjectContextDetailed returned error: %v", err)
	}
	feedbackIdx := strings.Index(got.Text, "## 工作方式反馈")
	regularIdx := strings.Index(got.Text, "## 相关记忆")
	if feedbackIdx < 0 || regularIdx < 0 || feedbackIdx > regularIdx {
		t.Fatalf("sections order invalid:\n%s", got.Text)
	}
	if got.FeedbackCount != 1 || got.RegularCount != 1 {
		t.Fatalf("counts = feedback %d regular %d", got.FeedbackCount, got.RegularCount)
	}
}

func TestInjector_FeedbackSearchDefensivelyFiltersType(t *testing.T) {
	store := &mockMemoryStore{
		searchResultsByType: map[MemoryType]*SearchResult{
			MemoryTypeFeedback: {
				Memories: []MemoryRecord{
					{ID: 1, UserID: "user-1", Type: MemoryTypeProject, Content: "项目事实不应进入 feedback 段", Metadata: mustGovernance(t, Governance{Confidence: 0.9})},
				},
			},
			"": {
				Memories: []MemoryRecord{
					{ID: 1, UserID: "user-1", Type: MemoryTypeProject, Content: "项目事实不应进入 feedback 段", Metadata: mustGovernance(t, Governance{Confidence: 0.9})},
				},
			},
		},
	}
	inj := NewInjectorWithConfig(store, InjectionConfig{
		MinConfidence:     0.5,
		FeedbackTopK:      3,
		MemoryTopK:        8,
		FeedbackMaxTokens: 200,
		MemoryMaxTokens:   500,
	}, zap.NewNop())

	got, err := inj.InjectContextDetailed(context.Background(), "查询", "s1", "user-1")
	if err != nil {
		t.Fatalf("InjectContextDetailed returned error: %v", err)
	}
	if strings.Contains(got.Text, "## 工作方式反馈") || strings.Contains(got.Text, "[feedback]") {
		t.Fatalf("unexpected feedback section: %s", got.Text)
	}
	if !strings.Contains(got.Text, "## 相关记忆") || !strings.Contains(got.Text, "[project] 项目事实不应进入 feedback 段") {
		t.Fatalf("regular section missing project memory: %s", got.Text)
	}
}

func TestInjector_FeedbackHasIndependentTokenBudget(t *testing.T) {
	store := &mockMemoryStore{
		searchResultsByType: map[MemoryType]*SearchResult{
			MemoryTypeFeedback: {
				Memories: []MemoryRecord{
					{ID: 1, UserID: "user-1", Type: MemoryTypeFeedback, Content: strings.Repeat("很长的反馈", 200), Metadata: mustGovernance(t, Governance{Confidence: 0.9})},
				},
			},
			"": {
				Memories: []MemoryRecord{
					{ID: 2, UserID: "user-1", Type: MemoryTypeProject, Content: "普通项目记忆", Metadata: mustGovernance(t, Governance{Confidence: 0.9})},
				},
			},
		},
	}
	inj := NewInjectorWithConfig(store, InjectionConfig{
		MinConfidence:     0.5,
		FeedbackTopK:      3,
		MemoryTopK:        8,
		FeedbackMaxTokens: 20,
		MemoryMaxTokens:   200,
	}, zap.NewNop())

	got, err := inj.InjectContextDetailed(context.Background(), "查询", "s1", "user-1")

	if err != nil {
		t.Fatalf("InjectContextDetailed returned error: %v", err)
	}
	if got.FeedbackCount != 0 || got.RegularCount != 1 {
		t.Fatalf("counts = feedback %d regular %d", got.FeedbackCount, got.RegularCount)
	}
	if got.SkippedFeedbackBudget != 1 || got.SkippedRegularBudget != 0 || got.SkippedTokenBudget != 1 {
		t.Fatalf("budget skips = feedback %d regular %d total %d", got.SkippedFeedbackBudget, got.SkippedRegularBudget, got.SkippedTokenBudget)
	}
	if !strings.Contains(got.Text, "普通项目记忆") {
		t.Fatalf("regular memory should remain injected: %s", got.Text)
	}
}

func TestInjector_MinScoreFiltersLowRelatedMemory(t *testing.T) {
	store := &mockMemoryStore{
		searchResultsByType: map[MemoryType]*SearchResult{
			MemoryTypeFeedback: {Memories: nil},
			"": {
				Memories: []MemoryRecord{
					{ID: 1, UserID: "user-1", Type: MemoryTypeProject, Content: "低相关记忆", Score: 0.1, Metadata: mustGovernance(t, Governance{Confidence: 0.9})},
					{ID: 2, UserID: "user-1", Type: MemoryTypeProject, Content: "高相关记忆", Score: 0.7, Metadata: mustGovernance(t, Governance{Confidence: 0.9})},
				},
			},
		},
	}
	inj := NewInjectorWithConfig(store, InjectionConfig{
		MinConfidence:     0.5,
		MinScore:          0.5,
		FeedbackTopK:      3,
		MemoryTopK:        8,
		FeedbackMaxTokens: 100,
		MemoryMaxTokens:   500,
	}, zap.NewNop())

	got, err := inj.InjectContextDetailed(context.Background(), "查询", "s1", "user-1")

	if err != nil {
		t.Fatalf("InjectContextDetailed returned error: %v", err)
	}
	if strings.Contains(got.Text, "低相关记忆") || !strings.Contains(got.Text, "高相关记忆") {
		t.Fatalf("unexpected text: %s", got.Text)
	}
	if got.SkippedLowScore != 1 {
		t.Fatalf("SkippedLowScore = %d, want 1", got.SkippedLowScore)
	}
	if len(store.searchOptions) < 2 || store.searchOptions[0].Type != MemoryTypeFeedback || store.searchOptions[1].MinScore != 0.5 {
		t.Fatalf("search options = %+v", store.searchOptions)
	}
}

func TestInjector_HybridUsesBatchGet(t *testing.T) {
	store := &mockMemoryStore{
		searchResult: &SearchResult{
			Memories: []MemoryRecord{
				{ID: 1, UserID: "user-1", Type: MemoryTypeProject, Content: "first from search"},
				{ID: 2, UserID: "user-1", Type: MemoryTypeProject, Content: "second from search"},
			},
		},
		searchResultsByType: map[MemoryType]*SearchResult{
			MemoryTypeFeedback: {Memories: nil},
		},
		batchRecords: []MemoryRecord{
			{ID: 2, UserID: "user-1", Type: MemoryTypeProject, Content: "second", Metadata: mustGovernance(t, Governance{Confidence: 0.9})},
			{ID: 1, UserID: "user-1", Type: MemoryTypeProject, Content: "first", Metadata: mustGovernance(t, Governance{Confidence: 0.9})},
		},
	}
	inj := NewInjectorWithConfig(store, InjectionConfig{
		MinConfidence:     0.5,
		FeedbackTopK:      3,
		MemoryTopK:        8,
		FeedbackMaxTokens: 100,
		MemoryMaxTokens:   500,
	}, zap.NewNop())
	inj.SetHybridSearcher(NewHybridSearcher(store, nil, nil, zap.NewNop()))

	got, err := inj.InjectContextDetailed(context.Background(), "查询", "s1", "user-1")

	if err != nil {
		t.Fatalf("InjectContextDetailed returned error: %v", err)
	}
	if store.getCalls != 0 {
		t.Fatalf("Get calls = %d, want 0", store.getCalls)
	}
	if len(store.batchIDs) != 2 || store.batchIDs[0] != 1 || store.batchIDs[1] != 2 {
		t.Fatalf("BatchGet ids = %+v, want [1 2]", store.batchIDs)
	}
	if got.RegularCount != 2 {
		t.Fatalf("RegularCount = %d, want 2", got.RegularCount)
	}
	firstIdx := strings.Index(got.Text, "first")
	secondIdx := strings.Index(got.Text, "second")
	if firstIdx < 0 || secondIdx < 0 || firstIdx > secondIdx {
		t.Fatalf("hybrid order not preserved:\n%s", got.Text)
	}
}

func mustGovernance(t *testing.T, g Governance) json.RawMessage {
	t.Helper()
	return EncodeGovernance(nil, g)
}

func TestEstimateTokens(t *testing.T) {
	tests := []struct {
		name string
		text string
		want int
	}{
		{name: "空字符串", text: "", want: 0},
		{name: "短字符串", text: "hi", want: 1},
		{name: "正常字符串", text: "这是一个测试文本", want: len("这是一个测试文本") / 4},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := estimateTokens(tt.text)
			if got != tt.want {
				t.Errorf("estimateTokens(%q) = %d, want %d", tt.text, got, tt.want)
			}
		})
	}
}
