#!/usr/bin/env bash
set -u

if [ "$#" -lt 1 ]; then
    echo "usage: $0 LOG [LOG ...]" >&2
    exit 1
fi

# Output can be overridden:
#   OUT=summary.csv ./summarize_logs.sh before.txt after.txt
OUT="${OUT:-lifeline-summary.csv}"

python3 - "$OUT" "$@" <<'PY'
import csv
import re
import sys
from pathlib import Path

out = sys.argv[1]
log_files = sys.argv[2:]

# benchmark state from the runner output
#   clean        = no lifecycle diagnostics
#   diagnostic   = one or more [LLxxxx] diagnostics
#   unknown      = unsupported target / excluded from analysis
#   error        = lifeline/config/type-check failure
#   other        = anything unexpected
DIAG_RE = re.compile(r"\[(LL\d+)\]")
UNKNOWN_RE = re.compile(
    r"(target\(s\) could not be analyzed|no compatible fact was found|"
    r"not statically identifiable|not locally available)"
)
ERROR_RE = re.compile(
    r"^lifeline: |unknown key |type checking failed:|"
    r"package .* does not contain package"
)
BENCH_RE = re.compile(r"^(?:===== )?(.+?)(?: =====)?$")
RUN_RE = re.compile(r"^\+ .*?/examples/(.+?)/?$")
OLD_RUN_RE = re.compile(r"^run ./examples/(.+?)/?$")
RESULT_RE = re.compile(r"^run ./examples/(.+?)/?(?: \(config: .*?\))?$")


def classify(lines):
    diagnostics = []
    has_unknown = False
    has_error = False

    for line in lines:
        diagnostics.extend(DIAG_RE.findall(line))
        if UNKNOWN_RE.search(line):
            has_unknown = True
        if ERROR_RE.search(line):
            has_error = True

    if has_error:
        status = "error"
    elif diagnostics:
        status = "diagnostic"
    elif has_unknown:
        status = "unknown"
    elif any("no lifecycle diagnostics" in x for x in lines):
        status = "clean"
    elif any("no recognized lifecycle constructs" in x for x in lines):
        status = "clean"
    else:
        status = "other"

    return status, ";".join(sorted(set(diagnostics))), len(diagnostics)


def parse_log(path):
    lines = Path(path).read_text(errors="replace").splitlines()

    results = {}
    current = None
    bucket = []

    def flush():
        nonlocal current, bucket
        if current is not None:
            status, rules, count = classify(bucket)
            results[current] = {
                "status": status,
                "rules": rules,
                "diagnostics": str(count),
            }
        current = None
        bucket = []

    for line in lines:
        # New runner format: ===== benchmark =====
        m = re.match(r"^===== (.+?) =====$", line)
        if m:
            flush()
            current = m.group(1)
            continue

        # New runner command line
        m = RUN_RE.match(line)
        if m and current is None:
            current = m.group(1)
            continue

        # Old runner format: run ./examples/foo/
        m = OLD_RUN_RE.match(line)
        if m:
            flush()
            current = m.group(1)
            continue

        if current is not None:
            bucket.append(line)

    flush()
    return results


all_results = {}
for log in log_files:
    all_results[log] = parse_log(log)

benchmarks = sorted({
    benchmark
    for results in all_results.values()
    for benchmark in results
})

# One compact row per benchmark, with each log represented by:
#   <log>.status
#   <log>.rules
#   <log>.diagnostics
fieldnames = ["benchmark"]
for log in log_files:
    stem = Path(log).stem
    fieldnames.extend([
        f"{stem}.status",
        f"{stem}.rules",
        f"{stem}.diagnostics",
    ])

with open(out, "w", newline="") as f:
    writer = csv.DictWriter(f, fieldnames=fieldnames)
    writer.writeheader()

    for benchmark in benchmarks:
        row = {"benchmark": benchmark}

        for log in log_files:
            stem = Path(log).stem
            result = all_results[log].get(benchmark, {
                "status": "missing",
                "rules": "",
                "diagnostics": "0",
            })

            row[f"{stem}.status"] = result["status"]
            row[f"{stem}.rules"] = result["rules"]
            row[f"{stem}.diagnostics"] = result["diagnostics"]

        writer.writerow(row)

print(f"Wrote {out}")
PY
