package master

import (
	"encoding/json"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/chef-guo/agents-hive/internal/config"
	"github.com/chef-guo/agents-hive/internal/imctx"
	"github.com/chef-guo/agents-hive/internal/llm"
	"github.com/chef-guo/agents-hive/internal/mcphost"
	"github.com/chef-guo/agents-hive/internal/router"
)

// ToolChoiceMode 合法值集合（与 openai-go SDK 对齐）。
const (
	ToolChoiceAuto     = "auto"
	ToolChoiceRequired = "required"
	ToolChoiceNone     = "none"
)

var (
	// urlPattern 粗粒度匹配 http/https URL、github.com/ 等域名片段。
	// P0-A 目标：截图那种"女娲.skill / 仓库地址"里的域名片段必须触发。
	urlPattern = regexp.MustCompile(`(?i)(https?://\S+|[\w-]+\.(com|org|net|io|cn|dev|ai|so)/\S*|github\.com/\S+)`)

	// filePathPattern 匹配绝对路径 / 明显的相对路径 / 文件扩展名引用。
	filePathPattern = regexp.MustCompile(`(^|\s)(/[\w./-]+|\./[\w./-]+|\w+/[\w./-]+\.\w+)`)

	// skillRefPattern 匹配 "X.skill" / "X.skl" / ".skill" 结尾的标识符。
	// 女娲.skill / 女娲.skll 这类都要命中。
	skillRefPattern = regexp.MustCompile(`[\p{L}\p{N}_-]+\.(skill|skll|skl|skils|skills)\b`)

	// whatIsPattern 匹配"X 是什么 / X 怎么用 / how to use X / what is X"。
	// 捕获组 1 = 目标名词（去首尾空白后用于查 skillsIndex）。
	// M7 扩招：加入口语变体（是啥 / 干嘛的 / 咋用 / 怎么搞 / 到底是什么 / 是什么吗 / 是干什么的）。
	whatIsPattern   = regexp.MustCompile(`^\s*(.{1,50}?)\s*(到底是什么|到底怎么用|是什么吗|是什么呢|是什么|是啥|是干嘛的|是干什么的|干嘛的|怎么用|咋用|怎么搞|如何使用|怎么使用)[？?。.~～]?\s*$`)
	whatIsPatternEN = regexp.MustCompile(`(?i)^\s*(what\s+is|what\s+does|what\s+are|how\s+to\s+use|how\s+do\s+i\s+use|how\s+does|tell\s+me\s+about)\s+(.{1,50}?)[?.]?\s*$`)

	// whatIsFallbackSuffixes 用于 Contains 兜底：句中含这些后缀也视作 whatIs 意图。
	// 用于命中"我想知道 X 是什么呢这样的事" / "那 X 到底怎么用啊" 这类非锚定句。
	// 按长度降序：保证 "到底怎么用" 优先于 "怎么用"，避免抓到 "到底" 这种填充词。
	whatIsFallbackSuffixes = []string{"到底怎么用", "到底是什么", "是干什么的", "是什么吗", "是什么呢", "是什么", "是啥", "怎么用", "咋用", "干嘛的", "干什么的"}
	// whatIsFillerPrefixes 从 whatIs X 模式抽出的 X 若以这些填充词开头，应剥离。
	// 按长度降序：防止 "这" 先命中后，"这个 X" 只剥 1 字符留下 "个 X"。
	whatIsFillerPrefixes = []string{"我想知道", "我想问", "请问", "那个", "这个", "那", "这"}

	// chitchatPhrases 短纯闲聊集合（完全匹配）。
	// 出错风险点：误判"你好，X 是什么"为 none。所以只接受短且不含疑问词的输入。
	chitchatPhrases = map[string]bool{
		"你好":        true,
		"您好":        true,
		"早":         true,
		"早上好":       true,
		"晚上好":       true,
		"谢谢":        true,
		"多谢":        true,
		"感谢":        true,
		"辛苦了":       true,
		"再见":        true,
		"拜拜":        true,
		"ok":        true,
		"好的":        true,
		"收到":        true,
		"hi":        true,
		"hello":     true,
		"hey":       true,
		"thanks":    true,
		"thank you": true,
		"bye":       true,
		"goodbye":   true,
	}
)

