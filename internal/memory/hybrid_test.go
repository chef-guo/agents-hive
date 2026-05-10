package memory

import (
	"context"
	"errors"
	"math"
	"testing"

	"go.uber.org/zap"
)

var _ MemoryStore = (*mockHybridStore)(nil)
var _ VectorStore = (*mockHybridVec)(nil)
var _ EmbeddingProvider = (*mockHybridEmbed)(nil)

// mockHybridStore 测试用 MemoryStore mock
type mockHybridStore struct {
	searchResult *SearchResult
	searchErr    error
}

func (m *mockHybridStore) Save(_ context.Context, _ *MemoryRecord) (int64, error) {
	return 0, nil
}
func (m *mockHybridStore) Get(_ context.Context, _ int64) (*MemoryRecord, error) {
	return nil, nil
}
func (m *mockHybridStore) Update(_ context.Context, _ *MemoryRecord) error {
	return nil
}
func (m *mockHybridStore) Delete(_ context.Context, _ int64) error {
	return nil
}
func (m *mockHybridStore) Search(_ context.Context, _ SearchOptions) (*SearchResult, error) {
	if m.searchErr != nil {
		return nil, m.searchErr
	}
	return m.searchResult, nil
}
func (m *mockHybridStore) List(_ context.Context, _ SearchOptions) (*SearchResult, error) {
	return nil, nil
}
func (m *mockHybridStore) Stats(_ context.Context) (*MemoryStats, error) {
	return &MemoryStats{}, nil
}
func (m *mockHybridStore) SetEmbedding(_ EmbeddingProvider, _ VectorStore) {}
func (m *mockHybridStore) Close() error                                    { return nil }

// mockHybridVec 测试用 VectorStore mock
type mockHybridVec struct {
	searchResult []VecSearchResult
	searchErr    error
}

func (m *mockHybridVec) Add(_ context.Context, _ int64, _ []float32) error { return nil }
func (m *mockHybridVec) Remove(_ context.Context, _ int64) error           { return nil }
func (m *mockHybridVec) Search(_ context.Context, _ []float32, _ int, _ string) ([]VecSearchResult, error) {
	if m.searchErr != nil {
		return nil, m.searchErr
	}
	return m.searchResult, nil
}
func (m *mockHybridVec) Count(_ context.Context) (int, error) { return len(m.searchResult), nil }
func (m *mockHybridVec) Close() error                         { return nil }

// mockHybridEmbed 测试用 EmbeddingProvider mock
type mockHybridEmbed struct {
	vec        []float32
	embedErr   error
	dimensions int
}

func (m *mockHybridEmbed) Embed(_ context.Context, texts []string) ([][]float32, error) {
	if m.embedErr != nil {
		return nil, m.embedErr
	}
	result := make([][]float32, len(texts))
	for i := range texts {
		result[i] = m.vec
	}
	return result, nil
}
func (m *mockHybridEmbed) Dimensions() int {
	if m.dimensions == 0 {
		return len(m.vec)
	}
	return m.dimensions
}

func TestHybridSearcher_Search_BothSuccess(t *testing.T) {
	store := &mockHybridStore{
		searchResult: &SearchResult{
			Memories: []MemoryRecord{
				{ID: 1, Score: 0.9},
				{ID: 2, Score: 0.7},
			},
		},
	}
	vec := &mockHybridVec{
		searchResult: []VecSearchResult{
			{ID: 2, Score: 0.95},
			{ID: 3, Score: 0.85},
		},
	}
	embed := &mockHybridEmbed{vec: []float32{1, 0, 0}}

	h := NewHybridSearcher(store, vec, embed, zap.NewNop())
	results, err := h.Search(context.Background(), "test", 5, "")
	if err != nil {
		t.Fatal(err)
	}

	// ID=2 在两路都出现，RRF 分数最高
	if len(results) != 3 {
		t.Fatalf("期望 3 个结果，得到 %d", len(results))
	}
	if results[0].ID != 2 {
		t.Errorf("第一个结果应为 ID=2，得到 %d", results[0].ID)
	}
}

