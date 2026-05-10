package eval

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/chef-guo/agents-hive/internal/memory"
)

func TestBuildRecordsAndAssertResult(t *testing.T) {
	c := Case{
		ID: "mc",
		Memories: []MemoryFixture{
			{ID: 1, UserID: "u1", Type: "user", Content: "可信", Confidence: 0.9},
		},
		WantInjectedIDs: []int64{1},
	}

	records, err := BuildRecords(c)
	if err != nil {
		t.Fatalf("BuildRecords returned error: %v", err)
	}
	if records[0].ID != 1 {
		t.Fatalf("record ID = %d, want 1", records[0].ID)
	}
	if err := AssertResult(c, memory.InjectionResult{
		Text:     "可信",
		Memories: []memory.InjectedMemory{{ID: 1, Type: memory.MemoryTypeUser}},
	}); err != nil {
		t.Fatalf("AssertResult returned error: %v", err)
	}
}

func TestBuildRecordsMapsExtendedFixtureFields(t *testing.T) {
	score := 0.72
	c := Case{
		ID: "mc",
		Memories: []MemoryFixture{
			{
				ID:          1,
				UserID:      "u1",
				Type:        "project",
				Content:     "x",
				Confidence:  0.9,
				Score:       &score,
				Tags:        []string{"route", "ci"},
				SessionID:   "s1",
				TargetScope: "workspace",
				WorkspaceID: "ws-1",
				ProjectID:   "proj-1",
				MemoryKind:  "semantic",
				SubjectType: "architecture",
			},
		},
	}
	records, err := BuildRecords(c)
	if err != nil {
		t.Fatalf("BuildRecords returned error: %v", err)
	}
	got := records[0]
	if got.Score != score || got.SessionID != "s1" || len(got.Tags) != 2 {
		t.Fatalf("record routing fields = %+v", got)
	}
	var meta map[string]any
	if err := json.Unmarshal(got.Metadata, &meta); err != nil {
		t.Fatalf("metadata json invalid: %v", err)
	}
	target, ok := meta["target"].(map[string]any)
	if !ok {
		t.Fatalf("metadata target = %+v, want object", meta["target"])
	}
	if target["target_scope"] != "workspace" || target["target_id"] != "ws-1" || meta["kind"] != "semantic" || meta["subject_type"] != "architecture" {
		t.Fatalf("metadata = %+v, want routing fields", meta)
	}
}

func TestAssertMetadataAndScope(t *testing.T) {
	c := Case{
		ID:     "mc",
		UserID: "u1",
		Memories: []MemoryFixture{
			{ID: 1, UserID: "u1", Type: "procedural", Content: "skill", TargetScope: "skill", SkillName: "memory-eval", MemoryKind: "procedural", SubjectType: "procedure"},
		},
		WantMetadata: []MetadataWant{{
			MemoryID:    1,
			TargetScope: "skill",
			TargetID:    "memory-eval",
			SkillName:   "memory-eval",
			Kind:        "procedural",
			SubjectType: "procedure",
		}},
		ScopeAssertions: []ScopeWant{{
			MemoryID:       1,
			RuntimeContext: RuntimeContext{UserID: "u1", SkillName: "memory-eval"},
			Allowed:        true,
			Reason:         "same_skill",
		}},
	}
	records, err := BuildRecords(c)
	if err != nil {
		t.Fatalf("BuildRecords returned error: %v", err)
	}
	if err := AssertMetadata(c, records); err != nil {
		t.Fatalf("AssertMetadata returned error: %v", err)
	}
	if err := AssertScope(c, records); err != nil {
		t.Fatalf("AssertScope returned error: %v", err)
	}
}

func TestAssertResultRejectsForbiddenText(t *testing.T) {
	err := AssertResult(Case{ID: "mc", ForbiddenText: []string{"secret"}}, memory.InjectionResult{Text: "secret"})
	if err == nil {
		t.Fatal("expected forbidden text error")
	}
}

func TestAssertResultRequiresSkippedIDEvidence(t *testing.T) {
	err := AssertResult(Case{ID: "mc", WantSkippedIDs: []int64{2}}, memory.InjectionResult{})
	if err == nil {
		t.Fatal("expected missing skipped id error")
	}
	err = AssertResult(Case{ID: "mc", WantSkippedIDs: []int64{2}}, memory.InjectionResult{
		SkippedMemoryIDs: []int64{2},
	})
	if err != nil {
		t.Fatalf("AssertResult returned error: %v", err)
	}
}

func TestFixturesRunThroughInjector(t *testing.T) {
	cases, err := LoadCases("testdata")
	if err != nil {
		t.Fatalf("LoadCases returned error: %v", err)
	}
	for _, loaded := range cases {
		if err := runCase(context.Background(), loaded); err != nil {
			t.Fatalf("runCase(%s) returned error: %v", loaded.Path, err)
		}
	}
}
