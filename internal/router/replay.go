package router

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"sync"
)

// ReplayStore 保存可按 trace_id 查询的本地决策 span。
type ReplayStore struct {
	mu    sync.RWMutex
	spans []DecisionSpan
}

// NewReplayStore 创建纯内存 replay store。
func NewReplayStore() *ReplayStore {
	return &ReplayStore{}
}

// Append 追加一个决策 span。
func (s *ReplayStore) Append(span DecisionSpan) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.spans = append(s.spans, span)
}

// FindByTraceID 返回指定 trace_id 的 span，按 created_at 升序。
func (s *ReplayStore) FindByTraceID(traceID string) []DecisionSpan {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return filterAndSortDecisionSpans(s.spans, traceID)
}

// LastRouteDecisionSummary 重建指定 trace_id 下最后一个 RouteDecision 摘要。
func (s *ReplayStore) LastRouteDecisionSummary(traceID string) (RouteDecisionSummary, bool) {
	spans := s.FindByTraceID(traceID)
	if len(spans) == 0 {
		return RouteDecisionSummary{}, false
	}
	return RouteDecisionSummaryFromSpan(spans[len(spans)-1]), true
}

// WriteDecisionSpansJSONL 写出 JSONL 格式的本地 replay 文件。
func WriteDecisionSpansJSONL(w io.Writer, spans []DecisionSpan) error {
	enc := json.NewEncoder(w)
	for _, span := range spans {
		if err := enc.Encode(span); err != nil {
			return err
		}
	}
	return nil
}

// LoadDecisionSpansJSONL 从 JSONL 加载 replay span。
func LoadDecisionSpansJSONL(r io.Reader) ([]DecisionSpan, error) {
	scanner := bufio.NewScanner(r)
	var spans []DecisionSpan
	line := 0
	for scanner.Scan() {
		line++
		b := scanner.Bytes()
		if len(b) == 0 {
			continue
		}
		var span DecisionSpan
		if err := json.Unmarshal(b, &span); err != nil {
			return nil, fmt.Errorf("decision span jsonl line %d: %w", line, err)
		}
		spans = append(spans, span)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return spans, nil
}

// NewReplayStoreFromSpans 创建带初始数据的 replay store。
func NewReplayStoreFromSpans(spans []DecisionSpan) *ReplayStore {
	store := NewReplayStore()
	for _, span := range spans {
		store.Append(span)
	}
	return store
}

// RouteDecisionSummary 是 replay UI/调试所需的最后决策摘要。
type RouteDecisionSummary struct {
	TraceID        string                       `json:"trace_id"`
	SessionIDHash  string                       `json:"session_id_hash,omitempty"`
	IntentKind     IntentKind                   `json:"intent_kind"`
	IntentSource   string                       `json:"intent_source,omitempty"`
	IntentDegraded bool                         `json:"intent_degraded,omitempty"`
	AllowedTools   []string                     `json:"allowed_tools,omitempty"`
	AllowedInputs  map[string]map[string]string `json:"allowed_inputs,omitempty"`
	VisibleOnly    []string                     `json:"visible_only,omitempty"`
	BlockedTools   []string                     `json:"blocked_tools,omitempty"`
	BlockedReasons map[string]string            `json:"blocked_reasons,omitempty"`
	Mode           DecisionMode                 `json:"mode"`
	Reason         string                       `json:"reason,omitempty"`
}

// RouteDecisionSummaryFromSpan 从 span 重建 RouteDecision 摘要。
func RouteDecisionSummaryFromSpan(span DecisionSpan) RouteDecisionSummary {
	return RouteDecisionSummary{
		TraceID:        span.TraceID,
		SessionIDHash:  span.SessionIDHash,
		IntentKind:     span.Intent.Kind,
		IntentSource:   span.Intent.Source,
		IntentDegraded: span.Intent.Degraded,
		AllowedTools:   append([]string(nil), span.Allowed...),
		AllowedInputs:  cloneDecisionSpanInputs(span.AllowedInputs),
		VisibleOnly:    append([]string(nil), span.VisibleOnly...),
		BlockedTools:   append([]string(nil), span.Blocked...),
		BlockedReasons: cloneStringMap(span.BlockedReasons),
		Mode:           span.Mode,
		Reason:         span.Reason,
	}
}

func filterAndSortDecisionSpans(spans []DecisionSpan, traceID string) []DecisionSpan {
	out := make([]DecisionSpan, 0, len(spans))
	for _, span := range spans {
		if span.TraceID == traceID {
			out = append(out, span)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].CreatedAt.Before(out[j].CreatedAt)
	})
	return out
}

func cloneStringMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}
