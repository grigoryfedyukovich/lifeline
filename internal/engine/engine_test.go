package engine

import (
	"strings"
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

func boolPtr(b bool) *bool { return &b }

// The following tests cover the WaitGroup/errgroup upgrade described in
// docs/cfg-migration-plan.md's Phase 3 completion section, exercised
// directly on hand-built model.JoinGroup fixtures the way TestRules and
// TestRecognizedProtocolsSuppressWarnings already do above, rather than
// through the frontend (internal/frontend/frontend_test.go covers the
// full pipeline for these; these tests pin down engine.go's own verdict
// logic on the model fields in isolation, including the fields' nil-means-
// "not established" convention).

func TestGroupJoinedButNotOnAllPathsFires(t *testing.T) {
	program := model.Program{Functions: []model.Function{{
		Name:   "Start",
		Groups: []model.JoinGroup{{Kind: "waitgroup", Name: "wg", Starts: 1, Joined: true, JoinedOnAllPaths: boolPtr(false)}},
	}}}
	diags := Analyze(program, config.Default())
	if len(diags) != 1 || diags[0].RuleID != "LL1003" {
		t.Fatalf("a group joined on some but not every path should fire LL1003, got %#v", diags)
	}
}

func TestGroupJoinedOnAllPathsButCountMismatchFires(t *testing.T) {
	program := model.Program{Functions: []model.Function{{
		Name:   "Start",
		Groups: []model.JoinGroup{{Kind: "waitgroup", Name: "wg", Starts: 2, Joined: true, JoinedOnAllPaths: boolPtr(true), CountMismatch: true}},
	}}}
	diags := Analyze(program, config.Default())
	if len(diags) != 1 || diags[0].RuleID != "LL1003" {
		t.Fatalf("a group joined on every path but with a proven count mismatch should still fire LL1003, got %#v", diags)
	}
}

func TestGroupNotOnAllPathsAndCountMismatchMentionsBoth(t *testing.T) {
	// !Joined and !joinedOnAllPaths can never both be true for the same
	// group (joinedOnAllPaths defaults to true whenever Joined is
	// false), but CountMismatch is computed entirely independently of
	// both -- literal Add/Done interval accounting, unrelated to
	// whether or which paths reach a Wait call -- so a group can
	// genuinely be joined on only some paths *and* have a real,
	// separately-verified Add/Done imbalance underneath that at the
	// same time. Before this was fixed, the switch that picks LL1003's
	// message/suggestion was exclusive: !joinedOnAllPaths matched first
	// and the independently-true CountMismatch was silently dropped
	// from the user-facing text (though Evidence, built up
	// unconditionally elsewhere, always carried both). This is still
	// exactly one diagnostic per group, as documented -- only the
	// message and suggestion text change to mention both.
	program := model.Program{Functions: []model.Function{{
		Name:   "Start",
		Groups: []model.JoinGroup{{Kind: "waitgroup", Name: "wg", Starts: 2, Joined: true, JoinedOnAllPaths: boolPtr(false), CountMismatch: true}},
	}}}
	diags := Analyze(program, config.Default())
	if len(diags) != 1 || diags[0].RuleID != "LL1003" {
		t.Fatalf("expected exactly one LL1003, got %#v", diags)
	}
	if !strings.Contains(diags[0].Message, "not every path") || !strings.Contains(diags[0].Message, "independently") || !strings.Contains(diags[0].Message, "outstanding worker") {
		t.Fatalf("message should mention both the path issue and the independently-true count mismatch, got %q", diags[0].Message)
	}
	if !strings.Contains(diags[0].Suggestion, "defer") || !strings.Contains(diags[0].Suggestion, "Add call is matched") {
		t.Fatalf("suggestion should mention fixing both the path issue and the count mismatch, got %q", diags[0].Suggestion)
	}
}

func TestGroupUnjoinedAndCountMismatchMentionsBoth(t *testing.T) {
	// The other combination the same fix covers: entirely unjoined
	// (never Waited at all, through any path) together with a
	// literal Add/Done imbalance -- e.g. `wg.Add(2); go worker()` with
	// no Wait() anywhere in the function. Before the fix, only the
	// "unjoined" message/suggestion was shown; the separately-true
	// count mismatch was silently dropped from the actionable text.
	program := model.Program{Functions: []model.Function{{
		Name:   "Start",
		Groups: []model.JoinGroup{{Kind: "waitgroup", Name: "wg", Starts: 1, CountMismatch: true}},
	}}}
	diags := Analyze(program, config.Default())
	if len(diags) != 1 || diags[0].RuleID != "LL1003" {
		t.Fatalf("expected exactly one LL1003, got %#v", diags)
	}
	if !strings.Contains(diags[0].Message, "no Wait or ownership transfer") || !strings.Contains(diags[0].Message, "independently") || !strings.Contains(diags[0].Message, "outstanding worker") {
		t.Fatalf("message should mention both being unjoined and the independently-true count mismatch, got %q", diags[0].Message)
	}
}

func TestErrgroupCountMismatchNeverAppliesEvenWhenUnjoined(t *testing.T) {
	// CountMismatch is never computed for an errgroup in practice
	// (computeGroupBalances skips any non-"waitgroup" kind entirely,
	// since errgroup.Group's Add/Done-equivalent accounting is internal
	// to the library), but this exercises the engine's own defensive
	// side of that boundary directly on the model: even if a
	// hand-built (or, hypothetically, a future) fact set had
	// CountMismatch set on an errgroup, the new "mention both" clause
	// must never fire for one, since its wording ("Add call... Done")
	// is WaitGroup-specific and would be nonsensical for errgroup's
	// Go()-only model.
	program := model.Program{Functions: []model.Function{{
		Name:   "Start",
		Groups: []model.JoinGroup{{Kind: "errgroup", Name: "g", Starts: 1, CountMismatch: true}},
	}}}
	diags := Analyze(program, config.Default())
	if len(diags) != 1 || diags[0].RuleID != "LL1004" {
		t.Fatalf("expected exactly one LL1004, got %#v", diags)
	}
	if strings.Contains(diags[0].Message, "independently") || strings.Contains(diags[0].Message, "Add") {
		t.Fatalf("errgroup message must never reference the WaitGroup-specific count-mismatch clause, got %q", diags[0].Message)
	}
}

func TestGroupJoinedOnAllPathsNoMismatchSuppressesLL1003(t *testing.T) {
	program := model.Program{Functions: []model.Function{{
		Name:   "Start",
		Groups: []model.JoinGroup{{Kind: "waitgroup", Name: "wg", Starts: 1, Joined: true, JoinedOnAllPaths: boolPtr(true)}},
	}}}
	if got := Analyze(program, config.Default()); len(got) != 0 {
		t.Fatalf("a group joined on every path with no count mismatch should not fire, got %#v", got)
	}
}

func TestGroupJoinedOnAllPathsNilTreatedAsFine(t *testing.T) {
	// JoinedOnAllPaths == nil means "not established" (e.g. Joined came
	// from a join_wrapper call, or there was no CFG to check), which must
	// be treated the same as Joined's own pre-Phase-3 meaning -- never as
	// a disproof.
	program := model.Program{Functions: []model.Function{{
		Name:   "Start",
		Groups: []model.JoinGroup{{Kind: "waitgroup", Name: "wg", Starts: 1, Joined: true}},
	}}}
	if got := Analyze(program, config.Default()); len(got) != 0 {
		t.Fatalf("nil JoinedOnAllPaths should be treated as no evidence of a problem, got %#v", got)
	}
}

func TestGroupStopAfterWaitFiresLL1005EvenWhenOtherwiseClean(t *testing.T) {
	program := model.Program{Functions: []model.Function{{
		Name:   "Start",
		Groups: []model.JoinGroup{{Kind: "waitgroup", Name: "wg", Starts: 1, Joined: true, JoinedOnAllPaths: boolPtr(true), StopAfterWait: true}},
	}}}
	diags := Analyze(program, config.Default())
	if len(diags) != 1 || diags[0].RuleID != "LL1005" {
		t.Fatalf("a proven wait-before-stop ordering should fire exactly LL1005 on an otherwise clean join, got %#v", diags)
	}
}

func TestGroupUnjoinedAndStopAfterWaitFiresBoth(t *testing.T) {
	program := model.Program{Functions: []model.Function{{
		Name:   "Start",
		Groups: []model.JoinGroup{{Kind: "errgroup", Name: "g", Starts: 1, StopAfterWait: true}},
	}}}
	diags := Analyze(program, config.Default())
	got := map[string]bool{}
	for _, d := range diags {
		got[d.RuleID] = true
	}
	if len(diags) != 2 || !got["LL1004"] || !got["LL1005"] {
		t.Fatalf("an unjoined errgroup with a proven bad stop order should fire both LL1004 and LL1005, got %#v", diags)
	}
}

func TestPolicy(t *testing.T) {
	diags := []Diagnostic{{RuleID: "LL1002", Verdict: Warning}}
	if FailsPolicy(diags, nil) || !FailsPolicy(diags, []string{"LL1002"}) || !FailsPolicy(diags, []string{"all"}) {
		t.Fatal("unexpected policy result")
	}
}
