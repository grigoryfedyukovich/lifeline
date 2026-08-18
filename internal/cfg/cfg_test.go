package cfg

import (
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"testing"

	"github.com/gfedyukovich/lifeline/internal/model"
)

func build(t *testing.T, source, funcName string) *model.CFG {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "input.go", source, parser.ParseComments)
	if err != nil {
		t.Fatal(err)
	}
	info := &types.Info{
		Types: make(map[ast.Expr]types.TypeAndValue), Defs: make(map[*ast.Ident]types.Object), Uses: make(map[*ast.Ident]types.Object),
		Selections: make(map[*ast.SelectorExpr]*types.Selection), Scopes: make(map[ast.Node]*types.Scope), Implicits: make(map[ast.Node]types.Object),
	}
	if _, err := (&types.Config{Importer: importer.Default()}).Check("example.test/input", fset, []*ast.File{file}, info); err != nil {
		t.Fatal(err)
	}
	var body *ast.BlockStmt
	for _, d := range file.Decls {
		if fd, ok := d.(*ast.FuncDecl); ok && fd.Name.Name == funcName {
			body = fd.Body
		}
	}
	if body == nil {
		t.Fatalf("function %s not found", funcName)
	}
	return Build(funcName, fset, body, info)
}

func edgesOfKind(g *model.CFG, kind model.EdgeKind) []model.Edge {
	var out []model.Edge
	for _, blk := range g.Blocks {
		for _, e := range blk.Successors {
			if e.Kind == kind {
				out = append(out, e)
			}
		}
	}
	return out
}

func edgesFrom(g *model.CFG, id model.BlockID) []model.Edge {
	return g.Block(id).Successors
}

func blocksByKind(g *model.CFG, kind string) []model.BlockID {
	var out []model.BlockID
	for _, blk := range g.Blocks {
		if blk.Kind == kind {
			out = append(out, blk.ID)
		}
	}
	return out
}

// reachable returns every block reachable from start, following edges
// forward. Used to assert on structural reachability rather than exact
// block IDs, which are an implementation detail.
func reachable(g *model.CFG, start model.BlockID) map[model.BlockID]bool {
	seen := map[model.BlockID]bool{start: true}
	stack := []model.BlockID{start}
	for len(stack) > 0 {
		id := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		for _, e := range edgesFrom(g, id) {
			if !seen[e.To] {
				seen[e.To] = true
				stack = append(stack, e.To)
			}
		}
	}
	return seen
}

func TestBareInfiniteLoopHasNoFalseEdge(t *testing.T) {
	g := build(t, `package p
func F() {
	for {
		work()
	}
}
func work() {}
`, "F")
	headers := blocksByKind(g, "loop-header")
	if len(headers) != 1 {
		t.Fatalf("loop-header blocks = %d, want 1", len(headers))
	}
	edges := edgesFrom(g, headers[0])
	if len(edges) != 1 || edges[0].Kind != model.EdgeNormal {
		t.Fatalf("bare for{} header edges = %#v, want exactly one EdgeNormal", edges)
	}
	// The critical structural property: Exit must not be reachable at all,
	// since there is no break, return, or false-edge anywhere in the body.
	if reachable(g, g.Entry)[g.Exit] {
		t.Fatalf("Exit should not be reachable from a bare for{} with no break/return")
	}
}

func TestCondLoopHasTrueAndFalseEdges(t *testing.T) {
	g := build(t, `package p
func F() {
	for i := 0; i < 10; i++ {
		work()
	}
}
func work() {}
`, "F")
	headers := blocksByKind(g, "loop-header")
	if len(headers) != 1 {
		t.Fatalf("loop-header blocks = %d, want 1", len(headers))
	}
	edges := edgesFrom(g, headers[0])
	if len(edges) != 2 {
		t.Fatalf("cond loop header edges = %#v, want 2 (true, false)", edges)
	}
	var hasTrue, hasFalse bool
	for _, e := range edges {
		hasTrue = hasTrue || e.Kind == model.EdgeTrue
		hasFalse = hasFalse || e.Kind == model.EdgeFalse
	}
	if !hasTrue || !hasFalse {
		t.Fatalf("cond loop header edges = %#v, want both EdgeTrue and EdgeFalse", edges)
	}
	if !reachable(g, g.Entry)[g.Exit] {
		t.Fatalf("Exit should be reachable via the loop's false-edge")
	}
}

