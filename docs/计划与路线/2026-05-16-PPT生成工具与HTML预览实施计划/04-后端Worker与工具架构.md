# 后端 Worker 与工具架构

> 上级索引：[2026-05-16-PPT生成工具与HTML预览实施计划.md](../2026-05-16-PPT生成工具与HTML预览实施计划.md)

## 4. 推荐架构

### 4.1 后端模块

新增目录：

```text
internal/presentation/
  spec/
    types.go
    outline.go
    planner.go
    validate.go
    schema.go
    validate_test.go
  runtime/
    context.go
  renderhtml/
    renderer.go
    swiss.go
    renderer_test.go
  templates/
    swiss/
      layouts.json
      themes.json
      fonts.json
      icons.json
  pptx/
    exporter.go
    exporter_test.go
    node_worker.go
  assets/
    upload.go
  service.go
  service_test.go
internal/diagram/
  spec/
    types.go
    validate.go
    schema.go
    validate_test.go
  render/
    mermaid.go
    mindmap.go
  service.go
  service_test.go
```

职责：

- `spec`：定义 `DeckSpec`、`SlideSpec`、主题、版式、图片槽位、下载产物元数据，并导出工具 input JSON Schema。
- `outline.go/planner.go`：定义 `OutlineSpec` 和 `LayoutPlanner` 接口，首发可不暴露给模型，但为后续一句话/大纲生成保留稳定入口。
- `runtime`：从 `context.Context` 提取服务端派生的 user/session/domain/tenant/turn/run facts，拒绝模型传入 owner。
- `renderhtml`：把 `DeckSpec` 渲染为安全 HTML。所有用户文本必须 escape；只允许 renderer 生成 HTML tag。
- `templates/swiss/fonts.json`：记录系统字体栈、自托管字体候选、PPTX fallback 和 license；首发即使不内置字体，也必须有该合同文件。
- `templates/swiss/icons.json`：记录允许使用的 inline SVG path id；renderer/exporter 只能引用 registry 内图标，不能消费模型传入的 SVG。
- `pptx`：Go 侧封装 PPTX exporter。首发用 Node worker + PptxGenJS 输出 `.pptx` bytes。
- `assets`：把 HTML/PPTX 上传到 asset 服务，设置 run-scoped namespace/tags/mime。当前 Hive asset 上传会按 namespace/content hash/owner 早返回去重，所以首发 namespace 必须包含 run id，run 表是 canonical truth。
- `service.go`：编排 validate、image resolve、HTML render、PPTX export、asset upload、run report。
- `internal/diagram/spec`：定义 `DiagramSpec`、`MindMapSpec`、Mermaid source 限制和 JSON Schema。
- `internal/diagram/render`：封装 Mermaid/Mindmap 的 source -> SVG/HTML/PNG 导出。
- `internal/diagram/service.go`：编排 validate、render/export、asset upload、run report，复用 presentation 的 RuntimeContext/asset policy 模式。

### 4.1.0 当前 Hive 代码适配闸门

实现前必须先补齐当前 Hive 的真实集成点，而不是只新增孤立模块：

- `internal/bootstrap/server.go` 的 `ServerComponents` 增加 `PresentationService`、`DiagramService` 或统一 artifact service 字段。
- `internal/api/server.go` 增加 presentation/diagram service 窄接口字段和 setter。
- `cmd/server/main.go` 在创建 API server 后注入新 service。
- `internal/api/routes.go` 注册 `GET /api/v1/presentation/runs/{run_id}` 和 `GET /api/v1/diagram/runs/{run_id}`。
- `internal/bootstrap/asset.go` 的 `initAssetAccessResolver` 接收 presentation/diagram run reader，或通过 service 提供窄授权查询；不能另建 resolver chain。
- `internal/config/config.go`、`defaults.go`、`Resolve/Normalize`、`config.example.json` 增加 presentation/diagram 配置。
- Dockerfile runtime stage 必须复制并安装 `tools/presentation-exporter` / `tools/diagram-exporter`，因为当前镜像只有 node/npm/chromium，没有 worker 源码和依赖。

### 4.1.1 Runtime Context 合同

`generate_ppt` 需要资产 owner 和权限事实，但当前 `toolctx.ToolContext` 只有 caller/trace/span/session；Master 执行工具时注入 `toolctx.WithSessionID` 和 `tools.WithKBRuntimeContext`。实现必须显式定义 presentation runtime，避免各层自行猜测 user。

固定接口：

```go
type RuntimeContext struct {
    UserID            string
    OwnerScope        string
    OwnerID           string
    DomainID          string
    TenantID          string
    AgentID           string
    SessionTemplateID string
    SessionID         string
    TurnID            string
    TraceID           string
    ToolCallID        string
}
```

派生规则：

