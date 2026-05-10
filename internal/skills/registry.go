package skills

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/chef-guo/agents-hive/internal/errs"
)

// registryKey 是 Registry 内部的复合索引键：
//   - UserID = "" 表示 public scope（所有租户共享）
//   - UserID != "" 表示 personal scope（仅该租户可见）
//
// 同名 skill 在 public + personal 层并存是合法场景（personal 覆盖 public）。
type registryKey struct {
	Name   string
	UserID string
}

// Registry 管理 skill 注册和查找。
//
// 租户隔离契约：
//   - public skill 以 registryKey{Name, ""} 索引
//   - personal skill 以 registryKey{Name, UserID} 索引
//   - Get(name, userID) 先查 personal，未命中回落 public
//   - Get(name) 等价于 Get(name, "") — 仅查 public（向后兼容）
type Registry struct {
	mu             sync.RWMutex
	skills         map[registryKey]*Skill
	pinnedVersions map[string]string // name → pinned semver（config 注入）
	toolBridge     *ToolBridge
	forkHandler    ForkHandler
	metrics        *Metrics
	logger         *zap.Logger
}

// ForkHandler 处理 context=fork 类型 skill 的隔离执行
type ForkHandler interface {
	ExecuteForked(ctx context.Context, skill *Skill, rctx RenderContext, executor ShellExecutor) (string, error)
}

// NewRegistry 创建新的 skill 注册表
func NewRegistry(logger *zap.Logger) *Registry {
	return &Registry{
		skills:         make(map[registryKey]*Skill),
		pinnedVersions: make(map[string]string),
		logger:         logger,
	}
}

// SetPinnedVersions 注入版本 pin（bootstrap 时调用）
func (r *Registry) SetPinnedVersions(pins map[string]string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if pins == nil {
		r.pinnedVersions = make(map[string]string)
		return
	}
	r.pinnedVersions = make(map[string]string, len(pins))
	for k, v := range pins {
		r.pinnedVersions[k] = v
	}
}

// firstUserID 从可选 userID 变参中提取首个非空值
func firstUserID(userID ...string) string {
	if len(userID) == 0 {
		return ""
	}
	return userID[0]
}

// Register 将 skill 添加到注册表。如果已存在同键 skill，则按 semver 比较决定：
//   - 新版本 > 已注册版本：替换 + info 日志
//   - 新版本 == 已注册版本：no-op + metrics `skill.registry.dup`
//   - 新版本 < 已注册版本：保留旧版 + warn（除非 pinnedVersions 强制）
//   - 无版本字段：默认替换（保持旧行为）
//
// Personal scope skill 必须在 Metadata.UserID 中带非空 userID，否则拒绝注册。
// 跨租户校验：personal + 非空 userID 组合 → 索引到 {Name, UserID}；
// public + 空 userID → 索引到 {Name, ""}；其它组合拒绝。
func (r *Registry) Register(skill *Skill) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	scope := skill.Metadata.Scope
	if scope == "" {
		scope = ScopePublic
	}
	if scope == ScopePersonal && skill.Metadata.UserID == "" {
		return errs.New(errs.CodeSkillInvalidName,
			fmt.Sprintf("personal scope skill %q requires non-empty userID", skill.Metadata.Name))
	}
	if scope == ScopePublic && skill.Metadata.UserID != "" {
		return errs.New(errs.CodeSkillInvalidName,
			fmt.Sprintf("public scope skill %q must not carry userID (got %q)", skill.Metadata.Name, skill.Metadata.UserID))
	}
	skill.Metadata.Scope = scope

	key := registryKey{Name: skill.Metadata.Name, UserID: skill.Metadata.UserID}

	if err := r.checkCircularLocked(skill.Metadata.Name, skill.Metadata.DependsOn, nil); err != nil {
		return err
	}

	existing, exists := r.skills[key]
	if exists {
		pinned := r.pinnedVersions[skill.Metadata.Name]
		if pinned != "" && existing.Metadata.Version == pinned && skill.Metadata.Version != pinned {
			r.logger.Warn("skill 版本被 pin 锁定，跳过注册",
				zap.String("name", skill.Metadata.Name),
				zap.String("user_id", skill.Metadata.UserID),
				zap.String("pinned", pinned),
				zap.String("incoming", skill.Metadata.Version))
			return nil
		}
		cmp := compareSemver(skill.Metadata.Version, existing.Metadata.Version)
		switch {
		case cmp == 0 && skill.Metadata.Version != "":
			if r.metrics != nil {
				r.metrics.RecordDup(skill.Metadata.Name)
			}
			r.logger.Debug("skill 同版本重复注册，no-op",
				zap.String("name", skill.Metadata.Name),
				zap.String("user_id", skill.Metadata.UserID),
				zap.String("version", skill.Metadata.Version))
			return nil
		case cmp < 0:
			r.logger.Warn("skill 版本降级，保留旧版",
				zap.String("name", skill.Metadata.Name),
				zap.String("user_id", skill.Metadata.UserID),
				zap.String("existing", existing.Metadata.Version),
				zap.String("incoming", skill.Metadata.Version))
			return nil
		}
		r.logger.Info("替换 skill",
			zap.String("name", skill.Metadata.Name),
			zap.String("user_id", skill.Metadata.UserID),
			zap.String("scope", string(scope)))
	} else {
		r.logger.Info("注册 skill",
			zap.String("name", skill.Metadata.Name),
			zap.String("user_id", skill.Metadata.UserID),
			zap.String("scope", string(scope)))
	}
	r.skills[key] = skill
	return nil
}

