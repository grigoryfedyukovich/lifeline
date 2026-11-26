#!/usr/bin/env bash
set -euo pipefail

ROOT=$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
cd "$ROOT"

version=${1:-$(tr -d '\r\n' < VERSION)}
awk -v v="$version" '
  $0 ~ "^## " v " —" {inside=1; next}
  inside && /^## / {exit}
  inside {print}
' CHANGELOG.md
