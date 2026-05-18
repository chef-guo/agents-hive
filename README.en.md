# agents-hive

**Language / 语言:** [中文](README.md) | English

[![Go Version](https://img.shields.io/badge/Go-1.25+-00ADD8?style=flat&logo=go)](https://golang.org)
[![Node.js](https://img.shields.io/badge/Node.js-22+-339933?style=flat&logo=nodedotjs&logoColor=white)](https://nodejs.org)
[![React](https://img.shields.io/badge/React-19-61DAFB?style=flat&logo=react&logoColor=111111)](https://react.dev)
[![TypeScript](https://img.shields.io/badge/TypeScript-5.9-3178C6?style=flat&logo=typescript&logoColor=white)](https://www.typescriptlang.org)
[![PostgreSQL](https://img.shields.io/badge/PostgreSQL-16-4169E1?style=flat&logo=postgresql&logoColor=white)](https://www.postgresql.org)
[![Docker](https://img.shields.io/badge/Docker-Compose-2496ED?style=flat&logo=docker&logoColor=white)](https://docs.docker.com/compose/)
[![License](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

**Repository:** [GitHub](https://github.com/chef-guo/agents-hive) | [Gitee mirror](https://gitee.com/smart_kitchen/agents-hive)

**Developer Guide:** [DEVELOPER_GUIDE.md](DEVELOPER_GUIDE.md)

agents-hive is an engineering runtime and quality control plane for ReAct agents. It does more than connect models to tools: it brings task ingress, planning, tool calls, human approval, SubAgent collaboration, memory context, IM delivery, execution tracing, quality evaluation, optimization, and rollback into one traceable and governable runtime chain.

The hard production problems are not just "how does the model call a function?" They are: why did the agent make this decision, which capabilities did it call, did it cross a permission boundary, where did the failure happen, can the run be replayed and evaluated, and can the next run avoid the same class of mistake? Hive turns agents from chat assistants with tools into hosted, constrained, auditable, scored, regression-tested, and continuously improving execution units.

In one line: **agents-hive = Agent Runtime + Agent Harness + Quality Control Plane + Ops Workbench**.

## Long-Term Goal: Control Plane + Local Worker Hive

Hive is not meant to be only a server-side agent runtime. The long-term goal is a governed agent control plane: the central system owns users, sessions, task orchestration, policy, audit, quality evaluation, and artifact storage; users can install a Hive CLI / Worker daemon on their own machines, intranet hosts, or dedicated compute nodes, and those nodes connect outbound to Hive as workers in the hive.

This lets agents use local environments under explicit control: local projects, shell commands, private networks, private models, or heavy compute. These capabilities must remain governed by central identity, policy, HITL, logs, and object storage, rather than becoming unaudited remote control of user machines.

The current `nodes` / `task_queue` tables are reserved schema for this direction: future worker registration, heartbeat, capability reporting, task claim, lease, retry, and result return. The generic Worker mode is not yet a complete production loop today; current background execution still lives in domain-specific systems such as scheduled tasks, Feishu retry/reclaim, embedding backlog, and Master internal queues.

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
| Worker / Node | Target direction: local CLI / daemon workers connect to the central control plane as hive nodes; current `nodes` / `task_queue` schema is reserved and the full generic worker loop is still under development |
| Ops Workbench | Web console for LLM, Prompt, Skill, Channel, users, quota, scheduled tasks, quality governance, and runtime configuration |

## Preview

<p align="center">
  <img src="assets/diagrams/hive-overview.svg" alt="agents-hive overview" width="100%">
</p>

agents-hive is not just a chat UI. It is a control plane for real agent work: ingress, runtime, permission, tools, knowledge, object storage, quality evaluation, and Worker nodes all flow through the same governable chain.

<table>
  <tr>
    <td width="33%" valign="top">
      <img src="assets/diagrams/runtime-flow.svg" alt="Runtime Flow"><br>
      <strong>Runtime Flow</strong><br>
      User requests enter the Plan / ReAct loop, tool calls pass through policy, HITL, and sandbox controls, and execution traces feed replay, eval, and rollback loops.
    </td>
    <td width="33%" valign="top">
      <img src="assets/diagrams/worker-hive.svg" alt="Local Worker Hive"><br>
      <strong>Local Worker Hive</strong><br>
      Local CLI / daemon workers, intranet machines, and compute nodes connect outbound to the central control plane, claim tasks under policy, and return artifacts.
    </td>
    <td width="33%" valign="top">
      <img src="assets/diagrams/kb-storage.svg" alt="Knowledge Base and Unified Storage"><br>
      <strong>Knowledge Base + Unified Storage</strong><br>
      Markdown, PDF/OCR, images, and attachments enter the KB, embedding, evidence citation, and S3/MinIO object storage pipeline.
    </td>
  </tr>
</table>

These SVGs are product and architecture diagrams for the target shape and core execution paths. The actual UI remains the Web console, Chat Runtime, IM channels, and Replay pages.

## Quick Start

### One Prompt for a Coding Agent

If you use Codex, Claude Code, Cursor, Windsurf, or another coding agent, you can paste this prompt:

```text
If agents-hive is not cloned yet, clone https://github.com/chef-guo/agents-hive.git; if GitHub access is unstable, use https://gitee.com/smart_kitchen/agents-hive.git instead. Then follow the Docker Compose path in README: create .env, build hive-sandbox:latest, run docker compose up -d, and tell me the access URL plus any missing configuration.
```

This prompt tells the coding agent to prefer Docker Compose, which avoids common setup misses around the sandbox image, PostgreSQL, and embedded frontend build.

### Docker Compose

The Docker deployment includes the Hive service, PostgreSQL, and MinIO. The Hive service embeds the frontend static assets and uses the host Docker socket to create sandbox containers for isolated execution. MinIO is the default unified object storage backend for KB images, chat attachments, and Agent artifacts.

```bash
git clone https://github.com/chef-guo/agents-hive.git
# If GitHub access is unstable, use the Gitee mirror:
# git clone https://gitee.com/smart_kitchen/agents-hive.git
cd agents-hive

# Use a strong password in production.
# Linux: DOCKER_GID=$(stat -c '%g' /var/run/docker.sock)
# macOS: DOCKER_GID=$(stat -f '%g' /var/run/docker.sock)
cat > .env <<EOF
POSTGRES_PASSWORD=your_strong_password
DOCKER_GID=$(stat -f '%g' /var/run/docker.sock 2>/dev/null || stat -c '%g' /var/run/docker.sock)
TZ=Asia/Shanghai
HIVE_PORT=18080
MINIO_ROOT_USER=minioadmin
MINIO_ROOT_PASSWORD=minioadmin
MINIO_BUCKET=hive-assets
EOF

# Default host workdir is ./.hive-workdir (see docker-compose.yml); avoids macOS /opt mount denial.
mkdir -p .hive-workdir/sessions
# The Hive container runs as a non-root user; the bind-mounted workdir must be writable.
chmod 0777 .hive-workdir .hive-workdir/sessions

# Sandbox containers run on the host Docker daemon, so build the sandbox image first.
docker build -t hive-sandbox:latest -f docker/sandbox/Dockerfile .

# The image embeds docker/config.docker.json and starts with --config /app/config.json by default.
docker compose up -d
docker compose logs -f hive
```

Open:

```text
http://localhost:18080
```

To build only the main service image:

```bash
docker build -t hive:latest .
```

Inside the Hive container the workdir prefix is always `/opt/hive/workdir` (matching `sessions_dir` in [docker/config.docker.json](docker/config.docker.json)). Prefer **`make docker-up`** / **`make docker-setup`**: they pick a host bind-mount path automatically (macOS / Git-Bash-style Windows → `./.hive-workdir`; Linux → `/opt/hive/workdir`). Plain **`docker compose up`** without `HIVE_WORKDIR_HOST` defaults to `./.hive-workdir` (safe everywhere). Override with `HIVE_WORKDIR_HOST` or `HIVE_WORKDIR_FORCE_PROJECT=1` (force project dir on Linux when using `make`). The host directory must be writable by the non-root `hive` user inside the container.

Unified object storage defaults to the Compose MinIO service, and the bucket is created by `minio-init`. Local or single-node deployments can use `asset.provider=local`; production deployments can set `asset.provider=s3` for AWS S3 or another S3-compatible service.

### Local Development

Local development requires Go 1.25+, Node.js, and PostgreSQL.

```bash
git clone https://github.com/chef-guo/agents-hive.git
# If GitHub access is unstable, use the Gitee mirror:
# git clone https://gitee.com/smart_kitchen/agents-hive.git
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
# Requires PostgreSQL: you can start only the DB (compose maps 5432 to localhost for config.json).
docker compose up -d postgres
# If config uses ${POSTGRES_PASSWORD}, export the same value as in .env, for example:
export POSTGRES_PASSWORD=same_as_in_dot_env
./server --config config.json
```

Or use a locally installed PostgreSQL with the `claw` database and matching credentials, then run `./server --config config.json` directly.

Start the frontend dev server:

```bash
cd frontend
npm install
# Defaults match config/vite (18080). If the backend listens elsewhere, set:
# echo 'VITE_DEV_API_TARGET=http://localhost:<port>' > frontend/.env.development.local
npm run dev
```

The Vite dev server currently listens on `http://localhost:3000` and proxies `/api` to `http://localhost:8080`.

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
| `HIVE_WORKDIR_HOST` | (Compose) Host path bind-mounted to `/opt/hive/workdir`; unset defaults to `./.hive-workdir`; `make docker-up` uses `/opt/hive/workdir` on Linux automatically |
| `HIVE_WORKDIR_FORCE_PROJECT` | Set to `1` to force `./.hive-workdir` on Linux even when using `make` |
| `CUSTOM_TOOLS_DIR` | Custom tools directory |
| `ASSET_PROVIDER` / `ASSET_LOCAL_BASE_PATH` | Unified object storage provider and local storage path |
| `MINIO_ENDPOINT` / `MINIO_ACCESS_KEY` / `MINIO_SECRET_KEY` / `MINIO_BUCKET` | MinIO / S3-compatible object storage settings |
| `S3_ENDPOINT` / `S3_ACCESS_KEY` / `S3_SECRET_KEY` / `S3_BUCKET` / `S3_REGION` / `S3_USE_SSL` | AWS S3 or another S3-compatible provider; set `S3_USE_SSL=false` explicitly for HTTP-compatible endpoints |
| `FILECONV_PDF_PROVIDER` | KB PDF-to-Markdown provider, default `mineru`; can be `external` or `none` |
| `FILECONV_PDF_BIN` / `FILECONV_PDF_ARGS` | Override the PDF provider command and arguments |
| `FILECONV_PDF_INSTALL_ENABLED` / `FILECONV_PDF_INSTALL_DIR` | MinerU startup self-check and auto-install switch/path |
| `CLAW_API_KEY` / `OPENAI_API_KEY` | Initial LLM configuration on first startup |
| `CLAW_LOG_FILE` / `CLAW_LOG_LEVEL` / `CLAW_CONSOLE_LEVEL` | Logging configuration |

See [config.example.json](config.example.json) for a complete example.

## KB Documents And PDF

Upload knowledge-base documents from the Admin Knowledge Base page or `POST /api/v1/kb/namespaces/{namespace}/documents:ingest-markdown`. This endpoint only accepts `multipart/form-data`: use `file` for the document, repeated `assets` fields for Markdown-referenced images, or `markdown` / `content` for direct text. Non-multipart requests return 415; there is no JSON/base64 ingest API.

Markdown, plain text, and DOCX enter the same Markdown ingest pipeline. PDF defaults to `fileconv.markdown.pdf.provider=mineru`; MinerU emits Markdown plus image assets, images are stored through `internal/asset`, and Markdown references are rewritten to internal `asset://` URIs. `asset://` is not a public URL; the frontend resolves it through the asset API to get a short-lived access URL.

KB retrieval uses PageIndex-style tree mode, not a separate vector database. The agent calls `kb.doc.meta`, then `kb.doc.structure`, then `kb.section.text` with tight `node_ids` or PDF page-anchor `page_ranges`. `kb.doc.meta` returns `page_count`, `line_count`, and `node_count`, so the agent can judge document scale before selecting tight ranges. When PDF/MinerU or an external provider emits Markdown with markers such as `<physical_index_5>`, `<page_5>`, `<!-- page: 5 -->`, or `[[page=5]]`, KB stores them as `start_page/end_page` in the structure tree and supports `page_ranges: ["5-7"]` to fetch exact text plus in-page image `asset_refs`.

When MinerU is configured, server startup checks whether `mineru` is executable. If it is missing and `install.enabled=true`, Hive creates an isolated Python venv under `fileconv.markdown.pdf.install.install_dir` and installs `mineru[all]`. Install failure is fail-fast; PDF ingest does not create degraded placeholder documents. To use another OCR/layout/model tool, set provider to `external` and configure a command that writes a Markdown file plus an asset directory.

### Default administrator and permissions (Web auth)

When the **auth service is ready** (PostgreSQL available and user tables initialized) and the **`users` table is still empty**, the system seeds a local administrator: **username `admin`**, **initial password `admin`**, with role **`admin`**. Change the password promptly on production or public deployments.

**User administration** and **invite-code** features (admin console and matching APIs) are available **only to users with the `admin` role**.

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
http://localhost:18080/api/v1
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
