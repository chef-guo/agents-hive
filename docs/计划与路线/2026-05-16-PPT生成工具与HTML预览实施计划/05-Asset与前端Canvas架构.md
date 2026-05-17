# Asset 与前端 Canvas 架构

> 上级索引：[2026-05-16-PPT生成工具与HTML预览实施计划.md](../2026-05-16-PPT生成工具与HTML预览实施计划.md)

### 4.4 Asset 存储

上传选项：

- namespace：`presentations/user/<owner_id>/run/<run_id>`
- JSON MIME：`application/json`
- PPTX MIME：`application/vnd.openxmlformats-officedocument.presentationml.presentation`
- HTML MIME：`text/html`
- tags：
  - `source_kind=generated_presentation`
  - `presentation_run_id=prun_...`
  - `presentation_style=swiss`
  - `presentation_format=deckspec|html|pptx`
  - `presentation_mode=editable`
  - `session_id=<session>`
  - `turn_id=<turn>`
  - `domain_id=<domain>`

下载：

- 前端通过 `/api/v1/assets/resolve?uri=...&session_id=...&purpose=presentation_download` 获取短期 URL。
- `presentation_download` 首发所有 provider 都走 Hive signed proxy，包含 local、MinIO、S3。当前 `ObjectStore.SignedURL(ctx,key,ttl)` 没有 response header 选项，不能可靠设置 S3/MinIO attachment filename。
- 预览可继续使用底层 signed URL；下载统一由 proxy 设置 attachment、filename 和 audit。
- 当前 Hive asset 上传按 namespace/content hash/owner 早返回去重，run-scoped namespace 是首发硬约束。run 表里的 asset URI 是 canonical truth，tags 只是排查线索。

Diagram 上传选项：

- namespace：`diagrams/user/<owner_id>/run/<run_id>`
- Mermaid source MIME：`text/vnd.mermaid`，若 provider 不支持该 MIME，则 fallback `text/plain; charset=utf-8` 并保留 `diagram_format=source` tag。
- MindMapSpec JSON MIME：`application/json`
- Mindmap Markdown MIME：`text/markdown`
- SVG MIME：`image/svg+xml`
- PNG MIME：`image/png`
- HTML MIME：`text/html`
- tags：
  - `source_kind=generated_diagram`
  - `diagram_run_id=drun_...`
  - `diagram_type=mermaid|mindmap`
  - `diagram_format=source|svg|png|html`
  - `diagram_source_format=mermaid|mindmap-json|mindmap-markdown`
  - `session_id=<session>`
  - `turn_id=<turn>`
  - `domain_id=<domain>`

Diagram 下载：

- 前端通过 `/api/v1/assets/resolve?uri=...&session_id=...&purpose=diagram_preview` 获取 SVG/HTML 预览 URL。
- 前端通过 `/api/v1/assets/resolve?uri=...&session_id=...&purpose=diagram_download` 获取 source/SVG/PNG/HTML 下载 URL。
- `diagram_download` 默认返回 attachment；Canvas 内 SVG preview 使用 `diagram_preview`，避免用户点击预览链接触发下载。
- `diagram_download` 同样所有 provider 都走 Hive signed proxy；不要依赖 S3/MinIO signed URL response header。
- 文件名使用安全 slug：
  - Mermaid source：`<title>.mmd`
  - Mermaid SVG：`<title>.svg`
  - Mindmap JSON：`<title>.mm.json`
  - Mindmap Markdown：`<title>.md`
  - Mindmap SVG/PNG/HTML：`<title>.svg|png|html`

### 4.4.1 Presentation asset 访问策略

当前 asset resolver 对非 KB 资产主要校验 owner，一旦用户拥有该资产，不区分 purpose 和 format。Presentation 首发必须补一个专门分支：

```go
if rec.Tags["source_kind"] == "generated_presentation" {
    return r.canResolvePresentationAsset(ctx, rec, rc)
}
```

实现位置必须是 `internal/bootstrap/asset.go` 的 `assetAccessResolver.CanResolveAsset` / `initAssetAccessResolver` 返回对象内部。当前 `AssetService.ResolveAsset` 只接受单个 `AccessResolver`，不能通过“新增 resolver 并追加到链上”的方式实现。

`canResolvePresentationAsset` 规则：

- `rc.UserID` 必须非空。
- `rc.OwnerScope == "user"`，`rc.OwnerID == rc.UserID`。
- `rec.OwnerScope == "user"`，`rec.OwnerID == rc.OwnerID`。
- `rc.Purpose` 必须是 `presentation_preview`、`presentation_download` 或 `presentation_audit`。
- `presentation_download` 只允许 `presentation_format=pptx`；HTML/DeckSpec 不能走 PPTX 下载按钮。
- `presentation_preview` 只允许 `presentation_format=html`。
- `presentation_audit` 首发只允许后端内部调用，不暴露给普通前端。
- 如果 `rec.Tags["session_id"]` 非空且 `rc.SessionID` 非空，二者必须匹配。
- 如果 `rec.Tags["session_id"]` 非空但 `rc.SessionID` 为空，同 owner 仍可下载 PPTX，用于历史消息和资产列表；必须记录审计字段。
- 如果 `rec.Tags["domain_id"]` 非空，`rc.DomainID` 为空时允许 user-owned direct preview；`rc.DomainID` 非空时必须匹配。
- `ttl` 保持短期，默认 5 分钟，最大不超过现有 asset resolver 上限。

必须新增测试：

- 同 owner + `presentation_download` + `pptx` 通过。
- 同 owner + `presentation_download` + `html` 拒绝。
- 同 owner + `presentation_preview` + `html` 通过。
- 同 owner + 错 `session_id` 拒绝。
- 同 owner + 空 `session_id` + 历史下载路径通过并记录审计。
- 错 owner 拒绝。
- 匿名用户拒绝。
- 普通 `user_asset` purpose 不能下载 `generated_presentation`。

