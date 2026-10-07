package frontend

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gfedyukovich/lifeline/internal/config"
)

type cancelAfterChecks struct {
	context.Context
	mu   sync.Mutex
	left int
}

func (c *cancelAfterChecks) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.left <= 0 {
		return context.Canceled
	}
	c.left--
	return nil
}

func (c *cancelAfterChecks) Deadline() (time.Time, bool) { return time.Time{}, false }
func (c *cancelAfterChecks) Done() <-chan struct{}       { return nil }
func (c *cancelAfterChecks) Value(key any) any           { return nil }

func TestBuildContextInterruptsFrontendTraversal(t *testing.T) {
	var source strings.Builder
	source.WriteString("package p\nfunc f() {\n")
	for i := 0; i < 4000; i++ {
		source.WriteString("_ = 1\n")
	}
	source.WriteString("}\n")
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "input.go", source.String(), 0)
	if err != nil {
		t.Fatal(err)
	}
	info := &types.Info{
		Types: map[ast.Expr]types.TypeAndValue{}, Defs: map[*ast.Ident]types.Object{}, Uses: map[*ast.Ident]types.Object{},
		Selections: map[*ast.SelectorExpr]*types.Selection{}, Scopes: map[ast.Node]*types.Scope{}, Implicits: map[ast.Node]types.Object{},
	}
	pkg, err := (&types.Config{}).Check("p", fset, []*ast.File{file}, info)
	if err != nil {
		t.Fatal(err)
	}
	ctx := &cancelAfterChecks{Context: context.Background(), left: 100}
	program, err := BuildContext(ctx, Input{Fset: fset, Files: []*ast.File{file}, Pkg: pkg, Info: info}, config.Default())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("BuildContext error = %v, want context.Canceled", err)
	}
	if len(program.Functions) != 0 {
		t.Fatalf("partially built function was published as complete: %d functions", len(program.Functions))
	}
}
