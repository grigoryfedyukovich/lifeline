# Semantics and trust boundary

## Result classes

Lifeline emits:

- `WARNING`: the modeled program contains a recognized lifecycle protocol with a missing element;
- `UNKNOWN`: a configured function bound or standalone timeout prevents a complete pass.

Unsupported direct targets are explicit in the internal model and are never counted as termination evidence. Version 0.1.1 does not yet emit a separate user-visible unsupported verdict; that visibility gap is tracked in the validated specification and roadmap.

No diagnostic is a proof that a goroutine leaks or fails to terminate. Evidence says that a local, supported stop/join protocol was not found.

## Modeled program

The frontend type-checks build-selected Go files and lowers lifecycle-relevant syntax to a parser-independent model. It also creates a deterministic local SSA-like summary that versions local definitions and records assignments, calls, goroutine starts, defers, loops, selects, returns, and canonical callees.

The analysis engine receives only:

- source spans;
- SSA-like neutral instructions;
- context factories and ownership observations;
- goroutine starts and bounded termination evidence;
- WaitGroup/errgroup starts, joins, and ownership transfers.

It does not retain AST node identities.

## Function boundaries

A named function and every function literal have distinct lifecycle bodies. Loops, returns, breaks, select exits, and stop calls inside a nested literal do not alter the enclosing body's lifecycle summary.

Nested `go` statements are still discovered and modeled as separate start sites. References to an outer cancel function or group from a nested closure remain references to the same typed object and may discharge or transfer the outer obligation.

## Context ownership

For the standard factories:

```text
context.WithCancel
context.WithCancelCause
context.WithDeadline
context.WithDeadlineCause
context.WithTimeout
context.WithTimeoutCause
```

Lifeline models the second result as a cancellation obligation. The obligation is discharged when the cancel function is:

- called, including in a `defer`;
- returned to the caller;
- passed as an argument;
- assigned to another named value or field;
- stored in a composite value.

Passing is intentionally conservative: Lifeline assumes ownership may transfer and does not inspect whether the receiving callee eventually invokes the cancel function.

Assigning the result to `_` is an immediate lost-cancel diagnostic.

## Goroutine termination evidence

`LL1002` is considered only for a modeled body containing `for { ... }`. The following are accepted as may-terminate evidence:

- a `return`;
- an explicit `break` path;
- a range over a channel;
- a select case that receives from `ctx.Done()` and returns;
- a select case that receives from a channel and returns;
- a context passed to another called operation;
- a configured stop operation.

Accepting an exit path suppresses a warning; Lifeline does not prove reachability. A stop path in a nested function does not suppress a warning for an enclosing loop.

## Join ownership

A local `sync.WaitGroup` becomes join-relevant after `Add` or `Go`. A local `errgroup.Group` becomes join-relevant after `Go`. The obligation is discharged by `Wait`, a configured join wrapper, or an observed ownership transfer.

Multi-value assignments are interpreted positionally when mapping a tracked value to its destination. A blank identifier in an unrelated assignment position does not suppress a transfer.

## Cross-package facts

Vet mode exports a versioned fact for each analyzed named function and imports it only for a direct cross-package goroutine target. The summary is deliberately small and contains no source body or opaque score. An incompatible fact version is ignored.

Standalone mode does not persist or import these vet facts. It still inspects direct same-package targets within the configured function bound.

## Bounds

The explicit bounds are:

- `max_functions` per package;
- the standalone command timeout.

`max_functions` is applied before lifecycle and SSA-like construction. Direct same-package target inspection cannot bypass it. Every diagnostic repeats the bound values. Reaching the function bound or timeout emits `LL9001`; JSON reports are marked incomplete.
