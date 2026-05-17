package master

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/chef-guo/agents-hive/internal/agentquality"
	"github.com/chef-guo/agents-hive/internal/collections"
	"github.com/chef-guo/agents-hive/internal/config"
	"github.com/chef-guo/agents-hive/internal/llm"
	"github.com/chef-guo/agents-hive/internal/mcphost"
	"github.com/chef-guo/agents-hive/internal/router"
	"github.com/chef-guo/agents-hive/internal/skills"
	"github.com/chef-guo/agents-hive/internal/toolruntime"
	"github.com/chef-guo/agents-hive/internal/tools"
)

type toolRecallObservation struct {
	Mode                     string
	QueryPreview             string
	CandidateCount           int
	CandidateNames           []string
	CandidateScores          map[string]float64
	CandidateToolNames       map[string]bool
	VisibleBeforeCount       int
	VisibleAfterCount        int
	VisibleTrimmedCount      int
	MaxVisibleTools          int
	RecalledToolNames        map[string]bool
	BlockedByPlanGate        bool
	SideEffectCandidateCount int
	RouteDecision            router.RouteDecision
	CandidateProfiles        []router.ToolProfile
	Entries                  map[string]admissionEntry
}

type admissionEntry struct {
	Name                string
	SurvivedPolicy      bool
	VisibleToModel      bool
	ExecutableByRuntime bool
	TaskCallable        bool
	DiscoveryOnly       bool
	MayRequireApproval  bool
	AllowedInputs       map[string]string
	DangerousActions    []string
	PrimaryBlockReason  string
}

const routeEmptyInputValue = "__empty__"

type toolVisibilityOptions struct {
	FastPath             bool
	MaxModelVisibleTools int
	FilesystemEnabled    *bool
}

// modelVisibleToolsForSession 收窄模型默认候选集：核心工具和质量杠杆工具默认可见，
// 其他扩展/MCP/自定义工具需要先通过 tool_search 发现。
func modelVisibleToolsForSession(session *SessionState, catalog []mcphost.ToolDefinition) []mcphost.ToolDefinition {
	return modelVisibleToolsForSessionWithRecall(session, catalog, "", config.DefaultToolRecallConfig())
}

// modelVisibleToolsForSessionWithRecall 在默认可见集基础上，把当前用户消息召回到的少量隐藏工具
// 临时加入本轮模型候选。召回结果不写入 session discovered state，显式 tool_search 成功后才持久可见。
func modelVisibleToolsForSessionWithRecall(session *SessionState, catalog []mcphost.ToolDefinition, latestUserQuery string, recallCfg config.ToolRecallConfig) []mcphost.ToolDefinition {
	visible, _ := modelVisibleToolsForSessionWithRecallObservation(session, catalog, latestUserQuery, recallCfg)
	return visible
}

func modelVisibleToolsForPreparedMessages(session *SessionState, catalog []mcphost.ToolDefinition, messages []llm.MessageWithTools) []mcphost.ToolDefinition {
	return modelVisibleToolsForPreparedMessagesWithRecallConfig(session, catalog, messages, config.DefaultToolRecallConfig())
}

func modelVisibleToolsForPreparedMessagesWithRecallConfig(session *SessionState, catalog []mcphost.ToolDefinition, messages []llm.MessageWithTools, recallCfg config.ToolRecallConfig) []mcphost.ToolDefinition {
	visible, _ := modelVisibleToolsForPreparedMessagesWithRecallObservation(session, catalog, messages, recallCfg)
	return visible
}

func modelVisibleToolsForPreparedMessagesWithRecallObservation(session *SessionState, catalog []mcphost.ToolDefinition, messages []llm.MessageWithTools, recallCfg config.ToolRecallConfig) ([]mcphost.ToolDefinition, toolRecallObservation) {
	return modelVisibleToolsForSessionWithRecallObservation(session, catalog, extractLatestUserQuery(messages), recallCfg)
}

func modelVisibleToolsForSessionWithRecallObservation(session *SessionState, catalog []mcphost.ToolDefinition, latestUserQuery string, recallCfg config.ToolRecallConfig) ([]mcphost.ToolDefinition, toolRecallObservation) {
	return modelVisibleToolsForSessionWithRecallObservationAndSkills(session, catalog, nil, latestUserQuery, recallCfg)
}