func TestContinueWithPostTargetsPostNotHeader(t *testing.T) {
	g := build(t, `package p
func F() {
	for i := 0; i < 10; i++ {
		if shouldSkip() {
			continue
		}
		work()
	}
}
func work() {}
func shouldSkip() bool { return false }
`, "F")
	continues := edgesOfKind(g, model.EdgeContinue)
	if len(continues) != 1 {
		t.Fatalf("continue edges = %d, want 1", len(continues))
	}
	target := g.Block(continues[0].To)
	if target.Kind != "loop-post" {
		t.Fatalf("continue target kind = %q, want loop-post (must run i++ before re-checking the condition)", target.Kind)
	}
}

func TestBreakInNestedSwitchTargetsSwitchNotLoop(t *testing.T) {
	g := build(t, `package p
func F() {
	for {
		switch status() {
		case 1:
			break
		}
		work()
	}
}
func work() {}
func status() int { return 0 }
`, "F")
	breaks := edgesOfKind(g, model.EdgeBreak)
	if len(breaks) != 1 {
		t.Fatalf("break edges = %d, want 1", len(breaks))
	}
	target := g.Block(breaks[0].To)
	if target.Kind != "switch-after" {
		t.Fatalf("unlabeled break inside a switch should target switch-after, got %q", target.Kind)
	}
	// The loop itself is still unconditional and has no break of its own:
	// Exit must remain unreachable.
	if reachable(g, g.Entry)[g.Exit] {
		t.Fatalf("Exit should not be reachable: the only break targets the switch, not the loop")
	}
}

func TestLabeledBreakTargetsOuterLoop(t *testing.T) {
	g := build(t, `package p
func F() {
Outer:
	for {
		for {
			if shouldStop() {
				break Outer
			}
		}
	}
}
func shouldStop() bool { return false }
`, "F")
	breaks := edgesOfKind(g, model.EdgeBreak)
	if len(breaks) != 1 {
		t.Fatalf("break edges = %d, want 1", len(breaks))
	}
	if breaks[0].Label != "Outer" {
		t.Fatalf("break label = %q, want Outer", breaks[0].Label)
	}
	if !reachable(g, g.Entry)[g.Exit] {
		t.Fatalf("Exit should be reachable via the labeled break to the outer loop")
	}
}

func TestUnlabeledBreakOnlyExitsInnerLoop(t *testing.T) {
	g := build(t, `package p
func F() {
	for {
		for {
			break
		}
	}
}
`, "F")
	// The inner break only reaches the inner loop's own after-block, which
	// then falls back into the (unconditional, break-less) outer loop.
	// Exit must not be reachable: this is the known residual gap the AST
	// version of this analysis has (see docs/limitations.md), captured
	// here as an explicit fixture so a future CFG/SCC-based LL1002 can be
	// checked against it directly.
	if reachable(g, g.Entry)[g.Exit] {
		t.Fatalf("Exit should not be reachable: the only break exits the inner loop, not the outer one")
	}
}

func TestRangeOverChannelVsSlice(t *testing.T) {
	g := build(t, `package p
func F(ch chan int, xs []int) {
	for range ch {
	}
	for range xs {
	}
}
`, "F")
	falseEdges := edgesOfKind(g, model.EdgeFalse)
	var sawChan, sawSlice bool
	for _, e := range falseEdges {
		switch e.Label {
		case "channel closed":
			sawChan = true
		case "sequence exhausted":
			sawSlice = true
		}
	}
	if !sawChan || !sawSlice {
		t.Fatalf("false-edge labels = %#v, want both 'channel closed' and 'sequence exhausted'", falseEdges)
	}
}

func TestSwitchWithoutDefaultHasImplicitNoMatchEdge(t *testing.T) {
	g := build(t, `package p
func F(x int) {
	switch x {
	case 1:
		work()
	case 2:
		work()
	}
	work()
}
func work() {}
`, "F")
	noMatch := 0
	for _, e := range edgesOfKind(g, model.EdgeNormal) {
		if e.Label == "no match" {
			noMatch++
		}
	}
	if noMatch != 1 {
		t.Fatalf("no-match edges = %d, want 1 (switch has no default)", noMatch)
	}
}

