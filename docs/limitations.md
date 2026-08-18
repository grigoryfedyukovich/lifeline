# Limitations

Lifeline 0.1.1 is intentionally conservative and bounded.

## Unsupported or approximate cases

- Indirect calls through interfaces, reflection, arbitrary function values, and generated dispatch are not resolved.
- Same-package direct goroutine targets are inspected only when they are within `max_functions`.
- Vet mode may use a compatible direct-function fact for a cross-package target. Standalone mode does not import those facts. As of this release this asymmetry is disclosed rather than silent: a standalone run that hits a cross-package target reports it under "goroutine target(s) could not be analyzed" in the clean-result summary (text) or `coverage.unsupported_targets` (JSON/SARIF), instead of indistinguishably reporting "no lifecycle diagnostics."
- Unsupported target bodies are retained as model evidence and are now surfaced in the report as coverage information (text: listed under a clean result; JSON/SARIF: `coverage.unsupported_targets`). They are informational, not diagnostics: they carry no rule ID, cannot match `-fail-on`, and are excluded from the pass/fail decision — a goroutine whose body could not be inspected is neither a finding nor proof of correctness.
- The coverage count (cancel bindings / goroutines / groups recognized) only reflects constructs the frontend actually modeled. A call to an unconfigured wrapper function (see `context_wrappers`/`start_wrappers`/`join_wrappers`/`stop_wrappers` above) never creates a binding to count in the first place, so it contributes nothing to this total and nothing to `unsupported_targets` either — there is no evidence trail for a construct the recognizer never matched syntactically. A nonzero coverage count in a function means *something* was checked; it does not mean *everything relevant* was checked.
- Aliasing is shallow. Passing or storing a cancel/group value is treated as ownership transfer, not followed interprocedurally.
- Loop-exit evidence (`break`, `return`, a select case, a stop-wrapper call, or a context passed to a called operation) is scoped to the specific unconditional loop it is lexically part of, using Go's own static break-target rules: an unlabeled `break` nested inside a further loop, `switch`, or `select` targets that construct, not the outer loop, so it no longer counts as evidence for the outer loop unless it is labeled to match it. This is lexical containment and Go's static scoping, not full reachability analysis: it does not prove the identified exit is reachable from every entry into the loop.
- This scoping is per unconditional loop, but a goroutine's overall "has an exit" status is still a single flag for the whole goroutine, not tracked per loop. If a goroutine contains two separate unconditional loops and only one of them has a recognized exit, that one loop's evidence currently suppresses `LL1002` for the goroutine as a whole, including the other, genuinely unresolved loop. Nested unconditional loops where only the inner one has a `break` are the concrete case this affects (e.g. `for { for { break } }`: the inner loop's exit is found, but the outer loop, which the inner `break` does not reach, is not independently flagged). Fixing this fully requires tracking exit evidence per loop rather than per goroutine, which is a larger model change than this release makes; it is called out here rather than fixed silently.
- A context passed to a called operation is accepted as delegated cancellation. The callee is not proved to honor it.
- Select analysis recognizes return-after-receive forms. Labeled `break`/`continue` targeting a loop are recognized when resolving loop exits (see above); other labeled control flow and more complex paths remain approximate.
- A bare (non-`select`) range over a channel is not treated as evidence that a different, unconditional loop elsewhere in the same body can terminate — closing that channel only ends its own range loop, not an unrelated one. It remains sufficient, as before, for a goroutine whose *only* loop is that range (there is no unconditional `for` loop to fire `LL1002` on in the first place).
- WaitGroup counts are qualitative. `Add(n)` records a start site rather than proving count balance.
- Configured wrappers use canonical names. A `start_wrapper`'s entry point is found among its arguments by type: an inline closure or a reference to a top-level declared function are both recognized, in either argument position, with no separate config needed to say where the callback is. A `context_wrapper`'s context and cancel-function results are identified by their static types rather than by position, so a wrapper may return them in either order and may return additional results (e.g. a trailing `error`) alongside them. `join_wrapper`/`stop_wrapper` matching was already independent of argument position. What remains unconfigurable: a function-typed value stored in a variable, struct field, or returned from another call (as opposed to a literal closure or a direct reference to a declaration) is not resolved, for either a `go` statement or a configured start wrapper — this is the same conservative boundary `buildGoroutine` already applies to plain `go` statements, not a gap specific to configuration.
- Cancellation bindings created inside anonymous functions are not promoted to separate top-level function records, although uses of enclosing bindings and nested goroutine starts are observed.
- Standalone `-tests` includes same-package test files but not external `package_name_test` packages. Vet mode receives normal Go compilation units.
- Suggested edits are emitted only for a blank cancel result in a short declaration.
- The local SSA-like summary has no phi nodes and is not a full control-flow SSA representation.

## False positives and negatives

A warning means the supported model found a missing protocol element. Code may still terminate through an unsupported mechanism. Conversely, accepting a context argument, break, return, or channel-close path is not proof that the path is reachable or correctly owned.

Prefer Lifeline as review evidence rather than an unsupervised correctness gate. CI failure is opt-in for this reason.

## Generated files and suppression

A file carrying the standard `// Code generated ... DO NOT EDIT.` marker (https://go.dev/s/generatedcode) is automatically excluded from analysis in both standalone and vet mode; it contributes no diagnostics and no coverage counts, as if it were not part of the package. A file can also be excluded by path via the `ignore_paths` config list, matched with `filepath.Match` semantics (no `**`; write a pattern like `*.pb.go` to match at any depth by basename rather than a directory-spanning pattern) against both the file's path relative to the working directory and its base filename.

A specific finding can be suppressed inline with a `//lifeline:ignore` comment. `//lifeline:ignore` alone suppresses every rule reported on that line; `//lifeline:ignore LL1001,LL1002` suppresses only the listed rules, and free text may follow (e.g. `//lifeline:ignore LL1002 -- see TICKET-123`). The comment is matched against the diagnostic's own reported line and against every evidence span's line, so it can go on whichever line most naturally names the construct — for `LL1002` in particular, that is usually the loop itself (e.g. `for { //lifeline:ignore`), which is a different line than the diagnostic's own reported position at the goroutine's start site. As before, rule-level `ignore` in config or restricting package patterns remain the coarser-grained options.

## Resource and timing limits

Standalone root packages are analyzed concurrently, but parsing and type checking inside one package are synchronous. A deadline that expires during one of those phases is observed when the operation returns or the next cancellation boundary is reached.

End-to-end runtime is recorded in the evaluation document. Phase-separated parse/type-check/model/recognition/render timing is not yet emitted by the tool.
