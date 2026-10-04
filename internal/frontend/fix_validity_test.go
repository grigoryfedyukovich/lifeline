package frontend

import (
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/gfedyukovich/lifeline/internal/config"
	"github.com/gfedyukovich/lifeline/internal/engine"
	"github.com/gfedyukovich/lifeline/internal/model"
)

// Regression tests for audit finding F4: an automatic edit must produce
// valid Go that keeps the resource's scope and lifetime, or not be offered.
// Every emitted fix in these tests is applied to the source and the result
// is parsed and type-checked.

const fixHeader = "package p\nimport \"context\"\nfunc use(context.Context) {}\n"

func analyzeForFixes(t *testing.T, source string, wrappers ...string) []engine.Diagnostic {
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
		t.Fatalf("test source does not type-check: %v", err)
	}
	cfg := config.Default()
	cfg.ContextWrappers = wrappers
	program, err := Build(Input{Fset: fset, Files: []*ast.File{file}, Pkg: pkg, Info: info}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	return engine.Analyze(program, cfg)
}

func applyEdits(src string, edits []model.FixEdit) string {
	sorted := append([]model.FixEdit(nil), edits...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Span.StartOffset > sorted[j].Span.StartOffset })
	for _, e := range sorted {
		src = src[:e.Span.StartOffset] + e.NewText + src[e.Span.EndOffset:]
	}
	return src
}

// checkFixed applies every fix in diags (each alone, then all together) and
// reports an error for any result that does not parse or type-check.
func checkFixed(t *testing.T, name, src string, diags []engine.Diagnostic) (fixes int) {
	t.Helper()
	var all []model.FixEdit
	for _, d := range diags {
		if d.SuggestedFix == nil {
			continue
		}
		fixes++
		all = append(all, d.SuggestedFix.Edits...)
		if err := compiles(applyEdits(src, d.SuggestedFix.Edits)); err != nil {
			t.Errorf("%s: fix for %s at line %d does not compile: %v\n--- result ---\n%s", name, d.RuleID, d.Position.StartLine, err, applyEdits(src, d.SuggestedFix.Edits))
		}
	}
	if len(all) > 0 {
		if err := compiles(applyEdits(src, all)); err != nil {
			t.Errorf("%s: all fixes applied together do not compile: %v\n--- result ---\n%s", name, err, applyEdits(src, all))
		}
	}
	return fixes
}

func compiles(src string) error {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "out.go", src, 0)
	if err != nil {
		return err
	}
	_, err = (&types.Config{Importer: importer.Default()}).Check("example.test/input", fset, []*ast.File{file}, &types.Info{})
	return err
}