- `SessionID` 从 `toolctx.GetSessionID(ctx)` 读取。
- `TraceID/TurnID/ToolCallID` 从 `toolctx.GetToolContext(ctx)` 读取；若仍走 Master-local trace bridge，也必须统一适配。
- `OwnerScope/OwnerID/DomainID/AgentID` 优先复用 `tools.KBRuntimeContextFromContext(ctx)`，因为 Master 已在 `executeTool` 里注入服务端事实。
- `UserID` 从 `auth.UserIDFrom(ctx)` 读取；如果为空，再使用 KB runtime 的 `OwnerID` 或 session.UserID 作为 user-owned CLI/dev fallback，但必须记录 warning。不能假设 auth middleware 总是存在。
- 模型输入中的 `owner_id/owner_scope/session_id/domain_id` 一律忽略或校验拒绝。

失败规则：

- 无 `UserID` 且无法安全 fallback：返回 `asset_unavailable` 或 `auth_required`，不要生成不可下载的孤儿资产。
- 无 `SessionID`：允许 CLI/dev 模式生成，但 asset tag 里 `session_id` 为空时不能通过 `presentation_download` 的 session 绑定校验。
- `OwnerScope` 首发只允许 `user`；tenant/domain 共享 PPT 放到后续版本。

下载授权决策：

- presentation 是用户长期资产，不是纯会话临时产物。
- `OwnerID/UserID` 是强授权边界，必须匹配。
- `SessionID` 是审计和当前会话一致性信号，不作为历史消息下载的唯一授权条件。
- 如果 asset tag 中 `session_id` 非空且 resolve 请求带 `session_id`，两者必须匹配。
- 如果历史消息或资产列表下载不带 `session_id`，同 owner 可下载 PPTX，但必须记录 `presentation_download_without_session` 审计字段。

### 4.2 Node PPTX exporter worker

新增目录：

```text
tools/presentation-exporter/
  package.json
  package-lock.json
  src/exporter.mjs
  src/layouts/swiss.mjs
  src/schema.mjs
  test/exporter.test.mjs
```

运行方式：

- Go 后端把 exporter request JSON 写入 worker stdin。
- Node worker 用 PptxGenJS 生成 PPTX，写到 Go 指定的 output path。
- Node worker stdout 只输出一行 JSON report，不输出二进制；stderr 只用于日志。
- Go 后端读取 output path bytes，校验大小和 PPTX 结构后上传 asset。

为什么用 Node worker：

- PptxGenJS 是当前最成熟的 JS PPTX 生成库。
- 与 Go 服务解耦，便于单测和升级。
- 不让模型调用 shell；这是后端可信代码路径，不经过 `bash` tool。

部署要求：

- 首发固定为 server 镜像内随附 Node worker，不做独立 exporter service。
- Docker/server runtime 需要 Node LTS，随 server 镜像安装 `tools/presentation-exporter` 依赖。
- 增加启动自检：如果 `presentation.enabled=true` 但 Node worker 不可用，`generate_ppt` 返回结构化错误，不影响其他工具。
- 独立 exporter service、队列扩容、远程 worker 池放到 M3 或更晚。

### 4.2.1 Worker 协议

为避免 stdout 二进制和日志混杂，首发固定使用 tempfile 协议。

请求：

```json
{
  "protocol_version": 1,
  "run_id": "prun_...",
  "mode": "editable",
  "deck": { "version": 1, "title": "...", "slides": [] },
  "resolved_assets": {
    "asset_hero": {
      "kind": "image",
      "mime": "image/png",
      "path": "/tmp/hive-presentation/prun_.../hero.png",
      "width": 1600,
      "height": 900
    }
  },
  "output_path": "/tmp/hive-presentation/prun_.../deck.pptx",
  "limits": {
    "max_slides": 30,
    "max_output_bytes": 52428800,
    "timeout_ms": 30000
  }
}
```

stdout report：

```json
{
  "ok": true,
  "protocol_version": 1,
  "run_id": "prun_...",
  "mode": "editable",
  "slide_count": 8,
  "output_bytes": 1843200,
  "layout_counts": { "S01": 1, "S04": 2 },
  "warnings": [],
  "metrics": {
    "render_ms": 420,
    "image_count": 4
  }
}
```

错误 report：

```json
{
  "ok": false,
  "error_kind": "unsupported_layout",
  "message": "layout S16 is not supported in editable mode",
  "json_pointer": "/slides/4/layout",
  "recoverable": true
}
```

Go 侧约束：

