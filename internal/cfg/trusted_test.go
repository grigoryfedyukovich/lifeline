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

// buildTrusted builds funcName's CFG with a trust predicate that treats any
// call to a bare identifier named in trusted as a trusted terminator.
func buildTrusted(t *testing.T, source, funcName string, trusted ...string) *model.CFG {
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
	names := map[string]bool{}
	for _, n := range trusted {
		names[n] = true
	}
	predicate := func(call *ast.CallExpr) bool {
		id, ok := call.Fun.(*ast.Ident)
		return ok && names[id.Name]
	}
	g, _ := Build(funcName, fset, body, info, predicate)
	return g
}

// A trusted call is an assumption, not a terminator: the block holding it
// must have BOTH a trusted-stop edge to Exit and an ordinary edge to the
// code that follows.
func TestTrustedCallKeepsNormalContinuation(t *testing.T) {
	g := buildTrusted(t, `package p
func delegate() {}
func after() {}
func F() {
	delegate()
	after()
}
`, "F", "delegate")

	stops := edgesOfKind(g, model.EdgeTrustedStop)
	if len(stops) != 1 || stops[0].To != g.Exit {
		t.Fatalf("trusted-stop edges = %#v, want exactly one, to Exit", stops)
	}
	from := stops[0].From
	var normalTo model.BlockID = -1
	for _, e := range edgesFrom(g, from) {
		if e.Kind == model.EdgeNormal {
			normalTo = e.To
		}
	}
	if normalTo == -1 {
		t.Fatalf("block %d has a trusted-stop edge but no normal continuation edge: %#v", from, edgesFrom(g, from))
	}
	// The statement after the trusted call must live in code reachable
	// from the trusted call's block via the normal edge.
	found := false
	for id := range reachable(g, normalTo) {
		for _, in := range g.Block(id).Instructions {
			if in.Callee == "example.test/input.after" {
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("the call after the trusted call is not reachable through its normal continuation")
	}
}

// The regression the audit describes (F1): delegating a context before a
// later unconditional loop must not delete that loop from the graph.
func TestTrustedCallBeforeLoopLeavesLoopReachableAndUnresolved(t *testing.T) {
	g := buildTrusted(t, `package p
func delegate() {}
func F() {
	delegate()
	for {
	}
}
`, "F", "delegate")

	reach := g.Reachable(g.Entry)
	sccs := 0
	for _, scc := range g.SCCs() {
		if !g.IsPersistentSCC(scc) {
			continue
		}
		sccs++
		if !reach[scc[0]] {
			t.Fatalf("the loop after a trusted call is unreachable from Entry; it was deleted from the graph")
		}
		member := map[model.BlockID]bool{}
		for _, id := range scc {
			member[id] = true
		}
		for _, id := range scc {
			for _, e := range g.Block(id).Successors {
				if !member[e.To] {
					t.Fatalf("a loop that does not contain the trusted call has an exit edge %#v", e)
				}
			}
		}
	}
	if sccs != 1 {
		t.Fatalf("persistent SCCs = %d, want 1", sccs)
	}
}

// A trusted call inside the loop still credits that loop with an escape:
// the trusted-stop edge leaves the loop's own SCC.
func TestTrustedCallInsideLoopStillLeavesItsSCC(t *testing.T) {
	g := buildTrusted(t, `package p
func delegate() {}
func F() {
	for {
		delegate()
	}
}
`, "F", "delegate")

	found := false
	for _, scc := range g.SCCs() {
		if !g.IsPersistentSCC(scc) {
			continue
		}
		member := map[model.BlockID]bool{}
		for _, id := range scc {
			member[id] = true
		}
		for _, id := range scc {
			for _, e := range g.Block(id).Successors {
				if e.Kind == model.EdgeTrustedStop && !member[e.To] && e.To == g.Exit {
					found = true
				}
			}
		}
	}
	if !found {
		t.Fatalf("no trusted-stop edge leaves the loop's SCC")
	}
}

// With no trust predicate the graph is purely structural: no trusted-stop
// edges and no extra blocks.
func TestNoPredicateProducesNoTrustedEdges(t *testing.T) {
	g := build(t, `package p
func delegate() {}
func F() {
	delegate()
	delegate()
}
`, "F")
	if n := len(edgesOfKind(g, model.EdgeTrustedStop)); n != 0 {
		t.Fatalf("trusted-stop edges = %d, want 0", n)
	}
	if got := blocksByKind(g, "after-trusted-call"); len(got) != 0 {
		t.Fatalf("after-trusted-call blocks = %v, want none", got)
	}
}
