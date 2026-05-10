package eval

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/chef-guo/agents-hive/internal/memory"
)

// BuildRecords 把 fixture 转成真实 memory.MemoryRecord，供 Injector 直接运行。
func BuildRecords(c Case) ([]memory.MemoryRecord, error) {
	records := make([]memory.MemoryRecord, 0, len(c.Memories))
	for _, fixture := range c.Memories {
		mt := memory.MemoryType(fixture.Type)
		if !memory.ValidMemoryTypes[mt] {
			return nil, fmt.Errorf("%s: memory %d type invalid: %s", c.ID, fixture.ID, fixture.Type)
		}

		g := memory.Governance{
			Confidence: fixture.Confidence,
			Source:     fixture.Source,
		}
		if fixture.ExpiresAt != "" {
			ts, err := time.Parse(time.RFC3339, fixture.ExpiresAt)
			if err != nil {
				return nil, fmt.Errorf("%s: memory %d expires_at invalid: %w", c.ID, fixture.ID, err)
			}
			g.ExpiresAt = ts
		}

		metadata := memory.EncodeGovernance(evalMetadata(fixture), g)
		records = append(records, memory.MemoryRecord{
			ID:        fixture.ID,
			UserID:    fixture.UserID,
			Type:      mt,
			Content:   fixture.Content,
			Tags:      append([]string(nil), fixture.Tags...),
			SessionID: fixture.SessionID,
			Metadata:  metadata,
		})
		if fixture.Score != nil {
			records[len(records)-1].Score = *fixture.Score
		}
	}
	return records, nil
}

func evalMetadata(fixture MemoryFixture) json.RawMessage {
	meta := map[string]any{}
	if usesStructuredTarget(fixture) {
		meta["target"] = fixtureTarget(fixture)
	} else if fixture.Target != "" {
		meta["target"] = fixture.Target
	}
	if fixture.MemoryKind != "" {
		meta["kind"] = fixture.MemoryKind
	} else if fixture.Kind != "" {
		meta["kind"] = fixture.Kind
	}
	if fixture.SubjectType != "" {
		meta["subject_type"] = fixture.SubjectType
	}
	if len(meta) == 0 {
		return nil
	}
	b, _ := json.Marshal(meta)
	return b
}

func usesStructuredTarget(fixture MemoryFixture) bool {
	return fixture.TargetScope != "" ||
		fixture.TargetID != "" ||
		fixture.Visibility != "" ||
		fixture.TargetUserID != "" ||
		fixture.TenantID != "" ||
		fixture.WorkspaceID != "" ||
		fixture.ProjectID != "" ||
		fixture.RepoID != "" ||
		fixture.TargetSessionID != "" ||
		fixture.AgentName != "" ||
		fixture.SkillName != ""
}

func fixtureTarget(fixture MemoryFixture) memory.MemoryTarget {
	scope := memory.TargetScope(fixture.TargetScope)
	if scope == "" {
		scope = memory.TargetScopeUser
	}
	visibility := memory.TargetVisibility(fixture.Visibility)
	if visibility == "" {
		if scope == memory.TargetScopeGlobal {
			visibility = memory.TargetVisibilityGlobal
		} else {
			visibility = memory.TargetVisibilityPrivate
		}
	}
	userID := fixture.TargetUserID
	if userID == "" {
		userID = fixture.UserID
	}
	sessionID := fixture.TargetSessionID
	if sessionID == "" && scope == memory.TargetScopeSession {
		sessionID = fixture.SessionID
	}
	target := memory.MemoryTarget{
		Scope:       scope,
		ID:          fixture.TargetID,
		Visibility:  visibility,
		UserID:      userID,
		TenantID:    fixture.TenantID,
		WorkspaceID: fixture.WorkspaceID,
		ProjectID:   fixture.ProjectID,
		RepoID:      fixture.RepoID,
		SessionID:   sessionID,
		AgentName:   fixture.AgentName,
		SkillName:   fixture.SkillName,
	}
	if target.ID == "" {
		target.ID = defaultFixtureTargetID(target)
	}
	return target
}

func defaultFixtureTargetID(target memory.MemoryTarget) string {
	switch target.Scope {
	case memory.TargetScopeUser:
		return target.UserID
	case memory.TargetScopeWorkspace:
		return target.WorkspaceID
	case memory.TargetScopeProject:
		return target.ProjectID
	case memory.TargetScopeRepo:
		return target.RepoID
	case memory.TargetScopeSession:
		return target.SessionID
	case memory.TargetScopeAgent:
		return target.AgentName
	case memory.TargetScopeSkill:
		return target.SkillName
	default:
		return ""
	}
}

