# Agent 定时任务系统方案

> 日期: 2026-05-05
> 修订: 2026-05-08
> 状态: READY TO IMPLEMENT after plan-ceo-review / plan-eng-review / plan-design-review / D2-D11 fixes
> 核心结论: 本期做通用用户级定时任务系统,但不按旧版"长事务 advisory lock + 一次性 rename + skill 预留 + 不存在的前端组件"执行。本文件是修订后的唯一事实源。

## 1. 业务场景

`scheduled_pushes` 生产 0 行说明旧能力没有形成真实使用入口。本期价值不再定义为"补一个 cron UI",而是让用户能把 Agent 变成可靠的后台工人。

本期用这两个真实场景验收:

1. **每日质量巡检**: 用户创建一个 `session` 定时任务,每天 09:00 自动新建 Web Session,执行"检查当前仓库测试/质量风险并生成报告"。用户登录后台能看到新 session、执行结果和失败原因。
2. **团队例行播报**: 用户创建一个 `im_push` 定时任务,每个工作日上午向飞书群推送固定模板或摘要。旧 `/api/v1/channels/push/schedules` 行为保持兼容。

非目标: 本期不做任务依赖图、模板市场、Webhook target、Skill target、prompt 加密、移动端完整管理 UI。

## 2. 当前代码证据

| 现状 | 代码位置 | 结论 |
|---|---|---|
| `scheduled_pushes` 表已存在 | `internal/store/postgres_migrate.go` | 物理表本期继续沿用,不立即 rename |
| 旧 push schedule API 只有 POST/GET/DELETE | `internal/api/push_schedule_handler.go` | 作为 alias 保留,内部委托新实现 |
| `cron.go` 仅支持 `Interval time.Duration` | `internal/master/cron.go` | 原地升级,不重写调度器 |
| `restoreFeishuPushSchedules` 启动恢复已存在 | `internal/bootstrap/helpers.go` | 替换为通用 scheduled task reload |
| `auth.WithUser(ctx, *auth.User)` 是真实接口 | `internal/auth/middleware.go` | session target 必须注入完整用户对象 |
| `SessionRequest` 不含 `OwnerUserID` | `internal/master/session.go` | user 只能从 context 传入 |
| 前端没有 `components/ui/Table/Sheet/Badge` | `frontend/src/components/` | Phase 5 必须按现有组件/样式实现 |
| 前端没有 `react-hook-form` | `frontend/package.json` | 本期不用它,使用受控表单 |

## 3. 本期范围

### 3.1 做什么

1. 新增 `/api/v1/scheduled-tasks` 用户级 CRUD API。
2. 支持两种 target:
   - `im_push`: 复用 `internal/channel/push.Service`。
   - `session`: 在任务 owner 名下创建/执行 Web Session。
3. 支持两种 schedule:
   - `interval_sec`: 保留旧能力。
   - `cron_expr` + `timezone`: 标准 5 字段 cron + IANA 时区。
4. 任务和 run history 持久化到 PostgreSQL。
5. 多实例下同一任务同一 tick 只执行一次。
6. 前端新增桌面版定时任务管理页。
7. 最近 run history 可查看,高频数据用周分区保留 4 周。

### 3.2 不做什么

- 不新增 `target_type='skill'`。CHECK 约束只允许 `im_push` / `session`。
- 不新增 dispatcher 注册表。两个 target 用显式 `switch`。
- 不做物理表 rename。`scheduled_pushes` 作为物理表保留,新代码可以用 `ScheduledTask` 类型表达语义。
- 不用长事务包住 Agent 执行。
- 不依赖不存在的 `frontend/src/components/ui/*`。
- 不引入 `react-hook-form`。
- 不做 mobile table-to-card 体验。`<768px` 显示桌面使用提示。

## 4. 数据模型

### 4.1 物理表策略

本期不执行 `ALTER TABLE scheduled_pushes RENAME TO scheduled_tasks`。原因:

1. 滚动部署期间旧进程仍会访问 `scheduled_pushes`。
2. 当前仓库迁移集中在 `pgInitSQL`,没有成熟版本化 migration runner。
3. 表名不影响 API/Go 类型语义,rename 可以等真实使用稳定后作为独立 cleanup。

### 4.2 `scheduled_pushes` 演进

在 `internal/store/postgres_migrate.go` 的 `CREATE TABLE IF NOT EXISTS scheduled_pushes` 中新增列,并给已有库补 `ALTER TABLE ... ADD COLUMN IF NOT EXISTS` 的初始化逻辑。不要删除旧列。

```sql
ALTER TABLE scheduled_pushes
  ADD COLUMN IF NOT EXISTS description TEXT NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS target_type TEXT NOT NULL DEFAULT 'im_push',
  ADD COLUMN IF NOT EXISTS target_config JSONB NOT NULL DEFAULT '{}'::jsonb,
  ADD COLUMN IF NOT EXISTS cron_expr TEXT NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS timezone TEXT NOT NULL DEFAULT 'UTC',
  ADD COLUMN IF NOT EXISTS active_run_id TEXT NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS lease_expires_at TIMESTAMPTZ;

DO $$
BEGIN
  IF NOT EXISTS (
    SELECT 1
    FROM pg_constraint
    WHERE conname = 'scheduled_pushes_target_type_check'
      AND conrelid = 'scheduled_pushes'::regclass
  ) THEN
    ALTER TABLE scheduled_pushes
      ADD CONSTRAINT scheduled_pushes_target_type_check
      CHECK (target_type IN ('im_push', 'session'));
  END IF;

  IF NOT EXISTS (
    SELECT 1
    FROM pg_constraint
    WHERE conname = 'scheduled_pushes_schedule_check'
      AND conrelid = 'scheduled_pushes'::regclass
  ) THEN
    ALTER TABLE scheduled_pushes
      ADD CONSTRAINT scheduled_pushes_schedule_check
      CHECK (
        (cron_expr <> '' AND interval_sec = 0)
        OR
        (cron_expr = '' AND interval_sec > 0)
      );
  END IF;
END $$;

COMMENT ON COLUMN scheduled_pushes.last_error IS
  '最近一次 run 的最终错误,仅用于 UI 展示,不驱动调度逻辑';

CREATE INDEX IF NOT EXISTS idx_scheduled_pushes_user_enabled
  ON scheduled_pushes(created_by, enabled, next_run_at);

-- 创建唯一索引前必须先做 preflight。若返回任何行,迁移 fail-fast,
-- 由人工清理重复 name 后再重跑,不要自动改用户可见数据。
SELECT created_by, name, COUNT(*)
FROM scheduled_pushes
GROUP BY created_by, name
HAVING COUNT(*) > 1;

CREATE UNIQUE INDEX IF NOT EXISTS idx_scheduled_pushes_user_name
  ON scheduled_pushes(created_by, name);
```