- 用 `exec.CommandContext` 设置超时。
- 用 `max_concurrent_workers` semaphore 限制并发 Node worker。超过上限时返回 `exporter_busy`，`recoverable_by="user"`，前端提示稍后重试；不要无限排队占住 tool call。
- worker 进程不接收模型可控命令参数，只有固定 worker path 和 stdin JSON。
- output path 必须位于 Go 创建的专用临时目录，目录权限收紧，完成后清理。
- 读取文件前先 stat，超过 `max_output_bytes` 直接失败。
- 校验 zip 中至少存在 `[Content_Types].xml`、`ppt/presentation.xml`、`ppt/slides/slide1.xml`。
- editable 模式继续运行 PPTX 结构验收，确认不是全页截图。
- worker 非 0 exit、stdout 为空、stdout 非 JSON、stdin 写入失败、output path 不存在，都必须映射为结构化错误并记录 stderr 摘要；不能只表现为 timeout。
- stderr 摘要写日志时必须限长，避免大输出污染日志；用户错误消息只暴露 error_kind/run_id，不暴露本地路径。

### 4.2.2 部署与配置

新增配置：

```json
{
  "presentation": {
    "enabled": true,
    "worker_command": "node",
    "worker_script": "tools/presentation-exporter/src/exporter.mjs",
    "timeout_seconds": 30,
    "sync_timeout_seconds": 12,
    "max_concurrent_workers": 3,
    "async_enabled": true,
    "max_slides": 30,
    "max_output_mb": 50,
    "max_image_mb": 10,
    "temp_dir": "",
    "enable_visual_export": false,
    "allow_remote_https_images": false
  }
}
```

配置默认值合同：

- `presentation.enabled` 默认 `false`，开发/内测环境显式打开；M2 首发前再改为按部署环境配置打开。
- `async_enabled` 默认 `true`，但如果 store 不可用，工具必须返回 `exporter_unavailable` 或 `asset_unavailable`，不能退化成不可追踪同步生成。
- `sync_timeout_seconds` 必须小于 `timeout_seconds`，默认 12s；同时必须小于 `RuntimePolicy.ToolTimeout`。当前本地 built-in tool 的直接超时主要来自 `Master.executeTool` 的 tool timeout，默认 2 分钟；`mcpOperationTimeout=30s` 主要约束远程 MCP client，不应被当作本地工具的唯一超时来源。
- `max_concurrent_workers` 默认 3；Go exporter 必须用 semaphore 控制同时运行的 Node worker 数量。
- `max_slides/max_output_mb/max_image_mb` 同时用于 schema、service 和 worker request，不能前后端各写一套不同限制。
- `temp_dir` 为空时使用 `os.MkdirTemp("", "hive-presentation-*")`；非空时启动自检目录存在、可写且不在仓库目录内。
- 配置 normalize/validate 必须覆盖 worker script 存在、temp dir 可写、`max_concurrent_workers > 0`、`sync_timeout_seconds < timeout_seconds`、`sync_timeout_seconds < RuntimePolicy.ToolTimeout`。

部署任务必须包含：

- `tools/presentation-exporter/package-lock.json` 提交到仓库。
- server Dockerfile 或部署镜像安装 Node LTS，并运行 `npm ci --omit=dev` 或构建 worker bundle。当前 Dockerfile runtime 只复制 `/hive` 和 config，不会自动包含顶层 `tools/`，必须显式 `COPY tools/presentation-exporter /app/tools/presentation-exporter`。
- server 启动时执行 presentation health check：Node 可执行、worker script 存在、`--health` 返回 protocol version。
- health check 失败时，如果 `presentation.enabled=true`，工具可见但调用返回 `exporter_unavailable`；不要影响 server 启动和其他工具。
- worker 临时目录必须按 run id 创建，成功或失败都清理；清理失败只记录 warning，不能影响错误返回。
- worker 依赖必须锁定 `package-lock.json`，CI 运行 `npm ci`，禁止无锁安装。
- 首发必须跑 `npm audit --omit=dev --audit-level=high`；若 PptxGenJS/JSZip 链路出现 high/critical 且无可接受例外，不得发布。
- health check 记录 `worker_protocol_version`；如果 worker protocol version 小于 Go request protocol version，调用时返回 `exporter_unavailable`，禁止发送不兼容请求。

### 4.2.3 Diagram exporter worker

新增目录：

```text
tools/diagram-exporter/
  package.json
  package-lock.json
  src/exporter.mjs
  src/mermaid.mjs
  src/mindmap.mjs
  src/sanitize-svg.mjs
  test/exporter.test.mjs
```

运行方式：

- Go 后端把 diagram export request JSON 写入 worker stdin。
- Node worker 根据 `diagram_type` 生成 source/SVG/PNG/HTML 文件到 Go 指定的 output paths。
- stdout 只输出一行 JSON report；stderr 只用于日志。
- Go 后端读取 output path bytes，校验大小、sanitize 结果和 MIME 后上传 asset。

为什么 diagram 单独 worker，而不是塞进 PPTX worker：

