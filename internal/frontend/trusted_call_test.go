package frontend

import "testing"

// Regression tests for audit finding F1: a call that receives a tracked
// context (context delegation) or a configured stop wrapper is trusted to
// honor cancellation, but it may still return normally. It must not delete
// the code after it from the control-flow graph, or a later unconditional
// loop silently disappears from LL1002's analysis.

func countRule(t *testing.T, source, rule string) int {
	t.Helper()
	n := 0
	for _, d := range analyzeSource(t, source) {
		if d.RuleID == rule {
			n++
		}
	}
	return n
}

func TestTrustedCall_DelegationBeforeLaterLoopStillWarns(t *testing.T) {
	// The audit's own regression candidate: ignore returns normally, so the
	// worker reaches an unconditional infinite loop.
	src := `package p
import "context"
func ignore(context.Context) {}
func worker(ctx context.Context) {
	ignore(ctx)
	for {}
}
func start(ctx context.Context) { go worker(ctx) }
`
	if got := countRule(t, src, "LL1002"); got != 1 {
		t.Fatalf("LL1002 count = %d, want 1: delegating ctx to a call that returns must not hide a later infinite loop", got)
	}
}

func TestTrustedCall_DelegationBeforeLaterLoopStillWarns_FuncLit(t *testing.T) {
	src := `package p
import "context"
func ignore(context.Context) {}
func start(ctx context.Context) {
	go func() {
		ignore(ctx)
		for {}
	}()
}
`
	if got := countRule(t, src, "LL1002"); got != 1 {
		t.Fatalf("LL1002 count = %d, want 1 for a goroutine literal", got)
	}
}

func TestTrustedCall_ContextConstructionBeforeLoopStillWarns(t *testing.T) {
	// context.WithCancel(ctx) is the most common context-taking call there
	// is and obviously returns; it must not make the loop after it dead code.
	src := `package p
import "context"
func worker(ctx context.Context) {
	c, cancel := context.WithCancel(ctx)
	defer cancel()
	_ = c
	for {}
}
func start(ctx context.Context) { go worker(ctx) }
`
	diags := analyzeSource(t, src)
	if len(diags) != 1 || diags[0].RuleID != "LL1002" {
		t.Fatalf("diagnostics = %#v, want exactly one LL1002", diags)
	}
}

func TestTrustedCall_DelegationInsideLoopStillResolvesThatLoop(t *testing.T) {
	// Unchanged permissive behavior: delegating the context inside the loop
	// is credited as that loop's stop path.
	for name, src := range map[string]string{
		"named": `package p
import "context"
func step(context.Context) {}
func worker(ctx context.Context) {
	for { step(ctx) }
}
func start(ctx context.Context) { go worker(ctx) }
`,
		"literal": `package p
import "context"
func step(context.Context) {}
func start(ctx context.Context) {
	go func() {
		for { step(ctx) }
	}()
}
`,
		"inside a branch of the loop": `package p
import "context"
func step(context.Context) {}
func worker(ctx context.Context, cond func() bool) {
	for {
		if cond() { step(ctx) }
	}
}
func start(ctx context.Context, cond func() bool) { go worker(ctx, cond) }
`,
		"delegation before and inside the loop": `package p
import "context"
func step(context.Context) {}
func worker(ctx context.Context) {
	step(ctx)
	for { step(ctx) }
}
func start(ctx context.Context) { go worker(ctx) }
`,
	} {
		if got := countRule(t, src, "LL1002"); got != 0 {
			t.Errorf("%s: LL1002 count = %d, want 0", name, got)
		}
	}
}

func TestTrustedCall_GenuineExitAfterDelegationStillResolves(t *testing.T) {
	src := `package p
import "context"
func ignore(context.Context) {}
func worker(ctx context.Context) {
	ignore(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
	}
}
func start(ctx context.Context) { go worker(ctx) }
`
	if got := countRule(t, src, "LL1002"); got != 0 {
		t.Fatalf("LL1002 count = %d, want 0: the loop has a real ctx.Done() return", got)
	}
}

func TestTrustedCall_DelegationWithoutLaterLoopIsClean(t *testing.T) {
	src := `package p
import "context"
func step(context.Context) {}
func worker(ctx context.Context) {
	c, cancel := context.WithCancel(ctx)
	defer cancel()
	step(c)
	<-c.Done()
}
func start(ctx context.Context) { go worker(ctx) }
`
	if diags := analyzeSource(t, src); len(diags) != 0 {
		t.Fatalf("diagnostics = %#v, want none", diags)
	}
}

func TestTrustedCall_StopWrapperBeforeLaterLoopStillWarns(t *testing.T) {
	// A configured stop wrapper that returns normally has the same
	// structural problem as a delegated context.
	before := `package p
func stopped() {}
func start() {
	go func() {
		stopped()
		for {}
	}()
}
`
	diags := analyzeSourceWithStopWrapper(t, before, "example.test/input.stopped")
	n := 0
	for _, d := range diags {
		if d.RuleID == "LL1002" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("stop wrapper before a later loop: LL1002 count = %d, want 1; diagnostics = %#v", n, diags)
	}

	inside := `package p
func stopped() {}
func start() {
	go func() {
		for { stopped() }
	}()
}
`
	for _, d := range analyzeSourceWithStopWrapper(t, inside, "example.test/input.stopped") {
		if d.RuleID == "LL1002" {
			t.Fatalf("a stop wrapper inside the loop should still resolve it, got %#v", d)
		}
	}
}
