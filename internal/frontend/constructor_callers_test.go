package frontend

import (
	"sort"
	"strings"
	"testing"
)

// Regression tests for audit finding F2: every constructor call creates a
// distinct handle, so each caller's obligation is judged on its own. One
// caller consuming its handle must never clear another caller's leak, a
// leaking caller must never blame a good one or the constructor, and none
// of that may depend on declaration order.

const f2Ctor = `package p
import "context"
type Handle struct{ cancel context.CancelFunc }
func New(parent context.Context) *Handle {
	_, cancel := context.WithCancel(parent)
	return &Handle{cancel: cancel}
}
`

// findingFunctions returns, sorted, the enclosing function of every
// diagnostic of the given rule, and fails if any other rule fired.
func findingFunctions(t *testing.T, src, rule string) []string {
	t.Helper()
	var fns []string
	for _, d := range analyzeSource(t, src) {
		if d.RuleID != rule {
			t.Fatalf("unexpected %s diagnostic: %#v", d.RuleID, d)
		}
		fns = append(fns, strings.TrimPrefix(d.Function, "example.test/input."))
	}
	sort.Strings(fns)
	return fns
}

func wantFunctions(t *testing.T, name string, got []string, want ...string) {
	t.Helper()
	sort.Strings(want)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("%s: findings in %v, want %v", name, got, want)
	}
}

// permutations of small fixed sets, so every test below runs in every
// declaration order without hand-writing each one.
func orderings(parts []string) [][]string {
	var out [][]string
	var rec func(cur, rest []string)
	rec = func(cur, rest []string) {
		if len(rest) == 0 {
			out = append(out, append([]string(nil), cur...))
			return
		}
		for i := range rest {
			next := append(append([]string(nil), rest[:i]...), rest[i+1:]...)
			rec(append(cur, rest[i]), next)
		}
	}
	rec(nil, parts)
	return out
}

func TestConstructorCallers_GoodCallerCannotHideBadCaller(t *testing.T) {
	good := `func good(parent context.Context) { h := New(parent); h.cancel() }`
	bad := `func bad(parent context.Context) { h := New(parent); _ = h }`
	for _, order := range orderings([]string{good, bad}) {
		src := f2Ctor + strings.Join(order, "\n")
		wantFunctions(t, "audit example", findingFunctions(t, src, "LL1001"), "bad")
	}
}

func TestConstructorCallers_EveryBadCallerReportedRegardlessOfGoodOnes(t *testing.T) {
	good := `func good(parent context.Context) { h := New(parent); h.cancel() }`
	bad1 := `func bad1(parent context.Context) { h := New(parent); _ = h }`
	bad2 := `func bad2(parent context.Context) { h := New(parent); _ = h }`
	for _, order := range orderings([]string{good, bad1, bad2}) {
		src := f2Ctor + strings.Join(order, "\n")
		wantFunctions(t, "two bad, one good", findingFunctions(t, src, "LL1001"), "bad1", "bad2")
	}
}

func TestConstructorCallers_PartialCallerNotClearedByFullCaller(t *testing.T) {
	partial := `func partial(parent context.Context, c bool) { h := New(parent); if c { h.cancel() } }`
	full := `func full(parent context.Context) { h := New(parent); h.cancel() }`
	for _, order := range orderings([]string{partial, full}) {
		src := f2Ctor + strings.Join(order, "\n")
		diags := analyzeSource(t, src)
		if len(diags) != 1 || diags[0].RuleID != "LL1001" || !strings.HasSuffix(diags[0].Function, ".partial") ||
			!strings.Contains(diags[0].Message, "some but not every path") {
			t.Errorf("want one partial-path LL1001 in partial, got %#v", diags)
		}
	}
}

func TestConstructorCallers_LeakIsReportedAtTheCallNotTheConstructor(t *testing.T) {
	diags := analyzeSource(t, f2Ctor+`func bad(parent context.Context) {
	h := New(parent)
	_ = h
}
`)
	if len(diags) != 1 {
		t.Fatalf("diagnostics = %#v, want one", diags)
	}
	d := diags[0]
	if d.Function != "example.test/input.bad" || d.Position.StartLine != 9 {
		t.Errorf("finding at %s line %d, want the h := New(parent) call in bad (line 9)", d.Function, d.Position.StartLine)
	}
	if d.SuggestedFix != nil {
		t.Errorf("a caller-side finding has no cancel variable to defer; SuggestedFix = %#v, want none", d.SuggestedFix)
	}
}

func TestConstructorCallers_NoCallersOrOnlyGoodCallersIsClean(t *testing.T) {
	if diags := analyzeSource(t, f2Ctor); len(diags) != 0 {
		t.Errorf("uncalled constructor: %#v", diags)
	}
	src := f2Ctor + `func a(p context.Context) { h := New(p); h.cancel() }
func b(p context.Context) { h := New(p); defer h.cancel() }
`
	if diags := analyzeSource(t, src); len(diags) != 0 {
		t.Errorf("only good callers: %#v", diags)
	}
}

func TestConstructorCallers_ConstructorThatCancelsItselfOwesNothing(t *testing.T) {
	// New cancels on every path itself (the returned handle is redundant),
	// so a caller ignoring the handle leaks nothing.
	src := `package p
import "context"
type Handle struct{ cancel context.CancelFunc }
func New(parent context.Context) *Handle {
	_, cancel := context.WithCancel(parent)
	defer cancel()
	return &Handle{cancel: cancel}
}
func bad(parent context.Context) { h := New(parent); _ = h }
`
	if diags := analyzeSource(t, src); len(diags) != 0 {
		t.Errorf("constructor discharges its own obligation, got %#v", diags)
	}
}