- Mermaid/Mindmap 依赖 Playwright/Chromium 和 SVG sanitize，生命周期、错误分类、资源限制与 PPTX 不同。
- PPTX worker 只负责 `DeckSpec -> .pptx`，减少 supply chain 和运行时权限面。
- 两个 worker 可以共用 Node LTS、临时目录、health check、semaphore 和日志模式，但发布开关独立。

请求：

```json
{
  "protocol_version": 1,
  "run_id": "drun_...",
  "diagram_type": "mermaid",
  "source_format": "mermaid",
  "source": "flowchart LR\nA[Chat] --> B[generate_diagram]",
  "mindmap": null,
  "outputs": ["source", "svg"],
  "output_paths": {
    "source": "/tmp/hive-diagram/drun_.../diagram.mmd",
    "svg": "/tmp/hive-diagram/drun_.../diagram.svg",
    "png": "/tmp/hive-diagram/drun_.../diagram.png",
    "html": "/tmp/hive-diagram/drun_.../diagram.html"
  },
  "limits": {
    "max_source_bytes": 102400,
    "max_svg_bytes": 2097152,
    "max_png_bytes": 10485760,
    "max_nodes": 300,
    "max_depth": 8,
    "timeout_ms": 20000
  }
}
```

stdout report：

```json
{
  "ok": true,
  "protocol_version": 1,
  "run_id": "drun_...",
  "diagram_type": "mermaid",
  "source_format": "mermaid",
  "output_bytes": {
    "source": 64,
    "svg": 18432,
    "png": 0,
    "html": 0
  },
  "warnings": [],
  "metrics": {
    "render_ms": 180,
    "node_count": 4,
    "edge_count": 3
  }
}
```

错误 report：

```json
{
  "ok": false,
  "error_kind": "mermaid_parse_failed",
  "message": "Parse error on line 2",
  "json_pointer": "/mermaid",
  "recoverable": true
}
```

Mermaid 导出策略：

- M2 首发服务端 SVG 导出优先使用 Mermaid 11.15.0 的 render API 或 `@mermaid-js/mermaid-cli` 11.15.0；实现时选择一种并固定在 worker 内，禁止模型传 CLI 参数。
- Mermaid 初始化固定 `securityLevel:'strict'`，`startOnLoad:false`，禁用 HTML label。
- Mermaid source 只允许 UTF-8 文本，最大 100KB。
- 输出 SVG 后必须通过 `sanitize-svg.mjs` 清洗：移除 `<script>`、`foreignObject>`、`on*` 属性、`javascript:` URL、外链 `<image>` 和外部 stylesheet。
- PNG 导出 M3 开启；如果 M2 请求 `png`，工具 schema 应拒绝或返回 `unsupported_output`，不要静默降级。

Mindmap 导出策略：

- M3 使用 `markmap-lib` 把 Markdown outline 转成规范化 tree，或直接消费 `MindMapSpec JSON`。
- 前端交互预览用 `markmap-view`；服务端导出 SVG/PNG/HTML 通过受控 Playwright 页面渲染。
- Markdown outline 只接受 heading/list 结构，不保留 raw HTML。
- `MindMapSpec JSON` 是审计事实源；即使用户输入 Markdown，也必须上传规范化 JSON source asset。
- 自包含 HTML 下载只允许作为 attachment，不能作为 iframe trusted preview 的默认路径。

Go 侧约束：

- 用 `diagram.max_concurrent_workers` semaphore 限制并发，超过上限返回 `diagram_exporter_busy`。
- worker 进程不接收模型可控命令参数，只有固定 worker path 和 stdin JSON。
- output paths 必须位于 Go 创建的 run 专用临时目录。
- output 文件存在性、大小、MIME、SVG sanitize 后内容都由 Go 侧二次校验。
- worker 非 0 exit、stdout 为空、stdout 非 JSON、stdin 写入失败、output path 不存在，都必须映射为结构化错误。
- diagram worker 默认不访问网络；Playwright 启动参数禁止 remote debugging 暴露端口。

### 4.2.4 Diagram 部署与配置

新增配置：

```json
{
  "diagram": {
    "enabled": true,
    "worker_command": "node",
    "worker_script": "tools/diagram-exporter/src/exporter.mjs",
    "timeout_seconds": 20,
    "sync_timeout_seconds": 8,
    "max_concurrent_workers": 3,
    "async_enabled": true,
    "max_source_kb": 100,
    "max_svg_mb": 2,
    "max_png_mb": 10,
    "max_nodes": 300,
    "max_depth": 8,
    "enable_png_export": false,
    "enable_mindmap": true,
    "temp_dir": ""
  }
}
```

配置默认值合同：

