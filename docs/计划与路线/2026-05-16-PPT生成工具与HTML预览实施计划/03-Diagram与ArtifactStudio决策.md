# Diagram 与 Artifact Studio 决策

> 上级索引：[2026-05-16-PPT生成工具与HTML预览实施计划.md](../2026-05-16-PPT生成工具与HTML预览实施计划.md)

### 决策 D：PPT 是 PresentationArtifact，脑图/Mermaid 是 DiagramArtifact

不要把脑图、Mermaid、流程图、架构图都塞进 `DeckSpec` 或普通 Markdown artifact。它们需要自己的事实源和下载合同：

```json
{
  "kind": "diagram",
  "diagram_type": "mindmap",
  "version": 1,
  "title": "Agent 工作流",
  "source_format": "mindmap-json",
  "mindmap": {
    "root": {
      "id": "root",
      "text": "Agent 工作流",
      "children": [
        { "id": "plan", "text": "计划", "children": [] },
        { "id": "tool", "text": "工具调用", "children": [] }
      ]
    }
  }
}
```

Mermaid 示例：

```json
{
  "kind": "diagram",
  "diagram_type": "mermaid",
  "version": 1,
  "title": "生成链路",
  "source_format": "mermaid",
  "mermaid": "flowchart LR\nA[Chat] --> B[generate_diagram]\nB --> C[Canvas Preview]\nB --> D[SVG/PNG Asset]"
}
```

统一规则：

- `generate_ppt` 只负责 presentation。
- `generate_diagram` 负责 `mermaid/mindmap/flowchart/sequence/architecture` 等非 PPT 可视化产物。
- `DiagramSpec` 是事实源，SVG/PNG/HTML 是派生产物。
- Canvas 使用 `DiagramRenderer` 路由到 `MermaidRenderer`；`MindMapRenderer` 默认 M3 或产品确认前移后启用。
- 下载按钮按类型提供：
  - Mermaid：`.mmd`、`.svg`，M3 可加 `.png`。
  - Mindmap：`.mm.json`、`.md`、`.svg`，M3 可加 `.png`。
  - 普通 diagram：`.json`、`.svg`。
- 资产使用 run-scoped namespace，例如 `diagrams/user/<owner_id>/run/<run_id>`，并附带 `source_kind=generated_diagram`、`diagram_run_id`、`diagram_type`、`diagram_format=source|svg|png|html` tags。当前 Hive asset 层会按 namespace/content hash/owner 早返回去重，tag 不能作为唯一事实源。
- 权限规则复用 presentation asset resolver 的 owner/purpose/session 边界，但 purpose 使用 `diagram_preview` / `diagram_download` / `diagram_audit`。

里程碑范围锁定：

- M2 必须包含 Mermaid Canvas 预览 + `.mmd/.svg` 下载，因为当前前端已有 Mermaid 依赖，且用户已明确要求 Mermaid 进入能力范围。
- Mindmap 生成和下载锁定进入 M3，除非产品明确优先级高于 PPT 首发时前移到 M2；它不是“可选想法”，而是 VisualizationArtifact 平台必须覆盖的第二类产物。
- 不做任意 Graphviz/DOT、PlantUML、Excalidraw、draw.io 导入导出，避免首发范围失控。

### 决策 D1：DiagramRun 是脑图/Mermaid 的审计和下载单位

`DiagramSpec` 不能只存在于聊天消息里。每次 `generate_diagram` 都必须创建 `DiagramRun`，并把 source/SVG/PNG/HTML 资产和 run id 绑定，确保预览、下载、权限、重试、历史恢复和审计可闭合。`diagram_runs` 是 canonical truth；asset tags 只是辅助索引。

固定字段：

```json
{
  "run_id": "drun_...",
  "kind": "diagram_run",
  "status": "succeeded",
  "stage": "",
  "diagram_type": "mermaid",
  "source_format": "mermaid",
  "title": "生成链路",
  "owner_scope": "user",
  "owner_id": "user_...",
  "user_id": "user_...",
  "session_id": "sess_...",
  "turn_id": "trace_...",
  "source_asset_uri": "asset://diagrams/user/user_.../run/drun_.../....mmd",
  "svg_asset_uri": "asset://diagrams/user/user_.../run/drun_.../....svg",
  "png_asset_uri": "",
  "html_asset_uri": "",
  "created_at": "2026-05-16T00:00:00Z"
}
```