说明:

- `platform` 保留,仅 `target_type='im_push'` 时使用。
- `target_config` 用于 target 参数。`im_push` 存 `platform/chat_id/open_id/msg_type/template/vars/idempotency_key`; `session` 存 `session_name`。
- `active_run_id` 和 `lease_expires_at` 用于短事务 claim,避免同一任务并发执行。
- `pg_constraint` guard 必须同时匹配 `conname` 和 `conrelid`,防止其他表同名约束导致迁移误判。
- `(created_by, name)` 唯一索引前必须做重复 preflight,发现重复时返回明确迁移错误,不自动重命名。

### 4.3 Run history 分区表

`scheduled_task_runs` 按 `scheduled_at` 周分区。不要做大批量 DELETE GC。

```sql
CREATE TABLE IF NOT EXISTS scheduled_task_runs (
    scheduled_at     TIMESTAMPTZ NOT NULL,
    id               TEXT NOT NULL,
    task_id          TEXT NOT NULL REFERENCES scheduled_pushes(id) ON DELETE CASCADE,
    started_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    finished_at      TIMESTAMPTZ,
    status           TEXT NOT NULL CHECK (status IN ('running','succeeded','failed','timeout','skipped')),
    attempt_count    INTEGER NOT NULL DEFAULT 0,
    output           TEXT NOT NULL DEFAULT '',
    error            TEXT NOT NULL DEFAULT '',
    session_id       TEXT NOT NULL DEFAULT '',
    claimed_by       TEXT NOT NULL DEFAULT '',
    claim_expires_at TIMESTAMPTZ,
    PRIMARY KEY (scheduled_at, id),
    UNIQUE (scheduled_at, task_id)
) PARTITION BY RANGE (scheduled_at);
```

每周创建一个分区:

```sql
CREATE TABLE IF NOT EXISTS scheduled_task_runs_2026_w19
  PARTITION OF scheduled_task_runs
  FOR VALUES FROM ('2026-05-04') TO ('2026-05-11');

CREATE INDEX IF NOT EXISTS idx_scheduled_task_runs_2026_w19_task_started
  ON scheduled_task_runs_2026_w19(task_id, scheduled_at DESC);
```

历史保留 4 周。GC 每周一 03:00 创建下周分区,并 `DROP TABLE IF EXISTS scheduled_task_runs_<old_week>`。

写入 run history 前不能只依赖 GC 预创建分区。新增:

```go
EnsureScheduledTaskRunPartition(ctx context.Context, scheduledAt time.Time) error
```

要求:

- `ClaimDueScheduledTaskRun` 和 `ClaimManualScheduledTaskRun` 在插入 `scheduled_task_runs` 前都必须先调用。
- 分区名按 ISO week 生成,例如 `scheduled_task_runs_2026_w19`。
- 建表和建索引必须幂等。并发 ensure 同一周分区时,用短事务 advisory lock 或等价互斥保护 DDL,不要影响任务执行事务。
- GC 只负责提前创建下周分区和删除 4 周前分区,不是写入安全性的唯一保障。

### 4.4 Go 类型

```go
type ScheduledTask struct {
    ID             string         `json:"id"`
    Name           string         `json:"name"`
    Description    string         `json:"description,omitempty"`
    TargetType     string         `json:"target_type"` // im_push | session
    TargetConfig   map[string]any `json:"target_config"`
    Platform       string         `json:"platform,omitempty"`
    Prompt         string         `json:"prompt"`
    CronExpr       string         `json:"cron_expr,omitempty"`
    IntervalSec    int            `json:"interval_sec,omitempty"`
    Timezone       string         `json:"timezone"`
    Enabled        bool           `json:"enabled"`
    CreatedBy      string         `json:"created_by"`
    LastRunAt      *time.Time     `json:"last_run_at,omitempty"`
    NextRunAt      *time.Time     `json:"next_run_at,omitempty"`
    LastError      string         `json:"last_error,omitempty"`
    ActiveRunID    string         `json:"active_run_id,omitempty"`
    LeaseExpiresAt *time.Time     `json:"lease_expires_at,omitempty"`
    CreatedAt      time.Time      `json:"created_at"`
    UpdatedAt      time.Time      `json:"updated_at"`
}

type ScheduledTaskRun struct {
    ScheduledAt    time.Time  `json:"scheduled_at"`
    ID             string     `json:"id"`
    TaskID         string     `json:"task_id"`
    StartedAt      time.Time  `json:"started_at"`
    FinishedAt     *time.Time `json:"finished_at,omitempty"`
    Status         string     `json:"status"`
    AttemptCount   int        `json:"attempt_count"`
    Output         string     `json:"output"`
    Error          string     `json:"error"`
    SessionID      string     `json:"session_id,omitempty"`
    ClaimedBy      string     `json:"claimed_by,omitempty"`
    ClaimExpiresAt *time.Time `json:"claim_expires_at,omitempty"`
}
```

