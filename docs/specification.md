# Lifeline — validated implementation specification

**Specification version:** 0.1.1  
**Primary language:** Go 1.25+  
**Category:** conservative goroutine lifecycle analyzer  
**Report schema:** `lifeline.report/v1`  
**Fact schema:** version 2  
**Backend:** `local-ast-types-ssa-summary/v2`

This is the authoritative specification for the implemented repository. The original project brief is retained as [`specification-original.md`](specification-original.md), and its requirement-by-requirement validation is recorded in [`spec-validation.md`](spec-validation.md).

## 1. Purpose

Lifeline is a `go/analysis` checker and standalone command that reports locally observable evidence of incomplete goroutine cancellation, ownership, or join protocols. It does not prove leaks, nontermination, or whole-program correctness.

## 2. Product goals

- Recognize common context, channel, `sync.WaitGroup`, and `errgroup` lifecycle idioms.
- Report lost cancellation functions, unconditional-loop goroutines without recognized stop paths, and locally unjoined worker groups.
- Explain the recognized protocol, evidence, assumptions, bounds, and a grounded action.
- Integrate with standalone CI, `go vet`, editor-compatible analysis drivers, JSON consumers, and SARIF consumers.
- Remain deterministic, local by default, and honest about unsupported reasoning.

## 3. Non-goals

- General termination or leak proofs.
- Whole-program reasoning through reflection, interfaces, arbitrary function values, generated dispatch, or unrestricted aliasing.
- Warning on every goroutine that lacks a context.
- Proving reachability of a recognized `return`, `break`, channel-close path, or context path.
- Replacing full Go SSA or a control-flow model checker.

## 4. Inputs

- Go package patterns accepted by `go list`.
- Build-selected source and export data from the local Go toolchain.
- Optional strict project configuration in JSON, YAML, or TOML.
- Vet-mode versioned function facts for direct cross-package goroutine targets.

Normal analysis performs no network requests.

## 5. Commands

```bash
lifeline ./...
lifeline -config lifeline.yaml ./internal/...
lifeline -format json ./... > lifeline.json
lifeline -format sarif ./... > lifeline.sarif
go vet -vettool="$(which lifeline)" ./...
lifeline -dump cfg ./...
lifeline -dump cfg -dump-format dot ./... | dot -Tpng -o cfg.png
lifeline -dump facts -dump-format json ./...
```

Standalone diagnostics do not fail the command by default. `-fail-on` enables an explicit policy failure using `-ci-exit-code`.

`-dump cfg` and `-dump facts` bypass the normal diagnostic pipeline: `-dump cfg` builds and prints a control-flow graph (see `docs/architecture.md`) for every named function and function literal; `-dump facts` prints, per goroutine, the computed stop-capability, termination, and (not yet fully wired, see `docs/architecture.md`) join facts from the Phase 3 dataflow solver. Neither runs any diagnostic rule, and both always exit `0` on success. Both are development/debugging aids, not report formats, and are not subject to `-fail-on`/`-ci-exit-code`.

Exit codes:

| Code | Meaning |
|---:|---|
| `0` | Analysis completed, including runs that emitted lifecycle warnings. |
| configured positive code | A `fail_on` policy matched. |
| `2` | Invalid flags, configuration, package loading, syntax, or type information. |
| `3` | Internal invariant failure or output-rendering failure. |

Vet mode follows the normal `go vet` diagnostic exit convention.

## 6. Diagnostic rules

