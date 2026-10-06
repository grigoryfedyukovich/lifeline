package report

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/gfedyukovich/lifeline/internal/config"
	"github.com/gfedyukovich/lifeline/internal/engine"
	"github.com/gfedyukovich/lifeline/internal/model"
)

// Regression tests for audit finding F8: a machine-readable run status that
// does not depend on which diagnostics survived filtering.

func truncated() model.Program {
	return model.Program{PackagePath: "example.test/p", FunctionCount: 5, Truncated: true, Functions: []model.Function{{Name: "example.test/p.a"}, {Name: "example.test/p.b"}}}
}

func render(t *testing.T, format string, prog model.Program, mutate func(*config.Config)) string {
	t.Helper()
	cfg := config.Default()
	if mutate != nil {
		mutate(&cfg)
	}
	diags, status := engine.AnalyzeWithStatus(prog, cfg)
	var buf bytes.Buffer
	if err := Write(&buf, format, diags, engine.Summarize(prog), status, ""); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

func bundleOf(t *testing.T, prog model.Program, mutate func(*config.Config)) Bundle {
	t.Helper()
	var b Bundle
	if err := json.Unmarshal([]byte(render(t, "json", prog, mutate)), &b); err != nil {
		t.Fatal(err)
	}
	return b
}

// The audit's first acceptance criterion.
func TestReport_SuppressingLL9001HidesTheNoticeNotTheTruncation(t *testing.T) {
	shown := bundleOf(t, truncated(), nil)
	hidden := bundleOf(t, truncated(), func(c *config.Config) { c.Ignore = []string{"LL9001"} })

	if len(shown.Diagnostics) != 1 || shown.Diagnostics[0].RuleID != "LL9001" {
		t.Fatalf("by default the truncation is shown as LL9001, got %#v", shown.Diagnostics)
	}
	if len(hidden.Diagnostics) != 0 {
		t.Fatalf("ignoring LL9001 must hide the diagnostic, got %#v", hidden.Diagnostics)
	}
	for name, b := range map[string]Bundle{"shown": shown, "hidden": hidden} {
		if !b.Incomplete || !b.Status.Incomplete {
			t.Errorf("%s: incomplete = %v / status.incomplete = %v, want true: the run was truncated either way", name, b.Incomplete, b.Status.Incomplete)
		}
		if b.Status.Units.Skipped != 3 || len(b.Status.Reasons) != 1 || b.Status.Reasons[0].Kind != "max_functions" {
			t.Errorf("%s: status = %#v, want 3 skipped units and a max_functions reason", name, b.Status)
		}
	}
	if hidden.Status.Suppressed.ByConfig != 1 || hidden.Status.Suppressed.ByRule["LL9001"] != 1 {
		t.Errorf("the hidden notice must be counted: %#v", hidden.Status.Suppressed)
	}
}

func TestReport_IncompleteIsNotDerivedFromDiagnostics(t *testing.T) {
	// An UNKNOWN diagnostic with a complete status does not make the run
	// incomplete, and an incomplete status with no diagnostics does.
	b := New([]engine.Diagnostic{{RuleID: "LL9001", Verdict: engine.Unknown}}, engine.Coverage{}, engine.Status{})
	if b.Incomplete {
		t.Error("incomplete must come from the status, not from the presence of an UNKNOWN diagnostic")
	}
	b = New(nil, engine.Coverage{}, engine.Status{Incomplete: true})
	if !b.Incomplete {
		t.Error("an incomplete status with no diagnostics must still be incomplete")
	}
}

func TestReport_TextSaysIncompleteEvenWhenTheNoticeIsHidden(t *testing.T) {
	hidden := render(t, "text", truncated(), func(c *config.Config) { c.Ignore = []string{"LL9001"} })
	if !strings.Contains(hidden, "analysis incomplete (max_functions)") || !strings.Contains(hidden, "only part of the input") {
		t.Errorf("a hidden truncation must still be stated in text output:\n%s", hidden)
	}
	shown := render(t, "text", truncated(), nil)
	if strings.Contains(shown, "analysis incomplete") {
		t.Errorf("when LL9001 is shown the notice must not be repeated:\n%s", shown)
	}
	// ignore all hides LL9001 too
	all := render(t, "text", truncated(), func(c *config.Config) { c.Ignore = []string{"all"} })
	if !strings.Contains(all, "analysis incomplete") {
		t.Errorf("ignore=all must not hide the truncation:\n%s", all)
	}
}

func modeledClean() model.Program {
	yes := true
	return model.Program{PackagePath: "example.test/p", FunctionCount: 1, Functions: []model.Function{{Name: "example.test/p.f", Cancels: []model.CancelBinding{{Factory: "context.WithCancel", Called: true, CalledOnAllPaths: &yes}}}}}
}

func unsupportedOnly() model.Program {
	return model.Program{PackagePath: "example.test/p", FunctionCount: 1, Functions: []model.Function{{Name: "example.test/p.f", Cancels: []model.CancelBinding{{Factory: "context.WithCancel", Escapes: true}}}}}
}

// The audit's second acceptance criterion.
func TestReport_UnsupportedOnlyInputIsNotEquivalentToFullyModeledInput(t *testing.T) {
	modeledText, unsupportedText := render(t, "text", modeledClean(), nil), render(t, "text", unsupportedOnly(), nil)
	if modeledText == unsupportedText {
		t.Fatalf("a fully verified run and a run that only handed its obligation off must not print the same thing:\n%s", modeledText)
	}
	if !strings.Contains(unsupportedText, "approximations: 1 obligation(s) handed off to code outside the owning function") {
		t.Errorf("the hand-off must be visible in text:\n%s", unsupportedText)
	}
	if strings.Contains(modeledText, "approximations") {
		t.Errorf("a fully verified run must not claim approximations:\n%s", modeledText)
	}

	m, u := bundleOf(t, modeledClean(), nil), bundleOf(t, unsupportedOnly(), nil)
	if m.Status.Unsupported.Total() != 0 || u.Status.Unsupported.HandedOff != 1 || u.Status.Unsupported.Total() != 1 {
		t.Errorf("status.unsupported: modeled %#v, unsupported-only %#v", m.Status.Unsupported, u.Status.Unsupported)
	}
	if u.Incomplete {
		t.Error("an approximated run is not incomplete: those are separate dimensions")
	}
	if len(m.Diagnostics) != 0 || len(u.Diagnostics) != 0 {
		t.Error("both inputs report no diagnostics; the status is the only difference")
	}
}

func TestReport_HiddenDiagnosticsAreVisibleInACleanSummary(t *testing.T) {
	prog := model.Program{PackagePath: "p", FunctionCount: 1, Functions: []model.Function{{Name: "p.f", Cancels: []model.CancelBinding{{Factory: "context.WithCancel", Discarded: true}}}}}
	out := render(t, "text", prog, func(c *config.Config) { c.Ignore = []string{"LL1001"} })
	if !strings.Contains(out, "1 diagnostic(s) found and hidden") {
		t.Errorf("a clean-looking result with hidden findings must say so:\n%s", out)
	}
}

func TestReport_ExcludedFilesAreVisible(t *testing.T) {
	prog := modeledClean()
	prog.ExcludedFiles = 2
	if out := render(t, "text", prog, nil); !strings.Contains(out, "2 file(s) excluded from analysis") {
		t.Errorf("excluded files must be stated:\n%s", out)
	}
	if b := bundleOf(t, prog, nil); b.Status.Units.ExcludedFiles != 2 {
		t.Errorf("status.units.excluded_files = %d, want 2", b.Status.Units.ExcludedFiles)
	}
}

func TestReport_SARIFCarriesTheStatus(t *testing.T) {
	var log struct {
		Runs []struct {
			Properties struct {
				Incomplete bool          `json:"incomplete"`
				Status     engine.Status `json:"status"`
			} `json:"properties"`
		} `json:"runs"`
	}
	out := render(t, "sarif", truncated(), func(c *config.Config) { c.Ignore = []string{"LL9001"} })
	if err := json.Unmarshal([]byte(out), &log); err != nil {
		t.Fatal(err)
	}
	if len(log.Runs) != 1 || !log.Runs[0].Properties.Incomplete || log.Runs[0].Properties.Status.Units.Skipped != 3 {
		t.Errorf("SARIF run properties must carry the status: %s", out)
	}
}
