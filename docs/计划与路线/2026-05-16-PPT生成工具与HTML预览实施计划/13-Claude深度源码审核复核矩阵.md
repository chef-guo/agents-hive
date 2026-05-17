# Claude 深度源码审核复核矩阵

> 审核日期：2026-05-17
> 复核对象：`11-深度源码审核报告.md`
> 复核原则：外部审核意见必须对照当前 Hive 代码验证，不能照单全收；实施以本矩阵、`08-模型协作风险审核.md` 和 `12-Hive当前代码适配深度审核.md` 的采纳结果为准。

---

## 1. 总体判断

`11-深度源码审核报告.md` 的大方向成立，尤其是字体/图标/动效、resolver 集成、proxy 下载、ToolAdapter、Docker worker 和 schema/provider 兼容这些问题。它的问题是两类：

- 部分意见已经被后续 `12-Hive当前代码适配深度审核.md` 修正，例如 30s MCP timeout 不能直接套到本地 built-in tool。
- 部分产品建议还停留在“可以更好”，没有明确进入首发合同，例如 S02/S21、custom theme、artifact intent 的 `tool_choice=required`。

本次复核结论：`11` 不能作为独立实施依据，但它揭示的问题必须进入 Phase 0 gate。当前计划已补齐大部分，还需补强三项：首发 Swiss 版式扩到 10 个、支持 custom accent、为 PPT/Diagram/Mindmap 意图增加 host 侧 tool_choice required 规则。

---

## 2. 逐项处理矩阵

| 领域 | 11 报告意见 | 复核结论 | 当前处理 |
|---|---|---|---|
| 外链字体/图标/动效 | Google Fonts、Lucide runtime、Motion One 未替代 | 正确 | 已写入 `02`/`07`：系统字体栈、PPTX fallback、`fonts.json`、`icons.json`、首发无 Motion/无外链 script |
| Swiss validator | `data-layout`、layout 白名单、图片槽位、S15/S16/S22 比例等隐藏规则 | 正确 | 已写入 `02`/`07`：`layouts.json` 承载 `title_font_tiers`、spacing、image slots、diversity rules |
| S08 MapLibre | 地图变体首发风险未锁定 | 正确 | 已写入 `02`/`07`：`map_variant:false`，地图型页面另开 PoC |
| html-anything regex | `deck.ts` 用 regex，不应复制 | 正确 | 已写入 `08`：前端用 DOMParser，Go 测试用 `golang.org/x/net/html` |
| 缩略图 iframe | 多 iframe 缩略图性能风险 | 正确 | 已写入 `05`/`07`：首发文本缩略图，mini iframe 仅当前页附近 |
| 截图 style hack | 不应复制 srcdoc regex hack | 正确 | 已写入 `08`：Playwright 视觉导出不复制该方案 |
| SSE convert | 不应复制 html-anything 的 SSE 局部编辑 | 正确 | 已写入 `01`/`08`：局部编辑走 DeckSpec 重渲染 |
| AccessResolver | 当前不是 resolver chain | 正确 | 已写入 `04`/`05`/`12`：改 `initAssetAccessResolver` 返回对象内部 `source_kind` 分支 |
| 工具注册方式 | 不应继续扩 `RegisterBuiltinTools` variadic | 正确 | 已写入 `04`/`07`：独立 `RegisterPPTGen` / `RegisterDiagramGen` |
| Asset proxy | 当前 proxy 硬编码 inline | 正确 | 已写入 `05`/`07`/`12`：修改现有 proxy，`purpose/disposition/filename` 进 HMAC |
| 旧 `ppt` 类型 | 当前只是 Markdown 别名 | 正确 | 已写入 `05`/`08`：保留旧 `ppt -> MarkdownRenderer`，新 `presentation -> DeckRenderer` |
| Bootstrap 顺序 | presentation 初始化必须在 asset 后、工具注册前 | 正确 | 已写入 `04`/`07`/`12`：明确初始化链路 |
| 20KB tool result 截断 | `html_preview` 不能进工具结果 | 正确 | 已被 `12` 提升为 P0：tool result 只返回小 manifest |
| 30s MCP timeout | 报告称 MCP 工具默认 30s | 部分正确 | 已被 `12` 修正：远程 MCP 受 `mcpOperationTimeout`，本地 built-in 主要受 `RuntimePolicy.ToolTimeout`；仍要求 async run |
| `$ref/$defs/oneOf` | provider 兼容风险 | 正确 | 已写入 `04`/`12`：provider smoke test 或 schema flatten |
| tool_choice 不强制 | “做 PPT”可能仍是 auto | 正确且需补强 | 本次补入：artifact intent 命中 PPT/diagram/mindmap 时 host 侧应返回 `tool_choice=required` 或等价强制调用机制 |
| ToolResultCard 路径 | 工具结果不会自动触发 Canvas | 方向正确，但路径表述需修正 | 已被 `12` 修正为 `ToolAdapter -> GeneratedArtifactResultCard`，不是 standalone `ToolResultCard` |
| Canvas 数据来源 | 工具结果 JSON 是第三种数据源 | 正确 | 已写入 `05`/`12`：卡片从 run/asset 打开 Canvas，artifact metadata 携带 runId |
| `ppt` 下载 `.md` | 旧类型下载不是 PPTX | 正确 | 已写入 `05`：PPTX 下载只能来自 generated presentation asset，旧 `ppt` 继续 markdown 兼容 |
| Docker runtime | `tools/` 不会自动进入镜像 | 正确 | 已写入 `04`/`07`/`09`：Dockerfile 显式 COPY/install worker |
| GC goroutine | 无现有 run GC 模式 | 正确 | 已写入 `05`/`12`：首发 admin dry-run，以 run 表 URI 为准；自动 goroutine 可后置 |
| S02/S21 | 首发 8 个版式缺过渡/结尾 | 正确，产品收益高 | 本次采纳：M2 首发从 8 个扩到 10 个，补 S02 Statement 和 S21 Closing |
| custom theme | 企业品牌色需求 | 正确，成本低 | 本次采纳：Swiss theme 增加 `custom` + `custom_accent` hex |

