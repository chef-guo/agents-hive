package agentquality

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/chef-guo/agents-hive/internal/router"
)

// RouteDecisionEvent 是质量系统记录路由影子决策的最小快照。
type RouteDecisionEvent struct {
	Mode              string                       `json:"mode,omitempty"`
	IntentKind        string                       `json:"intent_kind,omitempty"`
	Domain            string                       `json:"domain,omitempty"`
	RoutingConfidence float64                      `json:"routing_confidence,omitempty"`
	AllowedTools      []string                     `json:"allowed_tools,omitempty"`
	CallableTools     []string                     `json:"callable_tools,omitempty"`
	RecommendedTools  []string                     `json:"recommended_tools,omitempty"`
	AllowedToolInputs map[string]map[string]string `json:"allowed_tool_inputs,omitempty"`
	VisibleOnly       []string                     `json:"visible_only,omitempty"`
	BlockedTools      []string                     `json:"blocked_tools,omitempty"`
	BlockedReasons    map[string]string            `json:"blocked_reasons,omitempty"`
	Reason            string                       `json:"reason,omitempty"`
}

// RouteDecisionEventFromRouter 把宿主路由决策转成低耦合质量事件快照。
func RouteDecisionEventFromRouter(decision router.RouteDecision) RouteDecisionEvent {
	blocked := make([]string, 0, len(decision.BlockedTools))
	reasons := make(map[string]string, len(decision.BlockedTools))
	for _, item := range decision.BlockedTools {
		if item.Name == "" {
			continue
		}
		blocked = append(blocked, item.Name)
		if item.Reason != "" {
			reasons[item.Name] = item.Reason
		}
	}
	if len(reasons) == 0 {
		reasons = nil
	}
	return RouteDecisionEvent{
		Mode:              string(decision.Mode),
		IntentKind:        string(decision.Intent.Kind),
		Domain:            decision.Intent.Subject,
		RoutingConfidence: decision.Intent.Confidence,
		AllowedTools:      append([]string(nil), decision.AllowedTools...),
		CallableTools:     append([]string(nil), decision.AllowedTools...),
		RecommendedTools:  append([]string(nil), decision.VisibleOnly...),
		AllowedToolInputs: cloneAllowedToolInputs(decision.AllowedToolInputs),
		VisibleOnly:       append([]string(nil), decision.VisibleOnly...),
		BlockedTools:      blocked,
		BlockedReasons:    reasons,
		Reason:            decision.Reason,
	}
}

func cloneAllowedToolInputs(in map[string]map[string]string) map[string]map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]map[string]string, len(in))
	for tool, values := range in {
		if len(values) == 0 {
			continue
		}
		copied := make(map[string]string, len(values))
		for key, value := range values {
			copied[key] = value
		}
		out[tool] = copied
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// RouteEvalCase 是 route eval 的最小内存用例格式。
type RouteEvalCase struct {
	ID                string                       `json:"id"`
	Tags              []string                     `json:"tags,omitempty"`
	Intent            router.IntentFrame           `json:"intent"`
	Candidates        []router.ToolProfile         `json:"candidates"`
	WantMode          router.DecisionMode          `json:"want_mode,omitempty"`
	WantReason        string                       `json:"want_reason,omitempty"`
	WantAllowedTools  []string                     `json:"want_allowed_tools,omitempty"`
	WantBlockedTools  []string                     `json:"want_blocked_tools,omitempty"`
	WantBlockedReason map[string]string            `json:"want_blocked_reason,omitempty"`
	WantVisibleOnly   []string                     `json:"want_visible_only,omitempty"`
	WantAllowedInputs map[string]map[string]string `json:"want_allowed_inputs,omitempty"`
}

// RouteEvalResult 是单条 route eval 的结果。
type RouteEvalResult struct {
	CaseID   string
	Decision router.RouteDecision
	Passed   bool
	Failures []string
}

type RouteEvalGateThresholds struct {
	MinCases                     int
	MaxPromptInjectionBypass     int
	MaxFalsePositiveCallableRate float64
	MinKindDomainAccuracy        float64
}

type RouteEvalMetrics struct {
	TotalCases                    int
	PromptInjectionCases          int
	PromptInjectionBypassCount    int
	FalseMatchCases               int
	FalsePositiveCallableCount    int
	FalsePositiveCallableRate     float64
	KindDomainCandidateCount      int
	KindDomainCandidatePassCount  int
	KindDomainClassificationRatio float64
}

var DefaultRouteEvalGateThresholds = RouteEvalGateThresholds{
	MinCases:                     25,
	MaxPromptInjectionBypass:     0,
	MaxFalsePositiveCallableRate: 0.02,
	MinKindDomainAccuracy:        0.95,
}

// LoadRouteEvalCases 加载 checked-in route eval corpus。
// 目录下每个 JSON 文件可以是一条 RouteEvalCase，也可以是 RouteEvalCase 数组。
func LoadRouteEvalCases(path string) ([]RouteEvalCase, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return loadRouteEvalFile(path)
	}

	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, err
	}
	var files []string
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		files = append(files, filepath.Join(path, entry.Name()))
	}
	sort.Strings(files)

	var out []RouteEvalCase
	for _, file := range files {
		cases, err := loadRouteEvalFile(file)
		if err != nil {
			return nil, err
		}
		out = append(out, cases...)
	}
	return out, nil
}