func TestHybridSearcher_Search_FTSOnly(t *testing.T) {
	store := &mockHybridStore{
		searchResult: &SearchResult{
			Memories: []MemoryRecord{
				{ID: 1, Score: 0.9},
			},
		},
	}
	vec := &mockHybridVec{
		searchErr: assertNeverCalled{}, // 不应被调用
	}
	embed := &mockHybridEmbed{vec: []float32{1, 0, 0}}

	h := NewHybridSearcher(store, vec, embed, zap.NewNop())
	results, err := h.Search(context.Background(), "test", 5, "")
	if err != nil {
		t.Fatal(err)
	}

	// 只有 FTS 结果
	if len(results) != 1 {
		t.Fatalf("期望 1 个结果，得到 %d", len(results))
	}
	if results[0].ID != 1 {
		t.Errorf("结果应为 ID=1，得到 %d", results[0].ID)
	}
}

func TestHybridSearcher_Search_VecOnly(t *testing.T) {
	store := &mockHybridStore{
		searchErr: assertNeverCalled{}, // 不应被调用
	}
	vec := &mockHybridVec{
		searchResult: []VecSearchResult{
			{ID: 3, Score: 0.85},
		},
	}
	embed := &mockHybridEmbed{vec: []float32{1, 0, 0}}

	h := NewHybridSearcher(store, vec, embed, zap.NewNop())
	results, err := h.Search(context.Background(), "test", 5, "")
	if err != nil {
		t.Fatal(err)
	}

	// 只有 vec 结果
	if len(results) != 1 {
		t.Fatalf("期望 1 个结果，得到 %d", len(results))
	}
	if results[0].ID != 3 {
		t.Errorf("结果应为 ID=3，得到 %d", results[0].ID)
	}
}

func TestHybridSearcher_Search_BothFail(t *testing.T) {
	store := &mockHybridStore{searchErr: errFTS}
	vec := &mockHybridVec{searchErr: errVec}
	embed := &mockHybridEmbed{vec: []float32{1, 0, 0}}

	h := NewHybridSearcher(store, vec, embed, zap.NewNop())
	results, err := h.Search(context.Background(), "test", 5, "")
	if err != nil {
		t.Fatal(err)
	}

	// 两路都失败，返回空
	if results != nil {
		t.Errorf("期望 nil，得到 %v", results)
	}
}

func TestHybridSearcher_Search_EmbedFails(t *testing.T) {
	store := &mockHybridStore{
		searchResult: &SearchResult{
			Memories: []MemoryRecord{
				{ID: 1, Score: 0.9},
			},
		},
	}
	vec := &mockHybridVec{
		searchErr: assertNeverCalled{},
	}
	embed := &mockHybridEmbed{embedErr: errEmbed}

	h := NewHybridSearcher(store, vec, embed, zap.NewNop())
	results, err := h.Search(context.Background(), "test", 5, "")
	if err != nil {
		t.Fatal(err)
	}

	// embed 失败，降级为纯 FTS
	if len(results) != 1 {
		t.Errorf("期望 1 个结果，得到 %d", len(results))
	}
}

func TestHybridSearcher_RecordsVectorMismatchMetric(t *testing.T) {
	recorder := &recordingMemoryMetricRecorder{}
	store := &mockHybridStore{
		searchResult: &SearchResult{Memories: []MemoryRecord{{ID: 1, Score: 0.9}}},
	}
	vec := &mockHybridVec{searchErr: errors.New("查询向量维度不匹配：已建立维度 3，查询维度 2")}
	embed := &mockHybridEmbed{vec: []float32{1, 0}}

	h := NewHybridSearcher(store, vec, embed, zap.NewNop())
	h.SetMetrics(recorder)
	results, err := h.Search(context.Background(), "test", 5, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].ID != 1 {
		t.Fatalf("results = %+v, want FTS fallback result", results)
	}
	if !recorder.hasMetric(MetricVectorSpaceMismatchTotal, "operation", "search") {
		t.Fatalf("missing %s metric: %+v", MetricVectorSpaceMismatchTotal, recorder.events)
	}
	if !recorder.hasMetric(MetricHybridSearchFallbackTotal, "reason", HybridFallbackReasonVectorError) {
		t.Fatalf("missing vector fallback metric: %+v", recorder.events)
	}
}

