package observability

import (
	"context"
	"encoding/json"
	"sort"
	"time"
)

// TraceReader 提供 session 维度的可观测时间线查询。
type TraceReader interface {
	GetSessionTimeline(ctx context.Context, sessionID string, limit int) (TraceTimeline, error)
}

// TraceTimeline 是前端回放页使用的统一诊断视图。
type TraceTimeline struct {
	SessionID string              `json:"session_id"`
	TraceID   string              `json:"trace_id,omitempty"`
	Items     []TraceTimelineItem `json:"items"`
	AgentTree []AgentTraceNode    `json:"agent_tree,omitempty"`
}

type TraceTimelineItem struct {
	Kind         string         `json:"kind"` // span | quality_event
	TraceID      string         `json:"trace_id,omitempty"`
	SpanID       string         `json:"span_id,omitempty"`
	ParentSpanID string         `json:"parent_span_id,omitempty"`
	Operation    string         `json:"operation"`
	Service      string         `json:"service,omitempty"`
	Status       string         `json:"status,omitempty"`
	DurationMs   int            `json:"duration_ms,omitempty"`
	Attributes   map[string]any `json:"attributes,omitempty"`
	Timestamp    time.Time      `json:"timestamp"`
}

type AgentTraceEdge struct {
	ParentTraceID string `json:"parent_trace_id,omitempty"`
	ChildTraceID  string `json:"child_trace_id,omitempty"`
	AgentID       string `json:"agent_id,omitempty"`
	AgentType     string `json:"agent_type,omitempty"`
	GroupID       string `json:"group_id,omitempty"`
	Status        string `json:"status,omitempty"`
}

type AgentTraceNode struct {
	TraceID  string           `json:"trace_id"`
	AgentID  string           `json:"agent_id,omitempty"`
	Type     string           `json:"type,omitempty"`
	Status   string           `json:"status,omitempty"`
	Children []AgentTraceNode `json:"children,omitempty"`
}

// SortTraceTimelineItems 按时间稳定排序，时间相同按 span/operation 保持可重复输出。
func SortTraceTimelineItems(items []TraceTimelineItem) {
	sort.SliceStable(items, func(i, j int) bool {
		if !items[i].Timestamp.Equal(items[j].Timestamp) {
			return items[i].Timestamp.Before(items[j].Timestamp)
		}
		if items[i].TraceID != items[j].TraceID {
			return items[i].TraceID < items[j].TraceID
		}
		if items[i].SpanID != items[j].SpanID {
			return items[i].SpanID < items[j].SpanID
		}
		return items[i].Operation < items[j].Operation
	})
}

func BuildAgentTraceTree(items []TraceTimelineItem) []AgentTraceNode {
	edges := make([]AgentTraceEdge, 0)
	for _, item := range items {
		if item.Kind != "quality_event" || item.Operation != "quality.delegation" {
			continue
		}
		raw, ok := item.Attributes["quality_event"]
		if !ok {
			continue
		}
		edge, ok := delegationEdgeFromQualityEvent(raw)
		if ok && edge.ChildTraceID != "" {
			edges = append(edges, edge)
		}
	}
	return BuildAgentTraceTreeFromEdges(edges)
}

func BuildAgentTraceTreeFromEdges(edges []AgentTraceEdge) []AgentTraceNode {
	nodes := map[string]*AgentTraceNode{}
	childSet := map[string]bool{}
	for _, edge := range edges {
		parentID := edge.ParentTraceID
		childID := edge.ChildTraceID
		if childID == "" {
			continue
		}
		child := nodes[childID]
		if child == nil {
			child = &AgentTraceNode{TraceID: childID}
			nodes[childID] = child
		}
		child.AgentID = edge.AgentID
		child.Type = edge.AgentType
		child.Status = edge.Status
		if parentID == "" {
			continue
		}
		parent := nodes[parentID]
		if parent == nil {
			parent = &AgentTraceNode{TraceID: parentID}
			nodes[parentID] = parent
		}
		parent.Children = append(parent.Children, *child)
		childSet[childID] = true
	}
	roots := make([]AgentTraceNode, 0)
	for traceID, node := range nodes {
		if !childSet[traceID] {
			roots = append(roots, *node)
		}
	}
	sort.SliceStable(roots, func(i, j int) bool { return roots[i].TraceID < roots[j].TraceID })
	return roots
}

func delegationEdgeFromQualityEvent(raw any) (AgentTraceEdge, bool) {
	var b []byte
	switch v := raw.(type) {
	case json.RawMessage:
		b = v
	case []byte:
		b = v
	case string:
		b = []byte(v)
	default:
		var err error
		b, err = json.Marshal(v)
		if err != nil {
			return AgentTraceEdge{}, false
		}
	}
	var ev struct {
		Name        string `json:"name"`
		FinalStatus string `json:"final_status"`
		Delegation  struct {
			ParentTraceID string `json:"parent_trace_id"`
			ChildTraceID  string `json:"child_trace_id"`
			AgentID       string `json:"agent_id"`
			AgentType     string `json:"agent_type"`
			GroupID       string `json:"group_id"`
		} `json:"delegation"`
	}
	if err := json.Unmarshal(b, &ev); err != nil || ev.Name != "quality.delegation" {
		return AgentTraceEdge{}, false
	}
	return AgentTraceEdge{
		ParentTraceID: ev.Delegation.ParentTraceID,
		ChildTraceID:  ev.Delegation.ChildTraceID,
		AgentID:       ev.Delegation.AgentID,
		AgentType:     ev.Delegation.AgentType,
		GroupID:       ev.Delegation.GroupID,
		Status:        ev.FinalStatus,
	}, true
}
