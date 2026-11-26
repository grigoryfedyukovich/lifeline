#!/usr/bin/env bash
set -euo pipefail

ROOT=$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
cd "$ROOT"

files=$(mktemp)
links=$(mktemp)
trap 'rm -f "$files" "$links"' EXIT
find . -type f -name '*.md' -not -path './.git/*' -print | LC_ALL=C sort > "$files"

checked=0
while IFS= read -r file || [[ -n "$file" ]]; do
  : > "$links"
  grep -oE '\[[^][]+\]\([^)]+\)' "$file" > "$links" || true
  while IFS= read -r link || [[ -n "$link" ]]; do
    [[ -z "$link" ]] && continue
    target=${link#*](}
    target=${target%)}
    case "$target" in
      http://*|https://*|mailto:*|\#*) continue ;;
    esac
    target=${target%%#*}
    [[ -n "$target" ]] || continue
    base=$(dirname -- "$file")
    path="$base/$target"
    if [[ ! -e "$path" ]]; then
      printf 'markdown link check: %s -> %s does not exist\n' "$file" "$target" >&2
      exit 1
    fi
    checked=$((checked + 1))
  done < "$links"
done < "$files"

printf 'markdown local-link check: PASS (%d links)\n' "$checked"
