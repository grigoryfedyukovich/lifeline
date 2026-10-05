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
	"github.com/gfedyukovich/lifeline/internal/model"
)

// analyzeWithEffectLookup analyzes source with a cross-package effect lookup
// installed, the way vet mode supplies one from an imported fact.
func analyzeWithEffectLookup(t *testing.T, source string, lookup func(*types.Func, int) (model.ParamEffect, bool)) []engine.Diagnostic {
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
	program, err := Build(Input{Fset: fset, Files: []*ast.File{file}, Pkg: pkg, Info: info, LookupParamEffects: lookup}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	return engine.Analyze(program, cfg)
}

// Regression tests for audit finding F6: what a callee does with a cancel
// function or group passed to it is a set of explicit effects (may / must /
// returns / opaque), applied at the call, not one "consumed somewhere" bit.

const effHeader = "package p\nimport (\"context\"; \"sync\")\nvar _ sync.WaitGroup\nvar sink context.CancelFunc\nvar keep *sync.WaitGroup\nfunc work(context.Context) {}\n"

func cancelOwner(call string) string {
	return "func owner(parent context.Context, c bool) {\n\tctx, cancel := context.WithCancel(parent)\n\t" + call + "\n\tgo work(ctx)\n}\n"
}

func groupOwner(call string) string {
	return "func owner(c bool) {\n\tvar wg sync.WaitGroup\n\twg.Add(1)\n\tgo func() { defer wg.Done() }()\n\t" + call + "\n}\n"
}

func TestParamEffects_CancelMatrix(t *testing.T) {
	cases := []struct{ name, helper, call, want string }{
		// The audit's example: a wrapper around conditional cleanup must not
		// erase its condition.
		{"conditional cleanup is not erased", `func maybe(cancel context.CancelFunc, enabled bool) { if enabled { cancel() } }`, `maybe(cancel, c)`, "LL1001"},
		{"conditional cleanup, three hops down", `func h1(c context.CancelFunc, e bool) { h2(c, e) }
func h2(c context.CancelFunc, e bool) { h3(c, e) }
func h3(c context.CancelFunc, e bool) { if e { c() } }`, `h1(cancel, c)`, "LL1001"},
		{"early return skips the cleanup", `func h(cancel context.CancelFunc, e bool) { if e { return }; cancel() }`, `h(cancel, c)`, "LL1001"},
		{"switch with no default", `func h(c context.CancelFunc, k int) { switch k { case 1: c() } }`, `h(cancel, 1)`, "LL1001"},

		// A must effect is applied at the call: clean when the call is
		// unconditional, partial when the call itself is conditional.
		{"always cancels", `func always(cancel context.CancelFunc) { cancel() }`, `always(cancel)`, ""},
		{"always cancels, deferred call", `func always(cancel context.CancelFunc) { cancel() }`, `defer always(cancel)`, ""},
		{"always cancels via its own defer", `func h(cancel context.CancelFunc) { defer cancel() }`, `h(cancel)`, ""},
		{"always cancels, call is conditional", `func always(cancel context.CancelFunc) { cancel() }`, `if c { always(cancel) }`, "LL1001"},
		{"switch with a default cancels on every case", `func h(c context.CancelFunc, k int) { switch k { case 1: c(); default: c() } }`, `h(cancel, 1)`, ""},
		{"three hops that always cancel", `func h1(c context.CancelFunc) { h2(c) }
func h2(c context.CancelFunc) { h3(c) }
func h3(c context.CancelFunc) { c() }`, `h1(cancel)`, ""},
		{"self recursion that bottoms out in a cancel", `func h(c context.CancelFunc, n int) { if n <= 0 { c(); return }; h(c, n-1) }`, `h(cancel, 3)`, ""},
		{"mutual recursion that bottoms out in a cancel", `func ping(c context.CancelFunc, n int) { if n <= 0 { c(); return }; pong(c, n-1) }
func pong(c context.CancelFunc, n int) { ping(c, n-1) }`, `pong(cancel, 3)`, ""},

		// Unreachable cleanup and cleanup that never runs establish nothing.
		{"cleanup after an unconditional return", `func h(cancel context.CancelFunc) { return; cancel() }`, `h(cancel)`, "LL1001"},
		{"cleanup only in a closure that is never invoked", `func h(cancel context.CancelFunc) { f := func() { cancel() }; _ = f }`, `h(cancel)`, "LL1001"},
		{"cleanup assigned to blank", `func h(cancel context.CancelFunc) { _ = func() { cancel() } }`, `h(cancel)`, "LL1001"},
		{"never touched", `func h(cancel context.CancelFunc) {}`, `h(cancel)`, "LL1001"},
		{"cycle with no consumer anywhere", `func ping(c context.CancelFunc, n int) { if n < 9 { pong(c, n+1) } }
func pong(c context.CancelFunc, n int) { if n < 9 { ping(c, n+1) } }`, `ping(cancel, 0)`, "LL1001"},

		// Hand-offs stay permissive, as before: ownership may have moved.
		{"stored in a package variable", `func h(cancel context.CancelFunc) { sink = cancel }`, `h(cancel)`, ""},
		{"returned to the caller", `func h(cancel context.CancelFunc) context.CancelFunc { return cancel }`, `h(cancel)`, ""},
		{"used inside a literal that may run", `func h(cancel context.CancelFunc) { func() { cancel() }() }`, `h(cancel)`, ""},
	}
	for _, c := range cases {
		src := effHeader + c.helper + "\n" + cancelOwner(c.call)
		if got := rulesOnly(findings(t, src)); got != c.want {
			t.Errorf("%s: rules = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestParamEffects_GroupMatrix(t *testing.T) {
	cases := []struct{ name, helper, call, want string }{
		{"conditional Wait is not erased", `func maybeWait(g *sync.WaitGroup, e bool) { if e { g.Wait() } }`, `maybeWait(&wg, c)`, "LL1003"},
		{"always Wait", `func alwaysWait(g *sync.WaitGroup) { g.Wait() }`, `alwaysWait(&wg)`, ""},
		{"always Wait, deferred call", `func alwaysWait(g *sync.WaitGroup) { g.Wait() }`, `defer alwaysWait(&wg)`, ""},
		{"always Wait via its own defer", `func h(g *sync.WaitGroup) { defer g.Wait() }`, `h(&wg)`, ""},
		{"always Wait, call is conditional", `func alwaysWait(g *sync.WaitGroup) { g.Wait() }`, `if c { alwaysWait(&wg) }`, "LL1003"},
		{"Wait after an unconditional return", `func h(g *sync.WaitGroup) { return; g.Wait() }`, `h(&wg)`, "LL1003"},
		{"never waits", `func nop(g *sync.WaitGroup) {}`, `nop(&wg)`, "LL1003"},
		// The helper that forwards the group elsewhere is not the helper
		// that waits: it is a hand-off, labeled as such, not a join.
		{"forwards the group elsewhere", `func fwd(g *sync.WaitGroup) { keep = g }`, `fwd(&wg)`, ""},
	}
	for _, c := range cases {
		src := effHeader + c.helper + "\n" + groupOwner(c.call)
		if got := rulesOnly(findings(t, src)); got != c.want {
			t.Errorf("%s: rules = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestParamEffects_ForwardingIsLabeledAsAHandOffNotAJoin(t *testing.T) {
	src := effHeader + "func fwd(g *sync.WaitGroup) { keep = g }\n" + groupOwner("fwd(&wg)")
	program := buildProgramFor(t, src, nil)
	var evidence []string
	for _, fn := range program.Functions {
		if strings.HasSuffix(fn.Name, ".owner") {
			for _, g := range fn.Groups {
				for _, ev := range g.Evidence {
					evidence = append(evidence, ev.Kind)
				}
			}
		}
	}
	joined := strings.Join(evidence, ",")
	if !strings.Contains(joined, "parameter-transferred") || strings.Contains(joined, "callee-joins") {
		t.Errorf("evidence kinds = %q; a forwarding helper must be a parameter-transferred hand-off, never callee-joins", joined)
	}
}

func effectsOf(t *testing.T, src, fn string) map[int]model.ParamEffect {
	t.Helper()
	for _, f := range buildProgramFor(t, src, nil).Functions {
		if strings.HasSuffix(f.Name, "."+fn) {
			return f.ParamEffects
		}
	}
	t.Fatalf("function %s not found", fn)
	return nil
}

func TestParamEffects_ExportedValues(t *testing.T) {
	cases := []struct {
		name, helper string
		want         model.ParamEffect
	}{
		{"always", `func h(cancel context.CancelFunc) { cancel() }`, model.EffectMay | model.EffectMust},
		{"deferred", `func h(cancel context.CancelFunc) { defer cancel() }`, model.EffectMay | model.EffectMust},
		{"conditional", `func h(cancel context.CancelFunc, c bool) { if c { cancel() } }`, model.EffectMay},
		{"never", `func h(cancel context.CancelFunc) {}`, 0},
		{"unreachable", `func h(cancel context.CancelFunc) { return; cancel() }`, 0},
		{"dead closure", `func h(cancel context.CancelFunc) { _ = func() { cancel() } }`, 0},
		{"stored", `func h(cancel context.CancelFunc) { sink = cancel }`, model.EffectOpaque},
		{"returned", `func h(cancel context.CancelFunc) context.CancelFunc { return cancel }`, model.EffectReturns},
		{"called and stored", `func h(cancel context.CancelFunc) { cancel(); sink = cancel }`, model.EffectMay | model.EffectMust | model.EffectOpaque},
		{"live closure", `func h(cancel context.CancelFunc) { func() { cancel() }() }`, model.EffectOpaque},
	}
	for _, c := range cases {
		got, ok := effectsOf(t, effHeader+c.helper+"\n", "h")[0]
		if !ok {
			t.Errorf("%s: no recorded effect for parameter 0", c.name)
			continue
		}
		if got != c.want {
			t.Errorf("%s: effect = %v, want %v", c.name, got, c.want)
		}
	}
}

// "Exported and local effects have the same semantics": the effect a helper
// exports, applied to a caller in another package through the lookup hook,
// must give the same diagnostics as the same helper in the same package.
// context.AfterFunc(ctx, cancel) stands in for the external callee (its
// second parameter has the shape of a cancel function, and its declaration is
// not part of the parsed source).
func TestParamEffects_ExportedAndLocalHaveTheSameSemantics(t *testing.T) {
	helpers := []struct{ name, body string }{
		{"none", ``},
		{"may", `if ctx.Err() != nil { cancel() }`},
		{"must", `cancel()`},
		{"must via defer", `defer cancel()`},
		{"opaque", `sink = cancel`},
		{"must and opaque", `cancel(); sink = cancel`},
		{"unreachable", `return nil; cancel()`},
	}
	owners := []struct{ name, call string }{
		{"direct", `%s`},
		{"conditional", `if c { %s }`},
		{"deferred", `defer %s`},
	}
	for _, h := range helpers {
		local := effHeader + "func helper(ctx context.Context, cancel context.CancelFunc) func() bool {\n\t" + h.body + "\n\treturn nil\n}\n"
		effect := effectsOf(t, local, "helper")[1]
		for _, o := range owners {
			localSrc := local + cancelOwner(strings.ReplaceAll(o.call, "%s", "helper(ctx, cancel)"))
			externalSrc := effHeader + cancelOwner(strings.ReplaceAll(o.call, "%s", "context.AfterFunc(ctx, cancel)"))
			wantRules := rulesOnly(findings(t, localSrc))

			lookup := func(fn *types.Func, index int) (model.ParamEffect, bool) {
				if fn != nil && fn.Pkg() != nil && fn.Pkg().Path() == "context" && fn.Name() == "AfterFunc" && index == 1 {
					return effect, true
				}
				return 0, false
			}
			var got []string
			for _, d := range analyzeWithEffectLookup(t, externalSrc, lookup) {
				got = append(got, d.RuleID)
			}
			if strings.Join(got, ",") != wantRules {
				t.Errorf("helper %q (effect %v) called %s: same-package gives %q, via the exported effect %q", h.name, effect, o.name, wantRules, strings.Join(got, ","))
			}
		}
	}
}

func TestParamEffects_ExportedGroupSemanticsMatchLocal(t *testing.T) {
	// A WaitGroup parameter's effect crosses the package boundary the same
	// way. fmt.Fprintln(w, ...)'s first parameter is not a group, so this
	// uses the Input hook directly through a same-shaped stand-in: os.Exit's
	// signature has no group parameter either, so the check is on the
	// exported value itself: a conditional Wait must export as may-only.
	got, ok := effectsOf(t, effHeader+`func h(g *sync.WaitGroup, c bool) { if c { g.Wait() } }`+"\n", "h")[0]
	if !ok || got != model.EffectMay {
		t.Errorf("conditional Wait exports %v, want may-only", got)
	}
	got, ok = effectsOf(t, effHeader+`func h(g *sync.WaitGroup) { g.Wait() }`+"\n", "h")[0]
	if !ok || got != model.EffectMay|model.EffectMust {
		t.Errorf("unconditional Wait exports %v, want may+must", got)
	}
}

func TestParamEffects_LegacyBooleanLookupStillWorksAsAnOpaqueHandOff(t *testing.T) {
	src := effHeader + cancelOwner("context.AfterFunc(ctx, cancel)")
	consumed := func(fn *types.Func, index int) (bool, bool) {
		if fn != nil && fn.Pkg() != nil && fn.Pkg().Path() == "context" && fn.Name() == "AfterFunc" && index == 1 {
			return true, true
		}
		return false, false
	}
	if diags := analyzeSourceWithLookup(t, src, consumed); len(diags) != 0 {
		t.Errorf("a legacy 'consumed' answer is an opaque hand-off and must not report, got %#v", diags)
	}
	notConsumed := func(fn *types.Func, index int) (bool, bool) {
		if fn != nil && fn.Pkg() != nil && fn.Pkg().Path() == "context" && fn.Name() == "AfterFunc" && index == 1 {
			return false, true
		}
		return false, false
	}
	if got := rulesOnly(findingsFromDiags(analyzeSourceWithLookup(t, src, notConsumed))); got != "LL1001" {
		t.Errorf("a legacy 'not consumed' answer is a checked absence and must report, got %q", got)
	}
}

// Done() effects: an observed Done anywhere is not presented as an all-path
// or exactly-once guarantee.
func TestParamEffects_DoneEffects(t *testing.T) {
	doneOf := func(src string) model.ParamEffect {
		for _, f := range buildProgramFor(t, src, nil).Functions {
			if strings.HasSuffix(f.Name, ".worker") {
				return f.ParamDoneEffects[0]
			}
		}
		t.Fatalf("worker not found")
		return 0
	}
	hdr := "package p\nimport \"sync\"\n"
	cases := []struct {
		name, body string
		want       model.ParamEffect
	}{
		{"unconditional", `func worker(wg *sync.WaitGroup) { wg.Done() }`, model.EffectMay | model.EffectMust},
		{"deferred", `func worker(wg *sync.WaitGroup) { defer wg.Done() }`, model.EffectMay | model.EffectMust},
		{"conditional", `func worker(wg *sync.WaitGroup, c bool) { if c { wg.Done() } }`, model.EffectMay},
		{"early return first", `func worker(wg *sync.WaitGroup, c bool) { if c { return }; wg.Done() }`, model.EffectMay},
		{"after an unconditional return", `func worker(wg *sync.WaitGroup) { return; wg.Done() }`, 0},
		{"never", `func worker(wg *sync.WaitGroup) {}`, 0},
		{"delegated to a must helper", `func worker(wg *sync.WaitGroup) { done(wg) }
func done(wg *sync.WaitGroup) { wg.Done() }`, model.EffectMay | model.EffectMust},
		{"delegated to a may helper", `func worker(wg *sync.WaitGroup, c bool) { done(wg, c) }
func done(wg *sync.WaitGroup, c bool) { if c { wg.Done() } }`, model.EffectMay},
		{"delegated conditionally to a must helper", `func worker(wg *sync.WaitGroup, c bool) { if c { done(wg) } }
func done(wg *sync.WaitGroup) { wg.Done() }`, model.EffectMay},
	}
	for _, c := range cases {
		if got := doneOf(hdr + c.body + "\n"); got != c.want {
			t.Errorf("%s: Done effect = %v, want %v", c.name, got, c.want)
		}
	}
}

// A worker that only sometimes calls Done() is counted as at most one Done:
// the tally is an upper bound, so an undercount that holds even if every
// conditional Done fires is still a finding, and no exact balance is claimed.
func TestParamEffects_ConditionalDoneWorkerCountsAsAnUpperBound(t *testing.T) {
	hdr := "package p\nimport \"sync\"\n"
	must := "func worker(wg *sync.WaitGroup) { defer wg.Done() }\n"
	cond := must + "func sometimes(wg *sync.WaitGroup, c bool) { if c { wg.Done() } }\n"
	cases := []struct{ name, src, want string }{
		{"two must workers balance Add(2)", hdr + must + "func f() { var wg sync.WaitGroup; wg.Add(2); go worker(&wg); go worker(&wg); wg.Wait() }", ""},
		{"one must worker undercounts Add(2)", hdr + must + "func f() { var wg sync.WaitGroup; wg.Add(2); go worker(&wg); wg.Wait() }", "LL1003"},
		{"a conditional worker may balance Add(2): nothing is claimed", hdr + cond + "func f(c bool) { var wg sync.WaitGroup; wg.Add(2); go worker(&wg); go sometimes(&wg, c); wg.Wait() }", ""},
		{"Add(3) is short even if the conditional fires", hdr + cond + "func f(c bool) { var wg sync.WaitGroup; wg.Add(3); go worker(&wg); go sometimes(&wg, c); wg.Wait() }", "LL1003"},
		{"a worker that never calls Done does not count", hdr + "func idle(wg *sync.WaitGroup) {}\nfunc f() { var wg sync.WaitGroup; wg.Add(1); go idle(&wg); wg.Wait() }", "LL1003"},
		{"a worker whose Done is unreachable does not count", hdr + "func dead(wg *sync.WaitGroup) { return; wg.Done() }\nfunc f() { var wg sync.WaitGroup; wg.Add(1); go dead(&wg); wg.Wait() }", "LL1003"},
	}
	for _, c := range cases {
		if got := rulesOnly(findings(t, c.src)); got != c.want {
			t.Errorf("%s: rules = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestParamEffects_EffectSetHelpers(t *testing.T) {
	if !(model.EffectMay | model.EffectMust).Has(model.EffectMust) {
		t.Error("may|must must contain must")
	}
	if model.EffectMay.Has(model.EffectMust) {
		t.Error("may alone must not contain must")
	}
	if !model.EffectOpaque.Hands() || !model.EffectReturns.Hands() || (model.EffectMay | model.EffectMust).Hands() {
		t.Error("Hands() must be true exactly for returns/opaque")
	}
	if got := (model.EffectMay | model.EffectOpaque).String(); got != "may+opaque" {
		t.Errorf("String() = %q", got)
	}
	if model.ParamEffect(0).String() != "none" {
		t.Error("zero effect must print as none")
	}
}

func findingsFromDiags(diags []engine.Diagnostic) []string {
	var out []string
	for _, d := range diags {
		out = append(out, d.RuleID+"@"+strings.TrimPrefix(d.Function, "example.test/input."))
	}
	return out
}