## 5. 调度与执行模型

### 5.1 不使用长事务 advisory lock

旧方案的问题: `session` target 可能运行几分钟到 30 分钟。如果用 `pg_try_advisory_xact_lock` 包住整个 callback,数据库事务会跟 Agent 执行一样长,容易占连接、阻塞 vacuum、放大故障。

本期改为 **短事务 claim + 事务外执行 + 短事务 finalize**。

```
cron / interval tick
  |
  v
短事务 ClaimDueScheduledTaskRun
  - row lock scheduled_pushes
  - 检查 enabled / next_run_at <= now / active_run_id 是否空或 lease 过期
  - 写 active_run_id + lease_expires_at
  - 插入 scheduled_task_runs(status=running)
  - 计算并写下一次 next_run_at
  |
  v
提交事务
  |
  v
执行 im_push 或 session,最多 30 分钟
  |
  v
短事务 FinishScheduledTaskRun
  - status=succeeded/failed/timeout
  - 写 output/error/session_id
  - 清 active_run_id
  - 写 last_error
```

### 5.2 Claim SQL 形态

`ClaimDueScheduledTaskRun` 必须是一个短事务。

调用前先计算本次 `scheduled_at=task.next_run_at`,并调用 `EnsureScheduledTaskRunPartition(ctx, scheduledAt)`。分区 ensure 失败时不要进入 claim SQL,直接记录 metric/log 并返回错误。

```sql
WITH due AS (
  SELECT id, next_run_at AS scheduled_at
  FROM scheduled_pushes
  WHERE id = $1
    AND enabled = TRUE
    AND next_run_at IS NOT NULL
    AND next_run_at <= $2
    AND (active_run_id = '' OR lease_expires_at IS NULL OR lease_expires_at < $2)
    AND NOT EXISTS (
      SELECT 1
      FROM scheduled_task_runs r
      WHERE r.task_id = scheduled_pushes.id
        AND r.scheduled_at = scheduled_pushes.next_run_at
    )
  FOR UPDATE SKIP LOCKED
),
inserted AS (
  INSERT INTO scheduled_task_runs (
  scheduled_at, id, task_id, status, attempt_count, claimed_by, claim_expires_at
  )
  SELECT scheduled_at, $3, id, 'running', 0, $6, $4
  FROM due
  ON CONFLICT (scheduled_at, task_id) DO NOTHING
  RETURNING scheduled_at, id, task_id
),
updated AS (
  UPDATE scheduled_pushes sp
  SET active_run_id = inserted.id,
      lease_expires_at = $4,
      last_run_at = $2,
      next_run_at = $5,
      updated_at = NOW()
  FROM inserted
  WHERE sp.id = inserted.task_id
  RETURNING sp.*
)
SELECT inserted.scheduled_at, inserted.id, inserted.task_id
FROM inserted
JOIN updated ON updated.id = inserted.task_id;
```

如果返回 0 行,说明别的实例已经 claim,或者任务已禁用/未到点/仍在运行。本实例直接跳过。

### 5.3 手动 run-now

`run-now` 不复用 `ClaimDueScheduledTaskRun` 的 `next_run_at <= now` 条件。新增:

```go
ClaimManualScheduledTaskRun(ctx context.Context, taskID string, now time.Time, runID string, leaseUntil time.Time, claimedBy string) (*ScheduledTaskRun, error)
```

语义:

- `scheduled_at=now.UTC()`。
- 仍检查 `enabled=true`、owner 权限、`active_run_id` 为空或 lease 已过期。
- 插入 run 前调用 `EnsureScheduledTaskRunPartition(ctx, now)`。
- 不修改 `next_run_at`,手动执行不能推迟或提前下一次自动触发。
- 返回 0 行时表示仍在运行/被其他实例 claim/任务被禁用,API 返回 409 或可恢复错误,不要静默成功。
- 之后执行与 finalize 完全复用普通 tick 的 dispatcher 路径。

### 5.4 Finalize 顺序

`FinishScheduledTaskRun` 必须是一个短事务,顺序固定:

1. 按 `(scheduled_at, run_id)` 更新 `scheduled_task_runs` 自己的 `status/output/error/session_id/finished_at/attempt_count`。
2. 按 `(task_id, active_run_id)` 条件清理 `scheduled_pushes.active_run_id/lease_expires_at/last_error`。
3. 第二步影响 0 行不是错误,说明旧 run 超时后已有新 run 接管。此时 run history 仍必须保留最终状态,但不能清空新 run 的 lease。

清理 task lease 时必须带 run id 防旧 run 覆盖新 run:

```sql
UPDATE scheduled_pushes
SET active_run_id = '',
    lease_expires_at = NULL,
    last_error = $3,
    updated_at = NOW()
WHERE id = $1
  AND active_run_id = $2;
```

如果这条 UPDATE 影响 0 行,仍然更新 `scheduled_task_runs` 自己的 status/output/error,但不要清空 task 上的 `active_run_id`。这说明旧 run 已超时后被新 run 接管。

### 5.5 Retry 与 schedule 解耦

每次 schedule tick 只创建一条 run。run 内部最多重试 3 次,退避 1m/5m/15m。下一个 tick 是新 run,不继承上一次 run 的 retry 状态。

连续失败自动禁用规则:

- 不在 dispatcher 内联禁用。
- 在 finalize 或 GC 时检查最近 5 次 run。
- 只有 `len(recentRuns) == 5` 且 5 次全是 `failed` 或 `timeout` 时,设置 `enabled=false`,写 `last_error="最近 5 次执行均失败,已自动停用"`。
- 最近 run 少于 5 条时绝不自动停用,避免新任务第一次失败就被关闭。

