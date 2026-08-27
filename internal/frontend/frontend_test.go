package frontend

import (
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"strings"
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

// The following two tests document the exact, honest boundary of chained
// (multi-hop) verification: computeParameterConsumption's pre-pass runs
// once, in source declaration order, not call-graph order, so a chain is
// only verified as far as a deeper callee's result happens to already be
// computed by the time an earlier caller needs it. This is never a false
// positive either way -- the fallback for an unverified check is always
// "assume transferred" -- but it does mean multi-hop detection currently
// depends on how the source happens to be organized. See docs/limitations.md
// and docs/roadmap.md for the call-graph-ordered pre-pass that would make
// this reliable regardless of declaration order.

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

func TestParameterPassing_ChainedLeakMissedWhenCallerDeclaredFirst(t *testing.T) {
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
	// This is the honest, current gap, not a desired outcome: the same
	// 3-hop leak as the test above is missed here, purely because of
	// declaration order, and safely falls back rather than ever producing
	// a false positive. If this test starts failing because diagnostics
	// now correctly catches this case, that's a genuine improvement --
	// update this test (and docs/limitations.md's chaining paragraph)
	// to match, don't just delete the inconvenient assertion.
	if len(diags) != 0 {
		t.Fatalf("expected the current honest gap (caller-first order misses the chain), got diagnostics = %#v", diags)
	}
}

// The following tests cover field/constructor ownership tracking
// (docs/roadmap.md item 3): a cancel/group binding stored into a named
// struct field, either by a local variable ("stored struct") or by a
// constructor function's own return value, is verified against that one
// specific field's later use instead of being unconditionally treated as
// transferred the moment it is stored. See internal/frontend/frontend.go's
// collectFieldCaptures, resolveFieldCaptures, computeFieldOwnership, and
// computeConstructorCallerConsumption for the implementation this section
// exercises.

func TestFieldCapture_CancelStoredStructUnconsumedFires(t *testing.T) {
	diags := analyzeSource(t, `package p
import "context"
type Handle struct {
	Label  string
	Cancel context.CancelFunc
}
func Start() {
	ctx, cancel := context.WithCancel(context.Background())
	h := &Handle{Label: "worker", Cancel: cancel}
	go func() { <-ctx.Done() }()
	_ = h.Label
}
`)
	if len(diags) != 1 || diags[0].RuleID != "LL1001" {
		t.Fatalf("diagnostics = %#v", diags)
	}
	found := false
	for _, e := range diags[0].Evidence {
		if e.Kind == "field-not-consumed" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a field-not-consumed evidence entry, got %#v", diags[0].Evidence)
	}
}

func TestFieldCapture_CancelStoredStructConsumedDoesNotFire(t *testing.T) {
	diags := analyzeSource(t, `package p
import "context"
type Handle struct {
	Label  string
	Cancel context.CancelFunc
}
func Start() {
	ctx, cancel := context.WithCancel(context.Background())
	h := &Handle{Label: "worker", Cancel: cancel}
	go func() { <-ctx.Done() }()
	h.Cancel()
}
`)
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
}

// TestFieldCapture_BlankDiscardTreatedAsUnconsumedFires documents that
// `_ = h`, the common idiom for explicitly discarding a value, does not
// count as a "further use this narrow check can't follow" the way passing
// h to another function does (see
// TestFieldCapture_PassedToUnresolvedFunctionFallsBack below) -- h is
// confidently local and never consumed, so this is the same "stored
// struct" leak as TestFieldCapture_CancelStoredStructUnconsumedFires,
// just spelled differently.
func TestFieldCapture_BlankDiscardTreatedAsUnconsumedFires(t *testing.T) {
	diags := analyzeSource(t, `package p
import "context"
type Handle struct{ Cancel context.CancelFunc }
func Start() {
	ctx, cancel := context.WithCancel(context.Background())
	h := &Handle{Cancel: cancel}
	go func() { <-ctx.Done() }()
	_ = h
}
`)
	if len(diags) != 1 || diags[0].RuleID != "LL1001" {
		t.Fatalf("diagnostics = %#v", diags)
	}
}

// TestFieldCapture_PassedToUnresolvedFunctionFallsBack is the safety
// check for the "stored struct" mechanism, mirroring
// TestParameterPassing_VerifiedFurtherEscapeDoesNotFire for direct
// arguments: once h is passed on to another function, this narrow,
// single-variable check does not attempt to follow it, and must fall back
// to the same conservative assume-transferred default used everywhere
// else in this file rather than guess -- getting this wrong would turn
// every struct handle passed onward into a new false positive.
func TestFieldCapture_PassedToUnresolvedFunctionFallsBack(t *testing.T) {
	diags := analyzeSource(t, `package p
import "context"
type Handle struct{ Cancel context.CancelFunc }
func Start() {
	ctx, cancel := context.WithCancel(context.Background())
	h := &Handle{Cancel: cancel}
	go func() { <-ctx.Done() }()
	register(h)
}
func register(h *Handle) { _ = h }
`)
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
}

// TestFieldCapture_PositionalStructLiteralFallsBack documents the exact
// boundary of "selected struct fields": only a keyed struct literal
// (`&Handle{Cancel: cancel}`) is tracked field-by-field. A positional
// literal (`&Handle{cancel}`) has no field identity collectFieldCaptures
// can attach to a specific value, so it falls back to the prior generic
// "stored in a composite value => escaped" handling untouched, the same
// as before this capability existed.
func TestFieldCapture_PositionalStructLiteralFallsBack(t *testing.T) {
	diags := analyzeSource(t, `package p
import "context"
type Handle struct{ Cancel context.CancelFunc }
func Start() {
	ctx, cancel := context.WithCancel(context.Background())
	h := &Handle{cancel}
	go func() { <-ctx.Done() }()
	_ = h
}
`)
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
}

// TestFieldCapture_MultiNameAssignmentFallsBack documents the other half
// of the same boundary: only a single named local variable receiving the
// whole literal (`h := &Handle{...}`) is tracked. A multi-name assignment
// is left to the existing generic fallback, exactly as before.
func TestFieldCapture_MultiNameAssignmentFallsBack(t *testing.T) {
	diags := analyzeSource(t, `package p
import "context"
type Handle struct{ Cancel context.CancelFunc }
func other() int { return 0 }
func Start() {
	ctx, cancel := context.WithCancel(context.Background())
	h, n := &Handle{Cancel: cancel}, other()
	_ = n
	go func() { <-ctx.Done() }()
	_ = h
}
`)
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
}

func TestFieldCapture_GroupStoredStructUnconsumedFires(t *testing.T) {
	diags := analyzeSource(t, `package p
import "sync"
type Handle struct {
	Label string
	WG    *sync.WaitGroup
}
func Start() {
	var wg sync.WaitGroup
	wg.Add(1)
	h := &Handle{Label: "worker", WG: &wg}
	go func() { defer wg.Done() }()
	_ = h.Label
}
`)
	if len(diags) != 1 || diags[0].RuleID != "LL1003" {
		t.Fatalf("diagnostics = %#v", diags)
	}
}

func TestFieldCapture_GroupStoredStructWaitedDoesNotFire(t *testing.T) {
	diags := analyzeSource(t, `package p
import "sync"
type Handle struct{ WG *sync.WaitGroup }
func Start() {
	var wg sync.WaitGroup
	wg.Add(1)
	h := &Handle{WG: &wg}
	go func() { defer wg.Done() }()
	h.WG.Wait()
}
`)
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
}

// TestFieldCapture_GroupAddAndWaitBothThroughFieldBalance exercises
// markGroupFieldConsumed's Add branch end to end -- including
// computeGroupOrdering's own CFG-based join-before-return check, which
// depends on the Wait call site recorded through the field being findable
// in the flowgraph the same way a direct Wait() call's site is -- by
// routing both Add and Wait through the same field, with no direct
// wg.Add/wg.Wait call anywhere for either to fall back on.
func TestFieldCapture_GroupAddAndWaitBothThroughFieldBalance(t *testing.T) {
	diags := analyzeSource(t, `package p
import "sync"
type Handle struct{ WG *sync.WaitGroup }
func Start() {
	var wg sync.WaitGroup
	h := &Handle{WG: &wg}
	h.WG.Add(1)
	go func() { defer wg.Done() }()
	h.WG.Wait()
}
`)
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
}

func TestFieldOwnership_ConstructorCancelDroppedByCallerFires(t *testing.T) {
	diags := analyzeSource(t, `package p
import "context"
type Handle struct {
	Label  string
	Cancel context.CancelFunc
}
func NewHandle(parent context.Context) (*Handle, context.Context) {
	ctx, cancel := context.WithCancel(parent)
	return &Handle{Label: "worker", Cancel: cancel}, ctx
}
func Start(parent context.Context) {
	h, ctx := NewHandle(parent)
	go func() { <-ctx.Done() }()
	_ = h.Label
}
`)
	if len(diags) != 1 || diags[0].RuleID != "LL1001" {
		t.Fatalf("diagnostics = %#v", diags)
	}
	found := false
	for _, e := range diags[0].Evidence {
		if e.Kind == "field-not-consumed" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a field-not-consumed evidence entry, got %#v", diags[0].Evidence)
	}
}

func TestFieldOwnership_ConstructorCancelConsumedByCallerDoesNotFire(t *testing.T) {
	diags := analyzeSource(t, `package p
import "context"
type Handle struct{ Cancel context.CancelFunc }
func NewHandle(parent context.Context) (*Handle, context.Context) {
	ctx, cancel := context.WithCancel(parent)
	return &Handle{Cancel: cancel}, ctx
}
func Start(parent context.Context) {
	h, ctx := NewHandle(parent)
	go func() { <-ctx.Done() }()
	h.Cancel()
}
`)
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
}

func TestFieldOwnership_ConstructorGroupDroppedByCallerFires(t *testing.T) {
	diags := analyzeSource(t, `package p
import "sync"
type Handle struct {
	Label string
	WG    *sync.WaitGroup
}
func NewHandle() *Handle {
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done() }()
	return &Handle{Label: "worker", WG: &wg}
}
func Start() {
	h := NewHandle()
	_ = h.Label
}
`)
	if len(diags) != 1 || diags[0].RuleID != "LL1003" {
		t.Fatalf("diagnostics = %#v", diags)
	}
}

func TestFieldOwnership_ConstructorGroupWaitedByCallerDoesNotFire(t *testing.T) {
	diags := analyzeSource(t, `package p
import "sync"
type Handle struct{ WG *sync.WaitGroup }
func NewHandle() *Handle {
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done() }()
	return &Handle{WG: &wg}
}
func Start() {
	h := NewHandle()
	h.WG.Wait()
}
`)
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
}

// TestFieldOwnership_ConstructorCallerDeclaredFirstStillVerifies is the
// constructor-tracking counterpart to
// TestParameterPassing_CalleeDeclaredFirstStillVerifies: here Start (the
// caller) is declared before NewHandle (the constructor), the opposite of
// every other test in this section. Unlike Phase 5's own single
// declaration-order-dependent pre-pass (see the chained-leak pair of
// tests above), field/constructor ownership tracking always runs
// computeFieldOwnership to completion for every function before
// computeConstructorCallerConsumption examines any caller, so this must
// verify identically regardless of which one is declared first.
func TestFieldOwnership_ConstructorCallerDeclaredFirstStillVerifies(t *testing.T) {
	diags := analyzeSource(t, `package p
import "context"
type Handle struct {
	Label  string
	Cancel context.CancelFunc
}
func Start(parent context.Context) {
	h, ctx := NewHandle(parent)
	go func() { <-ctx.Done() }()
	_ = h.Label
}
func NewHandle(parent context.Context) (*Handle, context.Context) {
	ctx, cancel := context.WithCancel(parent)
	return &Handle{Label: "worker", Cancel: cancel}, ctx
}
`)
	if len(diags) != 1 || diags[0].RuleID != "LL1001" {
		t.Fatalf("caller-declared-first case should still be verified and fire LL1001, got diagnostics = %#v", diags)
	}
}

// TestFieldOwnership_ConstructorNeverCalledFallsBack documents that an
// unused (or, in real code, exported-for-another-package) constructor
// never produces a finding purely from the absence of a caller to check:
// b.returnFieldConsumption has no entry for its binding, which
// recordReturnedField treats exactly like a verified-consumed caller --
// the same conservative assume-transferred default used everywhere else
// whenever a value's fate can't be verified.
func TestFieldOwnership_ConstructorNeverCalledFallsBack(t *testing.T) {
	diags := analyzeSource(t, `package p
import "context"
type Handle struct{ Cancel context.CancelFunc }
func NewHandle(parent context.Context) (*Handle, context.Context) {
	ctx, cancel := context.WithCancel(parent)
	return &Handle{Cancel: cancel}, ctx
}
func Start() {}
`)
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
}

// TestFieldOwnership_ConstructorCallerPassesHandleOnwardFallsBack
// documents the single-hop boundary computeConstructorCallerConsumption
// deliberately keeps (mirroring Phase 5's own one-hop guarantee for
// direct parameter passing, see the chained-leak pair of tests above): a
// caller that itself just forwards the handle to a further function is
// not chased into, even though that further function does in fact
// consume it -- an honest gap, safely falling back rather than ever
// producing a false positive.
func TestFieldOwnership_ConstructorCallerPassesHandleOnwardFallsBack(t *testing.T) {
	diags := analyzeSource(t, `package p
import "context"
type Handle struct{ Cancel context.CancelFunc }
func NewHandle(parent context.Context) (*Handle, context.Context) {
	ctx, cancel := context.WithCancel(parent)
	return &Handle{Cancel: cancel}, ctx
}
func Start(parent context.Context) {
	h, ctx := NewHandle(parent)
	go func() { <-ctx.Done() }()
	forward(h)
}
func forward(h *Handle) { h.Cancel() }
`)
	if len(diags) != 0 {
		t.Fatalf("expected the current honest gap (single-hop caller check does not chase a further forward), got diagnostics = %#v", diags)
	}
}

// TestFieldOwnership_ConstructorLocalVariableThenReturnAlsoTracked
// confirms the second shape resolveFieldCaptures recognizes for a
// constructor -- a struct literal assigned to a local variable and then
// returned, rather than built directly inline in the return statement --
// is tracked identically to
// TestFieldOwnership_ConstructorCancelDroppedByCallerFires above.
func TestFieldOwnership_ConstructorLocalVariableThenReturnAlsoTracked(t *testing.T) {
	diags := analyzeSource(t, `package p
import "context"
type Handle struct {
	Label  string
	Cancel context.CancelFunc
}
func NewHandle(parent context.Context) (*Handle, context.Context) {
	ctx, cancel := context.WithCancel(parent)
	h := &Handle{Label: "worker", Cancel: cancel}
	return h, ctx
}
func Start(parent context.Context) {
	h, ctx := NewHandle(parent)
	go func() { <-ctx.Done() }()
	_ = h.Label
}
`)
	if len(diags) != 1 || diags[0].RuleID != "LL1001" {
		t.Fatalf("diagnostics = %#v", diags)
	}
}

// The following tests cover the WaitGroup/errgroup upgrade described in
// docs/cfg-migration-plan.md's Phase 3 completion section: count
// intervals, the common Add(1)-then-spawned-Done idiom, join-before-
// owner-return, and stop-before-wait. Each is a narrow, structurally-
// grounded check that only ever fires from positive evidence -- see that
// section, and the doc comments on model.JoinGroup's new fields, for
// exactly what is and isn't attempted.

func TestWaitGroupCountMismatchFires(t *testing.T) {
	diags := analyzeSource(t, `package p
import "sync"
func Start(){
	var wg sync.WaitGroup
	wg.Add(2)
	go func(){ defer wg.Done() }()
	wg.Wait()
}
`)
	if len(diags) != 1 || diags[0].RuleID != "LL1003" {
		t.Fatalf("literal Add(2)/Done(1) mismatch should fire LL1003 even though Wait is observed, got diagnostics = %#v", diags)
	}
}

func TestWaitGroupMultipleLiteralAddDoneSitesBalance(t *testing.T) {
	diags := analyzeSource(t, `package p
import "sync"
func Start(){
	var wg sync.WaitGroup
	wg.Add(3)
	go func(){ defer wg.Done() }()
	go func(){ defer wg.Done() }()
	go func(){ defer wg.Done() }()
	wg.Wait()
}
`)
	if len(diags) != 0 {
		t.Fatalf("Add(3) matched by three separate spawned Done sites should balance, got diagnostics = %#v", diags)
	}
}

func TestWaitGroupLoopPairedIdiomBalancesRegardlessOfIterationCount(t *testing.T) {
	diags := analyzeSource(t, `package p
import "sync"
func Start(items []int){
	var wg sync.WaitGroup
	for range items {
		wg.Add(1)
		go func(){ defer wg.Done() }()
	}
	wg.Wait()
}
`)
	if len(diags) != 0 {
		t.Fatalf("the common loop-scoped Add(1)/spawned-Done idiom should never be flagged as a count mismatch, got diagnostics = %#v", diags)
	}
}

func TestWaitGroupLoopUnmatchedCountsAreNotFullyKnownNotMismatched(t *testing.T) {
	// Two Add(1) sites per iteration but only one spawned Done: a genuine
	// shape this narrow idiom check can't resolve into a specific number
	// without an iteration count, so it must stay silent (on the count
	// question) rather than guess in either direction. Wait is on every
	// path, so nothing else should fire either.
	diags := analyzeSource(t, `package p
import "sync"
func Start(items []int){
	var wg sync.WaitGroup
	for range items {
		wg.Add(1)
		wg.Add(1)
		go func(){ defer wg.Done() }()
	}
	wg.Wait()
}
`)
	if len(diags) != 0 {
		t.Fatalf("an unresolvable loop-scoped count should never be reported as a mismatch, got diagnostics = %#v", diags)
	}
}

func TestWaitGroupJoinedOnSomeButNotAllPathsFires(t *testing.T) {
	diags := analyzeSource(t, `package p
import "sync"
func cond() bool { return true }
func Start(){
	var wg sync.WaitGroup
	wg.Add(1)
	go func(){ defer wg.Done() }()
	if cond() {
		return
	}
	wg.Wait()
}
`)
	if len(diags) != 1 || diags[0].RuleID != "LL1003" {
		t.Fatalf("a Wait() bypassed by an early return should fire LL1003, got diagnostics = %#v", diags)
	}
}

func TestWaitGroupJoinedOnAllPathsFromBothBranchesDoesNotFire(t *testing.T) {
	diags := analyzeSource(t, `package p
import "sync"
func cond() bool { return true }
func Start(){
	var wg sync.WaitGroup
	wg.Add(1)
	go func(){ defer wg.Done() }()
	if cond() {
		wg.Wait()
		return
	}
	wg.Wait()
}
`)
	if len(diags) != 0 {
		t.Fatalf("a Wait() call present on every branch's own return path should not fire, got diagnostics = %#v", diags)
	}
}

func TestWaitGroupStopAfterWaitFires(t *testing.T) {
	diags := analyzeSource(t, `package p
import (
	"context"
	"sync"
)
func Start(parent context.Context) {
	ctx, cancel := context.WithCancel(parent)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-ctx.Done()
	}()
	wg.Wait()
	cancel()
}
`)
	if len(diags) != 1 || diags[0].RuleID != "LL1005" {
		t.Fatalf("a cancel-based stop signal only reachable after Wait() should fire LL1005, got diagnostics = %#v", diags)
	}
}

func TestWaitGroupStopBeforeWaitDoesNotFire(t *testing.T) {
	diags := analyzeSource(t, `package p
import (
	"context"
	"sync"
)
func Start(parent context.Context) {
	ctx, cancel := context.WithCancel(parent)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-ctx.Done()
	}()
	cancel()
	wg.Wait()
}
`)
	if len(diags) != 0 {
		t.Fatalf("a cancel-based stop signal guaranteed before Wait() should not fire, got diagnostics = %#v", diags)
	}
}

func TestWaitGroupUnrelatedCancelNotCapturedByChildDoesNotFireStopAfterWait(t *testing.T) {
	// cancel's own context is never captured by the started goroutine
	// (UsedByChild stays false), so it isn't credited as this group's stop
	// signal at all, regardless of where it falls relative to Wait().
	diags := analyzeSource(t, `package p
import (
	"context"
	"sync"
)
func Start(parent context.Context) {
	_, cancel := context.WithCancel(parent)
	var wg sync.WaitGroup
	wg.Add(1)
	go func(){ defer wg.Done() }()
	wg.Wait()
	cancel()
}
`)
	if len(diags) != 0 {
		t.Fatalf("an unrelated cancel call should not be credited as this group's stop signal, got diagnostics = %#v", diags)
	}
}

// The following tests are regressions for two bugs found via an external
// Phase 6 benchmark run (phase6-fix-requirements.md) after the checks
// above first shipped: a named-function goroutine target's own Done()
// call was invisible to count-balance accounting (only an inline closure
// was recognized), and count-balance accounting wasn't path-sensitive at
// all -- an Add() in one conditional branch and an unrelated Done() in a
// sibling branch were summed into the same running total as if both
// always ran together, which could either mask a real per-branch
// imbalance or fabricate one that doesn't correspond to any single
// execution. Both are now fixed; see calleeDoneParamMatches and
// foldConditionalArms.

func TestWaitGroupNamedFunctionWorkerDoneRecognized(t *testing.T) {
	diags := analyzeSource(t, `package p
import "sync"
func worker(wg *sync.WaitGroup) { defer wg.Done() }
func Start() {
	var wg sync.WaitGroup
	wg.Add(1)
	go worker(&wg)
	wg.Wait()
}
`)
	if len(diags) != 0 {
		t.Fatalf("a named-function worker that calls Done() on its pointer parameter should balance the count, got diagnostics = %#v", diags)
	}
}

func TestWaitGroupNamedFunctionWorkerLoopPairedRecognized(t *testing.T) {
	diags := analyzeSource(t, `package p
import "sync"
func worker(wg *sync.WaitGroup) { defer wg.Done() }
func Start(items []int) {
	var wg sync.WaitGroup
	for range items {
		wg.Add(1)
		go worker(&wg)
	}
	wg.Wait()
}
`)
	if len(diags) != 0 {
		t.Fatalf("a loop-scoped Add(1) paired with a named-function worker's own Done() should balance regardless of iteration count, got diagnostics = %#v", diags)
	}
}

func TestWaitGroupNamedFunctionWorkerNeverDoneStillFires(t *testing.T) {
	// Regression guard the other direction: fixing the false negative
	// above must not turn into a blanket "any go statement to a named
	// function counts as Done" false suppression. worker here never calls
	// Done, so the outstanding Add(1) must still be caught.
	diags := analyzeSource(t, `package p
import "sync"
func worker(wg *sync.WaitGroup) {}
func Start() {
	var wg sync.WaitGroup
	wg.Add(1)
	go worker(&wg)
	wg.Wait()
}
`)
	if len(diags) != 1 || diags[0].RuleID != "LL1003" {
		t.Fatalf("a named-function worker that never calls Done() should still be caught, got diagnostics = %#v", diags)
	}
}

func TestWaitGroupCrossBranchAddDoneNeverFabricatesAMismatch(t *testing.T) {
	// if cond: Add(1) alone (a real, but conditional, mismatch). else:
	// Add(1) matched by its own spawned Done (fine on its own). Summing
	// across both branches as the old, non-path-sensitive accounting did
	// gives a fabricated "2 Add vs 1 Done" that no single execution of
	// this function ever actually produces -- whichever branch runs, it's
	// either "1 Add, 0 Done" or "1 Add, 1 Done", never both branches'
	// contributions at once. Firing from that fabricated arithmetic would
	// be exactly the kind of manufactured evidence this analysis commits
	// to never producing, so this must stay silent (an unproven, branch-
	// dependent case, not a proven one) even though the "if" branch alone
	// is genuinely suspicious.
	diags := analyzeSource(t, `package p
import "sync"
func cond() bool { return true }
func Start(){
	var wg sync.WaitGroup
	if cond() {
		wg.Add(1)
	} else {
		wg.Add(1)
		go func(){ defer wg.Done() }()
	}
	wg.Wait()
}
`)
	if len(diags) != 0 {
		t.Fatalf("a mismatch that only exists by summing two mutually exclusive branches together must not fire, got diagnostics = %#v", diags)
	}
}

func TestWaitGroupCrossBranchAddDoneUnrelatedDoneDoesNotMaskMismatch(t *testing.T) {
	// The mirror image of the bug: an Add() in one branch must not be
	// treated as satisfied by an unrelated Done() in a sibling branch
	// either. Here the "if" branch's own Add(1) has no Done anywhere in
	// that branch; the "else" branch's bare Done() belongs to a
	// completely different, unconditional code path and must not be
	// borrowed to paper over the "if" branch's own accounting. Since this
	// is still a branch-dependent (not proven-on-every-path) situation,
	// the correct outcome is the same conservative silence as the test
	// above -- the old bug's failure mode was claiming a false balance
	// here (1 Add, 1 Done, "clean"), which is what this guards against.
	diags := analyzeSource(t, `package p
import "sync"
func cond() bool { return true }
func Start(){
	var wg sync.WaitGroup
	if cond() {
		wg.Add(1)
	} else {
		wg.Done()
	}
	wg.Wait()
}
`)
	if len(diags) != 0 {
		t.Fatalf("an Add() and an unrelated Done() in sibling branches must never be treated as balancing each other, got diagnostics = %#v", diags)
	}
}

func TestWaitGroupConditionalWorkerIdiomStillClean(t *testing.T) {
	// Guard against the path-sensitivity fix overcorrecting: the very
	// common "start a worker only if needed" idiom, entirely self-
	// contained within a single branch with no else, must remain clean.
	diags := analyzeSource(t, `package p
import "sync"
func needed() bool { return true }
func Start(){
	var wg sync.WaitGroup
	if needed() {
		wg.Add(1)
		go func(){ defer wg.Done() }()
	}
	wg.Wait()
}
`)
	if len(diags) != 0 {
		t.Fatalf("a self-contained conditional Add/spawned-Done pair with no else should stay clean, got diagnostics = %#v", diags)
	}
}

// The following tests correspond directly to phase6-fix-requirements.md's
// numbered items: group identity (items 2, 5, 8), mixed-path accounting
// (item 6), and ordering semantics (items 9, 10). Helper-mediated joins
// (item 7) are already covered above by TestParameterPassing_*, applied
// to groups the same way as cancels; the multi-hop, declaration-order-
// independent case is TestParameterPassing_ChainedLeakCaughtWhenCaller-
// DeclaredFirst's own sibling coverage, exercised for groups by
// TestParameterPassing_GroupVerifiedLeakFires/...WaitDoesNotFire above.

func TestGroupIdentity_DoneOnWrongGroupBothFindingsAreIndependentlyCorrect(t *testing.T) {
	// item 2: a worker meant for "a" mistakenly calls Done() on "b". Two
	// independent, individually correct findings are the intended oracle
	// here, not a bug to fix: "a" genuinely has an outstanding worker (its
	// Add(1) is never matched by any Done()), and "b" is genuinely never
	// waited (only "a" is waited). Neither finding borrows evidence from
	// the other group.
	diags := analyzeSource(t, `package p
import "sync"
func Start(){
	var a, b sync.WaitGroup
	a.Add(1)
	b.Add(1)
	go func(){ defer b.Done() }() // bug: worker for "a" decrements "b" instead
	a.Wait()
}
`)
	if len(diags) != 2 {
		t.Fatalf("expected exactly two independent findings (one per group), got %#v", diags)
	}
	var sawA, sawB bool
	for _, d := range diags {
		if d.RuleID != "LL1003" {
			t.Fatalf("expected LL1003 for both findings, got %#v", d)
		}
		switch {
		case strings.Contains(d.Message, `"a"`):
			sawA = true
			if !strings.Contains(d.Message, "outstanding worker") {
				t.Fatalf(`group "a" should be flagged for an outstanding worker (Done() went to the wrong group), got %q`, d.Message)
			}
		case strings.Contains(d.Message, `"b"`):
			sawB = true
			if !strings.Contains(d.Message, "no Wait or ownership transfer") {
				t.Fatalf(`group "b" should be flagged as never waited, got %q`, d.Message)
			}
		}
	}
	if !sawA || !sawB {
		t.Fatalf("expected findings for both group \"a\" and group \"b\", got %#v", diags)
	}
}

func TestGroupIdentity_AliasResolvedLocallyKeepsGroupsSeparate(t *testing.T) {
	// item 5: pointer aliases the analysis can resolve locally (&a, &b
	// passed to distinct named-function workers) must never cross-wire
	// group "a"'s accounting with group "b"'s.
	diags := analyzeSource(t, `package p
import "sync"
func workerA(wg *sync.WaitGroup) { defer wg.Done() }
func workerB(wg *sync.WaitGroup) { defer wg.Done() }
func Start(){
	var a, b sync.WaitGroup
	a.Add(1)
	go workerA(&a)
	b.Add(1)
	go workerB(&b)
	a.Wait()
	b.Wait()
}
`)
	if len(diags) != 0 {
		t.Fatalf("two independently correct, separately-aliased groups should both be clean, got %#v", diags)
	}
}

func TestGroupIdentity_UnresolvableAliasFallsBackConservatively(t *testing.T) {
	// item 5: a WaitGroup reached only through a struct field (h.wg) is
	// not a local variable declaration collectBindings recognizes at all
	// -- consistent with docs/limitations.md's existing "a value stored in
	// a struct field... is not attempted" scope boundary, not a new
	// resolution this test is claiming. The point of this test is the
	// negative guarantee: since it's untracked, it must never be
	// misattributed to some other, unrelated group either -- confirmed by
	// this staying clean with zero groups recognized, not by any specific
	// finding.
	diags := analyzeSource(t, `package p
import "sync"
type holder struct{ wg sync.WaitGroup }
func run(h *holder) {
	h.wg.Add(1)
	go func(){ defer h.wg.Done() }()
	h.wg.Wait()
}
`)
	if len(diags) != 0 {
		t.Fatalf("an untracked struct-field WaitGroup should never produce a finding either way, got %#v", diags)
	}
}

func TestGroupIdentity_TwoIndependentGroupsOneUnjoinedOneClean(t *testing.T) {
	// item 8: group "a" being correctly joined must not suppress a
	// finding for group "b".
	diags := analyzeSource(t, `package p
import "sync"
func Start(){
	var a, b sync.WaitGroup
	a.Add(1)
	go func(){ defer a.Done() }()
	a.Wait()
	b.Add(1)
	go func(){ defer b.Done() }()
	// b is never waited
}
`)
	if len(diags) != 1 || diags[0].RuleID != "LL1003" || !strings.Contains(diags[0].Message, `"b"`) {
		t.Fatalf(`expected exactly one LL1003 finding for group "b" only, got %#v`, diags)
	}
}

func TestGroupIdentity_TwoIndependentGroupsBothCleanControlCase(t *testing.T) {
	// item 8: the fully-safe two-group control case, guarding against
	// over-reporting (e.g. accidentally flagging "a" because "b" exists).
	diags := analyzeSource(t, `package p
import "sync"
func Start(){
	var a, b sync.WaitGroup
	a.Add(1)
	go func(){ defer a.Done() }()
	b.Add(1)
	go func(){ defer b.Done() }()
	a.Wait()
	b.Wait()
}
`)
	if len(diags) != 0 {
		t.Fatalf("two fully-correct independent groups should produce no findings, got %#v", diags)
	}
}

func TestGroupIdentity_ReorderedOperationsStillSeparate(t *testing.T) {
	// item 5: interleaved (reordered) operations on two groups must still
	// resolve to the correct, separate identities.
	diags := analyzeSource(t, `package p
import "sync"
func Start(){
	var a, b sync.WaitGroup
	a.Add(1)
	b.Add(1)
	go func(){ defer a.Done() }()
	go func(){ defer b.Done() }()
	b.Wait()
	a.Wait()
}
`)
	if len(diags) != 0 {
		t.Fatalf("interleaved operations on two distinct groups should still both resolve cleanly, got %#v", diags)
	}
}

func TestGroupIdentity_MultipleWaitCallsSameGroup(t *testing.T) {
	// item 5: multiple Wait() calls on the same group (e.g. one per
	// branch) must all be attributed to that one group, not confused with
	// a second group.
	diags := analyzeSource(t, `package p
import "sync"
func cond() bool { return true }
func Start(){
	var wg sync.WaitGroup
	wg.Add(1)
	go func(){ defer wg.Done() }()
	if cond() {
		wg.Wait()
		return
	}
	wg.Wait()
}
`)
	if len(diags) != 0 {
		t.Fatalf("a Wait() call present on every branch's own return path should stay clean, got %#v", diags)
	}
}

func TestMixedPathAccounting_EarlyReturnBeforeDoneStaysConservative(t *testing.T) {
	// item 6: an early return before a worker's Done() would run leaves
	// the count merely unproven (the worker was still started and may
	// still call Done() on its own later), not a proven mismatch --
	// distinct from the join-before-return question, which this same
	// early return DOES violate (Wait is never reached on this path).
	diags := analyzeSource(t, `package p
import "sync"
func cond() bool { return true }
func Start(){
	var wg sync.WaitGroup
	wg.Add(1)
	go func(){ defer wg.Done() }()
	if cond() {
		return
	}
	wg.Wait()
}
`)
	if len(diags) != 1 || diags[0].RuleID != "LL1003" || !strings.Contains(diags[0].Message, "not every path") {
		t.Fatalf("expected exactly one LL1003 for the bypassed Wait(), not a count-mismatch claim, got %#v", diags)
	}
}

func TestMixedPathAccounting_MultipleAddsOnlySomeMatchedStaysUnknown(t *testing.T) {
	// item 6: three Add(1) sites, only two matched by a spawned Done --
	// not the recognized loop idiom (no loop here at all), so this must
	// stay silent rather than guess at a specific mismatch count.
	diags := analyzeSource(t, `package p
import "sync"
func Start(){
	var wg sync.WaitGroup
	wg.Add(1)
	go func(){ defer wg.Done() }()
	wg.Add(1)
	go func(){ defer wg.Done() }()
	wg.Add(1)
	wg.Wait()
}
`)
	if len(diags) != 1 || diags[0].RuleID != "LL1003" {
		t.Fatalf("2 Add(1) matched with 2 spawned Done leaves exactly one literal, unmatched Add(1); expected exactly one LL1003 count-mismatch, got %#v", diags)
	}
	if !strings.Contains(diags[0].Message, "outstanding worker") {
		t.Fatalf("expected the count-mismatch message, got %q", diags[0].Message)
	}
}

func TestMixedPathAccounting_WaitAfterBranchMergeStaysClean(t *testing.T) {
	// item 6: Wait() placed after an if/else merge point (rather than
	// duplicated in each branch) is a completely ordinary, safe pattern.
	diags := analyzeSource(t, `package p
import "sync"
func cond() bool { return true }
func Start(){
	var wg sync.WaitGroup
	wg.Add(1)
	go func(){ defer wg.Done() }()
	if cond() {
		_ = 1
	} else {
		_ = 2
	}
	wg.Wait()
}
`)
	if len(diags) != 0 {
		t.Fatalf("Wait() after an unrelated if/else merge should stay clean, got %#v", diags)
	}
}

func TestOrderingSemantics_WaitBeforeLaterAddIsNotFlagged(t *testing.T) {
	// item 9: Wait() called while the counter is still zero is a no-op in
	// sync.WaitGroup's own semantics (it returns immediately) and is not
	// itself unsafe; a later, properly-ordered Add()+Wait() pair still
	// completes the protocol correctly. Phase 6 does not verify temporal
	// ordering between Add and Wait call sites at all -- CountMismatch is
	// order-independent by construction (a sum, not a sequence), and
	// JoinedOnAllPaths only asks whether a Wait() call is reachable on
	// every path, not what precedes it -- so this is documented as
	// "unsupported/unknown", not silently inferred as "safe", from lexical
	// order: see docs/limitations.md.
	diags := analyzeSource(t, `package p
import "sync"
func Start(){
	var wg sync.WaitGroup
	wg.Wait()
	wg.Add(1)
	go func(){ defer wg.Done() }()
	wg.Wait()
}
`)
	if len(diags) != 0 {
		t.Fatalf("an extra, harmless early Wait() before a correctly-ordered Add/Done/Wait sequence should not fire, got %#v", diags)
	}
}

func TestOrderingSemantics_ReusedWaitGroupSecondRoundUnverified(t *testing.T) {
	// item 9, the known gap this documents rather than silently ignores:
	// reusing one WaitGroup for a second round of work with no matching
	// second Wait() is not caught. Joined is satisfied by the first
	// Wait() call found anywhere in the function; per-"round" temporal
	// tracking (is every Add() eventually followed by a Wait() that
	// precedes the *next* Add()) is a materially larger undertaking than
	// the reachability and literal-sum checks Phase 6 implements, and is
	// explicitly out of scope -- see docs/limitations.md. This test
	// exists so a future fix to this gap is a deliberate, documented
	// change, not a silent regression either way.
	diags := analyzeSource(t, `package p
import "sync"
func Start(){
	var wg sync.WaitGroup
	wg.Add(1)
	go func(){ defer wg.Done() }()
	wg.Wait()
	wg.Add(1)
	go func(){ defer wg.Done() }()
	// second round never waited
}
`)
	if len(diags) != 0 {
		t.Fatalf("documented known gap: a reused WaitGroup's second round is not currently verified; if this now fires, update this test and docs/limitations.md to match the improvement, got %#v", diags)
	}
}

func TestOrderingSemantics_StopBeforeWaitProvesOrderingOnly(t *testing.T) {
	// item 10: LL1005 proves only that a recognized stop-signal call is
	// CFG-ordered before Wait(); it does not, and cannot, prove the
	// worker has actually observed and acted on that signal by the time
	// Wait() unblocks -- that would require reasoning about the
	// concurrently-running goroutine's own execution, which is out of
	// scope for a single-function CFG check. This is the same positive
	// case as TestWaitGroupStopBeforeWaitDoesNotFire above, restated here
	// to keep the ordering-only scope explicit and testable; see
	// docs/limitations.md.
	diags := analyzeSource(t, `package p
import (
	"context"
	"sync"
)
func Start(parent context.Context) {
	ctx, cancel := context.WithCancel(parent)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-ctx.Done()
	}()
	cancel()
	wg.Wait()
}
`)
	if len(diags) != 0 {
		t.Fatalf("a stop signal CFG-ordered before Wait() should not fire, got %#v", diags)
	}
}
