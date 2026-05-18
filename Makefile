.PHONY: build-sandbox-image validate-skills test test-specdriven \
        docker-build docker-up docker-down docker-logs docker-setup \
        frontend-build frontend-embed hive-build hive-run

# Sprint 1.2 Round 4 红线：test-specdriven recipe 依赖 `set -o pipefail`，
# 这是 bash 内建；Debian/Ubuntu 的默认 /bin/sh → dash 在 0.5.11+ 才支持。
# 直接 pin 到 bash，避免 CI runner image 差异。
SHELL := /bin/bash

SANDBOX_IMAGE ?= hive-sandbox:latest
HIVE_IMAGE    ?= hive:latest

# docker compose 使用的宿主机工作目录（make 目标里若未设置则按 OS 探测）
hive_workdir_host = $(shell bash "$(CURDIR)/docker/hive-workdir-host.sh")

# 构建沙箱 Docker 镜像（必须在宿主机上预先构建，sandbox 容器运行在宿主机 Docker daemon 上）
build-sandbox-image:
	docker build -t $(SANDBOX_IMAGE) -f docker/sandbox/Dockerfile .

# 构建 Hive 主服务镜像（多阶段：前端 → Go 二进制 → runtime）
docker-build:
	docker build -t $(HIVE_IMAGE) .

# 首次部署初始化（构建所有镜像 + 按 OS 创建 workdir/sessions）
docker-setup: build-sandbox-image docker-build
	@export HIVE_WORKDIR_HOST="$${HIVE_WORKDIR_HOST:-$(hive_workdir_host)}"; \
	mkdir -p "$${HIVE_WORKDIR_HOST}/sessions"; \
	echo "已创建会话目录: $${HIVE_WORKDIR_HOST}/sessions"
	@if [ ! -f .env ]; then \
		echo "POSTGRES_PASSWORD=changeme" > .env; \
		echo "已生成 .env，请修改 POSTGRES_PASSWORD"; \
	fi
	@echo "初始化完成，执行 make docker-up 启动服务"

# 启动所有服务（后台运行）；自动设置 HIVE_WORKDIR_HOST（Mac→项目目录，Linux→/opt/hive/workdir）
docker-up:
	@export HIVE_WORKDIR_HOST="$${HIVE_WORKDIR_HOST:-$(hive_workdir_host)}"; \
	echo "HIVE_WORKDIR_HOST=$${HIVE_WORKDIR_HOST}"; \
	docker compose up -d

# 停止所有服务（保留数据卷）
docker-down:
	@export HIVE_WORKDIR_HOST="$${HIVE_WORKDIR_HOST:-$(hive_workdir_host)}"; \
	docker compose down

# 查看实时日志
docker-logs:
	@export HIVE_WORKDIR_HOST="$${HIVE_WORKDIR_HOST:-$(hive_workdir_host)}"; \
	docker compose logs -f hive

# 校验所有 skills/*/SKILL.md 的 frontmatter 格式
# 检查必填字段：name, description, license
validate-skills:
	@echo "Validating skill frontmatters..."
	@failed=0; \
	for md in skills/*/SKILL.md; do \
		skill=$$(dirname $$md | xargs basename); \
		missing=""; \
		grep -q "^name:" $$md || missing="$$missing name"; \
		grep -q "^description:" $$md || missing="$$missing description"; \
		grep -q "^license:" $$md || missing="$$missing license"; \
		if [ -n "$$missing" ]; then \
			echo "  FAIL $$skill: missing fields:$$missing"; \
			failed=1; \
		else \
			echo "  OK   $$skill"; \
		fi; \
	done; \
	if [ $$failed -eq 1 ]; then echo "Validation failed."; exit 1; fi; \
	echo "All skills valid."

# 运行所有单元测试
test:
	go test ./...

# spec-driven 认知层 CI 闸门：跑 harness + race detector + 覆盖率阈值 75%
# 任一失败（fixture 必选失败 / race 检测到 / coverage 不达线 / 任何 --- SKIP:）
# 都必须返回非零退出码，CI 据此决定是否允许把 spec_driven.mode 从 legacy 升到 dual。
#
# 包范围（Codex P0-5 + Sprint 1.2 R4 红线）：严格对齐
# openspec/changes/harden-spec-driven-phase2/specs/spec-eval-harness/spec.md#L67 —
# 同时跑 ./internal/specdriven/... 和 ./internal/master/...，后者是 spec 层在
# master 侧的胶水（lifecycle / session_loop）。
#
# Sprint 1.2 扩 -coverpkg + 扩 test package list：
#   - test package list 加入 ./internal/store/...：Sprint 1.2 blue army 自检发现的 P0——
#     `./internal/specdriven/...` 根本不 import `internal/store`，只扩 -coverpkg 不扩
#     测试包 = store 测试从不 run = SKIP→RED 永远触发不了 = 纸老虎。必须同时扩。
#   - -coverpkg 加 ./internal/store/...：让 master/specdriven 测试执行时对 store 代码
#     的调用进入覆盖分母（Sprint 2.3 CAS 三路 counter 前置，Codex R5-3 红线：
#     duplicate-create / ghost-id / stale-rev 三路都必须 emit `cas_conflict_total`）。
#
# Go 1.25 `covdata` 缺失问题通过 CI 镜像 pin `golang:1.25.1` 规避。
# testlog tee 到 coverage-specdriven.testlog 供 SKIP→RED 检测消费。
# `set -o pipefail` 确保 `go test` 非零时 tee 不把 exit code 吞掉（Makefile 顶部
# pin `SHELL := /bin/bash` 保证 pipefail 可用）。
test-specdriven:
	set -o pipefail; \
	go test -v ./internal/specdriven/... ./internal/master/... ./internal/store/... -race \
		-coverpkg=./internal/specdriven/...,./internal/store/... \
		-coverprofile=coverage-specdriven.out 2>&1 | tee coverage-specdriven.testlog
	bash scripts/check_specdriven_coverage.sh coverage-specdriven.out 75 coverage-specdriven.testlog

# 前端改动 → embed → 二进制 全链路落地
# vite.config.ts 的 outDir 已指向 ../internal/webui/dist/，产物直出 embed 目录，
# 无需再 rsync 搬运。internal/webui/embed.go 的 //go:embed dist/* 天然认到。
frontend-build:
	cd frontend && npm run build

# 兼容保留：旧脚本/CI 可能仍 invoke `make frontend-embed`。现在它等价于 frontend-build。
frontend-embed: frontend-build
	@echo "[frontend-embed] deprecated alias — vite 已直出到 internal/webui/dist/，无需再搬运"

hive-build: frontend-build
	go build -o ./hive ./cmd/server
	@echo "[hive-build] ./hive binary updated. kill the running backend and relaunch with: ./hive --config config.json"

# 一键重启（本机开发用）：杀掉 18080 上的 hive 进程，重新编译并启动
hive-run: hive-build
	@pid=$$(lsof -ti:18080 -sTCP:LISTEN 2>/dev/null | head -1); \
	if [ -n "$$pid" ]; then echo "killing old backend PID $$pid"; kill $$pid; sleep 1; fi
	./hive --config config.json