func TestHybridSearcher_Search_Limit(t *testing.T) {
	store := &mockHybridStore{
		searchResult: &SearchResult{
			Memories: []MemoryRecord{
				{ID: 1, Score: 0.9},
				{ID: 2, Score: 0.7},
				{ID: 3, Score: 0.5},
				{ID: 4, Score: 0.3},
			},
		},
	}
	vec := &mockHybridVec{
		searchResult: []VecSearchResult{
			{ID: 3, Score: 0.8},
			{ID: 4, Score: 0.6},
			{ID: 5, Score: 0.4},
			{ID: 6, Score: 0.2},
		},
	}
	embed := &mockHybridEmbed{vec: []float32{1, 0, 0}}

	h := NewHybridSearcher(store, vec, embed, zap.NewNop())
	results, err := h.Search(context.Background(), "test", 3, "")
	if err != nil {
		t.Fatal(err)
	}

	// 融合后取前 3
	if len(results) != 3 {
		t.Fatalf("期望 3 个结果，得到 %d", len(results))
	}
}

func TestHybridSearcher_Search_VecScoreZero(t *testing.T) {
	store := &mockHybridStore{
		searchResult: &SearchResult{
			Memories: []MemoryRecord{
				{ID: 1, Score: 0.9},
			},
		},
	}
	vec := &mockHybridVec{
		searchResult: []VecSearchResult{
			{ID: 2, Score: 0.0},  // Score <= 0 被过滤
			{ID: 3, Score: -0.1}, // 负分数
		},
	}
	embed := &mockHybridEmbed{vec: []float32{1, 0, 0}}

	h := NewHybridSearcher(store, vec, embed, zap.NewNop())
	results, err := h.Search(context.Background(), "test", 5, "")
	if err != nil {
		t.Fatal(err)
	}

	// vec 结果被过滤，只有 FTS 结果
	if len(results) != 1 {
		t.Errorf("期望 1 个结果，得到 %d", len(results))
	}
}

func TestHybridSearcher_Search_LimitZero(t *testing.T) {
	store := &mockHybridStore{
		searchResult: &SearchResult{
			Memories: []MemoryRecord{{ID: 1, Score: 0.9}},
		},
	}
	vec := &mockHybridVec{}
	embed := &mockHybridEmbed{vec: []float32{1, 0, 0}}

	h := NewHybridSearcher(store, vec, embed, zap.NewNop())
	// limit=0 默认为 10
	results, err := h.Search(context.Background(), "test", 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Errorf("期望 1 个结果，得到 %d", len(results))
	}
}

// --- fuse RRF 正确性 ---

var errFTS = assertNeverCalled{}
var errVec = assertNeverCalled{}
var errEmbed = assertNeverCalled{}

type assertNeverCalled struct{}

func (assertNeverCalled) Error() string { return "should not be called" }

func TestFuse_RRFScores(t *testing.T) {
	store := &mockHybridStore{}
	vec := &mockHybridVec{}
	embed := &mockHybridEmbed{vec: []float32{1, 0, 0}}
	h := NewHybridSearcher(store, vec, embed, zap.NewNop())

	ftsResult := &SearchResult{
		Memories: []MemoryRecord{
			{ID: 1}, // rank=0 → score = 1/(60+0+1) = 1/61
			{ID: 2}, // rank=1 → score = 1/(60+1+1) = 1/62
		},
	}
	vecResults := []VecSearchResult{
		{ID: 2, Score: 0.9}, // rank=0 → score = 1/61
		{ID: 3, Score: 0.8}, // rank=1 → score = 1/62
	}

	results := h.fuse(ftsResult, vecResults, 10)

	// ID=2 在两路都出现，RRF 分数 = 1/61 + 1/61 = 2/61
	// ID=1 只有 FTS = 1/61
	// ID=3 只有 vec = 1/62
	// 排序应为: 2 > 1 > 3
	if len(results) != 3 {
		t.Fatalf("期望 3 个结果，得到 %d", len(results))
	}
	if results[0].ID != 2 {
		t.Errorf("排序错误: 第一个应为 ID=2，得到 %d", results[0].ID)
	}
	if results[1].ID != 1 {
		t.Errorf("排序错误: 第二个应为 ID=1，得到 %d", results[1].ID)
	}
	if results[2].ID != 3 {
		t.Errorf("排序错误: 第三个应为 ID=3，得到 %d", results[2].ID)
	}

	// 验证 RRF 分数计算（按 ID 查找，而非按索引），返回前会归一化到 0..1。
	// FTS: ID=1 rank=0 → 1/61, ID=2 rank=1 → 1/62
	// Vec: ID=2 rank=0 → 1/61, ID=3 rank=1 → 1/62
	rawTop := 1.0/62.0 + 1.0/61.0
	wantScores := map[int64]float64{
		2: 1,                     // FTS rank=1 + vec rank=0
		1: (1.0 / 61.0) / rawTop, // FTS rank=0
		3: (1.0 / 62.0) / rawTop, // vec rank=1
	}
	for _, r := range results {
		expected, ok := wantScores[r.ID]
		if !ok {
			continue
		}
		if math.Abs(r.Score-expected) > 1e-10 {
			t.Errorf("ID=%d RRF 分数错误: 期望 %.10f，得到 %.10f", r.ID, expected, r.Score)
		}
	}
}