// checkCircularLocked 检查 name 的依赖链是否存在循环。调用方必须持有 r.mu 读锁或写锁。
// visited 表示当前递归路径上的节点（不跨兄弟分支共享，每个分支独立复制）。
// 循环检查仅按 name 匹配（跨 scope），因为 depends-on 仅声明 skill 名字。
func (r *Registry) checkCircularLocked(name string, deps []string, visited map[string]bool) error {
	if visited == nil {
		visited = make(map[string]bool)
	}
	visited[name] = true
	for _, dep := range deps {
		if visited[dep] {
			return errs.New(errs.CodeSkillLoadFailed,
				fmt.Sprintf("circular dependency detected: %q depends on %q which is already in the chain", name, dep))
		}
		// 优先查 public（跨租户共享的依赖基线）；personal 层 skill 不应在 depends-on 中被 name-lookup
		depKey := registryKey{Name: dep, UserID: ""}
		if s, ok := r.skills[depKey]; ok {
			// 每个分支独立复制 visited，避免兄弟节点共享同一 set 导致合法 DAG 被误判
			branchVisited := make(map[string]bool, len(visited)+1)
			for k, v := range visited {
				branchVisited[k] = v
			}
			if err := r.checkCircularLocked(dep, s.Metadata.DependsOn, branchVisited); err != nil {
				return err
			}
		}
	}
	return nil
}

// Unregister 从注册表中移除 skill（默认按 public 层；可选传 userID 移除 personal）。
func (r *Registry) Unregister(name string, userID ...string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := registryKey{Name: name, UserID: firstUserID(userID...)}
	if _, exists := r.skills[key]; !exists {
		return errs.New(errs.CodeSkillNotFound, fmt.Sprintf("skill %q (user_id=%q) not found", name, key.UserID))
	}
	delete(r.skills, key)
	r.logger.Info("已注销 skill", zap.String("name", name), zap.String("user_id", key.UserID))
	return nil
}

