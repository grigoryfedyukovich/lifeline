package model

import (
	"testing"
	"time"
)

func timeoutChan() <-chan time.Time {
	return time.After(2 * time.Second)
}

// orJoin is the simplest possible non-trivial join-semilattice: false is
// bottom, true is top, join is boolean OR. Used to test Solve's mechanics
// in isolation from the more complex WorkerState lattice.
func orJoin(a, b bool) bool { return a || b }

// linear: 0(entry) -> 1 -> 2(exit), no branches at all.
func linearGraph() *CFG {
	return &CFG{Entry: 0, Exit: 2, Blocks: []BasicBlock{
		{ID: 0, Successors: []Edge{{From: 0, To: 1}}},
		{ID: 1, Successors: []Edge{{From: 1, To: 2}}, Predecessors: []BlockID{0}},
		{ID: 2, Predecessors: []BlockID{1}},
	}}
}

// diamond: 0 -> {1, 2} -> 3. A branch and a merge, no cycle.
func diamondGraph() *CFG {
	return &CFG{Entry: 0, Exit: 3, Blocks: []BasicBlock{
		{ID: 0, Successors: []Edge{{From: 0, To: 1}, {From: 0, To: 2}}},
		{ID: 1, Successors: []Edge{{From: 1, To: 3}}, Predecessors: []BlockID{0}},
		{ID: 2, Successors: []Edge{{From: 2, To: 3}}, Predecessors: []BlockID{0}},
		{ID: 3, Predecessors: []BlockID{1, 2}},
	}}
}

// loopWithFactInBody: 0(entry) -> 1(header) -> 2(body) -> 1(back-edge);
// 1 -> 3(exit) also. A fact is set true only inside block 2 (the body);
// Solve must propagate it back around through the loop-back edge so that
// block 1's in-state reflects it on the second time around, and therefore
// so does the eventual exit at block 3 -- unless the fact is set on the
// very first pass through 2, a single forward pass without re-visiting
// would never see it feed back into 1 before 1 is (wrongly) considered
// final only counting the first, fact-free arrival from 0.
func loopWithFactInBody() *CFG {
	return &CFG{Entry: 0, Exit: 3, Blocks: []BasicBlock{
		{ID: 0, Successors: []Edge{{From: 0, To: 1}}},
		{ID: 1, Successors: []Edge{{From: 1, To: 2}, {From: 1, To: 3}}, Predecessors: []BlockID{0, 2}},
		{ID: 2, Successors: []Edge{{From: 2, To: 1}}, Predecessors: []BlockID{1}},
		{ID: 3, Predecessors: []BlockID{1}},
	}}
}

func TestSolve_LinearPropagatesForward(t *testing.T) {
	g := linearGraph()
	setAt := BlockID(1)
	transfer := func(id BlockID, in bool) bool {
		if id == setAt {
			return true
		}
		return in
	}
	out := Solve(g, false, false, transfer, orJoin)
	if out[0] {
		t.Fatalf("block 0 (before the fact is set) should be false")
	}
	if !out[1] || !out[2] {
		t.Fatalf("blocks 1 and 2 (at and after the fact) should be true: out=%v", out)
	}
}

func TestSolve_DiamondJoinsBothBranches(t *testing.T) {
	g := diamondGraph()
	// Set the fact only on the left branch (block 1), not the right (block 2).
	transfer := func(id BlockID, in bool) bool {
		if id == 1 {
			return true
		}
		return in
	}
	out := Solve(g, false, false, transfer, orJoin)
	if !out[1] {
		t.Fatalf("block 1 should be true: it's set there directly")
	}
	if out[2] {
		t.Fatalf("block 2 should be false: the fact was never set on this branch")
	}
	if !out[3] {
		t.Fatalf("block 3 (the merge) should be true: OR-join means true from either predecessor propagates, out=%v", out)
	}
}

// TestSolve_LoopPropagatesFactAroundBackEdge is the core property that
// justifies needing an iterative worklist solver instead of a single
// forward pass: a fact set inside a loop body must be visible at the
// header (and beyond) once the loop-back edge carries it around, which
// requires revisiting the header after the body has run at least once.
func TestSolve_LoopPropagatesFactAroundBackEdge(t *testing.T) {
	g := loopWithFactInBody()
	transfer := func(id BlockID, in bool) bool {
		if id == 2 { // the loop body always sets the fact
			return true
		}
		return in
	}
	out := Solve(g, false, false, transfer, orJoin)
	if out[0] {
		t.Fatalf("block 0 should be false: it runs before the loop at all")
	}
	if !out[1] {
		t.Fatalf("block 1 (the header) should be true once the back-edge carries the fact around, out=%v", out)
	}
	if !out[3] {
		t.Fatalf("block 3 (exit, reached from the header after the fact is visible there) should be true, out=%v", out)
	}
}

// TestSolve_TerminatesOnMonotoneLattice guards against an infinite loop in
// the solver itself: a bounded, monotone lattice (bool, OR) must reach a
// fixed point in a bounded number of steps regardless of how many blocks
// or back-edges the graph has. This doesn't assert a specific iteration
// count (an implementation detail); it just asserts Solve returns at all,
// which a broken worklist (e.g. one that always re-queues regardless of
// whether the state actually changed) would fail to do.
func TestSolve_TerminatesOnMonotoneLattice(t *testing.T) {
	g := loopWithFactInBody()
	done := make(chan map[BlockID]bool, 1)
	go func() {
		done <- Solve(g, false, false, func(id BlockID, in bool) bool { return in || id == 2 }, orJoin)
	}()
	select {
	case out := <-done:
		if len(out) != len(g.Blocks) {
			t.Fatalf("expected a result for every block, got %d of %d", len(out), len(g.Blocks))
		}
	case <-timeoutChan():
		t.Fatalf("Solve did not terminate within the test timeout")
	}
}
