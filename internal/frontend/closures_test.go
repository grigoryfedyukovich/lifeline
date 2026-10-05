package frontend

import (
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"sort"
	"strings"
	"testing"

	"github.com/gfedyukovich/lifeline/internal/config"
	"github.com/gfedyukovich/lifeline/internal/model"
)

// Regression tests for audit finding F5: function literals are analysis
// units of their own. A cancel function or join group declared inside a
// literal is judged like one declared in a named function; defining a
// closure is not invoking it; nothing is reported twice.

const closureHeader = "package p\nimport (\"context\"; \"sync\")\nvar _ sync.WaitGroup\nfunc run(f func()) { f() }\nfunc use(context.Context) {}\n"

// findings returns "RULE@function" for every diagnostic, sorted.
func findings(t *testing.T, src string) []string {
	t.Helper()
	var out []string
	for _, d := range analyzeSource(t, src) {
		out = append(out, d.RuleID+"@"+strings.TrimPrefix(d.Function, "example.test/input."))
	}
	sort.Strings(out)
	return out
}

func rulesOnly(fs []string) string {
	var rs []string
	for _, f := range fs {
		rs = append(rs, f[:strings.Index(f, "@")])
	}
	return strings.Join(rs, ",")
}

// The audit's acceptance: moving code from a named function into a literal
// preserves findings. Every scenario runs as a named function body and
// wrapped five ways, and the rule IDs must be identical (and match the
// expectation, so a pass cannot come from everything going silent).
func TestClosures_MovingCodeIntoALiteralPreservesFindings(t *testing.T) {
	scenarios := []struct {
		name, body, want string
	}{
		{"cancel never called", `_, cancel := context.WithCancel(parent); _ = cancel`, "LL1001"},
		{"cancel discarded", `ctx, _ := context.WithCancel(parent); use(ctx)`, "LL1001"},
		{"cancel deferred", `_, cancel := context.WithCancel(parent); defer cancel()`, ""},
		{"cancel called", `_, cancel := context.WithCancel(parent); cancel()`, ""},
		{"cancel only under a condition", `_, cancel := context.WithCancel(parent); if c { cancel() }`, "LL1001"},
		{"early return after acquire", `_, cancel := context.WithCancel(parent); if c { return }; cancel()`, "LL1001"},
		{"early return before acquire", `if c { return }; _, cancel := context.WithCancel(parent); defer cancel()`, ""},
		{"conditional acquire and defer", `if c { _, cancel := context.WithCancel(parent); defer cancel() }`, ""},
		{"group never joined", `var wg sync.WaitGroup; wg.Add(1); go func() { defer wg.Done() }()`, "LL1003"},
		{"group joined", `var wg sync.WaitGroup; wg.Add(1); go func() { defer wg.Done() }(); wg.Wait()`, ""},
		{"group joined only under a condition", `var wg sync.WaitGroup; wg.Add(1); go func() { defer wg.Done() }(); if c { wg.Wait() }`, "LL1003"},
		{"group started only in a branch, joined there", `if c { var wg sync.WaitGroup; wg.Add(1); go func() { defer wg.Done() }(); wg.Wait() }`, ""},
	}
	wrappers := []struct{ name, open, close string }{
		{"go literal", "go func() {", "}()"},
		{"invoked literal", "func() {", "}()"},
		{"deferred literal", "defer func() {", "}()"},
		{"callback", "run(func() {", "})"},
		{"stored literal", "f := func() {", "}\n\tf()"},
	}
	for _, s := range scenarios {
		named := closureHeader + "func f(parent context.Context, c bool) {\n\t" + s.body + "\n}\n"
		base := findings(t, named)
		if got := rulesOnly(base); got != s.want {
			t.Errorf("%s: as a named function: rules = %q, want %q", s.name, got, s.want)
			continue
		}
		for _, w := range wrappers {
			src := closureHeader + "func f(parent context.Context, c bool) {\n\t" + w.open + "\n\t" + s.body + "\n\t" + w.close + "\n}\n"
			if got := rulesOnly(findings(t, src)); got != s.want {
				t.Errorf("%s inside a %s: rules = %q, want %q (same as the named function)", s.name, w.name, got, s.want)
			}
		}
	}
}

func TestClosures_FindingsAreAttributedToTheLiteralAtTheRightLine(t *testing.T) {
	src := closureHeader + `func outer(parent context.Context) {
	go func() {
		_, cancel := context.WithCancel(parent)
		_ = cancel
	}()
}
`
	diags := analyzeSource(t, src)
	if len(diags) != 1 {
		t.Fatalf("diagnostics = %#v, want one", diags)
	}
	d := diags[0]
	if d.Function != "example.test/input.outer.func1" {
		t.Errorf("function = %q, want the literal's own identity outer.func1", d.Function)
	}
	if d.Position.StartLine != 8 {
		t.Errorf("line = %d, want 8 (the WithCancel call inside the literal)", d.Position.StartLine)
	}
}