`diagram_runs` 表结构：

```sql
CREATE TABLE IF NOT EXISTS diagram_runs (
  id                    TEXT PRIMARY KEY,
  status                TEXT NOT NULL,
  stage                 TEXT NOT NULL DEFAULT '',
  progress_percent      INTEGER NOT NULL DEFAULT 0,
  title                 TEXT NOT NULL DEFAULT '',
  diagram_type          TEXT NOT NULL,
  source_format         TEXT NOT NULL,
  owner_scope           TEXT NOT NULL DEFAULT 'user',
  owner_id              TEXT NOT NULL,
  user_id               TEXT NOT NULL DEFAULT '',
  session_id            TEXT NOT NULL DEFAULT '',
  turn_id               TEXT NOT NULL DEFAULT '',
  trace_id              TEXT NOT NULL DEFAULT '',
  tool_call_id          TEXT NOT NULL DEFAULT '',
  domain_id             TEXT NOT NULL DEFAULT '',
  source_asset_uri      TEXT NOT NULL DEFAULT '',
  svg_asset_uri         TEXT NOT NULL DEFAULT '',
  png_asset_uri         TEXT NOT NULL DEFAULT '',
  html_asset_uri        TEXT NOT NULL DEFAULT '',
  validation_status     TEXT NOT NULL DEFAULT '',
  error_kind            TEXT NOT NULL DEFAULT '',
  error_message         TEXT NOT NULL DEFAULT '',
  error_json_pointer    TEXT NOT NULL DEFAULT '',
  recoverable           BOOLEAN NOT NULL DEFAULT FALSE,
  recoverable_by        TEXT NOT NULL DEFAULT '',
  warnings              JSONB NOT NULL DEFAULT '[]'::jsonb,
  metrics               JSONB NOT NULL DEFAULT '{}'::jsonb,
  claimed_by            TEXT NOT NULL DEFAULT '',
  lease_expires_at      TIMESTAMPTZ,
  expires_at            TIMESTAMPTZ,
  created_at            TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at            TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  CONSTRAINT diagram_runs_status_check
    CHECK (status IN ('created','running','succeeded','failed','cancelled')),
  CONSTRAINT diagram_runs_stage_check
    CHECK (stage IN ('','validating','rendering_source','exporting_svg','exporting_png','exporting_html','uploading_assets')),
  CONSTRAINT diagram_runs_type_check
    CHECK (diagram_type IN ('mermaid','mindmap')),
  CONSTRAINT diagram_runs_source_format_check
    CHECK (source_format IN ('mermaid','mindmap-json','mindmap-markdown')),
  CONSTRAINT diagram_runs_recoverable_by_check
    CHECK (recoverable_by IN ('','model','user','operator','none'))
);

CREATE INDEX IF NOT EXISTS idx_diagram_runs_owner_updated
  ON diagram_runs(owner_scope, owner_id, updated_at DESC);
CREATE INDEX IF NOT EXISTS idx_diagram_runs_session
  ON diagram_runs(session_id, updated_at DESC);
CREATE INDEX IF NOT EXISTS idx_diagram_runs_status_lease
  ON diagram_runs(status, lease_expires_at);
```

状态机：

```text
created/status -> running(stage=validating)
running(stage=validating) -> running(stage=rendering_source)
running(stage=rendering_source) -> running(stage=exporting_svg)
running(stage=exporting_svg) -> running(stage=exporting_png)       # 仅请求 png 时
running(stage=exporting_svg|exporting_png) -> running(stage=exporting_html) # 仅 mindmap html 时
running(stage=exporting_svg|exporting_png|exporting_html) -> running(stage=uploading_assets)
running(stage=uploading_assets) -> succeeded(stage='')
running(stage=validating|rendering_source|exporting_svg|exporting_png|exporting_html|uploading_assets) -> failed(stage=失败阶段)
```

Run 查询 API：

```http
GET /api/v1/diagram/runs/{run_id}
```