func modelVisibleToolsForSessionWithRecallObservationAndSkills(session *SessionState, catalog []mcphost.ToolDefinition, skillMetas []skills.SkillMetadata, latestUserQuery string, recallCfg config.ToolRecallConfig) ([]mcphost.ToolDefinition, toolRecallObservation) {
	return modelVisibleToolsForSessionWithRecallObservationAndSkillsAndIntent(session, catalog, skillMetas, latestUserQuery, recallCfg, inferRouteIntent(latestUserQuery))
}

func modelVisibleToolsForSessionWithRecallObservationAndSkillsAndIntent(session *SessionState, catalog []mcphost.ToolDefinition, skillMetas []skills.SkillMetadata, latestUserQuery string, recallCfg config.ToolRecallConfig, intent router.IntentFrame) ([]mcphost.ToolDefinition, toolRecallObservation) {
	return modelVisibleToolsForSessionWithRecallObservationAndSkillsAndIntentWithOptions(session, catalog, skillMetas, latestUserQuery, recallCfg, intent, toolVisibilityOptions{})
}

func modelVisibleToolsForSessionWithRecallObservationAndSkillsAndIntentWithOptions(session *SessionState, catalog []mcphost.ToolDefinition, skillMetas []skills.SkillMetadata, latestUserQuery string, recallCfg config.ToolRecallConfig, intent router.IntentFrame, opts toolVisibilityOptions) ([]mcphost.ToolDefinition, toolRecallObservation) {
	if len(catalog) == 0 {
		return nil, toolRecallObservation{Mode: config.NormalizeToolRecallConfig(recallCfg).Mode}
	}
	catalog = filterCatalogByVisibilityOptions(stableToolDefinitions(catalog), opts)
	userID := ""
	if session != nil {
		userID = session.UserID
	}
	recallSet, obs := perTurnRecalledToolSetWithIntent(session, catalog, skillMetas, latestUserQuery, recallCfg, intent, userID)
	out := make([]mcphost.ToolDefinition, 0, len(catalog))
	for _, tool := range catalog {
		baselineVisible := isDefaultVisibleToolWithOptions(tool, opts)
		if decision := EvaluatePlanToolGate(context.Background(), session, tool.Name); !decision.Allowed {
			if obs.CandidateToolNames[tool.Name] {
				obs.BlockedByPlanGate = true
			}
			continue
		}
		if baselineVisible {
			obs.VisibleBeforeCount++
		}
		if baselineVisible || recallSet[tool.Name] {
			out = append(out, tool)
		}
	}
	out, obs.VisibleTrimmedCount = trimModelVisibleTools(out, opts, recallSet)
	obs.VisibleAfterCount = len(out)
	if opts.FastPath && opts.MaxModelVisibleTools > 0 {
		obs.MaxVisibleTools = opts.MaxModelVisibleTools
	}
	routeDecision := buildVisibleRouteDecision(session, out, skillMetas, intent, userID)
	obs.RouteDecision = routeDecision
	runtimeAllowedTools := allowedToolsForRuntime(session, routeDecision, out)
	runtimeAllowedInputs := mergeAllowedToolInputsForRuntime(routeDecision.AllowedToolInputs, out, intent, stringSet(runtimeAllowedTools))
	obs.Entries = buildAdmissionEntries(session, catalog, out, routeDecision, obs.CandidateProfiles, intent, runtimeAllowedInputs)
	if session != nil {
		session.SetRouteDecision(routeDecision)
		session.SetAllowedTools(runtimeAllowedTools)
		session.SetAllowedToolInputs(runtimeAllowedInputs)
	}
	return out, obs
}

func isToolEnabledByVisibilityOptions(name string, opts toolVisibilityOptions) bool {
	name = strings.TrimSpace(name)
	if name == "filesystem" {
		return opts.FilesystemEnabled == nil || *opts.FilesystemEnabled
	}
	return true
}

func filterCatalogByVisibilityOptions(catalog []mcphost.ToolDefinition, opts toolVisibilityOptions) []mcphost.ToolDefinition {
	if opts.FilesystemEnabled == nil || *opts.FilesystemEnabled {
		return catalog
	}
	out := make([]mcphost.ToolDefinition, 0, len(catalog))
	for _, tool := range catalog {
		if isToolEnabledByVisibilityOptions(tool.Name, opts) {
			out = append(out, tool)
		}
	}
	return out
}

func stableToolDefinitions(tools []mcphost.ToolDefinition) []mcphost.ToolDefinition {
	if len(tools) <= 1 {
		return tools
	}
	out := append([]mcphost.ToolDefinition(nil), tools...)
	sort.SliceStable(out, func(i, j int) bool {
		left := strings.TrimSpace(out[i].Name)
		right := strings.TrimSpace(out[j].Name)
		if left == right {
			return out[i].Description < out[j].Description
		}
		return left < right
	})
	return out
}

