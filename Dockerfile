# =============================================================================
# Hive — 多阶段构建
#
# 阶段说明：
#   frontend-builder  Node.js 构建前端静态资源（React/Vite）
#   go-builder        Go 编译二进制，将前端 dist 嵌入（go:embed）
#   runtime           最终运行镜像，基于 docker:cli，含 docker CLI 供 sandbox 使用
# =============================================================================

# -----------------------------------------------------------------------------
# Stage 1: 前端构建
# -----------------------------------------------------------------------------
FROM node:22.21.1 AS frontend-builder
# vite.config.ts 的 outDir 是 ../internal/webui/dist（相对 frontend/），
# 产物会落到 /src/internal/webui/dist/。镜像内 repo 结构与宿主机一致。
WORKDIR /src/frontend

# 根因：package-lock.json 在 darwin 生成，缺少 linux-* 平台的 native binding 条目
# （lightningcss / @tailwindcss/oxide 都命中）。npm CLI issue #4828 官方推荐：
# 构建阶段删除 lock + node_modules，重新解析。用 --prefer-offline 复用缓存避免重下。
# 产出的 lock 不回写，保留开发机的 darwin 版 lock 不变。
COPY frontend/package.json frontend/package-lock.json ./
RUN rm -f package-lock.json && \
    npm install --prefer-offline --no-audit --no-fund

# 再复制源码并构建
COPY frontend/ ./
RUN npm run build
# 产物直出到 /src/internal/webui/dist/（vite outDir=../internal/webui/dist）

# -----------------------------------------------------------------------------
# Stage 2: Go 二进制构建
# -----------------------------------------------------------------------------
FROM golang:1.25.1 AS go-builder
ARG TARGETARCH=amd64
WORKDIR /src

# golang:1.25.1 (Debian-based) 内置 git 2.47，无需额外安装

# 先下载依赖，利用缓存层
COPY go.mod go.sum ./
RUN go mod download

# 复制全部源码
COPY . .

# 将前端 dist 写入 go:embed 扫描目录
# internal/webui/embed.go 声明 //go:embed dist/*
COPY --from=frontend-builder /src/internal/webui/dist ./internal/webui/dist/

# 编译（CGO 禁用，生成静态链接二进制）
RUN CGO_ENABLED=0 GOOS=linux GOARCH=${TARGETARCH} \
    go build \
      -trimpath \
      -ldflags="-s -w" \
      -o /hive \
      ./cmd/server

# -----------------------------------------------------------------------------
# Stage 3: 运行时镜像
#
# 使用官方 docker:cli 镜像（Alpine-based），内置 docker CLI。
# Hive 通过 bind mount /var/run/docker.sock 与宿主机 Docker daemon 通信，
# 在宿主机上启动/管理 sandbox 容器（DooD 模式，非真正 DinD）。
# -----------------------------------------------------------------------------
FROM docker:27-cli AS runtime

# 安装运行时必要工具
#   bash / ca-certificates / tzdata  基础
#   nodejs npm                        agent-browser 通过 npm 安装
#   python3 py3-pip py3-virtualenv    MinerU PDF Markdown provider
#   chromium chromium-chromedriver    agent-browser 驱动的真浏览器
#   nss freetype harfbuzz ttf-freefont  Chromium 渲染依赖
#   font-noto-cjk                     中文页面截图避免豆腐块
#   wget                              HEALTHCHECK 已用
RUN apk add --no-cache \
        bash ca-certificates tzdata wget \
        nodejs npm python3 py3-pip py3-virtualenv \
        chromium chromium-chromedriver \
        nss freetype harfbuzz ttf-freefont \
        font-noto-cjk font-noto-emoji

# agent-browser: vercel-labs 的浏览器自动化 CLI，internal/tools/browser.go 依赖它
# PUPPETEER_SKIP_CHROMIUM_DOWNLOAD 阻止下载 Chrome for Testing（~300MB），复用系统 chromium
ENV PUPPETEER_SKIP_CHROMIUM_DOWNLOAD=true \
    PUPPETEER_EXECUTABLE_PATH=/usr/bin/chromium-browser \
    CHROME_BIN=/usr/bin/chromium-browser \
    NPM_CONFIG_FUND=false \
    NPM_CONFIG_UPDATE_NOTIFIER=false
RUN npm install -g agent-browser && \
    rm -rf /root/.npm /tmp/*

# KB PDF ingest 默认使用 MinerU。镜像构建时先安装一次；运行时仍会按
# fileconv.markdown.pdf 配置自检，缺失时可自动安装或 fail-fast。
RUN python3 -m pip install --break-system-packages --no-cache-dir uv && \
    uv pip install --system -U "mineru[all]" && \
    rm -rf /root/.cache/pip /tmp/*

# 创建非 root 用户运行应用
# hive 需要访问 /var/run/docker.sock，docker.sock 的 gid 在宿主机上通常为 999 或 970，
# 通过 docker-compose.yml 的 group_add 传入，无需写死在镜像里。
RUN addgroup -S hive && adduser -S hive -G hive

# 创建应用目录结构。命名卷首次挂载会继承镜像内目录权限；
# bind mount 的宿主机路径仍需由部署者保证 hive 用户可写。
RUN mkdir -p /app /data/logs /data/tools /opt/hive/workdir && \
    chown -R hive:hive /app /data /opt/hive/workdir

WORKDIR /app

# 复制编译好的二进制
COPY --from=go-builder /hive /app/hive
COPY docker/config.docker.json /app/config.json
RUN chmod +x /app/hive && chown hive:hive /app/hive

# 暴露 HTTP API + WebUI 端口
EXPOSE 18080

# 健康检查
HEALTHCHECK --interval=15s --timeout=5s --start-period=30s --retries=3 \
    CMD wget -qO- http://localhost:18080/api/v1/health || exit 1

USER hive

ENTRYPOINT ["/app/hive"]
CMD ["--config", "/app/config.json"]