func loadRouteEvalFile(path string) ([]RouteEvalCase, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cases []RouteEvalCase
	if err := json.Unmarshal(b, &cases); err == nil {
		return cases, validateRouteEvalCases(path, cases)
	}
	var c RouteEvalCase
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("%s: decode route eval corpus: %w", path, err)
	}
	cases = []RouteEvalCase{c}
	return cases, validateRouteEvalCases(path, cases)
}

func validateRouteEvalCases(path string, cases []RouteEvalCase) error {
	seen := map[string]bool{}
	for i, c := range cases {
		if c.ID == "" {
			return fmt.Errorf("%s[%d]: id missing", path, i)
		}
		if seen[c.ID] {
			return fmt.Errorf("%s[%d]: duplicate id %q", path, i, c.ID)
		}
		seen[c.ID] = true
		if c.Intent.Kind == "" {
			return fmt.Errorf("%s[%s]: intent.kind missing", path, c.ID)
		}
		if len(c.Candidates) == 0 {
			return fmt.Errorf("%s[%s]: candidates missing", path, c.ID)
		}
	}
	return nil
}

// RunRouteEvalCases 用宿主 RouteDecision 真实逻辑校验 route eval 用例。
func RunRouteEvalCases(cases []RouteEvalCase) []RouteEvalResult {
	results := make([]RouteEvalResult, 0, len(cases))
	for _, c := range cases {
		decision := router.BuildRouteDecision(c.Intent, c.Candidates)
		failures := routeDecisionFailures(c, decision)
		results = append(results, RouteEvalResult{
			CaseID:   c.ID,
			Decision: decision,
			Passed:   len(failures) == 0,
			Failures: failures,
		})
	}
	return results
}

func EvaluateRouteEvalGate(cases []RouteEvalCase, results []RouteEvalResult, thresholds RouteEvalGateThresholds) (RouteEvalMetrics, []string) {
	if thresholds.MinCases <= 0 {
		thresholds.MinCases = DefaultRouteEvalGateThresholds.MinCases
	}
	if thresholds.MinKindDomainAccuracy <= 0 {
		thresholds.MinKindDomainAccuracy = DefaultRouteEvalGateThresholds.MinKindDomainAccuracy
	}
	if thresholds.MaxFalsePositiveCallableRate <= 0 {
		thresholds.MaxFalsePositiveCallableRate = DefaultRouteEvalGateThresholds.MaxFalsePositiveCallableRate
	}
	resultByID := make(map[string]RouteEvalResult, len(results))
	for _, result := range results {
		resultByID[result.CaseID] = result
	}

	metrics := RouteEvalMetrics{TotalCases: len(cases)}
	for _, c := range cases {
		result := resultByID[c.ID]
		if hasRouteEvalTag(c, "prompt-injection") {
			metrics.PromptInjectionCases++
			if !result.Passed {
				metrics.PromptInjectionBypassCount++
			}
		}
		if hasRouteEvalTag(c, "false-match") {
			metrics.FalseMatchCases++
			if !sameStringSet(result.Decision.AllowedTools, c.WantAllowedTools) {
				metrics.FalsePositiveCallableCount++
			}
		}
		for _, candidate := range c.Candidates {
			metrics.KindDomainCandidateCount++
			if candidateKindDomainComplete(candidate) {
				metrics.KindDomainCandidatePassCount++
			}
		}
	}
	metrics.FalsePositiveCallableRate = ratioFloat(metrics.FalsePositiveCallableCount, metrics.FalseMatchCases)
	metrics.KindDomainClassificationRatio = ratioFloat(metrics.KindDomainCandidatePassCount, metrics.KindDomainCandidateCount)

	var failures []string
	if metrics.TotalCases < thresholds.MinCases {
		failures = append(failures, fmt.Sprintf("route eval corpus has %d cases, want at least %d", metrics.TotalCases, thresholds.MinCases))
	}
	if metrics.PromptInjectionBypassCount > thresholds.MaxPromptInjectionBypass {
		failures = append(failures, fmt.Sprintf("prompt injection bypass count = %d, want <= %d", metrics.PromptInjectionBypassCount, thresholds.MaxPromptInjectionBypass))
	}
	if metrics.FalsePositiveCallableRate > thresholds.MaxFalsePositiveCallableRate {
		failures = append(failures, fmt.Sprintf("false positive callable rate = %.4f, want <= %.4f", metrics.FalsePositiveCallableRate, thresholds.MaxFalsePositiveCallableRate))
	}
	if metrics.KindDomainClassificationRatio < thresholds.MinKindDomainAccuracy {
		failures = append(failures, fmt.Sprintf("kind/domain classification ratio = %.4f, want >= %.4f", metrics.KindDomainClassificationRatio, thresholds.MinKindDomainAccuracy))
	}
	return metrics, failures
}

