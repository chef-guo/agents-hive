#!/usr/bin/env bash
# wechatbot_legacy_audit.sh — 发布前旧个人微信协议回流审计
#
# 这是手动发布检查，不绑定 CI workflow。docs/ 允许保留历史说明和计划项；
# 产品代码、前端源码、配置示例和 Go 依赖不允许再出现旧协议入口。
#
# 退出码：0=通过，1=发现回流，2=环境错误。
set -euo pipefail

ROOT="${REPO_ROOT:-$(cd "$(dirname "$0")/.." && pwd)}"
cd "$ROOT"

echo "== wechatbot legacy protocol audit =="

scan_paths=(
  internal
  cmd
  frontend/src
  config.example.json
  go.mod
  go.sum
)

patterns=(
  'wechatpadpro'
  'wechaty'
  'wechat-wechatpadpro'
  'wechat-wechaty'
)

violations=0
for pattern in "${patterns[@]}"; do
  hits=$(grep -rEn -i "$pattern" "${scan_paths[@]}" 2>/dev/null || true)
  if [ -n "$hits" ]; then
    echo "FAIL: legacy WeChat protocol [$pattern] found in product path:"
    echo "$hits" | sed 's/^/  - /'
    violations=$((violations + 1))
  else
    echo "PASS: $pattern not found"
  fi
done

echo
if [ "$violations" -eq 0 ]; then
  echo "ALL_PASS"
  exit 0
fi

echo "FAILED ($violations legacy protocol pattern(s))" >&2
exit 1
