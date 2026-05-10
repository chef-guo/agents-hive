package memory

import (
	"strings"
	"time"
)

type ScopePolicy interface {
	Allow(record MemoryRecord, rctx RuntimeContext, now time.Time) (bool, string)
	SQLFilter(rctx RuntimeContext) ScopeSQLFilter
}

type ScopeSQLFilter struct {
	Clause string
	Args   []any
}

type DefaultScopePolicy struct{}

func (DefaultScopePolicy) Allow(record MemoryRecord, rctx RuntimeContext, _ time.Time) (bool, string) {
	target := DecodeMemoryTarget(record.Metadata, record.Type, record.UserID)
	if target.Scope == TargetScopeGlobal || target.Visibility == TargetVisibilityGlobal || target.Visibility == TargetVisibilityPublic {
		return true, "global_visibility"
	}

	ownerID := record.UserID
	if ownerID == "" {
		ownerID = target.UserID
	}
	switch target.Scope {
	case TargetScopeUser:
		if ownerID == rctx.UserID {
			return true, "same_user"
		}
		return false, "cross_user"
	case TargetScopeTeam, TargetScopeOrg, TargetScopeWorkspace, TargetScopeProject, TargetScopeRepo, TargetScopeSession:
		// 当前没有成员关系服务可验证团队/组织/工作区可见性，先只允许同一 owner 用户读取。
		if rctx.UserID != "" && ownerID == rctx.UserID {
			return true, "same_owner_fail_closed"
		}
		return false, "membership_unavailable"
	case TargetScopeAgent:
		if rctx.UserID != "" && ownerID == rctx.UserID && target.AgentName != "" && target.AgentName == rctx.AgentName {
			return true, "same_agent"
		}
		return false, "agent_scope_mismatch"
	case TargetScopeSkill:
		if rctx.UserID != "" && ownerID == rctx.UserID && target.SkillName != "" && target.SkillName == rctx.SkillName {
			return true, "same_skill"
		}
		return false, "skill_scope_mismatch"
	default:
		return false, "invalid_scope"
	}
}

func (DefaultScopePolicy) SQLFilter(rctx RuntimeContext) ScopeSQLFilter {
	userID := rctx.UserID
	return ScopeSQLFilter{
		Clause: `(
			COALESCE(NULLIF(metadata->'target'->>'target_scope', ''), '') = 'global'
			OR COALESCE(NULLIF(metadata->'target'->>'visibility', ''), '') IN ('global', 'public')
			OR (
				user_id = ?
				AND COALESCE(NULLIF(metadata->'target'->>'target_scope', ''), 'user') = 'user'
				AND COALESCE(NULLIF(metadata->'target'->>'visibility', ''), 'private') = 'private'
			)
			OR (
				? <> ''
				AND user_id = ?
				AND COALESCE(NULLIF(metadata->'target'->>'target_scope', ''), '') IN ('team', 'org', 'workspace', 'project', 'repo', 'session', 'agent', 'skill')
				AND COALESCE(NULLIF(metadata->'target'->>'visibility', ''), 'private') = 'private'
				AND (
					COALESCE(NULLIF(metadata->'target'->>'target_scope', ''), '') NOT IN ('agent', 'skill')
					OR (COALESCE(NULLIF(metadata->'target'->>'target_scope', ''), '') = 'agent' AND metadata->'target'->>'agent_name' = ?)
					OR (COALESCE(NULLIF(metadata->'target'->>'target_scope', ''), '') = 'skill' AND metadata->'target'->>'skill_name' = ?)
				)
			)
		)`,
		Args: []any{userID, userID, userID, rctx.AgentName, rctx.SkillName},
	}
}

func appendScopeSQL(query string, args []any, argIdx int, filter ScopeSQLFilter) (string, []any, int) {
	if strings.TrimSpace(filter.Clause) == "" {
		return query, args, argIdx
	}
	clause := filter.Clause
	for strings.Contains(clause, "?") {
		clause = strings.Replace(clause, "?", "$"+itoa(argIdx), 1)
		argIdx++
	}
	query += " AND " + clause
	args = append(args, filter.Args...)
	return query, args, argIdx
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
