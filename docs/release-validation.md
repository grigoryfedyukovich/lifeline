# Release validation

**Release version:** 0.2.0

This document defines the release-facing validation contract for Lifeline. The
release identity is the exact contents of the root `VERSION` file, without a
leading `v`. Current source, documentation, changelog, CLI output, and
version-bearing golden output must agree with it.

Run the version-consistency check against a built CLI with:

```bash
./scripts/check_version_consistency.sh ./bin/lifeline
```

The check is intentionally strict. A release candidate is invalid if any
current-version reference diverges, even if the executable itself still builds.

Run the complete clean-clone release gate with:

```bash
./scripts/validate_release.sh
```

The release gate delegates its ordinary build/test checks to the same
`scripts/validate_ci.sh` entry point used by hosted CI and `make ci`. Before a
release run, the benchmark evidence validator can be checked without running
the full benchmark matrix:

```bash
./scripts/benchmark_release.sh --self-test
```
