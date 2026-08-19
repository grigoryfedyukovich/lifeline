package model

import "testing"

// linear: 0 -> 1 -> 2 (no cycles at all)
func linearCFG() *CFG {
	return &CFG{Entry: 0, Exit: 2, Blocks: []BasicBlock{
		{ID: 0, Successors: []Edge{{From: 0, To: 1, Kind: EdgeNormal}}, Predecessors: nil},
		{ID: 1, Successors: []Edge{{From: 1, To: 2, Kind: EdgeNormal}}, Predecessors: []BlockID{0}},
		{ID: 2, Predecessors: []BlockID{1}},
	}}
}

// selfLoop: 0 -> 1 -> 1 (1 loops back to itself, never reaches 2)
func selfLoopCFG() *CFG {
	return &CFG{Entry: 0, Exit: 2, Blocks: []BasicBlock{
		{ID: 0, Successors: []Edge{{From: 0, To: 1, Kind: EdgeNormal}}},
		{ID: 1, Successors: []Edge{{From: 1, To: 1, Kind: EdgeLoopBack}}, Predecessors: []BlockID{0, 1}},
		{ID: 2, Predecessors: nil},
	}}
}

// trappedTwoLoop and resolvedTwoLoop mirror the exact residual gap Phase 2
// exists to fix: 0 -> header -> {body, after}; body -> header (back-edge).
// trapped has no escape at all; resolved has one edge from the header to
// after (e.g. a break/context check) that reaches Exit.
func twoBlockLoopCFG(withEscape bool) *CFG {
	header := Edge{From: 1, To: 1, Kind: EdgeLoopBack} // placeholder, replaced below
	_ = header
	blocks := []BasicBlock{
		{ID: 0, Successors: []Edge{{From: 0, To: 1, Kind: EdgeNormal}}},
		{ID: 1},                             // header, successors set below
		{ID: 2},                             // body
		{ID: 3, Predecessors: []BlockID{1}}, // after / exit target
	}
	blocks[1].Successors = append(blocks[1].Successors, Edge{From: 1, To: 2, Kind: EdgeNormal})
	blocks[2].Predecessors = []BlockID{1}
	blocks[2].Successors = append(blocks[2].Successors, Edge{From: 2, To: 1, Kind: EdgeLoopBack})
	blocks[1].Predecessors = []BlockID{0, 2}
	if withEscape {
		blocks[1].Successors = append(blocks[1].Successors, Edge{From: 1, To: 3, Kind: EdgeFalse})
	}
	return &CFG{Entry: 0, Exit: 3, Blocks: blocks}
}

func TestReachable_Linear(t *testing.T) {
	g := linearCFG()
	got := g.Reachable(0)
	for _, want := range []BlockID{0, 1, 2} {
		if !got[want] {
			t.Fatalf("block %d should be reachable from Entry, got %v", want, got)
		}
	}
}

func TestReachable_SelfLoopDoesNotEscape(t *testing.T) {
	g := selfLoopCFG()
	got := g.Reachable(0)
	if got[2] {
		t.Fatalf("block 2 (Exit) should not be reachable: block 1 only loops to itself")
	}
	if !got[1] {
		t.Fatalf("block 1 should still be reachable from Entry")
	}
}

func TestCanReach_Linear(t *testing.T) {
	g := linearCFG()
	got := g.CanReach(2)
	for _, want := range []BlockID{0, 1, 2} {
		if !got[want] {
			t.Fatalf("block %d should be able to reach Exit, got %v", want, got)
		}
	}
}

func TestCanReach_SelfLoopCannotReachExit(t *testing.T) {
	g := selfLoopCFG()
	got := g.CanReach(2)
	if got[1] {
		t.Fatalf("block 1 should not be able to reach Exit: it only loops to itself")
	}
	if !got[2] {
		t.Fatalf("Exit should trivially be able to reach itself")
	}
}

func TestSCCs_SelfLoopFormsPersistentSingleton(t *testing.T) {
	g := selfLoopCFG()
	sccs := g.SCCs()
	found := false
	for _, scc := range sccs {
		if len(scc) == 1 && scc[0] == 1 {
			if !g.IsPersistentSCC(scc) {
				t.Fatalf("block 1's self-loop SCC should be persistent")
			}
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a singleton SCC containing block 1, got %v", sccs)
	}
	// Blocks 0 and 2 have no cycle through them at all.
	for _, id := range []BlockID{0, 2} {
		for _, scc := range sccs {
			if len(scc) == 1 && scc[0] == id && g.IsPersistentSCC(scc) {
				t.Fatalf("block %d has no cycle and should not be a persistent SCC", id)
			}
		}
	}
}

func TestSCCs_TwoBlockCycleIsOnePersistentSCC(t *testing.T) {
	g := twoBlockLoopCFG(false)
	sccs := g.SCCs()
	var cyclic []BlockID
	for _, scc := range sccs {
		if g.IsPersistentSCC(scc) {
			cyclic = scc
		}
	}
	if len(cyclic) != 2 {
		t.Fatalf("expected one persistent SCC of size 2 (header, body), got %v from sccs=%v", cyclic, sccs)
	}
	member := map[BlockID]bool{}
	for _, id := range cyclic {
		member[id] = true
	}
	if !member[1] || !member[2] {
		t.Fatalf("persistent SCC should be exactly {header=1, body=2}, got %v", cyclic)
	}
}

// TestPersistentSCCEscape is the core Phase 2 property, expressed directly
// on the graph algorithms rather than through the engine: a persistent SCC
// with no edge leaving it that reaches Exit is trapped; one with such an
// edge is resolved. This is the primitive internal/engine's new LL1002
// check is built on.
func TestPersistentSCCEscape(t *testing.T) {
	for _, tc := range []struct {
		name        string
		withEscape  bool
		wantTrapped bool
	}{
		{"no escape edge at all -> trapped", false, true},
		{"one escape edge reaching Exit -> resolved", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := twoBlockLoopCFG(tc.withEscape)
			canExit := g.CanReach(g.Exit)
			var scc []BlockID
			for _, s := range g.SCCs() {
				if g.IsPersistentSCC(s) {
					scc = s
				}
			}
			if len(scc) == 0 {
				t.Fatalf("expected to find the persistent SCC")
			}
			member := map[BlockID]bool{}
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
			if resolved == tc.wantTrapped {
				t.Fatalf("resolved = %v, want trapped = %v", resolved, tc.wantTrapped)
			}
		})
	}
}