- `diagram.enabled` 默认 `false`，开发/内测环境显式打开；如果产品决定 M2 同发 Mermaid，首发环境打开该开关。
- `enable_png_export` 默认 `false`，M3 验证 Playwright PNG 稳定性后再开放。
- `enable_mindmap` 默认 `true` 仅表示工具 schema 可接受 mindmap；若 worker 依赖未就绪，health check 必须把 mindmap 标记为 unavailable。
- `max_source_kb/max_nodes/max_depth` 同时用于 tool schema、Go validator 和 worker request。
- `sync_timeout_seconds` 必须小于 `timeout_seconds`，默认 8s；同时必须小于 `RuntimePolicy.ToolTimeout`。非法配置启动时记录 error 并使用默认值。
- `temp_dir` 为空时使用 `os.MkdirTemp("", "hive-diagram-*")`；非空时启动自检目录存在、可写且不在仓库目录内。

部署任务必须包含：

- `tools/diagram-exporter/package-lock.json` 提交到仓库。
- server Dockerfile 或部署镜像复用 presentation worker 的 Node LTS/Chromium 安装策略，并显式复制 `tools/diagram-exporter` 到 runtime 镜像。
- server 启动时执行 diagram health check：Node 可执行、worker script 存在、`--health` 返回 protocol version、Mermaid/Markmap 版本和可用 outputs。
- `diagram.enabled=true` 但 health check 失败时，`generate_diagram` 调用返回 `diagram_exporter_unavailable`，不能影响 server 启动和其他工具。
- 首发必须跑 `npm audit --omit=dev --audit-level=high`；若 Mermaid/Markmap/Playwright 链路出现 high/critical 且无可接受例外，不得发布。

### 4.3 内置工具

新增工具：`generate_ppt`

注册方式：

- 使用独立顶层注册函数 `tools.RegisterPPTGen(sc.MCPHost, sc.AssetService, presentationService, logger)`，对齐现有 `tools.RegisterImageGen` / `tools.RegisterVideoGen` 模式。
- 不把 `PresentationService` 追加进 `RegisterBuiltinTools` 的 variadic optional 参数。该函数现有 optional 依赖已经过多，继续扩展会让工具依赖注入不可维护。
- `generate_diagram` 同理使用 `tools.RegisterDiagramGen(sc.MCPHost, sc.AssetService, diagramService, logger)`。
- 工具注册失败只影响对应工具可用性，不能影响其他内置工具注册。

Bootstrap 初始化顺序固定为：

```text
pgPool
  -> initAssetService
  -> NewPresentationRunStore / NewDiagramRunStore
  -> NewPresentationService / NewDiagramService
  -> initAssetAccessResolver 内部 source_kind 分支覆盖 presentation/diagram 授权
  -> RegisterBuiltinTools
  -> RegisterPPTGen / RegisterDiagramGen
```

约束：

- `PresentationRunStore` 和 `DiagramRunStore` 依赖 pgPool；pgPool 不可用时工具必须返回 `asset_unavailable` / `exporter_unavailable`，不能退化为不可追踪同步生成。
- `PresentationService` 必须在 `initAssetService` 之后创建，因为 DeckSpec/HTML/PPTX 都要上传 asset。
- `initAssetAccessResolver` 当前返回单个 resolver 对象，不能新增“resolver 链”。实现时在 `assetAccessResolver.CanResolveAsset` 中先判断 `source_kind=generated_presentation|generated_diagram`，再落回现有 KB/agent artifact 逻辑。
- Asset proxy 的 disposition/filename 支持修改现有 `/api/v1/assets/proxy` handler，不能新增绕过现有签名、ttl、owner 校验的平行下载端点。
- `internal/router/capability_registry.go` 增加 `generate_ppt` / `generate_diagram` 的 builtin tool rule；如果 action guard 默认权限规则需要显式允许 media local write，也必须同步测试。
- `internal/master/tool_choice_detector.go` 或等价 host intent guard 增加 artifact 生成意图：用户明确请求 PPT/演示文稿/slide deck/下载 PPTX 时返回 `tool_choice=required`，用户明确请求 Mermaid/脑图/流程图/架构图/可下载 SVG/PNG 时返回 `tool_choice=required`。只靠系统 prompt “优先调用工具”不够，当前 broad heuristic 在 `ToolChoiceForce` 关闭时不会覆盖普通“帮我做一份 PPT”。

工具结果合同：

- output schema 只能作为描述，不作为强校验依赖。当前 Host 只做轻量 JSON/required 诊断，前端也不会自动理解 output schema。
- `generate_ppt` / `generate_diagram` executor 必须用 Go 结构体 marshal 结果，返回前做强校验。
- 前端必须用 runtime parser 严格解析 `kind/status/run_id/asset_uri`，不能因为 output schema 存在就信任字符串。
- 工具 input schema 如果使用 `$defs/$ref/oneOf/const`，必须对项目实际 LLM provider 做兼容验证；必要时在注册前扁平化 schema。

