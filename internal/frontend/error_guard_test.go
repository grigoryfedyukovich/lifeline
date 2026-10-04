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

// Tests for the error-returning factory half of audit finding F3: an error
// branch may create no resource, so `ctx, cancel, err := f(); if err != nil
// { return err }; defer cancel()` owes no cancel on the error path, while an
// unrelated early return after a successful call still does.

func analyzeWithWrapper(t *testing.T, source string, wrappers ...string) []engine.Diagnostic {
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
	cfg.ContextWrappers = wrappers
	program, err := Build(Input{Fset: fset, Files: []*ast.File{file}, Pkg: pkg, Info: info}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	return engine.Analyze(program, cfg)
}

const errWrapHdr = `package p
import ("context"; "errors")
var errBoom = errors.New("boom")
func other() error { return nil }
func Make(parent context.Context, fail bool) (context.Context, context.CancelFunc, error) {
	if fail { return nil, nil, errBoom }
	ctx, cancel := context.WithCancel(parent)
	return ctx, cancel, nil
}
`

func wrapRules(t *testing.T, body string) string {
	t.Helper()
	var ids []string
	for _, d := range analyzeWithWrapper(t, errWrapHdr+body, "example.test/input.Make") {
		ids = append(ids, d.RuleID)
	}
	return strings.Join(ids, ",")
}

func TestErrorGuard_ContextWrapper(t *testing.T) {
	cases := []struct{ name, body, want string }{
		// The idiom.
		{"check, return, defer cancel is clean",
			`func f(p context.Context) error {
	ctx, cancel, err := Make(p, false)
	if err != nil { return err }
	defer cancel()
	<-ctx.Done()
	return nil
}`, ""},
		{"check, return, plain cancel is clean",
			`func f(p context.Context) error {
	_, cancel, err := Make(p, false)
	if err != nil { return err }
	cancel()
	return nil
}`, ""},
		{"err == nil block with defer is clean",
			`func f(p context.Context) {
	_, cancel, err := Make(p, false)
	if err == nil { defer cancel() }
}`, ""},
		{"err == nil with else-return is clean",
			`func f(p context.Context) error {
	_, cancel, err := Make(p, false)
	if err == nil {
		defer cancel()
	} else {
		return err
	}
	return nil
}`, ""},
		{"negated condition is understood",
			`func f(p context.Context) error {
	_, cancel, err := Make(p, false)
	if !(err == nil) { return err }
	defer cancel()
	return nil
}`, ""},
		{"err != nil && c: the then branch implies an error, every other path reaches the defer",
			`func f(p context.Context, c bool) error {
	_, cancel, err := Make(p, false)
	if err != nil && c { return err }
	defer cancel()
	return nil
}`, ""},
		{"err != nil || c: the then branch can be entered with a nil error, so it is not skipped",
			`func f(p context.Context, c bool) error {
	_, cancel, err := Make(p, false)
	if err != nil || c { return err }
	defer cancel()
	return nil
}`, "LL1001"},
		{"err == nil && c: the else branch can be entered with a nil error, so it is not skipped",
			`func f(p context.Context, c bool) error {
	_, cancel, err := Make(p, false)
	if err == nil && c {
		defer cancel()
	} else {
		return err
	}
	return nil
}`, "LL1001"},
		{"err == nil || c: the else branch implies an error",
			`func f(p context.Context, c bool) error {
	_, cancel, err := Make(p, false)
	if err == nil || c {
		defer cancel()
	} else {
		return err
	}
	return nil
}`, ""},
		{"check inside the if-init statement",
			`func f(p context.Context) error {
	if _, cancel, err := Make(p, false); err != nil {
		return err
	} else {
		defer cancel()
	}
	return nil
}`, ""},

		// Things the guard must not excuse.
		{"no check at all still warns",
			`func f(p context.Context) {
	_, cancel, _ := Make(p, false)
	_ = cancel
}`, "LL1001"},
		{"error ignored into a variable that is never checked still warns",
			`func f(p context.Context) {
	_, cancel, err := Make(p, false)
	_ = err
	_ = cancel
}`, "LL1001"},
		{"unrelated early return after the check still warns",
			`func f(p context.Context, c bool) error {
	_, cancel, err := Make(p, false)
	if err != nil { return err }
	if c { return nil }
	defer cancel()
	return nil
}`, "LL1001"},
		{"defer registered after an unrelated early return still warns",
			`func f(p context.Context, c bool) error {
	_, cancel, err := Make(p, false)
	if err != nil { return err }
	if c { return nil }
	cancel()
	return nil
}`, "LL1001"},
		{"cancel only under an unrelated condition still warns",
			`func f(p context.Context, c bool) error {
	_, cancel, err := Make(p, false)
	if err != nil { return err }
	if c { cancel() }
	return nil
}`, "LL1001"},
		{"err reassigned before a second check: the second check proves nothing about Make",
			`func f(p context.Context) error {
	_, cancel, err := Make(p, false)
	if err != nil { return err }
	err = other()
	if err != nil { return err }
	defer cancel()
	return nil
}`, "LL1001"},
		{"err reassigned before the only check: that check is about other()",
			`func f(p context.Context) error {
	_, cancel, err := Make(p, false)
	err = other()
	if err != nil { return err }
	defer cancel()
	return nil
}`, "LL1001"},
		{"a closure assigns err: nothing is skipped",
			`func f(p context.Context) error {
	_, cancel, err := Make(p, false)
	set := func() { err = other() }
	set()
	if err != nil { return err }
	defer cancel()
	return nil
}`, "LL1001"},
		{"the address of err is taken: nothing is skipped",
			`func f(p context.Context) error {
	_, cancel, err := Make(p, false)
	ep := &err
	_ = ep
	if err != nil { return err }
	defer cancel()
	return nil
}`, "LL1001"},
		{"check on a different error variable is not the factory's",
			`func f(p context.Context) error {
	_, cancel, _ := Make(p, false)
	err := other()
	if err != nil { return err }
	defer cancel()
	return nil
}`, "LL1001"},
		{"a check in a loop whose body reassigns err is dropped",
			`func f(p context.Context, n int) error {
	_, cancel, err := Make(p, false)
	for i := 0; i < n; i++ {
		if err != nil { return err }
		err = other()
	}
	defer cancel()
	return nil
}`, "LL1001"},
	}
	for _, c := range cases {
		if got := wrapRules(t, c.body); got != c.want {
			t.Errorf("%s: rules = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestErrorGuard_EvidenceNamesTheAssumption(t *testing.T) {
	// The guard rests on the factory's contract, which is assumed; when it
	// is what makes the verdict, the binding says so.
	src := errWrapHdr + `func f(p context.Context) error {
	_, cancel, err := Make(p, false)
	if err != nil { return err }
	defer cancel()
	return nil
}`
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "input.go", src, parser.ParseComments)
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
	cfg.ContextWrappers = []string{"example.test/input.Make"}
	program, err := Build(Input{Fset: fset, Files: []*ast.File{file}, Pkg: pkg, Info: info}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, fn := range program.Functions {
		if !strings.HasSuffix(fn.Name, ".f") {
			continue
		}
		for _, c := range fn.Cancels {
			for _, ev := range c.Evidence {
				if ev.Kind == "error-guard" {
					found = true
				}
			}
		}
	}
	if !found {
		t.Errorf("no error-guard evidence on the binding whose verdict depends on the error branch")
	}
	_ = engine.Analyze
}

// The same guard for an error-returning constructor whose handle is
// returned in a struct field: `h, err := New(...); if err != nil { return }`.
func TestErrorGuard_ConstructorCaller(t *testing.T) {
	ctor := `package p
import ("context"; "errors")
type Handle struct{ cancel context.CancelFunc }
func other() error { return nil }
func New(parent context.Context, fail bool) (*Handle, error) {
	if fail { return nil, errors.New("x") }
	_, cancel := context.WithCancel(parent)
	return &Handle{cancel: cancel}, nil
}
`
	cases := []struct{ name, body, want string }{
		{"check, return, defer consume is clean",
			`func caller(p context.Context) error {
	h, err := New(p, false)
	if err != nil { return err }
	defer h.cancel()
	return nil
}`, ""},
		{"check, return, plain consume is clean",
			`func caller(p context.Context) error {
	h, err := New(p, false)
	if err != nil { return err }
	h.cancel()
	return nil
}`, ""},
		{"handle dropped after the check still warns",
			`func caller(p context.Context) error {
	h, err := New(p, false)
	if err != nil { return err }
	_ = h
	return nil
}`, "LL1001"},
		{"unrelated early return after the check still warns",
			`func caller(p context.Context, c bool) error {
	h, err := New(p, false)
	if err != nil { return err }
	if c { return nil }
	h.cancel()
	return nil
}`, "LL1001"},
		{"err reassigned before the check still warns",
			`func caller(p context.Context) error {
	h, err := New(p, false)
	err = other()
	if err != nil { return err }
	defer h.cancel()
	return nil
}`, "LL1001"},
		{"no error result at all is unchanged",
			`func caller(p context.Context, c bool) {
	h, _ := New(p, false)
	if c { return }
	h.cancel()
}`, "LL1001"},
	}
	for _, c := range cases {
		if got := rulesOf(t, ctor+c.body); got != c.want {
			t.Errorf("%s: rules = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestErrorGuard_GroupConstructorCaller(t *testing.T) {
	ctor := `package p
import ("errors"; "sync")
type Pool struct{ wg *sync.WaitGroup }
func New(fail bool) (*Pool, error) {
	if fail { return nil, errors.New("x") }
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done() }()
	return &Pool{wg: &wg}, nil
}
`
	cases := []struct{ name, body, want string }{
		{"check, return, join is clean",
			`func caller() error { p, err := New(false); if err != nil { return err }; p.wg.Wait(); return nil }`, ""},
		{"unrelated early return after the check warns",
			`func caller(c bool) error { p, err := New(false); if err != nil { return err }; if c { return nil }; p.wg.Wait(); return nil }`, "LL1003"},
	}
	for _, c := range cases {
		if got := rulesOf(t, ctor+c.body); got != c.want {
			t.Errorf("%s: rules = %q, want %q", c.name, got, c.want)
		}
	}
}
