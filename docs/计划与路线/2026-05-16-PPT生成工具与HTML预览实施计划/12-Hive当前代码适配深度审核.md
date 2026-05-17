# Hive 当前代码适配深度审核

> 审核日期：2026-05-17
> 审核对象：当前 Hive 代码、既有计划文档、`11-深度源码审核报告.md`
> 审核结论：当前计划仍不能直接实施。必须先修正本文列出的 P0/P1 集成合同，否则实现会在 asset、Chat 工具结果、下载授权、bootstrap 注入和部署链路上返工。

---

## 1. 总结判断

Claude Code 的 `11-深度源码审核报告.md` 大部分意见成立，但还不够贴合当前 Hive 代码。它指出了方向性问题，比如 `ToolResultCard`、asset proxy、worker 超时、Docker 打包；本次审核进一步确认了这些问题在当前代码里的真实落点，并补充了几个会直接阻断实施的接口事实：

- `AssetService.Upload` 当前按 `(namespace, content_hash, owner_scope, owner_id)` 提前去重，命中后直接返回旧 URI，不会追加本次 run 的 tags。计划里“靠 `presentation_run_id + presentation_format` tags 做 pending/committed、重试复用和 GC”的方案在现有接口上不可实现。
- 正常 tool result 不走 `MessageBubble` 内部的独立 `ToolResultCard`。`MessageList` 会把 role=`tool` 且匹配 assistant tool call id 的消息隐藏，并合并进 `ToolAdapter`。所以 artifact 工具卡必须接入 `ToolAdapter`。
- `ToolResultBudgetCompactor` 默认 20KB 裁剪旧工具输出，`ToolExecutionBlock` 展示也截断到 2000 字符。`generate_ppt` / `generate_diagram` 不能返回大段 HTML/SVG，只能返回轻量 manifest + run id + asset URI。
- `/api/v1/assets/proxy` 当前只校验 `uri/expires/sig`，随后直接 `Download`，不会重新跑 `AccessResolver`；同时 `Content-Disposition` 硬编码 inline。新增下载语义必须让 `purpose/disposition/filename` 参与签名，且签名只能由已通过 resolver 的 `/assets/resolve` 生成。
- `ObjectStore.SignedURL(ctx,key,ttl)` 没有 response header 选项。S3/MinIO 直签 URL 无法按当前接口设置 attachment filename。Presentation/Diagram 下载必须选择“所有下载统一走 Hive proxy”或扩展 `ObjectStore.SignedURL` 签名选项；首发推荐前者。
- `ServerComponents`、`api.Server`、`cmd/server/main.go` 当前都没有 Presentation/Diagram service 注入点；routes 也没有 run 查询端点。计划必须把这些当前代码改点列清楚。
- `mcpOperationTimeout=30s` 主要约束远程 MCP 客户端连接/调用链路，不是本地 built-in tool 的直接执行超时。`generate_ppt` 若作为本地内置工具注册，当前主要超时来自 `Master.executeTool` 的 `RuntimePolicy.ToolTimeout`，默认 2 分钟。异步 run 设计仍然必要，但 30s MCP 超时不应被写成 local built-in tool 的唯一阻断根因。

实施前评分：按当前文档状态，工程可实施性约 7/10。补完本文 P0/P1 后才可进入 Phase 1。

---

## 2. P0 阻断项

### P0-1：Asset 去重会破坏 run 级 provenance 和 tags

证据：

- `internal/asset/service.go` 中 `Upload` 先计算 content hash，再调用 `GetByHashForOwner(namespace, hash, owner_scope, owner_id)`；命中后直接 `return AssetURIFromObjectKey(rec.Key)`。
- `internal/asset/pg_meta_store.go` 的 `Save` 虽然在 DB conflict 时合并 tags，但早返回路径不会调用 `Save`。
- `internal/asset/store.go` 的 `AssetMetaStore` 只有 `Save/Get/ListByNamespace/Delete`，没有 `ListByTags`、`UpdateTags` 或 run 级 metadata 更新接口。

影响：