固定输入 schema：

```json
{
  "type": "object",
  "additionalProperties": false,
  "properties": {
    "deck": {
      "type": "object",
      "$ref": "#/$defs/DeckSpec",
      "description": "结构化 DeckSpec。必须符合 presentation/spec 导出的强 schema。"
    },
    "outputs": {
      "type": "array",
      "items": { "type": "string", "enum": ["html", "pptx"] },
      "default": ["html", "pptx"],
      "description": "默认同时生成 html 和 pptx。"
    },
    "mode": {
      "type": "string",
      "enum": ["editable"],
      "default": "editable",
      "description": "首发仅支持 editable，输出原生 PPTX 对象。visual 模式不对用户开放。"
    }
  },
  "required": ["deck"],
  "$defs": {
    "DeckSpec": {
      "type": "object",
      "additionalProperties": false,
      "required": ["version", "title", "language", "style", "theme", "aspect", "slides"],
      "properties": {
        "version": { "type": "integer", "const": 1 },
        "title": { "type": "string", "minLength": 1, "maxLength": 80 },
        "language": { "type": "string", "enum": ["zh-CN", "en-US"] },
        "style": { "type": "string", "enum": ["swiss"] },
        "theme": { "type": "string", "enum": ["ikb", "lemon", "lemon_green", "safety_orange", "custom"] },
        "custom_accent": {
          "type": "string",
          "pattern": "^#[0-9A-Fa-f]{6}$",
          "description": "仅当 theme=custom 时允许，用作企业品牌 accent 色。"
        },
        "aspect": { "type": "string", "enum": ["16:9"] },
        "slides": {
          "type": "array",
          "minItems": 1,
          "maxItems": 30,
          "items": { "$ref": "#/$defs/SlideSpec" }
        }
      }
    },
    "SlideSpec": {
      "oneOf": [
        { "$ref": "#/$defs/S01Cover" },
        { "$ref": "#/$defs/S04SixCells" }
      ]
    },
    "S01Cover": {
      "type": "object",
      "additionalProperties": false,
      "required": ["layout", "title"],
      "properties": {
        "layout": { "const": "S01" },
        "title": { "type": "string", "minLength": 1, "maxLength": 42 },
        "subtitle": { "type": "string", "maxLength": 80 },
        "meta": { "type": "string", "maxLength": 60 },
        "notes": { "type": "string", "maxLength": 1000 }
      }
    },
    "S04SixCells": {
      "type": "object",
      "additionalProperties": false,
      "required": ["layout", "title", "cells"],
      "properties": {
        "layout": { "const": "S04" },
        "title": { "type": "string", "minLength": 1, "maxLength": 36 },
        "cells": {
          "type": "array",
          "minItems": 6,
          "maxItems": 6,
          "items": {
            "type": "object",
            "additionalProperties": false,
            "required": ["label", "body"],
            "properties": {
              "label": { "type": "string", "minLength": 1, "maxLength": 18 },
              "body": { "type": "string", "minLength": 1, "maxLength": 80 }
            }
          }
        },
        "notes": { "type": "string", "maxLength": 1000 }
      }
    }
  }
}
```

上面只展示两个 layout 的 schema 片段；真实实现必须由 layout registry 覆盖首发 10 个 Swiss layout，并在测试里断言工具注册 schema 和 Go validator 使用同一套 layout 定义。

Schema 来源合同：

- `internal/presentation/templates/*/layouts.json` 是 layout 字段、长度、图片槽位、PPTX 坐标和 public support 的唯一事实源。
- `schema.go` 必须从 registry 动态构建 JSON Schema，再注入 config 中的 `max_slides/max_image_mb/max_output_mb`。
- Go validator 必须读取同一个 registry，禁止手写一份与 JSON Schema 并行漂移的 layout 规则。
- 单测必须对同一组 valid/invalid fixtures 同时跑 JSON Schema 和 Go validator，断言通过/失败和 JSON pointer 一致。
- 工具注册时暴露的 schema 必须来自 `schema.go` 生成结果，不能在 `generate_ppt.go` 再手写一份 schema。

固定输出：

```json
{
  "kind": "presentation",
  "run_id": "prun_...",
  "status": "succeeded",
  "title": "一种新的工作方式",
  "style": "swiss",
  "theme": "ikb",
  "slide_count": 8,
  "mode": "editable",
  "deck_spec_asset_uri": "asset://presentations/user/user_.../run/prun_.../....json",
  "html_asset_uri": "asset://presentations/user/user_.../run/prun_.../....html",
  "pptx_asset_uri": "asset://presentations/user/user_.../run/prun_.../....pptx",
  "warnings": [],
  "validation": {
    "status": "passed",
    "checks": ["spec", "swiss-layout", "pptx-extract"]
  }
}
```

