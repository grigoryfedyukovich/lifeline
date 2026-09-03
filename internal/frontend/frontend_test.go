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

func TestErrgroupDiagnosticNeverUsesWaitGroupAddDoneWording(t *testing.T) {
	// engine.go rewrites LL1003/LL1004's message/suggestion text for
	// errgroup only in the !group.Joined case; the !joinedOnAllPaths
	// case is already kind-agnostic (no Add/Done wording to begin with),
	// and the CountMismatch case's wording ("literal Add/Done
	// accounting...") is WaitGroup-specific and was never rewritten for
	// errgroup. That default branch is unreachable for an errgroup as
	// things stand today: computeGroupBalances explicitly skips any
	// group whose Kind isn't "waitgroup" (g.group.Kind != "waitgroup" ||
	// g.obj == nil { continue }), with its own comment explaining why
	// ("errgroup.Group's Add/Done-equivalent accounting is internal to
	// the library"), so CountMismatch can never be true for one -- this
	// test pins that invariant from the diagnostic-message side, so
	// that if computeGroupBalances' errgroup skip is ever relaxed in the
	// future (teaching it to recognize Go() the way it recognizes Add()),
	// a contributor is forced to notice and update this wording at the
	// same time rather than silently shipping a WaitGroup-flavored
	// message on an errgroup finding.
	diags := analyzeErrgroupSource(t, `package p
import "golang.org/x/sync/errgroup"
func Start(){ var g errgroup.Group; g.Go(func() error { return nil }) }
`)
	if len(diags) != 1 || diags[0].RuleID != "LL1004" {
		t.Fatalf("diagnostics = %#v", diags)
	}
	if strings.Contains(diags[0].Message, "Add") || strings.Contains(diags[0].Message, "Done") {
		t.Fatalf("errgroup diagnostic message must never reference WaitGroup's Add/Done, got %q", diags[0].Message)
	}
	if strings.Contains(diags[0].Suggestion, "Add") || strings.Contains(diags[0].Suggestion, "Done") {
		t.Fatalf("errgroup diagnostic suggestion must never reference WaitGroup's Add/Done, got %q", diags[0].Suggestion)
	}
}