### 5.6 Timeout

每个 run 使用 `context.WithTimeout(ctx, 30*time.Minute)`。超时写:

- `scheduled_task_runs.status='timeout'`
- `scheduled_task_runs.error='scheduled task timed out after 30m'`
- `scheduled_pushes.last_error` 同步为该错误
- 清空 `active_run_id`

### 5.7 Schedule 计算

新增:

```go
type ScheduleSpec struct {
    Interval time.Duration
    CronExpr string
    Timezone string
}
```

规则:

- `Interval > 0` 与 `CronExpr != ""` 互斥。
- `CronExpr` 使用 `github.com/robfig/cron/v3` 标准 5 字段 parser。
- `Timezone` 必须能 `time.LoadLocation`。
- 普通用户 `interval_sec >= 60`。admin 可通过配置放宽到最低 10 秒,仅用于运维或测试。
- cron 表达式预解析后必须拒绝等价高频调度,即下一次和再下一次触发间隔低于当前用户最小 interval 时返回 validation error。
- 创建/更新任务时预计算 `next_run_at`。
- 每次 claim 时基于当前 task spec 计算下一次 `next_run_at`。

### 5.8 可观测性

首版必须复用现有 zap logger 和 `Master.enqueueMetric`,不做 dashboard。至少记录:

| Metric / log | Labels | 触发 |
|---|---|---|
| `scheduled_task.claim_total` | `result=claimed/skipped/error`, `target_type` | due/manual claim 返回 |
| `scheduled_task.run_total` | `status=succeeded/failed/timeout`, `target_type` | finalize |
| `scheduled_task.reload_total` | `result=ok/failed` | bootstrap reload 单任务结果 |
| `scheduled_task.partition_ensure_total` | `result=ok/failed` | ensure 分区 |
| structured log `scheduled task claim skipped` | `task_id`, `reason`, `run_id` | claim 返回 0 行 |
| structured log `scheduled task run finalized` | `task_id`, `run_id`, `status`, `lease_cleared` | finalize |

## 6. Target 执行

### 6.1 `im_push`

不要调用不存在的 `push.SendChannelMessage`。使用当前真实服务:

```go
type scheduledPushService interface {
    Push(ctx context.Context, req push.Request) error
    DispatchScheduledPrompt(ctx context.Context, prompt string) error
}
```

兼容两种输入:

1. 老记录: `target_config={}` 且 `prompt` 以 `scheduled_push:` 开头时,调用 `DispatchScheduledPrompt(ctx, task.Prompt)`。
2. 新记录: 从 `target_config` 组装 `push.Request`,调用 `Push(ctx, req)`。

### 6.2 `session`

session target 必须在 owner 身份下执行。当前真实接口是:

- `auth.WithUser(ctx, *auth.User)`
- `auth.Engine.GetUserByID(ctx, userID)`
- `SessionRequest{SessionID, Input}`
- `SessionManager.ProcessRequestWithResponse(ctx, req)`

新增一个用户解析依赖,不要在 `Master` 里假设存在 `m.authStore.GetUser`。

```go
type scheduledTaskUserResolver interface {
    GetUserByID(ctx context.Context, userID string) (*auth.User, error)
}
```

执行逻辑:

```go
func (m *Master) dispatchScheduledTask(ctx context.Context, task ScheduledTask, runID string) (sessionID string, output string, err error) {
    user, err := m.scheduledTaskUserResolver.GetUserByID(ctx, task.CreatedBy)
    if err != nil {
        return "", "", fmt.Errorf("scheduled task %s owner lookup failed: %w", task.ID, err)
    }
    if user == nil || user.Status != "active" {
        return "", "", fmt.Errorf("scheduled task %s owner is missing or inactive", task.ID)
    }
    ctx = auth.WithUser(ctx, user)

    switch task.TargetType {
    case "im_push":
        return m.dispatchScheduledIMPush(ctx, task)
    case "session":
        sessionID := fmt.Sprintf("scheduled-%s-%s", task.ID, runID)
        resp, err := m.sessionMgr.ProcessRequestWithResponse(ctx, SessionRequest{
            SessionID: sessionID,
            Input:     task.Prompt,
        })
        return sessionID, resp.Content, err
    default:
        return "", "", fmt.Errorf("unsupported target_type: %s", task.TargetType)
    }
}
```

注意:

- `SessionRequest` 不传 `OwnerUserID`。
- user 不存在时 fail-fast,写 run error,不 fallback admin。
- session 的 `UserID` 通过 `auth.UserIDFrom(ctx)` 写入现有 session 创建路径。

## 7. API

### 7.1 新 endpoints

| Method | Path | 说明 |
|---|---|---|
| POST | `/api/v1/scheduled-tasks` | 新建任务,`created_by=auth.UserFrom(ctx).ID` |
| GET | `/api/v1/scheduled-tasks` | 当前用户任务 |
| GET | `/api/v1/scheduled-tasks/{id}` | 当前用户任务详情,跨用户返回 404 |
| PUT | `/api/v1/scheduled-tasks/{id}` | 修改任务,跨用户返回 404 |
| DELETE | `/api/v1/scheduled-tasks/{id}` | 删除任务,跨用户返回 404 |
| POST | `/api/v1/scheduled-tasks/{id}/toggle` | 启停任务 |
| POST | `/api/v1/scheduled-tasks/{id}/run-now` | 手动触发一次 |
| GET | `/api/v1/scheduled-tasks/{id}/runs?limit=20` | 最近执行历史 |
| GET | `/api/v1/admin/scheduled-tasks` | admin 列出全部 |

### 7.2 旧接口兼容

