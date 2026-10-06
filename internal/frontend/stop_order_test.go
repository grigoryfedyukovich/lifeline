package frontend

import (
	"strings"
	"testing"
)

// Regression tests for audit finding F7: LL1005 relates a group to the stop
// signals of the contexts its own workers use, and models deferred calls by
// when they actually run (at return, in reverse registration order).

const stopHeader = "package p\nimport (\"context\"; \"sync\")\nvar _ sync.WaitGroup\nfunc worker(ctx context.Context, wg *sync.WaitGroup) { defer wg.Done(); <-ctx.Done() }\n"

// one group, one context-using worker; body is the code after the setup.
func stopCase(body string) string {
	return stopHeader + `func f(parent context.Context, c bool) {
	ctx, cancel := context.WithCancel(parent)
	_, _ = ctx, cancel
	var wg sync.WaitGroup
` + body + "\n}\n"
}

const usesCtx = "wg.Add(1); go func() { defer wg.Done(); <-ctx.Done() }()"

func TestStopOrder_Matrix(t *testing.T) {
	cases := []struct{ name, body, want string }{
		// The audit's acceptance, one by one.
		{"explicit stop before wait stays clean", usesCtx + "; cancel(); wg.Wait()", ""},
		{"genuine wait before stop stays flagged", usesCtx + "; wg.Wait(); cancel()", "LL1005"},
		{"defer-only cancel with a plain Wait is flagged", "defer cancel(); " + usesCtx + "; wg.Wait()", "LL1005"},

		// Deferred execution order: later registration runs first.
		{"defer Wait registered before defer cancel: cancel runs first, clean", "wg.Add(1); go func() { defer wg.Done(); <-ctx.Done() }(); defer wg.Wait(); defer cancel()", ""},
		{"defer cancel registered before defer Wait: Wait runs first, flagged", "defer cancel(); wg.Add(1); go func() { defer wg.Done(); <-ctx.Done() }(); defer wg.Wait()", "LL1005"},
		{"deferred Wait with a plain cancel in the body is clean", "defer wg.Wait(); " + usesCtx + "; cancel()", ""},
		{"deferred Wait and a deferred cancel registered after it, with the worker started later", "defer wg.Wait(); defer cancel(); " + usesCtx, ""},

		// Independence: no contamination between groups and signals.
		{"an unrelated goroutine's deferred cancel does not make a finite group look late",
			"defer cancel(); go func() { <-ctx.Done() }(); wg.Add(1); go func() { defer wg.Done() }(); wg.Wait()", ""},
		{"a cancel whose context no worker of the group uses is not its signal",
			"wg.Add(1); go func() { defer wg.Done() }(); wg.Wait(); cancel()", ""},

		// More than one signal: unsafe only if every one comes after.
		{"an explicit cancel before Wait outweighs a later one", usesCtx + "; cancel(); wg.Wait(); cancel()", ""},
		{"a cancel before Wait outweighs a deferred safety net", "defer cancel(); " + usesCtx + "; cancel(); wg.Wait()", ""},
		{"a conditional cancel before Wait is not proof of a late stop", usesCtx + "; if c { cancel() }; wg.Wait(); cancel()", ""},
		{"two cancels, both after Wait", usesCtx + "; wg.Wait(); cancel(); cancel()", "LL1005"},
	}
	for _, c := range cases {
		if got := rulesOnly(findings(t, stopCase(c.body))); got != c.want {
			t.Errorf("%s: rules = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestStopOrder_TwoIndependentGroupsAndSignals(t *testing.T) {
	two := func(body string) string {
		return stopHeader + `func f(parent context.Context) {
	ctx1, cancel1 := context.WithCancel(parent)
	ctx2, cancel2 := context.WithCancel(parent)
	var wg1, wg2 sync.WaitGroup
	wg1.Add(1); go func() { defer wg1.Done(); <-ctx1.Done() }()
	wg2.Add(1); go func() { defer wg2.Done(); <-ctx2.Done() }()
` + body + "\n}\n"
	}
	cases := []struct {
		name, body string
		wantGroups []string // the groups that must be reported
	}{
		{"each group is stopped before its own Wait", "cancel1(); wg1.Wait(); cancel2(); wg2.Wait()", nil},
		{"only the group waited before its own signal is reported", "cancel1(); wg1.Wait(); wg2.Wait(); cancel2()", []string{"wg2"}},
		{"the other group, mirrored", "wg1.Wait(); cancel1(); cancel2(); wg2.Wait()", []string{"wg1"}},
		{"a signal for one group before the other's Wait does not rescue it", "cancel2(); wg1.Wait(); cancel1(); wg2.Wait()", []string{"wg1"}},
		{"both late", "wg1.Wait(); wg2.Wait(); cancel1(); cancel2()", []string{"wg1", "wg2"}},
	}
	for _, c := range cases {
		var got []string
		for _, d := range analyzeSource(t, two(c.body)) {
			if d.RuleID != "LL1005" {
				t.Errorf("%s: unexpected %s", c.name, d.RuleID)
				continue
			}
			for _, g := range []string{"wg1", "wg2"} {
				if strings.Contains(d.Message, `"`+g+`"`) {
					got = append(got, g)
				}
			}
		}
		if strings.Join(got, ",") != strings.Join(c.wantGroups, ",") {
			t.Errorf("%s: reported groups %v, want %v", c.name, got, c.wantGroups)
		}
	}
}

func TestStopOrder_ContextsDerivedFromTheWorkersContextAreRelated(t *testing.T) {
	src := func(body string) string {
		return stopHeader + `func f(parent context.Context) {
	ctx, cancel := context.WithCancel(parent)
	child, cancelChild := context.WithTimeout(ctx, 0)
	var wg sync.WaitGroup
	wg.Add(1); go func() { defer wg.Done(); <-child.Done() }()
` + body + "\n}\n"
	}
	cases := []struct{ name, body, want string }{
		{"both cancels after Wait", "defer cancelChild(); wg.Wait(); cancel()", "LL1005"},
		{"the parent's cancel before Wait stops the child's users too", "cancel(); wg.Wait(); cancelChild()", ""},
		{"the child's own cancel before Wait", "cancelChild(); wg.Wait(); cancel()", ""},
	}
	for _, c := range cases {
		if got := rulesOnly(findings(t, src(c.body))); got != c.want {
			t.Errorf("%s: rules = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestStopOrder_WorkerShapes(t *testing.T) {
	cases := []struct{ name, src, want string }{
		{"named worker given the context: late cancel", stopHeader + `func f(parent context.Context) {
	ctx, cancel := context.WithCancel(parent)
	var wg sync.WaitGroup
	wg.Add(1)
	go worker(ctx, &wg)
	wg.Wait()
	cancel()
}`, "LL1005"},
		{"named worker given the context: early cancel", stopHeader + `func f(parent context.Context) {
	ctx, cancel := context.WithCancel(parent)
	var wg sync.WaitGroup
	wg.Add(1)
	go worker(ctx, &wg)
	cancel()
	wg.Wait()
}`, ""},
		{"WaitGroup.Go worker using the context: late cancel", stopHeader + `func f(parent context.Context) {
	ctx, cancel := context.WithCancel(parent)
	var wg sync.WaitGroup
	wg.Go(func() { <-ctx.Done() })
	wg.Wait()
	cancel()
}`, "LL1005"},
		{"WaitGroup.Go worker using the context: early cancel", stopHeader + `func f(parent context.Context) {
	ctx, cancel := context.WithCancel(parent)
	var wg sync.WaitGroup
	wg.Go(func() { <-ctx.Done() })
	cancel()
	wg.Wait()
}`, ""},
		{"a worker that never sees the context", stopHeader + `func f(parent context.Context) {
	_, cancel := context.WithCancel(parent)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done() }()
	wg.Wait()
	cancel()
}`, ""},
	}
	for _, c := range cases {
		if got := rulesOnly(findings(t, c.src)); got != c.want {
			t.Errorf("%s: rules = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestStopOrder_CorrelationIsReportedForWhatItIs(t *testing.T) {
	var contextGroup, wrapperGroup string
	for _, fn := range buildProgramFor(t, stopCase("wg.Add(1); go func() { defer wg.Done(); <-ctx.Done() }(); wg.Wait(); cancel()"), nil).Functions {
		for _, g := range fn.Groups {
			if g.StopAfterWait {
				contextGroup = g.StopCorrelation
			}
		}
	}
	if contextGroup != "worker-context" {
		t.Errorf("a context-correlated finding records StopCorrelation = %q, want worker-context", contextGroup)
	}
	_ = wrapperGroup

	src := `package p
import "sync"
func Shutdown() {}
func f() {
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done() }()
	wg.Wait()
	Shutdown()
}
`
	diags := analyzeSourceWithStopWrapper(t, src, "example.test/input.Shutdown")
	if len(diags) != 1 || diags[0].RuleID != "LL1005" {
		t.Fatalf("a stop wrapper called only after Wait must still be reported, got %#v", diags)
	}
	if !strings.Contains(diags[0].Message, "assumed") || strings.Contains(diags[0].Message, "its workers' own stop signal") {
		t.Errorf("a stop-wrapper finding must say the correlation is assumed, not assert it: %q", diags[0].Message)
	}
	found := false
	for _, ev := range diags[0].Evidence {
		if ev.Kind == "stop-after-wait" && strings.Contains(ev.Message, "not verified") {
			found = true
		}
	}
	if !found {
		t.Errorf("the evidence must carry the assumption: %#v", diags[0].Evidence)
	}
}

func TestStopOrder_DeferredStopWrapperFollowsDeferredOrder(t *testing.T) {
	mk := func(body string) string {
		return `package p
import "sync"
func Shutdown() {}
func f() {
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done() }()
` + body + "\n}\n"
	}
	cases := []struct {
		name, body string
		want       int
	}{
		{"deferred Wait registered first, deferred stop wrapper after: stop runs first", "defer wg.Wait(); defer Shutdown()", 0},
		{"deferred stop wrapper registered first, deferred Wait after: Wait runs first", "defer Shutdown(); defer wg.Wait()", 1},
		{"deferred stop wrapper with a plain Wait", "defer Shutdown(); wg.Wait()", 1},
		{"plain stop wrapper before Wait", "Shutdown(); wg.Wait()", 0},
	}
	for _, c := range cases {
		if got := len(analyzeSourceWithStopWrapper(t, mk(c.body), "example.test/input.Shutdown")); got != c.want {
			t.Errorf("%s: %d findings, want %d", c.name, got, c.want)
		}
	}
}
