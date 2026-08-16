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
