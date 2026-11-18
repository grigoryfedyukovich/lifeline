# Release performance claims

Lifeline does not currently record a normative release performance claim in
source control. Historical smoke measurements in `docs/evaluation.md` remain
historical observations, not release claims.

Any future release-facing performance claim must use the exact structured block
below so `scripts/check_performance_claims.sh` can reject incomplete claims:

```text
<!-- lifeline-performance-claim-template
commit: 0123456789abcdef0123456789abcdef01234567
platform: linux/amd64; CPU model and relevant runner class
workload: exact benchmark/corpus and command identity
repetitions: 5
median: 123 ns/op
p95: 145 ns/op
-->
```

Raw samples, machine-local paths, and generated evidence stay outside Git. The
release may attach the corresponding benchmark `metadata.json`, `summary.tsv`,
and raw output as CI/release artifacts.
