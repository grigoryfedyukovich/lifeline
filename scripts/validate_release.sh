#!/usr/bin/env bash
set -euo pipefail

ROOT=$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
cd "$ROOT"

./scripts/check_clean_tree.sh
head=$(git rev-parse HEAD)
version=$(tr -d '\r\n' < VERSION)
tmp=$(mktemp -d "${TMPDIR:-/tmp}/lifeline-release.XXXXXX")
trap 'rm -rf "$tmp"' EXIT
clone="$tmp/repo"

git clone --quiet --no-hardlinks "$ROOT" "$clone"
git -C "$clone" checkout --quiet --detach "$head"
[[ $(git -C "$clone" rev-parse HEAD) == "$head" ]] || {
  echo 'release validation: cloned commit does not match HEAD' >&2
  exit 1
}

(
  cd "$clone"
  ./scripts/validate_ci.sh
  ./scripts/quick_start.sh
  ./scripts/check_clean_tree.sh
  ./scripts/package_source.sh "$tmp/lifeline-v$version.tar.gz"
  ./scripts/check_clean_tree.sh
)

printf 'release validation: PASS version=%s commit=%s\n' "$version" "$head"
printf 'inspected source package: %s\n' "$tmp/lifeline-v$version.tar.gz"
