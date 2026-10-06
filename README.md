# Lifeline

Lifeline is a conservative Go static analyzer for goroutine lifecycle protocols. It reports local evidence of missing cancellation, unclear ownership, and missing joins without claiming to prove termination.

The same executable works as a normal command and as a `go vet` tool.

New to the project? Start with the [very high-level tutorial](docs/high-level-tutorial.md), then the [step-by-step tutorial](docs/tutorial.md), then use the [runnable examples index](examples/README.md) as a protocol catalog.

## Two-minute demo

```bash
go build -o ./bin/lifeline ./cmd/lifeline

# Diagnostics do not fail the command by default.
./bin/lifeline ./examples/ignored_context

# Make selected findings fail CI with an explicit exit code.
./bin/lifeline \
  -fail-on LL1001,LL1002 \
  -ci-exit-code 7 \
  ./...

# Use the go vet protocol.
go vet -vettool="$(pwd)/bin/lifeline" ./...
```

Expected standalone output:

```text
examples/ignored_context/worker.go:10:2: [LL1002] goroutine has an unconditional loop and no recognized termination path; available cancellation source: ctx.Done()
  evidence: unconditional for loop
  action: select on the available context's Done channel and return
  model: local-ast-types-ssa-summary/v2; max_functions=10000; timeout=5s
```

## Implemented rules

| Rule | Meaning |
|---|---|
| `LL1001` | A `context.WithCancel`, `WithTimeout`, or `WithDeadline` cancellation function is discarded or has no observed call/ownership transfer. |
| `LL1002` | A goroutine containing an unconditional loop has no recognized return, `break`, context delegation, context-select exit, channel-close exit, or configured stop operation. |
| `LL1003` | A local `sync.WaitGroup` accounts for workers but isn't fully, verifiably joined before the owner returns (no `Wait` observed, `Wait` skipped by some return path, or a proven `Add`/`Done` count mismatch). |
| `LL1004` | The same as `LL1003`, for a local `errgroup.Group` (no count-mismatch condition: `errgroup.Group` manages its own bookkeeping). |
| `LL1005` | A `sync.WaitGroup`/`errgroup.Group` is joined before its workers' own stop signal is proven to have been sent yet. |
| `LL9001` | Analysis is incomplete because a configured timeout or function bound was reached. Verdict: `UNKNOWN`. Hiding it (`ignore`) hides the notice only: the run status still says the run was incomplete. |

Every diagnostic includes a stable rule ID, source span, protocol, evidence, assumptions, configured bounds, backend version, and an action only when the recognizer has grounded support for it.

## Supported lifecycle patterns

Lifeline recognizes:

- standard context factories and direct cancellation calls;
- cancellation ownership returned, assigned, passed, or stored in a value;
- `select` cases that receive from `ctx.Done()` and return;
- contexts delegated to called operations;
- channel ranges and select cases that return after a channel signal;
- `sync.WaitGroup` `Add`/`Go` and `Wait`;
- `errgroup.Group` `Go` and `Wait`;
- direct same-package goroutine targets within the configured bound;
- versioned direct-function facts for cross-package targets in `go vet` mode;
- declarative context, start, join, and stop wrappers.

The analyzer deliberately does not report one-shot goroutines merely because they lack a context. `LL1002` requires an unconditional loop.

## Configuration

Lifeline searches upward from the working directory for:

```text
lifeline.yaml  .lifeline.yaml  lifeline.yml  .lifeline.yml
lifeline.toml  .lifeline.toml  lifeline.json .lifeline.json
```

Unknown keys are errors. Print the exact effective configuration with:

```bash
lifeline -print-config
```

Example:

```yaml
schema_version: 1
format: text
ci_exit_code: 7
timeout: 5s
max_functions: 10000
include_tests: false
fail_on:
  - LL1001
  - LL1002
ignore: []
context_wrappers:
  - example.com/project/lifecycle.WithCancel
start_wrappers:
  - example.com/project/workers.Start
join_wrappers:
  - example.com/project/workers.Group.Wait
stop_wrappers:
  - example.com/project/workers.Stopped
```

The YAML/TOML reader is intentionally strict and flat: scalar values, string arrays, and YAML string lists are supported. JSON is available when a fully standard syntax is preferred.

Wrapper names use these canonical forms:

```text
package/import/path.Function
package/import/path.Type.Method
```

Configured context wrappers are assumed to return `(context, cancel)` in result positions 0 and 1. Configured start wrappers are inspected when they receive a function literal. Join and stop wrappers are recognized by canonical call name.

## Run status

Diagnostics can be filtered (`ignore`, `//lifeline:ignore`), so they cannot say whether the run was complete. The JSON bundle and the SARIF run therefore carry a separate `status`, computed before any filtering:

