package model

// Span is parser-independent source metadata. Offsets are byte offsets in the
// source file; lines and columns are one-based.
type Span struct {
	File        string `json:"file"`
	StartLine   int    `json:"start_line"`
	StartColumn int    `json:"start_column"`
	EndLine     int    `json:"end_line"`
	EndColumn   int    `json:"end_column"`
	StartOffset int    `json:"start_offset,omitempty"`
	EndOffset   int    `json:"end_offset,omitempty"`
}

type Evidence struct {
	Kind    string `json:"kind"`
	Message string `json:"message"`
	Span    *Span  `json:"span,omitempty"`
}

type FixEdit struct {
	Span    Span   `json:"span"`
	NewText string `json:"new_text"`
}

type SuggestedFix struct {
	Message string    `json:"message"`
	Edits   []FixEdit `json:"edits"`
}

// Instruction is Lifeline's parser-independent SSA-like instruction summary.
// It deliberately records only information needed by lifecycle recognizers and
// diagnostics; it is not a general Go SSA representation.
type Instruction struct {
	Index   int      `json:"index"`
	Op      string   `json:"op"`
	Span    Span     `json:"span"`
	Callee  string   `json:"callee,omitempty"`
	Defines []string `json:"defines,omitempty"`
	Uses    []string `json:"uses,omitempty"`
}

type CancelBinding struct {
	Factory      string        `json:"factory"`
	ContextName  string        `json:"context_name,omitempty"`
	CancelName   string        `json:"cancel_name,omitempty"`
	Span         Span          `json:"span"`
	Discarded    bool          `json:"discarded"`
	Called       bool          `json:"called"`
	Escapes      bool          `json:"escapes"`
	UsedByChild  bool          `json:"used_by_child"`
	Evidence     []Evidence    `json:"evidence,omitempty"`
	SuggestedFix *SuggestedFix `json:"suggested_fix,omitempty"`
}

type Goroutine struct {
	Span              Span       `json:"span"`
	Kind              string     `json:"kind"`
	InfiniteLoop      bool       `json:"infinite_loop"`
	HasReturn         bool       `json:"has_return"`
	ContextStop       bool       `json:"context_stop"`
	ChannelStop       bool       `json:"channel_stop"`
	ExplicitStop      bool       `json:"explicit_stop"`
	AvailableContexts []string   `json:"available_contexts,omitempty"`
	CapturedNames     []string   `json:"captured_names,omitempty"`
	Evidence          []Evidence `json:"evidence,omitempty"`
	// CFG is the control-flow graph this goroutine's body was analyzed
	// with, when one was built (nil for cross-package fact-imported
	// summaries, which carry no body to build one from). It is an internal
	// analysis input, not part of the user-facing report -- see
	// internal/cfg for construction and `-dump cfg` to inspect one
	// directly -- so it is excluded from JSON.
	CFG *CFG `json:"-"`
	// ImportedUnresolvedLoop, when non-nil, is a cross-package go vet
	// fact's own CFG/SCC-derived verdict for whether this goroutine's body
	// has an unresolved persistent loop (Phase 4, docs/cfg-migration-plan.md).
	// It exists because a fact-imported goroutine has no CFG of its own to
	// compute this from directly (see CFG above) -- the exporting package
	// already computed it there and is trusted to have done so correctly,
	// the same way any other cross-package fact is trusted rather than
	// re-derived. Excluded from JSON for the same reason CFG is: an
	// internal analysis input, not part of the user-facing report.
	ImportedUnresolvedLoop *bool `json:"-"`
}