// detectToolChoice 根据最新用户输入决定本轮的 ToolChoice 策略。
//
// 返回值：
//   - ToolChoiceRequired：明确需要工具才能正确回答（含 URL / 文件路径 / ".skill" 引用 /
//     "X 是什么" 模式且 X 未在已知 skillsIndex 内）
//   - ToolChoiceNone：明确纯闲聊（问候 / 致谢 / 告别，短输入，无疑问词）
//   - ToolChoiceAuto：其他情况，交给模型自行决定（保持当前默认行为）
//
// skillsIndex：可选，已安装的 skill name → true 的映射。nil 时 "X 是什么" 规则退化为
// 只要命中该模式就返回 required（保守立场，宁可多搜）。
//
// 见 docs/计划与路线/Agent-质量护栏治理计划.md §2 P0-A。
func detectToolChoice(userQuery string, skillsIndex map[string]bool) string {
	return detectToolChoiceWithContext(userQuery, skillsIndex, nil)
}

func detectToolChoiceWithIntent(userQuery string, skillsIndex map[string]bool, refs []imctx.DocRef, intent router.IntentFrame) string {
	return detectToolChoiceWithIntentAndMessages(userQuery, skillsIndex, refs, intent, nil)
}

func detectToolChoiceWithIntentAndMessages(userQuery string, skillsIndex map[string]bool, refs []imctx.DocRef, intent router.IntentFrame, messages []llm.MessageWithTools) string {
	if isStructuredExternalSendIntent(intent) && shouldForceExternalSendToolChoice(intent, messages) {
		return ToolChoiceRequired
	}
	return detectToolChoiceWithContext(userQuery, skillsIndex, refs)
}

func detectToolChoiceWithContext(userQuery string, skillsIndex map[string]bool, refs []imctx.DocRef) string {
	q := strings.TrimSpace(userQuery)
	if q == "" {
		if len(refs) > 0 {
			return ToolChoiceRequired
		}
		return ToolChoiceAuto
	}

	// 规则 1：纯闲聊 → none
	if isChitchat(q) {
		return ToolChoiceNone
	}

	// 规则 1.5：IM 上下文里已经存在文档引用 → 必须先走工具。
	// 典型场景：飞书里“分析一下这个文档”本身没有 URL/路径/技能名，但 resolver 已解析出 <references>。
	// 若仍走 auto，模型很容易直接口头回答，导致“识别出文档但不真正读取正文”。
	if len(refs) > 0 {
		return ToolChoiceRequired
	}

	// 规则 2：含明显工具信号 → required
	if skillRefPattern.MatchString(q) || urlPattern.MatchString(q) || filePathPattern.MatchString(q) {
		return ToolChoiceRequired
	}

	// 规则 3："X 是什么 / 怎么用" 模式
	if target := extractWhatIsTarget(q); target != "" {
		if skillsIndex == nil || !skillsIndex[strings.ToLower(target)] {
			return ToolChoiceRequired
		}
	}

	return ToolChoiceAuto
}

func shouldEvaluateToolChoiceForTurn(userQuery string, refs []imctx.DocRef, guards config.QualityGuardsConfig, intent router.IntentFrame) bool {
	if guards.ToolChoiceForce || len(refs) > 0 {
		return true
	}
	return isStructuredExternalSendIntent(intent)
}

func toolChoiceRequiredTrigger(userQuery string, skillsIndex map[string]bool, refs []imctx.DocRef, intent router.IntentFrame) string {
	return toolChoiceRequiredTriggerWithMessages(userQuery, skillsIndex, refs, intent, nil)
}

