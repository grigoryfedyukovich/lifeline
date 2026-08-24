package mixedworker

// Run has two nested unconditional loops where only the inner one has an
// exit (a break). The outer loop never terminates. This is the exact
// "mixed resolution" pattern tests/differential/cfg_ast_test.go's
// TestFixed_NestedLoopsMixedResolution covers for a same-package/closure
// target; this fixture covers the same pattern for a cross-package go vet
// fact target, which needed a separate fix (Phase 4: the fact itself has
// to carry a CFG-derived verdict, not just the old flat booleans, or this
// regresses back to a false negative even after Phase 2 landed).
func Run() {
	for {
		for {
			break
		}
	}
}
