# Evaluation record

## Environment

- Tool: Lifeline 0.1.1
- Backend: `local-ast-types-ssa-summary/v2`
- Go toolchain: Go 1.23.2, linux/amd64
- Date: 2026-07-17

## Small example

Command:

```bash
/usr/bin/time -f 'elapsed=%e maxrss=%MKB' \
  ./bin/lifeline ./examples/ignored_context
```

Warm Go cache result:

```text
elapsed: 0.34 s
maximum resident set: 38,000 KB
expected diagnostic: LL1002
```

## Go standard library: `net/http`

Command:

```bash
/usr/bin/time -f 'elapsed=%e maxrss=%MKB' \
  ./bin/lifeline -timeout 30s -format json net/http
```

Warm Go cache result:

```text
diagnostics: 0
incomplete: false
elapsed: 0.86 s
maximum resident set: 71,068 KB
```

A cold export/build cache run in the same environment took 9.38 seconds and 202,436 KB. The difference is dominated by Go package/export preparation, which is why the performance target and comparison record explicitly distinguish warm-cache behavior.

This is a smoke/precision evaluation, not evidence of complete lifecycle correctness in `net/http`.

During initial development, provisional findings exposed missing ownership-transfer and break recognition. The frontend was corrected to model those patterns; no corpus-specific suppressions or names were added. The July 2026 external review then produced minimized regressions for nested goroutine boundaries, positional assignments, context type identity, function limits, and facts.

## Included examples and regressions

| Area | Expected result |
|---|---|
| ignored context | `LL1002` |
| lost/uncalled cancellation | `LL1001` |
| unjoined WaitGroup | `LL1003` |
| unjoined errgroup | `LL1004` |
| proper context/cancel/channel/group protocols | no warning |
| configured wrappers | configured behavior only when config is supplied |
| nested goroutine loop | warning attributed only to inner start |
| nested stop path | cannot suppress outer loop warning |
| direct cross-package target in vet mode | imported versioned fact supports `LL1002` |

Golden tests compare complete text output. JSON and SARIF tests verify schemas and rule fields. The entire repository passes the race detector.

## Portability validation

- Linux amd64 binary built and executed.
- macOS amd64 binary cross-built.
- macOS arm64 binary cross-built.

Cross-building validates compilation, not runtime behavior on a macOS host.

## Next corpus work

A broader precision study should sample several server/worker repositories, classify each finding independently, record unsupported targets, and retain minimized regression fixtures. Runtime should be split into package discovery, parse/type-check, model/SSA construction, recognition, and rendering phases.