func TestFixValidity_ScopeAndLifetime(t *testing.T) {
	cases := []struct {
		name, body string
		wantFix    bool
	}{
		// Safe: a standalone statement, executed at most once per call.
		{"standalone statement", `func f(p context.Context) { ctx, _ := context.WithCancel(p); use(ctx) }`, true},
		{"nested block", `func f(p context.Context, c bool) { if c { ctx, _ := context.WithCancel(p); use(ctx) } }`, true},
		{"deeply nested blocks", `func f(p context.Context, a, b bool) { if a { { if b { ctx, _ := context.WithCancel(p); use(ctx) } } } }`, true},
		{"switch case body", `func f(p context.Context, k int) { switch k { case 1: ctx, _ := context.WithCancel(p); use(ctx) } }`, true},
		{"select case body", `func f(p context.Context, ch chan int) { select { case <-ch: ctx, _ := context.WithCancel(p); use(ctx) } }`, true},
		{"trailing comment", "func f(p context.Context) { ctx, _ := context.WithCancel(p) // make\n\tuse(ctx) }", true},
		{"timeout factory", `func f(p context.Context) { ctx, _ := context.WithTimeout(p, 0); use(ctx) }`, true},
		{"cancel result first via configured wrapper is covered below", `func f(p context.Context) { ctx, _ := context.WithCancel(p); use(ctx) }`, true},

		// Unsafe: the init statement of if/for/switch cannot take `; defer`.
		{"if initializer", `func f(p context.Context) { if ctx, _ := context.WithCancel(p); ctx != nil { use(ctx) } }`, false},
		{"for initializer", `func f(p context.Context) { for ctx, _ := context.WithCancel(p); ctx != nil; { use(ctx); break } }`, false},
		{"switch initializer", `func f(p context.Context) { switch ctx, _ := context.WithCancel(p); ctx { default: use(ctx) } }`, false},
		{"type switch initializer", `func f(p context.Context) { switch ctx, _ := context.WithCancel(p); any(ctx).(type) { default: use(ctx) } }`, false},
		{"else-if initializer", `func f(p context.Context, c bool) { if c { } else if ctx, _ := context.WithCancel(p); ctx != nil { use(ctx) } }`, false},

		// Unsafe: a defer would hold every iteration's context until return.
		{"for body", `func f(p context.Context, n int) { for i := 0; i < n; i++ { ctx, _ := context.WithCancel(p); use(ctx) } }`, false},
		{"range body", `func f(p context.Context, xs []int) { for range xs { ctx, _ := context.WithCancel(p); use(ctx) } }`, false},
		{"infinite for body", `func f(p context.Context) { for { ctx, _ := context.WithCancel(p); use(ctx); return } }`, false},
		{"nested block inside a loop", `func f(p context.Context, c bool) { for { if c { ctx, _ := context.WithCancel(p); use(ctx) }; return } }`, false},
		{"switch case inside a loop", `func f(p context.Context, k int) { for { switch k { case 1: ctx, _ := context.WithCancel(p); use(ctx) }; return } }`, false},
		{"select case inside a loop", `func f(p context.Context, ch chan int) { for { select { case <-ch: ctx, _ := context.WithCancel(p); use(ctx) }; return } }`, false},
		{"loop after the statement does not matter", `func f(p context.Context, n int) { ctx, _ := context.WithCancel(p); use(ctx); for i := 0; i < n; i++ { } }`, true},

		// Unsafe: re-entrant control flow other than loops.
		{"labeled statement", `func f(p context.Context) { L: ctx, _ := context.WithCancel(p); use(ctx); goto L }`, false},
		{"goto elsewhere in the function", `func f(p context.Context, c bool) { ctx, _ := context.WithCancel(p); use(ctx); if c { goto done }; use(ctx); done: }`, false},

		// Unsafe: the deferred call would not compile.
		{"cancel cause func takes an argument", `func f(p context.Context) { ctx, _ := context.WithCancelCause(p); use(ctx) }`, false},

		// Not a short declaration: nothing to rewrite.
		{"plain assignment", `func f(p context.Context) { var ctx context.Context; ctx, _ = context.WithCancel(p); use(ctx) }`, false},
	}
	for _, c := range cases {
		src := fixHeader + c.body + "\n"
		diags := analyzeForFixes(t, src)
		var lost int
		for _, d := range diags {
			if d.RuleID == "LL1001" {
				lost++
			}
		}
		if lost != 1 {
			t.Errorf("%s: the diagnostic must stay even when the fix is withheld; LL1001 count = %d", c.name, lost)
			continue
		}
		fixes := checkFixed(t, c.name, src, diags)
		if (fixes == 1) != c.wantFix {
			t.Errorf("%s: fix emitted = %v, want %v", c.name, fixes == 1, c.wantFix)
		}
	}
}

func TestFixValidity_IdentifierCollisions(t *testing.T) {
	cases := []struct{ name, body string }{
		{"local named lifelineCancel", `func f(p context.Context) { lifelineCancel := 1; _ = lifelineCancel; ctx, _ := context.WithCancel(p); use(ctx) }`},
		{"locals named lifelineCancel and lifelineCancel2", `func f(p context.Context) { lifelineCancel, lifelineCancel2 := 1, 2; _, _ = lifelineCancel, lifelineCancel2; ctx, _ := context.WithCancel(p); use(ctx) }`},
		{"parameter named lifelineCancel", `func f(p context.Context, lifelineCancel int) { _ = lifelineCancel; ctx, _ := context.WithCancel(p); use(ctx) }`},
		{"package-level lifelineCancel used in the function", `var lifelineCancel = 1
func f(p context.Context) { _ = lifelineCancel; ctx, _ := context.WithCancel(p); use(ctx) }`},
		{"package-level lifelineCancel not mentioned", `var lifelineCancel = 1
func f(p context.Context) { ctx, _ := context.WithCancel(p); use(ctx) }`},
		{"a function named lifelineCancel", `func lifelineCancel() {}
func f(p context.Context) { lifelineCancel(); ctx, _ := context.WithCancel(p); use(ctx) }`},
		{"name used in a nested scope that follows", `func f(p context.Context, c bool) { ctx, _ := context.WithCancel(p); use(ctx); if c { lifelineCancel := 1; _ = lifelineCancel } }`},
		{"method receiver named lifelineCancel", `type T struct{}
func (lifelineCancel T) f(p context.Context) { ctx, _ := context.WithCancel(p); use(ctx) }`},
		{"three blanks in one function get distinct names", `func f(p context.Context, c bool) {
	ctx1, _ := context.WithCancel(p)
	use(ctx1)
	if c {
		ctx2, _ := context.WithCancel(p)
		use(ctx2)
	}
	ctx3, _ := context.WithCancel(p)
	use(ctx3)
}`},
	}
	for _, c := range cases {
		src := fixHeader + c.body + "\n"
		diags := analyzeForFixes(t, src)
		if fixes := checkFixed(t, c.name, src, diags); fixes == 0 {
			t.Errorf("%s: expected a (valid) fix to be offered, got none", c.name)
		}
	}
}

