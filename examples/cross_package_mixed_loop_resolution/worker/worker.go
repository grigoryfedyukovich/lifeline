// Package worker holds the goroutine target for the
// cross_package_mixed_loop_resolution example, kept in its own package
// specifically so the example demonstrates a cross-package goroutine
// target rather than a same-package or closure one.
package worker

// Run has two nested unconditional loops where only the inner one has an
// exit (a break). The outer loop never terminates: this is the "mixed
// resolution" pattern (see tests/differential/cfg_ast_test.go's
// TestFixed_NestedLoopsMixedResolution for the same-package/closure case,
// and TestVetCrossPackageFactCatchesMixedResolutionLoop in
// tests/integration/integration_test.go for this cross-package case) that
// used to be a false negative even after the same-package fix landed: a
// go vet fact only carried a flat per-function boolean, and the inner
// loop's own break set that same flag, incorrectly clearing the outer
// loop's finding. Phase 4 of docs/cfg-migration-plan.md fixed it by
// exporting the fact's own CFG/SCC verdict instead of a flat boolean.
func Run() {
	for {
		for {
			break
		}
	}
}
