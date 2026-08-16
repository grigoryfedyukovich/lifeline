package engine

import (
	"testing"

	"github.com/gfedyukovich/lifeline/internal/config"
	"github.com/gfedyukovich/lifeline/internal/model"
)

func TestRules(t *testing.T) {
	program := model.Program{
		PackagePath:   "example.test/p",
		FunctionCount: 1,
		Functions: []model.Function{{
			Name:       "Start",
			Cancels:    []model.CancelBinding{{Factory: "context.WithCancel", Discarded: true}},
			Goroutines: []model.Goroutine{{InfiniteLoop: true, AvailableContexts: []string{"ctx"}}},
			Groups:     []model.JoinGroup{{Kind: "waitgroup", Name: "wg", Starts: 1}},
		}},
	}
	diags := Analyze(program, config.Default())
	got := map[string]bool{}
	for _, d := range diags {
		got[d.RuleID] = true
	}
	for _, want := range []string{"LL1001", "LL1002", "LL1003"} {
		if !got[want] {
			t.Fatalf("missing %s in %#v", want, got)
		}
	}
}

func TestRecognizedProtocolsSuppressWarnings(t *testing.T) {
	program := model.Program{Functions: []model.Function{{
		Cancels:    []model.CancelBinding{{Called: true}},
		Goroutines: []model.Goroutine{{InfiniteLoop: true, ContextStop: true}},
		Groups:     []model.JoinGroup{{Kind: "errgroup", Starts: 1, Joined: true}},
	}}}
	if got := Analyze(program, config.Default()); len(got) != 0 {
		t.Fatalf("unexpected diagnostics: %#v", got)
	}
}

func TestPolicy(t *testing.T) {
	diags := []Diagnostic{{RuleID: "LL1002", Verdict: Warning}}
	if FailsPolicy(diags, nil) || !FailsPolicy(diags, []string{"LL1002"}) || !FailsPolicy(diags, []string{"all"}) {
		t.Fatal("unexpected policy result")
	}
}
