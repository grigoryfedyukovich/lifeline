#!/usr/bin/env bash
set -euo pipefail

ROOT=$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
cd "$ROOT"

require_literal() {
  local file=$1 literal=$2
  grep -F "$literal" "$file" >/dev/null || {
    printf 'validation contract: %s is missing %s\n' "$file" "$literal" >&2
    exit 1
  }
}

require_make_recipe() {
  local target=$1 recipe=$2
  awk -v target="$target:" -v recipe="\t$recipe" '
    $0 == target { getline; if ($0 == recipe) found=1 }
    END { exit(found ? 0 : 1) }
  ' Makefile || {
    printf 'validation contract: Makefile target %s does not delegate to %s\n' "$target" "$recipe" >&2
    exit 1
  }
}

require_make_recipe ci './scripts/validate_ci.sh'
require_make_recipe release './scripts/validate_release.sh'
require_literal .github/workflows/ci.yml './scripts/validate_ci.sh'
require_literal README.md './scripts/quick_start.sh'
require_literal README.md 'make ci'
require_literal README.md 'make release'
require_literal docs/release-validation.md './scripts/validate_release.sh'
require_literal docs/release-validation.md './scripts/benchmark_release.sh --self-test'

# Hosted CI must delegate validation rather than duplicate the command sequence.
if grep -Eq 'run:[[:space:]]+(go test|go vet|go build|go mod tidy|make (check|race|perfcheck|modcheck|build))' .github/workflows/ci.yml; then
  echo 'validation contract: hosted CI duplicates validation commands instead of calling the shared script' >&2
  exit 1
fi

printf 'validation contract: PASS\n'
