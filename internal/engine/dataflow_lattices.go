package engine

import "github.com/gfedyukovich/lifeline/internal/model"

// This file implements the "block-level abstract state and worklist
// solver" docs/cfg-migration-plan.md Phase 3 asks for, built on
// model.Solve. It computes dataflow facts; it does not change any
// diagnostic verdict. See docs/cfg-migration-plan.md's status header for
// what is and is not wired to real code yet.
//
// A note on scope: the migration doc's Phase 3 list is "context
// availability; stop capability; worker exit state; ownership transfer;
// join obligation". "Worker exit state" (termination) is deliberately not
// re-implemented here as a forward dataflow fact: it is naturally a
// backward question ("starting from here, can execution reach Exit"),
// which is exactly what Phase 2's cfg_verdict.go already computes
// correctly via model.CFG.CanReach and SCC analysis. Recomputing it via
// forward propagation would be redundant at best and a second,
// potentially inconsistent source of truth at worst. "Context
// availability" is folded into StopCapability below (StopAvailable is
// exactly "a stop mechanism is available"), rather than tracked as a
// separate fifth dimension.

// StopCapability tracks, per program point, whether a recognized way to
// end a worker's loop is available, and whether it has actually been
// reached (not merely available in principle -- a context can be captured
// by a closure without ever being checked).
type StopCapability int

const (
	StopNone StopCapability = iota
	StopAvailable
	StopProvenConsumed
)

func joinStopCapability(a, b StopCapability) StopCapability {
	if b > a {
		return b
	}
	return a
}

// JoinObligation tracks a single tracked group's (WaitGroup/errgroup) join
// state. Unlike StopCapability and Ownership below, which this file wires
// to real CFG structure, JoinObligation's transfer function is
// demonstrated only against synthetic fixtures (dataflow_lattices_test.go)
// for now: applying it to a real function requires identifying which
// specific call is the Wait() for which specific tracked group, the same
// kind of frontend-supplied predicate Phase 2 built for trusted-stop
// calls (internal/frontend's trustedTerminator), which has not been built
// for this dimension yet.
type JoinObligation int

const (
	JoinNone JoinObligation = iota
	JoinRequired
	JoinSatisfied
	JoinEscaped
)

func joinJoinObligation(a, b JoinObligation) JoinObligation {
	if b > a {
		return b
	}
	return a
}

// Ownership tracks a single tracked cancel/group value's ownership state:
// has it (on some path reaching this point) been transferred out of local
// scope (called, returned, stored, passed to a goroutine) yet. Local is
// bottom/initial; Transferred is the resolved state a transfer point
// produces. Unknown is reserved for future alias-tracking work (the
// migration doc explicitly says not to add broad alias analysis in this
// phase) and is not produced by anything in this file yet.
//
// Like JoinObligation, this is demonstrated against synthetic fixtures
// only for now; wiring it to real cancel/group bindings needs the same
// kind of frontend-supplied "which call is the discharge for which
// binding" predicate this file's StopCapabilityDataflow already has an
// example of building from real CFG structure.
type Ownership int

const (
	OwnershipLocal Ownership = iota
	OwnershipTransferred
	OwnershipUnknown
)

func joinOwnership(a, b Ownership) Ownership {
	if b > a {
		return b
	}
	return a
}

// pointTransfer builds a model.Solve transfer function from a predicate
// over blocks: once atPoint(id) is true for some block reachable on the
// path so far, the state becomes resolved (and monotonically stays
// resolved from there, via the incoming in-state) from that point on.
// This is the shared shape all three lattices above use: "has a
// resolving point been reached yet on any path to here."
func pointTransfer[S constraints](atPoint func(model.BlockID) bool, resolved S) func(model.BlockID, S) S {
	return func(id model.BlockID, in S) S {
		if atPoint(id) {
			return resolved
		}
		return in
	}
}

// constraints is satisfied by any of this file's three state types. Go's
// generics don't let a plain `any` parameter be compared for the
// pointTransfer closure's return type in a useful way here, so this is
// listed explicitly rather than left fully open -- these three types are
// the only ones this file's lattices define anyway.
type constraints interface {
	StopCapability | JoinObligation | Ownership
}

// StopCapabilityDataflow computes StopCapability at every block of g via
// model.Solve, from real CFG structure alone: a block "provably consumes"
// stop capability if it has an outgoing edge that itself leads straight to
// the function's exit -- a trusted-stop edge (a configured stop-wrapper
// call or delegated context, see internal/cfg's EdgeTrustedStop), a
// return, or a panic. This is a local check on the block's own edges, not
// a global "can this function reach exit at all" question (model.CanReach
// would be too permissive here: in a select loop, the branch that merely
// does work and loops back can trivially "reach exit" too, transitively,
// through a sibling branch that returns -- that isn't this block
// consuming stop capability, it's an unrelated branch doing so). Checking
// the block's own edges generalizes past select cases specifically: a
// plain `if shouldStop() { return }` inside a loop body counts exactly
// the same way a `case <-ctx.Done(): return` does.
//
// hasContext reports whether any stop mechanism is available in this body
// at all (from AvailableContexts, computed elsewhere); if false, the
// whole function's result is StopNone throughout, since there is nothing
// to consume.
func StopCapabilityDataflow(g *model.CFG, hasContext bool) map[model.BlockID]StopCapability {
	if g == nil {
		return nil
	}
	if !hasContext {
		out := make(map[model.BlockID]StopCapability, len(g.Blocks))
		for _, blk := range g.Blocks {
			out[blk.ID] = StopNone
		}
		return out
	}
	atPoint := func(id model.BlockID) bool {
		blk := g.Block(id)
		if blk == nil {
			return false
		}
		for _, e := range blk.Successors {
			switch e.Kind {
			case model.EdgeTrustedStop, model.EdgeReturn, model.EdgePanic:
				return true
			}
		}
		return false
	}
	return model.Solve(g, StopNone, StopAvailable, pointTransfer[StopCapability](atPoint, StopProvenConsumed), joinStopCapability)
}