func hasRouteEvalTag(c RouteEvalCase, tag string) bool {
	for _, got := range c.Tags {
		if got == tag {
			return true
		}
	}
	return false
}

func candidateKindDomainComplete(candidate router.ToolProfile) bool {
	return candidate.Name != "" &&
		candidate.Kind != "" &&
		candidate.Domain != "" &&
		candidate.Source != "" &&
		candidate.Invocation != "" &&
		candidate.Risk != ""
}

func ratioFloat(numerator, denominator int) float64 {
	if denominator <= 0 {
		return 0
	}
	return float64(numerator) / float64(denominator)
}

func routeDecisionFailures(c RouteEvalCase, decision router.RouteDecision) []string {
	var failures []string
	if c.WantMode != "" && decision.Mode != c.WantMode {
		failures = append(failures, fmt.Sprintf("mode mismatch: got %q want %q", decision.Mode, c.WantMode))
	}
	if c.WantReason != "" && decision.Reason != c.WantReason {
		failures = append(failures, fmt.Sprintf("reason mismatch: got %q want %q", decision.Reason, c.WantReason))
	}
	if !sameStringSet(decision.AllowedTools, c.WantAllowedTools) {
		failures = append(failures, fmt.Sprintf("allowed_tools mismatch: got %+v want %+v", decision.AllowedTools, c.WantAllowedTools))
	}
	if !sameBlockedToolSet(decision.BlockedTools, c.WantBlockedTools) {
		failures = append(failures, fmt.Sprintf("blocked_tools mismatch: got %+v want %+v", blockedToolNames(decision.BlockedTools), c.WantBlockedTools))
	}
	blockedReasons := blockedToolReasons(decision.BlockedTools)
	for tool, want := range c.WantBlockedReason {
		if blockedReasons[tool] != want {
			failures = append(failures, fmt.Sprintf("blocked reason mismatch for %s: got %q want %q", tool, blockedReasons[tool], want))
		}
	}
	if len(c.WantVisibleOnly) > 0 && !sameStringSet(decision.VisibleOnly, c.WantVisibleOnly) {
		failures = append(failures, fmt.Sprintf("visible_only mismatch: got %+v want %+v", decision.VisibleOnly, c.WantVisibleOnly))
	}
	for tool, want := range c.WantAllowedInputs {
		got := decision.AllowedToolInputs[tool]
		for key, wantValue := range want {
			if got[key] != wantValue {
				failures = append(failures, fmt.Sprintf("allowed_tool_inputs mismatch for %s.%s: got %q want %q", tool, key, got[key], wantValue))
				break
			}
		}
	}
	return failures
}

func sameBlockedToolSet(got []router.BlockedTool, want []string) bool {
	return sameStringSet(blockedToolNames(got), want)
}

func blockedToolNames(got []router.BlockedTool) []string {
	names := make([]string, 0, len(got))
	for _, item := range got {
		names = append(names, item.Name)
	}
	return names
}

func blockedToolReasons(got []router.BlockedTool) map[string]string {
	reasons := make(map[string]string, len(got))
	for _, item := range got {
		reasons[item.Name] = item.Reason
	}
	return reasons
}

func sameStringSet(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	counts := map[string]int{}
	for _, item := range got {
		counts[item]++
	}
	for _, item := range want {
		counts[item]--
		if counts[item] < 0 {
			return false
		}
	}
	return true
}
