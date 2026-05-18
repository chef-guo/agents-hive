#!/usr/bin/env bash
# 为 docker-compose 宿主机 bind mount 选择路径（左侧源路径）。
#
# 优先级：
#   1. 环境变量 HIVE_WORKDIR_HOST 已设置 → 原样输出（最高优先级）
#   2. HIVE_WORKDIR_FORCE_PROJECT=1 → ./.hive-workdir（Linux 本机开发不想用 /opt 时）
#   3. 自动：Darwin / Windows 类 Git Bash → ./.hive-workdir（避免 Docker Desktop 不共享 /opt）
#   4. 自动：Linux → /opt/hive/workdir（常见服务器 FHS 布局）
#
# 与 Makefile 的 docker-up / docker-setup 配合；直接运行 docker compose 时
# compose 文件仍带默认值 ./.hive-workdir，不依赖本脚本。

if [[ -n "${HIVE_WORKDIR_HOST:-}" ]]; then
  printf '%s' "$HIVE_WORKDIR_HOST"
  exit 0
fi

if [[ "${HIVE_WORKDIR_FORCE_PROJECT:-0}" == "1" ]]; then
  printf '%s' "./.hive-workdir"
  exit 0
fi

case "$(uname -s 2>/dev/null)" in
Darwin | MINGW* | MSYS* | CYGWIN*)
  printf '%s' "./.hive-workdir"
  ;;
Linux)
  printf '%s' "/opt/hive/workdir"
  ;;
*)
  printf '%s' "./.hive-workdir"
  ;;
esac