### 4.4.1.1 Diagram asset 访问策略

Diagram asset 复用同一 asset resolver 扩展点，但必须单独区分 `source_kind=generated_diagram`。

规则：

- `rc.UserID` 必须非空。
- `rc.OwnerScope == "user"`，`rc.OwnerID == rc.UserID`。
- `rec.OwnerScope == "user"`，`rec.OwnerID == rc.OwnerID`。
- `rc.Purpose` 必须是 `diagram_preview`、`diagram_download` 或 `diagram_audit`。
- `diagram_preview` 允许 `diagram_format=svg|html`。
- `diagram_download` 允许 `diagram_format=source|svg|png|html`，其中 `html` 下载必须是 attachment 且只能下载后端生成的自包含 HTML。
- `diagram_audit` 首发只允许后端内部调用。
- session/domain 匹配规则与 presentation 相同。

必须新增测试：

- 同 owner + `diagram_preview` + svg 通过。
- 同 owner + `diagram_download` + source/svg 通过。
- 同 owner + `diagram_download` + html 返回 attachment。
- 同 owner + `diagram_preview` + source 拒绝。
- 修改 diagram download proxy URL 的 `filename/disposition` 后签名失效。
- 普通 `user_asset` purpose 不能 resolve generated diagram。
- 错 owner/错 session/匿名用户拒绝。
- Mermaid source asset 不能通过 presentation purpose 下载。

### 4.4.2 下载响应语义

`/api/v1/assets/resolve` 当前返回短期 URL。Presentation/Diagram 正式下载不能依赖浏览器的 `<a download>` 属性决定文件名和 attachment 语义；这些语义必须由服务端 signed Hive proxy 的响应头保证。前端只负责跳转或点击已签名的下载 URL。

- PPTX 文件名使用安全 slug：`<title>-editable.pptx` 或 `<title>-visual.pptx`。
- proxy 响应 Content-Type 必须是 PPTX MIME。
- `presentation_download` 必须返回 `attachment; filename="..."`。
- HTML 预览 asset 在 M2 不作为普通用户 attachment 下载；若 M3 要公开“下载 HTML”，必须新增明确 purpose 或扩展 `presentation_download` 的 format 白名单，并让 filename/disposition/purpose 一起签名。
- 首发不出现 `<title>-visual.pptx` 用户下载文件；visual 文件名规则只为内部 QA 和后续公开入口保留。

实现决策：

- 扩展 `/api/v1/assets/resolve`：当 `purpose=presentation_download` 且 format 为 pptx 时，生成 proxy URL 时追加 `disposition=attachment` 和安全 `filename`。
- 扩展 `/api/v1/assets/resolve`：当 `purpose=diagram_download` 时，按 `diagram_format/source_format` 追加 attachment filename；当 `purpose=diagram_preview` 时保持 inline。
- 修改现有 `/api/v1/assets/proxy` handler：当前有文件名时硬编码 `inline; filename=...`，必须改为读取签名覆盖的 `purpose`、`disposition` 和 `filename` 后设置 `Content-Disposition`。不要新增平行下载 endpoint。
- `purpose`、`disposition`、`filename`、`uri`、`expires` 都必须参与 HMAC 签名，防止用户把 inline 链接篡改为 attachment 或改文件名注入 header。
- proxy handler 可以不重新跑 resolver，但只有 `/assets/resolve` 在 resolver 放行后才能签发 proxy URL，TTL 继续使用现有短期限制。
- `filename` 必须经过 CR/LF、路径分隔符、控制字符过滤；空文件名 fallback 为 `presentation-editable.pptx`。
- 非 presentation download 默认保持现有 inline 行为。

proxy URL 示例：

```text
/api/v1/assets/proxy?uri=asset%3A%2F%2Fpresentations%2F...&expires=...&purpose=presentation_download&disposition=attachment&filename=demo-editable.pptx&sig=...
```

必须新增测试：

- `purpose=presentation_download` 的 proxy 响应为 `Content-Disposition: attachment`。
- 普通 HTML preview proxy 仍为 inline。
- 修改 `purpose`、`disposition` 或 `filename` 后签名失效。
- 文件名中的 CR/LF 和路径分隔符被清理。

### 4.4.3 保留期、清理与配额

PPT 生成会产生 DeckSpec、HTML、PPTX、图片缓存和临时文件，首发必须定义保留与配额，避免 asset 表和对象存储无限增长。

保留期：

- `presentation_runs.expires_at` 默认 `created_at + 30 days`。
- `diagram_runs.expires_at` 默认 `created_at + 30 days`。
- DeckSpec JSON、HTML、PPTX 三类 asset 的对象保留期默认跟随 run。
- Diagram source、SVG、PNG、HTML asset 的对象保留期默认跟随 run。
- failed run 如果没有 PPTX asset，保留 7 天用于排查。
- failed diagram run 如果没有 SVG/PNG asset，保留 7 天用于排查。
- 临时目录只保留到 run 完成；worker 失败也必须清理，清理失败记录 `cleanup_warning`。

清理任务：

