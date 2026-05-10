package eval

import (
	"context"
	"reflect"
	"testing"

	"github.com/chef-guo/agents-hive/internal/memory"
)

func TestRunCasesSummarizesFixtures(t *testing.T) {
	summary, err := RunCases(context.Background(), "testdata")
	if err != nil {
		t.Fatalf("RunCases returned error: %v", err)
	}
	if summary.Total != RequiredFixtureCount {
		t.Fatalf("Total = %d, want %d", summary.Total, RequiredFixtureCount)
	}
	if summary.RequiredTotal != RequiredFixtureCount {
		t.Fatalf("RequiredTotal = %d, want %d", summary.RequiredTotal, RequiredFixtureCount)
	}
	if summary.Passed != summary.Total || summary.RequiredPassed != summary.RequiredTotal {
		t.Fatalf("summary = %+v, want all cases passed", summary)
	}
	if len(summary.Results) != summary.Total {
		t.Fatalf("len(Results) = %d, want %d", len(summary.Results), summary.Total)
	}
}

func TestFixtureMemoryStoreSortsByScoreBeforeLimit(t *testing.T) {
	store := fixtureMemoryStore{records: []memory.MemoryRecord{
		{ID: 1, UserID: "u1", Score: 0.1},
		{ID: 2, UserID: "u1", Score: 0.9},
		{ID: 3, UserID: "u1", Score: 0.5},
	}}
	got, err := store.Search(context.Background(), memory.SearchOptions{UserID: "u1", Limit: 2})
	if err != nil {
		t.Fatalf("Search returned error: %v", err)
	}
	if ids := memoryIDs(got.Memories); !reflect.DeepEqual(ids, []int64{2, 3}) {
		t.Fatalf("ids = %v, want [2 3]", ids)
	}
}

func TestFixtureMemoryStoreKeepsStableOrderWithoutScores(t *testing.T) {
	store := fixtureMemoryStore{records: []memory.MemoryRecord{
		{ID: 1, UserID: "u1"},
		{ID: 2, UserID: "u1"},
		{ID: 3, UserID: "u1"},
	}}
	got, err := store.Search(context.Background(), memory.SearchOptions{UserID: "u1", Limit: 2})
	if err != nil {
		t.Fatalf("Search returned error: %v", err)
	}
	if ids := memoryIDs(got.Memories); !reflect.DeepEqual(ids, []int64{1, 2}) {
		t.Fatalf("ids = %v, want [1 2]", ids)
	}
}

func memoryIDs(records []memory.MemoryRecord) []int64 {
	ids := make([]int64, 0, len(records))
	for _, rec := range records {
		ids = append(ids, rec.ID)
	}
	return ids
}
