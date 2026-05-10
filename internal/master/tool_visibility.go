package master

import (
	"context"
	"encoding/json"
	"strings"
	"unicode/utf8"

	"github.com/chef-guo/agents-hive/internal/agentquality"
	"github.com/chef-guo/agents-hive/internal/config"
	"github.com/chef-guo/agents-hive/internal/llm"
	"github.com/chef-guo/agents-hive/internal/mcphost"
	"github.com/chef-guo/agents-hive/internal/router"
	"github.com/chef-guo/agents-hive/internal/skills"
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
	RecalledToolNames        map[string]bool
	BlockedByPlanGate        bool
	SideEffectCandidateCount int
	RouteDecision            router.RouteDecision
	CandidateProfiles        []router.ToolProfile
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
	if len(catalog) == 0 {
		return nil, toolRecallObservation{Mode: config.NormalizeToolRecallConfig(recallCfg).Mode}
	}
	recallSet, obs := perTurnRecalledToolSet(session, catalog, skillMetas, latestUserQuery, recallCfg)
	out := make([]mcphost.ToolDefinition, 0, len(catalog))
	for _, tool := range catalog {
		baselineVisible := tool.Core || router.IsHostToolInSet(router.HostToolSetDefaultVisible, tool.Name)
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
	obs.VisibleAfterCount = len(out)
	if session != nil {
		session.SetAllowedToolInputs(obs.RouteDecision.AllowedToolInputs)
	}
	return out, obs
}

func perTurnRecalledToolSet(session *SessionState, catalog []mcphost.ToolDefinition, skillMetas []skills.SkillMetadata, latestUserQuery string, recallCfg config.ToolRecallConfig) (map[string]bool, toolRecallObservation) {
	recallCfg = config.NormalizeToolRecallConfig(recallCfg)
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
	if len(recalls) == 0 {
		profiles := recalledToolProfiles(nil, skillMetas)
		obs.CandidateProfiles = profiles
		obs.RouteDecision = router.BuildRouteDecisionWithBlocks(inferRouteIntent(latestUserQuery), profiles, "exec", sessionReflectionBlocks(session))
		return nil, obs
	}
	recalls = pruneGenericIMWhenFeishuDomainEntryRecalled(recalls)
	intent := inferRouteIntent(latestUserQuery)
	profiles := recalledToolProfiles(recalls, skillMetas)
	obs.CandidateProfiles = profiles
	decision := router.BuildRouteDecisionWithBlocks(intent, profiles, "exec", sessionReflectionBlocks(session))
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
	return router.RuleClassifyIntent(query)
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
		profiles = append(profiles, router.InferToolProfile(recall.Tool, router.ProfileHint{}))
	}
	for _, meta := range skillMetas {
		name := strings.TrimSpace(meta.Name)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		profiles = append(profiles, router.InferSkillWorkflowProfile(name, meta.Description))
	}
	return profiles
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

func pruneGenericIMWhenFeishuDomainEntryRecalled(recalls []tools.ToolRecallHit) []tools.ToolRecallHit {
	hasFeishuAPI := false
	for _, recall := range recalls {
		if recall.Tool.Name == "feishu_api" {
			hasFeishuAPI = true
			break
		}
	}
	if !hasFeishuAPI {
		return recalls
	}
	out := recalls[:0]
	for _, recall := range recalls {
		if recall.Tool.Name == "send_im_message" {
			continue
		}
		out = append(out, recall)
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
