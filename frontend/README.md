# Agents Claw - 前端控制台

## 简介

Agents Claw 前端是基于 React + TypeScript + Vite 构建的 AI Agent 管理控制台，提供直观的 Web 界面来管理和交互多 Agent 系统。

**核心功能**：
- 📱 会话管理 - 创建、查看和管理 Agent 对话会话
- 💬 实时聊天 - 与 AI Agent 进行流式对话交互
- 🤝 HITL 审批 - 人机协同决策，审批关键操作
- 🔌 WebSocket 连接 - 实时接收 Agent 状态和事件通知
- 🌐 国际化 - 支持中文和英文界面切换
- 🎨 主题切换 - 明暗主题自由切换

## 快速开始

### 环境要求

- **Node.js** >= 22
- **npm**（仓库跟踪 `package-lock.json`）

### 安装依赖

```bash
cd frontend
npm install
```

### 开发模式

启动开发服务器（默认端口 3000）：

```bash
npm run dev
```

访问 http://localhost:3000 即可打开前端界面。`vite.config.ts` 已把 `/api` 代理到 `http://localhost:8080`，并支持 WebSocket 代理。

### 生产构建

```bash
# 构建生产版本
npm run build

# 预览生产构建
npm run preview
```

构建产物会直接输出到仓库根目录的 `internal/webui/dist/`，由 Go 服务通过 `internal/webui/embed.go` 嵌入。不要手工编辑该目录。

## 环境变量配置

### VITE_API_BASE

前端使用 `VITE_API_BASE` 环境变量配置后端 API 的基础 URL。

- **说明**: 后端 API 基础 URL
- **默认值**: 空字符串（使用当前域名）
- **示例**: `http://localhost:18080`

#### 配置方式

**方式 1: 使用 `.env.local` 文件（推荐）**

在 `frontend/` 目录下创建 `.env.local` 文件：

```bash
# .env.local
VITE_API_BASE=http://localhost:18080
```

**方式 2: 命令行环境变量**

```bash
VITE_API_BASE=http://localhost:18080 pnpm dev
```

**方式 3: 系统环境变量**

```bash
export VITE_API_BASE=http://localhost:18080
pnpm dev
```

#### 工作原理

- 如果设置了 `VITE_API_BASE`，前端将使用该值作为 API 基础 URL
- 如果未设置（默认），前端将使用当前浏览器的域名和端口
- WebSocket 连接会自动根据协议选择 `ws://` 或 `wss://`

**示例**：

```typescript
// 当 VITE_API_BASE = "http://localhost:18080" 时：
// REST API: http://localhost:18080/api/v1/sessions
// WebSocket: ws://localhost:18080/api/v1/ws

// 当 VITE_API_BASE 为空时（生产环境）：
// REST API: https://your-domain.com/api/v1/sessions
// WebSocket: wss://your-domain.com/api/v1/ws
```

## 后端连接

### REST API

前端通过 REST API 与后端通信，处理会话、消息、Agents 和 Skills 等资源操作。

**配置**：
- **Base URL**: `VITE_API_BASE` 或当前域名
- **超时**: 30 秒
- **错误处理**: 自动重试和错误提示

**主要端点**：

| 端点 | 方法 | 说明 |
|------|------|------|
| `/api/v1/sessions` | GET | 获取会话列表 |
| `/api/v1/sessions/{id}` | GET | 获取会话详情 |
| `/api/v1/sessions` | POST | 创建新会话 |
| `/api/v1/sessions/{id}/messages` | POST | 发送消息到会话 |
| `/api/v1/agents` | GET | 获取可用 Agents |
| `/api/v1/skills` | GET | 获取可用 Skills |
| `/api/v1/profiles` | GET | 获取可用 Profiles |
| `/api/v1/hitl/submit` | POST | 提交 HITL 响应 |

### WebSocket 连接

前端使用 WebSocket 接收实时事件和消息流。