// Get 根据名称返回 skill。可选 userID 参数开启 personal-first 查找：
//   - Get(name)：等价 Get(name, "")，仅查 public 层（向后兼容）
//   - Get(name, userID)：先查 personal(userID)，未命中回落 public
//
// 绝不跨租户返回：Get(name, "alice") 绝不返回 bob 的 personal skill。
func (r *Registry) Get(name string, userID ...string) (*Skill, error) {
	uid := firstUserID(userID...)
	r.mu.RLock()
	defer r.mu.RUnlock()
	if uid != "" {
		if s, ok := r.skills[registryKey{Name: name, UserID: uid}]; ok {
			return s, nil
		}
	}
	if s, ok := r.skills[registryKey{Name: name, UserID: ""}]; ok {
		return s, nil
	}
	return nil, errs.New(errs.CodeSkillNotFound, fmt.Sprintf("skill %q not found for user %q", name, uid))
}

// List 返回已注册 skill 的元数据列表。
//   - List()：等价 List("")，仅返回 public 层（向后兼容）
//   - List(userID)：合并 personal(userID) + public，personal 覆盖同名 public
//
// 不返回其它租户的 personal skill（跨租户隔离）。
func (r *Registry) List(userID ...string) []SkillMetadata {
	uid := firstUserID(userID...)
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.listLocked(uid)
}

// listLocked 内部版本，调用方必须持有 r.mu 读锁。
// personal 优先覆盖同名 public。
func (r *Registry) listLocked(uid string) []SkillMetadata {
	personalNames := make(map[string]bool)
	result := make([]SkillMetadata, 0, len(r.skills))
	if uid != "" {
		for key, s := range r.skills {
			if key.UserID == uid {
				result = append(result, s.Metadata)
				personalNames[key.Name] = true
			}
		}
	}
	for key, s := range r.skills {
		if key.UserID == "" && !personalNames[key.Name] {
			result = append(result, s.Metadata)
		}
	}
	return result
}

// Invoke 返回 skill 渲染后的提示词内容。懒加载完整内容（Level 1 → Level 2），
// 然后使用给定的 RenderContext 渲染。这并不"执行"任何东西——它返回要注入到
// LLM 提示词中的文本。Registry 实现了 SkillResolver 接口，支持 ${SKILL:name} 占位符。
func (r *Registry) Invoke(name string, rctx RenderContext) (string, error) {
	s, err := r.Get(name)
	if err != nil {
		return "", err
	}
	if err := s.LoadContent(); err != nil {
		return "", err
	}
	if rctx.SkillDir == "" {
		rctx.SkillDir = s.Path
	}
	// 注入自身作为 Resolver，支持 ${SKILL:name} 占位符
	if rctx.Resolver == nil {
		rctx.Resolver = r
	}
	return s.Render(rctx), nil
}

// InvokeWithDynamicContext 类似 Invoke，但同时通过给定的 ShellExecutor
// 处理 !`command` 动态上下文占位符
func (r *Registry) InvokeWithDynamicContext(name string, rctx RenderContext, executor ShellExecutor) (string, error) {
	rendered, err := r.Invoke(name, rctx)
	if err != nil {
		return "", err
	}
	return ExecuteDynamicContext(rendered, executor)
}

// ListSummaries 返回已注册 skill 的 Level 1 摘要。
//   - ListSummaries()：等价 ListSummaries("")，仅 public
//   - ListSummaries(userID)：合并 personal(userID) + public，personal 覆盖同名 public；
//     `OverriddenPublic` 字段标记覆盖关系（personal 层 true，public 层 false）
func (r *Registry) ListSummaries(userID ...string) []SkillSummary {
	uid := firstUserID(userID...)
	r.mu.RLock()
	defer r.mu.RUnlock()

	personalByName := make(map[string]*Skill)
	if uid != "" {
		for key, s := range r.skills {
			if key.UserID == uid {
				personalByName[key.Name] = s
			}
		}
	}
	result := make([]SkillSummary, 0, len(r.skills))
	// personal 层（如有）
	for name, s := range personalByName {
		overridden := false
		if _, hasPublic := r.skills[registryKey{Name: name, UserID: ""}]; hasPublic {
			overridden = true
		}
		result = append(result, SkillSummary{
			Name:             s.Metadata.Name,
			Description:      s.Metadata.Description,
			OverriddenPublic: overridden,
		})
	}
	// public 层（跳过被 personal 覆盖的）
	for key, s := range r.skills {
		if key.UserID != "" {
			continue
		}
		if _, covered := personalByName[key.Name]; covered {
			continue
		}
		result = append(result, SkillSummary{
			Name:        s.Metadata.Name,
			Description: s.Metadata.Description,
		})
	}
	return result
}

