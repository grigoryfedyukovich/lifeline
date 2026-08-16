# Changelog

## Unreleased

- Standalone-mode text/JSON/SARIF output no longer collapses three different situations into the single string `no lifecycle diagnostics`. It now distinguishes: nothing lifecycle-relevant was recognized at all (`no recognized lifecycle constructs`); everything recognized was compliant (`no lifecycle diagnostics (checked N cancel binding(s), M goroutine(s), K group(s))`); and everything recognized was compliant except for goroutine targets whose bodies could not be inspected, which are now listed by file:line and reason instead of being silently absent. This also means a standalone run that hits a cross-package goroutine target (which standalone mode cannot resolve via facts, unlike `go vet`) now discloses that gap instead of reporting an indistinguishable clean result. JSON/SARIF gain a `coverage` object with the same counts. See `docs/limitations.md` for exactly what the new counts do and do not cover — in particular, a call to an unconfigured wrapper function still contributes nothing to any of these counts, since no binding is ever created for it to summarize.
- Fixed a false lifecycle claim: cross-package `go vet` goroutine targets no longer inherit the caller's locally-available context names into `LL1002`'s "available cancellation source" evidence. The target function's actual parameters are unknown to the imported cross-package fact, so asserting a caller-side context as available to it was ungrounded and could point at a context the target never receives.
- Raised minimum Go version to 1.25.0 (reversing the prior "lowered to 1.22" change) and upgraded `golang.org/x/tools` (v0.22.1 -> v0.49.0) and `golang.org/x/sync` (v0.7.0 -> v0.22.0). The pinned mid-2024 `x/tools` build of `unitchecker` cannot speak the vet-driver protocol used by Go 1.26's rewritten `go vet`/`go fix` implementation: under Go 1.26, `go vet -vettool=...` silently produced no diagnostics and exited 0 instead of reporting findings and failing. This is a breaking change for anyone building with Go 1.22-1.24; there is currently no known `x/tools` version that satisfies both the old and new protocol.
- Corrected CI matrix Go versions (was referencing a non-existent 1.26.x).
- Added a very high-level tutorial (`docs/high-level-tutorial.md`) linked from the README.

## 0.1.1 - 2026-07-17

- Added an end-to-end tutorial and runnable good/bad examples for cancellation, context/channel shutdown, WaitGroup, errgroup, and configured wrappers.
- Fixed nested-function boundary contamination that could both create and suppress `LL1002` incorrectly.
- Fixed positional ownership-transfer handling for multi-value assignments.
- Applied `max_functions` before lifecycle and SSA-like construction and prevented direct-callee bound bypass.
- Replaced unused package facts with versioned per-function facts consumed for direct cross-package goroutine targets in vet mode.
- Populated SSA-like callee names and retained the instruction summary in the language-neutral model.
- Replaced global analyzer flag state with independently constructible analyzer instances.
- Replaced context type string matching with type identity and interface implementation checks.
- Consolidated frontend traversals, precompiled wrapper sets, reused object-use sets, parallelized standalone root-package analysis, indexed source positions, and buffered text output.
- Hardened duration parsing and quoted inline-array parsing.
- Added regression coverage for all July 2026 review bugs and supporting code-quality changes.
- Added the original specification, a validated current specification, a compliance matrix, the external review, and a finding-by-finding resolution record.

## 0.1.0 - 2026-07-17

- Added dual standalone and `go vet -vettool` operation.
- Added `LL1001`–`LL1004` lifecycle rules and bounded `LL9001` unknown results.
- Added local type checking, same-package target inspection, and a narrow SSA-like summary.
- Added strict JSON and flat YAML/TOML configuration with declarative wrappers.
- Added text, versioned JSON, and SARIF 2.1.0 output.
- Added opt-in CI failure policy and distinct invalid/internal exit codes.
- Added a safe lost-cancel suggested edit.
- Added unit, golden, integration, policy, and machine-format tests.
- Added a `net/http` precision smoke evaluation and documented semantic limits.
