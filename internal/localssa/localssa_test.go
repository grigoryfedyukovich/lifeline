package localssa

import (
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"testing"
)

func TestVersionsDefinitions(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "x.go", `package p
func f(){ x := 1; x = 2; go func(){ _ = x }() }
`, 0)
	if err != nil {
		t.Fatal(err)
	}
	info := &types.Info{Defs: map[*ast.Ident]types.Object{}, Uses: map[*ast.Ident]types.Object{}, Types: map[ast.Expr]types.TypeAndValue{}}
	pkg, err := (&types.Config{}).Check("p", fset, []*ast.File{file}, info)
	_ = pkg
	if err != nil {
		t.Fatal(err)
	}
	fd := file.Decls[0].(*ast.FuncDecl)
	ir := Build("f", fd.Body, info)
	if len(ir.Instructions) == 0 {
		t.Fatal("empty IR")
	}
	found := false
	for _, in := range ir.Instructions {
		for _, d := range in.Defines {
			if d == "x#2" {
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("second definition not versioned: %#v", ir.Instructions)
	}
}

func TestCalleeIsPopulatedAndNestedBodiesAreIsolated(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "x.go", `package p
func helper() {}
func f(){ helper(); go helper(); _ = func(){ helper() } }
`, 0)
	if err != nil {
		t.Fatal(err)
	}
	info := &types.Info{Defs: map[*ast.Ident]types.Object{}, Uses: map[*ast.Ident]types.Object{}, Types: map[ast.Expr]types.TypeAndValue{}, Selections: map[*ast.SelectorExpr]*types.Selection{}}
	if _, err := (&types.Config{}).Check("example.test/p", fset, []*ast.File{file}, info); err != nil {
		t.Fatal(err)
	}
	fd := file.Decls[1].(*ast.FuncDecl)
	ir := Build("f", fd.Body, info)
	calls := 0
	for _, in := range ir.Instructions {
		if in.Callee == "example.test/p.helper" {
			calls++
		}
	}
	if calls != 3 { // call + go instruction + call nested beneath the go statement
		t.Fatalf("helper callee count = %d; IR = %#v", calls, ir.Instructions)
	}
}
