package engine

import (
	"fmt"
	"sort"
	"strings"

	"github.com/gfedyukovich/lifeline/internal/config"
	"github.com/gfedyukovich/lifeline/internal/model"
	"github.com/gfedyukovich/lifeline/internal/version"
)

type Verdict string

const (
	Warning Verdict = "WARNING"
	Unknown Verdict = "UNKNOWN"
)

type Diagnostic struct {
	SchemaVersion string              `json:"schema_version"`
	RuleID        string              `json:"rule_id"`
	Verdict       Verdict             `json:"verdict"`
	Message       string              `json:"message"`
	Position      model.Span          `json:"position"`
	Protocol      string              `json:"protocol"`
	Evidence      []model.Evidence    `json:"evidence"`
	Assumptions   []string            `json:"assumptions"`
	Bounds        map[string]any      `json:"bounds"`
	Suggestion    string              `json:"suggestion,omitempty"`
	SuggestedFix  *model.SuggestedFix `json:"suggested_fix,omitempty"`
	ToolVersion   string              `json:"tool_version"`
	Backend       string              `json:"backend"`
	PackagePath   string              `json:"package_path"`
	Function      string              `json:"function,omitempty"`
}

type Rule struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Level       string `json:"level"`
}

var Rules = []Rule{
	{ID: "LL1001", Name: "lost-cancel", Description: "A context cancellation function is discarded or has no observed call/ownership transfer.", Level: "warning"},
	{ID: "LL1002", Name: "goroutine-no-stop", Description: "An unconditional-loop goroutine has no recognized cancellation or termination path.", Level: "warning"},
	{ID: "LL1003", Name: "waitgroup-no-wait", Description: "A local WaitGroup accounts for workers but is not joined or transferred.", Level: "warning"},
	{ID: "LL1004", Name: "errgroup-no-wait", Description: "A local errgroup starts workers but is not waited or transferred.", Level: "warning"},
	{ID: "LL9001", Name: "analysis-incomplete", Description: "A configured bound truncated analysis; remaining code is unknown.", Level: "note"},
}

type analysisMetadata struct {
	assumptions []string
	bounds      map[string]any
}

var sharedAssumptions = []string{
	"local type-checked syntax, direct same-package callees, and versioned direct cross-package function facts",
	"reflection, generated dispatch, and arbitrary aliasing are not modeled",
	"a context passed to a called operation is treated as delegated cancellation",
	"absence of a recognized protocol is evidence, not a proof of nontermination",
}

func Analyze(program model.Program, cfg config.Config) []Diagnostic {
	meta := analysisMetadata{
		assumptions: sharedAssumptions,
		bounds:      map[string]any{"max_functions": cfg.MaxFunctions, "timeout": cfg.Timeout},
	}
	var out []Diagnostic
	for _, fn := range program.Functions {
		for _, c := range fn.Cancels {
			if c.Called || c.Escapes || (!c.Discarded && c.CancelName == "") {
				continue
			}
			msg := fmt.Sprintf("cancel function returned by %s is discarded", c.Factory)
			if !c.Discarded {
				msg = fmt.Sprintf("cancel function %q returned by %s has no observed call or ownership transfer", c.CancelName, c.Factory)
			}
			suggestion := "call the cancellation function on every path, usually with defer immediately after construction"
			if c.UsedByChild {
				msg += "; a child goroutine uses the derived context and may retain resources until parent cancellation"
			}
			out = append(out, base(program, fn, "LL1001", Warning, msg, c.Span, "context-cancel", c.Evidence, suggestion, c.SuggestedFix, meta))
		}
		for _, g := range fn.Goroutines {
			if !g.InfiniteLoop || g.HasReturn || g.ContextStop || g.ChannelStop || g.ExplicitStop {
				continue
			}
			msg := "goroutine has an unconditional loop and no recognized termination path"
			suggestion := "establish an explicit owner and stop protocol"
			if len(g.AvailableContexts) > 0 {
				msg += "; available cancellation source: " + strings.Join(g.AvailableContexts, ", ") + ".Done()"
				suggestion = "select on the available context's Done channel and return"
			}
			out = append(out, base(program, fn, "LL1002", Warning, msg, g.Span, "goroutine-stop", g.Evidence, suggestion, nil, meta))
		}
		for _, group := range fn.Groups {
			if group.Starts == 0 || group.Joined || group.Escapes {
				continue
			}
			rule := "LL1003"
			protocol := "waitgroup-join"
			msg := fmt.Sprintf("%s %q accounts for %d worker start(s) but no Wait or ownership transfer is observed", group.Kind, group.Name, group.Starts)
			suggestion := "wait for the group before the owner returns, or transfer ownership explicitly"
			if group.Kind == "errgroup" {
				rule = "LL1004"
				protocol = "errgroup-join"
				msg = fmt.Sprintf("errgroup %q starts %d worker(s) but no Wait or ownership transfer is observed", group.Name, group.Starts)
			}
			out = append(out, base(program, fn, rule, Warning, msg, group.Span, protocol, group.Evidence, suggestion, nil, meta))
		}
	}
	if program.Truncated {
		span := model.Span{}
		if len(program.Functions) > 0 {
			span = program.Functions[len(program.Functions)-1].Span
		}
		d := Diagnostic{
			SchemaVersion: version.ReportSchema,
			RuleID:        "LL9001", Verdict: Unknown,
			Message:  fmt.Sprintf("analysis stopped after %d of %d functions", len(program.Functions), program.FunctionCount),
			Position: span, Protocol: "analysis-bound",
			Evidence:    []model.Evidence{{Kind: "bound", Message: fmt.Sprintf("max_functions=%d", cfg.MaxFunctions)}},
			Assumptions: meta.assumptions, Bounds: meta.bounds, ToolVersion: version.Version, Backend: version.Backend, PackagePath: program.PackagePath,
		}
		out = append(out, d)
	}
	filtered := out[:0]
	for _, d := range out {
		if ignored(cfg.Ignore, d.RuleID) {
			continue
		}
		filtered = append(filtered, d)
	}
	sort.SliceStable(filtered, func(i, j int) bool {
		a, b := filtered[i], filtered[j]
		if a.Position.File != b.Position.File {
			return a.Position.File < b.Position.File
		}
		if a.Position.StartOffset != b.Position.StartOffset {
			return a.Position.StartOffset < b.Position.StartOffset
		}
		return a.RuleID < b.RuleID
	})
	return filtered
}

func base(program model.Program, fn model.Function, rule string, verdict Verdict, message string, pos model.Span, protocol string, evidence []model.Evidence, suggestion string, fix *model.SuggestedFix, meta analysisMetadata) Diagnostic {
	return Diagnostic{
		SchemaVersion: version.ReportSchema,
		RuleID:        rule, Verdict: verdict, Message: message, Position: pos,
		Protocol: protocol, Evidence: evidence, Assumptions: meta.assumptions, Bounds: meta.bounds,
		Suggestion: suggestion, SuggestedFix: fix, ToolVersion: version.Version, Backend: version.Backend,
		PackagePath: program.PackagePath, Function: fn.Name,
	}
}

func ignored(list []string, id string) bool {
	for _, item := range list {
		if item == "all" || item == id {
			return true
		}
	}
	return false
}

func FailsPolicy(diags []Diagnostic, failOn []string) bool {
	if len(failOn) == 0 {
		return false
	}
	for _, d := range diags {
		for _, id := range failOn {
			if id == d.RuleID || (id == "all" && d.Verdict == Warning) {
				return true
			}
		}
	}
	return false
}
