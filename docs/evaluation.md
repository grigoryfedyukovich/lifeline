# Evaluation record

## Environment

- Tool: Lifeline 0.1.1
- Backend: `local-ast-types-ssa-summary/v2`
- Go toolchain: Go 1.23.2, linux/amd64
- Date: 2026-07-17

## Small example

Command:

```bash
/usr/bin/time -f 'elapsed=%e maxrss=%MKB' \
  ./bin/lifeline ./examples/ignored_context
```

Warm Go cache result:

```text
elapsed: 0.34 s
maximum resident set: 38,000 KB
expected diagnostic: LL1002
```

## Go standard library: `net/http`

Command:

```bash
/usr/bin/time -f 'elapsed=%e maxrss=%MKB' \
  ./bin/lifeline -timeout 30s -format json net/http
```

Warm Go cache result:

```text
diagnostics: 0
incomplete: false  (status.incomplete; says nothing about status.unsupported)
elapsed: 0.86 s
maximum resident set: 71,068 KB
```

A cold export/build cache run in the same environment took 9.38 seconds and 202,436 KB. The difference is dominated by Go package/export preparation, which is why the performance target and comparison record explicitly distinguish warm-cache behavior.

This is a smoke/precision evaluation, not evidence of complete lifecycle correctness in `net/http`.

During initial development, provisional findings exposed missing ownership-transfer and break recognition. The frontend was corrected to model those patterns; no corpus-specific suppressions or names were added. The July 2026 external review then produced minimized regressions for nested goroutine boundaries, positional assignments, context type identity, function limits, and facts.

## Included examples and regressions

| Area | Expected result |
|---|---|
| ignored context | `LL1002` |
| lost/uncalled cancellation | `LL1001` |
| unjoined WaitGroup | `LL1003` |
| unjoined errgroup | `LL1004` |
| proper context/cancel/channel/group protocols | no warning |
| configured wrappers | configured behavior only when config is supplied |
| nested goroutine loop | warning attributed only to inner start |
| nested stop path | cannot suppress outer loop warning |
| direct cross-package target in vet mode | imported versioned fact supports `LL1002` |

Golden tests compare complete text output. JSON and SARIF tests verify schemas and rule fields. The entire repository passes the race detector.

## Portability validation

- Linux amd64 binary built and executed.
- macOS amd64 binary cross-built.
- macOS arm64 binary cross-built.

Cross-building validates compilation, not runtime behavior on a macOS host.

## Next corpus work

A broader precision study should sample several server/worker repositories, classify each finding independently, record unsupported targets, and retain minimized regression fixtures. Runtime should be split into package discovery, parse/type-check, model/SSA construction, recognition, and rendering phases.

## Repeating the examples and recent-feature experiments

Run the entire example corpus and the `net/http` smoke experiment with:

```bash
make experiments EXPERIMENT_OUT=/tmp/lifeline-experiments-current
```

The output directory must be new. The runner uses each example's own config,
requests JSON, and gives analysis a 30-second timeout. It performs one warm-up
and three measured repetitions per case, serially. Raw reports, process exit
codes, stderr, individual wall times, median/p95 (nearest rank), source hashes,
working-tree state, binary hash/build metadata, and host/toolchain details stay
in the local output directory. Generated results are not repository fixtures
and must remain out of commits. These are warm-cache end-to-end measurements;
they include package loading, type checking, analysis, and report rendering.
With only three samples, p95 is the maximum observed time, not a stable tail
latency estimate.

`examples/corpus.json` defines expectations for 27 added cases selected from
Git history. `TestRecentFeatureCorpus` checks the same expectations through the
standalone binary during ordinary integration tests. The additions cover:

| Feature | History | Cases |
|---|---|---|
| Local `var` cancellation acquisition | `2219d4a` | lost / deferred |
| Typed constant control flow | `da89f32` | dead / live loop exit and cleanup |
| Owner versus asynchronous helper joins | `892599f` | asynchronous / synchronous wait |
| Captured closure cleanup | `6919920` | uninvoked / invoked |
| Nested goroutine boundaries (review B1) | existing guard, expanded regression | inner loop / inner stop |
| Promoted context fields | `da5a014` | late / early cancellation |
| Stable pointer aliases | `efa4285` | late / early channel close |
| Constant-index channel arrays | `b263883` | late / early channel close |
| Nested context fields read through helpers | `7216e45`, `fb2ebbb` | late / early cancellation |
| Verified join helpers | `9e2e80c` | late / early cancellation |
| Configured join wrappers | `1cb4f3f` | late / early cancellation |
| Partial ownership handoff status | `10ea4da` | partial / verified cleanup |
| Imported constructor ownership | `6eb8d85` | independent good and bad callers |

Most additions are paired positive/negative controls. Expectations check rule
multiplicity; the constructor case checks caller attribution and the handoff
pair checks status even when no warning is emitted. Older examples without
manifest labels are recorded as **observed**, not validated. Unsupported targets,
handed-off obligations, and unestablished path checks remain separate columns;
no-warning output does not imply fully modeled behavior. These synthetic cases
demonstrate specific semantics, not real-project precision or recall.

