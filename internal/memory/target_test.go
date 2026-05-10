package memory

import (
	"encoding/json"
	"testing"
)

func TestNormalizeMemoryRecordAddsDefaultTargetKindAndSubjectType(t *testing.T) {
	rec := &MemoryRecord{
		Type:     MemoryTypeProcedural,
		Content:  "使用 rg 搜索代码",
		Metadata: json.RawMessage(`{"source":"manual"}`),
	}

	err := NormalizeMemoryRecord(rec, RuntimeContext{UserID: "user-1", WorkspaceID: "ws-1"})

	if err != nil {
		t.Fatalf("NormalizeMemoryRecord error = %v", err)
	}
	if rec.UserID != "user-1" {
		t.Fatalf("UserID = %q, want user-1", rec.UserID)
	}
	var meta struct {
		Source      string       `json:"source"`
		Kind        string       `json:"kind"`
		SubjectType string       `json:"subject_type"`
		Target      MemoryTarget `json:"target"`
	}
	if err := json.Unmarshal(rec.Metadata, &meta); err != nil {
		t.Fatalf("metadata invalid: %v", err)
	}
	if meta.Source != "manual" {
		t.Fatalf("existing metadata lost: %s", rec.Metadata)
	}
	if meta.Kind != "procedural" || meta.SubjectType != "procedure" {
		t.Fatalf("kind/subject_type = %q/%q, want procedural/procedure", meta.Kind, meta.SubjectType)
	}
	if meta.Target.Scope != TargetScopeUser || meta.Target.Visibility != TargetVisibilityPrivate || meta.Target.UserID != "user-1" || meta.Target.ID != "user-1" {
		t.Fatalf("target = %+v, want private user target", meta.Target)
	}
}

func TestNormalizeMemoryRecordAddsTargetIDForSkillScope(t *testing.T) {
	rec := &MemoryRecord{
		Type:     MemoryTypeProcedural,
		Content:  "skill scoped",
		Metadata: json.RawMessage(`{"target":{"target_scope":"skill","visibility":"private"}}`),
	}

	err := NormalizeMemoryRecord(rec, RuntimeContext{UserID: "user-1", SkillName: "skill-a"})

	if err != nil {
		t.Fatalf("NormalizeMemoryRecord error = %v", err)
	}
	target := DecodeMemoryTarget(rec.Metadata, rec.Type, rec.UserID)
	if target.Scope != TargetScopeSkill || target.SkillName != "skill-a" || target.ID != "skill-a" {
		t.Fatalf("target = %+v, want skill-a target id", target)
	}
}

func TestNormalizeMemoryRecordRejectsDirtyTarget(t *testing.T) {
	rec := &MemoryRecord{
		Type:     MemoryTypeUser,
		Content:  "bad",
		UserID:   "user-1",
		Metadata: json.RawMessage(`{"target":{"target_scope":"planet","visibility":"private"}}`),
	}

	err := NormalizeMemoryRecord(rec, RuntimeContext{})

	if err == nil {
		t.Fatal("NormalizeMemoryRecord error = nil, want invalid target error")
	}
}

func TestDecodeMemoryTargetDefaultsOldData(t *testing.T) {
	target := DecodeMemoryTarget(nil, MemoryTypeUser, "user-1")

	if target.Scope != TargetScopeUser || target.Visibility != TargetVisibilityPrivate || target.UserID != "user-1" || target.ID != "user-1" {
		t.Fatalf("target = %+v, want old data private user defaults", target)
	}
}

func TestDecodeMemoryKindDerivesFromType(t *testing.T) {
	if got := DecodeMemoryKind(nil, MemoryTypeEpisodic); got != "episodic" {
		t.Fatalf("episodic kind = %q, want episodic", got)
	}

	if got := DecodeMemoryKind(nil, MemoryTypeProject); got != "semantic" {
		t.Fatalf("project kind = %q, want semantic", got)
	}
}
