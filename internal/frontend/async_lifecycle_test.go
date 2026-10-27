package frontend

import (
	"github.com/gfedyukovich/lifeline/internal/config"
	"github.com/gfedyukovich/lifeline/internal/engine"
	"testing"
)

func TestAsyncJoinDoesNotJoinOwner(t *testing.T) {
	for _, tc := range []struct{ name, helper, join, want string }{
		{"direct", "", "go wg.Wait()", "LL1003"},
		{"helper", "func wait(g *sync.WaitGroup) { g.Wait() }", "go wait(&wg)", "LL1003"},
		{"helper starts waiter", "func wait(g *sync.WaitGroup) { go g.Wait() }", "wait(&wg)", "LL1003"},
		{"helper chain starts waiter", "func wait(g *sync.WaitGroup) { h(g) }; func h(g *sync.WaitGroup) { go g.Wait() }", "wait(&wg)", "LL1003"},
		{"invoked closure starts waiter", "", "func(){ go wg.Wait() }()", "LL1003"},
		{"async closure", "", "go func(){ wg.Wait() }()", "LL1003"},
		{"field", "", "h := struct{ g *sync.WaitGroup }{g: &wg}; go h.g.Wait()", "LL1003"},
		{"method", "type H struct{ g *sync.WaitGroup }; func(h H) Wait(){ h.g.Wait() }", "h := H{g: &wg}; go h.Wait()", "LL1003"},
		{"method starts waiter", "type H struct{ g *sync.WaitGroup }; func(h H) Wait(){ go h.g.Wait() }", "h := H{g: &wg}; h.Wait()", "LL1003"},
		{"method chain starts waiter", "type H struct{ g *sync.WaitGroup }; func(h H) Wait(){ go h.join() }; func(h H) join(){ h.g.Wait() }", "h := H{g: &wg}; h.Wait()", "LL1003"},
		{"normal join", "", "wg.Wait()", ""},
		{"deferred join", "", "defer wg.Wait()", ""},
		{"async and normal join", "", "go wg.Wait(); wg.Wait()", ""},
		{"async and deferred join", "", "go wg.Wait(); defer wg.Wait()", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := wgHeader + tc.helper + "\nfunc f(){ var wg sync.WaitGroup; wg.Add(1); go func(){ defer wg.Done() }(); " + tc.join + " }"
			if got := rulesOf(t, src); got != tc.want {
				t.Fatalf("rules=%q want=%q", got, tc.want)
			}
		})
	}
}

func TestAsyncConfiguredJoinDoesNotJoinOwner(t *testing.T) {
	src := wgHeader + "func wait(g *sync.WaitGroup) {}\nfunc f(){var wg sync.WaitGroup; wg.Add(1); go func(){defer wg.Done()}(); go wait(&wg)}"
	prog := buildProgramFor(t, src, func(c *config.Config) { c.JoinWrappers = []string{"example.test/input.wait"} })
	diags := engine.Analyze(prog, config.Default())
	if len(diags) != 1 || diags[0].RuleID != "LL1003" {
		t.Fatalf("diagnostics=%#v", diags)
	}
}

func TestAsyncCancelIsTransfer(t *testing.T) {
	for _, body := range []string{"go cancel()", "go release(cancel)", "func(){go cancel()}()"} {
		t.Run(body, func(t *testing.T) {
			prog := buildProgramFor(t, ctxHeader+"func release(c context.CancelFunc){c()}\nfunc f(parent context.Context){_,cancel:=context.WithCancel(parent); "+body+"}", nil)
			for _, fn := range prog.Functions {
				for _, c := range fn.Cancels {
					if c.Called || !c.Escapes {
						t.Fatalf("async call credited as owner discharge: %+v", c)
					}
				}
			}
			_, st := engine.AnalyzeWithStatus(prog, config.Default())
			if st.Unsupported.HandedOff != 1 {
				t.Fatalf("handoff missing: %+v", st)
			}
		})
	}
}

func TestAsyncStopDoesNotProveWaitBeforeStop(t *testing.T) {
	for _, body := range []string{usesCtx + "; defer cancel(); go cancel(); wg.Wait()", `defer cancel(); stop:=make(chan struct{}); wg.Add(1); go func(){defer wg.Done(); <-stop}(); go close(stop); wg.Wait()`} {
		if got := rulesOf(t, stopCase(body)); got != "" {
			t.Fatalf("rules=%q for %s", got, body)
		}
	}
}
