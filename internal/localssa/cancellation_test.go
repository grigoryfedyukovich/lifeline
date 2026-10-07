package localssa

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
)

type stepContext struct {
	context.Context
	mu   sync.Mutex
	left int
}

func (c *stepContext) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.left <= 0 {
		return context.Canceled
	}
	c.left--
	return nil
}

func (c *stepContext) Deadline() (time.Time, bool) { return time.Time{}, false }
func (c *stepContext) Done() <-chan struct{}       { return nil }
func (c *stepContext) Value(key any) any           { return nil }

func TestBuildContextCancelsDuringTraversal(t *testing.T) {
	var source strings.Builder
	source.WriteString("package p\nfunc f() {\n")
	for i := 0; i < 2000; i++ {
		source.WriteString("_ = 1\n")
	}
	source.WriteString("}\n")
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "input.go", source.String(), 0)
	if err != nil {
		t.Fatal(err)
	}
	info := &types.Info{Defs: map[*ast.Ident]types.Object{}, Uses: map[*ast.Ident]types.Object{}, Types: map[ast.Expr]types.TypeAndValue{}}
	if _, err := (&types.Config{}).Check("p", fset, []*ast.File{file}, info); err != nil {
		t.Fatal(err)
	}
	fd := file.Decls[0].(*ast.FuncDecl)
	ctx := &stepContext{Context: context.Background(), left: 40}
	ir, err := BuildContext(ctx, "f", fd.Body, info)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("BuildContext error = %v, want context.Canceled", err)
	}
	if len(ir.Instructions) >= 2000 {
		t.Fatalf("cancellation did not interrupt local IR construction: %d instructions", len(ir.Instructions))
	}
}