旧接口保留 3 个月:

- `POST /api/v1/channels/push/schedules`
- `GET /api/v1/channels/push/schedules`
- `DELETE /api/v1/channels/push/schedules/{id}`

兼容要求:

- 响应结构保持旧测试期望。
- 内部转成 `target_type='im_push'`。
- 增加 `Deprecation: true` header。
- 旧接口仍要求原来的 admin + `push:write` 权限。

### 7.3 用户隔离

普通用户:

- list 只查 `created_by = user.ID`
- get/update/delete/run-now/toggle 先按 id 查,再校验 owner;不匹配返回 404,不泄漏存在性

Admin:

- `/api/v1/admin/scheduled-tasks` 可看全部
- 普通 `/api/v1/scheduled-tasks` 仍按当前用户过滤

### 7.4 输入校验

创建/更新时校验:

- `name`: trim 后非空,同一 `created_by` 下唯一
- `target_type`: 只能 `im_push` / `session`
- `prompt`: trim 后非空,最大 8000 字符
- `interval_sec` 与 `cron_expr` 二选一
- 普通用户 `interval_sec >= 60`; admin 可配置最低 10 秒
- `cron_expr`: robfig cron 预解析通过
- `cron_expr`: 预计算连续两次触发间隔,低于当前用户最小 interval 时拒绝
- `timezone`: `time.LoadLocation` 通过,默认浏览器时区或 `UTC`
- `target_config`: 按 target type 校验必填字段
- per-user 任务上限 100,admin 不受限

配额错误体:

```json
{
  "error": "scheduled_tasks_quota_exceeded",
  "code": 429,
  "limit": 100,
  "current": 100,
  "message": "已达到每用户 100 个定时任务上限,请删除旧任务或联系管理员"
}
```

## 8. 后端实施阶段

### Phase 1: Store + schema

Files:

- Modify: `internal/store/postgres_migrate.go`
- Modify: `internal/store/types.go`
- Modify: `internal/store/postgres.go`
- Modify: `internal/store/memory_store.go`
- Create: `internal/store/scheduled_task_store_test.go`

Tasks:

- [ ] 在 `scheduled_pushes` 上新增字段,不 rename。
- [ ] 新增 `scheduled_task_runs` 分区表初始化与当前周/下周分区创建。
- [ ] 约束迁移 guard 用 `conrelid = 'scheduled_pushes'::regclass`,并测试同名约束在其他表存在时不误判。
- [ ] 创建 `(created_by, name)` 唯一索引前做重复 preflight,发现重复 fail-fast,不自动改用户数据。
- [ ] 新增 `ScheduledTask` / `ScheduledTaskRun` 类型。
- [ ] 新增 store 方法:
  - `SaveScheduledTask`
  - `GetScheduledTask`
  - `ListScheduledTasksByUser`
  - `ListAllScheduledTasks`
  - `ListEnabledScheduledTasks`
  - `DeleteScheduledTask`
  - `EnsureScheduledTaskRunPartition`
  - `ClaimDueScheduledTaskRun`
  - `ClaimManualScheduledTaskRun`
  - `FinishScheduledTaskRun`
  - `ListScheduledTaskRuns`
  - `BulkMarkScheduledTaskReloadFailures`
- [ ] 保留旧 `SaveScheduledPush/GetScheduledPush/...` 方法,内部可委托新 store。
- [ ] `ClaimDueScheduledTaskRun` / `ClaimManualScheduledTaskRun` 插入 run 前都必须确保对应周分区存在。
- [ ] `FinishScheduledTaskRun` 单事务先更新 run row,再按 `active_run_id` 条件清 task lease;清 lease 0 行不是错误。
- [ ] 单测覆盖 CRUD、用户隔离、唯一 name、claim 竞态、manual claim、partition ensure、finish 清 lease、旧 run 被新 run 接管。

### Phase 2: cron + bootstrap reload

Files:

- Modify: `internal/master/cron.go`
- Modify: `internal/master/cron_test.go`
- Modify: `internal/bootstrap/helpers.go`
- Modify: `internal/bootstrap/server.go`
- Add dependency: `github.com/robfig/cron/v3`

Tasks:

- [ ] 增加 `ScheduleSpec`。
- [ ] `CronCreate` 支持 interval 和 cron expr。
- [ ] `runCronJob` 不吞错误,至少记录 error log。
- [ ] 校验最小 interval:普通用户 60s,admin 配置可低至 10s;cron 等价高频调度要拒绝。
- [ ] `restoreFeishuPushSchedules` 替换为 `restoreScheduledTasksAsync`。
- [ ] reload 异步执行,健康检查不等待。
- [ ] 单条任务 reload 失败不影响其他任务。
- [ ] reload 失败批量写 `last_error` 并禁用该任务。
- [ ] emit 最小 metrics/logs: claim、run finalize、reload、partition ensure。
- [ ] 测试 interval 兼容、cron 时区、非法 cron、reload 不阻塞。

### Phase 3: dispatcher

Files:

- Create: `internal/master/scheduled_task_dispatch.go`
- Modify: `internal/master/master.go`
- Modify: `internal/bootstrap/server.go`

Tasks:

- [ ] 新增 `scheduledTaskUserResolver` 和 `scheduledPushService` 小接口。
- [ ] `Master` 新增 `SetScheduledTaskUserResolver` 和 `SetScheduledTaskPushService`,不要修改 `NewMaster` 签名。
- [ ] bootstrap 通过上述 setter 把 `authEngine` 和 `pushService` 注入 Master。
- [ ] `im_push` 分支兼容旧 `scheduled_push:` prompt 和新 `target_config`。
- [ ] `session` 分支通过 `auth.WithUser(ctx, user)` 注入身份。
- [ ] user resolver 或 push service 未注入时返回明确错误,不要 panic。
- [ ] unsupported target 返回明确错误。
- [ ] 单测覆盖 owner missing/inactive、im_push、session、unsupported target。