For a smaller rerun or a historical binary against the same current fixtures:

```bash
python3 scripts/run_experiments.py --binary ./bin/lifeline \
  --output /tmp/lifeline-features-current --features-only
python3 scripts/run_experiments.py --binary /path/to/baseline/lifeline \
  --binary-revision COMMIT --output /tmp/lifeline-features-baseline --features-only
```

A baseline can exit nonzero because it fails current semantic expectations;
inspect its saved reports to distinguish changed diagnostics from execution
failures. Build both revisions with the same toolchain and dependencies for a
controlled comparison. Historical text logs without pinned sources or runtime
metadata are qualitative references only.

## Synthetic scaling benchmark harness (added 2026-10-09)

The repository now includes Go benchmarks in `internal/frontend/benchmark_test.go` for three source-derived scaling risks from the deep audit:

- caller-first `context.CancelFunc` delegation chains, which exercise interprocedural parameter-effect propagation;
- constructor/handle forwarding chains, which exercise returned-obligation propagation and caller instantiation;
- many independent `sync.WaitGroup` instances in one body, which exercise group indexing/accounting.

`recent_features_benchmark_test.go` adds three families at 10, 50, and 100
independent owners: closure cleanup of `var`-declared cancellations, promoted
context fields through pointer aliases and helper joins, and channel-array
signals through pointer aliases. Normal tests assert their semantic outcomes,
so timing a fixture that silently stopped recognizing its protocol fails
validation. Together with the original families this gives 18 benchmark cases.

Run the reproducible frontend benchmark set with:

```bash
make bench
```

or directly with a fixed iteration count when comparing two revisions:

```bash
go test -run '^$' -bench 'BenchmarkBuild' -benchtime=3x -benchmem ./internal/frontend
```

For repeated samples, add `-count=5` and retain raw output locally. Run an
untimed warm-up invocation first. Compare medians and nearest-rank p95 for
`ns/op`, `B/op`, and `allocs/op`, retaining the work counters. Do not run the
end-to-end corpus concurrently with timing benchmarks.

Fixture parsing, importing, and `go/types` checking happen once before `b.ResetTimer`, so reported time and allocations measure `frontend.Build` rather than Go compiler/importer setup. Each fixture also has a normal regression (`TestBenchmarkFixturesBuild`) so ordinary tests verify that the benchmark programs remain valid analysis inputs.

No new canonical performance numbers are recorded here yet. This repository targets Go 1.25, and the environment used while adding the harness did not have the supported toolchain available offline. A compatibility run was used only to validate the benchmark code; its timing must not be compared with the historical Go 1.23.2 numbers above. The next evaluation update should record Go 1.25 hardware/toolchain details and retain raw `benchstat`-compatible output for before/after comparisons.

## Reproducible CI and module state (added 2026-10-09)

The October 2026 evaluation notes describe a `go.sum` and `.github/workflows/ci.yml` workflow, but **neither file is present in this source archive**. The following list describes the documented *intended* `ubuntu-latest` gates; they cannot be verified from this archive as shipped:

1. `make modcheck` (`go mod tidy -diff`) so dependency metadata drift fails without rewriting files;
2. `make check` for the ordinary test suite plus `go vet`;
3. `make race`;
4. `make perfcheck` for machine-independent algorithmic scaling budgets;
5. a one-iteration `BenchmarkBuild...` smoke run;
6. `make build` for the standalone executable.

This closes the earlier snapshot-level reproducibility gap where neither a checksum file nor a checked-in workflow was present.

## Machine-independent scaling budgets (added 2026-10-09)

CI now enforces algorithmic work budgets in addition to the benchmark smoke run. `frontend.Input.WorkStats` is an optional instrumentation sink reset for each build; it records parameter/Done fixed-point evaluations, constructor fixed-point evaluations, structural-CFG cache misses, and WaitGroup balance statement visits. `TestFrontendWorkBudgets` checks adversarial synthetic fixtures against linear bounds:

- a caller-first parameter chain may require separate monotone parameter and `Done` propagation waves, but stays within roughly three selected-function evaluations per function;
- a constructor-forwarding chain stays within roughly two constructor evaluations per selected function;
- each structural body is built at most once into the structural-CFG cache on the chain fixtures;
- independent WaitGroups stay within a constant number of semantically visited statements per group rather than reverting to `groups × body-size` rescans.

Run this gate directly with `make perfcheck`. The ordinary benchmark output also reports the same counters as `param-evals/build`, `ctor-evals/build`, `struct-cfg/build`, and `group-stmts/build`, alongside `ns/op` and allocations. These counters are intentionally hardware-independent and therefore suitable for hard CI thresholds now; elapsed-time, allocation, and RSS thresholds remain deferred until a supported-Go-1.25 baseline is recorded on a pinned runner.

