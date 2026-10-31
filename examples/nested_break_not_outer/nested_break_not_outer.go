// Package nested_break_not_outer has an outer loop with no exit of its own
// wrapping an inner loop whose break only leaves the inner loop. This is also
// the same-package "mixed resolution" shape (see TestFixed_NestedLoopsMixedResolution
// in tests/differential/cfg_ast_test.go).
package nested_break_not_outer

func Start() {
	go func() {
		for {
			for {
				break
			}
		}
	}()
}
