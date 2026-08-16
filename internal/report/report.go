package report

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"

	"github.com/gfedyukovich/lifeline/internal/engine"
	"github.com/gfedyukovich/lifeline/internal/model"
	"github.com/gfedyukovich/lifeline/internal/version"
)

type Bundle struct {
	SchemaVersion string              `json:"schema_version"`
	Tool          Tool                `json:"tool"`
	Diagnostics   []engine.Diagnostic `json:"diagnostics"`
	Coverage      engine.Coverage     `json:"coverage"`
	Incomplete    bool                `json:"incomplete"`
}

type Tool struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Backend string `json:"backend"`
}

func New(diags []engine.Diagnostic, coverage engine.Coverage) Bundle {
	incomplete := false
	for _, d := range diags {
		if d.Verdict == engine.Unknown {
			incomplete = true
		}
	}
	return Bundle{SchemaVersion: version.ReportSchema, Tool: Tool{Name: version.Tool, Version: version.Version, Backend: version.Backend}, Diagnostics: append([]engine.Diagnostic{}, diags...), Coverage: coverage, Incomplete: incomplete}
}

func Write(w io.Writer, format string, diags []engine.Diagnostic, coverage engine.Coverage, cwd string) error {
	switch format {
	case "text":
		return writeText(w, diags, coverage, cwd)
	case "json":
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		bundle := New(relativeDiagnostics(diags, cwd), relativeCoverage(coverage, cwd))
		return enc.Encode(bundle)
	case "sarif":
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(makeSARIF(diags, coverage, cwd))
	default:
		return fmt.Errorf("unsupported output format %q", format)
	}
}

func writeText(w io.Writer, diags []engine.Diagnostic, coverage engine.Coverage, cwd string) error {
	buffered := bufio.NewWriter(w)
	if len(diags) == 0 {
		if err := writeCleanSummary(buffered, relativeCoverage(coverage, cwd)); err != nil {
			return err
		}
		return buffered.Flush()
	}
	for _, d := range diags {
		file := displayPath(d.Position.File, cwd)
		if file == "" {
			file = d.PackagePath
		}
		if _, err := fmt.Fprintf(buffered, "%s:%d:%d: [%s] %s\n", file, d.Position.StartLine, d.Position.StartColumn, d.RuleID, d.Message); err != nil {
			return err
		}
		if len(d.Evidence) > 0 {
			for _, e := range d.Evidence {
				if _, err := fmt.Fprintf(buffered, "  evidence: %s\n", e.Message); err != nil {
					return err
				}
			}
		}
		if d.Suggestion != "" {
			if _, err := fmt.Fprintf(buffered, "  action: %s\n", d.Suggestion); err != nil {
				return err
			}
		}
		if _, err := fmt.Fprintf(buffered, "  model: %s; max_functions=%v; timeout=%v\n", d.Backend, d.Bounds["max_functions"], d.Bounds["timeout"]); err != nil {
			return err
		}
	}
	return buffered.Flush()
}

// writeCleanSummary renders the zero-diagnostics case. It distinguishes
// three situations that previously all produced the identical string
// "no lifecycle diagnostics": genuinely nothing lifecycle-relevant was
// found, everything found was compliant, or everything found was compliant
// except for goroutine targets whose bodies could not be inspected. See
// docs/limitations.md for what "recognized" does and does not cover.
func writeCleanSummary(w io.Writer, coverage engine.Coverage) error {
	if coverage.Recognized() == 0 {
		_, err := fmt.Fprintln(w, "no recognized lifecycle constructs (0 cancel bindings, 0 goroutines, 0 groups): "+
			"either this package has none, or it uses wrapper functions not declared in lifeline.yaml "+
			"(context_wrappers, start_wrappers, join_wrappers, stop_wrappers)")
		return err
	}
	if len(coverage.UnsupportedTargets) == 0 {
		_, err := fmt.Fprintf(w, "no lifecycle diagnostics (checked %d cancel binding(s), %d goroutine(s), %d group(s))\n",
			coverage.CancelBindings, coverage.Goroutines, coverage.Groups)
		return err
	}
	if _, err := fmt.Fprintf(w, "no lifecycle diagnostics (checked %d cancel binding(s), %d goroutine(s), %d group(s)); "+
		"%d goroutine target(s) could not be analyzed and are excluded from this result:\n",
		coverage.CancelBindings, coverage.Goroutines, coverage.Groups, len(coverage.UnsupportedTargets)); err != nil {
		return err
	}
	for _, u := range coverage.UnsupportedTargets {
		file := u.Position.File
		if file == "" {
			file = "?"
		}
		if _, err := fmt.Fprintf(w, "  %s:%d:%d  %s\n", file, u.Position.StartLine, u.Position.StartColumn, u.Message); err != nil {
			return err
		}
	}
	return nil
}

func relativeCoverage(coverage engine.Coverage, cwd string) engine.Coverage {
	out := coverage
	if len(coverage.UnsupportedTargets) > 0 {
		out.UnsupportedTargets = append([]engine.UnsupportedTarget{}, coverage.UnsupportedTargets...)
		for i := range out.UnsupportedTargets {
			out.UnsupportedTargets[i].Position.File = displayPath(out.UnsupportedTargets[i].Position.File, cwd)
		}
	}
	return out
}

