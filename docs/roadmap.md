# Roadmap

## Correctness and trust work

1. **In progress.** Replace the may-exit loop heuristic with a compact control-flow graph that distinguishes loop, switch, select, and labeled breaks. `internal/cfg` now builds and dumps (`-dump cfg`) this graph; no rule consumes it yet. See `docs/cfg-migration-plan.md` for the phased plan and current status, and `docs/architecture.md` for how it fits the rest of the codebase.
2. **Done.** Emit a low-noise user-visible `UNSUPPORTED` coverage outcome without turning every external goroutine target into a warning. Standalone text/JSON/SARIF output now distinguishes a clean result, a clean result with unsupported goroutine targets listed by file:line and reason, and a result where nothing lifecycle-relevant was recognized at all — see `docs/limitations.md`.
3. Track cancellation and group ownership through selected structs and constructor returns instead of treating every store as final discharge.
4. Model WaitGroup count intervals and common `Add`/`Done` imbalance patterns.
5. **Partially done.** Add generated-file, path, and source-annotation suppressions with auditable reasons. Generated-file detection, glob-based `ignore_paths`, and inline `//lifeline:ignore` comments are implemented (`docs/limitations.md`). Not yet done: a required, auditable *reason* string attached to a suppression (today's `//lifeline:ignore LL1002 -- reason` free text after `--` is not parsed or validated, just conventional).
6. Add phase-separated timing and cancellation accounting for package discovery, parsing, type checking, model/SSA construction, recognition, and rendering.
7. **Started.** Add property tests for model invariants and differential tests against a compact trusted control-flow implementation. `tests/differential/cfg_ast_test.go` compares today's engine against `internal/cfg`'s reachability for the one known false-negative loop-scoping doesn't close, and holds it as a fixed regression fixture. No model-invariant property tests yet.
8. Expand corpus evaluation beyond `net/http`, including precision labels and unsupported-site counts.

## Optional feature expansion

1. **Partially done.** Signature-aware wrapper declarations with callback/result/receiver indices. `start_wrapper` callbacks and `context_wrapper` result roles (context vs. cancel function) are now resolved by static type rather than fixed position or explicit config, which covers the common cases without adding config surface; explicit receiver-index configuration is not implemented (see `docs/limitations.md` for what remains unconfigurable).
2. Rich editor fixes for context-select skeletons when insertion is structurally safe.
3. A persistent cache keyed by tool/fact versions, Go toolchain, build configuration, source/dependency digests, configuration digest, and backend mode.
4. A corpus runner that emits precision-review tables and minimized fixtures.
5. An optional full `golang.org/x/tools/go/ssa` backend for alias and call-graph experiments.
6. A formal backend interface when a second backend exists; avoid introducing an abstraction with only one implementation.

Correctness and trust tasks should be completed before broadening the default warning set.
