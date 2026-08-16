# Lifeline examples

All example directories are ordinary Go packages. Build Lifeline once from the repository root:

```bash
go build -o ./bin/lifeline ./cmd/lifeline
```

Then run any row below. A clean example prints `no lifecycle diagnostics`.

| Example | Expected result | Demonstrates |
|---|---|---|
| `ignored_context` | `LL1002` | A looping worker ignores an available `context.Context`. |
| `proper_context` | clean | A `select` returns after `ctx.Done()`. |
| `lost_cancel` | `LL1001` | A standard context cancellation function is discarded. |
| `uncalled_cancel` | `LL1001` | A named cancellation function is never called or transferred. |
| `proper_cancel` | clean | The owner immediately defers its cancellation function. |
| `unjoined_waitgroup` | `LL1003` | A `sync.WaitGroup` starts accounting but is never waited. |
| `proper_waitgroup` | clean | The owner calls `Wait` before returning. |
| `unjoined_errgroup` | `LL1004` | An `errgroup.Group` starts work but is never waited. |
| `proper_errgroup` | clean | `errgroup.WithContext`, `Go`, and `Wait` form a complete protocol. |
| `channel_shutdown` | clean | A worker exits when its input channel closes. |
| `custom_context_wrapper` | `LL1001` with its config | A project context factory is declared in configuration. |
| `custom_start_wrapper` | `LL1002` with its config | A project goroutine launcher is declared in configuration. |

## Run the diagnostic examples

```bash
./bin/lifeline ./examples/ignored_context
./bin/lifeline ./examples/lost_cancel
./bin/lifeline ./examples/uncalled_cancel
./bin/lifeline ./examples/unjoined_waitgroup
./bin/lifeline ./examples/unjoined_errgroup
```

## Run the clean examples

```bash
./bin/lifeline ./examples/proper_context
./bin/lifeline ./examples/proper_cancel
./bin/lifeline ./examples/proper_waitgroup
./bin/lifeline ./examples/proper_errgroup
./bin/lifeline ./examples/channel_shutdown
```

## Run the configured-wrapper example

Without configuration, `ProjectWithCancel` is an unknown factory and Lifeline makes no claim:

```bash
./bin/lifeline ./examples/custom_context_wrapper
```

With the example configuration, the wrapper creates a cancellation obligation and the discarded result is reported:

```bash
./bin/lifeline \
  -config ./examples/custom_context_wrapper/lifeline.yaml \
  ./examples/custom_context_wrapper
```

The contrast is intentional: unknown wrappers are not guessed.

A custom goroutine launcher works the same way. Without configuration, Lifeline does not inspect the function passed to `Launch`:

```bash
./bin/lifeline ./examples/custom_start_wrapper
```

Declare `Launch` as a worker-start wrapper and rerun:

```bash
./bin/lifeline \
  -config ./examples/custom_start_wrapper/lifeline.yaml \
  ./examples/custom_start_wrapper
```

Lifeline now inspects the supplied function literal and reports its unconditional loop as `LL1002`.