func isDefaultVisibleTool(tool mcphost.ToolDefinition) bool {
	return isDefaultVisibleToolWithOptions(tool, toolVisibilityOptions{})
}

func isDefaultVisibleToolWithOptions(tool mcphost.ToolDefinition, opts toolVisibilityOptions) bool {
	name := strings.TrimSpace(tool.Name)
	if name == "" || isExecutionEntrypointTool(name) {
		return false
	}
	if opts.FastPath && opts.MaxModelVisibleTools > 0 {
		return isFastPathDefaultVisibleTool(name)
	}
	return tool.Core || router.IsHostToolInSet(router.HostToolSetDefaultVisible, name)
}

func isFastPathDefaultVisibleTool(name string) bool {
	switch strings.TrimSpace(name) {
	case "filesystem", "ls", "memory", "question", "skill", "tool_search", "read_file", "grep", "glob", "kb.doc.meta", "kb.doc.structure", "kb.section.text":
		return true
	default:
		return false
	}
}

func trimModelVisibleTools(tools []mcphost.ToolDefinition, opts toolVisibilityOptions, recallSet map[string]bool) ([]mcphost.ToolDefinition, int) {
	if !opts.FastPath || opts.MaxModelVisibleTools <= 0 || len(tools) <= opts.MaxModelVisibleTools {
		return tools, 0
	}
	selected := map[string]bool{}
	addSelected := func(name string) {
		if len(selected) >= opts.MaxModelVisibleTools {
			return
		}
		name = strings.TrimSpace(name)
		if name != "" {
			selected[name] = true
		}
	}
	for _, priority := range []string{"tool_search"} {
		for _, tool := range tools {
			name := strings.TrimSpace(tool.Name)
			if name == priority {
				addSelected(name)
				break
			}
		}
		if len(selected) >= opts.MaxModelVisibleTools {
			break
		}
	}
	for _, tool := range tools {
		if len(selected) >= opts.MaxModelVisibleTools {
			break
		}
		name := strings.TrimSpace(tool.Name)
		if name == "" || !recallSet[name] || selected[name] {
			continue
		}
		addSelected(name)
	}
	for _, priority := range []string{"question", "kb.doc.meta", "kb.doc.structure", "kb.section.text", "memory", "skill", "filesystem", "read_file", "grep", "glob", "ls"} {
		for _, tool := range tools {
			if len(selected) >= opts.MaxModelVisibleTools {
				break
			}
			name := strings.TrimSpace(tool.Name)
			if name == priority && !selected[name] {
				addSelected(name)
				break
			}
		}
	}
	kept := make([]mcphost.ToolDefinition, 0, len(selected))
	for _, tool := range tools {
		if selected[strings.TrimSpace(tool.Name)] {
			kept = append(kept, tool)
		}
	}
	return kept, len(tools) - len(kept)
}

func isExecutionEntrypointTool(name string) bool {
	switch strings.TrimSpace(name) {
	case "batch", "task", "spawn_agent", "parallel_dispatch":
		return true
	default:
		return false
	}
}

func perTurnRecalledToolSet(session *SessionState, catalog []mcphost.ToolDefinition, skillMetas []skills.SkillMetadata, latestUserQuery string, recallCfg config.ToolRecallConfig) (map[string]bool, toolRecallObservation) {
	return perTurnRecalledToolSetWithIntent(session, catalog, skillMetas, latestUserQuery, recallCfg, inferRouteIntent(latestUserQuery))
}

