package observability

import (
	"testing"
	"time"
)

func TestSortTraceTimelineItems(t *testing.T) {
	t2 := time.Date(2026, 5, 6, 10, 0, 2, 0, time.UTC)
	t1 := time.Date(2026, 5, 6, 10, 0, 1, 0, time.UTC)
	items := []TraceTimelineItem{
		{Operation: "b", Timestamp: t2},
		{Operation: "a", Timestamp: t1},
	}

	SortTraceTimelineItems(items)

	if items[0].Operation != "a" {
		t.Fatalf("first operation = %q, want a", items[0].Operation)
	}
}
