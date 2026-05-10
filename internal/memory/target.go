package memory

import (
	"encoding/json"
	"fmt"
)

type TargetScope string
type TargetVisibility string
type MemoryKind string

const (
	TargetScopeUser      TargetScope = "user"
	TargetScopeGlobal    TargetScope = "global"
	TargetScopeTeam      TargetScope = "team"
	TargetScopeOrg       TargetScope = "org"
	TargetScopeWorkspace TargetScope = "workspace"
	TargetScopeProject   TargetScope = "project"
	TargetScopeRepo      TargetScope = "repo"
	TargetScopeSession   TargetScope = "session"
	TargetScopeAgent     TargetScope = "agent"
	TargetScopeSkill     TargetScope = "skill"

	TargetVisibilityPrivate TargetVisibility = "private"
	TargetVisibilityGlobal  TargetVisibility = "global"
	TargetVisibilityPublic  TargetVisibility = "public"
)

// MemoryTarget 是 metadata.target 的规范结构。
type MemoryTarget struct {
	Scope       TargetScope      `json:"target_scope"`
	ID          string           `json:"target_id,omitempty"`
	Visibility  TargetVisibility `json:"visibility"`
	UserID      string           `json:"user_id,omitempty"`
	TenantID    string           `json:"tenant_id,omitempty"`
	WorkspaceID string           `json:"workspace_id,omitempty"`
	ProjectID   string           `json:"project_id,omitempty"`
	RepoID      string           `json:"repo_id,omitempty"`
	SessionID   string           `json:"session_id,omitempty"`
	AgentName   string           `json:"agent_name,omitempty"`
	SkillName   string           `json:"skill_name,omitempty"`
}

func DecodeMemoryTarget(raw json.RawMessage, memType MemoryType, userID string) MemoryTarget {
	meta := decodeMetadataMap(raw)
	var target MemoryTarget
	if v, ok := meta["target"]; ok {
		b, _ := json.Marshal(v)
		_ = json.Unmarshal(b, &target)
	}
	return normalizeTarget(target, RuntimeContext{UserID: userID}, memType)
}

func EncodeMemoryTarget(raw json.RawMessage, target MemoryTarget) json.RawMessage {
	meta := decodeMetadataMap(raw)
	meta["target"] = target
	return mustMarshalRaw(meta)
}

func DecodeMemoryKind(raw json.RawMessage, memType MemoryType) MemoryKind {
	meta := decodeMetadataMap(raw)
	if kind, _ := meta["kind"].(string); kind != "" {
		return MemoryKind(kind)
	}
	return defaultMemoryKind(memType)
}

func EncodeMemoryKind(raw json.RawMessage, kind MemoryKind) json.RawMessage {
	meta := decodeMetadataMap(raw)
	meta["kind"] = string(kind)
	return mustMarshalRaw(meta)
}

func NormalizeMemoryRecord(record *MemoryRecord, rctx RuntimeContext) error {
	if record == nil {
		return fmt.Errorf("记忆记录不能为空")
	}
	if record.Type == "" {
		record.Type = MemoryTypeUser
	}
	if !ValidMemoryTypes[record.Type] {
		return fmt.Errorf("无效的记忆类型: %s", record.Type)
	}
	if record.UserID == "" && rctx.UserID != "" {
		record.UserID = rctx.UserID
	}
	meta, err := normalizeMemoryMetadata(record.Metadata, record.Type, record.UserID, rctx)
	if err != nil {
		return err
	}
	record.Metadata = meta
	return nil
}

func normalizeMemoryMetadata(raw json.RawMessage, memType MemoryType, userID string, rctx RuntimeContext) (json.RawMessage, error) {
	meta, err := parseMetadataMap(raw)
	if err != nil {
		return nil, err
	}

	var target MemoryTarget
	if v, ok := meta["target"]; ok {
		b, _ := json.Marshal(v)
		if err := json.Unmarshal(b, &target); err != nil {
			return nil, fmt.Errorf("metadata.target 无效: %w", err)
		}
	}
	if rctx.UserID == "" {
		rctx.UserID = userID
	}
	target = normalizeTarget(target, rctx, memType)
	if err := validateTarget(target); err != nil {
		return nil, err
	}
	meta["target"] = target

	kind, _ := meta["kind"].(string)
	if kind == "" {
		kind = string(defaultMemoryKind(memType))
	}
	if !validMemoryKind(MemoryKind(kind)) {
		return nil, fmt.Errorf("无效的记忆 kind: %s", kind)
	}
	meta["kind"] = kind

	subjectType, _ := meta["subject_type"].(string)
	if subjectType == "" {
		meta["subject_type"] = defaultSubjectType(memType)
	}

	return mustMarshalRaw(meta), nil
}