- 复用现有 admin asset GC 能力时，presentation scope 首发按 `presentation_runs` 的 canonical asset URI 字段和 run-scoped namespace 找到可删对象，不依赖当前 `AssetMetaStore` 不支持的 tag query/update。
- 同一 GC 能力增加 diagram scope：按 `diagram_runs` 的 canonical asset URI 字段和 run-scoped namespace 找到可删对象。
- 如果未来要按 `presentation_run_id` / `diagram_run_id` tags 查询 pending asset，必须先扩展 `AssetMetaStore` 的 `ListByTags` / `UpdateTags` 或新增 logical reference 表。
- `presentation_runs` 过期后先删除 asset，再删除 run 记录或标记 `expired`；首发采用硬删除前必须确保 audit 日志已记录 `run_id/user_id/error_kind`。
- `diagram_runs` 过期后先删除 asset，再删除 run 记录或标记 `expired`；首发采用硬删除前必须确保 audit 日志已记录 `run_id/user_id/error_kind`。
- 清理任务默认 dry-run，可由 admin endpoint 或定时任务触发。

配额与限流：

- 每个用户并发 running presentation run 首发上限 2。
- 每个用户并发 running diagram run 首发上限 4。
- 每个用户每日成功生成 PPT 首发上限读取现有 quota/usage 体系；若没有对应 quota，新增 `presentation.daily_runs` 默认 20。
- 每个用户每日成功生成 diagram 首发上限读取现有 quota/usage 体系；若没有对应 quota，新增 `diagram.daily_runs` 默认 100。
- 单 run 最大 30 页、50MB PPTX、总图片 100MB、单图 10MB、小体积 data URI 1MB。
- 单 diagram run 最大 100KB Mermaid source、512KB MindMapSpec JSON、300 mindmap nodes、2MB SVG、10MB PNG。
- 超过配额返回 `quota_exceeded`，`recoverable=false`；超过内容限制返回 `invalid_spec` 或 `image_too_large`，模型可修复时 `recoverable=true`。

必须新增测试：

- 同一用户第 3 个 running run 被拒绝。
- 同一用户第 5 个 running diagram run 被拒绝。
- 过期 failed run 可被 GC dry-run 列出。
- 过期 failed diagram run 可被 GC dry-run 列出。
- GC 不删除未过期 run 的 asset。
- GC 不删除未过期 diagram run 的 asset。
- 超过 50MB PPTX 标记 failed 且不会上传半成品。
- 超过 2MB SVG 或 10MB PNG 标记 failed 且不会上传半成品。

### 4.5 前端 UI

新增或修改：

```text
frontend/src/store/canvas.ts
frontend/src/utils/artifactParser.ts
frontend/src/components/chat/ArtifactCard.tsx
frontend/src/components/chat/MessageBubble.tsx
frontend/src/components/chat/ToolAdapter.tsx
frontend/src/components/chat/GeneratedArtifactResultCard.tsx
frontend/src/components/canvas/CanvasPanel.tsx
frontend/src/components/canvas/deck/parseDeck.ts
frontend/src/components/canvas/renderers/DeckRenderer.tsx
frontend/src/components/canvas/renderers/DiagramRenderer.tsx
frontend/src/components/canvas/renderers/MermaidRenderer.tsx
frontend/src/components/canvas/renderers/MindMapRenderer.tsx   # 默认 M3 或 M2 前移 feature flag
frontend/src/components/canvas/renderers/HtmlRenderer.tsx
frontend/src/types/api.ts
frontend/src/hooks/useAsset.ts
frontend/src/hooks/usePresentationRun.ts
frontend/src/hooks/useDiagramRun.ts
```

行为：

- 新增 `ArtifactType = 'presentation' | 'diagram' | 'mindmap'`，保留现有 `html` 和 `mermaid`。
- `mermaid` 旧类型作为源代码 artifact 兼容；工具生成的 Mermaid 应优先归一为 `type:'diagram'`、`metadata.diagramType:'mermaid'`。
- 当前正常 tool result 会被 `MessageList` 合并到 assistant 气泡并走 `ToolAdapter`；`MessageBubble.tsx` 内部的 standalone `ToolResultCard` 不是 generate_ppt/generate_diagram 的主路径。首发必须在 `ToolAdapter` 里优先分派 `GeneratedArtifactResultCard`，generic `ToolExecutionBlock` 只做 fallback。
- Presentation artifact 包含：
  - `htmlContent?: string`
  - `htmlAssetUri?: string`
  - `pptxAssetUri?: string`
  - `deckSpecAssetUri?: string`
  - `runId?: string`
  - `mode?: 'editable'`
  - `status?: 'running' | 'succeeded' | 'failed'`
  - `slideCount?: number`
  - `warnings?: string[]`
  - `trustedGenerated?: boolean`
- `GeneratedArtifactResultCard` 识别 `generate_ppt` 的 JSON 输出，显示：
  - 标题
  - slide 数
  - “预览”按钮
  - “下载 PPTX”按钮
  - warnings 简短提示
- `GeneratedArtifactResultCard` 识别 `generate_diagram` 的 JSON 输出，显示：
  - 标题
  - diagram 类型
  - “预览”按钮
  - “下载 SVG / 下载源文件”按钮
  - warnings 或渲染错误提示
- `DeckRenderer` 使用 iframe 预览受信 HTML：
  - 首发 sandbox 不加 `allow-scripts`，只渲染当前页静态 HTML。
  - 如后续开启受信页面内脚本，只能使用 `allow-scripts`，不加 `allow-same-origin`。
  - 禁止 forms、popups、top-navigation。
  - 若是非受信 HTML artifact，仍使用现有手动启用 scripts 的 `HtmlRenderer`。
- 借鉴 `html-anything/src/lib/deck.ts`，约定工具生成的 deck HTML 每页为顶层 `<section class="slide" data-slide-id="N" data-layout="Sxx">`：
  - `<head>` 必须包含 `<meta name="hive-deck-version" content="1">`。
  - `parseDeck` 使用 `DOMParser` 拆整份 HTML，不照搬 regex。
  - `DeckRenderer` 主区域只挂载当前页 iframe。
  - 底部缩略图首发默认用页码/标题缩略项，避免为所有页同时渲染 iframe。
  - `<aside class="notes">` 从 slide 中移除，作为 speaker notes 面板展示。
