# AST/CFG migration — implementation phase index

**Status:** This is a compact index reconstructed from the implementation, [architecture](architecture.md), [roadmap](roadmap.md), and [validated specification](specification.md) in this distribution. It replaces references to a more detailed historical `cfg-migration-plan.md` that was absent from the supplied archive; it is **not** a recovered copy of that original plan. The actual implementation and tests remain authoritative.

## Phase 1–2 — CFG foundations and persistent loops

- `internal/cfg` models control-flow edges and reachable paths, including labeled breaks and structured control flow.
- `internal/engine/cfg_verdict.go` uses reachability and strongly connected components to distinguish persistent loop components from possible exits for `LL1002`.
- Inspect structural CFGs with `lifeline -dump cfg`.

## Phase 3 — Dataflow infrastructure

- `internal/model.Solve` provides a forward worklist solver over the CFG.
- `internal/engine/dataflow_lattices.go` defines `StopCapability`, `JoinObligation`, and `Ownership` lattices. These are **not** interchangeable with the rule-specific soundness conditions.
- The available stop-capability summary is exposed for inspection. Join/ownership dataflow results are not claimed as authoritative rule verdicts.

## Phase 4 — Cross-package loop summaries

- Versioned function facts carry the exported `LoopUnresolved` result for direct cross-package workers in vet mode.
- Standalone loading computes compatible in-memory dependency summaries when source is available; unavailable summaries remain unsupported.

## Phase 5 — Parameter and constructor ownership

- `internal/frontend.computeParameterEffects` propagates a supported subset of direct-function consumption/ownership effects.
- Constructor-return and field ownership handling retains evidence boundaries rather than treating opaque escape as proven local consumption.

## Phase 6 — Join protocol checks and balance

- `internal/frontend.computeGroupBalances` accounts for recognized WaitGroup `Add`/`Done` patterns using bounded positive evidence.
- CFG checks distinguish a join that skips some owner-return paths, and a join before workers' own stop source is consumed (`LL1005`).
- These checks have their own recognition conditions; they must not be inferred solely from generic `JoinObligation` lattice states.

## 8.2 — Inspection/debug facts

Use `lifeline -dump facts [-dump-format text|json] ./...` to inspect per-worker stop and termination summaries, including exit reachability and persistent-component outcomes. A join entry marked not analyzed means the debug dataflow view has not established that fact; it does not override rule-specific join recognition. See [architecture](architecture.md) for the current field meanings.

## Current work and validation

The [roadmap](roadmap.md) records follow-up opportunities, and [limitations](limitations.md) describes unsupported cases. The [evaluation record](evaluation.md) separates historical measured results from work not yet reproducibly verified from the source distribution.