### Phase 4: REST API

Files:

- Create: `internal/api/scheduled_tasks_handler.go`
- Create: `internal/api/scheduled_tasks_handler_test.go`
- Modify: `internal/api/routes.go`
- Modify: `internal/api/push_schedule_handler.go`
- Modify: `frontend/src/types/api.ts` after frontend contract lands

Tasks:

- [ ] 实现 8 个用户 API + 1 个 admin API。
- [ ] 旧 push schedule handler 委托新逻辑。
- [ ] 创建/更新时同步内存 cron: 先写 DB,再 `Master.CronCreate`;失败则回滚或禁用并返回 400。
- [ ] 删除时先 DB delete,再 `StopCron("scheduled-task-"+id)`。
- [ ] `run-now` 调用 `ClaimManualScheduledTaskRun`,执行/finalize 复用普通路径,`scheduled_at=now`,不修改 `next_run_at`。
- [ ] 旧接口响应增加 `Deprecation: true` header,旧测试期望保持不变。
- [ ] handler 单测覆盖权限、跨用户 404、429、cron 校验、旧接口回归。

### Phase 5: history partition GC

Files:

- Create: `internal/master/scheduled_task_history_gc.go`
- Create: `internal/master/scheduled_task_history_gc_test.go`

Tasks:

- [ ] 每周一 03:00 创建下周分区。
- [ ] Drop 4 周前分区。
- [ ] `CREATE TABLE IF NOT EXISTS` 和 `DROP TABLE IF EXISTS` 必须幂等。
- [ ] 测试 week key、分区 SQL、重复运行幂等。

## 9. 前端 UI

### 9.1 路由与入口

Files:

- Create: `frontend/src/pages/ScheduledTasks.tsx`
- Create: `frontend/src/components/scheduled-tasks/TaskTable.tsx`
- Create: `frontend/src/components/scheduled-tasks/TaskForm.tsx`
- Create: `frontend/src/components/scheduled-tasks/RunHistoryDrawer.tsx`
- Create: `frontend/src/store/scheduledTasks.ts`
- Modify: `frontend/src/api/node-client.ts`
- Modify: `frontend/src/App.tsx`
- Modify: `frontend/src/layouts/AdminSidebar.tsx`
- Modify: `frontend/src/i18n/locales/en.json`
- Modify: `frontend/src/i18n/locales/zh.json`
- Add dependency: `cronstrue`

Route:

- `/admin/scheduled-tasks`
- Sidebar label key: `nav.adminScheduledTasks`
- Icon: `CalendarClock` from `lucide-react`

### 9.2 组件依赖

仓库没有 `frontend/src/components/ui/Table.tsx` / `Sheet.tsx` / `Badge.tsx`,也没有 `react-hook-form`。本期前端按以下规则实现:

- 表格: 在 `TaskTable.tsx` 用原生 `<table>` + DESIGN.md token class。
- drawer: 在 `RunHistoryDrawer.tsx` 实现本地 fixed right panel,`role="dialog"`,不新增 Sheet primitive。
- badge/button/select: 可以复用 `frontend/src/components/ai-elements/ui/badge.tsx`、`button.tsx`、`select.tsx` 这些 leaf primitives,业务状态逻辑留在 `scheduled-tasks/` 组件内。
- 表单: 使用 React `useState` 受控表单,不引入 `react-hook-form`。
- cron 翻译: 新增 `cronstrue`。

### 9.3 UI 状态

五态必须实现:

| 状态 | 渲染 |
|---|---|
| loading | 3 行 skeleton |
| empty | lucide `CalendarPlus`,标题"还没有定时任务",CTA"新建任务" |
| success | table |
| error | 红条"加载失败" + 重试 |
| partial | 黄条"调度服务异常,N 个任务可能未按时执行" |

### 9.4 表格规格

列:

- enabled: 40px switch
- name: flex, truncate + title tooltip
- schedule: 200px,显示 cronstrue 文案 + timezone
- target: 140px,`im_push` / `session`
- last run: 120px,成功/失败/运行中/从未
- actions: 60px,MoreVertical 菜单

点击行展开最近 5 条 runs。完整历史从菜单打开右侧 drawer,分页 20 条。

### 9.5 a11y 与响应式

- `<table role="table">`,所有 `<th>` 加 `scope="col"`。
- 行可 keyboard focus。
- `Enter` 展开行。
- `E` 编辑,`R` 立即运行,`Delete` 删除并弹确认。
- run-now 状态变化写入 `aria-live="polite"`。
- `<768px` 不渲染主表,显示"请在桌面浏览器使用定时任务管理"。

## 10. 测试矩阵

### 10.1 后端

