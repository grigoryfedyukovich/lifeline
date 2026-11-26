#!/usr/bin/env bash
set -euo pipefail

ROOT=$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
cd "$ROOT"

out=${1:-}
[[ -n "$out" ]] || { echo 'usage: package_source.sh OUTPUT.tar.gz' >&2; exit 2; }
out=$(cd "$(dirname -- "$out")" && pwd)/$(basename -- "$out")

./scripts/check_clean_tree.sh >/dev/null
version=$(tr -d '\r\n' < VERSION)
head=$(git rev-parse HEAD)

git archive --format=tar.gz --prefix="lifeline-v$version/" -o "$out" "$head"
./scripts/check_package_contents.sh "$out"
printf 'source package: %s\n' "$out"