**URL 构建**：
```javascript
const proto = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
const host = VITE_API_BASE ? new URL(VITE_API_BASE).host : window.location.host;
const wsUrl = `${proto}://${host}/api/v1/ws`;
```

**连接特性**：
- **自动重连**: 指数退避策略，最多 10 次重试
- **心跳保活**: 定期发送 ping，保持连接活跃
- **状态管理**: 通过 Zustand 管理连接状态

### WebSocket 事件类型

前端通过 WebSocket 接收以下 7 种事件类型：

#### 1. input_request - HITL 输入请求

用于人机协同决策，当 Agent 需要人工审批时触发。

**Payload 结构**：
```json
{
  "type": "input_request",
  "payload": {
    "task_id": "task-123",
    "message": "请审批执行 git push"
  }
}
```

**前端处理**: 显示审批卡片，用户可以批准或拒绝操作。

#### 2. message - 消息流

流式或完整的 Agent 响应消息。

**Payload 结构**：
```json
{
  "type": "message",
  "payload": {
    "content": "正在分析代码...",
    "partial": true,
    "role": "assistant"
  }
}
```

**前端处理**: 实时更新聊天消息，支持流式渲染。

#### 3. agent_status - Agent 状态变化

Agent 的执行状态变化通知。

**Payload 结构**：
```json
{
  "type": "agent_status",
  "payload": {
    "status": "thinking",
    "profile": "code-writer"
  }
}
```

**状态类型**：
- `thinking` - Agent 思考中
- `tool_calling` - 调用工具中
- `completed` - 执行完成
- `error` - 执行错误
- `routing` - 路由到子 Agent

**前端处理**: 显示 Agent 当前状态和 Profile。

#### 4. tool_call - 工具调用事件 🆕

显示 Agent 正在使用的工具（2026-03-18 新增）。

**Payload 结构**：
```json
{
  "type": "tool_call",
  "payload": {
    "tool_name": "read_file",
    "args": "/path/to/file.go"
  }
}
```

**前端显示示例**:
```
🔧 使用工具: read_file
   参数: /path/to/file.go
```

**用途**: 让用户实时看到 Agent 正在执行的具体操作。

#### 5. agent_start - Agent 启动事件 🆕

显示 SubAgent 的启动和任务描述（2026-03-18 新增）。

**Payload 结构**：
```json
{
  "type": "agent_start",
  "payload": {
    "agent_name": "research",
    "task_desc": "分析项目架构"
  }
}
```

**前端显示示例**:
```
🤖 启动 Agent: research
   任务: 分析项目架构
```

**用途**: 展示多 Agent 协作的执行流程。

#### 6. skill_exec - Skill 执行事件 🆕

显示正在执行的 Skill（2026-03-18 新增）。

**Payload 结构**：
```json
{
  "type": "skill_exec",
  "payload": {
    "skill_name": "commit",
    "args": "-m 'fix bug'"
  }
}
```

**前端显示示例**:
```
✨ 执行 Skill: commit
   参数: -m 'fix bug'