工具结果大小限制：

- 工具结果只返回轻量 manifest，目标小于 5KB。不要把完整 HTML 放进 tool result。
- HTML asset 最大 5MB。
- HTML 不允许包含 data URI 图片；图片必须走 asset/resolved URL 或在后端 renderer 中使用受控引用。
- Canvas 打开时通过 `html_asset_uri` resolve/fetch HTML。
- 超过 5MB：返回 `html_too_large` 结构化错误，模型应缩短内容或减少 slide。
- 如果为了调试临时保留 `html_preview`，必须 behind dev flag，硬上限 8KB，且前端不得把它作为唯一数据源。首发用户路径默认不返回该字段。

错误输出必须结构化，不能只返回自然语言：

```json
{
  "kind": "presentation_error",
  "run_id": "prun_...",
  "error_kind": "invalid_spec",
  "message": "slide title is too long",
  "json_pointer": "/slides/2/title",
  "recoverable": true,
  "recoverable_by": "model",
  "repair_action": "shorten_title"
}
```

错误分类：

- `invalid_spec`：模型可修复，`recoverable=true`，`recoverable_by="model"`。
- `unsupported_layout`：模型可改 layout，`recoverable=true`，`recoverable_by="model"`。
- `image_resolution_failed`：必填图失败通常 `recoverable_by="model"`，用户提供的图不可访问时 `recoverable_by="user"`；可选图降级时返回 warning。
- `exporter_busy`：worker 并发已满，`recoverable=true`，`recoverable_by="user"`。
- `quota_exceeded`：用户配额超限，`recoverable=false`，`recoverable_by="user"`。
- `exporter_unavailable`：部署问题，`recoverable=false`，`recoverable_by="operator"`。
- `asset_unavailable`：asset service 或权限上下文问题，`recoverable=false`，`recoverable_by="operator"`。
- `pptx_validation_failed`：实现或 renderer bug，`recoverable=false`，`recoverable_by="operator"`，需要记录质量事件。
- `html_too_large`：内容可缩短，`recoverable=true`，`recoverable_by="model"`。

模型自动重试规则：

- tool description 必须明确：同一用户请求内，`recoverable_by="model"` 的错误最多自动修复重试 2 次。
- `PresentationRunStore` 记录 `retry_of_run_id` 或在 metrics 中记录 `repair_attempt`，用于追踪连续失败。
- 同一 session 连续 3 次 `invalid_spec/unsupported_layout/html_too_large` 后，下一次返回 `recoverable=false`、`recoverable_by="user"`，让用户看到明确失败而不是无限工具调用。

新增工具：`generate_diagram`

固定输入 schema：

```json
{
  "type": "object",
  "additionalProperties": false,
  "properties": {
    "diagram": {
      "type": "object",
      "$ref": "#/$defs/DiagramSpec",
      "description": "结构化 DiagramSpec。Mermaid 和 MindMapSpec 都必须符合 diagram/spec 导出的强 schema。"
    },
    "outputs": {
      "type": "array",
      "items": { "type": "string", "enum": ["source", "svg", "png", "html"] },
      "default": ["source", "svg"],
      "description": "默认生成 source 和 svg。png/html 由配置和 diagram_type 决定是否支持。"
    }
  },
  "required": ["diagram"],
  "$defs": {
    "DiagramSpec": {
      "oneOf": [
        { "$ref": "#/$defs/MermaidSpec" },
        { "$ref": "#/$defs/MindMapSpec" }
      ]
    },
    "MermaidSpec": {
      "type": "object",
      "additionalProperties": false,
      "required": ["kind", "diagram_type", "version", "title", "source_format", "mermaid"],
      "properties": {
        "kind": { "const": "diagram" },
        "diagram_type": { "const": "mermaid" },
        "version": { "type": "integer", "const": 1 },
        "title": { "type": "string", "minLength": 1, "maxLength": 80 },
        "source_format": { "const": "mermaid" },
        "mermaid": { "type": "string", "minLength": 1, "maxLength": 102400 }
      }
    },
    "MindMapSpec": {
      "type": "object",
      "additionalProperties": false,
      "required": ["kind", "diagram_type", "version", "title", "source_format", "mindmap"],
      "properties": {
        "kind": { "const": "diagram" },
        "diagram_type": { "const": "mindmap" },
        "version": { "type": "integer", "const": 1 },
        "title": { "type": "string", "minLength": 1, "maxLength": 80 },
        "source_format": { "type": "string", "enum": ["mindmap-json", "mindmap-markdown"] },
        "mindmap": { "$ref": "#/$defs/MindMapTree" }
      }
    },
    "MindMapTree": {
      "type": "object",
      "additionalProperties": false,
      "required": ["root"],
      "properties": {
        "root": { "$ref": "#/$defs/MindMapNode" }
      }
    },
    "MindMapNode": {
      "type": "object",
      "additionalProperties": false,
      "required": ["id", "text"],
      "properties": {
        "id": { "type": "string", "minLength": 1, "maxLength": 64 },
        "text": { "type": "string", "minLength": 1, "maxLength": 120 },
        "href": { "type": "string", "maxLength": 512 },
        "children": {
          "type": "array",
          "maxItems": 40,
          "items": { "$ref": "#/$defs/MindMapNode" }
        }
      }
    }
  }
}
```