func perTurnRecalledToolSetWithIntent(session *SessionState, catalog []mcphost.ToolDefinition, skillMetas []skills.SkillMetadata, latestUserQuery string, recallCfg config.ToolRecallConfig, intent router.IntentFrame, userID ...string) (map[string]bool, toolRecallObservation) {
	recallCfg = config.NormalizeToolRecallConfig(recallCfg)
	routeUserID := ""
	if len(userID) > 0 {
		routeUserID = userID[0]
	}
	obs := toolRecallObservation{
		Mode:               recallCfg.Mode,
		QueryPreview:       truncateRunes(strings.TrimSpace(latestUserQuery), 80),
		CandidateToolNames: map[string]bool{},
		RecalledToolNames:  map[string]bool{},
	}
	if strings.TrimSpace(latestUserQuery) == "" || len(catalog) == 0 {
		return nil, obs
	}
	if recallCfg.Mode == "off" || recallCfg.Limit <= 0 {
		return nil, obs
	}
	recalls := tools.RecallToolCatalog(catalog, latestUserQuery, recallCfg.Limit)
	recalls = appendDiscoveredToolRecalls(recalls, catalog, session)
	recalls = ensureExternalSendCandidates(recalls, catalog, intent, recallCfg.SideEffectMinScore)
	recalls = applyPlatformAwareExternalSendVisibility(recalls, catalog, intent, recallCfg.SideEffectMinScore)
	if len(recalls) == 0 {
		profiles := recalledToolProfiles(nil, skillMetas)
		obs.CandidateProfiles = profiles
		obs.RouteDecision = router.BuildRouteDecisionWithOptions(normalizeRouteIntent(intent), profiles, router.RouteDecisionOptions{
			ReflectionMode:   "exec",
			ReflectionBlocks: sessionReflectionBlocks(session),
			UserID:           routeUserID,
		})
		return nil, obs
	}
	intent = normalizeRouteIntent(intent)
	profiles := recalledToolProfiles(recalls, skillMetas)
	obs.CandidateProfiles = profiles
	decision := router.BuildRouteDecisionWithOptions(intent, profiles, router.RouteDecisionOptions{
		ReflectionMode:   "exec",
		ReflectionBlocks: sessionReflectionBlocks(session),
		UserID:           routeUserID,
	})
	obs.RouteDecision = decision
	allowed := stringSet(decision.AllowedTools)
	profilesByName := toolProfileByName(profiles)
	out := make(map[string]bool, len(recalls))
	for _, recall := range recalls {
		name := strings.TrimSpace(recall.Tool.Name)
		if name == "" {
			continue
		}
		sideEffect := router.ProfileHasSideEffect(profilesByName[name])
		if sideEffect && recall.Score < recallCfg.SideEffectMinScore {
			continue
		}
		if !sideEffect && recall.Score < recallCfg.MinScore {
			continue
		}
		obs.CandidateCount++
		obs.CandidateToolNames[name] = true
		if sideEffect {
			obs.SideEffectCandidateCount++
		}
		if recallCfg.LogCandidates {
			obs.CandidateNames = append(obs.CandidateNames, name)
			if obs.CandidateScores == nil {
				obs.CandidateScores = make(map[string]float64)
			}
			obs.CandidateScores[name] = recall.Score
		}
		if recallCfg.Mode == "inject" && allowed[name] {
			out[name] = true
			obs.RecalledToolNames[name] = true
		}
	}
	if len(out) == 0 {
		out = nil
	}
	return out, obs
}

func ensureExternalSendCandidates(recalls []tools.ToolRecallHit, catalog []mcphost.ToolDefinition, intent router.IntentFrame, score float64) []tools.ToolRecallHit {
	if !isExplicitExternalSendIntent(intent) {
		return recalls
	}
	seen := make(map[string]bool, len(recalls))
	for _, recall := range recalls {
		name := strings.TrimSpace(recall.Tool.Name)
		if name == "" {
			continue
		}
		seen[name] = true
	}
	if score <= 0 {
		score = 1
	}
	for _, tool := range catalog {
		name := strings.TrimSpace(tool.Name)
		if name == "" || seen[name] {
			continue
		}
		if !profileSupportsExternalSend(toolruntime.DescriptorFromDefinition(tool).Profile) {
			continue
		}
		recalls = append(recalls, tools.ToolRecallHit{Tool: tool, Score: score})
		seen[name] = true
	}
	return recalls
}

func applyPlatformAwareExternalSendVisibility(recalls []tools.ToolRecallHit, catalog []mcphost.ToolDefinition, intent router.IntentFrame, score float64) []tools.ToolRecallHit {
	if !isExplicitExternalSendIntent(intent) {
		return recalls
	}
	if hasToolVisibilitySignal(intent.Signals, "external_send_multi_platform_requires_question") {
		return pruneExternalSendToolsForQuestion(recalls)
	}
	hints := normalizedExternalSendPlatformHints(intent.AllowedDomainsHint)
	if len(hints) == 0 {
		return recalls
	}
	recalls = ensureUnifiedIMCandidates(recalls, catalog, score)
	if len(hints) == 1 && hints[0] != "feishu" {
		return pruneToolRecallByName(recalls, "feishu_api")
	}
	return recalls
}

