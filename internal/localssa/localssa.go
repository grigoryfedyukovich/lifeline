// Package localssa builds a deliberately small, deterministic SSA-like
// summary for lifecycle analysis. It versions local definitions and records
// calls, go statements, defers, loops, selects, and returns. It is not a
// replacement for golang.org/x/tools/go/ssa; its narrow purpose is to keep
// Lifeline's recognizers independent from parser node identity.
package localssa

import (
	"context"
	"fmt"
	"go/ast"
	"go/token"
	"go/types"

	"golang.org/x/tools/go/types/typeutil"
)

type Op string

const (
	OpAssign Op = "assign"
	OpCall   Op = "call"
	OpGo     Op = "go"
	OpDefer  Op = "defer"
	OpLoop   Op = "loop"
	OpSelect Op = "select"
	OpReturn Op = "return"
)

type Instruction struct {
	Index   int
	Op      Op
	Pos     token.Pos
	End     token.Pos
	Callee  string
	Defines []string
	Uses    []string
}

type Function struct {
	Name         string
	Instructions []Instruction
}

func Build(name string, body *ast.BlockStmt, info *types.Info) Function {
	f, _ := BuildContext(context.Background(), name, body, info)
	return f
}

// BuildContext is Build with cooperative cancellation. It polls ctx for every
// visited AST node and while collecting identifier uses so one large function
// cannot make a standalone deadline wait for this debug/inspection IR to finish.
func BuildContext(ctx context.Context, name string, body *ast.BlockStmt, info *types.Info) (Function, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	f := Function{Name: name}
	versions := map[types.Object]int{}
	var buildErr error
	appendInstruction := func(op Op, n ast.Node, callee string, defs, uses []string) {
		f.Instructions = append(f.Instructions, Instruction{
			Index: len(f.Instructions), Op: op, Pos: n.Pos(), End: n.End(), Callee: callee, Defines: defs, Uses: uses,
		})
	}
	ast.Inspect(body, func(n ast.Node) bool {
		if buildErr != nil {
			return false
		}
		if err := ctx.Err(); err != nil {
			buildErr = err
			return false
		}
		if n == nil {
			return true
		}
		switch x := n.(type) {
		case *ast.FuncLit:
			// A nested function has its own locals and lifecycle. Its instructions
			// must not be merged into the enclosing function's summary.
			return false
		case *ast.AssignStmt:
			defs := make([]string, 0, len(x.Lhs))
			for _, lhs := range x.Lhs {
				id, ok := lhs.(*ast.Ident)
				if !ok || id.Name == "_" {
					continue
				}
				obj := info.ObjectOf(id)
				if obj == nil {
					continue
				}
				versions[obj]++
				defs = append(defs, fmt.Sprintf("%s#%d", id.Name, versions[obj]))
			}
			appendInstruction(OpAssign, x, "", defs, identifierUsesContext(ctx, x, info, versions, &buildErr))
		case *ast.CallExpr:
			appendInstruction(OpCall, x, calleeName(info, x), nil, identifierUsesContext(ctx, x, info, versions, &buildErr))
		case *ast.GoStmt:
			appendInstruction(OpGo, x, calleeName(info, x.Call), nil, identifierUsesContext(ctx, x, info, versions, &buildErr))
		case *ast.DeferStmt:
			appendInstruction(OpDefer, x, calleeName(info, x.Call), nil, identifierUsesContext(ctx, x, info, versions, &buildErr))
		case *ast.ForStmt, *ast.RangeStmt:
			appendInstruction(OpLoop, n, "", nil, identifierUsesContext(ctx, n, info, versions, &buildErr))
		case *ast.SelectStmt:
			appendInstruction(OpSelect, x, "", nil, identifierUsesContext(ctx, x, info, versions, &buildErr))
		case *ast.ReturnStmt:
			appendInstruction(OpReturn, x, "", nil, identifierUsesContext(ctx, x, info, versions, &buildErr))
		}
		return true
	})
	return f, buildErr
}

func calleeName(info *types.Info, call *ast.CallExpr) string {
	if call == nil {
		return ""
	}
	obj := typeutil.Callee(info, call)
	fn, ok := obj.(*types.Func)
	if !ok {
		if obj != nil {
			return obj.Name()
		}
		return ""
	}
	if fn.Pkg() == nil {
		return fn.Name()
	}
	if sig, ok := fn.Type().(*types.Signature); ok && sig.Recv() != nil {
		recv := sig.Recv().Type()
		if ptr, ok := recv.(*types.Pointer); ok {
			recv = ptr.Elem()
		}
		if named, ok := recv.(*types.Named); ok && named.Obj().Pkg() != nil {
			return named.Obj().Pkg().Path() + "." + named.Obj().Name() + "." + fn.Name()
		}
	}
	return fn.Pkg().Path() + "." + fn.Name()
}

func identifierUses(n ast.Node, info *types.Info, versions map[types.Object]int) []string {
	var err error
	return identifierUsesContext(context.Background(), n, info, versions, &err)
}

func identifierUsesContext(ctx context.Context, n ast.Node, info *types.Info, versions map[types.Object]int, buildErr *error) []string {
	var out []string
	ast.Inspect(n, func(child ast.Node) bool {
		if *buildErr != nil {
			return false
		}
		if err := ctx.Err(); err != nil {
			*buildErr = err
			return false
		}
		if _, ok := child.(*ast.FuncLit); ok && child != n {
			return false
		}
		id, ok := child.(*ast.Ident)
		if !ok || id.Name == "_" {
			return true
		}
		obj := info.Uses[id]
		if obj == nil {
			return true
		}
		v := versions[obj]
		if v == 0 {
			out = append(out, id.Name+"#input")
		} else {
			out = append(out, fmt.Sprintf("%s#%d", id.Name, v))
		}
		return true
	})
	return out
}