```

**用途**: 显示 Agent 使用的高级技能。

#### 7. error - 错误事件

显示错误消息。

**Payload 结构**：
```json
{
  "type": "error",
  "payload": "连接失败"
}
```

**前端处理**: 显示错误提示，记录到日志。

#### 8. ping - 心跳

保持连接活跃，前端自动响应 `pong`。

## 项目结构

```
frontend/
├── src/
│   ├── api/                  # API 客户端
│   │   ├── client.ts         # 通用 HTTP 客户端（Fetch API 封装）
│   │   └── node-client.ts    # 业务 API 封装（Sessions、Agents、Skills 等）
│   ├── hooks/                # React Hooks
│   │   ├── useWebSocket.ts   # WebSocket 连接和事件处理
│   │   ├── useNodeClient.ts  # API 调用 Hook
│   │   ├── useTheme.ts       # 主题切换
│   │   └── useLanguage.ts    # 国际化语言切换
│   ├── store/                # Zustand 状态管理
│   │   ├── app.ts            # 应用全局状态
│   │   ├── chat.ts           # 聊天消息和状态
│   │   ├── hitl.ts           # HITL 审批请求队列
│   │   └── toast.ts          # 提示消息状态
│   ├── components/           # React 组件
│   │   ├── common/           # 通用组件（Button、Input 等）
│   │   └── hitl/             # HITL 审批相关组件
│   ├── layouts/              # 布局组件
│   │   ├── AppShell.tsx      # 主布局（包含 HITL 面板）
│   │   └── Sidebar.tsx       # 侧边栏导航
│   ├── pages/                # 页面组件
│   │   ├── Chat.tsx          # 聊天页面
│   │   ├── Sessions.tsx      # 会话列表
│   │   ├── Dashboard.tsx     # 仪表盘
│   │   ├── Settings.tsx      # 设置页面
│   │   ├── Agents.tsx        # Agent 列表
│   │   └── Skills.tsx        # Skill 列表
│   ├── i18n/                 # 国际化配置
│   │   ├── index.ts          # i18next 初始化
│   │   └── locales/          # 翻译文件（zh、en）
│   ├── types/                # TypeScript 类型定义
│   │   └── api.ts            # API 和 WebSocket 类型
│   ├── utils/                # 工具函数
│   ├── App.tsx               # 根组件
│   ├── main.tsx              # 入口文件
│   └── index.css             # 全局样式
├── public/                   # 静态资源
├── index.html                # HTML 模板
├── vite.config.ts            # Vite 配置
├── tsconfig.json             # TypeScript 配置
├── tsconfig.app.json         # App TypeScript 配置
├── tsconfig.node.json        # Node TypeScript 配置
├── eslint.config.js          # ESLint 配置
├── tailwind.config.js        # Tailwind CSS 配置
├── postcss.config.js         # PostCSS 配置
└── package.json              # 依赖管理
```

## 核心功能

### 1. WebSocket 实时连接

**位置**: `src/hooks/useWebSocket.ts`

**特性**：
- 自动连接管理
- 指数退避重连（最多 10 次）
- 心跳保活
- 连接状态管理
- 自动处理 7 种事件类型

**使用示例**：
```typescript
import { useWebSocket } from '@/hooks/useWebSocket';

function ChatView() {
  const { connected, error, send } = useWebSocket({
    url: wsUrl,
    enabled: true,
  });

  return (
    <div>
      {connected ? <span>✓ 已连接</span> : <span>⚠️ 未连接</span>}
    </div>
  );
}
```

### 2. HITL 人机协同

**位置**: `src/layouts/AppShell.tsx`, `src/components/hitl/`

**特性**：
- 双通道提交（WebSocket 优先，REST fallback）
- 右下角固定审批面板
- 队列管理（支持多个待审批请求）
- 自动显示/隐藏

**工作流程**：
1. 后端发送 `input_request` 事件
2. 前端显示审批卡片
3. 用户点击"批准"或"拒绝"
4. 优先通过 WebSocket 提交，失败则使用 REST API

### 3. 状态管理

使用 Zustand 进行轻量级状态管理：

| Store | 说明 | 主要状态 |
|-------|------|----------|
| `useAppStore` | 应用全局状态 | 主题、语言、侧边栏状态 |
| `useChatStore` | 聊天消息和状态 | 消息列表、流式状态、Agent 状态 |
| `useHitlStore` | HITL 请求队列 | 待审批请求、提交状态 |
| `useToastStore` | 提示消息 | 成功/错误提示 |

**使用示例**：
```typescript
import { useChatStore } from '@/store/chat';

