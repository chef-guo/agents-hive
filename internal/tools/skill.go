package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"go.uber.org/zap"

	"github.com/chef-guo/agents-hive/internal/auth"
	"github.com/chef-guo/agents-hive/internal/mcphost"
	"github.com/chef-guo/agents-hive/internal/sandbox"
	"github.com/chef-guo/agents-hive/internal/skills"
)

// sandboxToSkillsExecutor 适配器：将 sandbox.Executor 包装为 skills.SandboxExecutor
type sandboxToSkillsExecutor struct {
	inner sandboxExecutor
}

func (a *sandboxToSkillsExecutor) Execute(ctx context.Context, req skills.SandboxExecRequest) (skills.SandboxExecResult, error) {
	result, err := a.inner.Execute(ctx, sandbox.ExecRequest{
		Command:   req.Command,
		SessionID: req.SessionID,
		Timeout:   req.Timeout,
		WorkDir:   req.WorkDir,
		Env:       req.Env,
	})
	if err != nil {
		return skills.SandboxExecResult{}, err
	}
	return skills.SandboxExecResult{
		Stdout:     result.Stdout,
		Stderr:     result.Stderr,
		ExitCode:   result.ExitCode,
		Diagnostic: toSkillsExecDiagnostic(result.Diagnostic),
	}, nil
}

func (a *sandboxToSkillsExecutor) Close() error { return a.inner.Close() }

func toSkillsExecDiagnostic(diag *sandbox.ExecFailureDiagnostic) *skills.SandboxExecDiagnostic {
	if diag == nil {
		return nil
	}
	return &skills.SandboxExecDiagnostic{
		FailureType:          diag.FailureType,
		Summary:              diag.Summary,
		RequiresUserApproval: diag.RequiresUserApproval,
		SuggestedAction:      diag.SuggestedAction,
		SuggestedEnv:         diag.SuggestedEnv,
	}
}

// sandboxToSkillsAdapter 尝试将 sandboxExecutor 适配为 skills.SandboxExecutor
// 如果 globalExecutor 为 nil，返回 nil
func sandboxToSkillsAdapter() skills.SandboxExecutor {
	if globalExecutor == nil {
		return nil
	}
	return &sandboxToSkillsExecutor{inner: globalExecutor}
}

// skillInput 是 skill 工具的输入参数
type skillInput struct {
	Name      string `json:"name"`                // 技能名称（为空时列出所有技能）
	Arguments string `json:"arguments,omitempty"` // 可选参数
}

// skillRegistry 是 registerSkill 需要的最小接口，*skills.Registry 和 *skills.OverlayRegistry 均满足。
// Get/ListSummaries 变参 userID：不传=公开层；传=优先 personal 层（多租户 skill 隔离）。
type skillRegistry interface {
	ListSummaries(userID ...string) []skills.SkillSummary
	Get(name string, userID ...string) (*skills.Skill, error)
	GetForkHandler() skills.ForkHandler
	InvokeFull(ctx context.Context, name string, rctx skills.RenderContext, executor skills.ShellExecutor, runner *skills.ScriptRunner, hookRunner *skills.HookRunner) (string, error)
}

// skillSelfHealDiscovery 是 on_demand 自愈路径所需的最小 Discovery 接口。
// nil → 自愈禁用（等价 pre-change 行为，§8.4 契约保证 byte-identical）。
type skillSelfHealDiscovery interface {
	ResolveByName(ctx context.Context, name string, refresh bool) (*skills.ResolvedSkill, error)
}

// registerSkill 注册 skill 工具到 MCP host（旧签名，自愈禁用）
func registerSkill(host *mcphost.Host, logger *zap.Logger, skillReg skillRegistry) {
	registerSkillWithSelfHeal(host, logger, skillReg, nil)
}