- 借鉴 `html-anything/src/components/deck-viewer.tsx` 的交互，但适配 Hive Canvas：
  - 左右键 / PageUp / PageDown / Home / End 翻页。
  - 全屏演示按钮。
  - notes 显示开关。
  - 页码和缩略图条。
- Canvas 下载按钮对 presentation 只提供公开 PPTX 下载；HTML asset 仅用于预览、开发调试或审计，普通用户 M2 不显示 HTML 下载入口。
- 工具成功、running、failed 三种结果必须都带 `status`，避免前端靠 asset URI 是否为空猜状态。

### 4.5.1 前端数据模型

当前 `Artifact` 只有 `content: string`，下载逻辑会把 `content` blob 成文件；presentation 需要 metadata。实现必须使用判别联合，不允许把 presentation metadata 长期保留为裸 `Record<string, unknown>`。

```ts
export type ArtifactType =
  | 'html'
  | 'markdown'
  | 'json'
  | 'svg'
  | 'csv'
  | 'mermaid'
  | 'code'
  | 'ppt'
  | 'presentation'
  | 'diagram'
  | 'mindmap';

export interface BaseArtifact {
  id: string;
  title: string;
  language: string;
  content: string;
}

export interface PresentationArtifactMeta {
  kind: 'presentation';
  runId: string;
  title: string;
  mode: 'editable';
  status: 'running' | 'succeeded' | 'failed';
  style: string;
  theme: string;
  slideCount: number;
  htmlAssetUri?: string;
  pptxAssetUri?: string;
  deckSpecAssetUri?: string;
  warnings?: string[];
  trustedGenerated: true;
}

export type PresentationArtifact = BaseArtifact & {
  type: 'presentation';
  metadata: PresentationArtifactMeta;
};

export interface DiagramArtifactMeta {
  kind: 'diagram';
  runId: string;
  title: string;
  diagramType: 'mermaid' | 'mindmap';
  status: 'running' | 'succeeded' | 'failed';
  sourceFormat: 'mermaid' | 'mindmap-json' | 'mindmap-markdown';
  sourceAssetUri?: string;
  svgAssetUri?: string;
  pngAssetUri?: string;
  htmlAssetUri?: string;
  warnings?: string[];
  trustedGenerated: true;
}

export type DiagramArtifact = BaseArtifact & {
  type: 'diagram' | 'mindmap';
  metadata: DiagramArtifactMeta;
};

export type StandardArtifact = BaseArtifact & {
  type: Exclude<ArtifactType, 'presentation' | 'diagram' | 'mindmap'>;
  metadata?: never;
};

export type Artifact = PresentationArtifact | DiagramArtifact | StandardArtifact;
```

Canvas store 去重和更新规则：

- `presentation` / `diagram` / `mindmap` artifact 必须按 `metadata.kind + metadata.runId` 去重。
- 同一个 run 从 `running` 变成 `succeeded` 或 `failed` 时，`openArtifact` 应原地更新 metadata，不新增 tab。
- 标准 artifact 继续沿用 title/language/assetUri/content 去重。
- 历史消息恢复时，如果 tool result 只剩轻量 manifest，也必须能通过 run id 重新补齐 asset URI。

工具结果类型也必须用判别联合：

```ts
export type GeneratePPTResult =
  | {
      kind: 'presentation';
      run_id: string;
      status: 'succeeded';
      title: string;
      mode: 'editable';
      style: string;
      theme: string;
      slide_count: number;
      deck_spec_asset_uri: string;
      html_asset_uri: string;
      pptx_asset_uri: string;
      warnings?: string[];
    }
  | {
      kind: 'presentation';
      run_id: string;
      status: 'running';
      title: string;
      mode: 'editable';
      slide_count?: number;
      warnings?: string[];
    }
  | {
      kind: 'presentation_error';
      run_id?: string;
      error_kind: string;
      message: string;
      recoverable: boolean;
      recoverable_by?: 'model' | 'user' | 'operator' | 'none';
      json_pointer?: string;
      repair_action?: string;
    };
```

Diagram 工具结果类型：

```ts
export type GenerateDiagramResult =
  | {
      kind: 'diagram';
      run_id: string;
      status: 'succeeded';
      title: string;
      diagram_type: 'mermaid' | 'mindmap';
      source_format: 'mermaid' | 'mindmap-json' | 'mindmap-markdown';
      source_asset_uri: string;
      svg_asset_uri?: string;
      png_asset_uri?: string;
      html_asset_uri?: string;
      warnings?: string[];
    }
  | {
      kind: 'diagram';
      run_id: string;
      status: 'running';
      title: string;
      diagram_type: 'mermaid' | 'mindmap';
      warnings?: string[];
    }
  | {
      kind: 'diagram_error';
      run_id?: string;
      error_kind: string;
      message: string;
      recoverable: boolean;
      recoverable_by?: 'model' | 'user' | 'operator' | 'none';
      json_pointer?: string;
      repair_action?: string;
    };
```

Canvas 下载规则：

