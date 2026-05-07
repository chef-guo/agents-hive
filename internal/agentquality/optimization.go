package agentquality

import (
	"fmt"
	"regexp"
	"strings"
)

type SuggestionKind string

const (
	SuggestionPromptDiff      SuggestionKind = "prompt_diff_suggestion"
	SuggestionToolDescription SuggestionKind = "tool_description_suggestion"
	SuggestionSkillDraft      SuggestionKind = "skill_draft"
)

type OptimizationSuggestion struct {
	Kind           SuggestionKind `json:"kind"`
	Title          string         `json:"title"`
	Target         string         `json:"target,omitempty"`
	Rationale      string         `json:"rationale"`
	Proposed       string         `json:"proposed"`
	ReviewRequired bool           `json:"review_required"`
}

func BuildOptimizationSuggestions(rec CandidateRecord) []OptimizationSuggestion {
	if rec.SourceEvent.Name == EventReflection && isReflectionOptimizationFailure(rec.FailureType) {
		return buildReflectionFailureSuggestions(rec)
	}
	switch rec.FailureType {
	case FailureTool:
		return buildToolFailureSuggestions(rec)
	case FailureSkill:
		return []OptimizationSuggestion{buildSkillDraftSuggestion(rec)}
	case FailurePrompt:
		return []OptimizationSuggestion{buildPromptDiffSuggestion(rec)}
	case FailureContext:
		return []OptimizationSuggestion{{
			Kind:           SuggestionPromptDiff,
			Title:          "上下文污染防御提示建议",
			Target:         promptTarget(rec),
			Rationale:      "该失败被归因为 context，应让模型优先使用当前会话、附件和工具证据，避免被低置信历史记忆污染。",
			Proposed:       "在 system prompt 中补充：回答前必须区分当前会话证据、附件证据和历史 memory；当证据冲突时，以当前会话和附件为准，并说明被忽略的低置信记忆。",
			ReviewRequired: true,
		}}
	default:
		return nil
	}
}

func GoldenCaseFromPromotedCandidate(rec CandidateRecord) (Case, error) {
	if rec.Status != CandidatePromoted {
		return Case{}, fmt.Errorf("candidate %s is not promoted", rec.ID)
	}
	caseID := strings.TrimSpace(rec.PromotedCaseID)
	if caseID == "" {
		return Case{}, fmt.Errorf("promoted candidate requires promoted_case_id")
	}

	c := Case{
		ID:             caseID,
		Name:           firstNonEmpty(rec.Case.Name, "晋升回归用例 "+caseID),
		Route:          firstNonEmpty(rec.Case.Route, rec.Route),
		Input:          firstNonEmpty(rec.Case.Input, rec.Input),
		ExpectedTools:  append([]string(nil), rec.Case.ExpectedTools...),
		AllowedTools:   append([]string(nil), rec.Case.AllowedTools...),
		ExpectedSkills: append([]string(nil), rec.Case.ExpectedSkills...),
		ExpectedAgents: append([]string(nil), rec.Case.ExpectedAgents...),
		Scenario:       rec.Case.Scenario,
		ExpectedStatus: rec.Case.ExpectedStatus,
		FailureType:    firstFailureType(rec.Case.FailureType, rec.FailureType),
		Risk:           firstNonEmpty(rec.Case.Risk, rec.Risk),
		Required:       true,
		Notes:          buildPromotedNotes(rec),
	}
	if c.ExpectedStatus == "" || c.ExpectedStatus == StatusFail {
		c.ExpectedStatus = StatusPass
	}
	if c.Risk == "" {
		c.Risk = "safe"
	}
	if err := ValidateCase(c); err != nil {
		return Case{}, err
	}
	return c, nil
}

func buildToolFailureSuggestions(rec CandidateRecord) []OptimizationSuggestion {
	var out []OptimizationSuggestion
	out = append(out, buildPromptDiffSuggestion(rec))
	if tool := expectedToolName(rec); tool != "" {
		out = append(out, OptimizationSuggestion{
			Kind:           SuggestionToolDescription,
			Title:          "工具描述改写建议",
			Target:         tool,
			Rationale:      "该失败被归因为 tool，模型没有稳定选择预期工具；应把工具描述改成动作导向，并明确适用触发条件。",
			Proposed:       fmt.Sprintf("将 %s 的描述补强为：当用户要求定位代码、查找符号、确认仓库事实或验证当前文件内容时优先调用该工具；不要仅凭记忆回答。", tool),
			ReviewRequired: true,
		})
	}
	return out
}

