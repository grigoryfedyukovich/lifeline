package cfg

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"strings"
	"testing"
)

func TestBuildContextCancelsInsideLargeFunction(t *testing.T) {
	var bodyText strings.Builder
	bodyText.WriteString("package p\nfunc f() {\n")
	for i := 0; i < 2000; i++ {
		bodyText.WriteString("g()\n")
	}
	bodyText.WriteString("}\nfunc g() {}\n")
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "input.go", bodyText.String(), 0)
	if err != nil {
		t.Fatal(err)
	}
	info := &types.Info{Defs: map[*ast.Ident]types.Object{}, Uses: map[*ast.Ident]types.Object{}, Types: map[ast.Expr]types.TypeAndValue{}, Selections: map[*ast.SelectorExpr]*types.Selection{}}
	if _, err := (&types.Config{}).Check("p", fset, []*ast.File{file}, info); err != nil {
		t.Fatal(err)
	}
	fd := file.Decls[0].(*ast.FuncDecl)
	ctx, cancel := context.WithCancel(context.Background())
	seen := 0
	trusted := func(*ast.CallExpr) bool {
		seen++
		if seen == 10 {
			cancel()
		}
		return false
	}
	g, _, err := BuildContext(ctx, "f", fset, fd.Body, info, trusted)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("BuildContext error = %v, want context.Canceled", err)
	}
	instructions := 0
	for _, block := range g.Blocks {
		instructions += len(block.Instructions)
	}
	if instructions >= 2000 {
		t.Fatalf("cancellation did not interrupt CFG construction: %d instructions", instructions)
	}
}
