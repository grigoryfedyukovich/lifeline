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
| `LL1003` | A local `sync.WaitGroup` accounts for workers but has no observed `Wait` or ownership transfer. |
| `LL1004` | A local `errgroup.Group` starts workers but has no observed `Wait` or ownership transfer. |
| `LL9001` | Analysis is incomplete because a configured timeout or function bound was reached. Verdict: `UNKNOWN`. |

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

## Command line

```text
-config PATH          explicit YAML, TOML, or JSON file
-format FORMAT        text, json, or sarif
-fail-on RULES        comma-separated rule IDs or all
-ci-exit-code N       policy-failure code; 2 and 3 are reserved
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
| configured | A `-fail-on` policy matched. |
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

Lifeline emits a two-edit suggestion that retains the cancel function and immediately defers it. The proposed name is checked against identifiers in the function. No edit is emitted for field assignments, nonlocal ownership, or structurally ambiguous cases.

## Development

```bash
go test ./...
go build ./cmd/lifeline
```

Dependencies needed by the analyzer driver are vendored so normal builds and analysis do not require network access. The project currently targets Go 1.23 or newer. Standalone root packages are analyzed by a deterministic worker pool bounded by `GOMAXPROCS`.

See:

- [Validated specification](docs/specification.md)
- [Original-spec validation](docs/spec-validation.md)
- [Review resolution](docs/review-resolution.md)
- [Tutorial](docs/tutorial.md)
- [Runnable examples](examples/README.md)
- [Semantics](docs/semantics.md)
- [Architecture](docs/architecture.md)
- [Limitations](docs/limitations.md)
- [Evaluation](docs/evaluation.md)
- [Roadmap](docs/roadmap.md)
