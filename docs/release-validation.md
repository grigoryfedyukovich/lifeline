# Release validation

**Release version:** 0.2.0

This document defines the release-facing validation contract for Lifeline. The
release identity is the exact contents of the root `VERSION` file, without a
leading `v`. Current source, documentation, changelog, CLI output, and
version-bearing golden output must agree with it.

Run the version-consistency check against a built CLI with:

```bash
./scripts/check_version_consistency.sh ./bin/lifeline
```

The check is intentionally strict. A release candidate is invalid if any
current-version reference diverges, even if the executable itself still builds.

## Clean-clone release gate

Run:

```bash
./scripts/validate_release.sh
```

The gate requires the invoking checkout to be clean, records committed `HEAD`,
clones that exact commit into a temporary directory, runs the complete local CI
suite there, executes the documented quick start, creates a source package with
`git archive`, validates every package path against
`release/package-allowlist.txt`, and confirms the clone is still clean.

Because packaging is based on committed `HEAD`, unrelated untracked files and
machine-local evidence cannot enter the source archive. There is no dirty-tree
escape hatch analogous to `--allow-dirty`.

## Benchmark provenance gate

Release benchmarking is separate from ordinary CI:

```bash
./scripts/benchmark_release.sh --output /tmp/lifeline-benchmark-v0.2.0
```

The wrapper requires a clean checkout, runs the complete validation suite,
builds the release binary with `-mod=readonly -trimpath`, verifies the binary's
embedded VCS revision and `-version` output against committed `HEAD` and
`VERSION`, executes every group in `release/benchmark-groups.txt`, and requires
the configured number of samples for every group. It writes raw benchmark
output, binary build metadata, release metadata, and median/nearest-rank-p95
summary rows into the selected output directory.

Existing evidence can be checked independently with:

```bash
./scripts/validate_benchmark_evidence.sh /path/to/evidence
```

That command rejects stale commits, wrong versions, dirty-source binaries,
failed validation status, unknown benchmark groups, and partial sample sets.
`./scripts/benchmark_release.sh --self-test` exercises those rejection paths
without running the benchmark matrix.

## Shared local and hosted orchestration

`./scripts/validate_ci.sh` is the canonical ordinary validation entry point.
`make ci` delegates to it, and `.github/workflows/ci.yml` invokes the same script
on both Ubuntu and macOS. The workflow intentionally does not duplicate the Go
command list.

`./scripts/check_validation_contract.sh` checks those delegation points plus the
documented quick-start/release commands. Ordinary CI runs only a one-iteration
benchmark smoke plus `benchmark_release.sh --self-test`; the complete benchmark
matrix remains a release-preparation action.

## Hardening checks

The shared gate also validates local Markdown links and the structured contract
for release performance claims. A release-facing performance claim is accepted
only when it identifies the exact Git commit, platform, workload, repetition
count, median, and p95. Historical measurements that lack those fields remain
labeled as historical observations rather than being promoted to release claims.

Dependency vulnerability scanning uses an existing `govulncheck` executable
when one is already installed. Otherwise the shared gate runs the pinned
`golang.org/x/vuln` v1.9.0 scanner without adding it to the module dependency
set. The clean-clone release gate continues to inspect the source package
against `release/package-allowlist.txt`.