type JoinGroup struct {
	Kind     string     `json:"kind"`
	Name     string     `json:"name"`
	Span     Span       `json:"span"`
	Starts   int        `json:"starts"`
	Joined   bool       `json:"joined"`
	Escapes  bool       `json:"escapes"`
	Evidence []Evidence `json:"evidence,omitempty"`
	// CountMismatch is true iff literal, unambiguous accounting proves this
	// sync.WaitGroup's Add total exceeds its Done total (Phase 3
	// completion, "count intervals", docs/cfg-migration-plan.md); see
	// Evidence's "count-mismatch" entry for the specific numbers. Never
	// true from an inability to verify -- a non-literal Add argument, a
	// loop-scoped Add or Done not matched by the recognized
	// Add(1)-then-spawned-Done idiom, or any other shape this narrow check
	// doesn't attempt, all leave this false, not true. Always false for an
	// errgroup.Group, whose own Add/Done-equivalent bookkeeping is
	// internal to the library and not something a caller can get wrong the
	// same way.
	CountMismatch bool `json:"count_mismatch"`
	// JoinedOnAllPaths, when non-nil, is the owner function's own CFG-
	// verified answer (model.CFG.ReachableAvoiding) to whether every path
	// from entry to exit passes through at least one of this group's own
	// Wait() calls -- Phase 3 completion, "join-before-owner-return"
	// (docs/cfg-migration-plan.md). This mirrors Goroutine.
	// ImportedUnresolvedLoop's own nil-means-"not established" convention
	// deliberately: nil covers every case this wasn't (or couldn't be)
	// checked -- Joined is false to begin with, Joined came from a
	// join_wrapper call rather than a direct Wait() with a call site to
	// find a block for, or there was no CFG to check against at all -- and
	// is treated exactly like Joined's own pre-Phase-3 meaning, not as a
	// disproof. This is the same class of precision LL1002 already has for
	// loops (see UnresolvedLoop's own doc comment): a Wait() call reachable
	// on some path is not the same guarantee as a Wait() call reachable on
	// every path, and a flat bool alone can't tell those apart from "never
	// checked".
	JoinedOnAllPaths *bool `json:"joined_on_all_paths,omitempty"`
	// StopAfterWait is true iff the owner function also calls a recognized
	// worker stop signal (a configured stop_wrapper call, or the cancel
	// function of a tracked context) for its own body, and the CFG proves
	// that call is reachable only by a path that already passed through
	// one of this group's Wait() calls -- i.e. Wait is provably called
	// before the signal that would let its workers stop, not after (Phase
	// 3 completion, "stop-before-wait", docs/cfg-migration-plan.md). False
	// whenever no such stop-signal call exists in the owner body, or when
	// the relative order can't be established this way -- never true from
	// a mere inability to prove the safe order, only from a positive CFG
	// proof of the unsafe one.
	StopAfterWait bool `json:"stop_after_wait"`
}

// BlockID identifies a basic block within a CFG. It is stable within a
// single CFG's lifetime and, by construction, equal to that block's index
// in CFG.Blocks, so it can be used directly as a slice index via CFG.Block.
type BlockID int

// EdgeKind describes why control flows from one block to another, so a CFG
// consumer can distinguish (for example) "this edge exists because of an
// if/else branch" from "this edge exists because of an explicit break"
// without re-deriving that from the source AST.
type EdgeKind string

const (
	EdgeNormal      EdgeKind = "normal"       // unconditional fallthrough to the next block
	EdgeTrue        EdgeKind = "true"         // if/for condition true
	EdgeFalse       EdgeKind = "false"        // if/for condition false, or a range exhausted
	EdgeCase        EdgeKind = "case"         // switch/type-switch/select case taken (see Edge.Label)
	EdgeFallthrough EdgeKind = "fallthrough"  // an explicit `fallthrough` statement
	EdgeBreak       EdgeKind = "break"        // break, to the targeted loop/switch/select's successor
	EdgeContinue    EdgeKind = "continue"     // continue, to the targeted loop's continuation point
	EdgeLoopBack    EdgeKind = "loop-back"    // a loop's back-edge to its header (the edge that closes the cycle)
	EdgeGoto        EdgeKind = "goto"         // an explicit goto to a labeled statement
	EdgeReturn      EdgeKind = "return"       // return, to the function's Exit block
	EdgePanic       EdgeKind = "panic"        // panic(...), to the function's Exit block
	EdgeTrustedStop EdgeKind = "trusted-stop" // a recognized stop-wrapper call or context delegation, trusted to terminate even though it is not itself a return, panic, or CFG-visible branch
)