| ID | Verdict | Trigger |
|---|---|---|
| `LL1001` | `WARNING` | A supported context factory returns a cancel function that is discarded or has no observed call or ownership transfer. |
| `LL1002` | `WARNING` | A modeled goroutine body contains `for {}` and no accepted return, break, context, channel, or configured stop evidence. |
| `LL1003` | `WARNING` | A local `sync.WaitGroup` records worker starts but isn't fully, verifiably joined before the owner returns: no `Wait`/ownership transfer observed at all, a `Wait` observed but not on every return path, or a `Wait` on every path with a literal `Add`/`Done` count that still doesn't balance. |
| `LL1004` | `WARNING` | The same three conditions as `LL1003`, for a local `errgroup.Group` (the count-mismatch condition never applies: `errgroup.Group` manages its own `Add`/`Done`-equivalent bookkeeping internally). |
| `LL1005` | `WARNING` | A `sync.WaitGroup`/`errgroup.Group` is joined before its workers' own recognized stop signal is guaranteed to have been sent, proven via CFG reachability rather than merely unproven the other way. |
| `LL9001` | `UNKNOWN` | `max_functions` or the standalone timeout makes analysis incomplete. |

An unsupported direct target is retained as `unsupported` model evidence and is never interpreted as a successful termination proof. Version 0.1.1 does not emit a separate user-visible unsupported diagnostic; this remaining specification gap is tracked in the roadmap.

## 7. Recognized lifecycle evidence

### Cancellation ownership

Standard context factories and configured equivalents are modeled. The cancel obligation is discharged when the cancel function is:

- called directly or through `defer`;
- returned;
- passed as an argument to a resolvable same-package function whose own body is verified to consume it in turn (a direct call, `defer`, or any of the other discharge forms on this list — one hop reliably regardless of declaration order, a chain of hops as far as a pre-pass computed a deeper callee's result in time; see `docs/limitations.md`), or, when the callee cannot be resolved with confidence, unconditionally as before;
- assigned to a named value;
- stored into a named field of a *keyed* struct literal assigned to a single local variable, or built directly in a `return` statement, and verified against that specific field's later use — a call through the field (same-function "stored struct" shape) or a direct, resolvable, same-package caller's own use of the field (the "constructor" shape) — one verified hop either way; any other, more indirect storage (an unkeyed literal, a multi-name assignment, a slice/map/array, or a value passed on to a further function) remains unconditionally discharged, as before this capability existed.

A cancel value assigned to `_` is lost immediately. A suggested edit is emitted only for the unambiguous short-declaration form where retaining and immediately deferring the cancel function is syntax-preserving.

### Goroutine stop protocol

`LL1002` is considered only when a modeled body contains an unconditional `for` loop, and is evaluated over a control-flow graph (`internal/cfg`), not flat per-body booleans: it fires when the loop forms a reachable, persistent (cyclic) strongly-connected component with no edge leaving it that can reach the function's exit block. Each such component is judged independently, so two separate unconditional loops in the same goroutine where only one has its own exit are evaluated separately, not conflated into one whole-body verdict. Accepted may-exit evidence, each represented as a real graph edge to the function's exit rather than a body-wide flag:

- a `return` (anywhere reachable from the loop, regardless of nesting inside further switch/select/inner-loop constructs, since `return` exits the function unconditionally wherever it occurs);
- an explicit `break` or labeled `break`/`continue`/`goto`, resolved to its exact target under Go's own grammar (an unlabeled `break` inside a nested loop/switch/select targets that construct, not an outer loop, exactly as Go itself resolves it);
- a channel range (exhausted/closed exits its own loop; this no longer double-counts as evidence for a separate, unrelated loop in the same body);
- a `select` case whose body reaches the function's exit by any of the above means, not only a literal `return` immediately following a `ctx.Done()` receive;
- delegation of an available context to a called operation, or a configured stop operation — both represented as a trusted edge straight to the function's exit (`internal/cfg`'s `trusted-stop` edge kind), since neither is otherwise visible in pure control flow; `internal/cfg` itself has no notion of config or tracked contexts, only the predicate `internal/frontend` derives from them and passes in.

All of the above is reachability over a graph, not a proof: a resolving edge means a recognized escape exists, not that every execution takes it, and delegation/configured-stop edges are trusted rather than verified.

This applies whenever a body was available to build a CFG from. A cross-package goroutine target resolved through a `go vet` fact does not yet have one (facts still carry only flat per-body booleans) and uses the older flat check as a fallback; see `docs/limitations.md`.

