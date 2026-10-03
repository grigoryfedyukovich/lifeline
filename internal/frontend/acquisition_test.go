package frontend

import (
	"strings"
	"testing"
)

// Regression tests for audit finding F3: cleanup obligations begin where the
// resource is acquired (or the work is started), so a path that never got
// that far owes nothing, while a path that did and then bailed out still
// does. Each case below is judged on the rule IDs it produces.

const ctxHeader = "package p\nimport \"context\"\n"
const wgHeader = "package p\nimport \"sync\"\n"

func rulesOf(t *testing.T, src string) string {
	t.Helper()
	var ids []string
	for _, d := range analyzeSource(t, src) {
		ids = append(ids, d.RuleID)
	}
	return strings.Join(ids, ",")
}

func TestAcquisition_Cancel(t *testing.T) {
	cases := []struct {
		name, body, want string
	}{
		// The audit's acceptance matrix.
		{"conditional acquire and defer is clean",
			`func f(enabled bool, parent context.Context) {
	if enabled {
		_, cancel := context.WithCancel(parent)
		defer cancel()
	}
}`, ""},
		{"conditional acquire and plain cancel is clean",
			`func f(c bool, parent context.Context) {
	if c {
		_, cancel := context.WithCancel(parent)
		cancel()
	}
}`, ""},
		{"conditional cleanup after unconditional acquire warns",
			`func f(c bool, parent context.Context) {
	_, cancel := context.WithCancel(parent)
	if c { cancel() }
}`, "LL1001"},
		{"early return before acquire is clean (deferred)",
			`func f(c bool, parent context.Context) {
	if c { return }
	_, cancel := context.WithCancel(parent)
	defer cancel()
}`, ""},
		{"early return before acquire is clean (plain cancel)",
			`func f(c bool, parent context.Context) {
	if c { return }
	_, cancel := context.WithCancel(parent)
	cancel()
}`, ""},
		{"early return after acquire warns",
			`func f(c bool, parent context.Context) {
	_, cancel := context.WithCancel(parent)
	if c { return }
	cancel()
}`, "LL1001"},

		// Deferred calls run at exit on every path that registered them.
		{"defer right after acquire covers a later early return",
			`func f(c bool, parent context.Context) {
	_, cancel := context.WithCancel(parent)
	defer cancel()
	if c { return }
}`, ""},
		{"defer registered only after an early return does not cover it",
			`func f(c bool, parent context.Context) {
	_, cancel := context.WithCancel(parent)
	if c { return }
	defer cancel()
}`, "LL1001"},
		{"defer inside a branch does not cover the other branch",
			`func f(c bool, parent context.Context) {
	_, cancel := context.WithCancel(parent)
	if c { defer cancel() }
}`, "LL1001"},

		// Acquisition inside other control flow.
		{"acquire in a loop body with cancel in the same iteration is clean",
			`func f(n int, parent context.Context) {
	for i := 0; i < n; i++ {
		_, cancel := context.WithCancel(parent)
		cancel()
	}
}`, ""},
		{"break between acquire and cancel in a loop warns",
			`func f(c bool, parent context.Context) {
	for {
		_, cancel := context.WithCancel(parent)
		if c { break }
		cancel()
	}
}`, "LL1001"},
		{"one switch case acquires and cleans, others do not acquire",
			`func f(k int, parent context.Context) {
	switch k {
	case 1:
		_, cancel := context.WithCancel(parent)
		defer cancel()
	case 2:
	default:
		return
	}
}`, ""},
		{"acquire in if-init statement, cancelled only in the body, warns on the else path",
			`func f(c bool, parent context.Context) {
	if _, cancel := context.WithCancel(parent); c {
		cancel()
	}
}`, "LL1001"},
		{"nothing is owed on the path that skips a guarded acquire, but the acquired path still must clean up",
			`func f(a, b bool, parent context.Context) {
	if a {
		_, cancel := context.WithCancel(parent)
		if b { return }
		cancel()
	}
}`, "LL1001"},
	}
	for _, c := range cases {
		if got := rulesOf(t, ctxHeader+c.body); got != c.want {
			t.Errorf("%s: rules = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestAcquisition_Group(t *testing.T) {
	cases := []struct {
		name, body, want string
	}{
		{"conditionally started and joined group is clean",
			`func f(c bool) {
	if c {
		var wg sync.WaitGroup
		wg.Add(1)
		go func() { defer wg.Done() }()
		wg.Wait()
	}
}`, ""},
		{"early return before any start is clean",
			`func f(c bool) {
	if c { return }
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done() }()
	wg.Wait()
}`, ""},
		{"early return after the start warns",
			`func f(c bool) {
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done() }()
	if c { return }
	wg.Wait()
}`, "LL1003"},
		{"conditional Wait after an unconditional start warns",
			`func f(c bool) {
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done() }()
	if c { wg.Wait() }
}`, "LL1003"},
		{"deferred Wait written above the Add is clean",
			`func f() {
	var wg sync.WaitGroup
	defer wg.Wait()
	wg.Add(1)
	go func() { defer wg.Done() }()
}`, ""},
		{"deferred Wait registered after an early return does not cover it",
			`func f(c bool) {
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done() }()
	if c { return }
	defer wg.Wait()
}`, "LL1003"},
		{"a start in only one branch, joined after the branches, is clean",
			`func f(c bool) {
	var wg sync.WaitGroup
	if c {
		wg.Add(1)
		go func() { defer wg.Done() }()
	}
	wg.Wait()
}`, ""},
		{"a start in only one branch, joined only in the other, warns",
			`func f(c bool) {
	var wg sync.WaitGroup
	if c {
		wg.Add(1)
		go func() { defer wg.Done() }()
	} else {
		wg.Wait()
	}
}`, "LL1003"},
		{"start inside a closure cannot be placed, so no path verdict is invented",
			`func f(c bool) {
	var wg sync.WaitGroup
	func() {
		wg.Add(1)
		go func() { defer wg.Done() }()
	}()
	if c { wg.Wait() }
}`, ""},
	}
	for _, c := range cases {
		if got := rulesOf(t, wgHeader+c.body); got != c.want {
			t.Errorf("%s: rules = %q, want %q", c.name, got, c.want)
		}
	}
}

// A constructor's returned handle is checked from the call that produced
// it, not from the caller's entry (the constructor-caller half of F3).
func TestAcquisition_ConstructorCaller(t *testing.T) {
	cases := []struct {
		name, body, want string
	}{
		{"conditional call and consume is clean",
			`func caller(p context.Context, c bool) {
	if c {
		h := New(p)
		h.cancel()
	}
}`, ""},
		{"early return before the call is clean",
			`func caller(p context.Context, c bool) {
	if c { return }
	h := New(p)
	h.cancel()
}`, ""},
		{"early return after the call warns",
			`func caller(p context.Context, c bool) {
	h := New(p)
	if c { return }
	h.cancel()
}`, "LL1001"},
		{"conditional consume after an unconditional call warns",
			`func caller(p context.Context, c bool) {
	h := New(p)
	if c { h.cancel() }
}`, "LL1001"},
		{"deferred consume right after the call covers a later early return",
			`func caller(p context.Context, c bool) {
	h := New(p)
	defer h.cancel()
	if c { return }
}`, ""},
		{"deferred consume registered after an early return does not cover it",
			`func caller(p context.Context, c bool) {
	h := New(p)
	if c { return }
	defer h.cancel()
}`, "LL1001"},
	}
	for _, c := range cases {
		if got := rulesOf(t, f2Ctor+c.body); got != c.want {
			t.Errorf("%s: rules = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestAcquisition_GroupConstructorCaller(t *testing.T) {
	ctor := wgHeader + `type Pool struct{ wg *sync.WaitGroup }
func New() *Pool {
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done() }()
	return &Pool{wg: &wg}
}
`
	cases := []struct{ name, body, want string }{
		{"conditional call and join is clean",
			`func caller(c bool) { if c { p := New(); p.wg.Wait() } }`, ""},
		{"early return after the call warns",
			`func caller(c bool) { p := New(); if c { return }; p.wg.Wait() }`, "LL1003"},
	}
	for _, c := range cases {
		if got := rulesOf(t, ctor+c.body); got != c.want {
			t.Errorf("%s: rules = %q, want %q", c.name, got, c.want)
		}
	}
}
