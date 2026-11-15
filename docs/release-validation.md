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

## Clean-clone release gate

Run:

```bash
./scripts/validate_release.sh
```

The gate requires the invoking checkout to be clean, records committed `HEAD`,
clones that exact commit into a temporary directory, runs the complete local CI
suite there, executes the documented quick start, creates a source package with
`git archive`, validates every package path against
`release/package-allowlist.txt`, and confirms the clone is still clean.

Because packaging is based on committed `HEAD`, unrelated untracked files and
machine-local evidence cannot enter the source archive. There is no dirty-tree
escape hatch analogous to `--allow-dirty`.
