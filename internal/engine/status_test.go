package engine

import (
	"reflect"
	"testing"

	"github.com/gfedyukovich/lifeline/internal/config"
	"github.com/gfedyukovich/lifeline/internal/model"
)

// Regression tests for audit finding F8: the run status is computed from the
// program, never from the filtered diagnostics, so hiding diagnostics cannot
// change what the status says about the run.

func truncatedProgram() model.Program {
	return model.Program{
		PackagePath:   "example.test/p",
		FunctionCount: 5,
		Truncated:     true,
		Functions: []model.Function{
			{Name: "example.test/p.a", Cancels: []model.CancelBinding{{Factory: "context.WithCancel", Called: true}}},
			{Name: "example.test/p.b"},
		},
	}
}

func TestStatus_HidingLL9001KeepsTheRunIncomplete(t *testing.T) {
	for _, ignore := range [][]string{nil, {"LL9001"}, {"all"}} {
		cfg := config.Default()
		cfg.Ignore = ignore
		diags, st := AnalyzeWithStatus(truncatedProgram(), cfg)
		shown := false
		for _, d := range diags {
			if d.RuleID == "LL9001" {
				shown = true
			}
		}
		if wantShown := len(ignore) == 0; shown != wantShown {
			t.Errorf("ignore=%v: LL9001 shown=%v, want %v", ignore, shown, wantShown)
		}
		if !st.Incomplete {
			t.Errorf("ignore=%v: status.Incomplete=false; hiding the notice must not hide the truncation", ignore)
		}
		if len(st.Reasons) != 1 || st.Reasons[0].Kind != "max_functions" {
			t.Errorf("ignore=%v: reasons = %#v, want one max_functions reason", ignore, st.Reasons)
		}
		if st.Units.Discovered != 5 || st.Units.Analyzed != 2 || st.Units.Skipped != 3 {
			t.Errorf("ignore=%v: units = %#v, want 5 discovered, 2 analyzed, 3 skipped", ignore, st.Units)
		}
	}
}

func TestStatus_IsIndependentOfFiltering(t *testing.T) {
	prog := model.Program{
		PackagePath: "example.test/p", FunctionCount: 1,
		Functions: []model.Function{{Name: "example.test/p.f", Cancels: []model.CancelBinding{
			{Factory: "context.WithCancel", CancelName: "cancel", Discarded: true, Span: model.Span{File: "f.go", StartLine: 3}},
		}}},
	}
	base := config.Default()
	_, plain := AnalyzeWithStatus(prog, base)
	hide := config.Default()
	hide.Ignore = []string{"all"}
	diags, hidden := AnalyzeWithStatus(prog, hide)
	if len(diags) != 0 {
		t.Fatalf("ignore all must hide every diagnostic, got %#v", diags)
	}
	hidden.Suppressed, plain.Suppressed = Suppressed{}, Suppressed{}
	if !reflect.DeepEqual(plain, hidden) {
		t.Errorf("status changed when diagnostics were hidden:\n visible: %#v\n hidden:  %#v", plain, hidden)
	}
}

func TestStatus_CountsWhatWasHiddenAndBy(t *testing.T) {
	prog := model.Program{
		PackagePath: "example.test/p", FunctionCount: 1,
		Functions: []model.Function{{Name: "example.test/p.f", Cancels: []model.CancelBinding{
			{Factory: "context.WithCancel", Discarded: true, Span: model.Span{File: "f.go", StartLine: 3}},
			{Factory: "context.WithCancel", Discarded: true, Span: model.Span{File: "f.go", StartLine: 9}},
		}}},
		Suppressions: map[string]map[int][]string{"f.go": {9: {"LL1001"}}},
	}
	cfg := config.Default()
	_, st := AnalyzeWithStatus(prog, cfg)
	if st.Suppressed.ByComment != 1 || st.Suppressed.ByConfig != 0 || st.Suppressed.ByRule["LL1001"] != 1 {
		t.Errorf("comment suppression: %#v", st.Suppressed)
	}
	cfg.Ignore = []string{"LL1001"}
	_, st = AnalyzeWithStatus(prog, cfg)
	if st.Suppressed.ByConfig != 2 || st.Suppressed.ByComment != 0 || st.Suppressed.Total() != 2 {
		t.Errorf("config suppression: %#v", st.Suppressed)
	}
}

func TestStatus_SeparatesBoundedIncompletenessFromSemanticGaps(t *testing.T) {
	yes := true
	prog := model.Program{
		PackagePath: "example.test/p", FunctionCount: 1,
		Functions: []model.Function{{
			Name: "example.test/p.f",
			Cancels: []model.CancelBinding{
				{Factory: "context.WithCancel", Escapes: true},                        // handed off, nothing else known
				{Factory: "context.WithCancel", Called: true, Escapes: true},          // verified and also returned: not approximated
				{Factory: "context.WithCancel", Called: true},                         // discharged, all-paths unestablished
				{Factory: "context.WithCancel", Called: true, CalledOnAllPaths: &yes}, // fully verified
			},
			Groups:     []model.JoinGroup{{Kind: "waitgroup", Name: "wg", Starts: 1, Joined: true, Escapes: true}},
			Goroutines: []model.Goroutine{{Evidence: []model.Evidence{{Kind: "unsupported", Message: "target not statically identifiable"}}}},
		}},
	}
	_, st := AnalyzeWithStatus(prog, config.Default())
	if st.Incomplete {
		t.Errorf("a run with no bound hit is not incomplete, whatever it approximated: %#v", st)
	}
	want := Unsupported{Targets: 1, HandedOff: 1, UnestablishedPathChecks: 1}
	if st.Unsupported != want {
		t.Errorf("unsupported = %#v, want %#v", st.Unsupported, want)
	}
	if st.Unsupported.Total() != 3 {
		t.Errorf("total = %d, want 3", st.Unsupported.Total())
	}
}

