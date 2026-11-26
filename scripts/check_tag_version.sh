#!/usr/bin/env bash
set -euo pipefail

ROOT=$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
cd "$ROOT"

tag=${1:-${GITHUB_REF_NAME:-}}
[[ -n "$tag" ]] || { echo 'usage: check_tag_version.sh vX.Y.Z' >&2; exit 2; }
version=$(tr -d '\r\n' < VERSION)
expected="v$version"
[[ "$tag" == "$expected" ]] || {
  printf 'tag/version check: tag=%s, expected=%s from VERSION\n' "$tag" "$expected" >&2
  exit 1
}
grep -F "## $version —" CHANGELOG.md >/dev/null || {
  printf 'tag/version check: CHANGELOG has no %s release heading\n' "$version" >&2
  exit 1
}

if git rev-parse --verify -q "refs/tags/$tag^{commit}" >/dev/null; then
  tag_commit=$(git rev-parse "refs/tags/$tag^{commit}")
  head=$(git rev-parse HEAD)
  [[ "$tag_commit" == "$head" ]] || {
    printf 'tag/version check: %s points to %s, checkout HEAD is %s\n' "$tag" "$tag_commit" "$head" >&2
    exit 1
  }
fi

printf 'tag/version check: PASS (%s)\n' "$tag"
