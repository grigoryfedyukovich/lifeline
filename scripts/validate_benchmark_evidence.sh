#!/usr/bin/env bash
set -euo pipefail

ROOT=$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
cd "$ROOT"

evidence=${1:-}
[[ -n "$evidence" && -d "$evidence" ]] || {
  echo 'usage: validate_benchmark_evidence.sh EVIDENCE_DIR' >&2
  exit 2
}
evidence=$(cd "$evidence" && pwd)
metadata="$evidence/metadata.json"
raw="$evidence/benchmarks.txt"
summary="$evidence/summary.tsv"
groups_file="$ROOT/release/benchmark-groups.txt"

for required in "$metadata" "$raw" "$summary" "$groups_file"; do
  [[ -f "$required" ]] || { echo "benchmark evidence: missing $required" >&2; exit 1; }
done

./scripts/check_clean_tree.sh >/dev/null
head=$(git rev-parse HEAD)
version=$(tr -d '\r\n' < VERSION)

json_string() {
  local key=$1
  sed -n "s/^[[:space:]]*\"$key\":[[:space:]]*\"\([^\"]*\)\".*/\1/p" "$metadata" | head -n 1
}
json_number() {
  local key=$1
  sed -n "s/^[[:space:]]*\"$key\":[[:space:]]*\([0-9][0-9]*\).*/\1/p" "$metadata" | head -n 1
}

source_commit=$(json_string source_commit)
binary_version=$(json_string binary_version)
validation_status=$(json_string validation_status)
binary_vcs_revision=$(json_string binary_vcs_revision)
binary_vcs_modified=$(json_string binary_vcs_modified)
sample_count=$(json_number sample_count)
group_count=$(json_number group_count)

[[ "$source_commit" == "$head" ]] || { echo "benchmark evidence: stale commit $source_commit, current HEAD is $head" >&2; exit 1; }
[[ "$binary_version" == "$version" ]] || { echo "benchmark evidence: binary version $binary_version, current version is $version" >&2; exit 1; }
[[ "$validation_status" == passed ]] || { echo "benchmark evidence: validation status is '$validation_status', want passed" >&2; exit 1; }
[[ "$binary_vcs_revision" == "$head" ]] || { echo "benchmark evidence: binary revision $binary_vcs_revision, want $head" >&2; exit 1; }
[[ "$binary_vcs_modified" == false ]] || { echo "benchmark evidence: binary reports vcs.modified=$binary_vcs_modified" >&2; exit 1; }
[[ "$sample_count" =~ ^[1-9][0-9]*$ ]] || { echo 'benchmark evidence: invalid sample_count' >&2; exit 1; }

expected_count=$(grep -cve '^[[:space:]]*$' "$groups_file")
[[ "$group_count" == "$expected_count" ]] || { echo "benchmark evidence: group_count=$group_count, want $expected_count" >&2; exit 1; }

counts=$(mktemp)
trap 'rm -f "$counts"' EXIT
awk '
  /^Benchmark/ {
    name=$1
    sub(/-[0-9]+$/, "", name)
    count[name]++
  }
  END { for (name in count) print name "\t" count[name] }
' "$raw" | sort > "$counts"

actual_groups=$(wc -l < "$counts" | tr -d ' ')
[[ "$actual_groups" == "$expected_count" ]] || { echo "benchmark evidence: observed $actual_groups groups, want $expected_count" >&2; exit 1; }

while IFS= read -r group || [[ -n "$group" ]]; do
  [[ -z "$group" ]] && continue
  count=$(awk -F '\t' -v g="$group" '$1 == g { print $2 }' "$counts")
  [[ "$count" == "$sample_count" ]] || {
    echo "benchmark evidence: $group has ${count:-0} samples, want $sample_count" >&2
    exit 1
  }
done < "$groups_file"

while IFS=$'\t' read -r group count; do
  grep -Fx "$group" "$groups_file" >/dev/null || {
    echo "benchmark evidence: unexpected benchmark group $group" >&2
    exit 1
  }
done < "$counts"

summary_rows=$(awk 'NR > 1 && NF {n++} END {print n+0}' "$summary")
[[ "$summary_rows" == "$expected_count" ]] || { echo "benchmark evidence: summary has $summary_rows rows, want $expected_count" >&2; exit 1; }

printf 'benchmark evidence: PASS commit=%s version=%s groups=%s samples/group=%s\n' "$head" "$version" "$expected_count" "$sample_count"