func ensureUnifiedIMCandidates(recalls []tools.ToolRecallHit, catalog []mcphost.ToolDefinition, score float64) []tools.ToolRecallHit {
	if score <= 0 {
		score = 1
	}
	seen := make(map[string]bool, len(recalls))
	for i, recall := range recalls {
		name := strings.TrimSpace(recall.Tool.Name)
		if name != "" {
			seen[name] = true
			if name == "im_api" || name == "send_im_message" {
				recalls[i].Score = score
			}
		}
	}
	for _, tool := range catalog {
		name := strings.TrimSpace(tool.Name)
		if name != "im_api" && name != "send_im_message" {
			continue
		}
		if seen[name] {
			continue
		}
		recalls = append(recalls, tools.ToolRecallHit{Tool: tool, Score: score})
		seen[name] = true
	}
	return recalls
}

func pruneExternalSendToolsForQuestion(recalls []tools.ToolRecallHit) []tools.ToolRecallHit {
	out := recalls[:0]
	for _, recall := range recalls {
		profile := toolruntime.DescriptorFromDefinition(recall.Tool).Profile
		if profileSupportsExternalSend(profile) {
			continue
		}
		out = append(out, recall)
	}
	return out
}

func pruneToolRecallByName(recalls []tools.ToolRecallHit, name string) []tools.ToolRecallHit {
	out := recalls[:0]
	for _, recall := range recalls {
		if strings.TrimSpace(recall.Tool.Name) == name {
			continue
		}
		out = append(out, recall)
	}
	return out
}

func normalizedExternalSendPlatformHints(hints []string) []string {
	out := make([]string, 0, len(hints))
	seen := make(map[string]bool, len(hints))
	for _, hint := range hints {
		platform := strings.ToLower(strings.TrimSpace(hint))
		switch platform {
		case "feishu", "wechatbot", "wecom", "dingtalk":
		default:
			continue
		}
		if seen[platform] {
			continue
		}
		seen[platform] = true
		out = append(out, platform)
	}
	return out
}

func hasToolVisibilitySignal(signals []string, want string) bool {
	for _, signal := range signals {
		if strings.TrimSpace(signal) == want {
			return true
		}
	}
	return false
}

func isExplicitExternalSendIntent(intent router.IntentFrame) bool {
	return intent.Kind == router.IntentExternalWrite && intent.AllowsSideEffects && intent.RequiresExternal
}

func profileSupportsExternalSend(profile router.ToolProfile) bool {
	for _, capability := range profile.Capabilities {
		if capability == router.CapabilityExternalSend {
			return true
		}
	}
	return false
}

func appendDiscoveredToolRecalls(recalls []tools.ToolRecallHit, catalog []mcphost.ToolDefinition, session *SessionState) []tools.ToolRecallHit {
	if session == nil {
		return recalls
	}
	discovered := session.DiscoveredTools()
	if len(discovered) == 0 {
		return recalls
	}
	seen := make(map[string]bool, len(recalls))
	for _, recall := range recalls {
		name := strings.TrimSpace(recall.Tool.Name)
		if name != "" {
			seen[name] = true
		}
	}
	discoveredSet := stringSet(discovered)
	for _, tool := range catalog {
		name := strings.TrimSpace(tool.Name)
		if name == "" || !discoveredSet[name] || seen[name] {
			continue
		}
		recalls = append(recalls, tools.ToolRecallHit{Tool: tool, Score: 1})
		seen[name] = true
	}
	return recalls
}