```text
store:
  SaveScheduledTask
  (created_by, name) unique
  migration constraint guard scopes pg_constraint by conrelid
  migration duplicate (created_by, name) preflight fails clearly
  EnsureScheduledTaskRunPartition creates current/manual scheduled_at week
  EnsureScheduledTaskRunPartition is idempotent under concurrent callers
  ListScheduledTasksByUser only returns owner rows
  ClaimDueScheduledTaskRun only one caller wins
  ClaimDueScheduledTaskRun skips active non-expired lease
  ClaimDueScheduledTaskRun ensures partition before insert
  ClaimManualScheduledTaskRun uses scheduled_at=now
  ClaimManualScheduledTaskRun does not modify next_run_at
  ClaimManualScheduledTaskRun returns conflict while active lease exists
  FinishScheduledTaskRun updates run row before clearing task lease
  FinishScheduledTaskRun clears active_run_id
  FinishScheduledTaskRun records old run result without clearing new active_run_id after takeover
  ListScheduledTaskRuns returns newest first
  auto-disable requires exactly 5 recent failed/timeout runs
  1-4 failed/timeout runs do not auto-disable

cron:
  interval path still works
  normal user interval below 60s is rejected
  admin configured interval below 10s is rejected
  cron_expr 0 9 * * * with Asia/Shanghai computes next run
  cron_expr equivalent high-frequency schedule is rejected
  invalid timezone returns validation error
  invalid cron returns validation error
  reload failure disables only bad task
  reload emits scheduled_task.reload_total

dispatcher:
  im_push old scheduled_push prompt works
  im_push target_config works
  session injects auth.WithUser
  missing owner fails fast
  inactive owner fails fast
  nil scheduledTaskUserResolver returns explicit error
  nil scheduledPushService returns explicit error
  dispatch emits run_total status metrics

api:
  create/list/get/update/delete/toggle/run-now/runs
  cross-user get/update/delete returns 404
  cross-user run-now/toggle/runs returns 404
  per-user quota returns 429 standard body
  run-now before next_run_at executes immediately
  run-now active lease conflict returns recoverable error
  old push schedule API returns Deprecation header
  old push schedule API still passes existing tests

gc:
  create current/next partition idempotent
  drop old partition idempotent
  missing partition during claim is handled by EnsureScheduledTaskRunPartition, not by GC timing

observability:
  claim_total result=claimed/skipped/error
  run_total status=succeeded/failed/timeout
  partition_ensure_total result=ok/failed
  structured finalize log includes lease_cleared
```

Commands:

```bash
env GOCACHE=/tmp/go-build go test ./internal/store ./internal/master ./internal/api ./internal/bootstrap -run 'Scheduled|Cron|PushSchedule' -count=1
env GOCACHE=/tmp/go-build go test ./internal/channel/push -count=1
```

### 10.2 前端

```bash
cd frontend && npm test -- --run src
cd frontend && npm run lint
cd frontend && npm run build
```

E2E acceptance:

1. 创建 interval `session` task。
2. list 能看到。
3. 在 `next_run_at` 到达前点击 run-now,仍立即出现 running/succeeded run,且下一次自动触发时间不被改写。
4. history drawer 能看到 output/session_id。
5. 删除后 list 不再显示。
6. 用户 B 拿用户 A 的 id 请求返回 404。
7. 连续 1-4 次失败不自动停用,第 5 次失败后 disabled 且展示明确 last_error。
8. 调度服务异常时页面进入 partial state,可重试加载。

## 11. 验收标准

| 场景 | 期望 |
|---|---|
| 用户 A 创建任务 | 用户 A list 可见 |
| 用户 B 请求 A 的任务 | 404 |
| admin list | 可见全部任务 |
| `0 9 * * *` + `Asia/Shanghai` | 下一次触发是北京时间 09:00 |
| 两个 master 同时到点 | 只有一个 `ClaimDueScheduledTaskRun` 成功 |
| 用户手动 run-now | 走 `ClaimManualScheduledTaskRun`,不改变 `next_run_at` |
| 上次 run 还在 active lease 内 | 新 tick skipped,不并发执行 |
| 旧 run 超时后新 run 接管 | 旧 run finalize 只更新自己的 run row,不清新 run lease |
| `im_push` 旧 prompt | 继续走 `DispatchScheduledPrompt` |
| `session` target | Web 里出现 `scheduled-{taskID}-{runID}` session |
| owner 不存在或 disabled | run failed,不 fallback admin |
| 最近失败 run 少于 5 条 | 不自动停用 |
| 最近 5 条 run 全失败/timeout | 自动 disabled + last_error |
| cron reload 坏任务 | 坏任务 disabled + last_error,其他任务继续 |
| run history 周分区缺失 | claim 前 ensure 分区,不会因缺分区插入失败 |
| run history 超 4 周 | drop old weekly partition |
| 普通用户配置 1 秒 interval | 返回 validation error |
| `<768px` | 显示桌面提示,不挤压表格 |

## 12. 风险与处理

| 风险 | 处理 |
|---|---|
| 高频 interval 任务写 run 太多 | 普通用户最小 60s,admin 配置最低 10s,cron 等价高频调度拒绝 |
| session run 卡住 | 30 分钟 timeout + lease 过期后可接管 |
| 多实例重复执行 | `ClaimDueScheduledTaskRun` 短事务 row lock + active lease |
| run history 缺少目标周分区 | claim/manual-claim 前 `EnsureScheduledTaskRunPartition` |
| 旧 run finalize 覆盖新 run lease | finalize 单事务先写 run row,再按 `active_run_id` 条件清 lease |
| prompt 存 PG 含敏感内容 | 本期沿用 sessions/messages 同级存储假设,不新增加密 |
| 旧 API 破坏 | 旧 handler 测试作为 REG-CRITICAL |
| 前端组件路径误用 | 不引用不存在的 `components/ui/*`,不引入 `react-hook-form` |

## 13. 实施顺序

1. Phase 1: Store + schema。
2. Phase 2: cron + bootstrap reload。
3. Phase 3: dispatcher。
4. Phase 4: REST API。
5. Phase 5: history partition GC。
6. Phase 6: frontend UI。
7. 全链路 e2e + 旧 push schedule regression。

不要并行修改同一文件。可以并行的 lane:

- Lane A: store/schema。
- Lane B: frontend 静态页面骨架,等 API contract 后接线。
- Lane C: dispatcher 单测,等 store claim 接口后整合。

## 14. Review 决策摘要

已合并进正文的决策:

