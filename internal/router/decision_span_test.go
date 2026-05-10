package router

import (
	"bytes"
	"testing"
	"time"
)

func TestDecisionSpanFromRouteDecisionCapturesReplayFields(t *testing.T) {
	createdAt := time.Date(2026, 5, 8, 10, 0, 0, 0, time.UTC)
	candidates := []ToolProfile{
		{
			Name:       "read_file",
			Kind:       CapabilityKindBuiltinTool,
			Domain:     "filesystem",
			Source:     CapabilitySourceBuiltin,
			Invocation: InvocationDirectTool,
			Risk:       RiskReadOnly,
			Trust:      TrustBuiltIn,
			ReadOnly:   true,
		},
		UnknownMCPToolProfile("unknown_send"),
	}
	decision := BuildRouteDecision(IntentFrame{
		Kind:       IntentRead,
		Subject:    "config",
		Confidence: 0.91,
	}, candidates)

	span := NewDecisionSpan(decision, candidates, DecisionSpanOptions{
		TraceID:        "trace-1",
		SessionIDHash:  "session-hash",
		CreatedAt:      createdAt,
		IntentSource:   "classifier",
		IntentDegraded: true,
	})

	if span.TraceID != "trace-1" || span.SessionIDHash != "session-hash" {
		t.Fatalf("span trace/session mismatch: %+v", span)
	}
	if !span.CreatedAt.Equal(createdAt) {
		t.Fatalf("CreatedAt = %s, want %s", span.CreatedAt, createdAt)
	}
	if span.Intent.Kind != IntentRead || span.Intent.Confidence != 0.91 || span.Intent.Source != "classifier" || !span.Intent.Degraded {
		t.Fatalf("Intent projection mismatch: %+v", span.Intent)
	}
	if len(span.Candidates) != 2 || span.Candidates[0].Name != "read_file" || span.Candidates[1].Name != "unknown_send" {
		t.Fatalf("Candidates = %+v", span.Candidates)
	}
	if len(span.Allowed) != 1 || span.Allowed[0] != "read_file" {
		t.Fatalf("Allowed = %+v", span.Allowed)
	}
	if span.BlockedReasons["unknown_send"] != "unknown destructive/open-world tool" {
		t.Fatalf("BlockedReasons = %+v", span.BlockedReasons)
	}
	if span.Mode != DecisionModeAllow || span.Reason != "matched intent and capability profile" {
		t.Fatalf("decision fields mismatch: mode=%q reason=%q", span.Mode, span.Reason)
	}
}

func TestReplayStoreReturnsSpansByTraceIDAndRouteDecisionSummary(t *testing.T) {
	first := DecisionSpan{
		TraceID:       "trace-1",
		SessionIDHash: "session-hash",
		CreatedAt:     time.Date(2026, 5, 8, 10, 0, 0, 0, time.UTC),
		Intent:        DecisionSpanIntent{Kind: IntentPlan, Source: "fallback", Degraded: true},
		VisibleOnly:   []string{"tool_search"},
		Mode:          DecisionModeDiscover,
		Reason:        "discovery only",
	}
	last := DecisionSpan{
		TraceID:       "trace-1",
		SessionIDHash: "session-hash",
		CreatedAt:     time.Date(2026, 5, 8, 10, 1, 0, 0, time.UTC),
		Intent:        DecisionSpanIntent{Kind: IntentExternalWrite, Source: "classifier"},
		Allowed:       []string{"feishu_api"},
		VisibleOnly:   []string{"tool_search"},
		Blocked:       []string{"write_file"},
		BlockedReasons: map[string]string{
			"write_file": "side effect not allowed by intent",
		},
		Mode:   DecisionModeAllow,
		Reason: "matched intent and capability profile",
	}
	store := NewReplayStoreFromSpans([]DecisionSpan{
		last,
		{TraceID: "trace-2", CreatedAt: first.CreatedAt, Intent: DecisionSpanIntent{Kind: IntentRead}},
		first,
	})

	spans := store.FindByTraceID("trace-1")
	if len(spans) != 2 {
		t.Fatalf("FindByTraceID returned %d spans, want 2", len(spans))
	}
	if spans[0].Intent.Kind != IntentPlan || spans[1].Intent.Kind != IntentExternalWrite {
		t.Fatalf("spans not sorted by CreatedAt: %+v", spans)
	}

	summary, ok := store.LastRouteDecisionSummary("trace-1")
	if !ok {
		t.Fatal("LastRouteDecisionSummary ok=false, want true")
	}
	if summary.IntentKind != IntentExternalWrite || summary.Mode != DecisionModeAllow {
		t.Fatalf("summary mismatch: %+v", summary)
	}
	if len(summary.AllowedTools) != 1 || summary.AllowedTools[0] != "feishu_api" {
		t.Fatalf("summary AllowedTools = %+v", summary.AllowedTools)
	}
	if summary.BlockedReasons["write_file"] != "side effect not allowed by intent" {
		t.Fatalf("summary BlockedReasons = %+v", summary.BlockedReasons)
	}
}

func TestReplayJSONLLoadsDecisionSpanAndRebuildsRouteDecisionSummary(t *testing.T) {
	span := DecisionSpan{
		TraceID:       "trace-jsonl",
		SessionIDHash: "session-jsonl",
		CreatedAt:     time.Date(2026, 5, 8, 10, 2, 0, 0, time.UTC),
		Intent:        DecisionSpanIntent{Kind: IntentCreateSkill, Source: "rule"},
		Allowed:       []string{"skill"},
		AllowedInputs: map[string]map[string]string{"skill": {"name": "skill-creator"}},
		VisibleOnly:   []string{"tool_search"},
		Mode:          DecisionModeAllow,
		Reason:        "matched intent and capability profile",
	}

	var buf bytes.Buffer
	if err := WriteDecisionSpansJSONL(&buf, []DecisionSpan{span}); err != nil {
		t.Fatalf("WriteDecisionSpansJSONL: %v", err)
	}
	loaded, err := LoadDecisionSpansJSONL(&buf)
	if err != nil {
		t.Fatalf("LoadDecisionSpansJSONL: %v", err)
	}

	store := NewReplayStoreFromSpans(loaded)
	summary, ok := store.LastRouteDecisionSummary("trace-jsonl")
	if !ok {
		t.Fatal("LastRouteDecisionSummary ok=false, want true")
	}
	if summary.IntentKind != IntentCreateSkill || summary.AllowedInputs["skill"]["name"] != "skill-creator" {
		t.Fatalf("summary mismatch after JSONL replay: %+v", summary)
	}
}
