#!/usr/bin/env bash
# wechatbot_credential_audit.sh — 官方 wechatbot 凭证权限巡检
#
# 默认巡检路径与当前代码默认值保持一致：
#   sessions_dir = ~/.claw/sessions
#   cred_root    = ~/.claw/wechatbot
#
# 退出码：0=通过，1=发现风险，2=参数/环境错误。
set -euo pipefail

cred_root="${WECHATBOT_CRED_ROOT:-$HOME/.claw/wechatbot}"
require_exists=0

usage() {
  cat <<'USAGE'
Usage: scripts/wechatbot_credential_audit.sh [--cred-root DIR] [--require-exists]

Checks:
  - cred_root and users/* are not symlinks
  - users/* directories are 0700
  - credentials.json and state.json are regular files, not symlinks, mode 0600
  - credential file contents are never printed
USAGE
}

while [ "$#" -gt 0 ]; do
  case "$1" in
    --cred-root)
      if [ "$#" -lt 2 ] || [ -z "$2" ]; then
        echo "ERR: --cred-root requires a directory" >&2
        exit 2
      fi
      cred_root="$2"
      shift 2
      ;;
    --require-exists)
      require_exists=1
      shift
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      echo "ERR: unknown argument: $1" >&2
      usage >&2
      exit 2
      ;;
  esac
done

mode_of() {
  local path="$1"
  if stat -f "%Lp" "$path" >/dev/null 2>&1; then
    stat -f "%Lp" "$path"
    return
  fi
  stat -c "%a" "$path"
}

failures=0

fail() {
  echo "FAIL: $*"
  failures=$((failures + 1))
}

pass() {
  echo "PASS: $*"
}

warn() {
  echo "WARN: $*"
}

echo "== wechatbot credential audit =="
echo "cred_root: $cred_root"

if [ ! -e "$cred_root" ]; then
  if [ "$require_exists" -eq 1 ]; then
    fail "cred_root does not exist"
  else
    warn "cred_root does not exist; this is expected before any user logs in"
  fi
  if [ "$failures" -eq 0 ]; then
    echo "ALL_PASS"
    exit 0
  fi
  echo "FAILED ($failures issue(s))" >&2
  exit 1
fi

if [ -L "$cred_root" ]; then
  fail "cred_root must not be a symlink"
elif [ ! -d "$cred_root" ]; then
  fail "cred_root is not a directory"
else
  pass "cred_root is a directory and not a symlink"
fi

users_dir="$cred_root/users"
if [ ! -e "$users_dir" ]; then
  warn "users directory does not exist; no user credentials to inspect"
elif [ -L "$users_dir" ]; then
  fail "users directory must not be a symlink"
elif [ ! -d "$users_dir" ]; then
  fail "users path is not a directory"
else
  pass "users directory is present"
  found_owner=0
  for owner_dir in "$users_dir"/*; do
    [ -e "$owner_dir" ] || continue
    found_owner=1
    owner_name="$(basename "$owner_dir")"
    if [ -L "$owner_dir" ]; then
      fail "owner directory [$owner_name] must not be a symlink"
      continue
    fi
    if [ ! -d "$owner_dir" ]; then
      fail "owner path [$owner_name] is not a directory"
      continue
    fi
    owner_mode="$(mode_of "$owner_dir")"
    if [ "$owner_mode" != "700" ]; then
      fail "owner directory [$owner_name] mode is $owner_mode, want 700"
    else
      pass "owner directory [$owner_name] mode 700"
    fi

    for file_name in credentials.json state.json; do
      file_path="$owner_dir/$file_name"
      if [ ! -e "$file_path" ]; then
        warn "owner [$owner_name] missing $file_name"
        continue
      fi
      if [ -L "$file_path" ]; then
        fail "owner [$owner_name] $file_name must not be a symlink"
        continue
      fi
      if [ ! -f "$file_path" ]; then
        fail "owner [$owner_name] $file_name is not a regular file"
        continue
      fi
      file_mode="$(mode_of "$file_path")"
      if [ "$file_mode" != "600" ]; then
        fail "owner [$owner_name] $file_name mode is $file_mode, want 600"
      else
        pass "owner [$owner_name] $file_name mode 600"
      fi
    done
  done
  if [ "$found_owner" -eq 0 ]; then
    warn "no owner credential directories found"
  fi
fi

echo
if [ "$failures" -eq 0 ]; then
  echo "ALL_PASS"
  exit 0
fi

echo "FAILED ($failures issue(s))" >&2
exit 1