- 如果 namespace 固定为 `presentations` / `diagrams`，同一用户两个 run 生成相同 HTML/PPTX/SVG，会复用同一个 asset URI。第二个 run 的 `presentation_run_id` / `diagram_run_id` tag 不会写入。
- 计划中“按 `presentation_run_id + presentation_format` 查 pending/committed asset”无法用现有 store 实现。
- GC 如果依赖 asset tags，会漏删或误删；授权如果依赖 tags，会把旧 run 的 tag 当成本次 run 事实。

必须修正：

1. 首发默认使用 run-scoped namespace：

   ```text
   presentations/user/<owner_id>/run/<run_id>
   diagrams/user/<owner_id>/run/<run_id>
   ```

2. `presentation_runs` / `diagram_runs` 是 canonical truth。run 表字段里的 `deck_spec_asset_uri/html_asset_uri/pptx_asset_uri/source_asset_uri/svg_asset_uri/...` 才是授权、恢复、GC 的事实源。
3. asset tags 只做宽松索引和排查线索，不作为 pending/committed 的唯一事实。
4. 如果坚持全局 namespace 去重，必须先扩展 `AssetMetaStore`：`ListByTags`、`UpdateTags`、可表达“同一 object 多个 logical references”的表结构，并补并发测试。首发不建议走这条重改路径。
5. 必须新增测试：同一用户两个不同 run 上传完全相同 bytes，应得到不同 run namespace 下的可追踪 URI，或证明两个 run 表都能安全引用同一对象且 GC/授权不依赖丢失 tag。

### P0-2：工具结果不能携带完整 HTML/SVG

证据：

- `internal/compaction/tool_budget.go` 默认单条工具输出超过 20KB 会被旧上下文裁剪成占位文本。
- `frontend/src/components/chat/ToolExecutionBlock.tsx` 展示工具输出时超过 2000 字符会截断。
- `MessageList` 会把 tool result 内容持久化进消息历史，后续回放仍依赖该轻量内容。

影响：

- 计划中 `html_preview` 100KB 甚至 512KB 的设计会污染消息存储和 LLM 上下文，还会在后续轮次被裁剪，模型无法基于它做稳定修改。
- SVG、MindMap JSON、Chart JSON 等也不能无限塞入 tool result。

必须修正：

- `generate_ppt` 工具结果只返回轻量 manifest，目标控制在 5KB 内：

  ```json
  {
    "kind": "presentation",
    "run_id": "prun_...",
    "status": "succeeded",
    "title": "...",
    "html_asset_uri": "asset://...",
    "pptx_asset_uri": "asset://...",
    "deck_spec_asset_uri": "asset://...",
    "slide_count": 8,
    "warnings": []
  }
  ```

- `generate_diagram` 同理只返回 source/svg/png/html asset URI、run id、状态和 warnings。
- HTML/SVG/源码打开 Canvas 时通过 run API 或 asset resolve/fetch 拉取。
- 如果为了首屏体验保留 `html_preview/source_preview`，必须降为调试优化字段，硬上限 8KB，且不得作为唯一数据源。首发计划建议完全移除 `html_preview`。

### P0-3：前端工具结果路径必须接入 `ToolAdapter`

证据：

- `frontend/src/components/chat/MessageList.tsx` 的 `isIntegratedToolResult` 会跳过 integrated role=`tool` 消息独立渲染。
- tool result 被收集到 `toolResults` map，再传给 assistant 气泡中的 `MessageBubble`。
- `MessageBubble` 内 `ToolCallRow` 最终渲染 `ToolAdapter`。
- `frontend/src/components/chat/ToolAdapter.tsx` 当前只特殊处理 Todo 和 KB，其他工具落入 `ToolExecutionBlock`。

影响：

- 计划里“`ToolResultCard` parse `kind === "presentation"`”是错路径。正常 tool call 下，`MessageBubble` 内部 standalone `ToolResultCard` 不会显示。
- 如果只改 `ToolResultCard`，Chat 不会出现“预览 / 下载 PPTX / 下载 SVG”按钮。

必须修正：

1. 新增 `ArtifactToolResultCard` 或 `GeneratedArtifactResultCard`，由 `ToolAdapter` 在成功状态下优先分派：

   ```tsx
   if (!hasError && resolvedStatus === 'success' && isGeneratedArtifactTool(name, result)) {
     return <GeneratedArtifactResultCard name={name} result={result} sessionId={sessionId} />;
   }
   ```

