package engine

import (
	"testing"

	"github.com/gfedyukovich/lifeline/internal/model"
)

// selectLoopCFG mirrors a select-loop worker: header -> body, body has two
// comm-case branches -- one returns (a real stop point), the other does
// work and loops back (not a stop point on its own, even though the
// overall function does eventually terminate via the sibling branch).
func selectLoopCFG() *model.CFG {
	return &model.CFG{Entry: 0, Exit: 5, Blocks: []model.BasicBlock{
		{ID: 0, Successors: []model.Edge{{From: 0, To: 1, Kind: model.EdgeNormal}}},
		{ID: 1, Successors: []model.Edge{{From: 1, To: 2, Kind: model.EdgeCase}, {From: 1, To: 3, Kind: model.EdgeCase}}, Predecessors: []model.BlockID{0, 4}},
		{ID: 2, Kind: "comm-case", Successors: []model.Edge{{From: 2, To: 5, Kind: model.EdgeReturn}}, Predecessors: []model.BlockID{1}}, // ctx.Done(): return
		{ID: 3, Kind: "comm-case", Successors: []model.Edge{{From: 3, To: 4, Kind: model.EdgeNormal}}, Predecessors: []model.BlockID{1}}, // jobs: work()
		{ID: 4, Successors: []model.Edge{{From: 4, To: 1, Kind: model.EdgeLoopBack}}, Predecessors: []model.BlockID{3}},
		{ID: 5, Predecessors: []model.BlockID{2}},
	}}
}

func TestStopCapabilityDataflow_NoContext(t *testing.T) {
	g := selectLoopCFG()
	out := StopCapabilityDataflow(g, false)
	for id, s := range out {
		if s != StopNone {
			t.Fatalf("block %d: expected StopNone when hasContext=false, got %v", id, s)
		}
	}
}

func TestStopCapabilityDataflow_ConsumingBranchIsProvenConsumed(t *testing.T) {
	g := selectLoopCFG()
	out := StopCapabilityDataflow(g, true)
	if out[2] != StopProvenConsumed {
		t.Fatalf("block 2 (the ctx.Done()-and-return branch) should be StopProvenConsumed, got %v", out[2])
	}
}

// TestStopCapabilityDataflow_SiblingBranchIsNotProvenConsumed is the
// precision check that motivated using local edge checks instead of
// global CanReach: the "jobs" branch (block 3) does not itself terminate,
// even though the function as a whole can reach Exit via its sibling. If
// this test fails with StopProvenConsumed, the analysis has regressed to
// the over-permissive version that would mark every branch of a select
// loop as "consumed" just because some other branch resolves it.
func TestStopCapabilityDataflow_SiblingBranchIsNotProvenConsumed(t *testing.T) {
	g := selectLoopCFG()
	out := StopCapabilityDataflow(g, true)
	if out[3] == StopProvenConsumed {
		t.Fatalf("block 3 (the unrelated jobs branch) should not be StopProvenConsumed just because a sibling branch resolves the loop")
	}
	if out[3] != StopAvailable {
		t.Fatalf("block 3 should be StopAvailable (a context exists, just not consumed here), got %v", out[3])
	}
}

func TestStopCapabilityDataflow_HeaderSeesConsumptionAfterLoopBack(t *testing.T) {
	g := selectLoopCFG()
	out := StopCapabilityDataflow(g, true)
	// The header (block 1) is reached both directly from Entry and via the
	// loop-back edge from block 4. Its state should reflect StopAvailable
	// (from Entry) joined with whatever comes around the back-edge; since
	// the back-edge path (3 -> 4 -> 1) never itself resolves, the header
	// should stay StopAvailable, not StopProvenConsumed -- resolution only
	// ever happens inside block 2, which routes straight to Exit and never
	// feeds back into the header at all.
	if out[1] != StopAvailable {
		t.Fatalf("header (block 1) should be StopAvailable, got %v", out[1])
	}
}

// diamondBlocks builds a simple branch-and-merge CFG (0 -> {1,2} -> 3),
// reused below to demonstrate pointTransfer's generic mechanism applies
// correctly to Ownership and JoinObligation, not only StopCapability.
func diamondBlocks() *model.CFG {
	return &model.CFG{Entry: 0, Exit: 3, Blocks: []model.BasicBlock{
		{ID: 0, Successors: []model.Edge{{From: 0, To: 1}, {From: 0, To: 2}}},
		{ID: 1, Successors: []model.Edge{{From: 1, To: 3}}, Predecessors: []model.BlockID{0}},
		{ID: 2, Successors: []model.Edge{{From: 2, To: 3}}, Predecessors: []model.BlockID{0}},
		{ID: 3, Predecessors: []model.BlockID{1, 2}},
	}}
}

// TestOwnershipDataflow_TransferOnOneBranchResolvesTheMerge demonstrates
// pointTransfer applied to Ownership: a transfer point on only one branch
// of a diamond (e.g. a return statement passing a cancel func out) still
// resolves the merge block, matching Lifeline's existing philosophy
// elsewhere in the codebase that evidence of resolution on any one path is
// credited, not required on every path.
func TestOwnershipDataflow_TransferOnOneBranchResolvesTheMerge(t *testing.T) {
	g := diamondBlocks()
	atPoint := func(id model.BlockID) bool { return id == 1 } // transfer happens only on the left branch
	out := model.Solve(g, OwnershipLocal, OwnershipLocal, pointTransfer[Ownership](atPoint, OwnershipTransferred), joinOwnership)
	if out[1] != OwnershipTransferred {
		t.Fatalf("block 1 (the transfer point) should be OwnershipTransferred, got %v", out[1])
	}
	if out[2] != OwnershipLocal {
		t.Fatalf("block 2 (the branch with no transfer) should stay OwnershipLocal, got %v", out[2])
	}
	if out[3] != OwnershipTransferred {
		t.Fatalf("block 3 (the merge) should be OwnershipTransferred: resolution on either path is credited, got %v", out[3])
	}
}

// TestJoinObligationDataflow_SatisfiedOnOneBranchResolvesTheMerge is the
// same demonstration for JoinObligation, standing in for a Wait() call
// present on only one path (e.g. an early-return path that never started
// any workers, alongside a path that did and joins them).
func TestJoinObligationDataflow_SatisfiedOnOneBranchResolvesTheMerge(t *testing.T) {
	g := diamondBlocks()
	atPoint := func(id model.BlockID) bool { return id == 2 } // Wait() happens only on the right branch
	out := model.Solve(g, JoinNone, JoinRequired, pointTransfer[JoinObligation](atPoint, JoinSatisfied), joinJoinObligation)
	if out[2] != JoinSatisfied {
		t.Fatalf("block 2 (the Wait() point) should be JoinSatisfied, got %v", out[2])
	}
	if out[1] != JoinRequired {
		t.Fatalf("block 1 (no Wait() on this branch) should stay JoinRequired, got %v", out[1])
	}
	if out[3] != JoinSatisfied {
		t.Fatalf("block 3 (the merge) should be JoinSatisfied: resolution on either path is credited, got %v", out[3])
	}
}
