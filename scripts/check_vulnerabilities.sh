#!/usr/bin/env bash
set -euo pipefail

ROOT=$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
cd "$ROOT"

# Prefer an already-installed scanner. Hosted CI falls back to the pinned module
# version without adding it to go.mod or installing a persistent binary.
if command -v govulncheck >/dev/null 2>&1; then
  scanner=$(command -v govulncheck)
  printf 'vulnerability scan: using existing %s\n' "$scanner"
  "$scanner" -version || true
  exec "$scanner" -test ./...
fi

printf 'vulnerability scan: using pinned golang.org/x/vuln v1.9.0\n'
exec go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 -test ./...