- `artifact.type === 'presentation'` 且有 `pptxAssetUri`：resolve `purpose=presentation_download`，拿到 signed Hive proxy attachment URL 后触发浏览器下载；文件名和 attachment 语义以后端 `Content-Disposition` 为准。
- `artifact.type === 'presentation'` 的 HTML asset 只用于预览；M2 普通用户不提供“下载 HTML”。开发/审计若需导出 HTML，必须走单独受控入口，不能复用 `presentation_preview` 冒充 attachment 下载。
- `artifact.type === 'diagram'` 且有 `svgAssetUri`：resolve `purpose=diagram_download`，拿到 signed Hive proxy attachment URL 后下载 SVG。
- `artifact.type === 'diagram'` 且用户点“下载源文件”：resolve `sourceAssetUri` 并使用 `purpose=diagram_download`，Mermaid 下载 `.mmd`，Mindmap 下载 `.mm.json` 或 `.md`。
- `artifact.type === 'mindmap'` 启用后默认下载源 JSON，次级入口下载 SVG/PNG；若 Mindmap 未前移到 M2，M2 只保留类型合同、fixtures 和安全限制，不向普通用户显示 Mindmap 下载按钮。
- `artifact.type === 'mermaid'` 旧兼容路径仍下载 `.mmd`，但预览应走 `MermaidRenderer`，不再落入无预览状态。
- 其他类型保持现有 blob 下载。
- `ppt` 旧类型首发保留兼容：继续按 MarkdownRenderer 渲染并下载旧 `.md`，避免破坏历史消息。新工具结果一律使用 `presentation`，由 DeckRenderer 和 asset 下载处理；后续如统一迁移，必须有历史消息迁移脚本和回滚方案。
- `status:'running'` 时预览和下载按钮 disabled，显示生成中状态并轮询 run。
- 所有 presentation artifact 都必须保存 `runId`；即使工具已同步返回 succeeded，也要能用 run id 恢复状态。
- 所有 diagram/mindmap artifact 都必须保存 `runId`；即使工具已同步返回 succeeded，也要能用 run id 恢复 source/SVG/PNG/HTML asset URI。

### 4.5.2 DeckRenderer UX 约束

DeckRenderer 必须符合 `DESIGN.md` 的企业控制台风格：

- 顶部紧凑 toolbar：上一页、下一页、页码、缩略图开关、notes 开关、全屏、下载 PPTX。
- 按钮用 lucide icon，hover tooltip 说明；不使用 emoji。
- 不做营销式 hero、装饰渐变、卡片套卡片。
- slide 预览区域使用固定 16:9 aspect-ratio，避免翻页时布局跳动。
- 缩略图条高度固定，移动端改为横向小条或折叠按钮。
- notes 面板宽度/高度可控，移动端默认收起。
- 错误状态要显示具体动作：重新拉取 HTML、重新下载、复制错误 id。
- 所有可见文本走 i18n，新增 `zh.json/en.json` key。
- 缩略图首发不要为所有页同时渲染 iframe。默认显示页码/标题缩略项；如要渲染 iframe，只渲染当前页前后各 1 页，避免 Canvas 卡顿。

### 4.5.3 HTML 解析策略

`parseDeck` 可以借鉴 `html-anything` 的结构约定，但不要直接用 regex 作为主解析器。

要求：

- 使用 `DOMParser.parseFromString(html, 'text/html')`。
- 先检查 `<meta name="hive-deck-version" content="1">`；缺失或版本不匹配时返回结构化错误 `unsupported_deck_html_version`。
- 只接受顶层或 body 内的 `section.slide[data-slide-id][data-layout]`。
- 对每页复制原 `<head>` 中的 `<style>` 和安全 `<link rel="stylesheet">`；首发推荐后端生成内联 CSS，因此 link 应为空。
- 删除 `<aside class="notes">` 后再生成 per-slide HTML。
- 删除所有 `<script>`，除非 `trustedGenerated && allowScripts`。
- 若 slide 数为 0，返回结构化错误 `empty_deck_html`，Canvas 显示无法预览，不能空白。
- 若存在 slide 但缺 `data-slide-id` 或 `data-layout`，返回 `invalid_deck_html`，并显示复制 run id/错误详情入口。
- Go renderer 测试必须断言输出含 `hive-deck-version=1`；前端 `parseDeck` 单测覆盖正常 HTML、空 HTML、缺 meta、版本不匹配、缺 data-slide-id。
- Go 侧结构测试不要复制 `html-anything` 的 `SLIDE_RE`。使用 `golang.org/x/net/html` 解析 renderer 输出，断言 section/meta/aside/data-layout/data-image-slot 等结构。

### 4.5.4 Asset resolve hook

新增 `useAsset` 不应把 auth/owner 参数暴露给调用者：

```ts
resolveAsset({
  uri: result.pptx_asset_uri,
  purpose: 'presentation_download',
  sessionId: currentSessionId,
});
```

hook 职责：

- 调 `/api/v1/assets/resolve`。
- 只传 `uri/session_id/purpose/domain_id` 这类上下文参数。
- 不允许传 `owner_id/owner_scope`。
- 处理 403/404/503，返回用户可读错误。
- 对 PPTX/SVG/source 正式下载，只触发 `/assets/resolve` 返回的 signed Hive proxy attachment URL；不得把 asset 内容 fetch 成 Blob 再用 object URL 下载。普通非正式 artifact 仍可沿用现有 blob 下载。

### 4.5.5 Run 轮询 hook

新增 `usePresentationRun(runId)`，用于工具返回 `status:"running"` 后恢复结果。

行为：

- 调 `GET /api/v1/presentation/runs/{run_id}`。
- 默认每 2 秒轮询一次。
- 最长轮询 5 分钟；超过后停止并显示“生成仍在后台运行，可稍后刷新”。
- 页面卸载、Canvas 关闭、切换 session 时停止轮询。
- `status:"succeeded"` 时更新 artifact metadata，启用预览和下载按钮。
- `status:"failed"` 时显示 `error.message`、`error_kind` 和复制 run id 按钮。
- 如果 succeeded artifact 打开 Canvas 时 `htmlAssetUri` resolve/fetch 失败，自动 fallback 查询 run 状态；如果 run 仍 succeeded 但 asset resolve 失败，显示下载/预览错误，不清空 artifact。
- 历史消息恢复时，对所有 `metadata.kind === 'presentation'` 且有 `runId` 的 artifact 查询一次 run 状态，修正丢失的 asset URI、failed 状态或 warnings。
- 403/404 停止轮询并显示权限/不存在错误。
- 503 使用指数退避，最多重试 3 次。

