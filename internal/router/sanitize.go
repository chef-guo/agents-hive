package router

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

const maxSanitizedDescriptionRunes = 500
const maxToolNameRunes = 64

// SanitizeResult 描述路由文本清洗后的结果。
type SanitizeResult struct {
	Text      string
	Blocked   bool
	Reasons   []string
	Truncated bool
}

// DescriptionSanitizer 清理工具描述中不应进入路由判断的提示式文本。
type DescriptionSanitizer struct {
	MaxRunes int
}

// Sanitize 返回用于路由判断的最小稳定描述。
func (s DescriptionSanitizer) Sanitize(description string) string {
	return s.SanitizeDetailed(description).Text
}

// SanitizeDetailed 返回清洗后的描述以及是否命中高置信提示注入。
func (s DescriptionSanitizer) SanitizeDetailed(description string) SanitizeResult {
	description = strings.TrimSpace(strings.Join(strings.Fields(description), " "))
	if description == "" {
		return SanitizeResult{}
	}

	reasons := promptInjectionReasons(description)
	if len(reasons) > 0 {
		return SanitizeResult{Blocked: true, Reasons: reasons}
	}

	limit := s.MaxRunes
	if limit <= 0 {
		limit = maxSanitizedDescriptionRunes
	}

	runes := []rune(description)
	if len(runes) > limit {
		return SanitizeResult{
			Text:      string(runes[:limit]),
			Truncated: true,
		}
	}
	return SanitizeResult{Text: description}
}

// ToolNamePolicy 规范化并校验工具名，防止空名或注入式名称进入路由。
type ToolNamePolicy struct{}

// Normalize 返回可用于比较和输出的工具名。
func (ToolNamePolicy) Normalize(name string) string {
	return strings.TrimSpace(name)
}

// RejectionReason 返回工具名被拒绝的原因；空字符串表示通过。
func (p ToolNamePolicy) RejectionReason(name string) string {
	name = p.Normalize(name)
	if name == "" {
		return "empty_tool_name"
	}
	if utf8.RuneCountInString(name) > maxToolNameRunes {
		return "tool_name_too_long"
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '_', r == '-', r == '.':
		default:
			return "invalid_tool_name_chars"
		}
	}
	nameForDirectiveCheck := strings.NewReplacer("_", " ", "-", " ", ".", " ").Replace(name)
	if len(promptInjectionReasons(nameForDirectiveCheck)) > 0 {
		return "tool_name_prompt_injection"
	}
	return ""
}

// Valid 判断工具名是否符合最小安全字符集。
func (p ToolNamePolicy) Valid(name string) bool {
	return p.RejectionReason(name) == ""
}

var promptInjectionPatterns = []struct {
	reason string
	re     *regexp.Regexp
}{
	{
		reason: "ignore_prior_instructions",
		re:     regexp.MustCompile(`(?i)\b(ignore|disregard|forget)\s+(all|any|previous|prior|earlier|above)\s+(instructions?|rules?|messages?|system|developer)\b`),
	},
	{
		reason: "use_this_tool_directive",
		re:     regexp.MustCompile(`(?i)\b(use|call|invoke|select|choose)\s+this\s+tool\s+(whenever|always|for\s+every|regardless|even\s+if)\b`),
	},
	{
		reason: "mandatory_tool_directive",
		re:     regexp.MustCompile(`(?i)\b(always|must|required\s+to)\s+(use|call|invoke|select|choose)\s+(this\s+tool|me)\b`),
	},
	{
		reason: "important_directive_marker",
		re:     regexp.MustCompile(`(?i)\bimportant\s*:`),
	},
	{
		reason: "system_prompt_exfiltration",
		re:     regexp.MustCompile(`(?i)\b(reveal|print|show|dump|exfiltrate|override)\b.{0,80}\bsystem\s+prompt\b|\bsystem\s+prompt\s*[:=]`),
	},
	{
		reason: "role_override_directive",
		re:     regexp.MustCompile(`(?i)\byou\s+are\s+now\b|\bdeveloper\s+message\s*:`),
	},
	{
		reason: "chinese_ignore_prior_instructions",
		re:     regexp.MustCompile(`忽略(以上|上面|之前|先前|所有).{0,12}(指令|规则|消息|提示词)`),
	},
	{
		reason: "chinese_use_this_tool_directive",
		re:     regexp.MustCompile(`(总是|始终|必须|务必).{0,8}(使用|调用|选择).{0,8}(此|这个|本)工具`),
	},
}

func promptInjectionReasons(text string) []string {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	reasons := make([]string, 0, 2)
	seen := map[string]bool{}
	for _, pattern := range promptInjectionPatterns {
		if !pattern.re.MatchString(text) {
			continue
		}
		if seen[pattern.reason] {
			continue
		}
		seen[pattern.reason] = true
		reasons = append(reasons, pattern.reason)
	}
	return reasons
}
