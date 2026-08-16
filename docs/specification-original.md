# Lifeline — Functional Specification

**Primary language:** Go  
**Category:** Goroutine lifecycle analyzer  
**Suggested repository name:** `lifeline`

## 1. Purpose

A `go/analysis` checker that identifies goroutines with unclear cancellation, ownership, or join protocols.

## 2. Product goals

- Recognize common goroutine lifecycle patterns.
- Report missing cancellation, lost cancel functions, and unjoined workers.
- Explain evidence and uncertainty.
- Integrate with `go vet`, editors, tests, and SARIF.

## 3. Explicit non-goals

- Proving general termination.
- Whole-program reasoning across reflection and generated code.
- Flagging every long-lived goroutine as a defect.

## 4. Primary users

- Developers reviewing or validating small but semantically meaningful changes.
- Maintainers who need reproducible diagnostics rather than opaque scores.
- Researchers and students who want an executable, bounded implementation of a classical analysis idea.
- CI systems and coding agents consuming deterministic JSON output.

## 5. Inputs

Go packages and optional lifecycle configuration for project wrappers.

## 6. Outputs and verdicts

Source diagnostics, evidence paths, suggested fixes where safe, and analysis facts.

## 7. Command-line interface

```bash
lifeline ./...
go vet -vettool=$(which lifeline) ./...
lifeline -config lifeline.yaml ./internal/...
```

The CLI must return exit code `0` for a successful analysis run even when it finds a user-level defect, and a separate configurable nonzero CI exit code when policy requires failure. Invalid input and internal errors always use distinct exit codes.

## 8. Functional requirements

1. Diagnostics state the recognized protocol and missing element.
2. Unknown wrappers can be configured declaratively.
3. Facts are versioned to avoid stale cross-package summaries.
4. Suggested fixes are emitted only when syntax-preserving and semantically obvious.

## 9. Architecture

- `go/analysis` frontend with SSA construction.
- Goroutine-start and captured-resource identification.
- Protocol recognizers for context, channel, WaitGroup, errgroup, and explicit stop methods.
- Interprocedural facts for lifecycle summaries.
- Diagnostic and suggested-fix layer.

### Internal API boundary

The parser/frontend must produce a language-neutral internal model. Analysis code must not depend directly on source-parser node identities except through source-span metadata. Solver, graph, or execution backends should be hidden behind a narrow trait/interface so that tests can use a deterministic fake backend.

### Persistence and caching

The MVP may operate without a database. Cached artifacts must be content-addressed by tool version, input digest, configuration digest, and backend mode. Stale cache entries must never be accepted across incompatible semantic versions.

## 10. Semantics and trust model

- The tool must state exactly what is modeled and what is abstracted.
- Any bounded result must print its bound.
- Any solver result used as evidence should be replayed or independently validated when practical.
- `UNKNOWN` and `UNSUPPORTED` are first-class outcomes.
- Reports should include tool version and relevant backend version.

## 11. Running examples

### Example 1: Ignored context

**Input**

```text
func Start(ctx context.Context) {
    go func() {
        for { process(<-queue) }
    }()
}
```

**Run and expected output**

```text
worker.go:2: goroutine has no recognized termination path
available cancellation source: ctx.Done()
consider selecting on ctx.Done()
```

### Example 2: Lost cancel function

**Input**

```text
ctx, _ := context.WithCancel(parent)
go run(ctx)
```

**Run and expected output**

```text
main.go:1: cancel function returned by context.WithCancel is discarded
child goroutine may retain resources until parent cancellation
```

### Example 3: Proper errgroup

**Input**

```text
g, ctx := errgroup.WithContext(ctx)
g.Go(func() error { return serve(ctx) })
return g.Wait()
```

**Run and expected output**

```text
$ lifeline ./...
no lifecycle diagnostics
```

## 12. Configuration

Configuration should be accepted from a project-local YAML/TOML file and overridden by CLI flags. Unknown configuration keys are errors by default. Every effective configuration can be printed with `--print-config` for reproducibility.

## 13. Error handling

- Syntax and type errors include source spans and recovery hints.
- Backend timeouts produce `UNKNOWN`, not success or failure.
- Internal invariant violations produce a crash report containing a reproducible command but no source-code upload.
- Partial results may be emitted only when labeled incomplete.

## 14. Performance targets

- Startup under one second for small examples after installation.
- Typical examples complete in under five seconds.
- Memory use remains under 512 MB for documented MVP limits.
- A timeout and state/formula-size limit are configurable.
- Performance reports distinguish parsing, analysis, backend, witness extraction, and rendering time.

## 15. Security and privacy

- No network access is required for normal analysis.
- Input source remains local unless the user explicitly enables an integration.
- Generated reports avoid embedding unrelated source text.
- Subprocess execution, when required, uses argument arrays rather than shell interpolation.

## 16. Milestones

- M1: local context and channel recognizers.
- M2: WaitGroup/errgroup and SSA paths.
- M3: cross-package facts and wrappers.
- M4: suggested fixes and corpus evaluation.

## 17. Definition of done

The project is portfolio-ready when it has one polished end-to-end workflow, at least three running examples, a clearly documented semantic boundary, reproducible tests, one real-world evaluation, and an issue list that separates correctness work from optional feature expansion.


## Repository shape

```text
lifeline/
├── README.md
├── LICENSE
├── CHANGELOG.md
├── docs/
│   ├── semantics.md
│   ├── architecture.md
│   └── limitations.md
├── examples/
├── src/ or modules/
├── tests/
│   ├── unit/
│   ├── golden/
│   └── integration/
└── .github/workflows/
```

The initial repository should stay intentionally small. A complete vertical slice with honest limits is preferable to broad syntax support with uncertain semantics.

## Diagnostic contract

Every diagnostic should contain:

1. A stable rule or verdict identifier.
2. Source location or input element.
3. A concise statement of the issue.
4. Evidence: model, path, graph edge, conflicting rows, or other witness.
5. Assumptions, bounds, and approximation mode.
6. A suggested action only when it is grounded and mechanically defensible.

## Testing strategy

- **Unit tests:** parser, lattice/graph/constraint primitives, and rendering.
- **Golden tests:** full input-to-output examples committed as fixtures.
- **Property tests:** algebraic laws, witness replay, and graph/automata invariants where applicable.
- **Differential tests:** compare against concrete execution, compilation, or a simpler trusted algorithm.
- **Regression tests:** every reported bug receives a minimized fixture.
- **Corpus tests:** run against several real repositories or policy sets and record precision/runtime observations.

## Release criteria for v0.1

- The supported input subset is documented precisely.
- All examples in this specification run in CI.
- Machine-readable output is versioned.
- Unsupported or unknown cases never appear as successful proofs.
- Linux and macOS builds pass.
- The README includes a two-minute demo and a technically honest limitations section.