返回结构必须与 `generate_diagram` 的 succeeded/running/failed 结果同构，前端才能在历史消息恢复时把 pending diagram 原地升级：

```json
{
  "kind": "diagram",
  "run_id": "drun_...",
  "status": "succeeded",
  "title": "生成链路",
  "diagram_type": "mermaid",
  "source_format": "mermaid",
  "source_asset_uri": "asset://diagrams/....mmd",
  "svg_asset_uri": "asset://diagrams/....svg",
  "png_asset_uri": "",
  "html_asset_uri": "",
  "warnings": [],
  "updated_at": "2026-05-16T00:00:10Z"
}
```

约束：

- `diagram_runs` 首发放入同一个 store migration 机制，不另开未接入的迁移体系。
- `diagram_type='mermaid'` 覆盖 flowchart、sequence、class、state、gantt、C4 等 Mermaid 语法家族；不要为 Mermaid 子语法建数据库 enum。
- `source_format='mindmap-markdown'` 只表示输入源是 Markdown outline；归档时必须同时保存规范化后的 `MindMapSpec JSON`，否则后续编辑和审计困难。
- Mermaid/Mindmap 的 run 不写入 `presentation_runs`，但共享 RuntimeContext、asset resolver、quota、GC、observability 的实现模式。
- M2 Mermaid 可以同步等待 SVG 导出，但仍必须先创建 `diagram_runs`；超出 `diagram.sync_timeout_seconds` 后返回 `status:"running"` 并由 `DiagramRunWorker` 接管。
- GC、历史恢复和授权必须以 `diagram_runs` 的 asset URI 字段为准；如果未来要按 tags 查找/更新 diagram assets，必须先扩展 `AssetMetaStore` 的 tag query/update 能力。

### 决策 E：扩展能力统一为 AI Artifact Studio，不做工具超市

扩展方向不是“能接什么库就接什么库”，而是围绕办公交付链形成一组可组合 artifact：

| Artifact | Tool | Spec | Run Store | 首批下载 | 进入阶段 | 产品价值 |
|---|---|---|---|---|---|---|
| Presentation | `generate_ppt` | `DeckSpec` | `presentation_runs` | `.pptx`；HTML 为 preview asset，公开 attachment 下载后置 | M1-M2 | 汇报交付 |
| Diagram/Mindmap | `generate_diagram` | `DiagramSpec` | `diagram_runs` | `.mmd/.mm.json/.svg/.png` | M1-M3 | 结构化图解 |
| Chart | `generate_chart` | `ChartSpec` | `chart_runs` | `.vl.json/.svg/.png` | M3 | 数据洞察和 PPT 图表 |
| Table | `generate_table` | `TableSpec` | `table_runs` | `.xlsx/.csv/.json` | M3 | 数据底座和 Excel 交付 |
| Whiteboard | `generate_whiteboard` | `WhiteboardSpec` | `whiteboard_runs` | `.excalidraw/.svg/.png` | M4 | 架构草图和协作表达 |
| Equation | `generate_equation` | `EquationSpec` | `equation_runs` | `.tex/.svg/.png` | M4 | 科研/教育/金融表达 |
| Report | `generate_report` | `ReportSpec` | `report_runs` | `.pdf/.docx/.html` | M5 | 长文档沉淀 |

统一合同：

- 每类 artifact 都必须有强类型 Spec，不能让模型直接输出任意 HTML/JS/option object 后执行。
- 每次生成都必须有 run id、status、stage、metrics、warnings、error_kind、recoverable_by。
- 每类 artifact 都必须有 run-scoped namespace 和 asset tags：`source_kind=generated_<artifact>`、`<artifact>_run_id`、`<artifact>_format`、`session_id`、`turn_id`、`domain_id`。run store 永远是 canonical truth，tags 只做排查/宽松索引。
- 每类 artifact 都必须定义 purpose：`<artifact>_preview`、`<artifact>_download`、`<artifact>_audit`。
- Canvas renderer 必须按 artifact type 分派，不能把所有扩展都塞进 `HtmlRenderer`。
- PPT 和 Report 可以引用其他 artifact 的 asset URI，但引用必须记录 provenance，不能复制成无来源的图片。
- 数据类 artifact 必须记录 `data_provenance`：
  - `uploaded_user_file`
  - `kb_query`
  - `tool_result`
  - `model_generated`
  - `manual_input`
