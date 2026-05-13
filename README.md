# agents-hive

[![Go Version](https://img.shields.io/badge/Go-1.25+-00ADD8?style=flat&logo=go)](https://golang.org)
[![Node.js](https://img.shields.io/badge/Node.js-22+-339933?style=flat&logo=nodedotjs&logoColor=white)](https://nodejs.org)
[![React](https://img.shields.io/badge/React-19-61DAFB?style=flat&logo=react&logoColor=111111)](https://react.dev)
[![TypeScript](https://img.shields.io/badge/TypeScript-5.9-3178C6?style=flat&logo=typescript&logoColor=white)](https://www.typescriptlang.org)
[![PostgreSQL](https://img.shields.io/badge/PostgreSQL-16-4169E1?style=flat&logo=postgresql&logoColor=white)](https://www.postgresql.org)
[![Docker](https://img.shields.io/badge/Docker-Compose-2496ED?style=flat&logo=docker&logoColor=white)](https://docs.docker.com/compose/)
[![License](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

agents-hive 是面向 ReAct Agent 的工程化执行底座与质量控制平面。它不只是让模型接上工具，而是把一次复杂任务从入口、计划、工具调用、权限审批、SubAgent 协作、记忆上下文、IM 触达、执行轨迹、质量评测到优化回滚，收束到同一条可追踪、可复盘、可治理的运行链路。

它解决的不是“怎么让模型调用函数”，而是更难的生产问题：Agent 为什么做这个决策、调用了哪些能力、是否越权、失败发生在哪一步、能不能重放和评估、下一次能否避免同类错误。Hive 让 Agent 从“会聊天、会调用工具”的助手，升级为可托管、可约束、可审计、可评分、可回归、可持续进化的复杂任务执行单元。

一句话概括：**agents-hive = Agent Runtime + Agent Harness + Quality Control Plane + Ops Workbench**。

## 为什么是 Hive

- **不是聊天壳**：Web、CLI、HTTP API、IM Channel 都进入同一套会话、权限、工具、记忆和审计链路。
- **不是工具集合**：工具、Skill、MCP、自定义扩展和插件进程统一纳入能力发现、准入、审批和运行策略。
- **不是一次性 demo**：Replay / Journal / Trace / Trajectory 让每一步执行都能复盘，失败可以归因，样本可以沉淀为回归评测。
- **不是黑盒自动优化**：质量候选池、prompt smoke eval、优化建议、人工审批和 rollback 组成可控闭环，避免生产行为静默漂移。
- **不是单 Agent 孤岛**：Master Agent、Plan Runtime、SubAgent、远程 ACP Agent 和 Channel Router 共同支撑长任务、多入口和跨平台协作。

## 核心能力

| 能力 | Hive 提供什么 |
|------|---------------|
| Agent Runtime | ReAct 主循环、工具调用、HITL、上下文压缩、长任务恢复和 session-scoped todos |
| Quality Control Plane | Replay / Journal、质量事件、失败分类、回归样本、批量评测和优化回滚 |
| Tool / Skill / MCP | 内置工具、自定义工具、MCP Host、Skills、插件运行时、能力准入和危险操作审批 |
| Memory / Context | PostgreSQL 持久化、记忆治理、上下文注入、用量统计和 token accounting |
| SubAgent / ACP | 探索、总结、标题生成、压缩等内置 SubAgent，以及远程 Agent / ACP 集成 |
| IM Channel | 飞书、钉钉、企业微信、微信等通道复用统一会话、权限、HITL 和审计链路 |
| Ops Workbench | LLM / Prompt / Skill / Channel / 用户 / 配额 / 定时任务 / 质量治理的 Web 控制台 |

## 效果预览

**Chat Runtime**

![Chat Runtime](assets/screenshots/chat-runtime.png)

主聊天工作台统一承载会话、流式回复、工具调用、HITL、附件、Todos 和执行状态。

**Feishu Channel**

![Feishu Channel](assets/screenshots/feishu-chat.jpg)

飞书入口复用同一套会话、权限、工具调用和审计链路，让团队可以直接在 IM 场景中触发和跟踪 Agent 任务。

**WeChat Channel**

![WeChat Channel](assets/screenshots/wechat-chat.jpg)

微信入口通过统一 Channel Runtime 接入 Hive，IM 消息、Agent 回复和执行过程继续回到同一套可追踪链路。

**Session Replay**

![Session Replay](assets/screenshots/session-replay.png)

会话回放视图按时间线展示消息、工具调用、质量事件、trace 和关键决策，方便复盘 Agent 行为。

**Control Plane**

![Control Plane](assets/screenshots/settings-control-plane.png)

控制台集中管理 LLM、Prompt、Skill、Channel、权限、Memory、质量治理和运行时配置。

## 快速开始

### 一句话交给 Coding Agent 安装

如果你在用 Codex、Claude Code、Cursor、Windsurf 或其他 coding agent，可以直接把下面这句话发给它：

```text
如果还没 clone agents-hive，就先 clone https://github.com/chef-guo/agents-hive.git，然后按 README 的 Docker Compose 路径启动：生成 .env，构建 hive-sandbox:latest，执行 docker compose up -d，并告诉我访问地址和还缺哪些配置。
```

这条提示词会让 coding agent 优先走 Docker Compose，避免遗漏 sandbox 镜像、PostgreSQL 和前端 embed 构建这些容易卡住的步骤。

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

# sandbox 容器运行在宿主机 Docker daemon 上，需要先构建
docker build -t hive-sandbox:latest -f docker/sandbox/Dockerfile .

docker compose up -d
docker compose logs -f hive
```

访问：

```text
http://localhost:8080
```

如果需要单独构建主服务镜像：

```bash
docker build -t hive:latest .
```

sandbox bind mount 路径必须在宿主机和 Hive 容器内一致，默认使用 `/opt/hive/workdir`。如果修改该路径，需要同步修改 [docker-compose.yml](docker-compose.yml) 和 [docker/config.docker.json](docker/config.docker.json)。

### 本地开发

本地开发需要 Go 1.25+、Node.js、PostgreSQL。

```bash
git clone https://github.com/chef-guo/agents-hive.git
cd agents-hive

cp config.example.json config.json
# 编辑 config.json 或设置 POSTGRES_* / DATABASE_URL 等环境变量
# 首次启动 LLM 配置可通过 CLAW_API_KEY / OPENAI_API_KEY 注入，后续可在 Web UI 修改

cd frontend
npm install
npm run build
cd ..

go build -o claw ./cmd/claw
go build -o server ./cmd/server
```

启动后端：

```bash
./server --config config.json
```

启动前端开发服务器：

```bash
cd frontend
npm install
npm run dev
```

CLI 模式：

```bash
./claw -c config.json "分析当前项目结构"
./claw -c config.json -i
```

## 架构概览

```text
                 Web UI / CLI / HTTP API / IM Channel
                              |
                              v
                    API Server / Gateway / Auth
                              |
                              v
               Master Agent <--- Scheduler / Scheduled Tasks
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
          memory, scheduled tasks, quality data, trace data and accounting data.
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
| `internal/bootstrap` | 服务启动、定时任务恢复和后台运行循环 |
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
- Admin：LLM、Prompt、Skill、用户、用量、Memory、质量工作台、自动优化、定时任务。

UI 变更请保持现有组件、布局密度、颜色和交互约定；不要手工编辑 `internal/webui/dist/`。

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
| `GET/POST/PUT/DELETE` | `/scheduled-tasks[/{id}]` | 定时任务 CRUD |
| `POST` | `/scheduled-tasks/{id}/toggle` | 启停定时任务 |
| `POST` | `/scheduled-tasks/{id}/run-now` | 手动触发定时任务 |
| `GET` | `/scheduled-tasks/{id}/runs` | 定时任务运行历史 |
| `GET` | `/admin/scheduled-tasks` | 管理员读取全局定时任务 |
| `POST/GET/DELETE` | `/channels/push/schedules[/{id}]` | 兼容旧版 IM push 定时任务接口 |
| `GET` | `/ws` | WebSocket 实时事件 |

更多路由见 [internal/api/routes.go](internal/api/routes.go)。

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

## 感谢
![Control Plane](assets/screenshots/thank.png)  

## 交流群
![Control Plane](assets/screenshots/chat.jpg)  