- CEO review: 业务场景必须补齐,不能只做没人用的基础设施。
- Eng review: 不做 `skill` target,不做 dispatcher 注册表,不假设 `auth.WithUserID` 或 `OwnerUserID`。
- Design review: desktop only,状态 dot + 行展开 + drawer history,不做移动端完整管理。
- 本次 Codex 修订: 不用长事务 advisory lock,改为短事务 claim/finalize;不引用不存在的前端组件/库。
- 2026-05-08 plan-eng-review D2-D11: 完整方案保留,但分阶段实施;run-now 独立 manual claim;claim 前 ensure 分区;迁移 guard 绑定 `conrelid`;唯一索引 preflight;finalize 顺序固定;自动禁用必须满 5 次失败;首版最小 metrics/logs;Master 用专用 setter 注入 scheduled task 依赖;测试矩阵补齐边界;普通用户最小 interval 60s。

## 15. What already exists

| 已有能力 | 位置 | 本计划处理 |
|---|---|---|
| 旧 push schedule CRUD/API 权限测试 | `internal/api/push_handler_test.go` | 保留旧接口,作为兼容回归基线 |
| 旧 `scheduled_pushes` schema/store 方法 | `internal/store/postgres_migrate.go`, `internal/store/postgres.go` | 物理表沿用,旧方法委托新 ScheduledTask store |
| 进程内 interval cron | `internal/master/cron.go` | 原地扩展 `ScheduleSpec`,不引入独立调度服务 |
| 启动恢复 Feishu push schedule | `internal/bootstrap/helpers.go` | 替换为通用 `restoreScheduledTasksAsync` |
| `auth.WithUser(ctx, *auth.User)` / `auth.Engine.GetUserByID` | `internal/auth` | session target 复用 owner 身份,不新增伪接口 |
| push `Service.Push` / `DispatchScheduledPrompt` | `internal/channel/push/service.go` | im_push target 直接复用 |
| Master metrics/log 队列 | `internal/master/master.go` | scheduled task observability 复用 `enqueueMetric`/zap |
| frontend Vitest / Playwright | `frontend/vitest.config.ts`, `frontend/playwright.config.ts` | UI 与 E2E acceptance 使用现有测试栈 |

## 16. NOT in scope

- 物理表 rename: 延后到 scheduled task 真实使用稳定后单独 cleanup,避免滚动部署风险。
- `skill` target / dispatcher registry: 本期只支持 `im_push` 和 `session`,用显式 switch 保持边界清楚。
- 秒级普通用户任务: 普通用户最小 60s,admin 特例最低 10s,保护 run history 写入量。
- dashboard / alert: 首版只做 metrics 和结构化日志,可视化告警等生产高频后再补。
- DEFAULT partition 数据迁移: 选择 claim 前 ensure 周分区,不引入 default partition 后续搬迁成本。
- 自动重命名重复任务: 迁移发现重复 name 时 fail-fast,不静默改用户可见字段。
- 移动端完整管理 UI: `<768px` 显示桌面提示。

## 17. Failure modes

| Codepath | 生产失败方式 | 计划内处理 |
|---|---|---|
| `ClaimDueScheduledTaskRun` | 多实例同时触发同一 tick | row lock + unique `(scheduled_at, task_id)` + claim 竞态测试 |
| `ClaimManualScheduledTaskRun` | 用户在下一次自动触发前点击 run-now 不执行 | 独立 manual claim,`scheduled_at=now`,不改 `next_run_at` |
| 分区写入 | 目标周分区不存在导致 insert fail | claim 前 `EnsureScheduledTaskRunPartition`,记录 partition metric |
| `FinishScheduledTaskRun` | 旧 run 超时后覆盖新 run lease | 单事务先写 run row,再按 `active_run_id` 条件清 lease |
| 自动禁用 | 新任务第一次失败就被停 | 必须 recent runs 满 5 条且全失败/timeout |
| reload | 单条坏任务阻塞所有任务恢复 | 单任务失败隔离,坏任务 disabled + last_error |
| session dispatch | owner 缺失或禁用时 fallback admin | fail-fast 写 run error,不 fallback |
| 高频 interval | run history 写入量爆炸 | 普通用户 60s 最小间隔,admin 配置最低 10s |

## 18. Worktree parallelization

| Step | Modules touched | Depends on |
|---|---|---|
| Store/schema | `internal/store` | - |
| Cron/reload | `internal/master`, `internal/bootstrap` | Store types |
| Dispatcher | `internal/master`, `internal/bootstrap`, `internal/channel/push` | Store types |
| REST API | `internal/api`, `internal/master`, `internal/store` | Store + dispatcher |
| History GC | `internal/master`, `internal/store` | Store partition helper |
| Frontend UI | `frontend/src` | API contract |

Parallel lanes:

- Lane A: Store/schema -> REST API (sequential, shared store/API contract).
- Lane B: Dispatcher tests and shell implementation after store types land.
- Lane C: Frontend static page skeleton can start after API contract is stable, but API wiring waits for Lane A.
- Lane D: History GC can start after `EnsureScheduledTaskRunPartition` exists.

Execution order: finish Phase 1 first, then run Lane B + D in parallel while API starts. Frontend skeleton may proceed in parallel only if it does not edit generated `internal/webui/dist`.

## GSTACK REVIEW REPORT

| Review | Status | Findings |
|---|---|---|
| CEO Review | merged | 业务场景 anchor、scope cut、两 target 收敛 |
| Eng Review | merged | D2-D11 全部确认并合并: manual claim、partition ensure、migration guards、finalize 顺序、observability、测试矩阵、interval guard |
| Design Review | merged | desktop UI、a11y、状态与 history 规格 |
| Codex follow-up | merged | run claim 模型、前端真实依赖、正文/附录冲突清理 |

**VERDICT:** ready to implement in phases. Start with Phase 1 store/schema; keep the complete B scope, but do not skip the D2-D11 reliability/test requirements.