// Count 返回当前视图下的 skill 数量。
// Count()：仅 public 层；Count(userID)：personal + public 合并后去重。
func (r *Registry) Count(userID ...string) int {
	return len(r.List(userID...))
}

// ListForModel 返回未禁用模型调用的 skill 元数据（合并 personal + public，personal 覆盖）
func (r *Registry) ListForModel(userID ...string) []SkillMetadata {
	all := r.List(userID...)
	result := make([]SkillMetadata, 0, len(all))
	for _, m := range all {
		if !m.DisableModelInvocation {
			result = append(result, m)
		}
	}
	return result
}

// ListUserInvocable 返回用户可调用的 skill（合并 personal + public，personal 覆盖）
func (r *Registry) ListUserInvocable(userID ...string) []SkillMetadata {
	all := r.List(userID...)
	result := make([]SkillMetadata, 0, len(all))
	for _, m := range all {
		if m.IsUserInvocable() {
			result = append(result, m)
		}
	}
	return result
}

// FindBySpecRequirements 返回声明的 ProvidesRequirements 与 reqs 有交集的 skill。
// 最小实现：spec-driven-subagents 合入后会被其真实逻辑替换。
func (r *Registry) FindBySpecRequirements(reqs []string, userID string) []*Skill {
	if len(reqs) == 0 {
		return nil
	}
	reqSet := make(map[string]bool, len(reqs))
	for _, req := range reqs {
		reqSet[req] = true
	}
	r.mu.RLock()
	defer r.mu.RUnlock()

	// personal 先查
	var matched []*Skill
	seenName := make(map[string]bool)
	consider := func(key registryKey, s *Skill) {
		if seenName[key.Name] {
			return
		}
		for _, p := range s.Metadata.ProvidesRequirements {
			if reqSet[p] {
				matched = append(matched, s)
				seenName[key.Name] = true
				return
			}
		}
	}
	if userID != "" {
		for key, s := range r.skills {
			if key.UserID == userID {
				consider(key, s)
			}
		}
	}
	for key, s := range r.skills {
		if key.UserID == "" {
			consider(key, s)
		}
	}
	return matched
}

// RegisterFromPath 扫描单个 skill 目录（含 SKILL.md），解析 frontmatter 后注册。
// scope / userID 参数覆盖 frontmatter（bootstrap/install 场景下由外部强制指定）。
func (r *Registry) RegisterFromPath(_ context.Context, path string, scope SkillScope, userID string) error {
	if scope == "" {
		scope = ScopePublic
	}
	if scope == ScopePersonal && userID == "" {
		return errs.New(errs.CodeSkillInvalidName,
			fmt.Sprintf("RegisterFromPath: personal scope requires userID (path=%s)", path))
	}
	skill, err := loadSkillFromDir(path)
	if err != nil {
		return err
	}
	skill.Metadata.Scope = scope
	if scope == ScopePersonal {
		skill.Metadata.UserID = userID
	} else {
		skill.Metadata.UserID = ""
	}
	return r.Register(skill)
}

