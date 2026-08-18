# Architecture

```mermaid
flowchart LR
    CLI[Standalone CLI] --> GL[go list -deps -export]
    GL --> POOL[Bounded package worker pool]
    POOL --> PARSE[go/parser + go/types]
    VET[go vet unitchecker] --> FE[Typed frontend]
    PARSE --> FE
    FE --> MODEL[Language-neutral lifecycle model]
    FE --> SSA[Local SSA-like summary]
    SSA --> MODEL
    MODEL --> ENG[Protocol recognizers]
    ENG --> DIAG[Versioned diagnostics]
    DIAG --> TEXT[Text]
    DIAG --> JSON[JSON v1]
    DIAG --> SARIF[SARIF 2.1.0]
    VET <--> FACT[Versioned function object facts]
    FACT --> FE
    PARSE -.-> CFGB[CFG builder]
    CFGB -.-> CFGDUMP[-dump cfg: text or dot]
```

The dotted edges are deliberate: `internal/cfg` is built from the same typed AST as the frontend, but as of this writing nothing in `MODEL`/`ENG` consumes it yet. See "Control-flow graph" below.

## Packages

| Package | Responsibility |
|---|---|
| `cmd/lifeline` | Detect standalone versus vet protocol and dispatch. |
| `analyzer` | `go/analysis` adapter, versioned function facts, categories, source-position indexing, and suggested edits. |
| `internal/standalone` | Package discovery, bounded parallel loading, export-data importer, type checking, flags, exit policy, and `-dump cfg`. |
| `internal/frontend` | Typed Go recognition, lifecycle summaries, nested-function isolation, and lowering. |
| `internal/localssa` | Deterministic, narrow SSA-like instruction summary with canonical callees. |
| `internal/cfg` | Control-flow graph construction and text/DOT rendering from a typed AST body. Not yet consumed by any rule; see "Control-flow graph" below. |
| `internal/model` | Parser-independent spans, instructions, lifecycle records, and CFG types. |
| `internal/engine` | Rule evaluation, assumptions, bounds, and CI policy matching. |
| `internal/report` | Buffered text, JSON, and SARIF rendering. |
| `internal/config` | Strict JSON and flat YAML/TOML configuration plus shared CLI list parsing. |

## Standalone loading

The command invokes `go list -deps -export -json -e` with an argument array, never shell interpolation. Build selection, module replacements, vendoring, and tags are delegated to the Go command. Export files are used by `go/importer`; target package source is parsed and type-checked locally.

Root packages are sorted by import path, analyzed by a worker pool bounded by `GOMAXPROCS`, and reassembled in the original sorted order. The export-file map is immutable during worker execution. A first loading or analysis error cancels remaining work. Timeout paths retain completed diagnostics and label the report incomplete; invalid package/type errors remain hard input failures.

Dependencies are loaded for export data. With `-tests`, same-package test files are appended to the package; external `_test` packages remain a documented limitation.

## Vet mode and facts

The executable recognizes unitchecker protocol invocations and delegates to `golang.org/x/tools/go/analysis/unitchecker`.

For every analyzed named function, the analyzer exports a versioned object fact containing only its conservative body lifecycle summary:

- unconditional-loop presence;
- return evidence;
- context-stop evidence;
- channel-stop evidence;
- configured explicit-stop evidence.

A direct cross-package goroutine target may import this fact. Facts with an incompatible semantic version are ignored and treated as unavailable. Package-wide facts were removed because they were too coarse to connect safely to a particular call site.

## Frontend passes

For each function within `max_functions`, the frontend performs:

1. one definition pass for context-cancel bindings and join groups;
2. one combined pass for body lifecycle, ownership uses, and goroutine starts;
3. one SSA-like instruction pass.

Nested function literals have separate lifecycle boundaries. The combined pass may traverse them to observe uses of outer values and discover inner goroutine starts, but their loops and exits are excluded from the enclosing body's lifecycle summary.

Wrapper names are compiled into maps once per package. Object-use sets are collected once per relevant call or subtree instead of rescanning once per tracked cancel/group object.

## Internal API boundary

The engine imports neither `go/ast` nor `go/types`. All parser-specific reasoning is confined to the frontend, the local SSA builder, and the CFG builder. The model contains source spans rather than AST node identities, allowing deterministic engine tests and leaving room for a future alternative frontend or full-SSA backend.

## Control-flow graph

`internal/cfg` builds a parser-independent `model.CFG` (explicit basic blocks and edges: `if`/`for`/`range`/`switch`/`select` branches, loop back-edges, `break`/`continue`/`goto` resolved to their actual targets under Go's own scoping rules, `return`/`panic` routed to a function's exit block) from the same typed AST the frontend already walks. It is a structural pass only: nothing in `internal/engine` consumes a CFG today, and building one does not change any diagnostic.

This exists to replace `internal/frontend`'s flat, body-wide "does this contain a return/break/select-with-ctx.Done() anywhere" boolean summaries with true reachability over an explicit graph. The flat summaries can be — and were, prior to loop-scoping added in an earlier release — wrong in a specific, demonstrable way: evidence found anywhere in a goroutine body could suppress a finding about a completely unrelated loop in the same body. Loop-scoping closed the common cases of that class of bug without a graph (see `docs/limitations.md`), but one case remains structurally unfixable without one: a goroutine with two separate unconditional loops where only one has its own exit still clears the finding for both, because exit status is tracked per goroutine, not per loop. `tests/differential/cfg_ast_test.go` captures this exact case as an explicit fixture, verifying both that today's engine still has the gap (so a future fix is provable rather than assumed) and that the CFG already carries the correct answer for it (`TestKnownFalseNegative_NestedLoopsMixedResolution`).

Inspect a CFG directly with `lifeline -dump cfg [-dump-format text|dot] ./...`. This walks every named function and every nested function literal (goroutine bodies overwhelmingly being the latter) and bypasses the normal diagnostic pipeline entirely — it is a development aid, not a report format, and applies no `ignore_paths`/generated-file filtering.

No rule currently reads a CFG. Migrating a diagnostic (starting with `LL1002`, since it is the most direct fit — reachability of a function's exit from a persistent strongly-connected component) onto CFG/SCC-based reasoning is a distinct, deliberately separate piece of work from building and validating the graph itself.

## Caching

Lifeline 0.1.1 has no application-managed result cache. This avoids accepting stale summaries before source, dependency, toolchain, configuration, and backend digests can be represented safely. Vet object facts carry a semantic version and incompatible facts are rejected.

If a persistent cache is added, its key must include tool/fact versions, Go toolchain identity, build configuration, source and dependency digests, effective configuration, and backend mode.