func buildReflectionFailureSuggestions(rec CandidateRecord) []OptimizationSuggestion {
	reflection := rec.SourceEvent.Reflection
	trigger := firstNonEmpty(reflection.Trigger, "unknown")
	severity := firstNonEmpty(reflection.Severity, "unknown")
	var detail []string
	if reflection.Summary != "" {
		detail = append(detail, reflection.Summary)
	}
	if reflection.ToolName != "" {
		detail = append(detail, "tool="+reflection.ToolName)
	}
	if len(reflection.Recommended) > 0 {
		detail = append(detail, "recommended="+strings.Join(reflection.Recommended, "；"))
	}
	return []OptimizationSuggestion{{
		Kind:           SuggestionPromptDiff,
		Title:          "反思纠偏 Prompt 建议",
		Target:         promptTarget(rec),
		Rationale:      fmt.Sprintf("source event 是 quality.reflection，trigger=%s，severity=%s；该建议只能作为人工 review 草稿，不能自动 apply。", trigger, severity),
		Proposed:       fmt.Sprintf("review 草稿：当运行时反思触发 %s 时，先总结失败证据和连续失败次数，再要求模型改用更小、更可验证的下一步；参考线索：%s", trigger, firstNonEmpty(strings.Join(detail, "；"), strings.TrimSpace(rec.Input))),
		ReviewRequired: true,
	}}
}

func isReflectionOptimizationFailure(ft FailureType) bool {
	switch ft {
	case FailureRuntime, FailureTool, FailureModel:
		return true
	default:
		return false
	}
}

func buildPromptDiffSuggestion(rec CandidateRecord) OptimizationSuggestion {
	return OptimizationSuggestion{
		Kind:           SuggestionPromptDiff,
		Title:          "Prompt diff 建议",
		Target:         promptTarget(rec),
		Rationale:      "该失败需要人工评审后再修改生产 prompt；建议只作为 diff 草稿进入 review。",
		Proposed:       "review 草稿：建议补充规则：遇到仓库事实、文件路径、符号定位、版本信息、外部来源或未确认专有名词时，必须先调用合适工具取得证据；没有证据时不得凭记忆直接作答。",
		ReviewRequired: true,
	}
}

func buildSkillDraftSuggestion(rec CandidateRecord) OptimizationSuggestion {
	name := inferSkillName(rec.Input)
	if name == "" {
		name = "candidate-skill"
	}
	return OptimizationSuggestion{
		Kind:           SuggestionSkillDraft,
		Title:          "Skill 草稿建议",
		Target:         name,
		Rationale:      "该失败被归因为 skill，说明已有能力没有被稳定路由，或高频任务值得沉淀为可评审 skill 草稿。",
		Proposed:       fmt.Sprintf("---\nname: %s\ndescription: 从失败轨迹沉淀的候选 skill，必须人工 review 后才能安装或发布。\n---\n\n# 使用时机\n当用户输入与以下失败样例相似时使用：%s\n\n# 执行要求\n1. 先确认当前仓库/会话证据。\n2. 必要时调用工具，不得凭记忆回答。\n3. 输出前说明证据来源。\n", name, strings.TrimSpace(rec.Input)),
		ReviewRequired: true,
	}
}

func promptTarget(rec CandidateRecord) string {
	if rec.SourceEvent.Prompt.Key != "" {
		if rec.SourceEvent.Prompt.Version != "" {
			return rec.SourceEvent.Prompt.Key + "@" + rec.SourceEvent.Prompt.Version
		}
		return rec.SourceEvent.Prompt.Key
	}
	return "system/base"
}

func expectedToolName(rec CandidateRecord) string {
	if len(rec.Case.ExpectedTools) > 0 {
		return rec.Case.ExpectedTools[0]
	}
	if rec.SourceEvent.ToolDecision.Actual != "" {
		return rec.SourceEvent.ToolDecision.Actual
	}
	if len(rec.Case.AllowedTools) > 0 {
		return rec.Case.AllowedTools[0]
	}
	return ""
}

func inferSkillName(input string) string {
	lower := strings.ToLower(input)
	re := regexp.MustCompile(`([a-z0-9][a-z0-9_-]{1,48})\s*skill`)
	if m := re.FindStringSubmatch(lower); len(m) == 2 {
		return m[1]
	}
	if strings.Contains(lower, "review") || strings.Contains(input, "检查") {
		return "code-review"
	}
	return ""
}

func buildPromotedNotes(rec CandidateRecord) string {
	var parts []string
	if rec.ReviewNote != "" {
		parts = append(parts, rec.ReviewNote)
	}
	if rec.ReplayRef != "" {
		parts = append(parts, "source replay: "+rec.ReplayRef)
	}
	if rec.Fingerprint != "" {
		parts = append(parts, "fingerprint: "+rec.Fingerprint)
	}
	if len(parts) == 0 {
		return rec.Case.Notes
	}
	if rec.Case.Notes != "" {
		parts = append([]string{rec.Case.Notes}, parts...)
	}
	return strings.Join(parts, "\n")
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func firstFailureType(vals ...FailureType) FailureType {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