2. `generate_ppt` / `generate_diagram` 的 running 状态也要有卡片，不应只显示 generic running chip；卡片需要轮询 run 并原地升级。
3. `ToolExecutionBlock` 只保留为 generic fallback，不承载 artifact 产品 UI。
4. 前端测试应覆盖 `ToolAdapter`，而不是只覆盖 `ToolResultCard`。

### P0-4：下载 attachment 不能只改前端 `<a download>`

证据：

- `/api/v1/assets/resolve` 当前调用 `ResolveAsset` 后只对 local URL 走 `rewriteLocalAssetURL`。
- `/api/v1/assets/proxy` 当前只校验 `uri/expires/sig`，然后 `assetService.Download`，并硬编码 `inline; filename=...`。
- `ObjectStore.SignedURL` 只有 `SignedURL(ctx,key,ttl)`，没有 response content disposition 选项。S3/MinIO 直签 URL 无法按现有接口设置 attachment。

影响：

- 前端 `<a download>` 对跨域或 S3 signed URL 不可靠，且无法控制服务端 header。
- 如果只对 local proxy 支持 attachment，MinIO/S3 部署会下载行为不一致。
- proxy bypass resolver，签名参数必须覆盖 disposition/filename/purpose，否则用户可篡改 inline 预览链接为 attachment 下载，或注入文件名。

必须修正：

- 首发采用统一下载 proxy：`purpose=*_download` 的 resolve 结果一律返回 Hive proxy URL，不论底层 provider 是 local、MinIO 还是 S3。
- 预览可以继续使用直签 URL，但 presentation/diagram download 必须经过 proxy，保证 attachment、audit 和 header 一致。
- `uri/expires/purpose/disposition/filename` 全部进入 HMAC。建议也把 `mime` 或 `format` 纳入签名，避免后续 handler 分支漂移。
- proxy handler 只接受 `inline|attachment` 白名单 disposition，filename 过滤 CR/LF、路径分隔符和控制字符。
- proxy 不重新跑 resolver是可以接受的，但前提是签名只能由 `/assets/resolve` 在 resolver 放行后生成，TTL 继续短。

### P0-5：API/bootstrap/service 注入点缺失

证据：

- `internal/bootstrap/server.go` 的 `ServerComponents` 只有 `AssetService/AssetAccessResolver/KBService`，没有 Presentation/Diagram service。
- `internal/api/server.go` 没有 presentation/diagram service 字段和 setter。
- `cmd/server/main.go` 目前只给 API server 注入 asset/kb/access resolver 等既有服务。
- `internal/api/routes.go` 没有 `/api/v1/presentation/runs/{run_id}` 或 `/api/v1/diagram/runs/{run_id}`。

必须修正：

- `ServerComponents` 增加 `PresentationService`、`DiagramService` 或统一 `ArtifactRunService`。
- `api.Server` 增加对应窄接口字段和 `SetPresentationService` / `SetDiagramService`。
- `cmd/server/main.go` 在 API server 创建后注入新 service。
- `routes.go` 注册：

  ```text
  GET /api/v1/presentation/runs/{run_id}
  GET /api/v1/diagram/runs/{run_id}
  ```

- `initAssetAccessResolver` 如果要按 run 表校验 asset，签名应接收 run reader：

  ```go
  initAssetAccessResolver(logger, kbService, presentationRunReader, diagramRunReader)
  ```

### P0-6：output schema 不是强合同

证据：

- `internal/mcphost/convert.go` 的 `MCPToOpenAI` 不传 output schema。
- `internal/mcphost/output_schema.go` 只做合法 JSON 和顶层 required key 的轻量校验，诊断不阻断。
- 前端现在对 generic tool result 只是字符串展示，完全依赖业务卡片自行 parse。

必须修正：

- `generate_ppt` / `generate_diagram` executor 必须用 Go 结构体 marshal 返回，不允许拼 JSON 字符串。
- 工具内部返回前运行强校验，失败直接转为 `presentation_error` / `diagram_error`，不要依赖 Host output schema。
- 前端使用 TypeScript runtime parser，例如 `parseGeneratePPTResult(result: string): GeneratePPTResult | null`，严格检查 `kind/status/run_id/asset_uri`。
- 测试重点放在 typed marshal + parser，而不是只声明 output schema。

