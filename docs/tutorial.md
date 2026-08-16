# Lifeline tutorial

This tutorial walks from the first build to CI integration. Commands assume the repository root as the current directory.

## 1. Build the analyzer

Lifeline is a normal Go program. Its dependencies are vendored, so this build does not need network access.

```bash
go build -o ./bin/lifeline ./cmd/lifeline
./bin/lifeline -version
```

A successful version command has this shape:

```text
lifeline 0.1.1 (local-ast-types-ssa-summary/v2)
```

You can also install it into `GOBIN`:

```bash
go install ./cmd/lifeline
lifeline -version
```

## 2. Find a worker without a stop path

Run Lifeline on the intentionally defective example:

```bash
./bin/lifeline ./examples/ignored_context
```

The worker receives a context in its owner function, but its loop only waits for queue input:

```go
func Start(ctx context.Context) {
	go func() {
		for {
			process(<-queue)
		}
	}()
}
```

Lifeline reports `LL1002` and explains both sides of the evidence:

```text
examples/ignored_context/worker.go:10:2: [LL1002] goroutine has an unconditional loop and no recognized termination path; available cancellation source: ctx.Done()
  evidence: unconditional for loop
  action: select on the available context's Done channel and return
  model: local-ast-types-ssa-summary/v2; max_functions=10000; timeout=5s
```

Read this as a bounded review finding, not a termination proof:

- **Recognized protocol:** a goroutine with an unconditional loop.
- **Missing element:** a modeled stop path.
- **Available evidence:** the owner already has `ctx`.
- **Approximation:** local type-checked syntax and direct same-package callees.

By default, diagnostics do not make the standalone command fail:

```bash
./bin/lifeline ./examples/ignored_context
echo $?  # 0
```

## 3. Repair the worker

Compare the defective example with `examples/proper_context`:

```go
func Start(ctx context.Context) {
	go func() {
		for {
			select {
			case item := <-queue:
				process(item)
			case <-ctx.Done():
				return
			}
		}
	}()
}
```

Run it:

```bash
./bin/lifeline ./examples/proper_context
```

Expected output:

```text
no lifecycle diagnostics (checked 0 cancel binding(s), 1 goroutine(s), 0 group(s))
```

The accepted evidence is deliberately modest: Lifeline recognizes a `select` case that receives from `ctx.Done()` and returns. It does not prove that cancellation eventually occurs.

## 4. Understand cancellation ownership

Calling a context factory creates an ownership obligation for its cancellation result.

### Discarded cancellation

```bash
./bin/lifeline ./examples/lost_cancel
```

The source uses the blank identifier:

```go
ctx, _ := context.WithCancel(parent)
go run(ctx)
```

This produces `LL1001`. In JSON output, this exact syntax also carries a safe suggested edit:

```bash
./bin/lifeline -format json ./examples/lost_cancel > /tmp/lifeline.json
```

The edit retains the result under a fresh local name and inserts an immediate `defer`.

### Named but uncalled cancellation

```bash
./bin/lifeline ./examples/uncalled_cancel
```

Here the cancellation function has a name but is neither called nor transferred:

```go
ctx, cancel := context.WithCancel(parent)
_ = cancel
return ctx
```

Lifeline reports the unresolved obligation, but does not offer an automatic edit because the correct ownership decision may require restructuring.

### Correct local ownership

```bash
./bin/lifeline ./examples/proper_cancel
```

The usual local pattern is:

```go
ctx, cancel := context.WithTimeout(parent, timeout)
defer cancel()
```

Calling, deferring, returning, passing, assigning, or storing the cancellation function is treated as an ownership discharge or transfer. Lifeline does not follow transferred ownership interprocedurally in version 0.1.

## 5. Check worker joins

Stopping and joining are different obligations. A worker may terminate correctly but still outlive the function that owns it.

### WaitGroup without Wait

```bash
./bin/lifeline ./examples/unjoined_waitgroup
```

The example calls `Add(1)` but never calls `Wait`, producing `LL1003`.

Compare it with:

```bash
./bin/lifeline ./examples/proper_waitgroup
```

The clean example calls `workers.Wait()` before the owner returns.

### errgroup without Wait

```bash
./bin/lifeline ./examples/unjoined_errgroup
```