func toolChoiceRequiredTriggerWithMessages(userQuery string, skillsIndex map[string]bool, refs []imctx.DocRef, intent router.IntentFrame, messages []llm.MessageWithTools) string {
	q := strings.TrimSpace(userQuery)
	if len(refs) > 0 {
		return "refs"
	}
	if q == "" {
		return "auto"
	}
	if isStructuredExternalSendIntent(intent) && shouldForceExternalSendToolChoice(intent, messages) {
		return "external_send"
	}
	if skillRefPattern.MatchString(q) {
		return "skill_ref"
	}
	if urlPattern.MatchString(q) {
		return "url"
	}
	if filePathPattern.MatchString(q) {
		return "file_path"
	}
	if target := extractWhatIsTarget(q); target != "" {
		if skillsIndex == nil || !skillsIndex[strings.ToLower(target)] {
			return "what_is"
		}
	}
	return "auto"
}

func shouldForceExternalSendToolChoice(intent router.IntentFrame, messages []llm.MessageWithTools) bool {
	if len(messages) == 0 {
		return true
	}
	evidence := collectExternalSendEvidence(messagesFromLatestUser(messages), intent)
	if evidence.MessagingFailed || evidence.QuestionAsked || evidence.NoSendableRecipient {
		return false
	}
	return !evidence.SendAttemptValid
}

func hasRequiredIntentCallableTool(tools []mcphost.ToolDefinition, intent router.IntentFrame) bool {
	if intent.Kind == router.IntentUnknown {
		return false
	}
	profiles := make([]router.ToolProfile, 0, len(tools))
	for _, tool := range tools {
		profiles = append(profiles, router.InferToolProfile(tool, router.ProfileHint{}))
	}
	decision := router.BuildRouteDecision(intent, profiles)
	return len(decision.AllowedTools) > 0
}

func isStructuredExternalSendIntent(intent router.IntentFrame) bool {
	return intent.Kind == router.IntentExternalWrite && intent.AllowsSideEffects && intent.RequiresExternal
}

func refsForToolChoice(imCtx *imctx.IMMessageContext, imRefsRead bool) []imctx.DocRef {
	if imCtx == nil || imRefsRead {
		return nil
	}
	return imCtx.References
}

func isSuccessfulIMReferenceRead(call llm.ToolCall, isError bool) bool {
	if isError || call.Name != "feishu_api" {
		return false
	}

	var args struct {
		Action string `json:"action"`
	}
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		return false
	}

	switch args.Action {
	case "get_doc_content", "read_sheet", "list_bitable_tables", "list_bitable_records", "download_message_resource":
		return true
	default:
		return false
	}
}

// isChitchat 判断是否为纯闲聊短语。
// M6 修正：只接受整句精确命中 chitchatPhrases（去标点空白后），不再做前缀启发式。
// 前缀启发式误伤了"好的继续吗"、"收到请继续"之类中文指令，风险 > 收益。
// 宁可把边界模糊的短句交给 auto，让模型自行决定，也不因前缀匹配擅自置 none。
func isChitchat(q string) bool {
	stripped := strings.ToLower(stripPunctSpace(q))
	return chitchatPhrases[stripped]
}

