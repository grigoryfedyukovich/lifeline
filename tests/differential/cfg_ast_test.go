// Package differential holds fixtures from the AST->CFG migration
// (docs/cfg-migration-plan.md): explicit regression tests for the engine's
// lifecycle-diagnostic behavior, run directly against internal/cfg's graph
// algorithms alongside the full engine, so a verdict change is provable
// against a concrete before/after rather than taken on faith.
//
// TestFixed_NestedLoopsMixedResolution is the load-bearing case: it started
// as a fixture documenting a known false negative in the AST-based engine
// (see git history / CHANGELOG for the Phase 2 entry), and was updated in
// place once the CFG/SCC-based verdict (internal/engine/cfg_verdict.go)
// closed it, without ever changing what it asserts about the CFG itself.
package differential

import (
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"testing"

	flowgraph "github.com/gfedyukovich/lifeline/internal/cfg"
	"github.com/gfedyukovich/lifeline/internal/config"
	"github.com/gfedyukovich/lifeline/internal/engine"
	"github.com/gfedyukovich/lifeline/internal/frontend"
	"github.com/gfedyukovich/lifeline/internal/model"
)

type parsed struct {
	fset *token.FileSet
	file *ast.File
	info *types.Info
	pkg  *types.Package
}

func parse(t *testing.T, source string) parsed {
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
	pkg, err := (&types.Config{Importer: importer.Default()}).Check("example.test/input", fset, []*ast.File{file}, info)
	if err != nil {
		t.Fatal(err)
	}
	return parsed{fset: fset, file: file, info: info, pkg: pkg}
}

// currentDiagnostics runs today's full pipeline: internal/frontend's
// AST-based lifecycle summary followed by internal/engine's rule
// evaluation. This is the existing, unmodified contract.
func currentDiagnostics(t *testing.T, p parsed) []engine.Diagnostic {
	t.Helper()
	cfg := config.Default()
	program, err := frontend.Build(frontend.Input{Fset: p.fset, Files: []*ast.File{p.file}, Pkg: p.pkg, Info: p.info}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	return engine.Analyze(program, cfg)
}

// exitReachable builds a CFG for the named function's goroutine closure and
// reports whether its Exit block is reachable from Entry at all -- the
// structural question a future LL1002 would ask, in place of today's flat
// "InfiniteLoop && no evidence booleans" summary. funcName's body must
// directly contain a `go func(){...}()` closure (not a named-function
// target); fixtures should use that form so this resolves unambiguously.
func exitReachable(t *testing.T, p parsed, funcName string) bool {
	t.Helper()
	var body *ast.BlockStmt
	ast.Inspect(p.file, func(n ast.Node) bool {
		if fd, ok := n.(*ast.FuncDecl); ok && fd.Name.Name == funcName {
			body = fd.Body
		}
		return true
	})
	if body == nil {
		t.Fatalf("function %s not found", funcName)
	}
	var litBody *ast.BlockStmt
	ast.Inspect(body, func(n ast.Node) bool {
		if lit, ok := n.(*ast.FuncLit); ok && litBody == nil {
			litBody = lit.Body
			return false
		}
		return true
	})
	if litBody == nil {
		t.Fatalf("%s does not directly contain a goroutine closure (go func(){...}()); "+
			"this helper does not resolve named-function goroutine targets", funcName)
	}
	g := flowgraph.Build(funcName, p.fset, litBody, p.info, nil)
	return reachable(g, g.Entry)[g.Exit]
}

func reachable(g *model.CFG, start model.BlockID) map[model.BlockID]bool {
	seen := map[model.BlockID]bool{start: true}
	stack := []model.BlockID{start}
	for len(stack) > 0 {
		id := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		for _, e := range g.Block(id).Successors {
			if !seen[e.To] {
				seen[e.To] = true
				stack = append(stack, e.To)
			}
		}
	}
	return seen
}

func hasRule(diags []engine.Diagnostic, ruleID string) bool {
	for _, d := range diags {
		if d.RuleID == ruleID {
			return true
		}
	}
	return false
}

// TestFixed_NestedLoopsMixedResolution is the fixture for the residual gap
// documented (until now) in docs/limitations.md: two unconditional loops in
// the same goroutine, where only the inner one has its own break. Before
// Phase 2 of the AST->CFG migration, the AST-based engine tracked exit
// status as a single flag per goroutine, not per loop, so the inner loop's
// real exit incorrectly cleared the finding for the outer loop too, which
// never terminates. The CFG-based verdict (internal/engine/cfg_verdict.go)
// judges each persistent SCC independently, closing this gap.
//
// This fixture is deliberately paired with TestKnownFalseNegative-style
// framing in its history: it existed first as a regression test proving
// the gap was real and documented, then was updated (this version) to
// prove the fix once Phase 2 landed. Keeping both the "still broken" and
// "now fixed" assertions inline in the same file across that transition
// (via the CFG-direct assertion, which never changed) is what made the fix
// provable rather than assumed.
func TestFixed_NestedLoopsMixedResolution(t *testing.T) {
	p := parse(t, `package p
func Start() {
	go func() {
		for {
			for {
				break
			}
		}
	}()
}
`)
	diags := currentDiagnostics(t, p)
	if !hasRule(diags, "LL1002") {
		t.Fatalf("current engine should report LL1002: the inner break only exits the inner loop, "+
			"the outer loop never terminates -- if this stops firing, the CFG/SCC verdict regressed "+
			"back to the old flat-evidence behavior: diags=%#v", diags)
	}

	if exitReachable(t, p, "Start") {
		t.Fatalf("CFG should show Exit unreachable: the only break exits the inner loop, " +
			"the outer loop never terminates -- if this now shows reachable, the CFG builder regressed")
	}
}

// TestControl_SingleUnresolvedLoop is the "both mechanisms agree" control
// for the fixture above: a single unconditional loop with no exit of any
// kind. Both today's engine and the CFG should identify this as a real,
// unresolved problem. This exists so the false-negative fixture above is
// verified against a non-degenerate baseline, not an always-true assertion.
func TestControl_SingleUnresolvedLoop(t *testing.T) {
	p := parse(t, `package p
func Start() {
	go func() {
		for {
			work()
		}
	}()
}
func work() {}
`)
	diags := currentDiagnostics(t, p)
	if !hasRule(diags, "LL1002") {
		t.Fatalf("current engine should report LL1002 for a single unconditional loop with no exit: diags=%#v", diags)
	}
	if exitReachable(t, p, "Start") {
		t.Fatalf("CFG should show Exit unreachable: there is no break, return, or stop path anywhere")
	}
}

// TestControl_DirectBreakResolves is a second control: the simple, common
// case where the loop's own break resolves it. Both mechanisms should
// agree this is clean.
func TestControl_DirectBreakResolves(t *testing.T) {
	p := parse(t, `package p
func Start() {
	go func() {
		for {
			if shouldStop() {
				break
			}
			work()
		}
	}()
}
func work()            {}
func shouldStop() bool { return false }
`)
	diags := currentDiagnostics(t, p)
	if hasRule(diags, "LL1002") {
		t.Fatalf("current engine should not report LL1002: the loop's own break resolves it: diags=%#v", diags)
	}
	if !exitReachable(t, p, "Start") {
		t.Fatalf("CFG should show Exit reachable via the direct break")
	}
}