func normalizeTarget(target MemoryTarget, rctx RuntimeContext, memType MemoryType) MemoryTarget {
	if target.Scope == "" {
		target.Scope = TargetScopeUser
	}
	if target.Visibility == "" {
		if target.Scope == TargetScopeGlobal {
			target.Visibility = TargetVisibilityGlobal
		} else {
			target.Visibility = TargetVisibilityPrivate
		}
	}
	if target.UserID == "" {
		target.UserID = rctx.UserID
	}
	if target.TenantID == "" {
		target.TenantID = rctx.TenantID
	}
	if target.WorkspaceID == "" {
		target.WorkspaceID = rctx.WorkspaceID
	}
	if target.ProjectID == "" {
		target.ProjectID = rctx.ProjectID
	}
	if target.RepoID == "" {
		target.RepoID = rctx.RepoID
	}
	if target.SessionID == "" {
		target.SessionID = rctx.SessionID
	}
	if target.AgentName == "" {
		target.AgentName = rctx.AgentName
	}
	if target.SkillName == "" {
		target.SkillName = rctx.SkillName
	}
	if target.ID == "" {
		target.ID = defaultTargetID(target)
	}
	if target.Scope == TargetScopeSession && target.SessionID == "" {
		target.SessionID = rctx.SessionID
	}
	_ = memType
	return target
}

func validateTarget(target MemoryTarget) error {
	switch target.Scope {
	case TargetScopeUser, TargetScopeGlobal, TargetScopeTeam, TargetScopeOrg,
		TargetScopeWorkspace, TargetScopeProject, TargetScopeRepo, TargetScopeSession,
		TargetScopeAgent, TargetScopeSkill:
	default:
		return fmt.Errorf("无效的 target_scope: %s", target.Scope)
	}
	switch target.Visibility {
	case TargetVisibilityPrivate, TargetVisibilityGlobal, TargetVisibilityPublic:
	default:
		return fmt.Errorf("无效的 target visibility: %s", target.Visibility)
	}
	if target.Scope == TargetScopeGlobal && target.Visibility == TargetVisibilityPrivate {
		return fmt.Errorf("global target 不能使用 private visibility")
	}
	if target.Scope != TargetScopeGlobal && target.Visibility != TargetVisibilityPrivate {
		return fmt.Errorf("%s target 暂不支持 %s visibility", target.Scope, target.Visibility)
	}
	return nil
}

func defaultTargetID(target MemoryTarget) string {
	switch target.Scope {
	case TargetScopeUser:
		return target.UserID
	case TargetScopeWorkspace:
		return target.WorkspaceID
	case TargetScopeProject:
		return target.ProjectID
	case TargetScopeRepo:
		return target.RepoID
	case TargetScopeSession:
		return target.SessionID
	case TargetScopeAgent:
		return target.AgentName
	case TargetScopeSkill:
		return target.SkillName
	default:
		return ""
	}
}

func defaultMemoryKind(memType MemoryType) MemoryKind {
	switch memType {
	case MemoryTypeFeedback:
		return MemoryKind("feedback")
	case MemoryTypeReference:
		return MemoryKind("reference")
	case MemoryTypeProcedural:
		return MemoryKind("procedural")
	case MemoryTypeEpisodic:
		return MemoryKind("episodic")
	default:
		return MemoryKind("semantic")
	}
}

func defaultSubjectType(memType MemoryType) string {
	switch memType {
	case MemoryTypeProcedural:
		return "procedure"
	case MemoryTypeEpisodic:
		return "episode"
	default:
		return string(memType)
	}
}

func validMemoryKind(kind MemoryKind) bool {
	switch kind {
	case "semantic", "feedback", "reference", "procedural", "episodic":
		return true
	default:
		return false
	}
}

func parseMetadataMap(raw json.RawMessage) (map[string]any, error) {
	if len(raw) == 0 {
		return map[string]any{}, nil
	}
	var meta map[string]any
	if err := json.Unmarshal(raw, &meta); err != nil {
		return nil, fmt.Errorf("metadata 不是合法 JSON object: %w", err)
	}
	if meta == nil {
		meta = map[string]any{}
	}
	return meta, nil
}

func decodeMetadataMap(raw json.RawMessage) map[string]any {
	meta, err := parseMetadataMap(raw)
	if err != nil {
		return map[string]any{}
	}
	return meta
}
