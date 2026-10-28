package cfg

import "testing"

func TestTypedConstantControlFlow(t *testing.T) {
	for _, tc := range []struct {
		body string
		exit bool
	}{{"for true{}", false}, {"const run = !false;for run{}", false}, {"for{if false{return}}", false}, {"for{if 2<1{break}}", false}, {"for{if true{continue};return}", false}, {"for false{}", true}, {"if false{for{}}", true}, {"for true{break}", true}, {"for enabled{}", true}, {"true:=enabled;for true{}", true}} {
		t.Run(tc.body, func(t *testing.T) {
			g := build(t, "package p\nfunc F(enabled bool){"+tc.body+"}", "F")
			if got := reachable(g, g.Entry)[g.Exit]; got != tc.exit {
				t.Fatalf("Exit reachable=%v want=%v", got, tc.exit)
			}
		})
	}
}