```text
status.incomplete          analysis was stopped by a bound (max_functions) or a deadline (timeout)
status.reasons             why, one entry per cause
status.units               discovered / analyzed / skipped units (named functions and function literals), excluded_files
status.unsupported         what was judged under an approximation:
                             targets                  goroutine targets that could not be inspected
                             handed_off_obligations   cancel functions / groups assumed to have moved elsewhere
                             unestablished_path_checks  discharged somewhere, all-paths question not answered
status.suppressed          diagnostics found and then hidden, by config / by comment / by rule
status.assumptions         what the results rest on
```

`incomplete` (a resource gap: results cover only part of the input) and `unsupported` (a semantic gap: results are real but rest on approximations) are separate dimensions. A clean `incomplete: false` run is not a claim that no protocol violation exists, and it is not equivalent to a run with `unsupported` entries; text output states both situations when they apply.

Policy on the status is separate from `fail_on`: `-fail-on-incomplete` (`fail_on_incomplete: true`) and `-fail-on-unsupported` (`fail_on_unsupported: true`) fail with `ci_exit_code` based on the status alone, so ignoring `LL9001` cannot be used to pass an incomplete run.

Under `go vet`, which shows only diagnostics, the same data is available as a result and as a companion file: pass `-lifeline.status-out=PATH` and each analyzed package appends one JSON line (`package`, `coverage`, `status`). The analyzer also returns that record (`*analyzer.Result`) as its result, for drivers that consume analyzer results.

## Command line

```text
-config PATH          explicit YAML, TOML, or JSON file
-format FORMAT        text, json, or sarif
-fail-on RULES        comma-separated rule IDs or all
-ci-exit-code N       policy-failure code; 2 and 3 are reserved
-fail-on-incomplete   fail when analysis was cut short by a bound or timeout, whatever the diagnostics say
-fail-on-unsupported  fail when any part of the input was judged under an approximation
-timeout DURATION     overall standalone timeout
-max-functions N      per-package analysis bound
-tests                include same-package _test.go files
-print-config         print effective configuration
-version              print tool and backend versions
```

Exit codes:

| Code | Meaning |
|---:|---|
| `0` | Analysis completed, even if user-level diagnostics were emitted. |
| configured | A `-fail-on` policy matched, or `-fail-on-incomplete` / `-fail-on-unsupported` matched the run status. |
| `2` | Invalid configuration, package, syntax, or type information. |
| `3` | Internal invariant failure or rendering error. |

`go vet -vettool=...` follows the normal vet convention and returns nonzero when diagnostics are printed.

## JSON and SARIF

```bash
lifeline -format json ./... > lifeline.json
lifeline -format sarif ./... > lifeline.sarif
```

JSON reports use schema `lifeline.report/v1`. Source paths under the working directory are rendered relatively. SARIF output uses version 2.1.0 and includes Lifeline rule metadata.

## Safe suggested fix

For the exact form:

```go
ctx, _ := context.WithCancel(parent)
```

Lifeline emits a two-edit suggestion that retains the cancel function and immediately defers it. The proposed name is checked against every identifier in the function, and a second fix in the same function gets a different name. The diagnostic is always reported; the edit is offered only where it is valid Go that keeps the resource's scope and lifetime, so it is **withheld** when the assignment is:

- the initializer of an `if`, `for`, `switch` or type switch (there is no statement boundary to put `; defer ...` after);
- inside a `for` or `range` body, including through nested blocks, `switch` and `select` (a `defer` runs at function return, so it would hold every iteration's context instead of releasing each one);
- under a label, or anywhere in a function that uses `goto` (control flow can re-enter the statement);
- a factory whose cancel function takes an argument, such as `context.WithCancelCause` (`defer cancel()` would not compile);
- not a short declaration, a field assignment, or nonlocal ownership.

Every emitted fix is applied to source and type-checked in the test suite (`internal/frontend/fix_validity_test.go`).

## Development

```bash
go test ./...
go build ./cmd/lifeline
```

Dependencies needed by the analyzer driver are fetched from their upstream module proxy on first build like any normal Go module; the repository does not vendor them. The project currently targets Go 1.25 or newer (see `go.mod`; this was raised from 1.22 because `go vet`'s rewritten driver in Go 1.26 is not compatible with the older pinned `golang.org/x/tools`, see `CHANGELOG.md`). Standalone root packages are analyzed by a deterministic worker pool bounded by `GOMAXPROCS`.

See:

- [Validated specification](docs/specification.md)
- [Original-spec validation](docs/spec-validation.md)
- [Review resolution](docs/review-resolution.md)
- [Tutorial](docs/tutorial.md)
- [Runnable examples](examples/README.md)
- [Semantics](docs/semantics.md)
- [Architecture](docs/architecture.md)
- [AST/CFG migration plan](docs/cfg-migration-plan.md)
- [Limitations](docs/limitations.md)
- [Evaluation](docs/evaluation.md)
- [Roadmap](docs/roadmap.md)