// Defining a closure is not invoking it: the audit's second criterion.
func TestClosures_ADefinedButNeverInvokedLiteralDischargesNothing(t *testing.T) {
	cases := []struct {
		name, body, want string
	}{
		{"assigned to blank", `_ = func() { cancel() }`, "LL1001"},
		{"var blank", `var _ = func() { cancel() }`, "LL1001"},
		{"held in a variable only ever blanked", `f := func() { cancel() }; _ = f`, "LL1001"},
		{"var-held and only blanked", `var f = func() { cancel() }; _ = f`, "LL1001"},
		{"blank literal nested inside a go literal", `go func() { _ = func() { cancel() } }()`, "LL1001"},
		{"a dead literal does not hide a live call", `_ = func() { cancel() }; cancel()`, ""},

		// Invoked or transferred: unchanged, credited as before.
		{"immediately invoked", `func() { cancel() }()`, ""},
		{"deferred literal", `defer func() { cancel() }()`, ""},
		{"go literal", `go func() { cancel() }()`, ""},
		{"stored and called", `f := func() { cancel() }; f()`, ""},
		{"stored and deferred", `f := func() { cancel() }; defer f()`, ""},
		{"passed to a callee", `run(func() { cancel() })`, ""},
		{"stored and passed on", `f := func() { cancel() }; run(f)`, ""},
	}
	for _, c := range cases {
		src := closureHeader + "func f(parent context.Context) {\n\t_, cancel := context.WithCancel(parent)\n\t" + c.body + "\n}\n"
		if got := rulesOnly(findings(t, src)); got != c.want {
			t.Errorf("%s: rules = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestClosures_ADefinedButNeverInvokedLiteralDoesNotJoinAGroup(t *testing.T) {
	cases := []struct{ name, body, want string }{
		{"blank literal waiting", `_ = func() { wg.Wait() }`, "LL1003"},
		{"invoked literal waiting", `func() { wg.Wait() }()`, ""},
		{"deferred literal waiting", `defer func() { wg.Wait() }()`, ""},
	}
	for _, c := range cases {
		src := closureHeader + "func f() {\n\tvar wg sync.WaitGroup\n\twg.Add(1)\n\tgo func() { defer wg.Done() }()\n\t" + c.body + "\n}\n"
		if got := rulesOnly(findings(t, src)); got != c.want {
			t.Errorf("%s: rules = %q, want %q", c.name, got, c.want)
		}
	}
}

// A literal owns only what it declares: a variable declared outside is used
// outside too, where the literal's unit cannot see.
func TestClosures_ALiteralOnlyOwnsWhatItDeclares(t *testing.T) {
	cases := []struct{ name, src string }{
		{"cancel declared outside, assigned inside, called outside", closureHeader + `func f(parent context.Context) {
	var cancel context.CancelFunc
	done := make(chan struct{})
	go func() {
		_, cancel = context.WithCancel(parent)
		close(done)
	}()
	<-done
	cancel()
}
`},
		{"group declared outside, started inside, joined outside", closureHeader + `func f() {
	var wg sync.WaitGroup
	go func() {
		wg.Add(1)
		go func() { defer wg.Done() }()
	}()
	wg.Wait()
}
`},
	}
	for _, c := range cases {
		for _, f := range findings(t, c.src) {
			if strings.Contains(f, ".func") {
				t.Errorf("%s: literal unit reported %s about a variable it does not own", c.name, f)
			}
		}
	}
}

// Nested goroutines were already attributed to the declaration that
// contains them; the literal units must add nothing to that.
func TestClosures_NestedGoroutinesAreNotReportedTwice(t *testing.T) {
	cases := []struct {
		name, src string
		want      []string
	}{
		{"loop two goroutines down", "package p\nfunc start() {\n\tgo func() {\n\t\tgo func() {\n\t\t\tfor {\n\t\t\t}\n\t\t}()\n\t}()\n}\n",
			[]string{"LL1002@start"}},
		{"two sibling goroutines", "package p\nfunc start() {\n\tgo func() { for {} }()\n\tgo func() { for {} }()\n}\n",
			[]string{"LL1002@start", "LL1002@start"}},
		{"group started in a nested goroutine", "package p\nimport \"sync\"\nfunc start() {\n\tvar wg sync.WaitGroup\n\tgo func() {\n\t\twg.Add(1)\n\t\tgo func() { defer wg.Done() }()\n\t}()\n}\n",
			[]string{"LL1003@start"}},
	}
	for _, c := range cases {
		got := findings(t, c.src)
		if strings.Join(got, ",") != strings.Join(c.want, ",") {
			t.Errorf("%s: findings = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestClosures_NestedLiteralsLeakingTwoLevelsDown(t *testing.T) {
	src := closureHeader + `func start(parent context.Context) {
	go func() {
		go func() {
			_, cancel := context.WithCancel(parent)
			_ = cancel
		}()
	}()
}
`
	got := findings(t, src)
	if strings.Join(got, ",") != "LL1001@start.func1.1" {
		t.Errorf("findings = %v, want exactly one, attributed to start.func1.1", got)
	}
}

func buildProgramFor(t *testing.T, source string, mutate func(*config.Config)) model.Program {
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
	if mutate != nil {
		mutate(&cfg)
	}
	program, err := Build(Input{Fset: fset, Files: []*ast.File{file}, Pkg: pkg, Info: info}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	return program
}

func TestClosures_StableIdentities(t *testing.T) {
	src := `package p
type T struct{}
func (t *T) m() {
	_ = func() { _ = func() {} }
	_ = func() {}
}
func f() {
	_ = func() {}
	_ = func() { _ = func() {}; _ = func() {} }
}
`
	var names []string
	for _, fn := range buildProgramFor(t, src, nil).Functions {
		names = append(names, strings.ReplaceAll(fn.Name, "example.test/input.", ""))
	}
	want := []string{
		"(*T).m", "f",
		"(*T).m.func1", "(*T).m.func1.1", "(*T).m.func2",
		"f.func1", "f.func2", "f.func2.1", "f.func2.2",
	}
	if strings.Join(names, " ") != strings.Join(want, " ") {
		t.Errorf("function identities =\n  %v\nwant\n  %v", names, want)
	}
}

func TestClosures_TheFunctionBoundCoversLiteralsAndKeepsNamedFunctionsFirst(t *testing.T) {
	src := `package p
func a() { _ = func() {} }
func b() {}
`
	program := buildProgramFor(t, src, nil)
	if program.FunctionCount != 3 {
		t.Errorf("FunctionCount = %d, want 3 (two declarations and one literal)", program.FunctionCount)
	}
	for _, c := range []struct {
		limit     int
		want      []string
		truncated bool
	}{
		{10, []string{"a", "b", "a.func1"}, false},
		{3, []string{"a", "b", "a.func1"}, false},
		{2, []string{"a", "b"}, true},
		{1, []string{"a"}, true},
	} {
		p := buildProgramFor(t, src, func(cfg *config.Config) { cfg.MaxFunctions = c.limit })
		var names []string
		for _, fn := range p.Functions {
			names = append(names, strings.ReplaceAll(fn.Name, "example.test/input.", ""))
		}
		if strings.Join(names, ",") != strings.Join(c.want, ",") || p.Truncated != c.truncated {
			t.Errorf("max_functions=%d: analyzed %v truncated=%v, want %v truncated=%v", c.limit, names, p.Truncated, c.want, c.truncated)
		}
	}
}

func TestClosures_SuppressionCommentsApplyToLiteralFindings(t *testing.T) {
	src := closureHeader + `func f(parent context.Context) {
	go func() {
		_, cancel := context.WithCancel(parent) //lifeline:ignore LL1001
		_ = cancel
	}()
}
`
	if got := findings(t, src); len(got) != 0 {
		t.Errorf("a suppressed literal finding was reported: %v", got)
	}
}

// The literal units add no goroutine, summary or IR records of their own.
func TestClosures_LiteralUnitsCarryOnlyBindings(t *testing.T) {
	src := closureHeader + `func f(parent context.Context) {
	go func() {
		_, cancel := context.WithCancel(parent)
		defer cancel()
		go func() {}()
	}()
}
`
	for _, fn := range buildProgramFor(t, src, nil).Functions {
		if !strings.Contains(fn.Name, ".func") {
			continue
		}
		if len(fn.Goroutines) != 0 || len(fn.IR) != 0 || len(fn.BodyLifecycle.Evidence) != 0 || fn.BodyLifecycle.CFG != nil {
			t.Errorf("%s: literal unit carries goroutines=%d ir=%d summary=%v; those belong to the enclosing declaration", fn.Name, len(fn.Goroutines), len(fn.IR), fn.BodyLifecycle)
		}
		want := 0
		if strings.HasSuffix(fn.Name, ".func1") {
			want = 1 // the outer literal declares the cancel; the empty nested one declares nothing
		}
		if len(fn.Cancels) != want {
			t.Errorf("%s: cancels = %d, want %d", fn.Name, len(fn.Cancels), want)
		}
	}
}
