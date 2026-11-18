#!/usr/bin/env bash
set -euo pipefail

ROOT=$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
cd "$ROOT"

files=$(mktemp)
trap 'rm -f "$files"' EXIT
find . -type f -name '*.md' -not -path './.git/*' -print | LC_ALL=C sort > "$files"

awk '
function reset_claim() {
  commit=""; platform=""; workload=""; repetitions=""; median=""; p95=""
}
function fail(msg) {
  print "performance claim check: " FILENAME ":" FNR ": " msg > "/dev/stderr"
  bad=1
}
/^<!-- lifeline-performance-claim$/ {
  if (inside) fail("nested performance claim block")
  inside=1
  reset_claim()
  next
}
inside && /^commit:[[:space:]]*/ { commit=$0; sub(/^commit:[[:space:]]*/, "", commit); next }
inside && /^platform:[[:space:]]*/ { platform=$0; sub(/^platform:[[:space:]]*/, "", platform); next }
inside && /^workload:[[:space:]]*/ { workload=$0; sub(/^workload:[[:space:]]*/, "", workload); next }
inside && /^repetitions:[[:space:]]*/ { repetitions=$0; sub(/^repetitions:[[:space:]]*/, "", repetitions); next }
inside && /^median:[[:space:]]*/ { median=$0; sub(/^median:[[:space:]]*/, "", median); next }
inside && /^p95:[[:space:]]*/ { p95=$0; sub(/^p95:[[:space:]]*/, "", p95); next }
inside && /^-->$/ {
  if (length(commit) != 40 || commit !~ /^[0-9a-f]+$/) fail("commit must be a full 40-hex Git commit")
  if (platform == "") fail("platform is required")
  if (workload == "") fail("workload is required")
  if (repetitions !~ /^[1-9][0-9]*$/) fail("repetitions must be a positive integer")
  if (median == "") fail("median is required")
  if (p95 == "") fail("p95 is required")
  claims++
  inside=0
  next
}
END {
  if (inside) fail("unterminated performance claim block")
  if (bad) exit 1
  printf "performance claim check: PASS (%d structured claims)\n", claims+0
}
' $(cat "$files")
