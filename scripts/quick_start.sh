#!/usr/bin/env bash
set -euo pipefail

ROOT=$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
cd "$ROOT"

mkdir -p bin
go build -mod=readonly -trimpath -o ./bin/lifeline ./cmd/lifeline
./scripts/check_version_consistency.sh ./bin/lifeline
./bin/lifeline ./examples/ignored_context >/dev/null
./bin/lifeline ./examples/proper_errgroup >/dev/null
printf 'quick start: PASS\n'
