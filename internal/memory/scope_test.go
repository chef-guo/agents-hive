package memory

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestDefaultScopePolicyAllowPrivateUserAndGlobal(t *testing.T) {
	policy := DefaultScopePolicy{}
	now := time.Now()

	private := MemoryRecord{UserID: "user-1", Type: MemoryTypeUser}
	if allowed, reason := policy.Allow(private, RuntimeContext{UserID: "user-1"}, now); !allowed || reason != "same_user" {
		t.Fatalf("same-user private memory = %v/%s, want allowed/same_user", allowed, reason)
	}
	if allowed, reason := policy.Allow(private, RuntimeContext{UserID: "user-2"}, now); allowed || reason != "cross_user" {
		t.Fatalf("cross-user private memory = %v/%s, want denied/cross_user", allowed, reason)
	}

	globalMeta := mustMarshalRaw(map[string]any{
		"target": MemoryTarget{Scope: TargetScopeGlobal, Visibility: TargetVisibilityGlobal},
	})
	global := MemoryRecord{UserID: "user-1", Type: MemoryTypeReference, Metadata: globalMeta}
	if allowed, reason := policy.Allow(global, RuntimeContext{UserID: "user-2"}, now); !allowed || reason != "global_visibility" {
		t.Fatalf("global memory = %v/%s, want allowed/global_visibility", allowed, reason)
	}
}

func TestDefaultScopePolicyTeamFailsClosedToSameUser(t *testing.T) {
	policy := DefaultScopePolicy{}
	meta := mustMarshalRaw(map[string]any{
		"target": MemoryTarget{Scope: TargetScopeTeam, Visibility: TargetVisibilityPrivate, UserID: "user-1"},
	})
	record := MemoryRecord{UserID: "user-1", Type: MemoryTypeProject, Metadata: meta}

	if allowed, reason := policy.Allow(record, RuntimeContext{UserID: "user-1"}, time.Now()); !allowed || reason != "same_owner_fail_closed" {
		t.Fatalf("team target owned by same user = %v/%s, want allowed/same_owner_fail_closed", allowed, reason)
	}
	if allowed, reason := policy.Allow(record, RuntimeContext{UserID: "user-2"}, time.Now()); allowed || reason != "membership_unavailable" {
		t.Fatalf("team target other user = %v/%s, want denied/membership_unavailable", allowed, reason)
	}
}

func TestDefaultScopePolicyAgentAndSkillScopes(t *testing.T) {
	policy := DefaultScopePolicy{}
	now := time.Now()
	agentMeta := mustMarshalRaw(map[string]any{
		"target": MemoryTarget{Scope: TargetScopeAgent, Visibility: TargetVisibilityPrivate, UserID: "user-1", AgentName: "agent-a"},
	})
	agentRecord := MemoryRecord{UserID: "user-1", Type: MemoryTypeProcedural, Metadata: agentMeta}
	if allowed, reason := policy.Allow(agentRecord, RuntimeContext{UserID: "user-1", AgentName: "agent-a"}, now); !allowed || reason != "same_agent" {
		t.Fatalf("agent target = %v/%s, want allowed/same_agent", allowed, reason)
	}
	if allowed, reason := policy.Allow(agentRecord, RuntimeContext{UserID: "user-1", AgentName: "agent-b"}, now); allowed || reason != "agent_scope_mismatch" {
		t.Fatalf("agent mismatch = %v/%s, want denied/agent_scope_mismatch", allowed, reason)
	}

	skillMeta := mustMarshalRaw(map[string]any{
		"target": MemoryTarget{Scope: TargetScopeSkill, Visibility: TargetVisibilityPrivate, UserID: "user-1", SkillName: "skill-a"},
	})
	skillRecord := MemoryRecord{UserID: "user-1", Type: MemoryTypeProcedural, Metadata: skillMeta}
	if allowed, reason := policy.Allow(skillRecord, RuntimeContext{UserID: "user-1", SkillName: "skill-a"}, now); !allowed || reason != "same_skill" {
		t.Fatalf("skill target = %v/%s, want allowed/same_skill", allowed, reason)
	}
	if allowed, reason := policy.Allow(skillRecord, RuntimeContext{UserID: "user-1", SkillName: "skill-b"}, now); allowed || reason != "skill_scope_mismatch" {
		t.Fatalf("skill mismatch = %v/%s, want denied/skill_scope_mismatch", allowed, reason)
	}
}

func TestDefaultScopePolicySQLFilterIncludesOldDataSemantics(t *testing.T) {
	filter := DefaultScopePolicy{}.SQLFilter(RuntimeContext{UserID: "user-1"})

	if len(filter.Args) != 5 {
		t.Fatalf("args = %+v, want 5 args", filter.Args)
	}
	if !strings.Contains(filter.Clause, "COALESCE(NULLIF(metadata->'target'->>'target_scope', ''), 'user') = 'user'") {
		t.Fatalf("SQLFilter missing old data target_scope default: %s", filter.Clause)
	}
	if !strings.Contains(filter.Clause, "COALESCE(NULLIF(metadata->'target'->>'visibility', ''), 'private') = 'private'") {
		t.Fatalf("SQLFilter missing old data visibility default: %s", filter.Clause)
	}
	if !strings.Contains(filter.Clause, "metadata->'target'->>'agent_name' = ?") {
		t.Fatalf("SQLFilter missing agent scope filter: %s", filter.Clause)
	}
	if !strings.Contains(filter.Clause, "metadata->'target'->>'skill_name' = ?") {
		t.Fatalf("SQLFilter missing skill scope filter: %s", filter.Clause)
	}
}

func TestAppendScopeSQLNumbersPlaceholders(t *testing.T) {
	query, args, next := appendScopeSQL("SELECT * FROM memories WHERE TRUE", []any{"existing"}, 2, ScopeSQLFilter{
		Clause: "user_id = ? OR user_id = ?",
		Args:   []any{"u1", "u2"},
	})

	if next != 4 {
		t.Fatalf("next arg index = %d, want 4", next)
	}
	if !strings.Contains(query, "user_id = $2 OR user_id = $3") {
		t.Fatalf("query = %s", query)
	}
	if got, _ := json.Marshal(args); string(got) != `["existing","u1","u2"]` {
		t.Fatalf("args = %s", got)
	}
}
