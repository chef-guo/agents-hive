# agents-hive

agents-hive 是一个面向真实业务协作的 Agent 平台。它把 Web UI、CLI、IM Channel、MCP 工具、SubAgent、会话持久化、计划执行、质量评估和运行时治理放在同一个系统里，让 Agent 不只是一次性聊天，而是可以长期运行、可观察、可审计、可迭代。

[![Go Version](https://img.shields.io/badge/Go-1.25+-00ADD8?style=flat&logo=go)](https://golang.org)
[![License](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

## 项目定位

agents-hive 的核心目标是把 Agent 从「会话里的智能回复」推进到「可接入组织流程的工作系统」：

- 给用户一个可直接使用的 Web Chat 和管理台。
- 给 Agent 一个可控的 ReAct 执行循环、工具系统、计划状态和任务续航能力。
- 给团队一个可观测、可回放、可评估、可灰度优化的 Agent 运行平台。
- 给企业集成 IM、权限、审计、用户身份和多租户治理的落地路径。

它不是一个纯 SDK，也不是一个简单聊天壳。更接近一个 Agent Runtime + Control Plane + Workbench。

## 核心能力

**Agent Runtime**

- Master Agent 基于 ReAct 循环执行任务，支持工具调用、用户确认、上下文压缩和长任务恢复。
- Plan Runtime 将长任务拆成 session-scoped todos，并在 UI 中实时展示进度。
- SubAgent 支持探索、总结、标题生成、压缩等独立角色，也支持远程 Agent / ACP 集成。
- tool_search、skill_search、batch、parallel_dispatch 等机制用于降低工具暴露成本和提升任务分发质量。

**工具与扩展**

- 内置文件、搜索、Shell、Patch、Web、LSP、图片/语音/视频、IM 发送、任务板、记忆等工具。
- MCP Host 统一承载内置工具、自定义工具和外部 MCP Server。
- Skills 系统用 Markdown 描述能力包，支持本地、DB 覆盖、按需安装和权限治理。
- 插件运行时支持将扩展能力以独立进程接入。

**会话与可观测性**

- PostgreSQL 持久化会话、消息、配置、Prompt、Skill、质量用例和运行数据。
- Session fork / revert / regenerate / trace / trajectory 支持调试和回放。
- Replay Gallery、Session Replay、Quality Workbench 用于复盘 Agent 行为和生成评估样本。
- 用量统计、token accounting、质量候选池、自动优化建议为后续治理提供数据面。

**管理台**

- LLM Provider / Model 管理。
- Prompt 热更新和 smoke eval。
- Skill 管理和按需加载治理。
- 用户、认证、配额和用量统计。
- Memory Governance、Quality Workbench、自动优化、运行时策略查看。

**Channel 集成**

- 支持飞书、钉钉、企业微信、微信等 IM Channel。
- 飞书方向包含入站解析、交互回调、身份解析、出站发送、推送、观测、可靠性、安全和多租户治理文档。
- Channel 侧和 Web UI 共享会话、权限、HITL 和审计链路。

## 快速开始

### Docker Compose

Docker 部署包含 Hive 主服务和 PostgreSQL。Hive 主服务内嵌前端静态资源，并通过宿主机 Docker socket 创建 sandbox 容器执行隔离任务。

```bash
git clone https://github.com/chef-guo/agents-hive.git
cd agents-hive

# 生产环境请使用强密码
cat > .env <<EOF
POSTGRES_PASSWORD=your_strong_password
DOCKER_GID=$(stat -c '%g' /var/run/docker.sock)
TZ=Asia/Shanghai
HIVE_PORT=8080
EOF

mkdir -p /opt/hive/workdir/sessions

docker compose up -d
docker compose logs -f hive
```

访问：

```text
http://localhost:8080
```

如果需要单独构建镜像：

```bash
docker build -t hive:latest .
docker build -t hive-sandbox:latest -f docker/sandbox/Dockerfile .
```

部署细节以 [docker-compose.yml](docker-compose.yml) 和 [docker/config.docker.json](docker/config.docker.json) 为准。sandbox bind mount 路径必须在宿主机和 Hive 容器内一致，默认使用 `/opt/hive/workdir`。

### 本地开发

本地开发需要 Go、Node.js、PostgreSQL。

```bash
git clone https://github.com/chef-guo/agents-hive.git
cd agents-hive

cp config.example.json config.json
# 编辑 config.json 或设置 POSTGRES_* / DATABASE_URL 等环境变量

go build -o claw ./cmd/claw
go build -o server ./cmd/server
```

启动后端：

```bash
./server
```

启动前端开发服务器：

```bash
cd frontend
npm install
npm run dev
```

CLI 模式：

```bash
./claw "分析当前项目结构"
./claw -i
```

## 架构概览

```text
                 Web UI / CLI / HTTP API / IM Channel
                              |
                              v
                    API Server / Gateway / Auth
                              |
                              v
                         Master Agent
                              |
          +-------------------+-------------------+
          |                   |                   |
          v                   v                   v
      Tool Runtime        Plan Runtime        SubAgents / ACP
      MCP Host            Todos / Resume      Remote Agents
          |
          v
  Files / Shell / LSP / Web / IM / Memory / Custom MCP

          PostgreSQL stores sessions, config, prompts, skills,
          memory, quality data, trace data and accounting data.
```

关键代码路径：

| 路径 | 说明 |
|------|------|
| `cmd/claw` | CLI 入口 |
| `cmd/server` | HTTP Server 入口 |
| `frontend/src` | React 管理台和 Chat UI |
| `internal/master` | Master Agent、ReAct、计划执行、反思和会话循环 |
| `internal/tools` | 内置工具、工具搜索、任务工具、IM 工具 |
| `internal/mcphost` | MCP 工具宿主和 schema 转换 |
| `internal/subagent` | SubAgent 框架 |
| `internal/acpserver` / `internal/acpclient` | ACP 服务端和客户端 |
| `internal/channel` | 飞书、钉钉、企业微信、微信等 Channel |
| `internal/api` | HTTP API、管理台 API、会话 API |
| `internal/store` | PostgreSQL 存储和迁移 |
| `internal/agentquality` | Agent 质量样本、评估、建议和回滚 |
| `internal/qualityworkbench` | 质量工作台、回放、分组、报告 |
| `internal/trajectory` | 会话轨迹快照 |
| `internal/webui/dist` | 前端构建产物，由 Vite 生成并被 Go embed |

## 配置模型

agents-hive 使用两层配置：

- **启动配置**：服务监听、日志、数据库连接等启动前必须知道的参数，来自 `config.json`、环境变量或 CLI flags。
- **运行时配置**：LLM、Prompt、Skill、Channel、权限、Memory、MCP 等可在 Web UI 或 API 中修改，存储在 PostgreSQL。

常用环境变量：

| 环境变量 | 说明 |
|----------|------|
| `DATABASE_URL` | PostgreSQL DSN，优先于拆分字段 |
| `POSTGRES_HOST` / `POSTGRES_PORT` / `POSTGRES_DB` | PostgreSQL 地址、端口、库名 |
| `POSTGRES_USER` / `POSTGRES_PASSWORD` / `POSTGRES_SSL_MODE` | PostgreSQL 认证和 SSL 配置 |
| `SESSIONS_DIR` | 会话工作目录 |
| `CUSTOM_TOOLS_DIR` | 自定义工具目录 |
| `CLAW_API_KEY` / `OPENAI_API_KEY` | 首次启动初始化 LLM 配置 |
| `CLAW_LOG_FILE` / `CLAW_LOG_LEVEL` / `CLAW_CONSOLE_LEVEL` | 日志配置 |

完整示例见 [config.example.json](config.example.json)。

工具召回运行时配置见数据库里的 `agent.tool_recall`，常用值：

- `mode=observe`：只观测，不改可见工具。
- `mode=inject`：观测并注入召回候选。
- `mode=off`：关闭召回，作为回滚开关。
- `log_candidates=false`：不记录候选名称和分数，只保留聚合字段。

## Web UI

前端位于 [frontend](frontend)，使用 React、Vite、TypeScript、Tailwind CSS。

常用命令：

```bash
cd frontend
npm install
npm run dev
npm run build
npm run lint
npm test
```

`npm run build` 会把产物写入 `internal/webui/dist/`，Go 服务通过 `internal/webui/embed.go` 嵌入该目录。不要手工编辑 `internal/webui/dist/`。

主要页面：

- Chat：会话、工具调用、HITL、附件、Canvas、Todos。
- Sessions：会话列表、星标、标签、fork、revert。
- Replay Gallery / Session Replay：会话回放和轨迹查看。
- Settings：运行时配置、MCP、权限、IM Channel、远程 Agent。
- Admin：LLM、Prompt、Skill、用户、用量、Memory、质量工作台、自动优化。

UI 设计约束见 [DESIGN.md](DESIGN.md)。

## API 入口

HTTP API 默认前缀：

```text
http://localhost:8080/api/v1
```

常用资源：

| 方法 | 路径 | 说明 |
|------|------|------|
| `GET` | `/health` | 健康检查 |
| `GET` | `/capabilities` | 能力列表 |
| `POST` | `/sessions` | 创建会话 |
| `GET` | `/sessions` | 会话列表 |
| `POST` | `/sessions/{id}/messages` | 发送消息 |
| `GET` | `/sessions/{id}/messages` | 读取消息 |
| `GET` | `/sessions/{id}/todos` | 读取会话 todos |
| `GET` | `/sessions/{id}/trace` | 读取会话 trace |
| `GET` | `/sessions/{id}/trajectory/{step}` | 读取轨迹快照 |
| `POST` | `/sessions/{id}/fork` | Fork 会话 |
| `POST` | `/sessions/{id}/revert` | Revert 会话 |
| `GET` | `/ws` | WebSocket 实时事件 |

更多路由见 [internal/api/routes.go](internal/api/routes.go)。

## 文档索引

**架构与安全**

- [安全权限模型](docs/架构设计/安全权限模型.md)
- [Skill 市场协议](docs/架构设计/Skill-市场协议.md)
- [Skill 按需加载总览](docs/架构设计/skills/Skill-按需加载总览.md)
- [Skill 安装安全模型](docs/架构设计/skills/Skill-安装安全模型.md)
- [Skill Scope 与覆盖关系](docs/架构设计/skills/Skill-Scope与覆盖关系.md)
- [Skill Feature Flag 矩阵](docs/架构设计/skills/Skill-Feature-Flag矩阵.md)

**计划与路线**

- [Agent 长任务续航基准与恢复治理计划](docs/计划与路线/归档/Agent-长任务续航验收与压缩策略计划.md)
- [Agent 工具召回稳定化计划](docs/计划与路线/归档/Agent-工具召回稳定化计划.md)
- [Agent System Prompt 重整方案](docs/计划与路线/归档/Agent-System-Prompt重整方案.md)
- [Agent 定时任务系统方案](docs/计划与路线/Agent-定时任务系统方案.md)
- [Agent 记忆反馈闭环与治理计划](docs/计划与路线/Agent-记忆反馈闭环与治理计划.md)
- [多业务平台化计划](docs/计划与路线/多业务平台化计划.md)

**Channel**

- [飞书集成总览](docs/渠道对接/feishu-bot/README.md)
- [飞书能力矩阵](docs/渠道对接/feishu-bot/00-feature-matrix.md)
- [飞书身份解析](docs/渠道对接/feishu-bot/05-identity.md)
- [飞书出站发送](docs/渠道对接/feishu-bot/04-outbound.md)
- [飞书可靠性](docs/渠道对接/feishu-bot/09-reliability.md)
- [飞书多租户](docs/渠道对接/feishu-bot/11-multi-tenant.md)
- [微信机器人重构计划](docs/计划与路线/微信机器人重构计划.md)

**运维与验收**

- [CI baseline](docs/运维手册/ci-baseline.md)
- [Plan Runtime Todos 验收](docs/运维手册/plan-runtime-todos-acceptance.md)
- [Session Todo 可观测性](docs/运维手册/sessiontodo-observability.md)
- [IM Streaming Reply Live Smoke](docs/运维手册/im-streaming-reply-live-smoke.md)
- [Spec-driven rollout](docs/运维手册/spec-driven-rollout.md)
- [Spec-driven rollback](docs/运维手册/spec-driven-rollback.md)

**研究与路线图**

- [研究总览](docs/research/README.md)
- [最终计划](docs/research/FINAL-PLAN.md)
- [能力面路线](docs/research/ROADMAPS/CAPABILITY-SURFACE.md)
- [Agent 质量路线](docs/research/ROADMAPS/AGENT-QUALITY.md)
- [Memory 与 Context 路线](docs/research/ROADMAPS/MEMORY-AND-CONTEXT.md)
- [Multi-Agent 与 ACP 路线](docs/research/ROADMAPS/MULTI-AGENT-AND-ACP.md)

## 开发规范

- Go 代码使用 `gofmt`。
- Go 注释和日志使用中文，错误保持结构化。
- 测试优先使用表驱动风格。
- 前端使用 TypeScript、React、ESLint，保持现有组件和样式约定。
- 不手工编辑 `internal/webui/dist/`，只通过 `frontend/npm run build` 生成。
- 真实密钥只放在本地配置或环境变量，不提交 `config.json`、`.env` 等敏感文件。

常用验证：

```bash
go test ./... -v
go test -race ./...
go test -cover ./...

cd frontend
npm run lint
npm run build
npm test
```

## 许可证

MIT License

## 联系方式

- Issues: https://github.com/chef-guo/agents-hive/issues
