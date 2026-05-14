#!/usr/bin/env bash
# check_specdriven_coverage.sh — dual-flag 上线前的覆盖率闸门
# 用法: check_specdriven_coverage.sh <coverprofile> <min-percent> [testlog]
#
# 计量口径（Codex P0-4/P0-5 红线修复）：
#   改用 `go tool cover -func` 输出的末尾 `total:` 行，这是 Go 官方口径的
#   line-based 加权覆盖率——按"语句数"加权，跟 spec-eval-harness/spec.md
#   里 "≥75% line coverage" 的要求一一对应。
#
#   旧实现对每个 func 的百分比做算术平均（pct_sum/n），是 function-average
#   而不是 line coverage：一个 1 行的 helper 100% 和一个 200 行的 resolver
#   20% 会被当作 60% 平均，严重掩盖主逻辑未测的真相。Codex 直接把这条
#   定性为"假门"。
#
# 过滤：内部 spec-driven 模块的真实作用域——
#   - internal/specdriven/** （整个包都是 spec 模块）
#   - internal/store/spec_*.go （只收 spec_store.go / spec_session_store.go）
#
# Sprint 1.2 Round 4 蓝军自检红线（denominator 稀释陷阱）：
# 早期版本是 `internal/(specdriven|store)/` 通配，看似合理——但 `-coverpkg=./internal/store/...`
# 把整个 store 包（memory_store / postgres / prompt_store / seed / skill_store）全部
# 拉进 instrumentation，这些跟 spec 模块完全不相关的文件以 0% 身份混进分母，直接
# 把真实的 88.6% 稀释到 36.5%——阈值 75% 怎么设都过不了，**gate 变成永假**，
# 和纸老虎 grep 同属一类"假门"。
# 收紧到 `store/spec_` prefix 才是 Sprint 1.2 真实作用域：specdriven 全家 + 2 个
# spec_* 文件，Go 官方 line coverage = 88.6% → 阈值 75% 变成有效门槛。
# 忽略 testdata 与 *_test.go。加权是按"语句数"而不是 func 数。
#
# Sprint 1.2 SKIP→RED 闸门（Round 4 Codex 红线）：
#   testlog 参数可选，若提供则 grep `^--- SKIP:` → 命中即 fail。这一道
#   gate 是 dual-flag 准入硬条件：CI 里 TEST_DATABASE_URL 若因 service
#   misconfig 未注入，spec_store_test.go / spec_session_store_test.go 会
#   `t.Skip()` 悄悄跳过 PG 集成测试、仍让 total 覆盖率达阈值蒙混过关。
#   SKIP→RED 确保"被静默跳过的 test = workflow fail"，堵死这类假绿。
set -euo pipefail

profile="${1:?coverprofile path required}"
min_pct="${2:?minimum percent required}"
testlog="${3:-}"

# Sprint 1.2 Round 4 红线：任何 --- SKIP: 都必须 fail，不允许"被静默跳过"
# 的 PG 集成 test 混进绿色 CI。
if [[ -n "$testlog" ]]; then
  if [[ ! -f "$testlog" ]]; then
    echo "testlog not found: $testlog" >&2
    exit 1
  fi
  if grep -q '^--- SKIP:' "$testlog"; then
    echo "SKIP→RED: the following tests were skipped:" >&2
    echo "  - store/spec tests usually mean TEST_DATABASE_URL is missing" >&2
    echo "  - other tests usually mean a non-hermetic integration/stress test leaked into this gate" >&2
    grep '^--- SKIP:' "$testlog" >&2
    echo "refusing to promote — blocking dual-flag rollout" >&2
    exit 1
  fi
fi

if [[ ! -f "$profile" ]]; then
  echo "coverage profile not found: $profile" >&2
  exit 1
fi

# 过滤出 specdriven 相关的行写到 tmp profile，再跑一次 total。
# coverage profile 的首行固定是 `mode: <mode>`，必须保留否则 go tool cover 拒解析。
tmp_profile=$(mktemp)
trap 'rm -f "$tmp_profile"' EXIT

# 动态解析 module path（fail-open 到硬编码默认值）——fork / rename 场景下硬编码
# `github.com/chef-guo/agents-hive` 会让 grep 全 miss → fail-closed（L72-76）
# 会正确触发，但错误信息显示"no specdriven/store coverage data"会误导用户以为
# 是覆盖率问题，其实是 module path 对不上。用 `go list -m` 兜底。
if module_path=$(go list -m 2>/dev/null); then :; else module_path="github.com/chef-guo/agents-hive"; fi
module_re="${module_path//./\\.}"  # regex 中点号需转义

{
  head -n 1 "$profile"
  grep -E "^${module_re}/internal/(specdriven/|store/spec_)" "$profile" || true
} > "$tmp_profile"

# 如果过滤后只剩 mode 行（没有任何 specdriven/store 覆盖数据），视为 0%，直接判失败。
if [[ $(wc -l < "$tmp_profile") -le 1 ]]; then
  echo "no specdriven/store coverage data in profile — fail-closed" >&2
  echo "specdriven coverage: 0.00% (threshold ${min_pct}%)"
  exit 1
fi

# Sprint 1.2 DONE 断言：coverage profile 里 `internal/store/spec_store.go` 必须有
# **非零覆盖**——只看文件是否出现在 profile 是纸老虎，`-coverpkg=store` 生效后
# 即使 store 测试一条都没跑，profile 里也会有 0-count 的行。真正的 gate 是：
# 至少一行 `count > 0`（即至少一个 spec_store.go 的语句被真实执行过）。
#
# 行格式：`github.com/.../spec_store.go:L1.C1,L2.C2 <stmts> <count>`
# 最后一字段是 count；awk 累加 count，<=0 就 fail。
store_count=$(awk -F'[ \t]+' '
  /internal\/store\/spec_store\.go/ { sum += $NF }
  END { print (sum ? sum : 0) }
' "$tmp_profile")
if [[ "${store_count:-0}" -le 0 ]]; then
  echo "coverage profile has internal/store/spec_store.go but count=0 — no test actually executed store paths" >&2
  echo "  likely cause: ./internal/store/... missing from test package list, or TEST_DATABASE_URL missing (see SKIP→RED above)" >&2
  exit 1
fi

# `go tool cover -func` 的最后一行形如 `total:  (statements)  82.3%` ——
# 这就是 Go 官方的 line-based（更准确说是"语句覆盖"）总分。
pct=$(go tool cover -func="$tmp_profile" \
  | awk '/^total:/ { gsub("%", "", $NF); print $NF; exit }')

if [[ -z "$pct" ]]; then
  echo "failed to parse total: line from go tool cover output" >&2
  exit 1
fi

printf "specdriven coverage: %s%% (threshold %s%%)\n" "$pct" "$min_pct"

# bash 不支持浮点比较，用 awk。exit 0 表示"低于阈值"→ 脚本向外 exit 1。
if awk -v p="$pct" -v m="$min_pct" 'BEGIN { exit (p+0 < m+0) ? 0 : 1 }'; then
  echo "coverage below threshold — blocking dual-flag rollout" >&2
  exit 1
fi
