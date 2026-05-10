package eval

import (
	"context"
	"errors"
	"sort"

	"github.com/chef-guo/agents-hive/internal/memory"
)

type fixtureMemoryStore struct {
	records         []memory.MemoryRecord
	exposeCrossUser bool
	batchGetIDs     []int64
	getCalls        int
}

func (s *fixtureMemoryStore) Save(context.Context, *memory.MemoryRecord) (int64, error) {
	return 0, nil
}

func (s *fixtureMemoryStore) Get(_ context.Context, id int64) (*memory.MemoryRecord, error) {
	s.getCalls++
	for i := range s.records {
		if s.records[i].ID == id {
			return &s.records[i], nil
		}
	}
	return nil, nil
}

func (s *fixtureMemoryStore) BatchGet(_ context.Context, ids []int64) ([]memory.MemoryRecord, error) {
	s.batchGetIDs = append([]int64(nil), ids...)
	wanted := map[int64]bool{}
	for _, id := range ids {
		wanted[id] = true
	}
	out := make([]memory.MemoryRecord, 0, len(ids))
	for _, rec := range s.records {
		if wanted[rec.ID] {
			out = append(out, rec)
		}
	}
	return out, nil
}

func (s *fixtureMemoryStore) Update(context.Context, *memory.MemoryRecord) error { return nil }
func (s *fixtureMemoryStore) Delete(context.Context, int64) error                { return nil }

func (s *fixtureMemoryStore) Search(_ context.Context, opts memory.SearchOptions) (*memory.SearchResult, error) {
	matches := s.filterFixtureRecords(opts)
	return &memory.SearchResult{Memories: matches, Total: len(matches)}, nil
}

func (s *fixtureMemoryStore) List(_ context.Context, opts memory.SearchOptions) (*memory.SearchResult, error) {
	matches := s.filterFixtureRecords(opts)
	return &memory.SearchResult{Memories: matches, Total: len(matches)}, nil
}

func (s *fixtureMemoryStore) Stats(context.Context) (*memory.MemoryStats, error) {
	return &memory.MemoryStats{}, nil
}

func (s *fixtureMemoryStore) SetEmbedding(memory.EmbeddingProvider, memory.VectorStore) {}
func (s *fixtureMemoryStore) Close() error                                              { return nil }

func (s *fixtureMemoryStore) filterFixtureRecords(opts memory.SearchOptions) []memory.MemoryRecord {
	return filterFixtureRecordsWithVisibility(s.records, opts, s.exposeCrossUser)
}

func filterFixtureRecords(records []memory.MemoryRecord, opts memory.SearchOptions) []memory.MemoryRecord {
	return filterFixtureRecordsWithVisibility(records, opts, false)
}

func filterFixtureRecordsWithVisibility(records []memory.MemoryRecord, opts memory.SearchOptions, exposeCrossUser bool) []memory.MemoryRecord {
	limit := opts.Limit
	if limit <= 0 {
		limit = 10
	}

	out := make([]memory.MemoryRecord, 0, len(records))
	hasScore := false
	for _, rec := range records {
		if !exposeCrossUser && opts.UserID != "" && rec.UserID != "" && rec.UserID != opts.UserID {
			continue
		}
		if opts.Type != "" && rec.Type != opts.Type {
			continue
		}
		if opts.SessionID != "" && rec.SessionID != opts.SessionID {
			continue
		}
		if len(opts.Tags) > 0 && !hasAllTags(rec.Tags, opts.Tags) {
			continue
		}
		if opts.MinScore > 0 && rec.Score > 0 && rec.Score < opts.MinScore {
			continue
		}
		if rec.Score != 0 {
			hasScore = true
		}
		out = append(out, rec)
	}
	if hasScore {
		sort.SliceStable(out, func(i, j int) bool {
			return out[i].Score > out[j].Score
		})
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

func hasAllTags(have []string, want []string) bool {
	set := map[string]bool{}
	for _, tag := range have {
		set[tag] = true
	}
	for _, tag := range want {
		if !set[tag] {
			return false
		}
	}
	return true
}

type fixtureMetricRecorder struct {
	events []memory.MetricEvent
}

func (r *fixtureMetricRecorder) RecordMemoryMetric(_ context.Context, event memory.MetricEvent) {
	r.events = append(r.events, event)
}

func (r *fixtureMetricRecorder) hasMetric(name string, labels map[string]string) bool {
	for _, event := range r.events {
		if event.Name != name {
			continue
		}
		matched := true
		for key, want := range labels {
			if got, ok := event.Labels[key]; !ok || got != want {
				matched = false
				break
			}
		}
		if matched {
			return true
		}
	}
	return false
}

type fixtureEmbedder struct {
	vector []float32
	err    error
}

func (e fixtureEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	if e.err != nil {
		return nil, e.err
	}
	out := make([][]float32, len(texts))
	for i := range texts {
		out[i] = append([]float32(nil), e.vector...)
	}
	return out, nil
}

func (e fixtureEmbedder) Dimensions() int {
	return len(e.vector)
}

type fixtureVectorStore struct {
	results []memory.VecSearchResult
	err     error
}

func (v fixtureVectorStore) Add(context.Context, int64, []float32) error { return nil }
func (v fixtureVectorStore) Remove(context.Context, int64) error         { return nil }

func (v fixtureVectorStore) Search(context.Context, []float32, int, string) ([]memory.VecSearchResult, error) {
	if v.err != nil {
		return nil, v.err
	}
	return append([]memory.VecSearchResult(nil), v.results...), nil
}

func (v fixtureVectorStore) Count(context.Context) (int, error) { return len(v.results), nil }
func (v fixtureVectorStore) Close() error                       { return nil }

func buildHybridFixtures(h HybridFixture) (memory.EmbeddingProvider, memory.VectorStore) {
	vector := make([]float32, 0, len(h.Embedding))
	for _, v := range h.Embedding {
		vector = append(vector, float32(v))
	}
	if len(vector) == 0 {
		vector = []float32{1, 0, 0}
	}
	var embedErr error
	if h.EmbedError != "" {
		embedErr = errors.New(h.EmbedError)
	}
	results := make([]memory.VecSearchResult, 0, len(h.VectorResults))
	for _, result := range h.VectorResults {
		results = append(results, memory.VecSearchResult{ID: result.ID, Score: result.Score})
	}
	var vecErr error
	if h.VectorError != "" {
		vecErr = errors.New(h.VectorError)
	}
	return fixtureEmbedder{vector: vector, err: embedErr}, fixtureVectorStore{results: results, err: vecErr}
}