func TestFixValidity_DistinctNamesAcrossFixes(t *testing.T) {
	src := fixHeader + `func f(p context.Context, c bool) {
	ctx1, _ := context.WithCancel(p)
	use(ctx1)
	if c {
		ctx2, _ := context.WithCancel(p)
		use(ctx2)
	}
}
`
	seen := map[string]bool{}
	for _, d := range analyzeForFixes(t, src) {
		if d.SuggestedFix == nil {
			t.Fatalf("expected a fix at line %d", d.Position.StartLine)
		}
		name := d.SuggestedFix.Edits[0].NewText
		if seen[name] {
			t.Errorf("fix name %q reused by two fixes in one function", name)
		}
		seen[name] = true
	}
}

// A configured wrapper may return the cancel function in either position
// and with extra results; the fix must still replace the right blank.
func TestFixValidity_ConfiguredWrapperShapes(t *testing.T) {
	hdr := fixHeader + `
func Make(p context.Context) (context.Context, context.CancelFunc) { return context.WithCancel(p) }
func MakeSwapped(p context.Context) (context.CancelFunc, context.Context) { c, f := context.WithCancel(p); return f, c }
func MakeErr(p context.Context) (context.Context, context.CancelFunc, error) { c, f := context.WithCancel(p); return c, f, nil }
func MakeArg(p context.Context) (context.Context, func(int)) { c, _ := context.WithCancel(p); return c, func(int) {} }
`
	wr := []string{"example.test/input.Make", "example.test/input.MakeSwapped", "example.test/input.MakeErr", "example.test/input.MakeArg"}
	cases := []struct {
		name, body string
		wantFix    bool
	}{
		{"plain wrapper", `func f(p context.Context) { ctx, _ := Make(p); use(ctx) }`, true},
		{"cancel first", `func f(p context.Context) { _, ctx := MakeSwapped(p); use(ctx) }`, true},
		{"extra error result", `func f(p context.Context) { ctx, _, _ := MakeErr(p); use(ctx) }`, true},
		{"cancel takes an argument", `func f(p context.Context) { ctx, _ := MakeArg(p); use(ctx) }`, false},
	}
	for _, c := range cases {
		src := hdr + c.body + "\n"
		diags := analyzeForFixes(t, src, wr...)
		var fixed int
		for _, d := range diags {
			if d.RuleID == "LL1001" && strings.HasSuffix(d.Function, ".f") {
				if d.SuggestedFix != nil {
					fixed++
				}
			}
		}
		_ = checkFixed(t, c.name, src, diags)
		if (fixed == 1) != c.wantFix {
			t.Errorf("%s: fix emitted = %v, want %v", c.name, fixed == 1, c.wantFix)
		}
	}
}

// The repository's own examples are real input: every fix emitted for them
// must compile too.
func TestFixValidity_ExamplesThatEmitFixes(t *testing.T) {
	root := filepath.Join("..", "..", "examples")
	for _, ex := range []struct {
		dir     string
		wrapper string
	}{
		{"lost_cancel", ""},
		{"custom_context_wrapper", "example.test/input.ProjectWithCancel"},
	} {
		entries, err := os.ReadDir(filepath.Join(root, ex.dir))
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			if !strings.HasSuffix(e.Name(), ".go") {
				continue
			}
			raw, err := os.ReadFile(filepath.Join(root, ex.dir, e.Name()))
			if err != nil {
				t.Fatal(err)
			}
			src := string(raw)
			var wrappers []string
			if ex.wrapper != "" {
				wrappers = []string{ex.wrapper}
			}
			diags := analyzeForFixes(t, src, wrappers...)
			if checkFixed(t, ex.dir, src, diags) == 0 {
				t.Errorf("%s: expected the example to still offer its fix", ex.dir)
			}
		}
	}
}
