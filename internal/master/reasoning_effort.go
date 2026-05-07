package master

import (
	"strings"
	"unicode/utf8"

	"github.com/chef-guo/agents-hive/internal/airouter"
	"github.com/chef-guo/agents-hive/internal/config"
	"github.com/chef-guo/agents-hive/internal/llm"
)

func resolveReasoningEffort(manual, input, model string) string {
	if manual != "" {
		return manual
	}
	if !modelSupportsAutoReasoningEffort(model) {
		return ""
	}
	return autoReasoningEffort(input)
}

func (m *Master) resolveRequestReasoningEffort(input string) string {
	if m == nil {
		return ""
	}
	if !reasoningEffortAutoEnabled(m.config.ReasoningEffortAuto) {
		return ""
	}
	if m.router != nil {
		if !m.router.SupportsAutoReasoningEffort(airouter.TaskChat) {
			return ""
		}
		return applyReasoningEffortDefault(autoReasoningEffort(input), m.config.ReasoningEffortAuto)
	}
	if m.llmClient == nil {
		return ""
	}
	if !modelSupportsAutoReasoningEffort(m.llmClient.Model()) {
		return ""
	}
	return applyReasoningEffortDefault(autoReasoningEffort(input), m.config.ReasoningEffortAuto)
}

func (m *Master) resolveModelReasoningEffort(manual, input, model string) string {
	if manual != "" {
		return manual
	}
	if m == nil || !reasoningEffortAutoEnabled(m.config.ReasoningEffortAuto) {
		return ""
	}
	if !modelSupportsAutoReasoningEffort(model) {
		return ""
	}
	return applyReasoningEffortDefault(autoReasoningEffort(input), m.config.ReasoningEffortAuto)
}

func modelSupportsAutoReasoningEffort(model string) bool {
	meta := llm.GetModelMeta(model)
	return meta != nil && meta.Capabilities.Reasoning && len(meta.Capabilities.ReasoningEfforts) > 0
}

func autoReasoningEffort(input string) string {
	text := strings.ToLower(input)
	runes := utf8.RuneCountInString(input)
	highSignals := []string{
		"architecture", "设计", "迁移", "migration", "tradeoff", "权衡",
		"analyze", "分析", "debug", "root cause", "根因", "plan", "计划",
		"edge case", "边界", "refactor", "重构",
	}
	mediumSignals := []string{
		"implement", "实现", "add test", "测试", "fix", "修复",
		"build", "创建", "update", "修改", "review", "检查",
	}

	highScore := 0
	for _, signal := range highSignals {
		if strings.Contains(text, signal) {
			highScore++
		}
	}
	if highScore >= 2 || runes > 900 {
		return "high"
	}

	for _, signal := range mediumSignals {
		if strings.Contains(text, signal) {
			return "medium"
		}
	}
	if highScore == 1 || runes > 240 {
		return "medium"
	}
	return "low"
}

func reasoningEffortAutoEnabled(cfg config.ReasoningEffortAutoConfig) bool {
	if cfg.Enabled {
		return true
	}
	// master.Config 的零值来自大量测试和 CLI 内部构造，按产品默认视为开启。
	return strings.TrimSpace(cfg.DefaultLevel) == ""
}

func applyReasoningEffortDefault(effort string, cfg config.ReasoningEffortAutoConfig) string {
	if effort != "" && effort != "low" {
		return effort
	}
	switch cfg.DefaultLevel {
	case "medium", "high":
		return cfg.DefaultLevel
	default:
		if effort != "" {
			return effort
		}
		return "low"
	}
}