func buildAdmissionEntries(session *SessionState, catalog []mcphost.ToolDefinition, visible []mcphost.ToolDefinition, decision router.RouteDecision, candidateProfiles []router.ToolProfile, intent router.IntentFrame, runtimeAllowedInputs map[string]map[string]string) map[string]admissionEntry {
	entries := make(map[string]admissionEntry, len(catalog))
	visibleSet := make(map[string]bool, len(visible))
	for _, tool := range visible {
		name := strings.TrimSpace(tool.Name)
		if name != "" {
			visibleSet[name] = true
		}
	}
	callableSet := stringSet(decision.AllowedTools)
	blockReasons := routeBlockReasons(decision.BlockedTools)
	for name, reason := range routeBlockReasons(router.BuildRouteDecisionWithBlocks(normalizeRouteIntent(intent), candidateProfiles, "exec", sessionReflectionBlocks(session)).BlockedTools) {
		if blockReasons[name] == "" {
			blockReasons[name] = reason
		}
	}

	for _, tool := range catalog {
		name := strings.TrimSpace(tool.Name)
		if name == "" {
			continue
		}
		planDecision := EvaluatePlanToolGate(context.Background(), session, name)
		admission := toolruntime.Admit(toolruntime.DescriptorFromDefinition(tool), router.ToolPolicyContext{ForRoute: true})
		profile := admission.Descriptor.Profile
		policy := admission.Policy
		discoveryOnly := name == "tool_search" || policy.RouteStatus == router.ToolRouteDiscoveryOnly || profile.Invocation == router.InvocationDiscoveryOnly || blockReasons[name] == "discovery only" || blockReasons[name] == "discovery_only"
		primaryReason := ""
		switch {
		case !planDecision.Allowed:
			primaryReason = planDecision.Reason
		case discoveryOnly:
			primaryReason = "discovery_only"
		case blockReasons[name] != "":
			primaryReason = blockReasons[name]
		}
		allowedInputs := defaultRuntimeAllowedInputsForTool(name, runtimeAllowedInputs[name], callableSet[name])
		entries[name] = admissionEntry{
			Name:                name,
			SurvivedPolicy:      true,
			VisibleToModel:      visibleSet[name],
			ExecutableByRuntime: planDecision.Allowed,
			TaskCallable:        callableSet[name],
			DiscoveryOnly:       discoveryOnly,
			MayRequireApproval:  policy.MayRequireApproval,
			AllowedInputs:       allowedInputs,
			DangerousActions:    router.StructuredDangerousActions(name),
			PrimaryBlockReason:  primaryReason,
		}
	}
	return entries
}

func mergeAllowedToolInputsWithMixedReadDefaults(inputs map[string]map[string]string, visible []mcphost.ToolDefinition) map[string]map[string]string {
	return mergeAllowedToolInputsWithMixedDefaultsForAllowed(inputs, visible, router.IntentFrame{Kind: router.IntentRead}, nil)
}

func mergeAllowedToolInputsWithMixedDefaults(inputs map[string]map[string]string, visible []mcphost.ToolDefinition, intent router.IntentFrame) map[string]map[string]string {
	return mergeAllowedToolInputsWithMixedDefaultsForAllowed(inputs, visible, intent, nil)
}