// extractWhatIsTarget 从 query 里抽出"X 是什么 / 怎么用"的 X。命中返回 X（trim 后），
// 否则返回空字符串。
// M7：加 Contains 兜底 —— 锚定正则 miss 但句中含白名单后缀时，取离后缀最近的 token 作为 X。
func extractWhatIsTarget(q string) string {
	if m := whatIsPattern.FindStringSubmatch(q); len(m) == 3 {
		if t := stripFillerPrefix(strings.TrimSpace(m[1])); t != "" {
			return t
		}
	}
	if m := whatIsPatternEN.FindStringSubmatch(q); len(m) == 3 {
		if t := stripFillerPrefix(strings.TrimSpace(m[2])); t != "" {
			return t
		}
	}
	// Contains 兜底：按长度降序遍历后缀，取后缀前最后一个非填充 token。
	// M7-MED：lastTokenBefore 抓到的 token 可能带 "那/这/那个/我想知道" 前缀（例："那女娲到底怎么用" → "那女娲"），
	// 必须再走 stripFillerPrefix 才能得到裸目标 X（"女娲"）。否则 skillsIndex 查不到命中，
	// 本应回落 auto 的自家 skill 介绍问话会被强制 required，等同 H3 被绕过。
	for _, suf := range whatIsFallbackSuffixes {
		idx := strings.Index(q, suf)
		if idx <= 0 {
			continue
		}
		left := q[:idx]
		target := stripFillerPrefix(lastTokenBefore(left))
		if utf8.RuneCountInString(target) >= 2 && !isFillerWord(target) {
			return target
		}
	}
	return ""
}

// stripFillerPrefix 剥离 "我想知道 X" 里的填充前缀，返回裸 X。
func stripFillerPrefix(s string) string {
	for {
		trimmed := false
		for _, p := range whatIsFillerPrefixes {
			if strings.HasPrefix(s, p) {
				s = strings.TrimSpace(s[len(p):])
				trimmed = true
				break
			}
		}
		if !trimmed {
			return s
		}
	}
}

// isFillerWord 判断是否为填充词（"到底"、"那个" 等），防止当作 X 返回。
func isFillerWord(s string) bool {
	fillers := []string{"到底", "那", "这", "那个", "这个", "还", "就", "到", "底"}
	for _, f := range fillers {
		if s == f {
			return true
		}
	}
	return false
}

// lastTokenBefore 取字符串最右端一段非分隔符序列（先跳过尾部分隔符）。
func lastTokenBefore(left string) string {
	isSep := func(r rune) bool {
		return r == ' ' || r == '\t' || r == ',' || r == '，' || r == '。' || r == '.' || r == '：' || r == ':' || r == '"' || r == '\'' || r == '?' || r == '？' || r == '、'
	}
	runes := []rune(left)
	end := len(runes)
	for end > 0 && isSep(runes[end-1]) {
		end--
	}
	start := end
	for start > 0 && !isSep(runes[start-1]) {
		start--
	}
	return strings.TrimSpace(string(runes[start:end]))
}