// registerSkillWithSelfHeal 注册 skill 工具；当 discovery 非 nil 时，Get 失败会
// 触发 on-demand 自愈提示（tasks.md §8）。
func registerSkillWithSelfHeal(host *mcphost.Host, logger *zap.Logger, skillReg skillRegistry, discovery skillSelfHealDiscovery) {
	// Schema with name (optional) and arguments (optional)
	schema, _ := json.Marshal(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"name": map[string]any{
				"type":        "string",
				"description": "要调用的技能名称。为空时列出所有可用技能",
			},
			"arguments": map[string]any{
				"type":        "string",
				"description": "传递给技能的参数（可选）",
			},
		},
	})

	host.RegisterTool(
		mcphost.ToolDefinition{
			Name:        "skill",
			Description: "调用或列出可用技能。不传 name 参数时列出所有技能摘要，传入 name 时调用指定技能",
			InputSchema: schema,
		},
		func(ctx context.Context, input json.RawMessage) (*mcphost.ToolResult, error) {
			var params skillInput
			if err := json.Unmarshal(input, &params); err != nil {
				return errorResult("输入无效: " + err.Error()), nil
			}

			userID := auth.UserIDFrom(ctx)

			// 如果 name 为空，列出所有技能
			if params.Name == "" {
				var summaries []skills.SkillSummary
				if userID != "" {
					summaries = skillReg.ListSummaries(userID)
				} else {
					summaries = skillReg.ListSummaries()
				}
				if len(summaries) == 0 {
					return textResult("没有可用的技能"), nil
				}
				data, err := json.Marshal(summaries)
				if err != nil {
					return errorResult("序列化技能列表失败: " + err.Error()), nil
				}
				return textResult(string(data)), nil
			}

			// 调用指定技能（走完整管道：hooks + 动态上下文）
			rctx := skills.RenderContext{
				Arguments: params.Arguments,
			}
			executor := &skills.DefaultShellExecutor{
				Timeout:  600 * time.Second,
				Executor: sandboxToSkillsAdapter(),
			}

			// 检查是否为 context=fork 类型（需要隔离的 sub-agent 执行）
			var s *skills.Skill
			var err error
			if userID != "" {
				s, err = skillReg.Get(params.Name, userID)
			} else {
				s, err = skillReg.Get(params.Name)
			}
			if err != nil {
				// §8 自愈路径：Discovery 非 nil 时尝试解析 marketplace，命中则附加
				// suggested_action；保留原始错误字符串（§8.3 向后兼容）
				return skillGetErrorWithSelfHeal(ctx, discovery, params.Name, err), nil
			}
			if s.Metadata.Context == "fork" {
				forkHandler := skillReg.GetForkHandler()
				if forkHandler == nil {
					return errorResult(fmt.Sprintf("技能 %q 需要 fork 执行但 ForkHandler 未配置", params.Name)), nil
				}
				result, err := forkHandler.ExecuteForked(ctx, s, rctx, executor)
				if err != nil {
					return errorResult(fmt.Sprintf("fork 执行技能 %q 失败: %v", params.Name, err)), nil
				}
				return textResult(result), nil
			}

			hookRunner := skills.NewHookRunner(executor, logger)

			result, err := skillReg.InvokeFull(ctx, params.Name, rctx, executor, nil, hookRunner)
			if err != nil {
				return errorResult(fmt.Sprintf("调用技能 %q 失败: %v", params.Name, err)), nil
			}

			return textResult(result), nil
		},
	)

	logger.Info("已注册 skill 工具")
}

// skillGetErrorWithSelfHeal 在 skill Get 失败时，按 §8 自愈契约返回结果：
//   - discovery == nil → 原始错误字符串（§8.4 byte-identical baseline）
//   - discovery 非 nil 且 ResolveByName 命中 → 在 ToolResult 中追加 suggested_action
//     （§8.3 保留原始 error 字段内容不变，仅扩展 payload）
//   - discovery 非 nil 但未命中 → 原始错误字符串
func skillGetErrorWithSelfHeal(ctx context.Context, discovery skillSelfHealDiscovery, name string, origErr error) *mcphost.ToolResult {
	origMsg := fmt.Sprintf("获取技能 %q 失败: %v", name, origErr)
	if discovery == nil {
		return errorResult(origMsg)
	}
	resolved, rerr := discovery.ResolveByName(ctx, name, false)
	if rerr != nil || resolved == nil {
		return errorResult(origMsg)
	}
	payload, err := json.Marshal(map[string]any{
		"error": origMsg,
		"suggested_action": map[string]any{
			"tool": "skill_install",
			"args": map[string]any{
				"name":   name,
				"scope":  "personal",
				"source": resolved.Source,
			},
			"reason": fmt.Sprintf("skill %q 未在本地注册，可通过 skill_install 从 marketplace 安装", name),
		},
	})
	if err != nil {
		return errorResult(origMsg)
	}
	return &mcphost.ToolResult{Content: jsonText(string(payload)), IsError: true}
}

// registerSkillFromInterface 通过 interface{} 参数注册 skill 工具
// 避免 tools.go 直接导入 skills 包（防止 goimports 移除）
func registerSkillFromInterface(host *mcphost.Host, logger *zap.Logger, skillRegI interface{}) {
	if sr, ok := skillRegI.(*skills.OverlayRegistry); ok && sr != nil {
		registerSkill(host, logger, sr)
	} else if sr, ok := skillRegI.(*skills.Registry); ok && sr != nil {
		registerSkill(host, logger, sr)
	}
}