// InvokeWithScripts 调用 skill 并执行其捆绑的脚本，
// 返回渲染内容并附加脚本输出。添加总体 5 分钟超时保护
func (r *Registry) InvokeWithScripts(ctx context.Context, name string, rctx RenderContext, runner *ScriptRunner) (string, error) {
	// 超时硬编码为 5 分钟，后续可从配置项读取
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	rendered, err := r.Invoke(name, rctx)
	if err != nil {
		return "", err
	}

	s, err := r.Get(name)
	if err != nil {
		return "", err
	}

	if err := s.LoadBundledFiles(); err != nil {
		return "", err
	}

	if len(s.Bundled.Scripts) == 0 {
		return rendered, nil
	}

	results, err := runner.RunAllScripts(ctx, s.Path, s.Bundled.Scripts)
	if err != nil {
		return rendered, err
	}

	// Append script outputs to the rendered content
	var appendix string
	for _, script := range s.Bundled.Scripts {
		if output, ok := results[script]; ok && output != "" {
			appendix += fmt.Sprintf("\n\n--- Script output: %s ---\n%s", script, output)
		}
	}

	return rendered + appendix, nil
}

// InvokeFull 执行完整的 skill 调用流水线：
// hooks (pre-invoke) → 渲染 + 动态上下文 → hooks (post-invoke)
// scripts/ 目录仅作为 bundled resources 编目，不在普通调用中自动执行。
func (r *Registry) InvokeFull(ctx context.Context, name string, rctx RenderContext, executor ShellExecutor, runner *ScriptRunner, hookRunner *HookRunner) (string, error) {
	start := time.Now()
	result, err := r.invokeFull(ctx, name, rctx, executor, runner, hookRunner)
	if r.metrics != nil {
		r.metrics.RecordInvocation(name, time.Since(start), err)
	}
	return result, err
}

// invokeFull 是 InvokeFull 的内部实现
func (r *Registry) invokeFull(ctx context.Context, name string, rctx RenderContext, executor ShellExecutor, runner *ScriptRunner, hookRunner *HookRunner) (string, error) {
	s, err := r.Get(name)
	if err != nil {
		return "", err
	}

	// 运行 pre-invoke hooks
	if hookRunner != nil && s.Metadata.Hooks != nil {
		if err := hookRunner.RunPreInvoke(s.Metadata.Hooks, s.Path); err != nil {
			return "", err
		}
	}

	// 使用动态上下文渲染
	var rendered string
	if executor != nil {
		rendered, err = r.InvokeWithDynamicContext(name, rctx, executor)
	} else {
		rendered, err = r.Invoke(name, rctx)
	}
	if err != nil {
		return "", err
	}

	// 运行 post-invoke hooks
	if hookRunner != nil && s.Metadata.Hooks != nil {
		if err := hookRunner.RunPostInvoke(s.Metadata.Hooks, s.Path); err != nil {
			return rendered, err
		}
	}

	return rendered, nil
}

// SetToolBridge 设置 ToolBridge 用于工具执行
func (r *Registry) SetToolBridge(bridge *ToolBridge) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.toolBridge = bridge
}

// GetToolBridge 返回当前的 ToolBridge
func (r *Registry) GetToolBridge() *ToolBridge {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.toolBridge
}

// SetMetrics 设置指标收集器
func (r *Registry) SetMetrics(m *Metrics) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.metrics = m
}

// GetMetrics 返回当前的指标收集器
func (r *Registry) GetMetrics() *Metrics {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.metrics
}

// SetForkHandler 设置 ForkHandler 用于 context=fork 的 skill 隔离执行
func (r *Registry) SetForkHandler(handler ForkHandler) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.forkHandler = handler
}

// GetForkHandler 返回当前的 ForkHandler
func (r *Registry) GetForkHandler() ForkHandler {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.forkHandler
}

