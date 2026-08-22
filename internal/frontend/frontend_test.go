package frontend

import (
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"testing"

	"github.com/gfedyukovich/lifeline/internal/config"
	"github.com/gfedyukovich/lifeline/internal/engine"
)

func analyzeSource(t *testing.T, source string) []engine.Diagnostic {
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
	cfg := config.Default()
	program, err := Build(Input{Fset: fset, Files: []*ast.File{file}, Pkg: pkg, Info: info}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	return engine.Analyze(program, cfg)
}

func TestIgnoredContext(t *testing.T) {
	diags := analyzeSource(t, `package p
import "context"
var queue = make(chan int)
func Start(ctx context.Context) { go func(){ for { <-queue } }() }
`)
	if len(diags) != 1 || diags[0].RuleID != "LL1002" {
		t.Fatalf("diagnostics = %#v", diags)
	}
}

func TestContextSelectRecognized(t *testing.T) {
	diags := analyzeSource(t, `package p
import "context"
func Start(ctx context.Context) { go func(){ for { select { case <-ctx.Done(): return; default: } } }() }
`)
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
}

func TestLostCancelHasFix(t *testing.T) {
	diags := analyzeSource(t, `package p
import "context"
func Start(ctx context.Context) { child, _ := context.WithCancel(ctx); go func(){ <-child.Done() }() }
`)
	if len(diags) != 1 || diags[0].RuleID != "LL1001" || diags[0].SuggestedFix == nil {
		t.Fatalf("diagnostics = %#v", diags)
	}
}

func TestWaitGroupWithoutWait(t *testing.T) {
	diags := analyzeSource(t, `package p
import "sync"
func Start(){ var wg sync.WaitGroup; wg.Add(1); go func(){ defer wg.Done() }() }
`)
	if len(diags) != 1 || diags[0].RuleID != "LL1003" {
		t.Fatalf("diagnostics = %#v", diags)
	}
}

func TestWaitGroupJoin(t *testing.T) {
	diags := analyzeSource(t, `package p
import "sync"
func Start(){ var wg sync.WaitGroup; wg.Add(1); go func(){ defer wg.Done() }(); wg.Wait() }
`)
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
}

func TestNestedGoroutineLoopBelongsToInnerStart(t *testing.T) {
	diags := analyzeSource(t, `package p
func Start() {
	go func() {
		go func() { for {} }()
	}()
}
`)
	if len(diags) != 1 || diags[0].RuleID != "LL1002" || diags[0].Position.StartLine != 4 {
		t.Fatalf("diagnostics = %#v", diags)
	}
}

func TestNestedGoroutineStopDoesNotSuppressOuterLoop(t *testing.T) {
	diags := analyzeSource(t, `package p
import "context"
func Start(ctx context.Context) {
	go func() {
		for {}
		go func() { select { case <-ctx.Done(): return } }()
	}()
}
`)
	if len(diags) != 1 || diags[0].RuleID != "LL1002" || diags[0].Position.StartLine != 4 {
		t.Fatalf("diagnostics = %#v", diags)
	}
}

func TestEscapeAssignmentPairsBlankByPosition(t *testing.T) {
	for _, source := range []string{
		`package p
import "context"
func Start(parent context.Context) {
	_, cancel := context.WithCancel(parent)
	holder, _ := cancel, 0
	_ = holder
}
`,
		`package p
import "context"
func Start(parent context.Context) {
	_, cancel := context.WithCancel(parent)
	_, holder := 0, cancel
	_ = holder
}
`,
	} {
		diags := analyzeSource(t, source)
		if len(diags) != 0 {
			t.Fatalf("diagnostics = %#v", diags)
		}
	}
}

func TestErrgroupWithoutWait(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "input.go", `package p
import "golang.org/x/sync/errgroup"
func Start(){ var g errgroup.Group; g.Go(func() error { return nil }) }
`, parser.ParseComments)
	if err != nil {
		t.Fatal(err)
	}
	info := &types.Info{
		Types: make(map[ast.Expr]types.TypeAndValue), Defs: make(map[*ast.Ident]types.Object), Uses: make(map[*ast.Ident]types.Object),
		Selections: make(map[*ast.SelectorExpr]*types.Selection), Scopes: make(map[ast.Node]*types.Scope), Implicits: make(map[ast.Node]types.Object),
	}
	imp := &fixtureImporter{fallback: importer.Default(), errgroup: makeErrgroupPackage()}
	pkg, err := (&types.Config{Importer: imp}).Check("example.test/input", fset, []*ast.File{file}, info)
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	program, err := Build(Input{Fset: fset, Files: []*ast.File{file}, Pkg: pkg, Info: info}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	diags := engine.Analyze(program, cfg)
	if len(diags) != 1 || diags[0].RuleID != "LL1004" {
		t.Fatalf("diagnostics = %#v", diags)
	}
}

type fixtureImporter struct {
	fallback types.Importer
	errgroup *types.Package
}

func (i *fixtureImporter) Import(path string) (*types.Package, error) {
	if path == "golang.org/x/sync/errgroup" {
		return i.errgroup, nil
	}
	return i.fallback.Import(path)
}

func makeErrgroupPackage() *types.Package {
	pkg := types.NewPackage("golang.org/x/sync/errgroup", "errgroup")
	name := types.NewTypeName(token.NoPos, pkg, "Group", nil)
	named := types.NewNamed(name, types.NewStruct(nil, nil), nil)
	pkg.Scope().Insert(name)
	recv := types.NewVar(token.NoPos, pkg, "g", types.NewPointer(named))
	errorType := types.Universe.Lookup("error").Type()
	callback := types.NewSignature(nil, nil, types.NewTuple(types.NewVar(token.NoPos, nil, "", errorType)), false)
	goSig := types.NewSignature(recv, types.NewTuple(types.NewVar(token.NoPos, pkg, "f", callback)), nil, false)
	named.AddMethod(types.NewFunc(token.NoPos, pkg, "Go", goSig))
	waitSig := types.NewSignature(recv, nil, types.NewTuple(types.NewVar(token.NoPos, nil, "", errorType)), false)
	named.AddMethod(types.NewFunc(token.NoPos, pkg, "Wait", waitSig))
	pkg.MarkComplete()
	return pkg
}

func TestBreakAndChannelRangeRecognized(t *testing.T) {
	for _, source := range []string{
		`package p
func Start(){ go func(){ for { break } }() }
`,
		`package p
func Start(ch <-chan int){ go func(){ for range ch {} }() }
`,
	} {
		if diags := analyzeSource(t, source); len(diags) != 0 {
			t.Fatalf("diagnostics = %#v", diags)
		}
	}
}

func TestConfiguredStartAndStopWrappers(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "input.go", `package p
func launch(f func()) { f() }
func stopped() {}
func Start(){ launch(func(){ for { stopped() } }) }
`, parser.ParseComments)
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
	cfg := config.Default()
	cfg.StartWrappers = []string{"example.test/input.launch"}
	cfg.StopWrappers = []string{"example.test/input.stopped"}
	program, err := Build(Input{Fset: fset, Files: []*ast.File{file}, Pkg: pkg, Info: info}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if diags := engine.Analyze(program, cfg); len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
}

func TestMaxFunctionsBoundsIRAndDirectCallees(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "input.go", `package p
func First(){ go Second() }
func Second(){ for {} }
`, 0)
	if err != nil {
		t.Fatal(err)
	}
	info := &types.Info{
		Types: make(map[ast.Expr]types.TypeAndValue), Defs: make(map[*ast.Ident]types.Object), Uses: make(map[*ast.Ident]types.Object),
		Selections: make(map[*ast.SelectorExpr]*types.Selection), Scopes: make(map[ast.Node]*types.Scope), Implicits: make(map[ast.Node]types.Object),
	}
	pkg, err := (&types.Config{}).Check("example.test/input", fset, []*ast.File{file}, info)
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.MaxFunctions = 1
	program, err := Build(Input{Fset: fset, Files: []*ast.File{file}, Pkg: pkg, Info: info}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(program.Functions) != 1 || len(program.Functions[0].IR) == 0 || !program.Truncated {
		t.Fatalf("program = %#v", program)
	}
	diags := engine.Analyze(program, cfg)
	if len(diags) != 1 || diags[0].RuleID != "LL9001" {
		t.Fatalf("diagnostics = %#v", diags)
	}
}

func TestContextTypeUsesInterfaceIdentity(t *testing.T) {
	pkg := types.NewPackage("example.com/mycontext", "mycontext")
	named := types.NewNamed(types.NewTypeName(token.NoPos, pkg, "ContextWrapper", nil), types.NewStruct(nil, nil), nil)
	if isContextType(named, nil) {
		t.Fatal("unrelated type was classified as context.Context")
	}
}

// The following tests cover Phase 5 of the AST->CFG migration
// (docs/cfg-migration-plan.md): verifying, for a cancel/group value passed
// directly as an argument to a same-package function, whether that
// function's own body actually consumes it, instead of unconditionally
// treating any pass-as-argument as a discharged obligation.

func TestParameterPassing_VerifiedLeakFires(t *testing.T) {
	diags := analyzeSource(t, `package p
import "context"
func Start(parent context.Context) {
	ctx, cancel := context.WithCancel(parent)
	run(cancel)
	go work(ctx)
}
func run(c context.CancelFunc) {
	_ = c
}
func work(context.Context) {}
`)
	if len(diags) != 1 || diags[0].RuleID != "LL1001" {
		t.Fatalf("diagnostics = %#v", diags)
	}
	found := false
	for _, e := range diags[0].Evidence {
		if e.Kind == "parameter-not-consumed" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a parameter-not-consumed evidence entry, got %#v", diags[0].Evidence)
	}
}

func TestParameterPassing_VerifiedDeferConsumptionDoesNotFire(t *testing.T) {
	diags := analyzeSource(t, `package p
import "context"
func Start(parent context.Context) {
	ctx, cancel := context.WithCancel(parent)
	run(cancel)
	go work(ctx)
}
func run(c context.CancelFunc) {
	defer c()
}
func work(context.Context) {}
`)
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
}

func TestParameterPassing_VerifiedFurtherEscapeDoesNotFire(t *testing.T) {
	diags := analyzeSource(t, `package p
import "context"
type holder struct{ stop context.CancelFunc }
func Start(parent context.Context) {
	ctx, cancel := context.WithCancel(parent)
	run(cancel)
	go work(ctx)
}
func run(c context.CancelFunc) {
	_ = holder{stop: c}
}
func work(context.Context) {}
`)
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
}

// TestParameterPassing_UnresolvableCalleeFallsBack is the safety check:
// when the callee can't be resolved to a same-package function declaration
// (here, a func-typed parameter -- an indirect call), argumentConsumed
// must report verified=false and observeCall must fall back to the prior
// unconditional "assume transferred" behavior, not assume a leak. Getting
// this wrong would turn every indirect call passing a cancel func into a
// new false positive.
func TestParameterPassing_UnresolvableCalleeFallsBack(t *testing.T) {
	diags := analyzeSource(t, `package p
import "context"
func Start(parent context.Context, register func(context.CancelFunc)) {
	ctx, cancel := context.WithCancel(parent)
	register(cancel)
	go work(ctx)
}
func work(context.Context) {}
`)
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
}

func TestParameterPassing_GroupVerifiedLeakFires(t *testing.T) {
	diags := analyzeSource(t, `package p
import "sync"
func Start() {
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		work()
	}()
	run(&wg)
}
func run(g *sync.WaitGroup) {
	_ = g
}
func work() {}
`)
	if len(diags) != 1 || diags[0].RuleID != "LL1003" {
		t.Fatalf("diagnostics = %#v", diags)
	}
}

func TestParameterPassing_GroupVerifiedWaitDoesNotFire(t *testing.T) {
	diags := analyzeSource(t, `package p
import "sync"
func Start() {
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		work()
	}()
	run(&wg)
}
func run(g *sync.WaitGroup) {
	g.Wait()
}
func work() {}
`)
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
}

// TestParameterPassing_CalleeDeclaredFirstStillVerifies is a concrete
// regression guard for why computeParameterConsumption runs as its own
// complete pre-pass in Build, rather than being computed lazily on first
// use during buildFunction's main loop: here run (the callee) is declared
// before Start (the caller) in source order, the reverse of every other
// test in this file, so this is the one case that would behave differently
// under a naive "compute on demand, whichever function needs it first"
// implementation versus the pre-pass this code actually uses.
func TestParameterPassing_CalleeDeclaredFirstStillVerifies(t *testing.T) {
	diags := analyzeSource(t, `package p
import "context"
func run(c context.CancelFunc) {
	_ = c
}
func work(context.Context) {}
func Start(parent context.Context) {
	ctx, cancel := context.WithCancel(parent)
	run(cancel)
	go work(ctx)
}
`)
	if len(diags) != 1 || diags[0].RuleID != "LL1001" {
		t.Fatalf("callee-declared-first case should still be verified and fire LL1001, got diagnostics = %#v", diags)
	}
}

// The following tests cover chained (multi-hop) verification, now that
// computeParameterConsumption runs to an interprocedural fixed point
// (docs/cfg-migration-plan.md, Phase 5's interprocedural extension)
// instead of a single declaration-order pass: Build keeps re-running it
// for every function until a full sweep changes nothing, so a chain
// resolves to the same answer regardless of which order its functions
// happen to be declared in. Both declaration orders below cover the same
// 3-hop leak/non-leak pair a single pass could only catch in one of the
// two orders (see git history for the previous, order-dependent
// behavior); a resolvable same-package chain is no longer a source of
// declaration-order-dependent results.

func TestParameterPassing_ChainedLeakCaughtWhenCalleesDeclaredFirst(t *testing.T) {
	diags := analyzeSource(t, `package p
import "context"
func hop3(c context.CancelFunc) { _ = c }
func hop2(c context.CancelFunc) { hop3(c) }
func hop1(c context.CancelFunc) { hop2(c) }
func work(context.Context) {}
func Start(parent context.Context) {
	ctx, cancel := context.WithCancel(parent)
	hop1(cancel)
	go work(ctx)
}
`)
	if len(diags) != 1 || diags[0].RuleID != "LL1001" {
		t.Fatalf("3-hop leak with leaf-first declaration order should be caught, got diagnostics = %#v", diags)
	}
}

// TestParameterPassing_ChainedLeakCaughtWhenCallerDeclaredFirst is the
// same 3-hop leak as the test above, with the declaration order reversed
// (caller first, leaf last -- the more common style, and the order that
// used to be the "honest gap" before the fixed point). It must now also
// fire: the whole point of iterating computeParameterConsumption to a
// fixed point, rather than running it once, is that this order no longer
// changes the answer.
func TestParameterPassing_ChainedLeakCaughtWhenCallerDeclaredFirst(t *testing.T) {
	diags := analyzeSource(t, `package p
import "context"
func Start(parent context.Context) {
	ctx, cancel := context.WithCancel(parent)
	hop1(cancel)
	go work(ctx)
}
func hop1(c context.CancelFunc) { hop2(c) }
func hop2(c context.CancelFunc) { hop3(c) }
func hop3(c context.CancelFunc) { _ = c }
func work(context.Context) {}
`)
	if len(diags) != 1 || diags[0].RuleID != "LL1001" {
		t.Fatalf("3-hop leak with caller-first declaration order should now also be caught, got diagnostics = %#v", diags)
	}
}

// TestParameterPassing_ChainedConsumptionCaughtRegardlessOfOrder mirrors
// the leak tests above with the non-leaking case (the leaf hop actually
// calls the cancel function) in both declaration orders, confirming the
// fixed point doesn't just flip every chain to "leak" -- it converges to
// the correct answer either way.
func TestParameterPassing_ChainedConsumptionCaughtRegardlessOfOrder(t *testing.T) {
	calleesFirst := analyzeSource(t, `package p
import "context"
func hop3(c context.CancelFunc) { c() }
func hop2(c context.CancelFunc) { hop3(c) }
func hop1(c context.CancelFunc) { hop2(c) }
func work(context.Context) {}
func Start(parent context.Context) {
	ctx, cancel := context.WithCancel(parent)
	hop1(cancel)
	go work(ctx)
}
`)
	if len(calleesFirst) != 0 {
		t.Fatalf("3-hop consumption with leaf-first declaration order should not fire, got diagnostics = %#v", calleesFirst)
	}
	callerFirst := analyzeSource(t, `package p
import "context"
func Start(parent context.Context) {
	ctx, cancel := context.WithCancel(parent)
	hop1(cancel)
	go work(ctx)
}
func hop1(c context.CancelFunc) { hop2(c) }
func hop2(c context.CancelFunc) { hop3(c) }
func hop3(c context.CancelFunc) { c() }
func work(context.Context) {}
`)
	if len(callerFirst) != 0 {
		t.Fatalf("3-hop consumption with caller-first declaration order should not fire, got diagnostics = %#v", callerFirst)
	}
}

// TestParameterPassing_MutualRecursionConverges is a cycle in the
// dependency graph the fixed point must still terminate on and resolve
// correctly: two functions that only ever pass the cancel function to
// each other, never calling or otherwise consuming it, is a genuine leak
// (the least fixed point -- "not consumed unless proven otherwise" --
// is the correct, safe answer for a cycle with no base-case evidence
// either way), not a hang and not a false negative from the cycle itself.
func TestParameterPassing_MutualRecursionConverges(t *testing.T) {
	diags := analyzeSource(t, `package p
import "context"
func work(context.Context) {}
func Start(parent context.Context) {
	ctx, cancel := context.WithCancel(parent)
	ping(cancel, 0)
	go work(ctx)
}
func ping(c context.CancelFunc, n int) {
	if n < 10 {
		pong(c, n+1)
	}
}
func pong(c context.CancelFunc, n int) {
	if n < 10 {
		ping(c, n+1)
	}
}
`)
	if len(diags) != 1 || diags[0].RuleID != "LL1001" {
		t.Fatalf("mutual recursion with no consumption anywhere should fire LL1001, got diagnostics = %#v", diags)
	}
}

// TestParameterPassing_SelfRecursionWithConsumptionConverges is the same
// cycle shape as above, but with a base case that actually calls the
// cancel function, confirming the fixed point still finds real
// consumption through a self-recursive call rather than only through
// calls to other functions.
func TestParameterPassing_SelfRecursionWithConsumptionConverges(t *testing.T) {
	diags := analyzeSource(t, `package p
import "context"
func work(context.Context) {}
func Start(parent context.Context) {
	ctx, cancel := context.WithCancel(parent)
	countdown(cancel, 3)
	go work(ctx)
}
func countdown(c context.CancelFunc, n int) {
	if n <= 0 {
		c()
		return
	}
	countdown(c, n-1)
}
`)
	if len(diags) != 0 {
		t.Fatalf("self-recursion that eventually consumes the cancel function should not fire, got diagnostics = %#v", diags)
	}
}
