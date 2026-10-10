package frontend

import (
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"testing"

	"github.com/gfedyukovich/lifeline/internal/model"
)

func TestGroupBalanceIndexSeparatesIndependentGroups(t *testing.T) {
	const src = `package p
import "sync"
func f() {
	var a, b sync.WaitGroup
	a.Add(1)
	go func() { defer a.Done() }()
	a.Wait()
	b.Add(1)
	go func() { defer b.Done() }()
	b.Wait()
}`
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "input.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	info := &types.Info{
		Types:      map[ast.Expr]types.TypeAndValue{},
		Defs:       map[*ast.Ident]types.Object{},
		Uses:       map[*ast.Ident]types.Object{},
		Selections: map[*ast.SelectorExpr]*types.Selection{},
	}
	if _, err := (&types.Config{Importer: importer.Default()}).Check("example.test/p", fset, []*ast.File{file}, info); err != nil {
		t.Fatal(err)
	}

	var body *ast.BlockStmt
	var aObj, bObj types.Object
	ast.Inspect(file, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.FuncDecl:
			if x.Name.Name == "f" {
				body = x.Body
			}
		case *ast.Ident:
			if obj := info.Defs[x]; obj != nil {
				switch x.Name {
				case "a":
					aObj = obj
				case "b":
					bObj = obj
				}
			}
		}
		return true
	})
	if body == nil || aObj == nil || bObj == nil {
		t.Fatalf("failed to find function/groups: body=%v a=%v b=%v", body != nil, aObj, bObj)
	}

	groups := []*groupState{
		{group: model.JoinGroup{Kind: "waitgroup"}, obj: aObj},
		{group: model.JoinGroup{Kind: "waitgroup"}, obj: bObj},
	}
	idx := buildGroupBalanceIndex(body, groups, info)
	if got := idx.waitPositions[aObj]; len(got) != 1 || got[0] != 3 {
		t.Fatalf("a wait positions = %v, want [3]", got)
	}
	if got := idx.waitPositions[bObj]; len(got) != 1 || got[0] != 6 {
		t.Fatalf("b wait positions = %v, want [6]", got)
	}
	// a.Add and b.Add are separate top-level statements. The index should
	// let the per-group walker reject the unrelated one without entering it.
	if !idx.has(idx.topLevel[1], aObj) || idx.has(idx.topLevel[1], bObj) {
		t.Fatalf("a.Add relevance: a=%v b=%v", idx.has(idx.topLevel[1], aObj), idx.has(idx.topLevel[1], bObj))
	}
	if !idx.has(idx.topLevel[4], bObj) || idx.has(idx.topLevel[4], aObj) {
		t.Fatalf("b.Add relevance: a=%v b=%v", idx.has(idx.topLevel[4], aObj), idx.has(idx.topLevel[4], bObj))
	}
}

func TestGroupBalanceIndexPreservesIndependentDiagnostics(t *testing.T) {
	diags := analyzeSource(t, `package p
import "sync"
func Start() {
	var good, bad sync.WaitGroup
	good.Add(1)
	go func() { defer good.Done() }()
	good.Wait()
	bad.Add(2)
	go func() { defer bad.Done() }()
	bad.Wait()
}`)
	var countMismatch int
	for _, d := range diags {
		if d.RuleID == "LL1003" && d.Message != "" {
			// The exact wording is intentionally not asserted here; the existing
			// focused tests own wording. We only require exactly one bad-group
			// lifecycle finding and no contamination from the balanced group.
			countMismatch++
		}
	}
	if countMismatch != 1 {
		t.Fatalf("diagnostics = %#v; want exactly one LL1003 finding", diags)
	}
}