func stripPunctSpace(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r == ' ' || r == '\t' || r == '\n' || r == '，' || r == ',' || r == '。' || r == '.' || r == '！' || r == '!' {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// extractLatestUserQuery 从 messages 里反向找最后一条 role=user 的消息，
// 返回其文本内容（trim 后）。找不到返回空字符串——detectToolChoice 对空字符串返回 auto，
// 等价于保持旧行为。
func extractLatestUserQuery(messages []llm.MessageWithTools) string {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "user" {
			return strings.TrimSpace(messages[i].Content.Text())
		}
	}
	return ""
}

func buildRequiredToolRetryMessage(refs []imctx.DocRef) string {
	if len(refs) == 0 {
		return "上一轮必须调用工具（tool_choice=required），但未收到任何 tool_call。请立即发起一次 websearch / skill / 文件读取 等工具调用回答用户问题，不要凭记忆回答。"
	}

	var b strings.Builder
	b.WriteString("上一轮必须调用工具（tool_choice=required），但未收到任何 tool_call。")
	b.WriteString("当前 IM 上下文里已经有飞书文档引用，请优先调用 feishu_api 读取这些引用后再回答，不要凭记忆分析。")
	b.WriteString("可用引用如下：")
	for i, ref := range refs {
		if i > 0 {
			b.WriteString("; ")
		}
		b.WriteString(string(ref.Type))
		b.WriteString(":")
		b.WriteString(ref.Token)
		switch ref.Type {
		case imctx.RefDocx, imctx.RefDoc, imctx.RefMindnote, imctx.RefFile:
			b.WriteString(" -> feishu_api action=get_doc_content(document_id=token)")
		case imctx.RefSheet:
			b.WriteString(" -> feishu_api action=read_sheet(spreadsheet_token=token, range=\"A1:Z1000\")")
		case imctx.RefBitable:
			b.WriteString(" -> feishu_api action=list_bitable_tables(app_token=token)")
		case imctx.RefWiki:
			b.WriteString(" -> feishu_api action=wiki_get_node(node_token=token)，再按返回的 obj_type/obj_token 读取")
		default:
			b.WriteString(" -> 使用 feishu_api 读取")
		}
	}
	return b.String()
}

// requiredGuardAction 描述 P0-A required+0 追责闸门在某一轮的动作。
type requiredGuardAction int

const (
	requiredGuardPass  requiredGuardAction = iota // 本轮不触发 guard（正常走后续终态/工具分支）
	requiredGuardRetry                            // 追加追责 system 消息并 continue 下一轮
	requiredGuardFail                             // 结构化失败退出
)

// shouldSuppressStreamPartial 判断本轮是否应屏蔽 ChatWithToolsStream 回调里
// "role=assistant, partial=true" 的流式广播。
// P0-A/C3 round-4：tool_choice=required 时，模型的文字流在 guard 判定前是"无法信任的"——
// 如果模型硬抗不调工具，那些流式 token 就是幻觉答案，不能先落到前端 UI。
// 屏蔽它直到终态（L649 pass 分支）一次性 broadcast，retry/fail 则完全丢弃。
// 其它 toolChoice 值（auto/none/"")保持原流式 UX。
func shouldSuppressStreamPartial(toolChoice string) bool {
	return toolChoice == ToolChoiceRequired
}

// emitAssistantMessage 根据 guard 决策判断本轮是否应 persist + broadcast assistant 消息。
// P0-A/C3：required+0 的坏回答（无工具、凭记忆胡编）不能先落历史再 retry/fail，
// 否则用户 UI 已渲染、session.Messages 已污染，等于 guard 事后诸葛亮。
// 只有 requiredGuardPass 时才 emit；retry / fail 一律丢弃本轮 assistant 输出。
func emitAssistantMessage(action requiredGuardAction) bool {
	return action == requiredGuardPass
}

// evaluateRequiredGuard 计算当前轮的 guard 动作与新的 breach 计数，不做任何副作用。
// 拆出纯函数是为了独立单测 C2-HIGH-A / C2-HIGH-B 的语义修复：
//   - C2-HIGH-A：breach 必须在 toolCalls>0 的一轮立即清零，避免长任务累计误杀；
//   - C2-HIGH-B：toolChoice=required 且 toolCalls==0 时无条件触发，不受 finish_reason 影响。
//
// 传入：
//   - toolChoice：本轮派发的 tool_choice 字符串
//   - toolCalls：本轮返回的 tool_calls 数
//   - breach：进入本轮前累计的未出工具计数（跨轮累计）
//
// 返回：
//   - action：本轮应执行的动作
//   - nextBreach：本轮处理后新的计数（传给下一轮）
func evaluateRequiredGuard(toolChoice string, toolCalls int, breach int) (requiredGuardAction, int) {
	if toolCalls > 0 {
		// C2-HIGH-A：模型产出工具调用，连锁断了，计数归零。
		return requiredGuardPass, 0
	}
	if toolChoice != ToolChoiceRequired {
		// 非 required：计数不变（保留给后续 required 轮次比较）。
		return requiredGuardPass, breach
	}
	// required + 0 工具：触发 guard。
	if breach >= 1 {
		return requiredGuardFail, breach + 1
	}
	return requiredGuardRetry, breach + 1
}

// truncateForLog 截断字符串便于日志输出，超长时追加省略号。
func truncateForLog(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	runes := []rune(s)
	return string(runes[:n]) + "..."
}