测试：

- running -> succeeded 后按钮从 disabled 变 enabled。
- running -> failed 后显示结构化错误。
- 组件卸载后不再请求。
- 403 不重试。
- succeeded 工具结果响应丢失后，历史消息可通过 run id 恢复 artifact。
- succeeded artifact 的 HTML asset resolve 失败时会 fallback 查 run，不出现空白 Canvas。

新增 `useDiagramRun(runId)`，用于 `generate_diagram` 返回 `status:"running"` 后恢复结果。

行为：

- 调 `GET /api/v1/diagram/runs/{run_id}`。
- 默认每 2 秒轮询一次。
- 最长轮询 3 分钟；超过后停止并显示“图表仍在后台生成，可稍后刷新”。
- 页面卸载、Canvas 关闭、切换 session 时停止轮询。
- `status:"succeeded"` 时更新 artifact metadata，启用预览和下载按钮。
- `status:"failed"` 时显示 `error.message`、`error_kind`、`json_pointer` 和复制 run id 按钮。
- 如果 succeeded diagram artifact 打开 Canvas 时 `svgAssetUri/sourceAssetUri` resolve/fetch 失败，自动 fallback 查询 run 状态；如果 run 仍 succeeded 但 asset resolve 失败，显示下载/预览错误，不清空 artifact。
- 历史消息恢复时，对所有 `metadata.kind === 'diagram'` 且有 `runId` 的 artifact 查询一次 run 状态，修正丢失的 source/svg/png/html asset URI、failed 状态或 warnings。
- 403/404 停止轮询并显示权限/不存在错误。
- 503 使用指数退避，最多重试 3 次。

测试：

- running -> succeeded 后 Mermaid/Mindmap 下载按钮从 disabled 变 enabled。
- running -> failed 后显示结构化错误。
- 组件卸载后不再请求。
- 403 不重试。
- succeeded diagram 工具结果响应丢失后，历史消息可通过 run id 恢复 artifact。
- succeeded artifact 的 SVG asset resolve 失败时会 fallback 查 run，不出现空白 Canvas。

### 4.5.6 部分失败展示

如果 DeckSpec 和 HTML 已生成，但 PPTX export 失败：

- run 状态必须是 `failed`。
- `GeneratedArtifactResultCard` 可显示“预览 HTML”作为调试/临时查看入口，但下载 PPTX 必须 disabled。
- 对普通用户文案必须明确：“PPTX 生成失败，未生成可下载 PPTX”。
- 对开发/管理员可展开查看 `run_id/error_kind/json_pointer`。
- 不允许把 HTML artifact 自动当成 PPTX 成功结果。

### 4.5.7 DiagramRenderer / MindMapRenderer

DiagramRenderer 是 Canvas 的通用可视化图表入口，避免 Mermaid、脑图、后续 Graphviz/PlantUML 各自发明下载和错误状态。

里程碑边界：

- M2 必做 `DiagramRenderer + MermaidRenderer`，支持 Mermaid source/SVG 预览和下载。
- `MindMapRenderer` 默认属于 M3 用户可见能力。M0/M2 可以先冻结 `MindMapSpec`、fixtures、schema、安全限制和 behind-flag 骨架，但不能把未完成的 Mindmap 入口暴露给普通用户。
- 如果产品决定把 Mindmap 前移到 M2，必须同时前移 `MindMapRenderer`、服务端 SVG 导出、下载 purpose、历史恢复、Playwright 移动端验收和安全负测。

MermaidRenderer：

- 复用当前 `frontend/src/components/chat/MermaidBlock.tsx` 的懒加载策略，但抽出可复用 sanitize/render helper。
- 使用 Mermaid 11.15.0，`securityLevel:'strict'`。
- 渲染结果 SVG 必须再次 sanitize，移除 `<script>`、`on*`、外链资源和危险 URL。
- Canvas 中提供缩放、适配宽度、复制源码、下载 `.mmd`、下载 `.svg`。
- 渲染失败显示 `mermaid_parse_failed`，保留源码 tab，不能空白。

MindMapRenderer：

- 首发事实源优先 `MindMapSpec JSON`，可选支持 Markdown outline 转换。
- 前端用 `markmap-view` 做交互预览，`markmap-lib` 用于 Markdown -> tree 转换。
- 节点文本按纯文本处理；节点链接只允许 `https/http/mailto`，默认新窗口打开并加 `rel="noopener noreferrer"`。
- Canvas 中提供展开/折叠、适配视图、下载 `.mm.json`、下载 `.md`、下载 `.svg`。
- PNG 导出首发可由服务端 Playwright 生成，浏览器端 PNG 只作为 M3 增强。

测试：

- Mermaid 正常 flowchart 渲染非空 SVG。
- Mermaid 恶意 `click`/HTML/script 不执行，SVG sanitize 后不含事件属性。
- Mermaid 语法错误显示结构化错误和源码。
- MindMapSpec 正常渲染节点树。
- MindMapSpec 含危险链接被拒绝或清洗。
- Canvas 下载 Mermaid `.mmd/.svg`、Mindmap `.mm.json/.svg` 文件名和 MIME 正确。

### 4.5.8 plan-design-review 前端设计验收矩阵

本轮按 `plan-design-review` 对前端计划做文本审核，设计范围是 App UI，不是营销页。计划必须沿用 `DESIGN.md` 的工业化控制台风格：冷静、密集但可读、少装饰、lucide 图标、清晰状态和可恢复错误。当前设计完整度评估为 **8/10**；补齐下列矩阵后才能进入 Phase 6 实现。

