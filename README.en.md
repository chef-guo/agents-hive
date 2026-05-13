# agents-hive

**Language / 语言:** [中文](README.md) | English

[![Go Version](https://img.shields.io/badge/Go-1.25+-00ADD8?style=flat&logo=go)](https://golang.org)
[![Node.js](https://img.shields.io/badge/Node.js-22+-339933?style=flat&logo=nodedotjs&logoColor=white)](https://nodejs.org)
[![React](https://img.shields.io/badge/React-19-61DAFB?style=flat&logo=react&logoColor=111111)](https://react.dev)
[![TypeScript](https://img.shields.io/badge/TypeScript-5.9-3178C6?style=flat&logo=typescript&logoColor=white)](https://www.typescriptlang.org)
[![PostgreSQL](https://img.shields.io/badge/PostgreSQL-16-4169E1?style=flat&logo=postgresql&logoColor=white)](https://www.postgresql.org)
[![Docker](https://img.shields.io/badge/Docker-Compose-2496ED?style=flat&logo=docker&logoColor=white)](https://docs.docker.com/compose/)
[![License](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

agents-hive is an engineering runtime and quality control plane for ReAct agents. It does more than connect models to tools: it brings task ingress, planning, tool calls, human approval, SubAgent collaboration, memory context, IM delivery, execution tracing, quality evaluation, optimization, and rollback into one traceable and governable runtime chain.

The hard production problems are not just "how does the model call a function?" They are: why did the agent make this decision, which capabilities did it call, did it cross a permission boundary, where did the failure happen, can the run be replayed and evaluated, and can the next run avoid the same class of mistake? Hive turns agents from chat assistants with tools into hosted, constrained, auditable, scored, regression-tested, and continuously improving execution units.

In one line: **agents-hive = Agent Runtime + Agent Harness + Quality Control Plane + Ops Workbench**.

## Why Hive

- **Not just a chat shell**: Web, CLI, HTTP API, and IM channels all enter the same session, permission, tool, memory, and audit pipeline.
- **Not just a tool collection**: tools, skills, MCP, custom extensions, and plugin processes are unified under capability discovery, access control, approval, and runtime policy.
- **Not a one-off demo**: Replay, Journal, Trace, and Trajectory make every execution step reviewable. Failures can be attributed and converted into regression cases.
- **Not black-box auto-optimization**: quality candidates, prompt smoke eval, optimization suggestions, human approval, and rollback form a controlled feedback loop.
- **Not a single-agent island**: Master Agent, Plan Runtime, SubAgents, remote ACP agents, and Channel Router support long-running tasks, multiple ingress paths, and cross-platform collaboration.

## Core Capabilities

| Area | What Hive Provides |
|------|--------------------|
| Agent Runtime | ReAct main loop, tool calls, HITL, context compaction, long-task resume, and session-scoped todos |
| Quality Control Plane | Replay / Journal, quality events, failure classification, regression samples, batch evaluation, and optimization rollback |
| Tool / Skill / MCP | Built-in tools, custom tools, MCP Host, Skills, plugin runtime, capability admission, and approval for dangerous operations |
| Memory / Context | PostgreSQL persistence, memory governance, context injection, usage statistics, and token accounting |
| SubAgent / ACP | Built-in SubAgents for exploration, summary, title generation, and compaction, plus remote agent / ACP integration |
| IM Channel | Feishu, DingTalk, WeCom, WeChat, and other channels reuse the same session, permission, HITL, and audit pipeline |
| Ops Workbench | Web console for LLM, Prompt, Skill, Channel, users, quota, scheduled tasks, quality governance, and runtime configuration |

## Preview

**Chat Runtime**

![Chat Runtime](assets/screenshots/chat-runtime.png)

The main chat workspace hosts sessions, streaming replies, tool calls, HITL, attachments, Todos, and execution status.

**Feishu Channel**

![Feishu Channel](assets/screenshots/feishu-chat.jpg)

Feishu ingress reuses the same session, permission, tool-call, and audit pipeline, so teams can trigger and track agent tasks directly in IM.

**WeChat Channel**

![WeChat Channel](assets/screenshots/wechat-chat.jpg)

WeChat ingress connects through the unified Channel Runtime. IM messages, agent replies, and execution history still flow into the same traceable chain.

**Session Replay**

![Session Replay](assets/screenshots/session-replay.png)

Session Replay shows messages, tool calls, quality events, trace data, and key decisions on a timeline for agent behavior review.

**Control Plane**

![Control Plane](assets/screenshots/settings-control-plane.png)

The console centralizes LLM, Prompt, Skill, Channel, permission, Memory, quality governance, and runtime configuration.

## Quick Start

### One Prompt for a Coding Agent

If you use Codex, Claude Code, Cursor, Windsurf, or another coding agent, you can paste this prompt:

```text
If agents-hive is not cloned yet, clone https://github.com/chef-guo/agents-hive.git, then follow the Docker Compose path in README: create .env, build hive-sandbox:latest, run docker compose up -d, and tell me the access URL plus any missing configuration.
```

This prompt tells the coding agent to prefer Docker Compose, which avoids common setup misses around the sandbox image, PostgreSQL, and embedded frontend build.

### Docker Compose

The Docker deployment includes the Hive service and PostgreSQL. The Hive service embeds the frontend static assets and uses the host Docker socket to create sandbox containers for isolated execution.

```bash
git clone https://github.com/chef-guo/agents-hive.git
cd agents-hive

# Use a strong password in production.
cat > .env <<EOF
POSTGRES_PASSWORD=your_strong_password
DOCKER_GID=$(stat -c '%g' /var/run/docker.sock)
TZ=Asia/Shanghai
HIVE_PORT=8080
EOF

mkdir -p /opt/hive/workdir/sessions

# Sandbox containers run on the host Docker daemon, so build the sandbox image first.
docker build -t hive-sandbox:latest -f docker/sandbox/Dockerfile .

docker compose up -d
docker compose logs -f hive
```

Open:

```text
http://localhost:8080
```

To build only the main service image:

```bash
docker build -t hive:latest .
```

The sandbox bind mount path must be identical on the host and inside the Hive container. The default is `/opt/hive/workdir`. If you change it, update both [docker-compose.yml](docker-compose.yml) and [docker/config.docker.json](docker/config.docker.json).

### Local Development

Local development requires Go 1.25+, Node.js, and PostgreSQL.

```bash
git clone https://github.com/chef-guo/agents-hive.git
cd agents-hive

cp config.example.json config.json
# Edit config.json or set POSTGRES_* / DATABASE_URL environment variables.
# Initial LLM config can be injected with CLAW_API_KEY / OPENAI_API_KEY;
# later changes can be made in the Web UI.

cd frontend
npm install
npm run build
cd ..

go build -o claw ./cmd/claw
go build -o server ./cmd/server
```

Start the backend:

```bash
./server --config config.json
```

Start the frontend dev server:

```bash
cd frontend
npm install
npm run dev
```

CLI mode:

```bash
./claw -c config.json "Analyze the current project structure"
./claw -c config.json -i
```

## Architecture Overview

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
          memory, scheduled tasks, quality data, trace data,
          and accounting data.
```

Key paths:

| Path | Description |
|------|-------------|
| `cmd/claw` | CLI entrypoint |
| `cmd/server` | HTTP server entrypoint |
| `frontend/src` | React admin console and Chat UI |
| `internal/master` | Master Agent, ReAct, plan execution, reflection, and session loop |
| `internal/tools` | Built-in tools, tool search, task tools, and IM tools |
| `internal/mcphost` | MCP host and schema conversion |
| `internal/subagent` | SubAgent framework |
| `internal/acpserver` / `internal/acpclient` | ACP server and client |
| `internal/channel` | Feishu, DingTalk, WeCom, WeChat, and other channels |
| `internal/api` | HTTP API, admin API, and session API |
| `internal/store` | PostgreSQL storage and migrations |
| `internal/bootstrap` | Service startup, scheduled-task recovery, and background loops |
| `internal/agentquality` | Agent quality samples, evaluation, suggestions, and rollback |
| `internal/qualityworkbench` | Quality workbench, replay, grouping, and reports |
| `internal/trajectory` | Session trajectory snapshots |
| `internal/webui/dist` | Frontend build output generated by Vite and embedded by Go |

## Configuration Model

agents-hive uses two configuration layers:

- **Boot configuration**: service listener, logging, database connection, and other values needed before startup. Sources are `config.json`, environment variables, and CLI flags.
- **Runtime configuration**: LLM, Prompt, Skill, Channel, permissions, Memory, MCP, and related settings. These can be changed from the Web UI or API and are stored in PostgreSQL.

Common environment variables:

| Variable | Description |
|----------|-------------|
| `DATABASE_URL` | PostgreSQL DSN; takes precedence over split fields |
| `POSTGRES_HOST` / `POSTGRES_PORT` / `POSTGRES_DB` | PostgreSQL host, port, and database |
| `POSTGRES_USER` / `POSTGRES_PASSWORD` / `POSTGRES_SSL_MODE` | PostgreSQL auth and SSL settings |
| `SESSIONS_DIR` | Session work directory |
| `CUSTOM_TOOLS_DIR` | Custom tools directory |
| `CLAW_API_KEY` / `OPENAI_API_KEY` | Initial LLM configuration on first startup |
| `CLAW_LOG_FILE` / `CLAW_LOG_LEVEL` / `CLAW_CONSOLE_LEVEL` | Logging configuration |

See [config.example.json](config.example.json) for a complete example.

## Web UI

The frontend lives in [frontend](frontend) and uses React, Vite, TypeScript, and Tailwind CSS.

Common commands:

```bash
cd frontend
npm install
npm run dev
npm run build
npm run lint
npm test
```

`npm run build` writes output to `internal/webui/dist/`, which is embedded by the Go service through `internal/webui/embed.go`. Do not edit `internal/webui/dist/` by hand.

Main pages:

- Chat: sessions, tool calls, HITL, attachments, Canvas, and Todos.
- Replay Gallery / Session Replay: session replay and trajectory inspection.
- Preferences: personal theme, language, and user-level WeChat Bot connection.
- Admin Overview: system status and core resource overview.
- Admin Settings: runtime configuration, MCP, permissions, IM channels, external resources, and remote agents.
- Admin Workbench: LLM, Prompt, Skill, users, usage, Memory, Quality Workbench, Auto Optimization, and Scheduled Tasks.

For UI changes, keep the existing component patterns, layout density, color system, and interaction conventions. Do not manually edit `internal/webui/dist/`.

## API Entry Points

Default HTTP API prefix:

```text
http://localhost:8080/api/v1
```

Common resources:

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/health` | Health check |
| `GET` | `/capabilities` | Capability list |
| `POST` | `/sessions` | Create a session |
| `GET` | `/sessions` | List sessions |
| `POST` | `/sessions/{id}/messages` | Send a message |
| `GET` | `/sessions/{id}/messages` | Read messages |
| `GET` | `/sessions/{id}/todos` | Read session todos |
| `GET` | `/sessions/{id}/trace` | Read session trace |
| `GET` | `/sessions/{id}/trajectory/{step}` | Read trajectory snapshot |
| `POST` | `/sessions/{id}/fork` | Fork a session |
| `POST` | `/sessions/{id}/revert` | Revert a session |
| `GET/POST/PUT/DELETE` | `/scheduled-tasks[/{id}]` | Scheduled task CRUD |
| `POST` | `/scheduled-tasks/{id}/toggle` | Enable or disable a scheduled task |
| `POST` | `/scheduled-tasks/{id}/run-now` | Trigger a scheduled task manually |
| `GET` | `/scheduled-tasks/{id}/runs` | Scheduled task run history |
| `GET` | `/admin/scheduled-tasks` | Admin global scheduled task listing |
| `POST/GET/DELETE` | `/channels/push/schedules[/{id}]` | Legacy-compatible IM push scheduling API |
| `GET` | `/ws` | WebSocket real-time events |

See [internal/api/routes.go](internal/api/routes.go) for more routes.

## Development Guidelines

- Format Go code with `gofmt`.
- Use Chinese for Go comments and logs; keep errors structured.
- Prefer table-driven tests.
- Use TypeScript, React, and ESLint for frontend work; follow existing component and styling conventions.
- Do not manually edit `internal/webui/dist/`; generate it by running `npm run build` inside `frontend/`.
- Keep real secrets in local config or environment variables. Do not commit `config.json`, `.env`, or other sensitive files.

Common verification commands:

```bash
go test ./... -v
go test -race ./...
go test -cover ./...

cd frontend
npm run lint
npm run build
npm test
```

## License

MIT License

## Contact

- Issues: https://github.com/chef-guo/agents-hive/issues

## Acknowledgements

![Acknowledgements](assets/screenshots/thank.png)

## Community

![Community](assets/screenshots/chat.jpg)