- 如果 `data_provenance="model_generated"`，UI 和下载 metadata 必须标记“模型生成数据”，防止用户误以为是真实业务数据。

路线锁定：

- M3：先做 `ChartSpec` + `TableSpec`，因为它们能直接增强 PPT 内容质量。
- M4：再做 `WhiteboardSpec` + `EquationSpec`，覆盖架构草图和公式表达。
- M5：最后做 `ReportSpec`，把 PPT、图表、表格、脑图沉淀为 PDF/DOCX/HTML 报告。
- PlantUML、Graphviz、draw.io、Figma、任意 React app artifact 暂不进入 M3/M4，除非有明确客户需求和安全 PoC。

### 决策 E1：ChartSpec / TableSpec 是 M3 的数据底座

`generate_chart` 不应让模型直接吐 ECharts option 或 Vega spec 任意对象。首发应定义受限 `ChartSpec`：

```json
{
  "kind": "chart",
  "version": 1,
  "title": "月度收入趋势",
  "chart_type": "line",
  "data_provenance": "uploaded_user_file",
  "dataset": {
    "columns": [
      { "name": "month", "type": "temporal" },
      { "name": "revenue", "type": "quantitative" }
    ],
    "rows": [
      { "month": "2026-01", "revenue": 120 }
    ]
  },
  "encoding": {
    "x": { "field": "month", "type": "temporal" },
    "y": { "field": "revenue", "type": "quantitative" }
  }
}
```

Chart 规则：

- M3 首发只开放 bar/line/area/scatter/pie 的受限 schema。
- Vega-Lite 是首发 canonical renderer；ECharts 放 M4 作为复杂业务图增强。
- chart source asset 保存 `ChartSpec JSON` 和派生 `.vl.json`。
- 下载 `.svg` 作为默认正式图片，`.png` behind config。
- Chart 可以被 `generate_ppt` 引用为图片或未来对象化 chart，但首发不承诺 PPTX 原生 chart 可编辑。

`generate_table` 定义 `TableSpec`：

```json
{
  "kind": "table",
  "version": 1,
  "title": "月度收入明细",
  "data_provenance": "uploaded_user_file",
  "columns": [
    { "key": "month", "label": "月份", "type": "string" },
    { "key": "revenue", "label": "收入", "type": "number", "format": "currency" }
  ],
  "rows": [
    { "month": "2026-01", "revenue": 120 }
  ]
}
```

Table 规则：

- M3 首发下载 `.xlsx/.csv/.json`。
- ExcelJS 是首发 `.xlsx` writer。
- 表格最大行列数必须配置化，默认 10,000 rows / 100 columns；超过返回 `table_too_large`。
- 公式单元格默认禁止，M4 再评估受控公式白名单，避免 CSV/Excel formula injection。
- `.xlsx` 导出必须防公式注入：以 `= + - @` 开头的用户文本默认转义或按纯文本写入。

### 决策 E2：Whiteboard / Equation / Report 后置但先锁边界

Whiteboard：

- M4 使用 Excalidraw source JSON 作为 `WhiteboardSpec` 的事实源。
- 下载 `.excalidraw/.svg/.png`。
- tldraw 只作为后续完整白板 SDK 候选，必须先完成 license 和 bundle size 评审。
- 白板不允许任意外部图片 URL；图片必须先进入 asset。

Equation：

- M4 定义 `EquationSpec`，事实源是 TeX/MathML + display mode + macros allowlist。
- KaTeX 是默认 renderer；MathJax 作为兼容增强。
- 下载 `.tex/.svg/.png`。
- 禁止用户自定义危险 macro 或 HTML extension。

Report：

- M5 定义 `ReportSpec`，section-based，支持引用 presentation/chart/table/diagram/equation asset。
- 下载 `.html/.pdf/.docx`。
- PDF/DOCX 引擎单独 PoC；首发不把浏览器截图 PDF 当成唯一正式报告。
- Report 必须保留所有引用 artifact 的 provenance，避免复制后失去审计链路。