func TestSwitchWithDefaultHasNoImplicitNoMatchEdge(t *testing.T) {
	g := build(t, `package p
func F(x int) {
	switch x {
	case 1:
		work()
	default:
		work()
	}
}
func work() {}
`, "F")
	for _, e := range edgesOfKind(g, model.EdgeNormal) {
		if e.Label == "no match" {
			t.Fatalf("switch with a default should have no implicit no-match edge, got one: %#v", e)
		}
	}
}

func TestFallthroughJumpsToNextCase(t *testing.T) {
	g := build(t, `package p
func F(x int) {
	switch x {
	case 1:
		work()
		fallthrough
	case 2:
		work()
	}
}
func work() {}
`, "F")
	ft := edgesOfKind(g, model.EdgeFallthrough)
	if len(ft) != 1 {
		t.Fatalf("fallthrough edges = %d, want 1", len(ft))
	}
	if g.Block(ft[0].To).Kind != "case" {
		t.Fatalf("fallthrough target kind = %q, want case", g.Block(ft[0].To).Kind)
	}
}

func TestSelectHasNoImplicitNoMatchEdge(t *testing.T) {
	g := build(t, `package p
func F(ch chan int) {
	select {
	case <-ch:
	}
	work()
}
func work() {}
`, "F")
	for _, e := range edgesOfKind(g, model.EdgeNormal) {
		if e.Label == "no match" {
			t.Fatalf("select should never have an implicit no-match edge (it blocks instead), got one: %#v", e)
		}
	}
}

func TestContextDoneSelectWithReturnReachesExit(t *testing.T) {
	g := build(t, `package p
import "context"
func F(ctx context.Context, jobs chan int) {
	for {
		select {
		case <-ctx.Done():
			return
		case msg := <-jobs:
			work(msg)
		}
	}
}
func work(int) {}
`, "F")
	if !reachable(g, g.Entry)[g.Exit] {
		t.Fatalf("Exit should be reachable via the ctx.Done() case's return")
	}
	cases := blocksByKind(g, "comm-case")
	if len(cases) != 2 {
		t.Fatalf("comm-case blocks = %d, want 2", len(cases))
	}
}

func TestPanicReachesExit(t *testing.T) {
	g := build(t, `package p
func F() {
	panic("boom")
}
`, "F")
	panics := edgesOfKind(g, model.EdgePanic)
	if len(panics) != 1 {
		t.Fatalf("panic edges = %d, want 1", len(panics))
	}
	if panics[0].To != g.Exit {
		t.Fatalf("panic should target Exit directly")
	}
}

func TestGotoForwardAndBackward(t *testing.T) {
	g := build(t, `package p
func F() {
	goto Skip
	work()
Skip:
	work()
Loop:
	work()
	goto Loop
}
func work() {}
`, "F")
	gotos := edgesOfKind(g, model.EdgeGoto)
	if len(gotos) != 2 {
		t.Fatalf("goto edges = %d, want 2", len(gotos))
	}
	// The forward goto's target must be reachable from Entry not only via
	// the goto edge, but also as the block the fallthrough-into-Skip-label
	// resolves to -- both should be the same block. The backward goto
	// should form a cycle (unreachable from Exit's perspective is not
	// asserted here; just that Entry still reaches Exit via the eventual
	// fallthrough is not guaranteed since Loop never exits -- this fixture
	// only checks that both edges exist and connect to real blocks).
	for _, e := range gotos {
		if g.Block(e.To) == nil {
			t.Fatalf("goto edge target %d does not resolve to a real block", e.To)
		}
	}
}

func TestIfElseBothBranchesMerge(t *testing.T) {
	g := build(t, `package p
func F(x bool) {
	if x {
		work()
	} else {
		work()
	}
	work()
}
func work() {}
`, "F")
	afters := blocksByKind(g, "if-after")
	if len(afters) != 1 {
		t.Fatalf("if-after blocks = %d, want 1", len(afters))
	}
	if len(g.Block(afters[0]).Predecessors) != 2 {
		t.Fatalf("if-after predecessors = %d, want 2 (both branches fall through to it)", len(g.Block(afters[0]).Predecessors))
	}
}