function ChatMessages() {
  const messages = useChatStore(state => state.messages);
  const addMessage = useChatStore(state => state.addMessage);

  return (
    <div>
      {messages.map(msg => <Message key={msg.id} {...msg} />)}
    </div>
  );
}
```

### 4. 国际化 (i18n)

**支持语言**: 中文（zh）、英文（en）

**Hook**: `useLanguage()`

**配置**: `src/i18n/`

**使用示例**：
```typescript
import { useTranslation } from 'react-i18next';

function Navbar() {
  const { t, i18n } = useTranslation();

  return (
    <div>
      <h1>{t('app.title')}</h1>
      <button onClick={() => i18n.changeLanguage('en')}>
        English
      </button>
    </div>
  );
}
```

### 5. 主题切换

**支持主题**: 明亮模式（light）、暗黑模式（dark）

**Hook**: `useTheme()`

**使用示例**：
```typescript
import { useTheme } from '@/hooks/useTheme';

function ThemeToggle() {
  const { theme, toggleTheme } = useTheme();

  return (
    <button onClick={toggleTheme}>
      {theme === 'dark' ? '🌙' : '☀️'}
    </button>
  );
}
```

## 开发指南

### 添加新的 WebSocket 事件类型处理

**步骤**：

1. 在 `src/types/api.ts` 中添加类型定义：

```typescript
export interface NewEventPayload {
  field1: string;
  field2: number;
}
```

2. 在 `src/hooks/useWebSocket.ts` 的 `onmessage` 中添加 case：

```typescript
case 'new_event':
  if (msg.payload) {
    // 处理新事件
    useChatStore.getState().addSystemMessage({
      type: 'new_event',
      content: msg.payload.field1,
    });
  }
  break;
```

3. 在 UI 组件中消费状态：

```typescript
function SystemMessage({ type, content }: SystemMessageProps) {
  const icons = {
    // ...
    new_event: '🎉',
  };

  return (
    <div className="system-message">
      <span className="icon">{icons[type]}</span>
      <span className="content">{content}</span>
    </div>
  );
}
```

### 调用后端 API

使用 `useNodeClient` Hook：

```typescript
import { useNodeClient } from '@/hooks/useNodeClient';

function SessionsList() {
  const client = useNodeClient();
  const [sessions, setSessions] = useState([]);

  useEffect(() => {
    const fetchSessions = async () => {
      try {
        const data = await client.getSessions();
        setSessions(data);
      } catch (error) {
        console.error('获取会话失败:', error);
      }
    };
    fetchSessions();
  }, []);

  return <div>{/* 渲染会话列表 */}</div>;
}
```

### 添加新页面

1. 在 `src/pages/` 创建新页面组件
2. 在 `src/App.tsx` 添加路由：

```typescript
import NewPage from './pages/NewPage';

<Route path="/new-page" element={<NewPage />} />
```

3. 在 `src/layouts/Sidebar.tsx` 添加导航链接

### 国际化文本

1. 在 `src/i18n/locales/zh.json` 和 `en.json` 添加翻译：

```json
// zh.json
{
  "newFeature": {
    "title": "新功能"
  }
}

// en.json
{
  "newFeature": {
    "title": "New Feature"
  }
}
```

2. 在组件中使用：

```typescript
const { t } = useTranslation();
<h1>{t('newFeature.title')}</h1>
```

## 故障排查

### WebSocket 连接失败

**症状**: 前端显示 "⚠️ WebSocket 已断开"

**可能原因和解决方案**：

1. **后端未启用 WebSocket**
   - 检查后端配置 `config.json`:
     ```json
     {
       "hitl": {
         "websocket_enabled": true
       }
     }
     ```

2. **CORS 配置问题**
   - 确认 `config.json` 中的 `server.cors_origins` 包含前端地址：
     ```json
     {
       "server": {
         "cors_origins": ["http://localhost:3000"]
       }
     }
     ```

3. **网络代理问题**
   - 某些代理不支持 WebSocket 升级
   - 尝试禁用代理或配置代理支持 WebSocket

4. **端口冲突**
   - 确认后端服务正在运行且端口正确
   - 检查 `VITE_API_BASE` 配置

**调试步骤**：
```bash
# 1. 检查后端是否运行
curl http://localhost:18080/api/v1/health