// Edge is a directed control-flow edge between two blocks of the same CFG.
type Edge struct {
	From  BlockID  `json:"from"`
	To    BlockID  `json:"to"`
	Kind  EdgeKind `json:"kind"`
	Label string   `json:"label,omitempty"` // e.g. a case's rendered condition, or a break/continue/goto's label
	Span  Span     `json:"span"`            // span of the branching construct itself
}

// BasicBlock is a maximal straight-line sequence of instructions: control
// only enters at the top and leaves at the bottom, to whatever Successors
// lists. Kind is an annotation for readability (dumps, explanations); it is
// not load-bearing for any graph algorithm.
type BasicBlock struct {
	ID           BlockID       `json:"id"`
	Kind         string        `json:"kind,omitempty"`
	Span         Span          `json:"span"`
	Instructions []Instruction `json:"instructions,omitempty"`
	Successors   []Edge        `json:"successors,omitempty"`
	Predecessors []BlockID     `json:"predecessors,omitempty"`
}

// CFG is a parser-independent control-flow graph for a single function or
// function-literal body. Unlike Function.IR (a flat, deterministic
// traversal order with no branch/merge relationships), a CFG makes control
// flow explicit: which blocks can reach which others, and why. See
// internal/cfg for the builder that produces this from a typed AST.
type CFG struct {
	Function string       `json:"function"`
	Entry    BlockID      `json:"entry"`
	Exit     BlockID      `json:"exit"`
	Blocks   []BasicBlock `json:"blocks"`
}

// Block returns the block with the given ID, or nil if id is out of range.
func (g *CFG) Block(id BlockID) *BasicBlock {
	if g == nil || int(id) < 0 || int(id) >= len(g.Blocks) {
		return nil
	}
	return &g.Blocks[id]
}

// Reachable returns every block reachable from start by following edges
// forward, including start itself.
func (g *CFG) Reachable(start BlockID) map[BlockID]bool {
	seen := map[BlockID]bool{start: true}
	stack := []BlockID{start}
	for len(stack) > 0 {
		id := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		for _, e := range g.Blocks[id].Successors {
			if !seen[e.To] {
				seen[e.To] = true
				stack = append(stack, e.To)
			}
		}
	}
	return seen
}

// CanReach returns every block that can reach target by following edges
// forward, equivalently every block reachable from target by following
// Predecessors backward. Includes target itself.
func (g *CFG) CanReach(target BlockID) map[BlockID]bool {
	seen := map[BlockID]bool{target: true}
	stack := []BlockID{target}
	for len(stack) > 0 {
		id := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		for _, pred := range g.Blocks[id].Predecessors {
			if !seen[pred] {
				seen[pred] = true
				stack = append(stack, pred)
			}
		}
	}
	return seen
}