func TestFuse_BothNil(t *testing.T) {
	store := &mockHybridStore{}
	vec := &mockHybridVec{}
	embed := &mockHybridEmbed{vec: []float32{1, 0, 0}}
	h := NewHybridSearcher(store, vec, embed, zap.NewNop())

	results := h.fuse(nil, nil, 10)
	if results != nil {
		t.Errorf("期望 nil，得到 %v", results)
	}
}

func TestFuse_FTSNil(t *testing.T) {
	store := &mockHybridStore{}
	vec := &mockHybridVec{}
	embed := &mockHybridEmbed{vec: []float32{1, 0, 0}}
	h := NewHybridSearcher(store, vec, embed, zap.NewNop())

	results := h.fuse(nil, []VecSearchResult{{ID: 5, Score: 0.8}}, 10)
	if len(results) != 1 || results[0].ID != 5 {
		t.Errorf("期望 [{ID:5}]，得到 %v", results)
	}
}

func TestFuse_VecNil(t *testing.T) {
	store := &mockHybridStore{}
	vec := &mockHybridVec{}
	embed := &mockHybridEmbed{vec: []float32{1, 0, 0}}
	h := NewHybridSearcher(store, vec, embed, zap.NewNop())

	results := h.fuse(&SearchResult{Memories: []MemoryRecord{{ID: 7}}}, nil, 10)
	if len(results) != 1 || results[0].ID != 7 {
		t.Errorf("期望 [{ID:7}]，得到 %v", results)
	}
}

// --- sortScoredIDs 正确性 ---

func TestSortScoredIDs_Descending(t *testing.T) {
	ids := []ScoredID{
		{ID: 3, Score: 0.1},
		{ID: 1, Score: 0.9},
		{ID: 5, Score: 0.5},
		{ID: 2, Score: 0.7},
		{ID: 4, Score: 0.3},
	}

	sortScoredIDs(ids)

	expected := []int64{1, 2, 5, 4, 3}
	for i, id := range expected {
		if ids[i].ID != id {
			t.Errorf("位置 %d: 期望 ID=%d，得到 ID=%d (score=%.1f)", i, id, ids[i].ID, ids[i].Score)
		}
	}
}

func TestSortScoredIDs_AlreadySorted(t *testing.T) {
	ids := []ScoredID{
		{ID: 1, Score: 0.9},
		{ID: 2, Score: 0.7},
		{ID: 3, Score: 0.5},
	}

	sortScoredIDs(ids)

	for i := 1; i < len(ids); i++ {
		if ids[i].Score > ids[i-1].Score {
			t.Errorf("位置 %d: 分数 %.2f > 前一个 %.2f", i, ids[i].Score, ids[i-1].Score)
		}
	}
}

func TestSortScoredIDs_Empty(t *testing.T) {
	sortScoredIDs([]ScoredID{}) // 不应 panic
}

func TestSortScoredIDs_Single(t *testing.T) {
	sortScoredIDs([]ScoredID{{ID: 99, Score: 0.5}})
}
