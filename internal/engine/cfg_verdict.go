package engine

import (
	"github.com/gfedyukovich/lifeline/internal/model"
)

// unresolvedLoop is the CFG/SCC replacement for the old flat "InfiniteLoop
// && no evidence booleans anywhere in the body" check: it reports whether
// g's control-flow graph contains a reachable, persistent (cyclic)
// strongly connected component with no edge leaving it that can reach the
// function's Exit block.
//
// This closes a real gap the flat check had, even after loop-scoping
// (see docs/limitations.md): evidence found anywhere in a goroutine body
// could suppress a finding about a completely unrelated loop in the same
// body, because "has an exit" was tracked as one flag for the whole
// goroutine. Here, each persistent SCC is judged only by whether IT has an
// escape edge reaching Exit, so a goroutine with two separate unconditional
// loops where only one has its own exit is correctly still flagged for the
// other. tests/differential/cfg_ast_test.go holds this exact case as a
// fixture, verified against both this function directly and the full
// engine.
//
// A block with no cycle through it is technically its own trivial,
// non-persistent SCC (see model.CFG.IsPersistentSCC); only genuine cycles
// are considered here, matching the existing rule's scope of "unconditional
// loop", not "any code path that fails to reach Exit" (a bare `select{}`
// with no loop at all, for instance, is not something this rule has ever
// flagged, and still isn't).
func unresolvedLoop(g *model.CFG) bool {
	if g == nil {
		return false
	}
	reach := g.Reachable(g.Entry)
	canExit := g.CanReach(g.Exit)
	for _, scc := range g.SCCs() {
		if !g.IsPersistentSCC(scc) {
			continue
		}
		if !reach[scc[0]] {
			continue // dead code; not reachable from the goroutine's own entry
		}
		member := make(map[model.BlockID]bool, len(scc))
		for _, id := range scc {
			member[id] = true
		}
		resolved := false
		for _, id := range scc {
			for _, e := range g.Block(id).Successors {
				if !member[e.To] && canExit[e.To] {
					resolved = true
				}
			}
		}
		if !resolved {
			return true
		}
	}
	return false
}
