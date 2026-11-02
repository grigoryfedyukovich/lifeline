#!/usr/bin/env python3
"""Run the example corpus with raw JSON, provenance, and warm-cache timings."""

import argparse
import collections
import csv
import datetime
import hashlib
import json
import math
import os
from pathlib import Path
import platform
import statistics
import subprocess
import time


ROOT = Path(__file__).resolve().parents[1]


def command_output(*args):
    return subprocess.check_output(args, cwd=ROOT, text=True).strip()


def digest(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def summarize(report):
    status = report["status"]
    unsupported = status["unsupported"]
    diagnostics = report["diagnostics"]
    return {
        "rules": sorted(d["rule_id"] for d in diagnostics),
        "functions": sorted(d.get("function", "").rsplit(".", 1)[-1] for d in diagnostics),
        "locations": sorted((d["rule_id"], d["position"]["file"], d["position"]["start_line"]) for d in diagnostics),
        "incomplete": status["incomplete"],
        "unsupported_targets": unsupported["targets"],
        "handed_off": unsupported["handed_off_obligations"],
        "unestablished_path_checks": unsupported["unestablished_path_checks"],
        "units": status["units"],
    }


def validate(result, expected):
    errors = []
    if result["incomplete"]:
        errors.append("incomplete analysis")
    if expected is not None:
        for key in ("rules", "functions", "handed_off"):
            if key in expected:
                want = sorted(expected[key]) if isinstance(expected[key], list) else expected[key]
                if result[key] != want:
                    errors.append(f"{key}: got {result[key]!r}, expected {want!r}")
    return errors


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", type=Path, default=ROOT / "bin/lifeline")
    parser.add_argument("--binary-revision", help="source revision used to build the binary, especially for archived baselines")
    parser.add_argument("--output", type=Path, required=True, help="new local output directory (keep out of Git)")
    parser.add_argument("--repetitions", type=int, default=3)
    parser.add_argument("--warmups", type=int, default=1)
    parser.add_argument("--features-only", action="store_true")
    parser.add_argument("--net-http", action="store_true", help="also repeat the historical standard-library smoke experiment")
    args = parser.parse_args()
    if args.repetitions < 1 or args.warmups < 0:
        parser.error("repetitions must be positive and warmups nonnegative")
    binary = args.binary.resolve()
    if not binary.is_file():
        parser.error(f"build the analyzer first: binary missing at {binary}")
    out = args.output.resolve()
    out.mkdir(parents=True, exist_ok=False)
    # Protect generated evidence even when the selected directory is in a checkout.
    (out / ".gitignore").write_text("*\n")
    manifest = json.loads((ROOT / "examples/corpus.json").read_text())
    if manifest["schema_version"] != 1 or not manifest["cases"]:
        parser.error("unsupported or empty corpus manifest")
    expected = {c["name"]: c for c in manifest["cases"]}
    if len(expected) != len(manifest["cases"]):
        parser.error("duplicate corpus case names")
    cases = sorted(expected) if args.features_only else sorted(
        p.name for p in (ROOT / "examples").iterdir() if p.is_dir() and any(p.glob("*.go"))
    )
    if set(expected) - set(cases):
        parser.error(f"missing example packages: {sorted(set(expected) - set(cases))}")
    if args.net_http:
        cases.append("net-http")
    paths = command_output("git", "ls-files", "--cached", "--others", "--exclude-standard").splitlines()
    hashes = {p: digest(ROOT / p) for p in paths if (ROOT / p).is_file() and
              (p.endswith((".go", ".yaml", ".toml", ".py")) or p in ("go.mod", "go.sum", "examples/corpus.json"))}
    metadata = {
        "started_at": datetime.datetime.now(datetime.timezone.utc).isoformat(),
        "source_commit": command_output("git", "rev-parse", "HEAD"),
        "working_tree": command_output("git", "status", "--short"),
        "source_sha256": hashes,
        "platform": platform.platform(), "machine": platform.machine(), "cpu_count": os.cpu_count(),
        "go_version": command_output("go", "version"),
        "go_env": json.loads(command_output("go", "env", "-json", "GOOS", "GOARCH", "GOVERSION", "GOCACHE", "GOMODCACHE", "CGO_ENABLED")),
        "binary": str(binary), "binary_sha256": digest(binary),
        "binary_revision": args.binary_revision,
        "binary_build_info": command_output("go", "version", "-m", str(binary)),
        "repetitions": args.repetitions, "warmups": args.warmups,
        "cache_policy": "existing Go caches; explicit per-case warmups; no cache deletion",
        "timing": "serial end-to-end process wall time including go list, type checking, analysis, JSON rendering; p95 nearest rank",
    }
    (out / "metadata.json").write_text(json.dumps(metadata, indent=2) + "\n")
    results = []
    for name in cases:
        folder = out / name
        folder.mkdir()
        argv = [str(binary)]
        config = ROOT / "examples" / name / "lifeline.yaml"
        if config.exists():
            argv += ["-config", str(config)]
        argv += ["-format", "json", "-timeout", "30s", "net/http" if name == "net-http" else "./examples/" + name]
        samples, errors, observations = [], [], []
        for i in range(args.warmups + args.repetitions):
            phase = "warmup" if i < args.warmups else "sample"
            label = f"{phase}-{i if phase == 'warmup' else i - args.warmups + 1}"
            start = time.perf_counter()
            try:
                process = subprocess.run(argv, cwd=ROOT, capture_output=True, text=True, timeout=90)
                elapsed = time.perf_counter() - start
                (folder / f"{label}.json").write_text(process.stdout)
                (folder / f"{label}.stderr").write_text(process.stderr)
                raw = {"argv": argv, "exit_code": process.returncode, "elapsed_seconds": elapsed}
                (folder / f"{label}.run.json").write_text(json.dumps(raw, indent=2) + "\n")
                if process.returncode:
                    raise ValueError(f"exit code {process.returncode}: {process.stderr[:300]}")
                observation = summarize(json.loads(process.stdout))
                errors.extend(f"{label}: {e}" for e in validate(observation, expected.get(name)))
                observations.append(observation)
                if phase == "sample":
                    samples.append(elapsed)
            except (subprocess.TimeoutExpired, ValueError, KeyError, TypeError) as exc:
                errors.append(f"{label}: {exc}")
        if observations and any(o != observations[0] for o in observations[1:]):
            errors.append("nondeterministic semantic results across repetitions")
        result = {"name": name, "expected": expected.get(name), "errors": errors, "samples_seconds": samples}
        if observations:
            result.update(observations[-1])
        if samples:
            result.update(median_seconds=statistics.median(samples), p95_seconds=sorted(samples)[math.ceil(.95 * len(samples)) - 1])
        results.append(result)
        print(f"{name}: {'FAIL' if errors else 'PASS' if name in expected else 'observed'} {result.get('rules', [])}", flush=True)
        # Keep partial results available if a longer experiment is interrupted.
        (out / "results.json").write_text(json.dumps(results, indent=2) + "\n")
    columns = ["name", "validation", "rules", "incomplete", "unsupported_targets", "handed_off", "unestablished_path_checks", "median_seconds", "p95_seconds", "errors"]
    with (out / "summary.csv").open("w", newline="") as stream:
        writer = csv.DictWriter(stream, fieldnames=columns)
        writer.writeheader()
        for result in results:
            row = {key: result.get(key, "") for key in columns}
            row["validation"] = "FAIL" if result["errors"] else "PASS" if result["expected"] else "observed"
            row["rules"] = ";".join(result.get("rules", []))
            row["errors"] = "; ".join(result["errors"])
            writer.writerow(row)
    counts = collections.Counter("failed" if r["errors"] else "validated" if r["expected"] else "observed" for r in results)
    metadata.update(finished_at=datetime.datetime.now(datetime.timezone.utc).isoformat(), results=dict(counts))
    (out / "metadata.json").write_text(json.dumps(metadata, indent=2) + "\n")
    print(f"Results: {dict(counts)}; saved to {out}")
    return 1 if counts["failed"] else 0


if __name__ == "__main__":
    raise SystemExit(main())
