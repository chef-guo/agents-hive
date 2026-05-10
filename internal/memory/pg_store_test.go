package memory

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
)

func TestFilterByMinScoreDropsZeroScoreWhenThresholdEnabled(t *testing.T) {
	memories := []MemoryRecord{
		{ID: 1, Score: 0},
		{ID: 2, Score: 0.2},
		{ID: 3, Score: 0.7},
	}

	got := filterByMinScore(memories, 0.5)

	if len(got) != 1 || got[0].ID != 3 {
		t.Fatalf("filterByMinScore returned %+v, want only ID 3", got)
	}
}

func TestFilterByMinScoreKeepsScorelessWhenThresholdDisabled(t *testing.T) {
	memories := []MemoryRecord{
		{ID: 1, Score: 0},
		{ID: 2, Score: 0.2},
	}

	got := filterByMinScore(memories, 0)

	if len(got) != 2 {
		t.Fatalf("filterByMinScore returned %+v, want all records", got)
	}
}

func TestUniquePositiveIDs(t *testing.T) {
	got := uniquePositiveIDs([]int64{3, 0, 2, 3, -1, 2, 1})
	want := []int64{3, 2, 1}
	if len(got) != len(want) {
		t.Fatalf("uniquePositiveIDs = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("uniquePositiveIDs = %+v, want %+v", got, want)
		}
	}
}

func TestMemoryRecordIDs(t *testing.T) {
	got := memoryRecordIDs([]MemoryRecord{{ID: 7}, {ID: 9}})
	want := []int64{7, 9}
	if len(got) != len(want) {
		t.Fatalf("memoryRecordIDs = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("memoryRecordIDs = %+v, want %+v", got, want)
		}
	}
}

func TestMemoryFirstClassColumnValuesFromNormalizedMetadata(t *testing.T) {
	record := &MemoryRecord{
		Type:   MemoryTypeProcedural,
		UserID: "user-1",
		Metadata: json.RawMessage(`{
			"target": {
				"target_scope": "agent",
				"visibility": "private",
				"agent_name": "schema-worker"
			},
			"kind": "procedural"
		}`),
	}
	if err := NormalizeMemoryRecord(record, RuntimeContext{UserID: "user-1", AgentName: "schema-worker"}); err != nil {
		t.Fatalf("NormalizeMemoryRecord() error = %v", err)
	}

	got := memoryFirstClassColumnValues(record.Metadata, record.Type, record.UserID, record.SessionID)

	if got.TargetScope != "agent" {
		t.Fatalf("TargetScope = %q, want agent", got.TargetScope)
	}
	if got.TargetID != "schema-worker" {
		t.Fatalf("TargetID = %q, want schema-worker", got.TargetID)
	}
	if got.Visibility != "private" {
		t.Fatalf("Visibility = %q, want private", got.Visibility)
	}
	if got.MemoryKind != "procedural" {
		t.Fatalf("MemoryKind = %q, want procedural", got.MemoryKind)
	}
	if got.SubjectType != "procedure" {
		t.Fatalf("SubjectType = %q, want procedure", got.SubjectType)
	}
}

func TestMemoryFirstClassColumnValuesDefaultsFromNormalizedMetadata(t *testing.T) {
	record := &MemoryRecord{
		Type:   MemoryTypeFeedback,
		UserID: "user-1",
	}
	if err := NormalizeMemoryRecord(record, RuntimeContext{UserID: "user-1"}); err != nil {
		t.Fatalf("NormalizeMemoryRecord() error = %v", err)
	}

	got := memoryFirstClassColumnValues(record.Metadata, record.Type, record.UserID, record.SessionID)

	if got.TargetScope != "user" {
		t.Fatalf("TargetScope = %q, want user", got.TargetScope)
	}
	if got.TargetID != "user-1" {
		t.Fatalf("TargetID = %q, want user-1", got.TargetID)
	}
	if got.Visibility != "private" {
		t.Fatalf("Visibility = %q, want private", got.Visibility)
	}
	if got.MemoryKind != "feedback" {
		t.Fatalf("MemoryKind = %q, want feedback", got.MemoryKind)
	}
	if got.SubjectType != "feedback" {
		t.Fatalf("SubjectType = %q, want feedback", got.SubjectType)
	}
}

func TestRelaxedSearchTermsPreferHighSignalTokens(t *testing.T) {
	got := relaxedSearchTerms("go test 失败了，我应该怎么定位失败测试？请按我的历史工作方式给我建议。")
	want := []string{"go", "test", "失败了", "定位失败测试"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("relaxedSearchTerms = %+v, want %+v", got, want)
	}
}

func TestRelaxedSearchTermsKeepsToolPatternTokens(t *testing.T) {
	got := relaxedSearchTerms("go test rg 怎么排查？")
	want := []string{"go", "rg", "test", "排查"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("relaxedSearchTerms = %+v, want %+v", got, want)
	}
}

func (m *mockHybridStore) BatchGet(_ context.Context, _ []int64) ([]MemoryRecord, error) {
	return nil, nil
}

func (s *governanceFallbackStore) BatchGet(context.Context, []int64) ([]MemoryRecord, error) {
	return nil, nil
}