func analyzeErrgroupSource(t *testing.T, source string) []engine.Diagnostic {
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
	return engine.Analyze(program, cfg)
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

func TestParameterPassing_GroupVerifiedWaitStillCatchesCountMismatch(t *testing.T) {
	// A verified helper-mediated join (run's own body genuinely calls
	// Wait() on the parameter) must record Joined, not Escapes: Escapes
	// makes engine.go skip the group's diagnostics entirely (Starts == 0
	// || Escapes), which would silently discard a real, independently-
	// computed CountMismatch finding along with it. Add(2) matched by
	// only one spawned Done() is a genuine bug -- Wait() may never
	// return -- regardless of whether Wait() is called directly or
	// through a resolvable one-hop helper; this must fire exactly the
	// same as it would if Start called wg.Wait() itself. See
	// docs/limitations.md's Phase 5 paragraph.
	diags := analyzeSource(t, `package p
import "sync"
func Start() {
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
	}()
	run(&wg)
}
func run(g *sync.WaitGroup) {
	g.Wait()
}
`)
	if len(diags) != 1 || diags[0].RuleID != "LL1003" {
		t.Fatalf("a count mismatch behind a verified helper-mediated Wait() should still fire LL1003, got diagnostics = %#v", diags)
	}
}

func TestParameterPassing_GroupHelperConditionalWaitUnverifiedGap(t *testing.T) {
	// Known, accepted limitation (docs/limitations.md): computeParameterConsumption's
	// scratch pass over the callee's body does not run computeGroupOrdering
	// or computeGroupBalances against it, so it has no way to tell a
	// Wait() call that is actually reached from one buried inside a
	// condition that happens to never be true for this particular call
	// site's own argument. run's own Wait() is gated behind `ok`, and
	// Start passes a literal false -- Wait() genuinely never executes,
	// so this group is never joined at all, but is currently reported
	// clean anyway. If this starts firing, that is a real improvement:
	// update this test (and docs/limitations.md's Phase 5 paragraph) to
	// match, do not treat a newly-passing test here as a regression.
	diags := analyzeSource(t, `package p
import "sync"
func Start() {
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
	}()
	run(&wg, false)
}
func run(g *sync.WaitGroup, ok bool) {
	if ok {
		g.Wait()
	}
}
`)
	if len(diags) != 0 {
		t.Fatalf("documented known gap: a helper's conditionally-unreached Wait() is not currently verified; if this now fires, update this test and docs/limitations.md to match the improvement, got %#v", diags)
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

func TestWaitGroupLoopConditionalArmSplitIsNotFullyKnownNotFalselyBalanced(t *testing.T) {
	// Add(1) in one arm of an if/else and the matching spawned Done() in
	// the *other*, mutually exclusive arm, both directly inside a loop
	// body: on any one iteration, exactly one of the two branches runs,
	// so this can never actually self-balance within a single iteration
	// the way the ordinary `wg.Add(1); go worker()` loop idiom does (see
	// TestWaitGroupLoopPairedIdiomBalancesRegardlessOfIterationCount) --
	// whether the running totals happen to even out across iterations
	// depends on how many times cond() returns true vs false, which this
	// analysis does not, and should not, try to track. The if/else is
	// walked through foldConditionalArms before either loopScope counter
	// is ever touched: since the "if" arm's own delta (Add, no Done) is
	// nonzero, foldConditionalArms marks the loop's own scope
	// otherActivity rather than folding anything into addOnes/
	// spawnedDones, which in turn makes the whole loop (and so the whole
	// function's WaitGroup accounting) not fully known -- silent for the
	// correct reason (genuine uncertainty), not because the mismatched
	// per-branch counts were mistaken for a balanced total.
	diags := analyzeSource(t, `package p
import "sync"
func cond() bool { return false }
func Start(){
	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		if cond() {
			wg.Add(1)
		} else {
			go func(){ defer wg.Done() }()
		}
	}
	wg.Wait()
}
`)
	if len(diags) != 0 {
		t.Fatalf("a loop-scoped conditional split between Add and its spawned Done must stay silent (not fully known), got %#v", diags)
	}
}

func TestWaitGroupSwitchFallthroughMismatchIsNotFullyKnownNotFalselyBalanced(t *testing.T) {
	// case 1 falls through into case 2, so an x == 1 run actually calls
	// Add twice (once from each case's own body) against a single
	// unconditional spawned Done after the switch -- but walkCaseClauses
	// computes each case's own groupBalance in isolation, with no
	// awareness that a preceding case's fallthrough would also run this
	// one's statements on that path. This does not, however, make the
	// switch's own per-case check unsound: foldConditionalArms requires
	// *every* one of a switch's arms (case 1's own body alone: Add(1),
	// no Done; case 2's own body alone: Add(1), no Done; the implicit
	// "no case matches" arm) to *each independently* net to zero before
	// treating the whole switch as balanced -- and here neither case 1
	// nor case 2 does on its own, so the switch correctly falls back to
	// not fully known, the same as it would for any other switch whose
	// cases don't individually balance, fallthrough or not. See
	// TestWaitGroupSwitchFallthroughBothArmsSelfBalancedIsRecognizedSafe
	// for why this same isolated-arm check is also sound in the other
	// direction: it never mistakes a genuinely mismatched fallthrough
	// chain for a balanced one, either.
	diags := analyzeSource(t, `package p
import "sync"
func Start(x int){
	var wg sync.WaitGroup
	switch x {
	case 1:
		wg.Add(1)
		fallthrough
	case 2:
		wg.Add(1)
	}
	go func(){ defer wg.Done() }()
	wg.Wait()
}
`)
	if len(diags) != 0 {
		t.Fatalf("a switch fallthrough with a per-case Add/Done mismatch must stay silent (not fully known), got %#v", diags)
	}
}

func TestWaitGroupSwitchFallthroughBothArmsSelfBalancedIsRecognizedSafe(t *testing.T) {
	// Each case here is independently self-balanced (its own Add(1)
	// matched by its own spawned Done()), so whichever one actually runs
	// -- and, via fallthrough, both together on the x == 1 path -- nets
	// to zero either way: two independently-balanced pairs summed is
	// still balanced. walkCaseClauses/foldConditionalArms's fallthrough-
	// blind, "every arm independently nets to zero" check recognizes
	// this correctly without needing to know fallthrough chains them:
	// mathematically, summing any number of already-balanced (Add ==
	// Done) arms can never produce an imbalance, so requiring every
	// individual arm to balance is already sufficient to make the whole
	// switch's contribution to the running total exactly zero. No
	// Wait() call exists in this test on purpose, to isolate the balance
	// question from the separate "was it joined" question: LL1003
	// should still fire for being entirely unjoined (2 worker starts,
	// nothing waits for them), but must not additionally claim a count
	// mismatch, since there provably isn't one.
	diags := analyzeSource(t, `package p
import "sync"
func Start(x int){
	var wg sync.WaitGroup
	switch x {
	case 1:
		wg.Add(1)
		go func(){ defer wg.Done() }()
		fallthrough
	case 2:
		wg.Add(1)
		go func(){ defer wg.Done() }()
	}
}
`)
	if len(diags) != 1 || diags[0].RuleID != "LL1003" || !strings.Contains(diags[0].Message, "no Wait or ownership transfer is observed") || strings.Contains(diags[0].Message, "outstanding worker") {
		t.Fatalf("a fallthrough chain of individually-self-balanced cases must not be reported as a count mismatch (only as unjoined), got %#v", diags)
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

func TestWaitGroupDeferOnlyCancelStillFiresStopAfterWait(t *testing.T) {
	// `defer cancel()` immediately after WithCancel, with no other call to
	// cancel anywhere, genuinely deadlocks: the deferred call does not run
	// until Start is already returning, which cannot happen until Wait()
	// has returned, which cannot happen until the goroutine sees
	// ctx.Done(), which cannot happen until cancel() runs. internal/cfg
	// records the defer at its own lexical position (right after
	// WithCancel, before Add/go/Wait), which is *before* Wait() in both
	// block and in-block-index terms -- so without special handling for
	// an all-deferred cancel binding, stopProvenAfterWait's ordinary
	// same-block index comparison would (wrongly) conclude the stop
	// signal is proven to arrive before Wait() and never fire LL1005 at
	// all, missing a deterministic deadlock. See allCallsDeferred and
	// computeGroupOrdering's own doc comment.
	diags := analyzeSource(t, `package p
import (
	"context"
	"sync"
)
func Start(parent context.Context) {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-ctx.Done()
	}()
	wg.Wait()
}
`)
	if len(diags) != 1 || diags[0].RuleID != "LL1005" {
		t.Fatalf("a stop signal only ever sent via a deferred call should fire LL1005, got diagnostics = %#v", diags)
	}
}

func TestWaitGroupDeferCancelAlongsideExplicitStopBeforeWaitDoesNotFire(t *testing.T) {
	// The idiomatic combination -- `defer cancel()` right after
	// WithCancel as a panic/early-return safety net, *plus* an explicit
	// cancel() at the point the caller actually wants to signal shutdown,
	// guaranteed to run before Wait() -- must not be flagged. This is
	// examples/stop_before_wait's own shape: the explicit call already
	// proves the signal is sent before Wait() regardless of the
	// deferred call's own (misleadingly early) recorded position, so the
	// all-deferred short-circuit in computeGroupOrdering must not apply
	// merely because *a* call site for this binding happens to be a
	// defer -- only when *every* call site is.
	diags := analyzeSource(t, `package p
import (
	"context"
	"sync"
)
func Start(parent context.Context) {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
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
		t.Fatalf("a defer-as-safety-net alongside an explicit stop-before-wait call should not fire, got diagnostics = %#v", diags)
	}
}

func analyzeSourceWithStopWrapper(t *testing.T, source string, stopWrapper string) []engine.Diagnostic {
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
	cfg.StopWrappers = []string{stopWrapper}
	program, err := Build(Input{Fset: fset, Files: []*ast.File{file}, Pkg: pkg, Info: info}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	return engine.Analyze(program, cfg)
}

func TestWaitGroupDeferOnlyStopWrapperStillFiresStopAfterWait(t *testing.T) {
	// The exact same defer-mispositioning bug as
	// TestWaitGroupDeferOnlyCancelStillFiresStopAfterWait, but for a
	// configured stop_wrapper instead of a cancel function:
	// internal/cfg's defer mispositioning is not specific to
	// context.CancelFunc values, and this case was originally missed
	// when the cancel-binding fix above was made. `defer shutdown()`
	// (shutdown configured as a stop_wrapper) at the top of the
	// function, with no other call to it, deadlocks exactly like the
	// equivalent defer-cancel case: the deferred call cannot run until
	// Start is already returning, which cannot happen until Wait() has
	// returned, which cannot happen until the worker sees the signal
	// shutdown() was meant to send.
	diags := analyzeSourceWithStopWrapper(t, `package p
import "sync"
func shutdown() {}
func waitForShutdown() {}
func Start() {
	defer shutdown()
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		waitForShutdown()
	}()
	wg.Wait()
}
`, "example.test/input.shutdown")
	if len(diags) != 1 || diags[0].RuleID != "LL1005" {
		t.Fatalf("a stop_wrapper only ever called via defer should fire LL1005, got diagnostics = %#v", diags)
	}
}

func TestWaitGroupDeferStopWrapperAlongsideExplicitStopBeforeWaitDoesNotFire(t *testing.T) {
	// The stop_wrapper analogue of
	// TestWaitGroupDeferCancelAlongsideExplicitStopBeforeWaitDoesNotFire:
	// `defer shutdown()` as a panic/early-return safety net, plus an
	// explicit shutdown() call guaranteed to run before Wait(), must not
	// be flagged -- the explicit call's own correctly-recorded position
	// already proves the signal is sent in time, regardless of the
	// deferred call's misleading one.
	diags := analyzeSourceWithStopWrapper(t, `package p
import "sync"
func shutdown() {}
func waitForShutdown() {}
func Start() {
	defer shutdown()
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		waitForShutdown()
	}()
	shutdown()
	wg.Wait()
}
`, "example.test/input.shutdown")
	if len(diags) != 0 {
		t.Fatalf("a defer-as-safety-net alongside an explicit stop_wrapper call before Wait() should not fire, got diagnostics = %#v", diags)
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

func TestGroupIdentity_UnusedLocalAliasDoesNotSuppressUnjoined(t *testing.T) {
	// Taking a group's address into a local pointer variable that is
	// itself independently tracked (any *sync.WaitGroup-typed local
	// qualifies per groupKind, not just ones passed to a function) is not
	// evidence that the group was transferred anywhere -- only that an
	// alias now exists. Before resolveAliasEscapes existed,
	// observeEscapeAssignment set wg.Escapes the moment `p := &wg` was
	// observed, regardless of whether p was ever used for anything
	// afterward -- silently discarding a real, otherwise-detected
	// unjoined-group finding. p here is declared and immediately
	// discarded; it must not launder wg past LL1003.
	diags := analyzeSource(t, `package p
import "sync"
func Start(){
	var wg sync.WaitGroup
	wg.Add(1)
	go func(){ defer wg.Done() }()
	p := &wg
	_ = p
	// wg is never waited
}
`)
	if len(diags) != 1 || diags[0].RuleID != "LL1003" {
		t.Fatalf("an unused local alias of a group must not suppress its own unjoined finding, got %#v", diags)
	}
}

func TestGroupIdentity_ReassignedAliasDoesNotSuppressEitherGroup(t *testing.T) {
	// The exact reported shape: `p := &a; p = &b`, with only "b" ever
	// started and never waited, and p never itself used for anything.
	// Both assignments independently set p as a pending alias for their
	// own RHS group ("a" for the first, "b" for the second); since p's
	// own final state shows no activity at all, neither assignment
	// discharges its group's obligation. "a" stays silent because it was
	// genuinely never used for anything (Starts == 0, not because of any
	// alias reasoning) -- only "b" should fire.
	diags := analyzeSource(t, `package p
import "sync"
func Start(){
	var a, b sync.WaitGroup
	p := &a
	p = &b
	b.Add(1)
	go func(){}()
	_ = p
	// b is never waited
}
`)
	if len(diags) != 1 || diags[0].RuleID != "LL1003" || !strings.Contains(diags[0].Message, `"b"`) {
		t.Fatalf("a group reassigned into an otherwise-unused alias must still fire its own unjoined finding, got %#v", diags)
	}
}

func TestGroupIdentity_AliasLaterVerifiedJoinedStaysConservative(t *testing.T) {
	// The flip side of the two tests above, guarding against
	// overcorrection: when the alias p *is* later put to genuine use
	// (here, verified-consumed by a one-hop helper that calls p.Wait()),
	// resolveAliasEscapes must still mark the original wg as Escapes, not
	// Joined -- the same conservative "unverified defaults to safe"
	// answer as before this fix existed. Directly crediting wg with
	// p's own Joined status would require trusting that p still points to
	// wg by the time p.Wait() runs, which -- as
	// TestGroupIdentity_ReassignedAliasDoesNotSuppressEitherGroup's own
	// reassignment shape demonstrates -- is not something this analysis
	// verifies. Silence here is the correct, conservative answer, not an
	// unrelated gap.
	diags := analyzeSource(t, `package p
import "sync"
func foo(g *sync.WaitGroup) { g.Wait() }
func Start(){
	var wg sync.WaitGroup
	wg.Add(1)
	go func(){ defer wg.Done() }()
	p := &wg
	foo(p)
}
`)
	if len(diags) != 0 {
		t.Fatalf("an alias later verified-joined through a helper should still resolve conservatively (Escapes, not a false positive), got %#v", diags)
	}
}

func TestGroupIdentity_AliasedPointerVariableDoesNotFireSpuriousFinding(t *testing.T) {
	// A *sync.WaitGroup-typed local variable is tracked as its own group
	// binding purely by type (groupKind), the same as an ordinary
	// function parameter receiving a group by pointer -- but p here is
	// not a genuinely separate WaitGroup, just another name for wg's
	// exact same underlying value. Before the second, unconditional rule
	// in resolveAliasEscapes existed, this was a confirmed false
	// positive: wg.Done() and wg.Wait() are attributed to wg's own
	// binding (which stays silent regardless, since wg.Starts == 0 --
	// wg.Add() is never called directly, only through p), while
	// p.Add(1) is attributed to p's own, entirely separate binding,
	// leaving p accounting for a start with no join ever observed
	// through its own name -- spurious LL1003 on code that is actually
	// correct and safe at runtime (wg's counter genuinely goes
	// 0 -> 1 -> 0, and Wait() genuinely unblocks).
	diags := analyzeSource(t, `package p
import "sync"
func Start(){
	var wg sync.WaitGroup
	var p *sync.WaitGroup
	p = &wg
	p.Add(1)
	go func(){ wg.Done() }()
	wg.Wait()
}
`)
	if len(diags) != 0 {
		t.Fatalf("a pointer variable that is merely an alias for an already-tracked group must not independently fire, got %#v", diags)
	}
}

func TestGroupIdentity_AliasedPointerVariableUsedConsistentlyDoesNotFire(t *testing.T) {
	// The same aliased pointer variable, but with every call (Add,
	// Done, Wait) consistently made through p rather than split across
	// p and wg. This was already silent before the fix (p's own
	// accounting is internally balanced and joined), and must remain
	// silent after it -- the unconditional alias-suppression rule
	// applies regardless, since p is still a recorded alias target for
	// wg, but the outcome here (silence) does not change.
	diags := analyzeSource(t, `package p
import "sync"
func Start(){
	var wg sync.WaitGroup
	var p *sync.WaitGroup
	p = &wg
	p.Add(1)
	go func(){ p.Done() }()
	p.Wait()
}
`)
	if len(diags) != 0 {
		t.Fatalf("a consistently-used pointer alias should stay silent, got %#v", diags)
	}
}

func TestCancelIdentity_ReassignedAliasDoesNotSuppressEitherCancel(t *testing.T) {
	// The cancel-side analogue: two independently constructed cancel
	// functions, with the first variable reassigned to hold the second's
	// value. Before resolveAliasEscapes existed, this reassignment alone
	// marked cancel2 as Escapes (its value was "assigned into a tracked
	// binding"), even though cancel1 -- the only place cancel2's value
	// was assigned to -- is never itself called. Both must now fire.
	diags := analyzeSource(t, `package p
import "context"
func Start(parent context.Context){
	ctx1, cancel1 := context.WithCancel(parent)
	_, cancel2 := context.WithCancel(ctx1)
	cancel1 = cancel2
	_ = cancel1
}
`)
	if len(diags) != 2 {
		t.Fatalf("reassigning one cancel binding to hold another's value must not suppress either's lost-cancel finding, got %#v", diags)
	}
	for _, d := range diags {
		if d.RuleID != "LL1001" {
			t.Fatalf("expected both diagnostics to be LL1001, got %#v", diags)
		}
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

func TestOrderingSemantics_DeferredWaitAtTopIsNotFlagged(t *testing.T) {
	// A `defer wg.Wait()` placed immediately after the WaitGroup is
	// declared -- the canonical way to guarantee a join on every return
	// path, mirroring `defer cancel()` for a cancel function -- must not
	// be flagged, even though internal/cfg records a defer at the defer
	// statement's own lexical position rather than at the function's
	// actual exit (see internal/cfg/cfg.go's handling of *ast.DeferStmt).
	// That positioning puts this Wait call site in the entry block,
	// lexically before the later Add()/go; computeGroupOrdering's
	// ReachableAvoiding(entry, {entry-block}) then returns the empty set
	// (ReachableAvoiding treats a start block that is itself in the
	// avoid set as unable to reach anything, per
	// TestReachableAvoiding_StartInAvoidSetIsEmpty in
	// internal/model/cfg_algorithms_test.go), so JoinedOnAllPaths comes
	// out true -- the correct "not flagged" verdict for this snippet, but
	// for a block-granularity reason that has nothing to do with actually
	// understanding defer's run-at-return semantics. This test exists so
	// that a future change to defer's CFG placement, or to
	// computeGroupOrdering's block-granularity reachability check, can't
	// silently start flagging this extremely common, correct idiom as a
	// join-ordering violation. See docs/limitations.md.
	diags := analyzeSource(t, `package p
import "sync"
func Start(){
	var wg sync.WaitGroup
	defer wg.Wait()
	wg.Add(1)
	go func(){ defer wg.Done() }()
}
`)
	if len(diags) != 0 {
		t.Fatalf("a deferred wg.Wait() declared immediately after the WaitGroup, with Add/go following it lexically, should not be flagged as join-not-on-all-paths, got %#v", diags)
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
