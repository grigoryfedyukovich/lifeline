# Roadmap

## Correctness and trust work

1. Replace the may-exit loop heuristic with a compact control-flow graph that distinguishes loop, switch, select, and labeled breaks.
2. Emit a low-noise user-visible `UNSUPPORTED` coverage outcome without turning every external goroutine target into a warning.
3. Track cancellation and group ownership through selected structs and constructor returns instead of treating every store as final discharge.
4. Model WaitGroup count intervals and common `Add`/`Done` imbalance patterns.
5. Add generated-file, path, and source-annotation suppressions with auditable reasons.
6. Add phase-separated timing and cancellation accounting for package discovery, parsing, type checking, model/SSA construction, recognition, and rendering.
7. Add property tests for model invariants and differential tests against a compact trusted control-flow implementation.
8. Expand corpus evaluation beyond `net/http`, including precision labels and unsupported-site counts.

## Optional feature expansion

1. Signature-aware wrapper declarations with callback/result/receiver indices.
2. Rich editor fixes for context-select skeletons when insertion is structurally safe.
3. A persistent cache keyed by tool/fact versions, Go toolchain, build configuration, source/dependency digests, configuration digest, and backend mode.
4. A corpus runner that emits precision-review tables and minimized fixtures.
5. An optional full `golang.org/x/tools/go/ssa` backend for alias and call-graph experiments.
6. A formal backend interface when a second backend exists; avoid introducing an abstraction with only one implementation.

Correctness and trust tasks should be completed before broadening the default warning set.
