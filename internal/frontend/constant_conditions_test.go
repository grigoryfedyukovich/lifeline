package frontend

import "testing"

func TestConstantConditionsInWorkerLoops(t *testing.T) {
	for _, tc := range []struct{ name, decl, body, want string }{
		{"true", "", "for true {}", "LL1002"}, {"named", "const run = 3>2", "for run {}", "LL1002"}, {"typed", "const run bool=true", "for run {}", "LL1002"},
		{"false return", "", "for {if false{return}}", "LL1002"}, {"false break", "", "for {if 1>2{break}}", "LL1002"}, {"true continue", "", "for {if true{continue};return}", "LL1002"}, {"true else", "", "for {if true{}else{return}}", "LL1002"},
		{"return", "", "for true{return}", ""}, {"break", "", "for true{break}", ""}, {"false loop", "", "for false{}", ""}, {"false branch", "", "if false{for{}}", ""},
		{"dynamic loop", "", "for enabled{}", ""}, {"dynamic exit", "", "for{if enabled{return}}", ""}, {"shadowed true", "", "true:=enabled;for true{}", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := rulesOf(t, "package p\n"+tc.decl+"\nfunc f(enabled bool){go func(){"+tc.body+"}()}"); got != tc.want {
				t.Fatalf("rules=%q want=%q", got, tc.want)
			}
		})
	}
}
func TestConstantConditionsDoNotInventCleanup(t *testing.T) {
	for _, tc := range []struct{ name, body, want string }{
		{"false cancel", "if false{cancel()}", "LL1001"}, {"false defer", "if false{defer cancel()}", "LL1001"}, {"true cancel", "if true{cancel()}", ""}, {"true defer", "if 1<2{defer cancel()}", ""},
		{"false loop", "for false{cancel()}", "LL1001"}, {"false transfer", "if false{sink=cancel}", "LL1001"}, {"false async", "if false{go cancel()}", "LL1001"}, {"dynamic", "if enabled{cancel()}", "LL1001"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := ctxHeader + "var sink context.CancelFunc\nfunc f(parent context.Context,enabled bool){_,cancel:=context.WithCancel(parent);" + tc.body + "}"
			if got := rulesOf(t, src); got != tc.want {
				t.Fatalf("rules=%q want=%q", got, tc.want)
			}
		})
	}
}
func TestConstantConditionsInOwnerJoins(t *testing.T) {
	for _, tc := range []struct{ body, want string }{{"if false{wg.Wait()}", "LL1003"}, {"if true{wg.Wait()}", ""}, {"if false{}else{wg.Wait()}", ""}, {"for false{wg.Wait()}", "LL1003"}, {"if enabled{wg.Wait()}", "LL1003"}} {
		t.Run(tc.body, func(t *testing.T) {
			src := wgHeader + "func f(enabled bool){var wg sync.WaitGroup;wg.Add(1);go func(){defer wg.Done()}();" + tc.body + "}"
			if got := rulesOf(t, src); got != tc.want {
				t.Fatalf("rules=%q want=%q", got, tc.want)
			}
		})
	}
}
func TestConstantDeadCodeCreatesNoObligations(t *testing.T) {
	for _, body := range []string{`if false{_,cancel:=context.WithCancel(parent);_=cancel}`, `for false{_,cancel:=context.WithCancel(parent);_=cancel}`, `if false{go func(){_,cancel:=context.WithCancel(parent);_=cancel}()}`, `if true{}else{go func(){_,cancel:=context.WithCancel(parent);_=cancel}()}`} {
		if got := rulesOf(t, ctxHeader+"func f(parent context.Context){"+body+"}"); got != "" {
			t.Fatalf("dead code: %s rules=%q", body, got)
		}
	}
}