// ReachableAvoiding returns every block reachable from start by following
// edges forward without ever entering a block in avoid, including start
// itself (unless start is itself in avoid, in which case the result is
// empty: nothing is reachable "without entering avoid" from a start point
// that already is one). Unlike Reachable, this treats every block in avoid
// as if it had been deleted from the graph entirely, not merely as an
// ordinary block to stop at.
//
// This is the primitive a "does every path from A to B pass through C"
// question reduces to, for any set of candidate C blocks: B is reachable
// from A avoiding every block in C if and only if some path from A to B
// exists that never touches C, i.e. C does not lie on every such path. The
// call sites in internal/engine use this both ways -- "is the owner's own
// Exit reachable without ever passing through a Wait() call" (join-before-
// return) and "is a stop-signal call reachable without first passing
// through a Wait() call" (stop-before-wait, checked in the opposite
// direction: if the stop signal is NOT reachable avoiding Wait, Wait
// necessarily precedes it on every path).
func (g *CFG) ReachableAvoiding(start BlockID, avoid map[BlockID]bool) map[BlockID]bool {
	seen := map[BlockID]bool{}
	if avoid[start] {
		return seen
	}
	seen[start] = true
	stack := []BlockID{start}
	for len(stack) > 0 {
		id := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		for _, e := range g.Blocks[id].Successors {
			if avoid[e.To] || seen[e.To] {
				continue
			}
			seen[e.To] = true
			stack = append(stack, e.To)
		}
	}
	return seen
}

// SCCs returns the strongly connected components of g via Tarjan's
// algorithm, each as a list of BlockIDs. Every block belongs to exactly
// one SCC; a block with no cycle through it is a trivial singleton SCC of
// its own. Use IsPersistentSCC to tell a genuine cycle apart from that
// trivial case.
func (g *CFG) SCCs() [][]BlockID {
	n := len(g.Blocks)
	index := make([]int, n)
	low := make([]int, n)
	onStack := make([]bool, n)
	for i := range index {
		index[i] = -1
	}
	var stack []BlockID
	var result [][]BlockID
	counter := 0

	var strongConnect func(v BlockID)
	strongConnect = func(v BlockID) {
		index[v] = counter
		low[v] = counter
		counter++
		stack = append(stack, v)
		onStack[v] = true

		for _, e := range g.Blocks[v].Successors {
			w := e.To
			if index[w] == -1 {
				strongConnect(w)
				if low[w] < low[v] {
					low[v] = low[w]
				}
			} else if onStack[w] && index[w] < low[v] {
				low[v] = index[w]
			}
		}

		if low[v] == index[v] {
			var scc []BlockID
			for {
				top := len(stack) - 1
				w := stack[top]
				stack = stack[:top]
				onStack[w] = false
				scc = append(scc, w)
				if w == v {
					break
				}
			}
			result = append(result, scc)
		}
	}

	for v := 0; v < n; v++ {
		if index[v] == -1 {
			strongConnect(BlockID(v))
		}
	}
	return result
}

// IsPersistentSCC reports whether scc represents a genuine cycle: more
// than one block, or a single block with a self-loop (an edge from itself
// to itself). Tarjan's algorithm gives every block its own singleton SCC
// when it isn't part of a real cycle; this is how a caller tells the two
// apart.
func (g *CFG) IsPersistentSCC(scc []BlockID) bool {
	if len(scc) > 1 {
		return true
	}
	if len(scc) == 1 {
		v := scc[0]
		for _, e := range g.Blocks[v].Successors {
			if e.To == v {
				return true
			}
		}
	}
	return false
}

type Function struct {
	Name          string          `json:"name"`
	Span          Span            `json:"span"`
	Contexts      []string        `json:"contexts,omitempty"`
	Cancels       []CancelBinding `json:"cancels,omitempty"`
	Goroutines    []Goroutine     `json:"goroutines,omitempty"`
	Groups        []JoinGroup     `json:"groups,omitempty"`
	BodyLifecycle Goroutine       `json:"body_lifecycle"`
	IR            []Instruction   `json:"ir,omitempty"`
}

type Program struct {
	PackagePath   string     `json:"package_path"`
	Functions     []Function `json:"functions"`
	FunctionCount int        `json:"function_count"`
	Truncated     bool       `json:"truncated"`
	// Suppressions maps file -> source line -> suppressed rule IDs, derived
	// from "//lifeline:ignore" comments. "*" means every rule is suppressed
	// on that line. This is an internal control input for the engine, not
	// part of the public report, so it is excluded from JSON output.
	Suppressions map[string]map[int][]string `json:"-"`
}