---

## 3. P1 高优先级缺口

### P1-1：RuntimeContext 不能假设 `auth.UserIDFrom(ctx)` 总是存在

当前 Master 执行工具时注入了 `toolctx.WithSessionID`、trace context 和 `tools.WithKBRuntimeContext`。`KBRuntimeContext` 有 owner/session/domain/tenant/agent facts，但没有显式 `UserID`。auth 关闭或 CLI/dev 场景下，API request ctx 里可能没有 user。

修正：

- 工具 runtime 应优先读取 `KBRuntimeContext.OwnerID/OwnerScope/SessionID/DomainID` 和 `toolctx` trace facts。
- `UserID` 若为空，可在 owner_scope=user 且 owner_id 非空时作为 local/dev fallback，但必须记录 warning。
- worker 从 DB 恢复时，不依赖请求 ctx；必须从 run record 重建 runtime facts。

### P1-2：`mcpOperationTimeout=30s` 的解释需要修正

Claude 报告说 MCP 工具默认 30s 超时。对远程 MCP client 这成立，但本计划要注册的是本地 built-in tool，当前更直接的超时在 `Master.executeTool`：普通工具默认受 `RuntimePolicy.ToolTimeout` 限制，未配置时是 2 分钟。

修正：

- 计划不要写“本地 `generate_ppt` 必然受 30s MCP 超时失败”。
- 仍然要求工具调用快速返回：创建 run、启动/唤醒 worker、等待 `sync_timeout_seconds`，未完成就返回 `running`。
- `sync_timeout_seconds` 必须小于 `RuntimePolicy.ToolTimeout`，并在 config normalize/validate 中校验。
- 如果未来把 exporter 做成远程 MCP service，再单独处理 `mcpOperationTimeout` 或远程 client per-tool timeout。

### P1-3：tool schema 的 `$defs/$ref/oneOf` provider 兼容性未验证

`internal/llm/tools.go` 把 input schema 原样传给 provider。不同 provider 对 `$ref/$defs/oneOf/const` 支持差异很大。

修正：

- 首发生成 provider-compatible schema：优先扁平化 `$ref`，限制 `oneOf` 层级。
- 对项目实际启用的 LLM provider 做 schema smoke test。
- 如果不能保证 provider 支持复杂 schema，工具输入改为 `deck` 的较扁平对象 + 服务端强 validator，模型失败通过 JSON pointer 修复。

### P1-4：Canvas 数据模型缺少 run 级去重/更新

当前 `openArtifact` 用 title、language、assetUri、content 全量去重。running -> succeeded 更新时，如果 content 或 assetUri 变化，很容易打开第二个 tab。

修正：

- `Artifact` 改判别联合后，`presentation/diagram/mindmap` 用 `metadata.kind + metadata.runId` 去重。
- `openArtifact` 对同 run id 应更新现有 artifact metadata，而不是追加。
- running artifact 轮询成功后原地升级。

### P1-5：Canvas 声明了 Mermaid/SVG 但没有预览 renderer

当前 `ArtifactType` 包含 `svg`、`mermaid`，但 `CanvasPanel.PreviewContent` 只处理 html/markdown/json/ppt/code，其余落入 no preview。

修正：

- Phase 6/7 前置新增 `MermaidRenderer`、`SvgRenderer` 或统一 `DiagramRenderer`。
- 普通 `<artifact type="mermaid">` 至少能 Canvas 预览和下载 `.mmd`。
- 但正式 SVG/PNG 下载只能来自 `generate_diagram` 的 run/asset。

### P1-6：Mermaid SVG sanitize 不足

当前 `MermaidBlock` 只移除 `<script>` 和 `on*` 属性。还缺：

- `<foreignObject>`
- `javascript:` / `data:text/html` URL
- 外链 image/style/link
- Mermaid click action 和 HTML label 产生的危险结构

修正：

- 抽出共享 sanitize helper，Chat 和 Canvas 都复用。
- 服务端 diagram worker 也做 sanitize，前端再次 defense-in-depth。
- 恶意 fixture 必须覆盖上述节点/属性。

### P1-7：Docker/部署计划还没落到当前 Dockerfile