func TestConstructorCallers_UnverifiableCallerNeitherClearsNorBlames(t *testing.T) {
	// `escape` hands the handle to an unresolvable interface method: that
	// call site is unverified, so it produces no finding, and it must
	// not clear (or be blamed for) the verifiable bad caller.
	unknown := `type sink interface{ take(*Handle) }
func escape(p context.Context, s sink) { h := New(p); s.take(h) }`
	bad := `func bad(p context.Context) { h := New(p); _ = h }`
	for _, order := range orderings([]string{unknown, bad}) {
		src := f2Ctor + strings.Join(order, "\n")
		wantFunctions(t, "unverifiable + bad", findingFunctions(t, src, "LL1001"), "bad")
	}
}

func TestConstructorCallers_TwoCallsInOneCallerJudgedSeparately(t *testing.T) {
	src := f2Ctor + `func mixed(p context.Context) {
	a := New(p)
	a.cancel()
	b := New(p)
	_ = b
}
`
	diags := analyzeSource(t, src)
	if len(diags) != 1 || diags[0].Position.StartLine != 11 {
		t.Fatalf("want exactly one LL1001 at the second call (line 11), got %#v", diags)
	}
}

func TestConstructorCallers_PassThroughWrapperCallersJudgedIndividually(t *testing.T) {
	src := f2Ctor + `func Middle(p context.Context) *Handle { return New(p) }
func good(p context.Context) { h := Middle(p); h.cancel() }
func bad(p context.Context) { h := Middle(p); _ = h }
func direct(p context.Context) { h := New(p); h.cancel() }
`
	wantFunctions(t, "wrapper", findingFunctions(t, src, "LL1001"), "bad")
}

func TestConstructorCallers_SameCancelReturnedTwiceIsDischargedByEitherField(t *testing.T) {
	// One cancel function reachable through two results is one
	// obligation: consuming it through either is enough, and the caller
	// consuming only the second result must not be reported.
	ctor := `package p
import "context"
type A struct{ c context.CancelFunc }
type B struct{ c context.CancelFunc }
func New(parent context.Context) (*A, *B) {
	_, cancel := context.WithCancel(parent)
	return &A{c: cancel}, &B{c: cancel}
}
`
	//
	// Both results are bound to variables on purpose: with `_` for the
	// unconsumed one, that result is never verified at all and the test
	// would pass vacuously. Here the first result is bound and dropped, so
	// a check that only knew the first (fn, result, field) site would call
	// this a leak.
	src := ctor + `func useSecond(p context.Context) { a, b := New(p); _ = a; b.c() }
func useFirst(p context.Context) { a, b := New(p); _ = b; a.c() }
`
	if diags := analyzeSource(t, src); len(diags) != 0 {
		t.Errorf("consuming through either result discharges the obligation, got %#v", diags)
	}
	leak := ctor + `func dropBoth(p context.Context) { a, b := New(p); _, _ = a, b }
`
	wantFunctions(t, "dropping both results", findingFunctions(t, leak, "LL1001"), "dropBoth")
}

func TestConstructorCallers_GroupCallersJudgedIndividually(t *testing.T) {
	ctor := `package p
import "sync"
type Pool struct{ wg *sync.WaitGroup }
func New() *Pool {
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done() }()
	return &Pool{wg: &wg}
}
`
	good := `func good() { p := New(); p.wg.Wait() }`
	bad := `func bad() { p := New(); _ = p }`
	for _, order := range orderings([]string{good, bad}) {
		src := ctor + strings.Join(order, "\n")
		wantFunctions(t, "group good+bad", findingFunctions(t, src, "LL1003"), "bad")
	}
}

func TestConstructorCallers_GroupPartialJoinNotClearedByFullJoin(t *testing.T) {
	ctor := `package p
import "sync"
type Pool struct{ wg *sync.WaitGroup }
func New() *Pool {
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done() }()
	return &Pool{wg: &wg}
}
`
	partial := `func partial(c bool) { p := New(); if c { p.wg.Wait() } }`
	full := `func full() { p := New(); p.wg.Wait() }`
	for _, order := range orderings([]string{partial, full}) {
		src := ctor + strings.Join(order, "\n")
		wantFunctions(t, "group partial+full", findingFunctions(t, src, "LL1003"), "partial")
	}
}

// A constructor's literal Add/Done imbalance is a fact about the
// constructor's own code. The constructor's binding is settled as
// transferred once a caller is verified, so when a caller drops the handle
// the caller-side finding must still carry the mismatch note, as the
// constructor-side finding did before findings moved to call sites.
func TestConstructorCallers_GroupCountMismatchSurvivesCallerAttribution(t *testing.T) {
	src := `package p
import "sync"
type Pool struct{ wg *sync.WaitGroup }
func New() *Pool {
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done() }()
	return &Pool{wg: &wg}
}
func bad() { p := New(); _ = p }
`
	diags := analyzeSource(t, src)
	if len(diags) != 1 || diags[0].RuleID != "LL1003" || !strings.HasSuffix(diags[0].Function, ".bad") {
		t.Fatalf("want one LL1003 in bad, got %#v", diags)
	}
	if !strings.Contains(diags[0].Message, "Add/Done accounting also shows an outstanding worker") {
		t.Errorf("the constructor's Add/Done mismatch was dropped from the finding: %q", diags[0].Message)
	}
}