func fixtureRuntimeContext(c Case) memory.RuntimeContext {
	rctx := memory.RuntimeContext{
		UserID:       c.RuntimeContext.UserID,
		TenantID:     c.RuntimeContext.TenantID,
		WorkspaceID:  c.RuntimeContext.WorkspaceID,
		ProjectID:    c.RuntimeContext.ProjectID,
		RepoID:       c.RuntimeContext.RepoID,
		SessionID:    c.RuntimeContext.SessionID,
		AgentName:    c.RuntimeContext.AgentName,
		SkillName:    c.RuntimeContext.SkillName,
		TaskType:     c.RuntimeContext.TaskType,
		CurrentFiles: append([]string(nil), c.RuntimeContext.CurrentFiles...),
		ToolIntent:   c.RuntimeContext.ToolIntent,
	}
	if rctx.UserID == "" {
		rctx.UserID = c.UserID
	}
	if rctx.SessionID == "" {
		rctx.SessionID = c.SessionID
	}
	return rctx
}

func scopeRuntimeContext(c Case, rctx RuntimeContext) memory.RuntimeContext {
	override := Case{UserID: c.UserID, SessionID: c.SessionID, RuntimeContext: rctx}
	return fixtureRuntimeContext(override)
}

// AssertResult 校验注入结果满足 fixture 期望。
func AssertResult(c Case, result memory.InjectionResult) error {
	injected := map[int64]bool{}
	for _, mem := range result.Memories {
		injected[mem.ID] = true
	}
	skipped := map[int64]bool{}
	for _, id := range result.SkippedMemoryIDs {
		skipped[id] = true
	}

	for _, id := range c.WantInjectedIDs {
		if !injected[id] {
			return fmt.Errorf("%s: expected memory %d injected", c.ID, id)
		}
	}
	for _, id := range c.WantSkippedIDs {
		if injected[id] {
			return fmt.Errorf("%s: expected memory %d skipped", c.ID, id)
		}
		if !skipped[id] {
			return fmt.Errorf("%s: expected memory %d recorded as skipped", c.ID, id)
		}
	}
	if len(c.WantOrderIDs) > 0 {
		pos := map[int64]int{}
		for i, mem := range result.Memories {
			pos[mem.ID] = i
		}
		last := -1
		for _, id := range c.WantOrderIDs {
			p, ok := pos[id]
			if !ok {
				return fmt.Errorf("%s: expected memory %d in injected order", c.ID, id)
			}
			if p <= last {
				return fmt.Errorf("%s: expected memory order %v, got injected %+v", c.ID, c.WantOrderIDs, result.Memories)
			}
			last = p
		}
	}
	for _, text := range c.ForbiddenText {
		if strings.Contains(result.Text, text) {
			return fmt.Errorf("%s: forbidden text %q injected", c.ID, text)
		}
	}
	return nil
}

func AssertStoreExpectations(c Case, store *fixtureMemoryStore) error {
	if len(c.WantBatchGetIDs) > 0 && !reflect.DeepEqual(store.batchGetIDs, c.WantBatchGetIDs) {
		return fmt.Errorf("%s: BatchGet ids = %v, want %v", c.ID, store.batchGetIDs, c.WantBatchGetIDs)
	}
	if c.WantGetCalls != nil && store.getCalls != *c.WantGetCalls {
		return fmt.Errorf("%s: Get calls = %d, want %d", c.ID, store.getCalls, *c.WantGetCalls)
	}
	return nil
}

func AssertMetrics(c Case, recorder *fixtureMetricRecorder) error {
	for _, want := range c.WantMetrics {
		if !recorder.hasMetric(want.Name, want.Labels) {
			return fmt.Errorf("%s: expected metric %s labels %v, got %+v", c.ID, want.Name, want.Labels, recorder.events)
		}
	}
	return nil
}

