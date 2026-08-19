package model

// Solve computes, for every block in g, an abstract dataflow state via
// forward propagation: starting from initial at Entry and bottom
// everywhere else, it repeatedly computes each block's in-state as the
// join of its predecessors' out-states, applies transfer to get a new
// out-state, and re-queues successors whenever a block's out-state
// changes -- a standard iterative fixed-point worklist algorithm.
//
// This exists because a "MAY have happened by this point" fact (has a
// value's ownership transferred yet, has a join obligation been satisfied
// yet, and so on) cannot be computed correctly by a single forward pass
// when the CFG has a cycle: a fact established partway through a loop body
// must still be visible when that same body is reached again via the
// loop's back-edge, which requires revisiting blocks until nothing
// changes, not visiting each block once.
//
// S must form a join-semilattice under join: join must be commutative,
// associative, and idempotent (join(a, a) == a), and transfer must be
// monotone with respect to it (applying transfer to a "more resolved"
// input never produces a "less resolved" output), or the solver is not
// guaranteed to reach a fixed point and Solve may not terminate.
func Solve[S comparable](g *CFG, bottom, initial S, transfer func(BlockID, S) S, join func(S, S) S) map[BlockID]S {
	out := make(map[BlockID]S, len(g.Blocks))
	processed := make([]bool, len(g.Blocks))
	for i := range g.Blocks {
		out[g.Blocks[i].ID] = bottom
	}
	inQueue := make([]bool, len(g.Blocks))
	queue := make([]BlockID, 0, len(g.Blocks))
	push := func(id BlockID) {
		if !inQueue[id] {
			inQueue[id] = true
			queue = append(queue, id)
		}
	}
	push(g.Entry)

	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		inQueue[id] = false

		var in S
		if id == g.Entry {
			in = initial
		} else {
			preds := g.Blocks[id].Predecessors
			if len(preds) == 0 {
				in = bottom // unreachable block; leave it at bottom
			} else {
				in = out[preds[0]]
				for _, p := range preds[1:] {
					in = join(in, out[p])
				}
			}
		}
		next := transfer(id, in)
		// Propagate on the first processing of this block regardless of
		// whether next differs from bottom: a successor has never seen a
		// real (non-bottom) value from this block yet, even if this
		// block's own computed value happens to equal bottom (e.g. no
		// relevant fact holds here). Comparing only next != out[id] would
		// wrongly treat that as "nothing to propagate" and leave
		// successors stuck at bottom forever.
		changed := !processed[id] || next != out[id]
		processed[id] = true
		if changed {
			out[id] = next
			for _, e := range g.Blocks[id].Successors {
				push(e.To)
			}
		}
	}
	return out
}