func relativeDiagnostics(diags []engine.Diagnostic, cwd string) []engine.Diagnostic {
	out := make([]engine.Diagnostic, len(diags))
	copy(out, diags)
	for i := range out {
		out[i].Position.File = displayPath(out[i].Position.File, cwd)
		out[i].Evidence = append([]model.Evidence{}, out[i].Evidence...)
		for j := range out[i].Evidence {
			if out[i].Evidence[j].Span != nil {
				span := *out[i].Evidence[j].Span
				span.File = displayPath(span.File, cwd)
				out[i].Evidence[j].Span = &span
			}
		}
		if out[i].SuggestedFix != nil {
			fix := *out[i].SuggestedFix
			fix.Edits = append([]model.FixEdit{}, fix.Edits...)
			for j := range fix.Edits {
				fix.Edits[j].Span.File = displayPath(fix.Edits[j].Span.File, cwd)
			}
			out[i].SuggestedFix = &fix
		}
	}
	return out
}

func displayPath(path, cwd string) string {
	if path == "" {
		return ""
	}
	if cwd != "" {
		if rel, err := filepath.Rel(cwd, path); err == nil && rel != "" && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return rel
		}
	}
	return filepath.Clean(path)
}

type sarifLog struct {
	Version string     `json:"version"`
	Schema  string     `json:"$schema"`
	Runs    []sarifRun `json:"runs"`
}

type sarifRun struct {
	Tool       sarifTool      `json:"tool"`
	Results    []sarifResult  `json:"results"`
	Properties map[string]any `json:"properties,omitempty"`
}

type sarifTool struct {
	Driver sarifDriver `json:"driver"`
}
type sarifDriver struct {
	Name           string      `json:"name"`
	Version        string      `json:"version"`
	InformationURI string      `json:"informationUri,omitempty"`
	Rules          []sarifRule `json:"rules"`
}
type sarifRule struct {
	ID               string `json:"id"`
	Name             string `json:"name"`
	ShortDescription struct {
		Text string `json:"text"`
	} `json:"shortDescription"`
	DefaultConfiguration struct {
		Level string `json:"level"`
	} `json:"defaultConfiguration"`
}
type sarifResult struct {
	RuleID  string `json:"ruleId"`
	Level   string `json:"level"`
	Message struct {
		Text string `json:"text"`
	} `json:"message"`
	Locations  []sarifLocation `json:"locations,omitempty"`
	Properties map[string]any  `json:"properties,omitempty"`
}
type sarifLocation struct {
	PhysicalLocation sarifPhysical `json:"physicalLocation"`
}
type sarifPhysical struct {
	ArtifactLocation struct {
		URI string `json:"uri"`
	} `json:"artifactLocation"`
	Region struct {
		StartLine   int `json:"startLine"`
		StartColumn int `json:"startColumn"`
		EndLine     int `json:"endLine,omitempty"`
		EndColumn   int `json:"endColumn,omitempty"`
	} `json:"region"`
}

func makeSARIF(diags []engine.Diagnostic, coverage engine.Coverage, cwd string) sarifLog {
	rules := append([]engine.Rule(nil), engine.Rules...)
	sort.Slice(rules, func(i, j int) bool { return rules[i].ID < rules[j].ID })
	driver := sarifDriver{Name: version.Tool, Version: version.Version, InformationURI: version.InformationURI}
	for _, r := range rules {
		sr := sarifRule{ID: r.ID, Name: r.Name}
		sr.ShortDescription.Text = r.Description
		sr.DefaultConfiguration.Level = r.Level
		driver.Rules = append(driver.Rules, sr)
	}
	// coverage is carried as a run-level property rather than synthetic
	// per-target results: unsupported targets are not rule violations, and
	// giving them a fabricated ruleId would misrepresent them as findings
	// against a registered rule to SARIF consumers.
	run := sarifRun{Tool: sarifTool{Driver: driver}, Results: []sarifResult{}, Properties: map[string]any{"backend": version.Backend, "coverage": relativeCoverage(coverage, cwd)}}
	for _, d := range diags {
		level := "warning"
		if d.Verdict == engine.Unknown {
			level = "note"
		}
		r := sarifResult{RuleID: d.RuleID, Level: level, Properties: map[string]any{"protocol": d.Protocol, "assumptions": d.Assumptions, "bounds": d.Bounds}}
		r.Message.Text = d.Message
		if d.Position.File != "" {
			var loc sarifLocation
			loc.PhysicalLocation.ArtifactLocation.URI = filepath.ToSlash(displayPath(d.Position.File, cwd))
			loc.PhysicalLocation.Region.StartLine = max(1, d.Position.StartLine)
			loc.PhysicalLocation.Region.StartColumn = max(1, d.Position.StartColumn)
			loc.PhysicalLocation.Region.EndLine = d.Position.EndLine
			loc.PhysicalLocation.Region.EndColumn = d.Position.EndColumn
			r.Locations = []sarifLocation{loc}
		}
		run.Results = append(run.Results, r)
	}
	return sarifLog{Version: "2.1.0", Schema: "https://json.schemastore.org/sarif-2.1.0.json", Runs: []sarifRun{run}}
}