func AssertMetadata(c Case, records []memory.MemoryRecord) error {
	byID := map[int64]memory.MemoryRecord{}
	for _, record := range records {
		byID[record.ID] = record
	}
	for _, want := range c.WantMetadata {
		record, ok := byID[want.MemoryID]
		if !ok {
			return fmt.Errorf("%s: metadata memory %d not found", c.ID, want.MemoryID)
		}
		var meta struct {
			Kind        string `json:"kind"`
			SubjectType string `json:"subject_type"`
			Governance  struct {
				Source string `json:"source"`
			} `json:"governance"`
		}
		if err := json.Unmarshal(record.Metadata, &meta); err != nil {
			return fmt.Errorf("%s: memory %d metadata invalid: %w", c.ID, want.MemoryID, err)
		}
		target := memory.DecodeMemoryTarget(record.Metadata, record.Type, record.UserID)
		kind := string(memory.DecodeMemoryKind(record.Metadata, record.Type))
		subjectType := meta.SubjectType
		if subjectType == "" {
			subjectType = defaultSubjectTypeForEval(record.Type)
		}
		if err := assertStringWant(c.ID, want.MemoryID, "target_scope", string(target.Scope), want.TargetScope); err != nil {
			return err
		}
		if err := assertStringWant(c.ID, want.MemoryID, "target_id", target.ID, want.TargetID); err != nil {
			return err
		}
		if err := assertStringWant(c.ID, want.MemoryID, "visibility", string(target.Visibility), want.Visibility); err != nil {
			return err
		}
		if err := assertStringWant(c.ID, want.MemoryID, "target_user_id", target.UserID, want.TargetUserID); err != nil {
			return err
		}
		if err := assertStringWant(c.ID, want.MemoryID, "workspace_id", target.WorkspaceID, want.WorkspaceID); err != nil {
			return err
		}
		if err := assertStringWant(c.ID, want.MemoryID, "project_id", target.ProjectID, want.ProjectID); err != nil {
			return err
		}
		if err := assertStringWant(c.ID, want.MemoryID, "repo_id", target.RepoID, want.RepoID); err != nil {
			return err
		}
		if err := assertStringWant(c.ID, want.MemoryID, "session_id", target.SessionID, want.SessionID); err != nil {
			return err
		}
		if err := assertStringWant(c.ID, want.MemoryID, "agent_name", target.AgentName, want.AgentName); err != nil {
			return err
		}
		if err := assertStringWant(c.ID, want.MemoryID, "skill_name", target.SkillName, want.SkillName); err != nil {
			return err
		}
		if err := assertStringWant(c.ID, want.MemoryID, "kind", kind, want.Kind); err != nil {
			return err
		}
		if err := assertStringWant(c.ID, want.MemoryID, "subject_type", subjectType, want.SubjectType); err != nil {
			return err
		}
		if err := assertStringWant(c.ID, want.MemoryID, "source", meta.Governance.Source, want.Source); err != nil {
			return err
		}
	}
	return nil
}

func defaultSubjectTypeForEval(memType memory.MemoryType) string {
	switch memType {
	case memory.MemoryTypeProcedural:
		return "procedure"
	case memory.MemoryTypeEpisodic:
		return "episode"
	default:
		return string(memType)
	}
}

func assertStringWant(caseID string, memoryID int64, field, got, want string) error {
	if want == "" || got == want {
		return nil
	}
	return fmt.Errorf("%s: memory %d metadata %s = %q, want %q", caseID, memoryID, field, got, want)
}

func AssertScope(c Case, records []memory.MemoryRecord) error {
	if len(c.ScopeAssertions) == 0 && len(c.WantSQLContains) == 0 && len(c.WantSQLArgs) == 0 {
		return nil
	}
	byID := map[int64]memory.MemoryRecord{}
	for _, record := range records {
		byID[record.ID] = record
	}
	policy := memory.DefaultScopePolicy{}
	now := time.Now()
	for _, want := range c.ScopeAssertions {
		record, ok := byID[want.MemoryID]
		if !ok {
			return fmt.Errorf("%s: scope assertion memory %d not found", c.ID, want.MemoryID)
		}
		allowed, reason := policy.Allow(record, scopeRuntimeContext(c, want.RuntimeContext), now)
		if allowed != want.Allowed {
			return fmt.Errorf("%s: scope memory %d allowed = %v, want %v (%s)", c.ID, want.MemoryID, allowed, want.Allowed, reason)
		}
		if want.Reason != "" && reason != want.Reason {
			return fmt.Errorf("%s: scope memory %d reason = %q, want %q", c.ID, want.MemoryID, reason, want.Reason)
		}
	}
	filter := policy.SQLFilter(fixtureRuntimeContext(c))
	for _, want := range c.WantSQLContains {
		if !strings.Contains(filter.Clause, want) {
			return fmt.Errorf("%s: SQLFilter missing %q in %s", c.ID, want, filter.Clause)
		}
	}
	for _, want := range c.WantSQLArgs {
		found := false
		for _, arg := range filter.Args {
			if fmt.Sprint(arg) == want {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("%s: SQLFilter args %v missing %q", c.ID, filter.Args, want)
		}
	}
	return nil
}
