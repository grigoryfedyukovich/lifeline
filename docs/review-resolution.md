# Code review resolution — v0.1.1

This document records the disposition of the July 2026 source-verified review in [`docs/reviews/lifeline_code_review_2026-07.md`](reviews/lifeline_code_review_2026-07.md).

All four reported bugs, all ten code smells, and all seven performance findings were addressed. The fixes intentionally preserve Lifeline's conservative warning contract rather than expanding the default rule set.

## Bugs

| ID | Resolution | Validation |
|---|---|---|
| B1 | Lifecycle inspection now stops at nested function-literal boundaries. Inner `go` statements are still discovered as separate start sites, so an inner loop is attributed to the inner goroutine and an inner stop path cannot suppress an outer loop warning. | `TestNestedGoroutineLoopBelongsToInnerStart`, `TestNestedGoroutineStopDoesNotSuppressOuterLoop` |
| B2 | `localssa.Build` runs only from `buildFunction`, after `max_functions` truncation. Same-package callees beyond the bound are explicitly marked unsupported instead of being inspected through a side path. | `TestMaxFunctionsBoundsIRAndDirectCallees` |
| B3 | The permanently false `isBlankAssignmentContext` stub was removed. Passing a cancel function as an argument is documented and implemented as a conservative ownership transfer; discarding the call result does not change that transfer. | Frontend ownership tests and full integration suite |
| B4 | Assignment escape detection now pairs `RHS[i]` only with `LHS[i]` when the assignment has positional correspondence. A blank identifier in another position no longer suppresses a real transfer. | `TestEscapeAssignmentPairsBlankByPosition` |

The review described B4 as an `LL1001` false negative. The implementation defect actually caused a real escape to be missed and could therefore produce a false-positive `LL1001`; the underlying pairing bug is fixed either way.

## Code smells

| ID | Resolution |
|---|---|
| S1 | CSV flag parsing moved to `internal/config.SplitCSV`; analyzer and standalone mode share it. |
| S2 | Removed the unused map iteration variable in context-name collection. |
| S3 | Replaced the unused package-wide fact with versioned per-function object facts. Facts are exported for analyzed named functions and imported for direct cross-package goroutine targets in vet mode. |
| S4 | `localssa.Instruction.Callee` is populated for calls, `go`, and `defer`; the SSA-like instructions are now attached to the language-neutral function model instead of being built and discarded. |
| S5 | Added `analyzer.New()`. Each analyzer instance owns closure-local flag state; the exported singleton is constructed through that factory. |
| S6 | Context recognition now resolves the actual `context.Context` interface and uses type identity/`types.Implements`; string-substring matching was removed. |
| S7 | The SARIF information URI moved to `internal/version.InformationURI`. |
| S8 | `Config.Duration` returns `(time.Duration, error)` and validates independently, even if a caller bypasses `Config.Validate`. |
| S9 | Inline array parsing now tokenizes commas outside quoted elements and reports unterminated quotes or empty elements. |
| S10 | Text output is buffered and flush errors are returned. |

## Performance findings

| ID | Resolution |
|---|---|
| P1 | Wrapper lists are compiled once into maps in the frontend builder; hot-path lookups are constant-time. |
| P2 | Standalone root packages are parsed, type-checked, and analyzed by a deterministic worker pool bounded by `GOMAXPROCS`. Results are reassembled in sorted package order. |
| P3 | Cancellation/group definitions share one AST traversal. Function lifecycle, ownership uses, and goroutine starts share a second traversal with explicit nested-function depth tracking. The separate SSA-like traversal remains because it produces a distinct internal representation. |
| P4 | Assumptions and bounds are constructed once per `engine.Analyze` call and shared immutably by diagnostics from that run. |
| P5 | Vet-mode source files are indexed once by cleaned path; diagnostic and edit positions use constant-time lookups. |
| P6 | Identifier-name collection for a suggested cancel name is lazy and runs only for a blank cancel in a short declaration. |
| P7 | Call arguments and relevant subtrees are converted to object sets once per observation, replacing repeated per-cancel/per-group AST scans. |

## Additional hardening

The review fixes also led to several adjacent improvements:

- nested function bodies are excluded from the enclosing function's SSA-like summary;
- `max_functions` now bounds direct same-package target inspection as well as top-level function modeling;
- integration tests build the Lifeline binary once per test process instead of once per test case;
- cross-package object-fact behavior is covered by a real `go vet -vettool` integration test;
- fact and backend semantic versions were incremented.

## Validation commands

```bash
go test -count=1 ./...
go test -race -count=1 ./...
go vet ./analyzer ./cmd/lifeline ./internal/...
go build ./cmd/lifeline
GOOS=darwin GOARCH=amd64 go build ./cmd/lifeline
GOOS=darwin GOARCH=arm64 go build ./cmd/lifeline
```

The regression fixtures for B1, B4, context identity, configured wrappers, errgroup, channel range, explicit break, SSA callees, independent analyzer flags, position indexing, and function facts are committed with the source.
