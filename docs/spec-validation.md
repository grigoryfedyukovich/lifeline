# Original specification validation

**Validated against:** [`specification-original.md`](specification-original.md)  
**Implementation:** Lifeline 0.1.1  
**Validation date:** 2026-07-17

Status meanings:

- **Implemented** — present in code and covered by documentation/tests.
- **Partial** — the central behavior exists, but a stated detail remains incomplete.
- **Not applicable** — the condition does not arise in the current backend.
- **Planned** — intentionally deferred and listed in the roadmap.

## Requirement matrix

| Original requirement | Status | Evidence or qualification |
|---|---|---|
| `go/analysis` goroutine lifecycle checker | Implemented | `analyzer.New`, `unitchecker`, standalone driver |
| Context, channel, WaitGroup, errgroup, explicit stop recognizers | Implemented | `LL1001`–`LL1004`, wrapper configuration, frontend regression tests |
| Evidence and uncertainty | Implemented | Evidence records, assumptions, bounds, `WARNING`/`UNKNOWN` |
| `go vet`, editors, tests, SARIF | Implemented | Vettool integration, `go/analysis` API, integration suite, SARIF 2.1.0 |
| Do not prove termination or flag every goroutine | Implemented | `LL1002` requires an unconditional loop; limitations are explicit |
| Go packages plus project wrapper config | Implemented | Go package patterns; strict JSON/YAML/TOML config |
| Source diagnostics, safe fixes, analysis facts | Implemented | Text/JSON/SARIF, narrow lost-cancel edit, versioned function object facts |
| Standalone exit 0 despite findings | Implemented | Default policy test |
| Configurable CI failure code | Implemented | `-fail-on`, `-ci-exit-code` |
| Distinct invalid/internal exit codes | Implemented | Exit 2 and 3 integration behavior |
| Diagnostics state protocol and missing element | Implemented | Stable rule contract and protocol fields |
| Declarative unknown wrappers | Implemented | Context/start/join/stop wrapper lists |
| Versioned cross-package summaries | Implemented | Fact version 2, object-fact import/export, vet integration fixture |
| Fixes only when obvious | Implemented | Only blank cancel in short declaration receives an edit |
| SSA construction | Partial | A deterministic lifecycle-focused SSA-like summary is built and retained; full Go SSA/CFG is intentionally not claimed |
| Language-neutral internal model | Implemented | Engine has no AST/types dependency; model uses spans and neutral records |
| Narrow backend interface/fake backend | Partial | Package boundaries are narrow and model tests are deterministic, but there is no formal pluggable backend interface yet |
| Content-addressed persistent cache | Not applicable | Lifeline owns no persistent cache in 0.1.1; cache-key requirements are recorded before any cache is added |
| Exact modeled/abstracted semantics | Implemented | `docs/semantics.md`, `docs/limitations.md`, current specification |
| Every bounded result prints its bound | Implemented | Text/JSON diagnostics include `max_functions` and timeout |
| Solver evidence replay | Not applicable | No solver backend is used |
| `UNKNOWN` first-class | Implemented | `LL9001`, bundle `incomplete` |
| `UNSUPPORTED` first-class | Partial | Unsupported targets are explicit model evidence and never treated as proof, but no separate user-visible verdict is emitted yet |
| Tool/backend versions in reports | Implemented | JSON diagnostics/bundle, text model line, SARIF driver |
| Three original running examples | Implemented | Golden and tutorial examples run in CI |
| YAML/TOML plus CLI override | Implemented | Also supports strict JSON |
| Unknown config keys error | Implemented | Decoder/config tests |
| Effective config printing | Implemented | `-print-config` |
| Syntax/type spans and hints | Implemented | Standalone loader error paths |
| Timeout produces `UNKNOWN` | Implemented | `LL9001` timeout path |
| Internal crash report with reproduction, no upload | Implemented | Panic boundary in standalone main |
| Partial results labeled incomplete | Implemented | `LL9001` and JSON bundle flag |
| Startup below one second for small examples | Implemented in recorded environment | Warm-cache example: ~0.34 s |
| Typical examples below five seconds | Implemented in recorded environment | Integration examples pass comfortably |
| Memory below 512 MB | Implemented in recorded environment | 38 MB small example; 71 MB warm `net/http` |
| Configurable timeout and size/state limit | Implemented by adaptation | Timeout plus deterministic `max_functions`; no formulas/states exist in this backend |
| Phase-separated performance reports | Planned | End-to-end evaluation exists; phase instrumentation remains roadmap work |
| No network required | Implemented | Vendored dependencies; local Go toolchain only |
| Source remains local | Implemented | No upload/integration path exists |
| Avoid unrelated source text in reports | Implemented | Locations and concise evidence only |
| Subprocess argument arrays | Implemented | `exec.CommandContext` with explicit args |
| M1 local context/channel | Implemented | Rules and examples |
| M2 WaitGroup/errgroup and SSA | Implemented | Rules, tests, retained SSA-like model |
| M3 cross-package facts and wrappers | Implemented in vet mode | Versioned direct-function facts plus all wrapper classes |
| M4 fixes and corpus evaluation | Implemented at MVP depth | Safe cancel fix and `net/http` smoke evaluation |
| Polished workflow, examples, semantic boundary | Implemented | README, tutorial, architecture, semantics, limitations |
| Reproducible tests | Implemented | Unit, regression, golden, integration, race tests |
| One real-world evaluation | Implemented | `net/http` record |
| Correctness vs optional issue list | Implemented | `docs/roadmap.md` |
| Property tests | Planned | No algebraic/property generator yet |
| Differential tests | Planned | No trusted comparison backend yet |
| Multi-repository corpus tests | Partial | One standard-library corpus smoke test; broader study planned |
| Unsupported/unknown never shown as proof | Implemented | Neither is converted to a successful proof claim; unsupported visibility remains partial as noted above |
| Linux and macOS builds | Implemented | Linux binary executed; darwin amd64 and arm64 cross-built |
| Two-minute demo and honest limitations | Implemented | README and limitations documentation |

## Overall conclusion

The repository is portfolio-ready under the original definition of done and substantially conforms to the functional brief. The remaining specification debt is explicit rather than silently claimed:

1. emit a user-visible, low-noise `UNSUPPORTED` coverage outcome;
2. add phase-separated performance instrumentation;
3. add property/differential tests and a broader corpus study;
4. introduce a formal backend interface only when a second backend exists.

These items do not invalidate the current warning rules, but they should be completed before describing Lifeline as a comprehensive interprocedural lifecycle verifier.