func mergeAllowedToolInputsWithMixedDefaultsForAllowed(inputs map[string]map[string]string, visible []mcphost.ToolDefinition, intent router.IntentFrame, allowed map[string]bool) map[string]map[string]string {
	out := collections.CloneNonEmptyNestedStringMap(inputs)
	for _, tool := range visible {
		name := strings.TrimSpace(tool.Name)
		if name == "" {
			continue
		}
		if len(allowed) > 0 && !allowed[name] {
			continue
		}
		if len(out[name]) > 0 {
			continue
		}
		defaults := router.MixedAllowedToolInputsForIntent(intent, name)
		if len(defaults) == 0 {
			defaults = defaultRuntimeAllowedInputsForTool(name, nil, true)
		}
		if len(defaults) == 0 {
			continue
		}
		if out == nil {
			out = make(map[string]map[string]string)
		}
		out[name] = defaults
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func mergeAllowedToolInputsForRuntime(inputs map[string]map[string]string, visible []mcphost.ToolDefinition, intent router.IntentFrame, allowed map[string]bool) map[string]map[string]string {
	if len(allowed) == 0 {
		return nil
	}
	filtered := make(map[string]map[string]string, len(inputs))
	for name, constraints := range inputs {
		name = strings.TrimSpace(name)
		if name == "" || !allowed[name] || len(constraints) == 0 {
			continue
		}
		filtered[name] = constraints
	}
	return mergeAllowedToolInputsWithMixedDefaultsForAllowed(filtered, visible, intent, allowed)
}

func allowedToolsForRuntime(session *SessionState, decision router.RouteDecision, visible []mcphost.ToolDefinition) []string {
	allowed := stringSet(decision.AllowedTools)
	for _, tool := range visible {
		name := strings.TrimSpace(tool.Name)
		if name == "" {
			continue
		}
		if isRuntimeDiscoveryEntrypoint(name) || isRuntimeListEntrypoint(name) {
			allowed[name] = true
			continue
		}
		if session != nil && session.PlanMode && (router.IsHostToolInSet(router.HostToolSetPlanControl, name) || router.IsHostToolInSet(router.HostToolSetPlanAllowed, name)) {
			allowed[name] = true
		}
	}
	if len(allowed) == 0 {
		return nil
	}
	out := make([]string, 0, len(allowed))
	for name := range allowed {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func isRuntimeDiscoveryEntrypoint(name string) bool {
	return strings.TrimSpace(name) == "tool_search"
}

func isRuntimeListEntrypoint(name string) bool {
	return strings.TrimSpace(name) == "skill"
}

func buildVisibleRouteDecision(session *SessionState, visible []mcphost.ToolDefinition, skillMetas []skills.SkillMetadata, intent router.IntentFrame, userID string) router.RouteDecision {
	return router.BuildRouteDecisionWithOptions(normalizeRouteIntent(intent), visibleRouteProfiles(visible, skillMetas), router.RouteDecisionOptions{
		ReflectionMode:   "exec",
		ReflectionBlocks: sessionReflectionBlocks(session),
		UserID:           userID,
	})
}

func visibleRouteProfiles(visible []mcphost.ToolDefinition, skillMetas []skills.SkillMetadata) []router.ToolProfile {
	profiles := make([]router.ToolProfile, 0, len(visible)+len(skillMetas))
	seen := make(map[string]bool, len(visible)+len(skillMetas))
	for _, tool := range visible {
		name := strings.TrimSpace(tool.Name)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		descriptor := toolruntime.DescriptorFromDefinition(tool)
		profile := descriptor.Profile
		if hint := profileHintForVisibilityTool(name); hint.Kind != "" {
			profile = router.InferToolProfile(tool, hint)
		}
		profiles = append(profiles, profile)
	}
	for _, meta := range skillMetas {
		name := strings.TrimSpace(meta.Name)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		profiles = append(profiles, router.InferSkillWorkflowProfileFromMetadata(router.SkillWorkflowMetadata{
			Name:        name,
			Description: meta.Description,
			Scope:       string(meta.Scope),
			UserID:      meta.UserID,
		}))
	}
	return profiles
}

func defaultRuntimeAllowedInputsForTool(name string, routed map[string]string, callable bool) map[string]string {
	if len(routed) > 0 {
		return collections.CloneNonEmptyStringMap(routed)
	}
	if callable && router.IsMixedReadWriteTool(name) {
		return router.MixedReadOnlyToolInputs(name)
	}
	if name == "skill" {
		return map[string]string{"name": routeEmptyInputValue}
	}
	return nil
}

func routeBlockReasons(blocked []router.BlockedTool) map[string]string {
	out := make(map[string]string, len(blocked))
	for _, block := range blocked {
		name := strings.TrimSpace(block.Name)
		if name != "" {
			out[name] = strings.TrimSpace(block.Reason)
		}
	}
	return out
}

func toolProfileByName(profiles []router.ToolProfile) map[string]router.ToolProfile {
	out := make(map[string]router.ToolProfile, len(profiles))
	for _, profile := range profiles {
		if profile.Name != "" {
			out[profile.Name] = profile
		}
	}
	return out
}

func sessionReflectionBlocks(session *SessionState) []router.ReflectionBlock {
	if session == nil {
		return nil
	}
	return session.ListReflectionBlocks()
}

func (o toolRecallObservation) toEvent(traceID, turnID, selectedTool string, used bool) agentquality.ToolRecall {
	return agentquality.ToolRecall{
		Mode:                     o.Mode,
		TurnID:                   turnID,
		TraceID:                  traceID,
		QueryPreview:             o.QueryPreview,
		CandidateCount:           o.CandidateCount,
		CandidateNames:           append([]string(nil), o.CandidateNames...),
		CandidateScores:          cloneToolRecallScores(o.CandidateScores),
		VisibleBeforeCount:       o.VisibleBeforeCount,
		VisibleAfterCount:        o.VisibleAfterCount,
		VisibleTrimmedCount:      o.VisibleTrimmedCount,
		MaxVisibleTools:          o.MaxVisibleTools,
		SelectedTool:             selectedTool,
		ModelUsedRecalledTool:    used,
		BlockedByPlanGate:        o.BlockedByPlanGate,
		SideEffectCandidateCount: o.SideEffectCandidateCount,
	}
}

func (o toolRecallObservation) toRouteDecisionEvent() agentquality.RouteDecisionEvent {
	return agentquality.RouteDecisionEventFromRouter(o.RouteDecision)
}

func (o toolRecallObservation) toDecisionSpan(traceID, sessionIDHash string) router.DecisionSpan {
	return router.NewDecisionSpan(o.RouteDecision, o.CandidateProfiles, router.DecisionSpanOptions{
		TraceID:        traceID,
		SessionIDHash:  sessionIDHash,
		IntentSource:   "rule",
		IntentDegraded: false,
	})
}

func cloneToolRecallScores(in map[string]float64) map[string]float64 {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]float64, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func truncateRunes(s string, max int) string {
	if max <= 0 || utf8.RuneCountInString(s) <= max {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	count := 0
	for _, r := range s {
		if count >= max {
			break
		}
		b.WriteRune(r)
		count++
	}
	return b.String()
}

func inferRouteIntent(query string) router.IntentFrame {
	intent := router.RuleClassifyIntent(query)
	if isSideEffectRouteIntent(intent) {
		intent.Kind = router.IntentAnswer
		intent.RequiresExternal = false
		intent.AllowsSideEffects = false
		intent.Signals = appendSignalForToolVisibility(intent.Signals, "side_effect_intent_suppressed")
	}
	return normalizeRouteIntent(intent)
}

func normalizeRouteIntent(intent router.IntentFrame) router.IntentFrame {
	if intent.Kind == "" {
		intent.Kind = router.IntentAnswer
	}
	return intent
}

func isSideEffectRouteIntent(intent router.IntentFrame) bool {
	if intent.AllowsSideEffects {
		return true
	}
	switch intent.Kind {
	case router.IntentWriteLocal, router.IntentExternalWrite, router.IntentCreateSkill, router.IntentModifySkill, router.IntentManageTool:
		return true
	default:
		return false
	}
}

func appendSignalForToolVisibility(signals []string, signal string) []string {
	for _, existing := range signals {
		if existing == signal {
			return signals
		}
	}
	return append(signals, signal)
}

func recalledToolProfiles(recalls []tools.ToolRecallHit, skillMetas []skills.SkillMetadata) []router.ToolProfile {
	profiles := make([]router.ToolProfile, 0, len(recalls)+len(skillMetas))
	seen := make(map[string]bool, len(recalls)+len(skillMetas))
	for _, recall := range recalls {
		name := strings.TrimSpace(recall.Tool.Name)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		descriptor := toolruntime.DescriptorFromDefinition(recall.Tool)
		profile := descriptor.Profile
		if hint := profileHintForVisibilityTool(name); hint.Kind != "" {
			profile = router.InferToolProfile(recall.Tool, hint)
		}
		profiles = append(profiles, profile)
	}
	for _, meta := range skillMetas {
		name := strings.TrimSpace(meta.Name)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		profiles = append(profiles, router.InferSkillWorkflowProfileFromMetadata(router.SkillWorkflowMetadata{
			Name:        name,
			Description: meta.Description,
			Scope:       string(meta.Scope),
			UserID:      meta.UserID,
		}))
	}
	return profiles
}

func profileHintForVisibilityTool(name string) router.ProfileHint {
	if name != "im_api" {
		return router.ProfileHint{}
	}
	return router.ProfileHint{
		Kind:       router.CapabilityKindBuiltinTool,
		Domain:     "messaging",
		Source:     router.CapabilitySourceBuiltin,
		Invocation: router.InvocationDirectTool,
		Risk:       router.RiskExternalWrite,
		Trust:      router.TrustBuiltIn,
		SideEffect: true,
		Capabilities: []router.Capability{
			router.CapabilityExternalSend,
			router.CapabilityExternalSendFeishu,
			router.CapabilityExternalSendWechatBot,
			router.CapabilityExternalSendWeCom,
			router.CapabilityExternalSendDingTalk,
		},
	}
}

func stringSet(items []string) map[string]bool {
	out := make(map[string]bool, len(items))
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item != "" {
			out[item] = true
		}
	}
	return out
}

func recordToolDiscoveryFromResult(session *SessionState, toolCall llm.ToolCall, content string, isError bool) {
	if session == nil || isError || toolCall.Name != "tool_search" {
		return
	}
	session.RecordDiscoveredTools(discoveredToolNamesFromToolSearchResult(content))
}

func discoveredToolNamesFromToolSearchResult(content string) []string {
	content = strings.TrimSpace(content)
	if content == "" {
		return nil
	}
	var payload struct {
		Results []struct {
			Name string `json:"name"`
		} `json:"results"`
	}
	if err := json.Unmarshal([]byte(content), &payload); err != nil {
		return nil
	}
	names := make([]string, 0, len(payload.Results))
	seen := make(map[string]bool, len(payload.Results))
	for _, result := range payload.Results {
		name := strings.TrimSpace(result.Name)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		names = append(names, name)
	}
	return names
}
