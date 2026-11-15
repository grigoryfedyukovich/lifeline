#!/usr/bin/env bash
set -euo pipefail

ROOT=$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
cd "$ROOT"

git rev-parse --is-inside-work-tree >/dev/null 2>&1 || {
  echo 'clean-tree check: not inside a Git work tree' >&2
  exit 1
}

status=$(git status --porcelain=v1 --untracked-files=all)
if [[ -n "$status" ]]; then
  echo 'clean-tree check: FAIL; tracked/untracked changes are present:' >&2
  printf '%s\n' "$status" >&2
  exit 1
fi

printf 'clean-tree check: PASS (%s)\n' "$(git rev-parse --short=12 HEAD)"
