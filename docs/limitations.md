# Limitations

Lifeline 0.1.1 is intentionally conservative and bounded.

## Unsupported or approximate cases

- Indirect calls through interfaces, reflection, arbitrary function values, and generated dispatch are not resolved.
- Same-package direct goroutine targets are inspected only when they are within `max_functions`.
- Vet mode may use a compatible direct-function fact for a cross-package target. Standalone mode does not import those facts.
- Unsupported target bodies are retained as model evidence, but no separate user-visible `UNSUPPORTED` diagnostic is emitted yet.
- Aliasing is shallow. Passing or storing a cancel/group value is treated as ownership transfer, not followed interprocedurally.
- A context passed to a called operation is accepted as delegated cancellation. The callee is not proved to honor it.
- Any explicit `break` in an unconditional-loop body is treated as a possible loop exit; reachability and break target are not proved precisely.
- Select analysis recognizes return-after-receive forms. Labeled control flow and more complex paths are approximate.
- Channel range is treated as a valid close protocol even when no local closer is identified.
- WaitGroup counts are qualitative. `Add(n)` records a start site rather than proving count balance.
- Configured wrappers use canonical names and fixed conventions; callback/result indices are not configurable.
- Cancellation bindings created inside anonymous functions are not promoted to separate top-level function records, although uses of enclosing bindings and nested goroutine starts are observed.
- Standalone `-tests` includes same-package test files but not external `package_name_test` packages. Vet mode receives normal Go compilation units.
- Suggested edits are emitted only for a blank cancel result in a short declaration.
- The local SSA-like summary has no phi nodes and is not a full control-flow SSA representation.

## False positives and negatives

A warning means the supported model found a missing protocol element. Code may still terminate through an unsupported mechanism. Conversely, accepting a context argument, break, return, or channel-close path is not proof that the path is reachable or correctly owned.

Prefer Lifeline as review evidence rather than an unsupervised correctness gate. CI failure is opt-in for this reason.

## Generated files and suppression

Version 0.1.1 does not automatically suppress generated files and has no path/annotation suppression. Use rule-level `ignore` or restrict package patterns. Auditable file/path suppression is planned.

## Resource and timing limits

Standalone root packages are analyzed concurrently, but parsing and type checking inside one package are synchronous. A deadline that expires during one of those phases is observed when the operation returns or the next cancellation boundary is reached.

End-to-end runtime is recorded in the evaluation document. Phase-separated parse/type-check/model/recognition/render timing is not yet emitted by the tool.