Nested function literals have independent lifecycle summaries and independent CFGs. Their loops and stop paths cannot alter the enclosing goroutine's summary, although nested goroutine start sites are analyzed separately.

### Join protocol

- `sync.WaitGroup.Add` and `sync.WaitGroup.Go` create a local join obligation.
- `errgroup.Group.Go` creates a local join obligation.
- `Wait`, a configured join wrapper, or an observed ownership transfer discharges that obligation.
- The same field/constructor ownership tracking described above for cancellation applies to a group: stored into a named field of a keyed struct literal (a single local variable, or built directly in a `return`), the obligation is verified against that field's own later `Add`/`Go`/`Wait` use — same-function or, for a constructor, one verified hop into a direct caller — rather than unconditionally discharged; any more indirect storage remains unconditionally discharged, as before.

As of Phase 6 (`docs/cfg-migration-plan.md`), discharge is no longer a single flat "was `Wait` ever observed" boolean, and `Add`/`Done` counts are no longer purely qualitative — three independently-verified upgrades, each only ever firing from positive evidence:

- **Count balance** (`sync.WaitGroup` only): a literal (or constant-folded named `const`) `Add(n)`/`Done()` call outside any loop is counted exactly; the common `Add(1)` paired with a `go func(){ defer wg.Done() }()` inside the same loop is recognized as self-balancing regardless of iteration count. A mismatch is reported only when every `Add`/`Done` site for a group fits one of those two shapes and the totals still don't balance; anything else (a non-literal amount, an unmatched loop-scoped call) leaves the count unproven rather than guessed. `errgroup.Group` is exempt: its bookkeeping is internal to the library.
- **Join-before-owner-return**: a CFG reachability check (`model.CFG.ReachableAvoiding`) that every path from the owner's own entry to its exit passes through at least one `Wait` call for the group — the same class of precision `LL1002` already has for loops (reachable on *some* path is not reachable on *every* path). A `Wait` bypassed by an early return now fires, where the old flat boolean would have suppressed it.
- **Stop-before-wait**: the same reachability check in reverse, against a recognized worker stop signal (a configured `stop_wrapper` call, or a tracked context's cancel function that is both called and observed to be captured by a goroutine started in the same function). Firing requires a positive CFG proof that the stop signal is reachable only *after* `Wait`, never merely an absence of proof that it comes before; this is reported as `LL1005`, separately from `LL1003`/`LL1004`.

The stop-signal recognition above is function-scoped, not correlated to which specific group a given goroutine belongs to; see `docs/limitations.md` for the resulting residual risk when a function manages more than one WaitGroup/errgroup and more than one stop mechanism. Both CFG checks are same-function only.

## 8. Architecture and internal boundary

- Standalone mode uses `go list -deps -export -json -e`, `go/parser`, and `go/types`.
- Root packages are analyzed concurrently with a worker count bounded by `GOMAXPROCS`; output remains sorted deterministically.
- The typed frontend lowers parser objects into `internal/model` records containing spans, lifecycle facts, and SSA-like instructions.
- `internal/localssa` versions local definitions and records lifecycle-relevant operations and canonical callees.
- `internal/cfg` builds a parser-independent control-flow graph from the same typed AST, dumpable via `-dump cfg`. `internal/frontend` attaches one to each goroutine body, and `internal/engine` consumes it for `LL1002`'s verdict (`docs/architecture.md`) via `model.CFG`'s own graph algorithms, without importing `go/ast`/`go/types` itself.
- `internal/model.Solve` is a generic forward-dataflow worklist solver over a CFG; `internal/engine`'s `StopCapability`/`JoinObligation`/`Ownership` lattices are built on it (`docs/cfg-migration-plan.md` Phase 3, `docs/architecture.md`), dumpable via `-dump facts`. Only `StopCapability` is wired to real goroutines as of this release; no diagnostic rule reads a dataflow result yet.
- `internal/engine` imports neither `go/ast` nor `go/types`.
- Vet mode exports and imports versioned object facts for named function lifecycle summaries. As of fact schema version 3 (Phase 4, `docs/cfg-migration-plan.md`), the exported fact includes a CFG/SCC-derived `LoopUnresolved` verdict alongside the prior flat booleans, so a cross-package `LL1002` target gets the same precision a same-package or closure target has from `internal/cfg`/`internal/engine`. An incompatible fact version is unavailable, not reinterpreted.
- Rendering is isolated in `internal/report`.

There is no Lifeline-managed persistent result cache in v0.1.1. If one is introduced, its key must include the tool and fact versions, Go toolchain/build configuration, source and dependency digests, effective configuration, and backend mode.

## 9. Configuration

Configuration is discovered upward from the current directory or selected explicitly with `-config`. Unknown keys and invalid values are errors. `-print-config` emits the effective configuration as JSON.

Supported keys:

```text
schema_version, format, ci_exit_code, timeout, max_functions,
include_tests, fail_on, ignore, ignore_paths, context_wrappers,
start_wrappers, join_wrappers, stop_wrappers
```

Wrapper names use canonical forms:

```text
import/path.Function
import/path.Type.Method
```

The YAML/TOML parser deliberately supports a strict flat subset: scalar values, quoted inline string arrays, and YAML string lists. JSON uses the standard library decoder with unknown-field rejection.

`ignore_paths` excludes files by path from analysis (not just from reported output): each pattern is matched with `filepath.Match` semantics against both the file's path relative to the working directory and its base filename. A file carrying the standard `// Code generated ... DO NOT EDIT.` marker is excluded automatically, unconditionally of config. A specific finding can additionally be suppressed inline with a `//lifeline:ignore` (optionally `//lifeline:ignore LL1001,LL1002`) comment; see `docs/limitations.md` for exact matching rules.

## 10. Output contract

Each diagnostic contains:

1. report schema and stable rule identifier;
2. `WARNING` or `UNKNOWN` verdict;
3. source span and package/function identity;
4. recognized protocol and concise message;
5. evidence records;
6. modeling assumptions and configured bounds;
7. tool and backend versions;
8. a suggested action and edit only when mechanically defensible.

Text, JSON, and SARIF 2.1.0 are supported. JSON bundles mark the complete report `incomplete` when any `UNKNOWN` diagnostic is present.

## 11. Bounds and errors

- `max_functions` is applied before function lifecycle and SSA-like construction.
- Same-package direct targets beyond the bound are not inspected through a side path.
- The standalone overall timeout produces `LL9001` rather than a success/failure claim.
- Syntax and type failures contain source-oriented Go diagnostics and a recovery hint.
- Internal panics are caught in standalone mode and print a local reproduction command without uploading source.

## 12. Performance and security targets

Validated on Go 1.23.2, linux/amd64:

- small example, warm Go cache: approximately 0.34 seconds and 38 MB maximum RSS;
- `net/http`, warm Go cache: approximately 0.86 seconds and 71 MB maximum RSS;
- all documented examples complete below the five-second target individually;
- memory remains below the 512 MB MVP target in the recorded evaluation.

Subprocesses use argument arrays rather than shell interpolation. Reports include only relevant source locations and evidence, not unrelated source bodies.

Phase-separated runtime reporting is not yet implemented; it remains correctness/observability work rather than an undocumented claim.

## 13. Testing and release criteria

The repository includes:

- unit tests for config, frontend recognizers, engine rules, local SSA, analyzer flags, and source positions;
- minimized regressions for every bug fixed from the July 2026 review;
- golden end-to-end examples;
- standalone policy, JSON, SARIF, and tutorial integration tests;
- a real vet-mode cross-package fact test;
- race-detector validation;
- a `net/http` smoke evaluation;
- Linux execution and macOS amd64/arm64 cross-build validation.

Property-based and differential tests, a broader multi-repository corpus, phase timings, and a user-visible `UNSUPPORTED` outcome remain explicit roadmap items.