# 2. 查看浏览器控制台 Network -> WS 标签
# 3. 检查 WebSocket 握手响应

# 4. 查看后端日志
tail -f ~/.claw/logs/claw.log
```

### CORS 错误

**症状**: 浏览器控制台显示 CORS 错误

**解决方案**：

**开发模式**：

使用 Vite 代理（推荐）：

```typescript
// vite.config.ts
export default defineConfig({
  server: {
    proxy: {
      '/api': {
        target: 'http://localhost:18080',
        changeOrigin: true,
        ws: true, // 代理 WebSocket
      },
    },
  },
});
```

然后设置 `VITE_API_BASE=""` （空字符串）。

**生产模式**：

确认后端 CORS 配置：

```json
{
  "server": {
    "cors_origins": [
      "https://your-frontend-domain.com"
    ]
  }
}
```

### API 请求超时

**症状**: 请求长时间无响应

**解决方案**：

1. 检查后端服务状态
2. 查看后端日志排查慢查询
3. 调整超时配置（`src/api/client.ts`）：

```typescript
const timeout = 60000; // 增加到 60 秒
```

### 状态不同步

**症状**: 前端显示的数据与后端不一致

**调试步骤**：

1. **检查 WebSocket 连接状态**：
   ```typescript
   const { connected } = useWebSocket({ url: wsUrl });
   console.log('WebSocket connected:', connected);
   ```

2. **查看 WebSocket 消息日志**：
   - 打开浏览器开发者工具
   - Network -> WS -> 点击连接 -> Messages 标签

3. **使用 Zustand DevTools**：
   ```typescript
   // 在 store 中启用 DevTools
   import { devtools } from 'zustand/middleware';

   export const useChatStore = create(
     devtools((set) => ({ /* ... */ }))
   );
   ```

4. **手动刷新数据**：
   - 点击刷新按钮或重新加载页面
   - 调用 API 强制同步

## 部署

### Docker 部署

当前仓库不单独发布前端 nginx 镜像。生产部署路径是根目录的多阶段 `Dockerfile`：

1. `frontend-builder` 阶段运行 `npm run build`。
2. Vite 产物写入 `internal/webui/dist/`。
3. `go-builder` 阶段把该目录复制到 Go embed 目录并编译 `cmd/server`。
4. `runtime` 阶段运行内嵌 Web UI 的 Hive 服务。

从仓库根目录构建和启动：

```bash
docker build -t hive:latest .
docker compose up -d
```

如果只想本地验证前端生产构建：

```bash
cd frontend
npm run build
npm run preview
```

### Nginx 配置

**nginx.conf**：

```nginx
server {
  listen 80;
  server_name your-domain.com;

  root /usr/share/nginx/html;
  index index.html;

  # 启用 gzip 压缩
  gzip on;
  gzip_types text/plain text/css application/json application/javascript text/xml application/xml application/xml+rss text/javascript;

  # SPA 路由支持
  location / {
    try_files $uri $uri/ /index.html;
  }

  # API 代理
  location /api/ {
    proxy_pass http://backend:18080;
    proxy_http_version 1.1;
    proxy_set_header Upgrade $http_upgrade;
    proxy_set_header Connection "upgrade";
    proxy_set_header Host $host;
    proxy_set_header X-Real-IP $remote_addr;
    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    proxy_set_header X-Forwarded-Proto $scheme;

    # WebSocket 超时设置
    proxy_read_timeout 3600s;
    proxy_send_timeout 3600s;
  }

  # 缓存静态资源
  location ~* \.(js|css|png|jpg|jpeg|gif|ico|svg|woff|woff2|ttf|eot)$ {
    expires 1y;
    add_header Cache-Control "public, immutable";
  }
}
```

### 环境变量配置

**构建时设置**：

```bash
# 生产环境
VITE_API_BASE=https://api.your-domain.com npm run build

