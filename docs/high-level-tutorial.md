# Lifeline — Very High-Level Tutorial

Lifeline is a **conservative static analyzer** for Go that helps you spot incomplete goroutine lifecycle protocols. It never claims to prove leaks or non-termination; it only reports local evidence that something important is missing.

## What it looks for (the four rules)

| ID     | Short name          | What is missing                                      |
|--------|---------------------|------------------------------------------------------|
| LL1001 | lost-cancel         | Cancellation function from `context.With*` is discarded or never called/transferred |
| LL1002 | goroutine-no-stop   | Goroutine with an unconditional loop has no recognized way to stop |
| LL1003 | waitgroup-no-wait   | `sync.WaitGroup` starts workers but never `Wait`s    |
| LL1004 | errgroup-no-wait    | `errgroup.Group` starts workers but never `Wait`s    |

There is also `LL9001` when analysis hits a configured bound (max functions / timeout).

## 30-second start

```bash
go build -o bin/lifeline ./cmd/lifeline
./bin/lifeline ./examples/ignored_context   # reports LL1002
./bin/lifeline ./examples/proper_context    # clean
```

Or use it as a `go vet` tool:

```bash
go vet -vettool=$(pwd)/bin/lifeline ./...
```

## Mental model

1. **Owner** creates a context / WaitGroup / errgroup and starts goroutines.
2. **Worker** should have a recognized stop path (select on `ctx.Done()`, channel close, break/return, or configured stop).
3. **Join** should happen before the owner returns (`Wait`, or ownership transfer).

Lifeline only looks inside the current package (plus a tiny amount of cross-package fact data when run under `go vet`). It is deliberately incomplete and safe to run in CI only when you opt in with `-fail-on`.

## Configuration (optional)

Drop a `lifeline.yaml` (or `.lifeline.yaml`, TOML, JSON) anywhere above the packages you analyze:

```yaml
schema_version: 1
format: text
fail_on: [LL1001, LL1002]
context_wrappers:
  - my/project/lifecycle.WithCancel
start_wrappers:
  - my/project/workers.Go
```

Unknown keys are rejected. See `lifeline.example.yaml` and the full tutorial in `docs/tutorial.md`.

## When to use it

- Code review of long-running workers and background loops.
- CI gate for the most common lifecycle mistakes (opt-in).
- Teaching / onboarding around context and join protocols.

## When not to use it

- As a completeness or termination proof.
- On code that relies on reflection, interfaces, or complex aliasing (those patterns are outside the model).

For the full walk-through with every example and CI recipe, continue to [docs/tutorial.md](tutorial.md).
