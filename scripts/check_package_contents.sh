#!/usr/bin/env bash
set -euo pipefail

ROOT=$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
archive=${1:-}
[[ -n "$archive" && -f "$archive" ]] || {
  echo 'usage: check_package_contents.sh RELEASE.tar.gz' >&2
  exit 2
}

version=$(tr -d '\r\n' < "$ROOT/VERSION")
prefix="lifeline-v$version/"
allowlist="$ROOT/release/package-allowlist.txt"
[[ -f "$allowlist" ]] || { echo 'package allowlist is missing' >&2; exit 1; }

entries=$(mktemp)
trap 'rm -f "$entries"' EXIT
tar -tzf "$archive" > "$entries"
entry_count=$(wc -l < "$entries" | tr -d ' ')
((entry_count > 0)) || { echo 'package archive is empty' >&2; exit 1; }

seen_version=0
seen_mod=0
seen_sum=0
seen_license=0
seen_source=0
while IFS= read -r entry || [[ -n "$entry" ]]; do
  [[ "$entry" == "$prefix"* ]] || { echo "package entry outside $prefix: $entry" >&2; exit 1; }
  rel=${entry#"$prefix"}
  [[ -n "$rel" ]] || continue
  [[ "$rel" != /* && "$rel" != *'../'* && "$rel" != '..' ]] || { echo "unsafe package path: $rel" >&2; exit 1; }
  [[ "$rel" == */ ]] && continue

  allowed=0
  while IFS= read -r rule || [[ -n "$rule" ]]; do
    [[ -z "$rule" || "$rule" == \#* ]] && continue
    if [[ "$rule" == */ ]]; then
      [[ "$rel" == "$rule"* ]] && allowed=1 && break
    elif [[ "$rel" == "$rule" ]]; then
      allowed=1
      break
    fi
  done < "$allowlist"
  ((allowed == 1)) || { echo "package entry is not allowlisted: $rel" >&2; exit 1; }

  case "$rel" in
    VERSION) seen_version=1 ;;
    go.mod) seen_mod=1 ;;
    go.sum) seen_sum=1 ;;
    LICENSE) seen_license=1 ;;
    internal/version/version.go) seen_source=1 ;;
  esac
done < "$entries"

((seen_version && seen_mod && seen_sum && seen_license && seen_source)) || {
  echo 'package archive is missing one or more required release files' >&2
  exit 1
}

printf 'package contents: PASS (%s entries)\n' "$entry_count"
