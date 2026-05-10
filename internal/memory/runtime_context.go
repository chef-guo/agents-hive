package memory

import (
	"context"

	"github.com/chef-guo/agents-hive/internal/auth"
)

type runtimeContextKey struct{}

// RuntimeContext 描述一次记忆读写发生时的调用者与任务上下文。
type RuntimeContext struct {
	UserID       string
	TenantID     string
	WorkspaceID  string
	ProjectID    string
	RepoID       string
	SessionID    string
	AgentName    string
	SkillName    string
	TaskType     string
	CurrentFiles []string
	ToolIntent   string
}

// WithRuntimeContext 将 RuntimeContext 写入 context。
func WithRuntimeContext(ctx context.Context, rctx RuntimeContext) context.Context {
	return context.WithValue(ctx, runtimeContextKey{}, rctx)
}

// RuntimeContextFromContext 从 context 提取 RuntimeContext，并补齐 auth 中的 user_id。
func RuntimeContextFromContext(ctx context.Context) RuntimeContext {
	if ctx == nil {
		return RuntimeContext{}
	}
	rctx, _ := ctx.Value(runtimeContextKey{}).(RuntimeContext)
	if rctx.UserID == "" {
		rctx.UserID = auth.UserIDFrom(ctx)
	}
	return rctx
}

// RuntimeContextFrom 是 RuntimeContextFromContext 的简写兼容入口。
func RuntimeContextFrom(ctx context.Context) RuntimeContext {
	return RuntimeContextFromContext(ctx)
}

// MergeRuntimeContext 返回以 override 非空字段覆盖 base 后的上下文。
func MergeRuntimeContext(base RuntimeContext, override RuntimeContext) RuntimeContext {
	if override.UserID != "" {
		base.UserID = override.UserID
	}
	if override.TenantID != "" {
		base.TenantID = override.TenantID
	}
	if override.WorkspaceID != "" {
		base.WorkspaceID = override.WorkspaceID
	}
	if override.ProjectID != "" {
		base.ProjectID = override.ProjectID
	}
	if override.RepoID != "" {
		base.RepoID = override.RepoID
	}
	if override.SessionID != "" {
		base.SessionID = override.SessionID
	}
	if override.AgentName != "" {
		base.AgentName = override.AgentName
	}
	if override.SkillName != "" {
		base.SkillName = override.SkillName
	}
	if override.TaskType != "" {
		base.TaskType = override.TaskType
	}
	if len(override.CurrentFiles) > 0 {
		base.CurrentFiles = append([]string(nil), override.CurrentFiles...)
	}
	if override.ToolIntent != "" {
		base.ToolIntent = override.ToolIntent
	}
	return base
}