func TestStatus_AFullyModeledRunHasNoGaps(t *testing.T) {
	yes := true
	prog := model.Program{
		PackagePath: "example.test/p", FunctionCount: 1,
		Functions: []model.Function{{Name: "example.test/p.f", Cancels: []model.CancelBinding{
			{Factory: "context.WithCancel", Called: true, CalledOnAllPaths: &yes},
		}}},
	}
	_, st := AnalyzeWithStatus(prog, config.Default())
	if st.Incomplete || st.Unsupported.Total() != 0 || st.Suppressed.Total() != 0 || st.Units.Skipped != 0 {
		t.Errorf("a fully verified run must have an empty status, got %#v", st)
	}
}

func TestStatus_AddMergesPackages(t *testing.T) {
	a := Status{Units: UnitStatus{Discovered: 3, Analyzed: 3}, Unsupported: Unsupported{HandedOff: 1}, Suppressed: Suppressed{ByConfig: 1, ByRule: map[string]int{"LL1001": 1}}, Assumptions: []string{"x", "y"}}
	b := Status{Units: UnitStatus{Discovered: 5, Analyzed: 2, Skipped: 3, ExcludedFiles: 2}, Incomplete: true, Reasons: []StatusReason{{Kind: "max_functions", Message: "m"}}, Unsupported: Unsupported{Targets: 2}, Suppressed: Suppressed{ByComment: 4, ByRule: map[string]int{"LL1001": 2, "LL1003": 1}}, Assumptions: []string{"y", "z"}}
	got := a.Add(b)
	if got.Units != (UnitStatus{Discovered: 8, Analyzed: 5, Skipped: 3, ExcludedFiles: 2}) || !got.Incomplete || len(got.Reasons) != 1 {
		t.Errorf("units/incomplete/reasons: %#v", got)
	}
	if got.Unsupported != (Unsupported{Targets: 2, HandedOff: 1}) {
		t.Errorf("unsupported: %#v", got.Unsupported)
	}
	if got.Suppressed.ByConfig != 1 || got.Suppressed.ByComment != 4 || got.Suppressed.ByRule["LL1001"] != 3 || got.Suppressed.ByRule["LL1003"] != 1 {
		t.Errorf("suppressed: %#v", got.Suppressed)
	}
	if !reflect.DeepEqual(got.Assumptions, []string{"x", "y", "z"}) {
		t.Errorf("assumptions: %#v", got.Assumptions)
	}
	if (Status{}).Add(Status{}).Incomplete {
		t.Error("empty plus empty must stay complete")
	}
}

func TestStatus_TimeoutIsRecordedAsIncomplete(t *testing.T) {
	st := Status{}.WithTimeout("analysis exceeded timeout 5s")
	if !st.Incomplete || len(st.Reasons) != 1 || st.Reasons[0].Kind != "timeout" {
		t.Errorf("status = %#v", st)
	}
}

func TestStatusPolicy_ReadsOnlyTheStatus(t *testing.T) {
	cases := []struct {
		name string
		st   Status
		cfg  func(*config.Config)
		want bool
	}{
		{"nothing configured", Status{Incomplete: true, Unsupported: Unsupported{Targets: 1}}, func(*config.Config) {}, false},
		{"incomplete under fail_on_incomplete", Status{Incomplete: true}, func(c *config.Config) { c.FailOnIncomplete = true }, true},
		{"complete under fail_on_incomplete", Status{}, func(c *config.Config) { c.FailOnIncomplete = true }, false},
		{"unsupported under fail_on_unsupported", Status{Unsupported: Unsupported{HandedOff: 1}}, func(c *config.Config) { c.FailOnUnsupported = true }, true},
		{"supported under fail_on_unsupported", Status{}, func(c *config.Config) { c.FailOnUnsupported = true }, false},
		{"incomplete does not trip fail_on_unsupported", Status{Incomplete: true}, func(c *config.Config) { c.FailOnUnsupported = true }, false},
		{"hiding LL9001 does not defeat fail_on_incomplete", Status{Incomplete: true}, func(c *config.Config) { c.FailOnIncomplete = true; c.Ignore = []string{"LL9001"} }, true},
	}
	for _, c := range cases {
		cfg := config.Default()
		c.cfg(&cfg)
		if got := FailsStatusPolicy(c.st, cfg); got != c.want {
			t.Errorf("%s: FailsStatusPolicy = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestStatus_LiteralUnitsCountAsUnits(t *testing.T) {
	prog := model.Program{PackagePath: "p", FunctionCount: 4, Functions: []model.Function{{Name: "p.a"}, {Name: "p.b"}, {Name: "p.a.func1"}}, Truncated: true}
	_, st := AnalyzeWithStatus(prog, config.Default())
	if st.Units.Discovered != 4 || st.Units.Analyzed != 3 || st.Units.Skipped != 1 {
		t.Errorf("units = %#v", st.Units)
	}
}