# 或者使用 .env.production 文件
echo "VITE_API_BASE=https://api.your-domain.com" > .env.production
npm run build
```

**注意**：
- Vite 环境变量在构建时被静态替换，不能在运行时动态修改
- 如果需要运行时配置，需要额外设计运行时配置注入；当前 `VITE_*` 变量仍是构建时替换。

### 健康检查

在 Nginx 配置中添加健康检查端点：

```nginx
location /health {
  access_log off;
  return 200 "healthy\n";
  add_header Content-Type text/plain;
}
```

### SSL/HTTPS 配置（推荐）

```nginx
server {
  listen 443 ssl http2;
  server_name your-domain.com;

  ssl_certificate /etc/nginx/ssl/cert.pem;
  ssl_certificate_key /etc/nginx/ssl/key.pem;

  # SSL 优化
  ssl_protocols TLSv1.2 TLSv1.3;
  ssl_ciphers HIGH:!aNULL:!MD5;
  ssl_prefer_server_ciphers on;

  # ... 其他配置同上
}

# HTTP 重定向到 HTTPS
server {
  listen 80;
  server_name your-domain.com;
  return 301 https://$server_name$request_uri;
}
```

## 技术栈

### 核心框架

- **React** 19.2.4 - UI 框架
- **TypeScript** 5.9.3 - 类型安全
- **Vite** 7.0.0 - 构建工具

### 状态管理

- **Zustand** 5.0.11 - 轻量级状态管理

### UI 和样式

- **Tailwind CSS** 4.2.1 - CSS 框架
- **Lucide React** 0.577.0 - 图标库
- **streamdown** 2.5.0 - 流式 Markdown 渲染
- **Shiki** 4.0.2 - 代码高亮

### 路由

- **React Router** 7.13.1 - 客户端路由

### 国际化

- **i18next** 25.8.18 - 国际化框架
- **react-i18next** 16.5.8 - React 集成

### 开发工具

- **ESLint** 9.39.4 - 代码检查
- **TypeScript ESLint** 8.56.1 - TypeScript 规则

### HTTP 和 WebSocket

- **原生 Fetch API** - HTTP 请求
- **原生 WebSocket API** - 实时通信

## 相关文档

- [主 README](../README.md) - 完整的项目文档和架构说明
- [配置示例](../config.example.json) - 后端配置参考
- [API 参考](../README.md#http-api-参考) - REST API 详细文档
- [开发者文档](../DEVELOPER_GUIDE.md) - 当前源码结构、启动链路和排障说明

## 常见问题

### Q: 如何切换到不同的后端地址？

A: 修改 `.env.local` 文件中的 `VITE_API_BASE`，然后重启开发服务器。

### Q: 为什么 WebSocket 事件没有显示？

A: 检查以下几点：
1. WebSocket 连接是否成功（查看连接状态图标）
2. 后端是否启用了 WebSocket（`websocket_enabled: true`）
3. 浏览器控制台是否有错误
4. 后端日志是否有事件发送记录

### Q: 如何查看发送的 WebSocket 消息？

A: 打开浏览器开发者工具 -> Network -> WS -> 选择连接 -> Messages 标签。

### Q: 生产环境如何设置 API 地址？

A: 构建时设置环境变量：
```bash
VITE_API_BASE=https://api.production.com npm run build
```

### Q: 如何添加新的语言支持？

A:
1. 在 `src/i18n/locales/` 添加新的语言文件（如 `ja.json`）
2. 在 `src/i18n/index.ts` 中导入新语言
3. 更新语言选择器组件

---

**项目维护**: Agents Claw Team
**最后更新**: 2026-05-16
**前端版本**: 0.0.0
