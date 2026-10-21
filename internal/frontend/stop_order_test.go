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

func TestStopOrder_ContextStoredInLocalStructField(t *testing.T) {
	const hdr = `package p
import ("context"; "sync")
type holder struct { ctx context.Context; other context.Context }
func fieldWorker(h holder, wg *sync.WaitGroup) { defer wg.Done(); <-h.ctx.Done() }
func finiteWorker(h holder, wg *sync.WaitGroup) { defer wg.Done(); _ = h }
`
	cases := []struct{ name, body, want string }{
		{"literal worker: late cancel through field", `
ctx, cancel := context.WithCancel(parent)
h := holder{ctx: ctx}
var wg sync.WaitGroup
wg.Add(1); go func(){ defer wg.Done(); <-h.ctx.Done() }()
wg.Wait(); cancel()`, "LL1005"},
		{"literal worker: early cancel through field", `
ctx, cancel := context.WithCancel(parent)
h := holder{ctx: ctx}
var wg sync.WaitGroup
wg.Add(1); go func(){ defer wg.Done(); <-h.ctx.Done() }()
cancel(); wg.Wait()`, ""},
		{"direct field assignment is tracked", `
ctx, cancel := context.WithCancel(parent)
var h holder; h.ctx = ctx
var wg sync.WaitGroup
wg.Add(1); go func(){ defer wg.Done(); <-h.ctx.Done() }()
wg.Wait(); cancel()`, "LL1005"},
		{"unkeyed struct literal is tracked", `
ctx, cancel := context.WithCancel(parent)
h := holder{ctx, nil}
var wg sync.WaitGroup
wg.Add(1); go func(){ defer wg.Done(); <-h.ctx.Done() }()
wg.Wait(); cancel()`, "LL1005"},
		{"local whole-value copy preserves context fields", `
ctx, cancel := context.WithCancel(parent)
h := holder{ctx: ctx}; h2 := h
var wg sync.WaitGroup
wg.Add(1); go func(){ defer wg.Done(); <-h2.ctx.Done() }()
wg.Wait(); cancel()`, "LL1005"},
		{"named worker parameter: late cancel", `
ctx, cancel := context.WithCancel(parent)
h := holder{ctx: ctx}
var wg sync.WaitGroup
wg.Add(1); go fieldWorker(h, &wg)
wg.Wait(); cancel()`, "LL1005"},
		{"named worker parameter: early cancel", `
ctx, cancel := context.WithCancel(parent)
h := holder{ctx: ctx}
var wg sync.WaitGroup
wg.Add(1); go fieldWorker(h, &wg)
cancel(); wg.Wait()`, ""},
		{"named worker that does not read the context field stays uncorrelated", `
ctx, cancel := context.WithCancel(parent)
h := holder{ctx: ctx}
var wg sync.WaitGroup
wg.Add(1); go finiteWorker(h, &wg)
wg.Wait(); cancel()`, ""},
	}
	for _, tc := range cases {
		src := hdr + "func f(parent context.Context) {" + tc.body + "\n}\n"
		if got := rulesOnly(findings(t, src)); got != tc.want {
			t.Errorf("%s: rules = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestStopOrder_ContextStructFieldsStayIndependent(t *testing.T) {
	src := `package p
import ("context"; "sync")
type holder struct { a, b context.Context }
func f(parent context.Context) {
	ctx1, cancel1 := context.WithCancel(parent)
	ctx2, cancel2 := context.WithCancel(parent)
	h := holder{a: ctx1, b: ctx2}
	var wg1, wg2 sync.WaitGroup
	wg1.Add(1); go func(){ defer wg1.Done(); <-h.a.Done() }()
	wg2.Add(1); go func(){ defer wg2.Done(); <-h.b.Done() }()
	cancel1(); wg1.Wait()
	wg2.Wait(); cancel2()
}`
	var got []string
	for _, d := range analyzeSource(t, src) {
		if d.RuleID != "LL1005" {
			continue
		}
		for _, g := range []string{"wg1", "wg2"} {
			if strings.Contains(d.Message, `"`+g+`"`) {
				got = append(got, g)
			}
		}
	}
	if strings.Join(got, ",") != "wg2" {
		t.Fatalf("reported groups %v, want [wg2]", got)
	}
}

func TestStopOrder_NestedContextFieldPathsCorrelate(t *testing.T) {
	const hdr = `package p
import ("context"; "sync")
type nestedHolder struct { ctx context.Context }
type service struct { worker nestedHolder }
func nestedContextWorker(s service, wg *sync.WaitGroup) { defer wg.Done(); <-s.worker.ctx.Done() }
func holderContextWorker(h nestedHolder, wg *sync.WaitGroup) { defer wg.Done(); <-h.ctx.Done() }
`
	cases := []struct{ name, body, want string }{
		{"nested literal worker: late cancel", `
ctx, cancel := context.WithCancel(parent)
s := service{worker: nestedHolder{ctx: ctx}}
var wg sync.WaitGroup
wg.Add(1); go func(){ defer wg.Done(); <-s.worker.ctx.Done() }()
wg.Wait(); cancel()`, "LL1005"},
		{"nested direct field assignment", `
ctx, cancel := context.WithCancel(parent)
var s service; s.worker.ctx = ctx
var wg sync.WaitGroup
wg.Add(1); go func(){ defer wg.Done(); <-s.worker.ctx.Done() }()
wg.Wait(); cancel()`, "LL1005"},
		{"nested unkeyed composite", `
ctx, cancel := context.WithCancel(parent)
s := service{nestedHolder{ctx}}
var wg sync.WaitGroup
wg.Add(1); go func(){ defer wg.Done(); <-s.worker.ctx.Done() }()
wg.Wait(); cancel()`, "LL1005"},
		{"nested whole-value copy", `
ctx, cancel := context.WithCancel(parent)
s1 := service{worker: nestedHolder{ctx: ctx}}; s2 := s1
var wg sync.WaitGroup
wg.Add(1); go func(){ defer wg.Done(); <-s2.worker.ctx.Done() }()
wg.Wait(); cancel()`, "LL1005"},
		{"named worker reads nested path", `
ctx, cancel := context.WithCancel(parent)
s := service{worker: nestedHolder{ctx: ctx}}
var wg sync.WaitGroup
wg.Add(1); go nestedContextWorker(s, &wg)
wg.Wait(); cancel()`, "LL1005"},
		{"nested argument prefix composes with worker field", `
ctx, cancel := context.WithCancel(parent)
s := service{worker: nestedHolder{ctx: ctx}}
var wg sync.WaitGroup
wg.Add(1); go holderContextWorker(s.worker, &wg)
wg.Wait(); cancel()`, "LL1005"},
		{"early cancel stays safe", `
ctx, cancel := context.WithCancel(parent)
s := service{worker: nestedHolder{ctx: ctx}}
var wg sync.WaitGroup
wg.Add(1); go nestedContextWorker(s, &wg)
cancel(); wg.Wait()`, ""},
	}
	for _, tc := range cases {
		src := hdr + "func f(parent context.Context) {" + tc.body + "\n}\n"
		if got := rulesOnly(findings(t, src)); got != tc.want {
			t.Errorf("%s: rules = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestStopOrder_NestedContextFieldPathsStayIndependent(t *testing.T) {
	src := `package p
import ("context"; "sync")
type nestedHolder struct { ctx context.Context }
type service struct { a, b nestedHolder }
func f(parent context.Context) {
	ctx1, cancel1 := context.WithCancel(parent)
	ctx2, cancel2 := context.WithCancel(parent)
	s := service{a: nestedHolder{ctx: ctx1}, b: nestedHolder{ctx: ctx2}}
	var wg1, wg2 sync.WaitGroup
	wg1.Add(1); go func(){ defer wg1.Done(); <-s.a.ctx.Done() }()
	wg2.Add(1); go func(){ defer wg2.Done(); <-s.b.ctx.Done() }()
	cancel1(); wg1.Wait()
	wg2.Wait(); cancel2()
}`
	var got []string
	for _, d := range analyzeSource(t, src) {
		if d.RuleID != "LL1005" {
			continue
		}
		for _, g := range []string{"wg1", "wg2"} {
			if strings.Contains(d.Message, `"`+g+`"`) {
				got = append(got, g)
			}
		}
	}
	if strings.Join(got, ",") != "wg2" {
		t.Fatalf("reported groups %v, want [wg2]", got)
	}
}

func TestStopOrder_ChannelCloseCorrelatesWithWorkerTermination(t *testing.T) {
	const hdr = `package p
import "sync"
func chanWorker(stop <-chan struct{}, wg *sync.WaitGroup) {
	defer wg.Done()
	select { case <-stop: return }
}
func rangeWorker(stop <-chan struct{}, wg *sync.WaitGroup) {
	defer wg.Done()
	for range stop {}
}
`
	cases := []struct{ name, body, want string }{
		{"literal select worker: late close", `
stop := make(chan struct{})
var wg sync.WaitGroup
wg.Add(1); go func(){ defer wg.Done(); select { case <-stop: return } }()
wg.Wait(); close(stop)`, "LL1005"},
		{"literal select worker: early close", `
stop := make(chan struct{})
var wg sync.WaitGroup
wg.Add(1); go func(){ defer wg.Done(); select { case <-stop: return } }()
close(stop); wg.Wait()`, ""},
		{"named select worker: late close", `
stop := make(chan struct{})
var wg sync.WaitGroup
wg.Add(1); go chanWorker(stop, &wg)
wg.Wait(); close(stop)`, "LL1005"},
		{"named range worker: late close", `
stop := make(chan struct{})
var wg sync.WaitGroup
wg.Add(1); go rangeWorker(stop, &wg)
wg.Wait(); close(stop)`, "LL1005"},
		{"deferred close after plain Wait", `
stop := make(chan struct{})
var wg sync.WaitGroup
wg.Add(1); go chanWorker(stop, &wg)
defer close(stop); wg.Wait()`, "LL1005"},
		{"deferred close registered after deferred Wait runs first", `
stop := make(chan struct{})
var wg sync.WaitGroup
wg.Add(1); go chanWorker(stop, &wg)
defer wg.Wait(); defer close(stop)`, ""},
		{"deferred close registered before deferred Wait runs after", `
stop := make(chan struct{})
var wg sync.WaitGroup
wg.Add(1); go chanWorker(stop, &wg)
defer close(stop); defer wg.Wait()`, "LL1005"},
	}
	for _, tc := range cases {
		src := hdr + "func f() {" + tc.body + "\n}\n"
		if got := rulesOnly(findings(t, src)); got != tc.want {
			t.Errorf("%s: rules = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestStopOrder_ChannelCloseRequiresTerminationShape(t *testing.T) {
	src := `package p
import "sync"
func f() {
	data := make(chan int, 1)
	var wg sync.WaitGroup
	wg.Add(1)
	go func(){ defer wg.Done(); _ = <-data }()
	data <- 1
	wg.Wait()
	close(data)
}`
	if got := rulesOnly(findings(t, src)); got != "" {
		t.Fatalf("ordinary data receive must not make close(data) a stop signal, got %q", got)
	}
}

func TestStopOrder_ChannelSignalsStayIndependent(t *testing.T) {
	src := `package p
import "sync"
func f() {
	stop1 := make(chan struct{})
	stop2 := make(chan struct{})
	var wg1, wg2 sync.WaitGroup
	wg1.Add(1); go func(){ defer wg1.Done(); select { case <-stop1: return } }()
	wg2.Add(1); go func(){ defer wg2.Done(); select { case <-stop2: return } }()
	close(stop1); wg1.Wait()
	wg2.Wait(); close(stop2)
}`
	var got []string
	for _, d := range analyzeSource(t, src) {
		if d.RuleID != "LL1005" {
			continue
		}
		for _, g := range []string{"wg1", "wg2"} {
			if strings.Contains(d.Message, `"`+g+`"`) {
				got = append(got, g)
			}
		}
	}
	if strings.Join(got, ",") != "wg2" {
		t.Fatalf("reported groups %v, want [wg2]", got)
	}
}

func TestStopOrder_ChannelCorrelationIsReported(t *testing.T) {
	src := `package p
import "sync"
func f() {
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1); go func(){ defer wg.Done(); select { case <-stop: return } }()
	wg.Wait(); close(stop)
}`
	program := buildProgramFor(t, src, nil)
	for _, fn := range program.Functions {
		for _, g := range fn.Groups {
			if g.StopAfterWait {
				if g.StopCorrelation != "worker-channel" {
					t.Fatalf("channel-correlated finding records StopCorrelation = %q, want worker-channel", g.StopCorrelation)
				}
				return
			}
		}
	}
	t.Fatal("expected a stop-after-wait group")
}

func TestStopOrder_ChannelAliasesAndFieldsCorrelate(t *testing.T) {
	const hdr = `package p
import "sync"
type holder struct { stop chan struct{} }
func holderWorker(h holder, wg *sync.WaitGroup) {
	defer wg.Done()
	select { case <-h.stop: return }
}
`
	cases := []struct{ name, body, want string }{
		{"worker alias, close original", `
stop := make(chan struct{})
alias := stop
var wg sync.WaitGroup
wg.Add(1); go func(){ defer wg.Done(); select { case <-alias: return } }()
wg.Wait(); close(stop)`, "LL1005"},
		{"worker original, close alias", `
stop := make(chan struct{})
alias := stop
var wg sync.WaitGroup
wg.Add(1); go func(){ defer wg.Done(); select { case <-stop: return } }()
wg.Wait(); close(alias)`, "LL1005"},
		{"literal reads field, close original", `
stop := make(chan struct{})
h := holder{stop: stop}
var wg sync.WaitGroup
wg.Add(1); go func(){ defer wg.Done(); select { case <-h.stop: return } }()
wg.Wait(); close(stop)`, "LL1005"},
		{"literal reads field, close field", `
h := holder{stop: make(chan struct{})}
var wg sync.WaitGroup
wg.Add(1); go func(){ defer wg.Done(); select { case <-h.stop: return } }()
wg.Wait(); close(h.stop)`, "LL1005"},
		{"named worker reads field", `
stop := make(chan struct{})
h := holder{stop: stop}
var wg sync.WaitGroup
wg.Add(1); go holderWorker(h, &wg)
wg.Wait(); close(stop)`, "LL1005"},
		{"struct copy preserves field source", `
stop := make(chan struct{})
h1 := holder{stop: stop}
h2 := h1
var wg sync.WaitGroup
wg.Add(1); go func(){ defer wg.Done(); select { case <-h2.stop: return } }()
wg.Wait(); close(stop)`, "LL1005"},
		{"early close through alias is safe", `
stop := make(chan struct{})
alias := stop
var wg sync.WaitGroup
wg.Add(1); go func(){ defer wg.Done(); select { case <-stop: return } }()
close(alias); wg.Wait()`, ""},
	}
	for _, tc := range cases {
		src := hdr + "func f() {" + tc.body + "\n}\n"
		if got := rulesOnly(findings(t, src)); got != tc.want {
			t.Errorf("%s: rules = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestStopOrder_ChannelFieldSignalsStayIndependent(t *testing.T) {
	src := `package p
import "sync"
type holder struct { stop chan struct{} }
func f() {
	stop1 := make(chan struct{})
	stop2 := make(chan struct{})
	h1 := holder{stop: stop1}
	h2 := holder{stop: stop2}
	var wg1, wg2 sync.WaitGroup
	wg1.Add(1); go func(){ defer wg1.Done(); select { case <-h1.stop: return } }()
	wg2.Add(1); go func(){ defer wg2.Done(); select { case <-h2.stop: return } }()
	close(h1.stop); wg1.Wait()
	wg2.Wait(); close(h2.stop)
}`
	var got []string
	for _, d := range analyzeSource(t, src) {
		if d.RuleID != "LL1005" {
			continue
		}
		for _, g := range []string{"wg1", "wg2"} {
			if strings.Contains(d.Message, `"`+g+`"`) {
				got = append(got, g)
			}
		}
	}
	if strings.Join(got, ",") != "wg2" {
		t.Fatalf("reported groups %v, want [wg2]", got)
	}
}

func TestStopOrder_NestedChannelFieldPathsCorrelate(t *testing.T) {
	const hdr = `package p
import "sync"
type nestedStopHolder struct { stop chan struct{} }
type stopService struct { worker nestedStopHolder }
func nestedChannelWorker(s stopService, wg *sync.WaitGroup) {
	defer wg.Done()
	select { case <-s.worker.stop: return }
}
func holderChannelWorker(h nestedStopHolder, wg *sync.WaitGroup) {
	defer wg.Done()
	select { case <-h.stop: return }
}
`
	cases := []struct{ name, body, want string }{
		{"nested literal worker, close original", `
stop := make(chan struct{})
s := stopService{worker: nestedStopHolder{stop: stop}}
var wg sync.WaitGroup
wg.Add(1); go func(){ defer wg.Done(); select { case <-s.worker.stop: return } }()
wg.Wait(); close(stop)`, "LL1005"},
		{"nested literal worker, close nested field", `
s := stopService{worker: nestedStopHolder{stop: make(chan struct{})}}
var wg sync.WaitGroup
wg.Add(1); go func(){ defer wg.Done(); select { case <-s.worker.stop: return } }()
wg.Wait(); close(s.worker.stop)`, "LL1005"},
		{"nested direct field assignment", `
stop := make(chan struct{})
var s stopService; s.worker.stop = stop
var wg sync.WaitGroup
wg.Add(1); go func(){ defer wg.Done(); select { case <-s.worker.stop: return } }()
wg.Wait(); close(stop)`, "LL1005"},
		{"nested unkeyed composite", `
stop := make(chan struct{})
s := stopService{nestedStopHolder{stop}}
var wg sync.WaitGroup
wg.Add(1); go func(){ defer wg.Done(); select { case <-s.worker.stop: return } }()
wg.Wait(); close(stop)`, "LL1005"},
		{"nested whole-value copy", `
stop := make(chan struct{})
s1 := stopService{worker: nestedStopHolder{stop: stop}}; s2 := s1
var wg sync.WaitGroup
wg.Add(1); go func(){ defer wg.Done(); select { case <-s2.worker.stop: return } }()
wg.Wait(); close(stop)`, "LL1005"},
		{"named worker reads nested path", `
stop := make(chan struct{})
s := stopService{worker: nestedStopHolder{stop: stop}}
var wg sync.WaitGroup
wg.Add(1); go nestedChannelWorker(s, &wg)
wg.Wait(); close(stop)`, "LL1005"},
		{"nested argument prefix composes with worker field", `
stop := make(chan struct{})
s := stopService{worker: nestedStopHolder{stop: stop}}
var wg sync.WaitGroup
wg.Add(1); go holderChannelWorker(s.worker, &wg)
wg.Wait(); close(stop)`, "LL1005"},
		{"early close stays safe", `
stop := make(chan struct{})
s := stopService{worker: nestedStopHolder{stop: stop}}
var wg sync.WaitGroup
wg.Add(1); go nestedChannelWorker(s, &wg)
close(s.worker.stop); wg.Wait()`, ""},
	}
	for _, tc := range cases {
		src := hdr + "func f() {" + tc.body + "\n}\n"
		if got := rulesOnly(findings(t, src)); got != tc.want {
			t.Errorf("%s: rules = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestStopOrder_NestedChannelFieldPathsStayIndependent(t *testing.T) {
	src := `package p
import "sync"
type nestedStopHolder struct { stop chan struct{} }
type stopService struct { a, b nestedStopHolder }
func f() {
	stop1 := make(chan struct{})
	stop2 := make(chan struct{})
	s := stopService{a: nestedStopHolder{stop: stop1}, b: nestedStopHolder{stop: stop2}}
	var wg1, wg2 sync.WaitGroup
	wg1.Add(1); go func(){ defer wg1.Done(); select { case <-s.a.stop: return } }()
	wg2.Add(1); go func(){ defer wg2.Done(); select { case <-s.b.stop: return } }()
	close(s.a.stop); wg1.Wait()
	wg2.Wait(); close(s.b.stop)
}`
	var got []string
	for _, d := range analyzeSource(t, src) {
		if d.RuleID != "LL1005" {
			continue
		}
		for _, g := range []string{"wg1", "wg2"} {
			if strings.Contains(d.Message, `"`+g+`"`) {
				got = append(got, g)
			}
		}
	}
	if strings.Join(got, ",") != "wg2" {
		t.Fatalf("reported groups %v, want [wg2]", got)
	}
}

func TestStopOrder_HelperMediatedChannelShutdown(t *testing.T) {
	const hdr = `package p
import "sync"
func waitStop(ch <-chan struct{}) { for range ch {} }
func waitStop2(ch <-chan struct{}) { waitStop(ch) }
func workerViaHelper(ch <-chan struct{}, wg *sync.WaitGroup) { defer wg.Done(); waitStop2(ch) }
func closeStop(ch chan struct{}) { close(ch) }
func closeStop2(ch chan struct{}) { closeStop(ch) }
type helperHolder struct { stop chan struct{} }
func workerFieldViaHelper(h helperHolder, wg *sync.WaitGroup) { defer wg.Done(); waitStop(h.stop) }
func maybeClose(ch chan struct{}, ok bool) { if ok { close(ch) } }
`
	cases := []struct{ name, body, want string }{
		{"literal delegates receive to helper", `
stop := make(chan struct{})
var wg sync.WaitGroup
wg.Add(1); go func(){ defer wg.Done(); waitStop(stop) }()
wg.Wait(); close(stop)`, "LL1005"},
		{"named worker delegates receive through helper chain", `
stop := make(chan struct{})
var wg sync.WaitGroup
wg.Add(1); go workerViaHelper(stop, &wg)
wg.Wait(); close(stop)`, "LL1005"},
		{"named worker passes channel field to helper", `
stop := make(chan struct{})
h := helperHolder{stop: stop}
var wg sync.WaitGroup
wg.Add(1); go workerFieldViaHelper(h, &wg)
wg.Wait(); close(stop)`, "LL1005"},
		{"owner delegates close to helper", `
stop := make(chan struct{})
var wg sync.WaitGroup
wg.Add(1); go func(){ defer wg.Done(); select { case <-stop: return } }()
wg.Wait(); closeStop(stop)`, "LL1005"},
		{"owner delegates close through helper chain", `
stop := make(chan struct{})
var wg sync.WaitGroup
wg.Add(1); go func(){ defer wg.Done(); select { case <-stop: return } }()
wg.Wait(); closeStop2(stop)`, "LL1005"},
		{"must-close helper before wait is safe", `
stop := make(chan struct{})
var wg sync.WaitGroup
wg.Add(1); go func(){ defer wg.Done(); select { case <-stop: return } }()
closeStop(stop); wg.Wait()`, ""},
		{"conditional close helper is not promoted to a stop signal", `
stop := make(chan struct{})
var wg sync.WaitGroup
wg.Add(1); go func(){ defer wg.Done(); select { case <-stop: return } }()
wg.Wait(); maybeClose(stop, false)`, ""},
	}
	for _, tc := range cases {
		src := hdr + "func f() {" + tc.body + "\n}\n"
		if got := rulesOnly(findings(t, src)); got != tc.want {
			t.Errorf("%s: rules = %q, want %q", tc.name, got, tc.want)
		}
	}
}