---

## 3. 对 11 报告需要修正的地方

### 3.1 30s MCP timeout 不是本地 built-in tool 的直接超时

当前计划要实现的是 Hive 内置工具注册。对这条路径，当前代码里更直接的执行限制来自 `internal/runtimepolicy/policy.go` 的 `ToolTimeout`，默认 2 分钟；`internal/mcphost/client.go` 的 `mcpOperationTimeout=30s` 主要影响远程 MCP client。结论不是“generate_ppt 必须设置 60s MCP timeout”，而是：

- 本地 built-in 工具必须快速创建 run 并返回；
- `sync_timeout_seconds` 必须小于 worker timeout 和 `RuntimePolicy.ToolTimeout`；
- 如果未来把 exporter 拆成远程 MCP 服务，再单独处理远程 MCP timeout。

### 3.2 ToolResultCard 不是当前前端主路径

`11` 说要改 `ToolResultCard`，方向是“工具结果需要产品卡片”，但当前 Hive 正常 role=`tool` 消息会被 `MessageList` 集成到 assistant 气泡内，并由 `ToolAdapter` 渲染。实施必须改：

```text
ToolAdapter -> GeneratedArtifactResultCard -> Canvas openArtifact/useRun/useAsset
```

standalone `ToolResultCard` 抽取可以作为 MessageBubble 维护性重构，但不是 artifact 主路径依赖。

### 3.3 “首发 8 个版式”需要产品层修正

工程上 8 个版式能做薄片，但用户真的要求“做一份汇报”时，缺 `S02 Statement` 和 `S21 Closing` 会让节奏不完整。两者实现成本低，且能改善绝大多数 deck 的开头/过渡/收尾质量。因此首发质量闸门改为 Swiss 10 个版式。

---

## 4. 必须补入 Phase 0 的新增闸门

1. `detectToolChoice` 或等价 host intent guard 增加 artifact 意图识别：
   - PPT / 演示文稿 / slide deck / 下载 PPTX -> `generate_ppt` 必须进入 required 工具选择路径。
   - Mermaid / 脑图 / 思维导图 / 流程图 / 架构图 / 可下载 SVG/PNG -> `generate_diagram` 必须进入 required 工具选择路径。
   - 明确只要 Mermaid 源码且不需要预览/下载时，可保留普通回答。
2. Swiss M2 首发版式白名单改为：
   - `S01,S02,S03,S04,S08,S11,S15,S19,S21,S22`
3. Swiss theme schema 改为：
   - `theme: "ikb" | "lemon" | "lemon_green" | "safety_orange" | "custom"`
   - `custom_accent?: "#RRGGBB"`，当 `theme="custom"` 时必填；非 custom 时禁止或忽略并 warning。
4. `swiss-basic-10.json` fixture 覆盖新增 `S02` 和 `S21`。
5. `tool_choice` 测试必须包含中文和英文 artifact 请求：
   - “帮我做一份 PPT”
   - “生成一个可下载的 slide deck”
   - “画一个 Mermaid 流程图并下载 SVG”
   - “生成一个脑图”

---

## 5. 实施阶段退回条件

如果实现分支出现以下任一情况，按本矩阵退回计划评审：

- 只靠 prompt 指望模型调用 `generate_ppt`，没有 host 侧 artifact 意图 guard 或等价强制机制。
- 仍按 Swiss 8 个 layout 宣称达到 M2 首发质量。
- 企业品牌色只能从 4 个固定色里选，不能传 custom accent。
- 把 `mcpOperationTimeout=30s` 当成本地 built-in tool 的唯一超时依据。
- artifact 工具 UI 改了 standalone `ToolResultCard`，但没有接入 `ToolAdapter`。
- 下载 PPTX/SVG/source 走前端 blob 或直签 URL，绕过 signed Hive proxy。