// InvokeChain 先调用 skill 的所有 depends-on 依赖，再调用 skill 本身。
// 返回所有输出拼接后的结果（依赖输出 + 主 skill 输出，以分隔线分隔）。
func (r *Registry) InvokeChain(ctx context.Context, name string, rctx RenderContext, executor ShellExecutor, runner *ScriptRunner, hookRunner *HookRunner) (string, error) {
	s, err := r.Get(name)
	if err != nil {
		return "", err
	}

	var parts []string

	// 先调用所有依赖
	for _, dep := range s.Metadata.DependsOn {
		depResult, depErr := r.InvokeFull(ctx, dep, rctx, executor, runner, hookRunner)
		if depErr != nil {
			return "", fmt.Errorf("dependency %q: %w", dep, depErr)
		}
		if depResult != "" {
			parts = append(parts, depResult)
		}
	}

	// 再调用 skill 本身
	result, err := r.InvokeFull(ctx, name, rctx, executor, runner, hookRunner)
	if err != nil {
		return "", err
	}
	parts = append(parts, result)

	return strings.Join(parts, "\n\n---\n\n"), nil
}

// listSkillPaths 返回所有已注册 skill 的目录路径（供 Watcher 使用）
func (r *Registry) listSkillPaths() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	paths := make([]string, 0, len(r.skills))
	for _, s := range r.skills {
		if s.Path != "" {
			paths = append(paths, s.Path)
		}
	}
	return paths
}

// nameByPath 根据目录路径查找 skill 名称（供 Watcher 使用）
func (r *Registry) nameByPath(path string) string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for key, s := range r.skills {
		if s.Path == path {
			return key.Name
		}
	}
	return ""
}

// compareSemver 比较两个 semver 字符串，返回 -1/0/+1。
// 空字符串按 0 处理；非法 semver 按字典序比较（保守回退）。
// 仅支持 major.minor.patch（忽略 pre-release）；足够区分热更新的版本递增。
func compareSemver(a, b string) int {
	if a == b {
		return 0
	}
	if a == "" {
		return -1
	}
	if b == "" {
		return 1
	}
	stripV := func(s string) string {
		if len(s) > 0 && (s[0] == 'v' || s[0] == 'V') {
			return s[1:]
		}
		return s
	}
	a = stripV(a)
	b = stripV(b)
	pa := strings.SplitN(a, ".", 4)
	pb := strings.SplitN(b, ".", 4)
	for i := 0; i < 3; i++ {
		var ai, bi int
		var err error
		if i < len(pa) {
			ai, err = strconv.Atoi(stripSuffix(pa[i]))
			if err != nil {
				return strings.Compare(a, b)
			}
		}
		if i < len(pb) {
			bi, err = strconv.Atoi(stripSuffix(pb[i]))
			if err != nil {
				return strings.Compare(a, b)
			}
		}
		if ai != bi {
			if ai < bi {
				return -1
			}
			return 1
		}
	}
	return 0
}

// stripSuffix 去除 pre-release / metadata 后缀：1.2.3-rc1 → 1.2.3 → part "3-rc1" → "3"
func stripSuffix(s string) string {
	if i := strings.IndexAny(s, "-+"); i > 0 {
		return s[:i]
	}
	return s
}

// loadSkillFromDir 扫描目录中的 SKILL.md，返回已解析的 Skill（Level 1）。
// scope/userID 由 caller 注入（RegisterFromPath 或 install 场景）。
func loadSkillFromDir(dir string) (*Skill, error) {
	skillFile := filepath.Join(dir, "SKILL.md")
	info, err := os.Stat(skillFile)
	if err != nil {
		return nil, errs.Wrap(errs.CodeSkillLoadFailed, fmt.Sprintf("stat %s", skillFile), err)
	}
	if info.IsDir() {
		return nil, errs.New(errs.CodeSkillLoadFailed, fmt.Sprintf("%s is a directory", skillFile))
	}
	skill, err := discoverMetadataOnly(skillFile, dir)
	if err != nil {
		return nil, err
	}
	if err := ValidateName(skill.Metadata.Name); err != nil {
		return nil, err
	}
	return skill, nil
}