已有 UI 资产必须复用：

- `CanvasPanel` 的右侧工作区、顶部 tablist、preview/code 分段控件、折叠侧栏。
- `ToolAdapter` 的 AI Elements `<Tool>` 外壳和 Hive 三态映射；`GeneratedArtifactResultCard` 只能作为业务 slot 接入，不另造一套聊天卡片体系。
- `DESIGN.md` 的 Geist / DM Sans / JetBrains Mono 字体层级、light-blue accent、zinc neutral、语义色、4px spacing、控制台密度。
- `lucide-react` 图标按钮和 hover tooltip；不使用 emoji、装饰渐变、卡片套卡片或营销式 hero。

信息架构必须固定为：

```text
Chat message
  -> ToolAdapter
    -> GeneratedArtifactResultCard
      -> primary actions: Preview, Download
      -> secondary actions: Source/HTML, Copy run id, Retry when allowed
  -> CanvasPanel
    -> artifact tabs
    -> compact toolbar
      -> preview/code/source mode
      -> renderer-specific controls
      -> download menu
    -> renderer viewport
      -> DeckRenderer | DiagramRenderer | MermaidRenderer | MindMapRenderer(when enabled)
    -> status/error/notes/source side panel
```

交互状态覆盖：

| Feature | Loading | Empty | Error | Success | Partial |
|---|---|---|---|---|---|
| `GeneratedArtifactResultCard` | 显示工具名、run id、stage/progress，Preview/Download disabled | 不出现空卡，缺 result 时退回 `ToolExecutionBlock` | 显示 `error_kind`、用户动作、复制 run id | 显示标题、类型、主按钮和 warnings | HTML/SVG 可预览但正式下载失败时只开放预览，下载 disabled |
| `DeckRenderer` | skeleton + “正在加载预览” + 可取消/重试 | `empty_deck_html` 结构化错误，不空白 | `unsupported_deck_html_version` / `invalid_deck_html` + 复制 run id | 16:9 viewport、页码、notes、缩略项、下载 PPTX | HTML 成功但 PPTX 失败时明确“未生成可下载 PPTX” |
| `DiagramRenderer` | SVG/source 拉取中，下载 disabled | source 为空显示 `empty_diagram_source` | parse/sanitize/resolve 错误保留 source tab | SVG 非空、缩放、适配宽度、source/SVG 下载 | source 可用但 SVG 失败时保留源码和重试 |
| `useAsset` 下载 | 按钮 spinner，禁止重复点击 | 无 asset URI 时按钮隐藏或 disabled | 403/404/503 显示权限/不存在/服务不可用 | 通过 signed Hive proxy 下载 | preview 可用但 download purpose 被拒时不改写 artifact |
| 历史恢复 | 首次打开按 run id 查询一次 | run 不存在显示历史记录不可恢复 | 403/404 停止轮询 | 原 tab 原地补齐 asset URI | 旧 manifest 缺字段时降级显示可解释错误 |

键盘、响应式和可访问性：

- Artifact tab 必须使用 `role=tablist/tab`、`aria-selected` 和可见 focus ring；左右键切换 tab，Enter/Space 激活。
- Deck 翻页支持 ArrowLeft/ArrowRight、PageUp/PageDown、Home/End；全屏模式 Esc 退出并恢复焦点。
- 所有 icon-only 按钮必须有 `aria-label` 和 tooltip；移动端点击目标不小于 44px。
- Running、failed、succeeded 状态变化使用 `aria-live="polite"` 区域播报；错误详情不能只靠颜色表达。
- 375px 移动端：Canvas 默认全宽覆盖主区，缩略图和 notes 默认收起，toolbar 可横向滚动但按钮不重叠。
- 768px 平板：允许 notes bottom sheet；缩略项最多一行横滑。
- 1440px 桌面：Canvas 右侧宽度保持稳定，16:9 viewport 不因 toolbar/warnings 抖动。

发布阻断：

- Phase 6 没有 `Desktop 1440x900`、`Tablet 768x1024`、`Mobile 390x844` Playwright 截图或等价视觉验收，阻断。
- 工具卡、DeckRenderer、DiagramRenderer 任一 running/failed/partial/success 状态未覆盖，阻断。
- 移动端 toolbar、notes、缩略图、下载菜单任一重叠，阻断。
- 新 UI 绕过 `ToolAdapter`、复用旧 `ToolResultCard` 主路径或把所有 artifact 塞进 `HtmlRenderer`，阻断。

### 4.6 视觉版导出与截图兜底

首发主路径仍是 `DeckSpec -> PptxGenJS editable PPTX`。同时可以规划一个明确标注的视觉兜底：

```text
internal/presentation/visual/
  screenshot_worker.go
  export.go
frontend/src/components/canvas/deck/exportPngZip.ts
```

借鉴 `html-anything/src/lib/export/image.ts` 和 `src/lib/export/deck.ts`：

- 首选服务端 Playwright worker 把每页 HTML 渲染成 PNG，避免依赖用户浏览器和 iframe same-origin。
- 浏览器端 PNG zip 只作为开发/QA 辅助功能，不进入首发默认用户路径。
- 等待 document complete、stylesheets、fonts、images decode 和两帧 layout settle；不实现 Tailwind Play CDN 专门等待，因为 Hive renderer 不允许 Tailwind CDN。
- 不复制 `html-anything` 在截图前正则替换 srcdoc style 的 hack。Playwright worker 直接设置 viewport、device scale factor 和页面级 CSS class，保证截图布局与 renderer 合同一致。
- 导出 PNG zip，用于设计 QA 和人工检查。
- 导出 visual PPTX：每页一张 full-bleed PNG，并保留 speaker notes。

产品规则：

