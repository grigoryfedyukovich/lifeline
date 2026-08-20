package engine

import (
	"github.com/gfedyukovich/lifeline/internal/model"
)

// TerminationFacts summarizes what the SCC-based analysis found for a
// single goroutine's CFG: whether Exit is reachable at all, and which
// persistent (cyclic) strongly-connected components exist, each with its
// own resolved/trapped status. This is the same information
// UnresolvedLoop's verdict is derived from, exposed in a form suitable for
// -dump facts (docs/cfg-migration-plan.md section 8.2) rather than folded
// straight into a single bool.
type TerminationFacts struct {
	ExitReachable  bool
	PersistentSCCs []SCCFacts
}

// SCCFacts describes one persistent (cyclic) strongly-connected component:
// which blocks it contains and whether it has an edge leaving it that can
// reach the function's exit.
type SCCFacts struct {
	Blocks   []model.BlockID
	Resolved bool
}

// SummarizeTermination computes TerminationFacts for g: for every
// reachable, persistent (cyclic) strongly-connected component, whether it
// has an edge leaving it that can reach the function's exit. A block with
// no cycle through it is technically its own trivial, non-persistent SCC
// (see model.CFG.IsPersistentSCC); only genuine cycles are considered
// here, matching LL1002's existing scope of "unconditional loop", not "any
// code path that fails to reach Exit" (a bare `select{}` with no loop at
// all, for instance, is not something this rule has ever flagged, and
// still isn't).
func SummarizeTermination(g *model.CFG) TerminationFacts {
	if g == nil {
		return TerminationFacts{}
	}
	reach := g.Reachable(g.Entry)
	canExit := g.CanReach(g.Exit)
	facts := TerminationFacts{ExitReachable: reach[g.Exit]}
	for _, scc := range g.SCCs() {
		if !g.IsPersistentSCC(scc) || !reach[scc[0]] {
			continue // not a real cycle, or dead code not reachable from entry
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
		facts.PersistentSCCs = append(facts.PersistentSCCs, SCCFacts{Blocks: scc, Resolved: resolved})
	}
	return facts
}

// UnresolvedLoop is the CFG/SCC replacement for the old flat "InfiniteLoop
// && no evidence booleans anywhere in the body" check: it reports whether
// g's control-flow graph contains a reachable, persistent (cyclic)
// strongly connected component with no edge leaving it that can reach the
// function's Exit block -- i.e. whether SummarizeTermination found any
// unresolved component at all.
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
// Exported so analyzer.go's cross-package fact export can compute exactly
// the same verdict for a function's own body that this file computes for a
// same-package or closure goroutine target, rather than duplicating the
// logic (Phase 4, docs/cfg-migration-plan.md).
func UnresolvedLoop(g *model.CFG) bool {
	for _, scc := range SummarizeTermination(g).PersistentSCCs {
		if !scc.Resolved {
			return true
		}
	}
	return false
}