Calling `group.Go` creates a join obligation. Dropping the group produces `LL1004`.

The complete pattern is in `examples/proper_errgroup`:

```go
group, ctx := errgroup.WithContext(ctx)
group.Go(func() error { return serve(ctx) })
return group.Wait()
```

Run it:

```bash
./bin/lifeline ./examples/proper_errgroup
```

## 6. Use channel closure as a stop protocol

A worker ranging over a receive channel naturally exits when its owner closes that channel:

```go
func Start(jobs <-chan int) {
	go func() {
		for job := range jobs {
			process(job)
		}
	}()
}
```

Lifeline accepts this bounded pattern:

```bash
./bin/lifeline ./examples/channel_shutdown
```

Version 0.1.1 does not prove that any reachable owner actually closes the channel. This remains an explicit limitation of the modeled trust boundary.

## 7. Declare a project-specific context wrapper

Real projects often hide `context.WithCancel` behind a helper. Lifeline does not guess unknown helpers.

The custom example defines:

```go
func ProjectWithCancel(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithCancel(parent)
}
```

Run without configuration:

```bash
./bin/lifeline ./examples/custom_context_wrapper
```

Expected result:

```text
no lifecycle diagnostics (checked 0 cancel binding(s), 1 goroutine(s), 0 group(s))
```

Note what the coverage figures do and do not tell you here: `0 cancel binding(s)` is not "checked and found compliant" — it means the recognizer never matched `ProjectWithCancel` as a cancel factory at all, so there was nothing to check. The `1 goroutine(s)` comes from the unrelated `go run(ctx)` call, which the recognizer does understand independent of any wrapper configuration. A nonzero coverage count in a function is not a signal that every relevant construct in it was found.

Now declare the wrapper using its canonical package path:

```yaml
context_wrappers:
  - github.com/gfedyukovich/lifeline/examples/custom_context_wrapper.ProjectWithCancel
```

Run with the supplied configuration:

```bash
./bin/lifeline \
  -config ./examples/custom_context_wrapper/lifeline.yaml \
  ./examples/custom_context_wrapper
```

The discarded cancellation result now produces `LL1001`.

Configured context wrappers must return `(context, cancel)` in result positions 0 and 1. Wrapper names use one of these forms:

```text
package/import/path.Function
package/import/path.Type.Method
```

The same configuration file can list `start_wrappers`, `join_wrappers`, and `stop_wrappers`. Print the exact effective configuration before debugging a surprising result:

```bash
./bin/lifeline \
  -config ./examples/custom_context_wrapper/lifeline.yaml \
  -print-config
```

Unknown configuration keys are errors rather than ignored typos.

### Declare a project-specific worker launcher

`examples/custom_start_wrapper` hides a goroutine behind this helper:

```go
func Launch(worker func()) {
	go worker()
}
```

Without configuration, the call is not assumed to start a worker:

```bash
./bin/lifeline ./examples/custom_start_wrapper
```

```text
no lifecycle diagnostics (checked 0 cancel binding(s), 1 goroutine(s), 0 group(s)); 1 goroutine target(s) could not be analyzed and are excluded from this result:
  examples/custom_start_wrapper/main.go:7:2  goroutine target body is not statically identifiable
```

That goroutine and its unsupported target are `Launch`'s own `go worker()` statement, found regardless of whether `Launch` is configured anywhere: `worker` is a `func()` parameter, not a statically resolvable declaration, so Lifeline can see the start site but not what it does. This is unrelated to `Start`'s call to `Launch` — that call still is not recognized as a goroutine start at all until `Launch` is declared as a `start_wrapper` below.

The supplied configuration declares its canonical name:

```yaml
start_wrappers:
  - github.com/gfedyukovich/lifeline/examples/custom_start_wrapper.Launch
```

Run with that configuration:

```bash
./bin/lifeline \
  -config ./examples/custom_start_wrapper/lifeline.yaml \
  ./examples/custom_start_wrapper
```

Lifeline now inspects the function literal passed to `Launch` and reports `LL1002` for its unconditional loop. Version 0.1.1 inspects configured start wrappers only when a function literal is directly present in the argument list.

## 8. Analyze a real module

Run Lifeline on all packages selected by the current build environment:

```bash
./bin/lifeline ./...
```

Useful narrower patterns include:

```bash
./bin/lifeline ./internal/...
./bin/lifeline ./cmd/...
./bin/lifeline -tests ./...
```

The standalone loader follows Go package selection, build tags, and type checking. Invalid packages and configuration errors use exit code 2.

For an initial rollout, prefer reviewing the text report without a failure policy. Record false positives and unsupported ownership conventions, then add wrappers or narrow CI policy only after the findings are understood.

## 9. Integrate with go vet

The same executable detects the `go vet` protocol:

```bash
go vet -vettool="$(pwd)/bin/lifeline" ./...
```

Vet mode uses normal `go vet` exit behavior: diagnostics make the vet invocation nonzero. The standalone command is the better interface when you need Lifeline's configurable policy exit code or JSON/SARIF rendering.

When this command is run inside the Lifeline repository itself, the intentionally defective example packages produce diagnostics. In another project, run it on the package patterns that project wants to review.

## 10. Produce JSON and SARIF

Versioned JSON is suitable for scripts and coding agents:

```bash
./bin/lifeline -format json ./... > lifeline.json
```

The top-level schema is `lifeline.report/v1`. Each diagnostic contains:

- rule ID and verdict;
- source span and function;
- recognized protocol;
- evidence;
- assumptions and bounds;
- tool/backend versions;
- a suggestion and structured fix only when defensible.

With `jq`, list compact findings:

```bash
jq -r '.diagnostics[] | "\(.rule_id) \(.position.file):\(.position.start_line) \(.message)"' lifeline.json
```

Generate SARIF for code-scanning systems:

```bash
./bin/lifeline -format sarif ./... > lifeline.sarif
```

Lifeline emits SARIF 2.1.0 rule metadata and source locations. Uploading that file is an external CI-platform concern; normal analysis itself requires no network access.

## 11. Add an explicit CI policy

Start by failing only on rules your project has reviewed:

```bash
./bin/lifeline \
  -fail-on LL1001,LL1003,LL1004 \
  -ci-exit-code 7 \
  ./...
```

The exit contract is:

| Exit code | Meaning |
|---:|---|
| `0` | Analysis completed; diagnostics may still be present. |
| configured code | A selected `fail_on` rule matched. |
| `2` | Invalid configuration, package, syntax, or type information. |
| `3` | Internal invariant or rendering failure. |

Codes 2 and 3 are reserved and cannot be selected as the CI policy code.

A project-local `lifeline.yaml` can make policy reproducible:

```yaml
schema_version: 1
format: text
ci_exit_code: 7
timeout: 10s
max_functions: 20000
include_tests: true
fail_on:
  - LL1001
  - LL1003
  - LL1004
ignore: []
context_wrappers: []
start_wrappers: []
join_wrappers: []
stop_wrappers: []
```

Check the merged file-and-flag configuration with `-print-config` in CI logs.

## 12. Handle UNKNOWN honestly

Lifeline has explicit resource bounds:

```bash
./bin/lifeline -max-functions 1 ./...
./bin/lifeline -timeout 100ms ./...
```

When a bound truncates analysis, Lifeline emits `LL9001` with verdict `UNKNOWN`. In JSON, the report's `incomplete` field becomes `true`.

Do not interpret an incomplete report as clean. A defensible CI policy can include `LL9001` explicitly:

```bash
./bin/lifeline -fail-on LL9001 -ci-exit-code 7 ./...
```

`-fail-on all` applies to warnings, not `UNKNOWN`, so bounded incompleteness must be selected deliberately.

## 13. Suggested adoption workflow

1. Build Lifeline and run it without `fail_on`.
2. Inspect evidence and compare warnings with actual ownership conventions.
3. Add declarative wrappers for stable project APIs.
4. Add minimized regression examples for every accepted or rejected finding.
5. Enable JSON or SARIF collection in CI.
6. Fail only on reviewed rules, with `LL9001` handled explicitly.
7. Revisit assumptions after Lifeline upgrades because semantic versions and fact versions can change the modeled boundary.

For a compact command catalog, see [the examples index](../examples/README.md). For the exact trust boundary, continue with [Semantics](semantics.md) and [Limitations](limitations.md).