- 默认下载按钮始终下载 editable PPTX。
- visual PPTX 必须在 UI 中标注“视觉版，文本不可编辑”。
- 如果某个 layout 没有对象映射，不允许静默把默认 PPTX 降级为截图；工具应返回 validation error 或 warnings，并让用户显式选择 visual mode。

依赖决策：

- 首发不要把 `pptxgenjs/jszip/modern-screenshot` 加进前端主 bundle。
- 如果后续保留浏览器端 PNG zip，可只引入 `modern-screenshot` 和 `jszip`，并做动态 import。
- visual PPTX 仍应由服务端生成，避免前端 PPTX 依赖和大文件内存压力。

### 4.7 与 Chat artifact 的关系

首发推荐让工具结果直接驱动 Canvas，而不是要求模型把完整 HTML 再包进 `<artifact>`：

1. 模型调用 `generate_ppt`。
2. 工具返回 JSON。
3. `ToolAdapter` 分派 `GeneratedArtifactResultCard`，根据 JSON 提供预览/下载。
4. 助手最终回复只做简短说明。

原因：

- 避免超大 HTML 污染消息文本和上下文。
- 避免模型二次改坏工具输出。
- 前端可以直接处理 asset URI 和下载状态。

兼容路径：

- 如果助手仍输出 `<artifact type="html">`，Canvas 继续按 HTML 预览。
- 如果助手输出 fenced ```mermaid 代码块，聊天正文继续 inline 渲染；点击打开 Canvas 时转换为 `diagram` artifact。
- 如果助手输出 `<artifact type="mermaid">`，`artifactParser` 必须识别为 Mermaid 源文件 artifact，并在 Canvas 中用 MermaidRenderer 预览。
- 如果助手输出 `<artifact type="mindmap">`，首发应解析为 mindmap source artifact；若缺少结构化 metadata，则只能作为源文件预览/下载，不允许伪装成已服务端导出的 SVG。
- 但下载 PPTX 只能来自 `generate_ppt` 工具结果，不能由普通 HTML artifact 自动推断。
- 下载 SVG/PNG 优先来自 `generate_diagram` 工具结果；普通 Mermaid artifact 可前端下载 `.mmd`，但服务端 SVG/PNG 下载需要 diagram asset。

### 4.8 模板与版式 Registry

借鉴 `html-anything/src/lib/templates/loader.ts` 的 folder-per-template 思路，但不要做成用户安装 skill。必须增加系统内置 registry：

```text
internal/presentation/templates/
  swiss/
    template.json
    layouts.json
    themes.json
    fixtures/swiss-basic-4.json
    fixtures/swiss-basic-10.json
  editorial/
    template.json
    layouts.json
    themes.json
    fixtures/editorial-basic-6.json
```

职责：

- `template.json`：模板元信息、许可证来源、默认主题、首发支持状态。
- `layouts.json`：layout id、必填字段、可选字段、图片槽位、最大文字长度、HTML renderer 名称、PPTX renderer 名称。
- `themes.json`：主题 token，来源于 `guizang-ppt-skill/references/themes*.md`，但改为结构化色值。
- `fixtures`：用于 HTML/PPTX golden test，不经过模型。

这样新增模板不是硬编码在 prompt 里，而是注册到后端 schema、校验、HTML renderer 和 PPTX renderer 的同一套表中。

`layouts.json` 首发必须包含 PPTX 坐标合同，不能只写字段名：

```json
{
  "id": "S04",
  "name": "Six Cells",
  "supported_public": true,
  "editable_pptx": true,
  "map_variant": false,
  "aspect": "16:9",
  "canvas": { "width_in": 13.333, "height_in": 7.5 },
  "fields": {
    "title": { "type": "string", "max_length": 36 },
    "cells": { "type": "array", "min_items": 6, "max_items": 6 }
  },
  "pptx_slots": {
    "title": { "kind": "text", "x": 0.62, "y": 0.45, "w": 8.8, "h": 0.55, "font_pt": 28 },
    "cells[0]": { "kind": "card", "x": 0.62, "y": 1.45, "w": 3.75, "h": 1.55 },
    "cells[1]": { "kind": "card", "x": 4.78, "y": 1.45, "w": 3.75, "h": 1.55 },
    "cells[2]": { "kind": "card", "x": 8.94, "y": 1.45, "w": 3.75, "h": 1.55 }
  },
  "html_renderer": "swiss.S04",
  "pptx_renderer": "swiss.S04"
}
```

坐标合同规则：

- 所有 PPTX 坐标使用 inch，基准画布 13.333 × 7.5。
- 坐标来源优先使用 Carbon 2x spacing token。Node exporter 必须提供 `spacingToInch(sp int) float64`，并在测试中校验 token 到 inch 的转换不漂移。
- `pptx_slots` 是测试输入，不只是文档；exporter 单测应读取它检查对象是否在画布内。
- 每个 public layout 必须有 `html_renderer` 和 `pptx_renderer`。
- 未标记 `editable_pptx=true` 的 layout 不得进入公开 schema。
- 每个 text slot 必须声明 `font_pt/min_font_pt/max_lines/overflow_policy`。
- 中文标题 slot 必须声明 `title_font_tiers`，按字符数选择字号；HTML renderer 和 PPTX exporter 使用同一组 tier。
- `overflow_policy` 首发只允许 `reject` 或 `split_slide`；禁止 exporter 静默缩小到不可读。
- image slot 必须声明 `aspect_ratio/crop_policy/max_bytes`。
- image slot 必须声明 `data_image_slot`，HTML renderer 输出本地/内部图片时必须带 `data-image-slot`。
- 7-8 页 deck 的 layout 多样性作为跨页 validator：M1/M2 初期 warning，首发闸门可升级为 error。
