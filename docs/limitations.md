# Limitations

Lifeline 0.1.1 is intentionally conservative and bounded.

## Unsupported or approximate cases

- Indirect calls through interfaces, reflection, arbitrary function values, and generated dispatch are not resolved.
- Same-package direct goroutine targets are inspected only when they are within `max_functions`.
- Vet mode may use a compatible direct-function fact for a cross-package target. Standalone mode does not import those facts. As of this release this asymmetry is disclosed rather than silent: a standalone run that hits a cross-package target reports it under "goroutine target(s) could not be analyzed" in the clean-result summary (text) or `coverage.unsupported_targets` (JSON/SARIF), instead of indistinguishably reporting "no lifecycle diagnostics."
- Unsupported target bodies are retained as model evidence and are now surfaced in the report as coverage information (text: listed under a clean result; JSON/SARIF: `coverage.unsupported_targets`). They are informational, not diagnostics: they carry no rule ID, cannot match `-fail-on`, and are excluded from the pass/fail decision — a goroutine whose body could not be inspected is neither a finding nor proof of correctness.
- The coverage count (cancel bindings / goroutines / groups recognized) only reflects constructs the frontend actually modeled. A call to an unconfigured wrapper function (see `context_wrappers`/`start_wrappers`/`join_wrappers`/`stop_wrappers` above) never creates a binding to count in the first place, so it contributes nothing to this total and nothing to `unsupported_targets` either — there is no evidence trail for a construct the recognizer never matched syntactically. A nonzero coverage count in a function means *something* was checked; it does not mean *everything relevant* was checked.
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