当前 runtime 镜像有 node/npm/chromium，但只复制 `/hive` 和 config，没有 `tools/presentation-exporter` / `tools/diagram-exporter`，仓库目前也没有顶层 `tools/` 目录。

修正：

- 新建 `tools/` 后 Dockerfile runtime stage 必须显式 COPY worker 目录。
- `npm ci --omit=dev` 或构建 bundle 必须进入 Dockerfile/CI。
- worker path 默认值要区分本地开发和容器路径，如 `/app/tools/presentation-exporter/src/exporter.mjs`。

### P1-8：Config 缺少 Presentation/Diagram 配置与校验

当前 `config.Config` 没有 `PresentationConfig` / `DiagramConfig`。`Default()`、`SetDefaults()`、`Resolve()`、env override、`config.example.json` 都没有对应字段。

修正：

- 新增配置结构和默认值。
- `sync_timeout_seconds < timeout_seconds`、`max_concurrent_workers > 0`、`temp_dir` 可写、worker script 存在等必须 normalize/validate。
- auth/store/asset 不满足时，工具可见但返回结构化 unavailable，还是隐藏工具，需要在配置合同中明确。首发推荐工具可见但调用返回结构化错误，便于用户知道部署缺口。

### P1-9：Router/action guard 默认权限需要同步

当前 `capability_registry.go` 有 `generate_image/generate_video/text_to_speech`，没有 `generate_ppt/generate_diagram`。如果 action guard 或 tool policy 开启，新工具可能被默认拦截或缺少风险分类。

修正：

- 增加 builtin tool rules：`Domain:"media"`, `RiskLocalWrite`, `SideEffect:true`。
- 检查 default permission rules / host tool set 是否需要把它们纳入可见或允许组。
- 增加 router tests，验证不会被误归类为 runtime exec 或 external write。

### P1-10：GC 不能依赖当前 AssetMetaStore tags

当前没有 tag query/update，也没有 presentation/diagram run GC 参考实现。现有 asset GC 是 orphan object 级别，不等于 run 级过期清理。

修正：

- 首发 run GC 以 run 表 canonical URI 为准：扫描 expired/failed runs，逐个删除 run 字段中记录的 asset URI。
- admin endpoint dry-run 先行；自动 goroutine 可以后置。
- 不要写“按 tags 找 pending asset”作为首发必选路径，除非先扩展 store。

---

## 4. 当前代码适配后的主链路

```text
User asks PPT / diagram
  -> LLM chooses generate_ppt / generate_diagram
  -> Tool executor derives RuntimeContext from toolctx + KB runtime + auth fallback
  -> Create run row (presentation_runs / diagram_runs)
  -> Synchronous wait up to sync_timeout_seconds
      -> Worker validates spec
      -> Resolves images/source
      -> Renders HTML/SVG/PPTX
      -> Uploads assets under run-scoped namespace
      -> Atomically writes asset URIs to run row
  -> Tool returns small manifest only
  -> MessageList integrates tool result into ToolAdapter
  -> ToolAdapter renders GeneratedArtifactResultCard
  -> Card opens Canvas with runId metadata
  -> Canvas fetches run/assets by purpose
  -> Download goes through signed Hive proxy for attachment
```

---

## 5. 必须回写到计划的修改

本审核要求主计划做这些硬修：

1. 所有 `presentations` / `diagrams` 全局 namespace 默认改为 run-scoped namespace。
2. 删除或降级 `html_preview` / `source_preview` 大字段；tool result 只返回轻量 manifest。
3. 前端 artifact 工具卡从 `ToolResultCard` 改为 `ToolAdapter` 优先分派。
4. 下载 attachment 改成“resolve 放行后生成 signed proxy URL”，且 `purpose/disposition/filename` 参与 HMAC。
5. S3/MinIO 下载首发统一走 Hive proxy，或明确扩展 `ObjectStore.SignedURL` 选项；二选一不能模糊。
6. 增加 API/bootstrap/cmd/server/service setter/routes 的确切文件清单。
7. output schema 不再当强校验；改 typed result + runtime parser。
8. 修正 30s MCP timeout 表述：local built-in 主要受 Master tool timeout，远程 MCP 才受 `mcpOperationTimeout`。
9. Phase 0 验收增加“当前 Hive 代码适配合同”闸门，未通过不得进入实现。

