#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

echo "Checking skill-creator resources at: ${ROOT_DIR}"

test -f "${ROOT_DIR}/SKILL.md"
test -f "${ROOT_DIR}/templates/skill-template.md"
test -f "${ROOT_DIR}/scripts/validate.sh"

grep -q "./skills/<name>/" "${ROOT_DIR}/SKILL.md"
grep -q -- "--global" "${ROOT_DIR}/SKILL.md"
grep -q "显式" "${ROOT_DIR}/SKILL.md"

echo "OK: skill-creator resource check passed"
