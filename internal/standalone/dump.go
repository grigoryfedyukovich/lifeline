package standalone

import (
	"context"
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"io"
	"sort"

	flowgraph "github.com/gfedyukovich/lifeline/internal/cfg"
	"github.com/gfedyukovich/lifeline/internal/config"
)

// dumpCFGs builds and renders a CFG for every named function declaration
// across the packages matching patterns. It bypasses the normal lifecycle
// analysis pipeline entirely -- no frontend.Build, no engine.Analyze, no
// report -- because this is a development/debugging aid for inspecting the
// intermediate representation directly, not a diagnostic report. Nothing in
// the rule engine consumes a CFG yet; this exists so the graph can be
// validated against real code before anything is built on top of it.
//
// Unlike the normal analysis path, this does not filter out generated
// files or apply ignore_paths: someone asking to see a CFG most likely
// wants to see every function that's there, not a lint-scoped subset.
func dumpCFGs(ctx context.Context, patterns []string, cfg config.Config, format string, w io.Writer) error {
	packages, err := listPackages(ctx, patterns)
	if err != nil {
		return err
	}
	exports := map[string]string{}
	for _, p := range packages {
		if p.Export != "" {
			exports[p.ImportPath] = p.Export
		}
	}
	var roots []listedPackage
	for _, p := range packages {
		if p.DepOnly || p.ForTest != "" {
			continue
		}
		if p.Error != nil {
			return fmt.Errorf("package %s: %s", p.ImportPath, p.Error.Err)
		}
		if len(p.GoFiles)+len(p.CgoFiles) == 0 {
			continue
		}
		roots = append(roots, p)
	}
	sort.Slice(roots, func(i, j int) bool { return roots[i].ImportPath < roots[j].ImportPath })

	for _, p := range roots {
		if err := ctx.Err(); err != nil {
			return err
		}
		loaded, err := loadOne(p, exports, cfg.IncludeTests)
		if err != nil {
			return fmt.Errorf("load %s: %w", p.ImportPath, err)
		}
		for _, file := range loaded.Files {
			for _, d := range file.Decls {
				fd, ok := d.(*ast.FuncDecl)
				if !ok || fd.Body == nil {
					continue
				}
				dumpOne(w, p.ImportPath+"."+funcDisplayName(fd), fd.Body, loaded.Fset, loaded.Info, format)
				dumpNestedLiterals(w, p.ImportPath+"."+funcDisplayName(fd), fd.Body, loaded.Fset, loaded.Info, format)
			}
		}
	}
	return nil
}

func funcDisplayName(fd *ast.FuncDecl) string {
	if fd.Recv == nil || len(fd.Recv.List) == 0 {
		return fd.Name.Name
	}
	recv := fd.Recv.List[0].Type
	if star, ok := recv.(*ast.StarExpr); ok {
		if id, ok := star.X.(*ast.Ident); ok {
			return "(*" + id.Name + ")." + fd.Name.Name
		}
	}
	if id, ok := recv.(*ast.Ident); ok {
		return id.Name + "." + fd.Name.Name
	}
	return fd.Name.Name
}

func dumpOne(w io.Writer, name string, body *ast.BlockStmt, fset *token.FileSet, info *types.Info, format string) {
	// nil: -dump cfg shows the purely structural graph, with no config- or
	// tracked-context-driven trust decisions applied (see internal/frontend
	// for where those get built and passed for the real analysis).
	g, _ := flowgraph.Build(name, fset, body, info, nil)
	switch format {
	case "dot":
		fmt.Fprint(w, flowgraph.RenderDOT(g))
	default:
		fmt.Fprint(w, flowgraph.RenderText(g))
	}
}

// dumpNestedLiterals finds every function literal directly within body
// (not itself nested inside another literal already found) and dumps its
// CFG too, recursing into each one for literals nested further still. This
// matters because the interesting control flow in a concurrency analyzer's
// own domain -- a worker's loop, its select, its stop path -- overwhelmingly
// lives inside a goroutine closure, not a top-level named function; without
// this, -dump cfg would show everything except the code it exists to show.
// Names follow Go's own runtime convention for anonymous functions:
// "parent.func1", "parent.func2", ..., numbered per enclosing scope in
// textual order, with "parent.func1.func1" for a literal nested in one.
func dumpNestedLiterals(w io.Writer, parentName string, body ast.Node, fset *token.FileSet, info *types.Info, format string) {
	counter := 0
	ast.Inspect(body, func(n ast.Node) bool {
		lit, ok := n.(*ast.FuncLit)
		if !ok {
			return true
		}
		counter++
		name := fmt.Sprintf("%s.func%d", parentName, counter)
		dumpOne(w, name, lit.Body, fset, info, format)
		dumpNestedLiterals(w, name, lit.Body, fset, info, format)
		return false
	})
}
