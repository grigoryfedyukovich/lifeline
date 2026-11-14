#!/usr/bin/env bash
set -euo pipefail

ROOT=$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
cd "$ROOT"

fail() {
  printf 'version consistency: %s\n' "$*" >&2
  exit 1
}

[[ -f VERSION ]] || fail 'VERSION is missing'
version=$(tr -d '\r\n' < VERSION)
[[ "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || fail "invalid VERSION: $version"

source_version=$(sed -n 's/^[[:space:]]*Version[[:space:]]*=[[:space:]]*"\([^"]*\)".*/\1/p' internal/version/version.go | head -n 1)
[[ "$source_version" == "$version" ]] || fail "internal/version/version.go=$source_version, want $version"

spec_version=$(sed -n 's/^\*\*Specification version:\*\*[[:space:]]*\([^[:space:]]*\).*/\1/p' docs/specification.md | head -n 1)
[[ "$spec_version" == "$version" ]] || fail "docs/specification.md=$spec_version, want $version"

validation_version=$(sed -n 's/^\*\*Release version:\*\*[[:space:]]*\([^[:space:]]*\).*/\1/p' docs/release-validation.md | head -n 1)
[[ "$validation_version" == "$version" ]] || fail "docs/release-validation.md=$validation_version, want $version"

changelog_version=$(sed -n 's/^## \([0-9][0-9]*\.[0-9][0-9]*\.[0-9][0-9]*\).*/\1/p' CHANGELOG.md | head -n 1)
[[ "$changelog_version" == "$version" ]] || fail "current CHANGELOG release=$changelog_version, want $version"

backend=$(sed -n 's/^[[:space:]]*Backend[[:space:]]*=[[:space:]]*"\([^"]*\)".*/\1/p' internal/version/version.go | head -n 1)
[[ -n "$backend" ]] || fail 'could not read backend identity'
expected_cli="lifeline $version ($backend)"

golden=$(tr -d '\r\n' < tests/golden/version.txt)
[[ "$golden" == "$expected_cli" ]] || fail "tests/golden/version.txt='$golden', want '$expected_cli'"

grep -Fqx "$expected_cli" docs/tutorial.md || fail 'docs/tutorial.md version example diverges'
grep -Fq "Lifeline $version is intentionally conservative and bounded." docs/limitations.md || fail 'docs/limitations.md current version diverges'
grep -Fq "Lifeline $version has no application-managed result cache." docs/architecture.md || fail 'docs/architecture.md current version diverges'
grep -Fq "no Lifeline-managed persistent result cache in v$version" docs/specification.md || fail 'docs/specification.md current cache statement diverges'

binary=${1:-./bin/lifeline}
[[ -x "$binary" ]] || fail "CLI binary is missing or not executable: $binary"
cli_version=$($binary -version | tr -d '\r\n')
[[ "$cli_version" == "$expected_cli" ]] || fail "CLI -version='$cli_version', want '$expected_cli'"

printf 'version consistency: PASS (%s)\n' "$version"
