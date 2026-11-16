#!/usr/bin/env bash
set -euo pipefail

ROOT=$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
cd "$ROOT"

usage() {
  cat >&2 <<'USAGE'
usage:
  benchmark_release.sh --output DIR [--samples N] [--benchtime DURATION]
  benchmark_release.sh --self-test
USAGE
  exit 2
}

self_test=0
out=
samples=${LIFELINE_BENCHMARK_SAMPLES:-5}
benchtime=${LIFELINE_BENCHTIME:-1s}
while (($#)); do
  case "$1" in
    --output) (($# >= 2)) || usage; out=$2; shift 2 ;;
    --samples) (($# >= 2)) || usage; samples=$2; shift 2 ;;
    --benchtime) (($# >= 2)) || usage; benchtime=$2; shift 2 ;;
    --self-test) self_test=1; shift ;;
    *) usage ;;
  esac
done

[[ "$samples" =~ ^[1-9][0-9]*$ ]] || { echo 'samples must be a positive integer' >&2; exit 2; }

write_metadata() {
  local dir=$1 status=$2 count=$3 revision=$4 version=$5 vcs_revision=$6 vcs_modified=$7 go_version=$8 goos=$9 goarch=${10} benchtime_value=${11}
  cat > "$dir/metadata.json" <<EOF_META
{
  "schema_version": 1,
  "validation_status": "$status",
  "source_commit": "$revision",
  "binary_version": "$version",
  "binary_vcs_revision": "$vcs_revision",
  "binary_vcs_modified": "$vcs_modified",
  "go_version": "$go_version",
  "goos": "$goos",
  "goarch": "$goarch",
  "workload": "go test ./internal/frontend -bench ^BenchmarkBuild",
  "sample_count": $count,
  "group_count": $(grep -cve '^[[:space:]]*$' release/benchmark-groups.txt),
  "benchtime": "$benchtime_value"
}
EOF_META
}

write_summary() {
  local raw=$1 destination=$2 count=$3
  printf 'group\tsamples\tmedian_ns_op\tp95_ns_op\n' > "$destination"
  while IFS= read -r group || [[ -n "$group" ]]; do
    [[ -z "$group" ]] && continue
    values=$(mktemp)
    awk -v g="$group" '
      /^Benchmark/ {
        name=$1
        sub(/-[0-9]+$/, "", name)
        if (name != g) next
        for (i=2; i<=NF; i++) {
          if ($i == "ns/op" && i > 1) { print $(i-1); break }
        }
      }
    ' "$raw" | sort -n > "$values"
    n=$(wc -l < "$values" | tr -d ' ')
    [[ "$n" == "$count" ]] || { rm -f "$values"; echo "summary: $group has $n timing samples, want $count" >&2; exit 1; }
    median=$(awk -v n="$n" 'NR == int((n+1)/2) {a=$1} NR == int((n+2)/2) {b=$1} END {if (n%2) print a; else printf "%.3f", (a+b)/2}' "$values")
    rank=$(( (95 * n + 99) / 100 ))
    p95=$(awk -v r="$rank" 'NR == r {print $1}' "$values")
    printf '%s\t%s\t%s\t%s\n' "$group" "$n" "$median" "$p95" >> "$destination"
    rm -f "$values"
  done < release/benchmark-groups.txt
}

if ((self_test)); then
  ./scripts/check_clean_tree.sh >/dev/null
  tmp=$(mktemp -d "${TMPDIR:-/tmp}/lifeline-benchmark-selftest.XXXXXX")
  trap 'rm -rf "$tmp"' EXIT
  head=$(git rev-parse HEAD)
  version=$(tr -d '\r\n' < VERSION)
  raw="$tmp/benchmarks.txt"
  : > "$raw"
  for pass in 1 2; do
    while IFS= read -r group || [[ -n "$group" ]]; do
      [[ -z "$group" ]] && continue
      printf '%s-8\t1\t%d ns/op\n' "$group" "$((1000 + pass))" >> "$raw"
    done < release/benchmark-groups.txt
  done
  write_summary "$raw" "$tmp/summary.tsv" 2
  write_metadata "$tmp" passed 2 "$head" "$version" "$head" false synthetic linux amd64 1x
  ./scripts/validate_benchmark_evidence.sh "$tmp" >/dev/null

  cp "$tmp/metadata.json" "$tmp/metadata.good.json"
  perl -0pi -e 's/"source_commit": "[^"]+"/"source_commit": "deadbeef"/' "$tmp/metadata.json"
  if ./scripts/validate_benchmark_evidence.sh "$tmp" >/dev/null 2>&1; then echo 'self-test failed to reject stale commit' >&2; exit 1; fi
  mv "$tmp/metadata.good.json" "$tmp/metadata.json"

  cp "$tmp/metadata.json" "$tmp/metadata.good.json"
  perl -0pi -e 's/"binary_version": "[^"]+"/"binary_version": "9.9.9"/' "$tmp/metadata.json"
  if ./scripts/validate_benchmark_evidence.sh "$tmp" >/dev/null 2>&1; then echo 'self-test failed to reject wrong version' >&2; exit 1; fi
  mv "$tmp/metadata.good.json" "$tmp/metadata.json"

  cp "$tmp/benchmarks.txt" "$tmp/benchmarks.good.txt"
  sed -i '1d' "$tmp/benchmarks.txt"
  if ./scripts/validate_benchmark_evidence.sh "$tmp" >/dev/null 2>&1; then echo 'self-test failed to reject partial evidence' >&2; exit 1; fi
  mv "$tmp/benchmarks.good.txt" "$tmp/benchmarks.txt"

  printf 'benchmark validator self-test: PASS\n'
  exit 0
fi

[[ -n "$out" ]] || usage
./scripts/check_clean_tree.sh
head=$(git rev-parse HEAD)
version=$(tr -d '\r\n' < VERSION)

[[ ! -e "$out" ]] || { echo "output already exists: $out" >&2; exit 1; }
mkdir -p "$out/bin"
out=$(cd "$out" && pwd)

# Release evidence is valid only when the same commit passes the complete validation suite.
make ci
./scripts/check_clean_tree.sh >/dev/null

go build -mod=readonly -trimpath -o "$out/bin/lifeline" ./cmd/lifeline
binary_version=$($out/bin/lifeline -version | awk '{print $2}')
[[ "$binary_version" == "$version" ]] || { echo "binary version $binary_version != VERSION $version" >&2; exit 1; }

build_info=$(go version -m "$out/bin/lifeline")
printf '%s\n' "$build_info" > "$out/binary-build-info.txt"
vcs_revision=$(printf '%s\n' "$build_info" | sed -n 's/^[[:space:]]*build[[:space:]]*vcs.revision=//p' | head -n 1)
vcs_modified=$(printf '%s\n' "$build_info" | sed -n 's/^[[:space:]]*build[[:space:]]*vcs.modified=//p' | head -n 1)
[[ "$vcs_revision" == "$head" ]] || { echo "binary VCS revision $vcs_revision != HEAD $head" >&2; exit 1; }
[[ "$vcs_modified" == false ]] || { echo "binary reports vcs.modified=$vcs_modified" >&2; exit 1; }

raw="$out/benchmarks.txt"
go test -mod=readonly -run '^$' -bench '^BenchmarkBuild' -benchmem -benchtime="$benchtime" -count="$samples" ./internal/frontend | tee "$raw"
write_summary "$raw" "$out/summary.tsv" "$samples"
write_metadata "$out" passed "$samples" "$head" "$binary_version" "$vcs_revision" "$vcs_modified" "$(go env GOVERSION)" "$(go env GOOS)" "$(go env GOARCH)" "$benchtime"
./scripts/validate_benchmark_evidence.sh "$out"

printf 'benchmark artifacts: %s\n' "$out"
printf 'benchmark summary (slowest median ns/op first):\n'
sorted=$(mktemp)
trap 'rm -f "$sorted"' EXIT
{ head -n 1 "$out/summary.tsv"; tail -n +2 "$out/summary.tsv" | sort -t $'\t' -k3,3nr; } > "$sorted"
sed -n '1,7p' "$sorted"