Schema 来源合同：

- `internal/diagram/spec/schema.go` 是 `generate_diagram` tool schema 的唯一来源，`generate_diagram.go` 不手写第二份 schema。
- `diagram.max_source_kb/max_nodes/max_depth/enable_png_export/enable_mindmap` 必须注入 schema 和 validator。
- Mermaid source 的最大长度按 bytes 计算，不能只按 rune 数。
- MindMapSpec validator 必须 DFS 检查节点数、深度、重复 id、危险 href、空 children、raw HTML。
- 单测必须对同一组 valid/invalid fixtures 同时跑 JSON Schema 和 Go validator，断言错误 JSON pointer 一致。

固定输出：

```json
{
  "kind": "diagram",
  "run_id": "drun_...",
  "status": "succeeded",
  "title": "生成链路",
  "diagram_type": "mermaid",
  "source_format": "mermaid",
  "source_asset_uri": "asset://diagrams/user/user_.../run/drun_.../....mmd",
  "svg_asset_uri": "asset://diagrams/user/user_.../run/drun_.../....svg",
  "png_asset_uri": "",
  "html_asset_uri": "",
  "warnings": [],
  "validation": {
    "status": "passed",
    "checks": ["spec", "source-size", "svg-sanitize"]
  }
}
```

工具结果大小限制：

- 工具结果只返回轻量 manifest，目标小于 5KB。不要把完整 Mermaid source、MindMapSpec JSON 或 SVG 放进 tool result。
- Mermaid source asset 最大 100KB。
- MindMapSpec JSON source asset 最大 512KB，但节点数仍受 `max_nodes` 限制。
- Canvas 打开时通过 `source_asset_uri` / `svg_asset_uri` resolve/fetch。
- SVG asset 首发最大 2MB，PNG asset 首发最大 10MB。
- 如果为了调试临时保留 `source_preview`，必须 behind dev flag，硬上限 4KB，且前端不得把它作为唯一数据源。

错误输出：

```json
{
  "kind": "diagram_error",
  "run_id": "drun_...",
  "error_kind": "mermaid_parse_failed",
  "message": "Parse error on line 2",
  "json_pointer": "/diagram/mermaid",
  "recoverable": true,
  "recoverable_by": "model",
  "repair_action": "fix_mermaid_syntax"
}
```

错误分类：

- `invalid_diagram_spec`：模型可修复，`recoverable_by="model"`。
- `diagram_source_too_large`：模型可缩短，`recoverable_by="model"`。
- `mermaid_parse_failed`：模型可修 Mermaid 语法，`recoverable_by="model"`。
- `mindmap_too_large`：模型可缩减节点，`recoverable_by="model"`。
- `unsafe_diagram_link`：模型可删除或替换链接，`recoverable_by="model"`。
- `unsupported_diagram_type`：模型可改成 Mermaid/mindmap，`recoverable_by="model"`。
- `unsupported_output`：用户或模型请求未启用的 png/html，`recoverable_by="model"` 或 `user`，取决于请求来源。
- `diagram_exporter_busy`：worker 并发已满，`recoverable_by="user"`。
- `diagram_exporter_unavailable`：部署问题，`recoverable_by="operator"`。
- `diagram_asset_unavailable`：asset service 或权限上下文问题，`recoverable_by="operator"`。
- `svg_sanitize_failed`：实现或 renderer bug，`recoverable_by="operator"`，需要记录质量事件。

模型自动重试规则：

- tool description 必须明确：`recoverable_by="model"` 的 diagram 错误最多自动修复重试 2 次。
- 同一 session 连续 3 次 `invalid_diagram_spec/mermaid_parse_failed/mindmap_too_large` 后，下一次返回 `recoverable=false`、`recoverable_by="user"`。
- 如果用户只是要求“画个流程图/时序图/架构图”，模型应该优先选择 Mermaid；如果用户明确要求“脑图/思维导图”，选择 mindmap。
- 如果用户要求可下载图片，模型必须调用 `generate_diagram`，不能只输出 fenced Mermaid 代码块。
