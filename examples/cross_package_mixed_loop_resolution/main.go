// Package crosspackagemixedloopresolution starts a goroutine whose target
// lives in a different package (./worker) and whose body has the "mixed
// resolution" nested-loop shape: an outer loop with no exit of its own,
// wrapping an inner loop that does have one. See ./worker/worker.go for
// why this specific shape matters.
//
// Run this example through `go vet`, not the standalone lifeline binary
// directly, to see the diagnostic this is meant to demonstrate:
//
//	go vet -vettool=$(which lifeline) ./examples/cross_package_mixed_loop_resolution/...
//
// A plain `lifeline ./examples/cross_package_mixed_loop_resolution` looks
// at this package in isolation, with no cross-package fact information
// available at all, and will not recognize worker.Run's own loop shape
// from here (see docs/limitations.md and docs/cfg-migration-plan.md's
// Phase 4 entry for how a go vet fact closes that specific gap).
package crosspackagemixedloopresolution

import "github.com/gfedyukovich/lifeline/examples/cross_package_mixed_loop_resolution/worker"

func Start() {
	go worker.Run()
}
