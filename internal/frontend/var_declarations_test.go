package frontend

import (
	"github.com/gfedyukovich/lifeline/internal/config"
	"github.com/gfedyukovich/lifeline/internal/engine"
	"testing"
)

func TestVarDeclarationCancellation(t *testing.T) {
	for _, tc := range []struct{ name, body, want string }{
		{"named lost", "var _,cancel=context.WithCancel(parent);_=cancel", "LL1001"}, {"discarded", "var child,_=context.WithCancel(parent);_=child", "LL1001"},
		{"deferred", "var _,cancel=context.WithCancel(parent);defer cancel()", ""}, {"called", "var _,cancel=context.WithCancel(parent);cancel()", ""},
		{"conditional", "var _,cancel=context.WithCancel(parent);if enabled{cancel()}", "LL1001"}, {"conditional acquisition", "if enabled{var _,cancel=context.WithCancel(parent);defer cancel()}", ""},
		{"grouped", "var(child,cancel=context.WithCancel(parent);ignored=release(cancel));_,_=child,ignored", ""}, {"grouped lost", "var(child,cancel=context.WithCancel(parent);ignored=0);_,_,_=child,cancel,ignored", "LL1001"},
		{"dead", "if false{var _,cancel=context.WithCancel(parent);_=cancel}", ""}, {"closure", "func(){var _,cancel=context.WithCancel(parent);_=cancel}()", "LL1001"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := ctxHeader + "func release(c context.CancelFunc)int{c();return 0}\nfunc f(parent context.Context,enabled bool){" + tc.body + "}"
			if got := rulesOf(t, src); got != tc.want {
				t.Fatalf("rules=%q want=%q", got, tc.want)
			}
		})
	}
}
func TestVarDeclarationDoesNotOfferAssignmentFix(t *testing.T) {
	d := analyzeSource(t, ctxHeader+"func f(parent context.Context){var child,_=context.WithCancel(parent);_=child}")
	if len(d) != 1 || d[0].RuleID != "LL1001" || d[0].SuggestedFix != nil {
		t.Fatalf("unsafe fix: %#v", d)
	}
}
func TestVarDeclarationErrorGuard(t *testing.T) {
	for _, early := range []bool{false, true} {
		body := ""
		if early {
			body = "if enabled{return nil};"
		}
		src := ctxHeader + `func factory(parent context.Context)(context.Context,context.CancelFunc,error){child,cancel:=context.WithCancel(parent);return child,cancel,nil}
func f(parent context.Context,enabled bool)error{var child,cancel,err=factory(parent);_=child;if err!=nil{return err};` + body + `defer cancel();return nil}`
		prog := buildProgramFor(t, src, func(c *config.Config) { c.ContextWrappers = []string{"example.test/input.factory"} })
		d := engine.Analyze(prog, config.Default())
		want := 0
		if early {
			want = 1
		}
		if len(d) != want || early && d[0].RuleID != "LL1001" {
			t.Fatalf("early=%v diagnostics=%#v", early, d)
		}
	}
}
func TestVarDeclarationStopBeforeWait(t *testing.T) {
	src := `package p
import("context";"sync")
func f(parent context.Context){var ctx,cancel=context.WithCancel(parent);defer cancel();var wg sync.WaitGroup;wg.Add(1);go func(){defer wg.Done();<-ctx.Done()}();wg.Wait()}`
	if got := rulesOf(t, src); got != "LL1005" {
		t.Fatalf("rules=%q", got)
	}
}
func TestVarDeclarationConstructorObligation(t *testing.T) {
	src := ctxHeader + `type H struct{cancel context.CancelFunc};func New(parent context.Context)H{var _,cancel=context.WithCancel(parent);return H{cancel:cancel}};func f(parent context.Context){h:=New(parent);_=h}`
	if got := rulesOf(t, src); got != "LL1001" {
		t.Fatalf("rules=%q", got)
	}
}