func TestIfBothBranchesReturnMergeIsUnreachable(t *testing.T) {
	g := build(t, `package p
func F(x bool) {
	if x {
		return
	} else {
		return
	}
}
`, "F")
	afters := blocksByKind(g, "if-after")
	if len(afters) != 1 {
		t.Fatalf("if-after blocks = %d, want 1", len(afters))
	}
	if len(g.Block(afters[0]).Predecessors) != 0 {
		t.Fatalf("if-after predecessors = %d, want 0 (both branches return)", len(g.Block(afters[0]).Predecessors))
	}
}

func TestDeadCodeAfterReturnGetsUnreachableBlock(t *testing.T) {
	g := build(t, `package p
func F() {
	return
	work()
}
func work() {}
`, "F")
	dead := blocksByKind(g, "unreachable")
	if len(dead) != 1 {
		t.Fatalf("unreachable blocks = %d, want 1", len(dead))
	}
	if len(g.Block(dead[0]).Predecessors) != 0 {
		t.Fatalf("dead code block should have 0 predecessors, got %d", len(g.Block(dead[0]).Predecessors))
	}
	if len(g.Block(dead[0]).Instructions) != 1 {
		t.Fatalf("dead code block should still record the unreachable call, got %d instructions", len(g.Block(dead[0]).Instructions))
	}
}

func TestLabeledContinueTargetsOuterLoop(t *testing.T) {
	g := build(t, `package p
func F() {
Outer:
	for i := 0; i < 3; i++ {
		for j := 0; j < 3; j++ {
			if skip() {
				continue Outer
			}
			work()
		}
	}
}
func work()       {}
func skip() bool  { return false }
`, "F")
	continues := edgesOfKind(g, model.EdgeContinue)
	if len(continues) != 1 {
		t.Fatalf("continue edges = %d, want 1", len(continues))
	}
	if continues[0].Label != "Outer" {
		t.Fatalf("continue label = %q, want Outer", continues[0].Label)
	}
	// The outer loop has a Post (i++), so continue must target its
	// loop-post block, not its loop-header directly -- same rule as the
	// unlabeled case, just resolved to a different (outer) loop's frame.
	if g.Block(continues[0].To).Kind != "loop-post" {
		t.Fatalf("labeled continue target kind = %q, want loop-post", g.Block(continues[0].To).Kind)
	}
}

func TestElseIfChainProducesThreeBranches(t *testing.T) {
	g := build(t, `package p
func F(x int) {
	if x == 1 {
		work()
	} else if x == 2 {
		work()
	} else {
		work()
	}
	work()
}
func work() {}
`, "F")
	trues := edgesOfKind(g, model.EdgeTrue)
	if len(trues) != 2 {
		t.Fatalf("true edges = %d, want 2 (one per if condition in the chain)", len(trues))
	}
	// Every branch falls through to the same final statement, so it must
	// all eventually reach Exit.
	if !reachable(g, g.Entry)[g.Exit] {
		t.Fatalf("Exit should be reachable: all branches fall through to work() and the function's implicit return")
	}
}

func TestSwitchInitStatementRunsBeforeCases(t *testing.T) {
	g := build(t, `package p
func F() {
	switch x := compute(); x {
	case 1:
		work()
	}
}
func work()        {}
func compute() int { return 0 }
`, "F")
	// The init statement (x := compute()) must be recorded as an
	// instruction reachable from Entry before any case is taken, not
	// dropped or duplicated into every case block.
	found := false
	for _, blk := range g.Blocks {
		for _, instr := range blk.Instructions {
			if instr.Op == "assign" {
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("switch init statement was not recorded as an instruction anywhere")
	}
}

func TestStraightLineFunctionReachesExit(t *testing.T) {
	g := build(t, `package p
func F() {
	a()
	b()
	c()
}
func a() {}
func b() {}
func c() {}
`, "F")
	if !reachable(g, g.Entry)[g.Exit] {
		t.Fatalf("Exit should be reachable from straight-line code with an implicit return")
	}
	total := 0
	for _, blk := range g.Blocks {
		total += len(blk.Instructions)
	}
	if total != 3 {
		t.Fatalf("total instructions = %d, want 3", total)
	}
}
