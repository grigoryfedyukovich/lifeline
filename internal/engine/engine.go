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
	{ID: "LL1003", Name: "waitgroup-no-wait", Description: "A local WaitGroup accounts for workers but is not fully, verifiably joined before the owner returns.", Level: "warning"},
	{ID: "LL1004", Name: "errgroup-no-wait", Description: "A local errgroup starts workers but is not fully, verifiably joined before the owner returns.", Level: "warning"},
	{ID: "LL1005", Name: "wait-before-stop", Description: "A group is joined before its workers' own stop signal is guaranteed to have been sent.", Level: "warning"},
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
			switch {
			case g.CFG != nil:
				// CFG/SCC-based verdict (Phase 2 of the AST->CFG migration,
				// see docs/cfg-migration-plan.md): true reachability over
				// explicit control flow, for every case that has a body to
				// build a CFG from.
				if !UnresolvedLoop(g.CFG) {
					continue
				}
			case g.ImportedUnresolvedLoop != nil:
				// A cross-package go vet fact carrying its own CFG/SCC-
				// derived verdict (Phase 4): the exporting package already
				// computed this from its own CFG and is trusted here the
				// same way any other cross-package fact is trusted.
				if !*g.ImportedUnresolvedLoop {
					continue
				}
			default:
				// No CFG and no imported verdict available: an unsupported
				// target with no body to analyze (InfiniteLoop is never set
				// for these, so this always continues), or a fact from a
				// version too old to carry ImportedUnresolvedLoop.
				if !g.InfiniteLoop || g.HasReturn || g.ContextStop || g.ChannelStop || g.ExplicitStop {
					continue
				}
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
			if group.Starts == 0 || group.Escapes {
				continue
			}
			// Phase 3 completion (docs/cfg-migration-plan.md): what used to
			// be a single flat "was Wait observed anywhere" check is now
			// three independently-verified questions, each with its own
			// message so the finding says which one actually failed --
			// unjoined (unchanged from before this phase), joined but not
			// on every path back to the owner's return (join-before-owner-
			// return), and joined on every path but literal accounting
			// still proves an outstanding worker (count intervals). Only
			// the unjoined case existed before; the other two are real,
			// checked findings the old flat Joined bool could not tell
			// apart from "everything's fine". JoinedOnAllPaths == nil (not
			// established at all -- no CFG, or Joined came from a
			// join_wrapper call with no call site to check) is treated the
			// same as "on all paths", matching Joined's own pre-Phase-3
			// meaning: this only ever downgrades a verdict on positive
			// evidence, never on an inability to check.
			joinedOnAllPaths := group.JoinedOnAllPaths == nil || *group.JoinedOnAllPaths
			if !(group.Joined && joinedOnAllPaths && !group.CountMismatch) {
				rule, protocol, msg := "LL1003", "waitgroup-join", ""
				switch {
				case !group.Joined:
					msg = fmt.Sprintf("%s %q accounts for %d worker start(s) but no Wait or ownership transfer is observed", group.Kind, group.Name, group.Starts)
				case !joinedOnAllPaths:
					msg = fmt.Sprintf("%s %q is joined on some but not every path back to the owner's return", group.Kind, group.Name)
				default: // group.CountMismatch
					msg = fmt.Sprintf("%s %q is joined on every path, but literal Add/Done accounting proves an outstanding worker; Wait may never return", group.Kind, group.Name)
				}
				suggestion := "wait for the group before the owner returns, or transfer ownership explicitly"
				switch {
				case !group.Joined:
					// keep the default suggestion above
				case !joinedOnAllPaths:
					suggestion = "move the Wait call so every return path reaches it, e.g. with defer"
				default: // group.CountMismatch
					suggestion = "check every Add call is matched by exactly one Done, including on early-return paths"
				}
				// The three cases above are meant to say which one of three
				// independently-verified questions failed (see the big
				// comment on the outer loop above), but they were only ever
				// checked as an exclusive switch: whichever case matched
				// first won, silently dropping any of the other two that
				// also happened to be true for the same group. !Joined and
				// !joinedOnAllPaths can never both be true together
				// (joinedOnAllPaths defaults to true whenever Joined is
				// false, so there is nothing to drop between those two),
				// but CountMismatch is computed entirely independently of
				// both -- literal Add/Done interval accounting, unrelated
				// to whether a Wait call was ever seen or which paths reach
				// it -- so a group can easily be genuinely unjoined (or
				// joined on only some paths) *and* have a real, separately
				// verified Add/Done imbalance underneath that. Evidence
				// already carries both regardless (computeGroupBalances
				// appends its own "count-mismatch" entry unconditionally),
				// but the message/suggestion text a person actually reads
				// first previously mentioned only whichever came first in
				// the switch above. Confirmed concretely: `wg.Add(2); go
				// worker; if cond { return }; wg.Wait()` reported only "is
				// joined on some but not every path", with a suggestion to
				// add a defer -- which would fix that path issue but leave
				// the real, separately-verified Add/Done imbalance
				// completely unmentioned in the actionable text, to be
				// discovered only on a second run after the first fix, if
				// at all.
				if group.CountMismatch && (!group.Joined || !joinedOnAllPaths) {
					msg += "; independently, literal Add/Done accounting also shows an outstanding worker"
					suggestion += "; also check every Add call is matched by exactly one Done, including on early-return paths"
				}
				if group.Kind == "errgroup" {
					rule, protocol = "LL1004", "errgroup-join"
					if !group.Joined {
						msg = fmt.Sprintf("errgroup %q starts %d worker(s) but no Wait or ownership transfer is observed", group.Name, group.Starts)
					}
				}
				out = append(out, base(program, fn, rule, Warning, msg, group.Span, protocol, group.Evidence, suggestion, nil, meta))
			}
			if group.StopAfterWait {
				msg := fmt.Sprintf("%s %q is joined before its workers' own stop signal is guaranteed to have been sent", group.Kind, group.Name)
				suggestion := "send the stop signal before waiting, not after"
				out = append(out, base(program, fn, "LL1005", Warning, msg, group.Span, "wait-before-stop", group.Evidence, suggestion, nil, meta))
			}
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
		if d.RuleID != "LL9001" && suppressedByComment(program.Suppressions, d) {
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

// suppressedByComment reports whether an inline "//lifeline:ignore" comment
// suppresses this diagnostic. It checks the diagnostic's own reported line
// as well as every evidence span's line, not just the former: LL1002's
// reported position is the goroutine's start site, which is often a
// different line than the specific loop a reviewer would naturally
// annotate, and that loop's own position is carried as evidence. This lets
// a suppression comment go on whichever line is most natural for the
// specific construct, matching how most inline-suppression comments read
// in practice. suppressions is Program.Suppressions, a pure file -> line ->
// rule-IDs index computed once during parsing (see internal/frontend);
// this is a lookup only and performs no I/O of its own.
func suppressedByComment(suppressions map[string]map[int][]string, d Diagnostic) bool {
	if lineSuppressed(suppressions, d.Position.File, d.Position.StartLine, d.RuleID) {
		return true
	}
	for _, ev := range d.Evidence {
		if ev.Span != nil && lineSuppressed(suppressions, ev.Span.File, ev.Span.StartLine, d.RuleID) {
			return true
		}
	}
	return false
}

func lineSuppressed(suppressions map[string]map[int][]string, file string, line int, ruleID string) bool {
	for _, id := range suppressions[file][line] {
		if id == "*" || id == ruleID {
			return true
		}
	}
	return false
}

// UnsupportedTarget describes a goroutine start site whose target body could
// not be inspected, so it contributes no lifecycle evidence and can never
// produce an LL1002 finding either way. These are distinct from constructs
// that were never recognized at all (see Coverage.NothingRecognized): a
// start site *was* found, but its body is opaque to this analysis.
type UnsupportedTarget struct {
	Function string     `json:"function"`
	Position model.Span `json:"position"`
	Message  string     `json:"message"`
}

// Coverage summarizes how many lifecycle-relevant constructs this run
// actually recognized and modeled, independent of whether any of them
// produced a diagnostic. It exists so a clean result can be told apart from
// a package with nothing recognized in it, and so goroutine targets that
// were found but could not be inspected are visible rather than silently
// absent. See docs/limitations.md for the constructs this cannot detect at
// all (for example, a wrapper function not declared in configuration never
// creates a binding to summarize in the first place).
type Coverage struct {
	CancelBindings     int                 `json:"cancel_bindings"`
	Goroutines         int                 `json:"goroutines"`
	Groups             int                 `json:"groups"`
	UnsupportedTargets []UnsupportedTarget `json:"unsupported_targets,omitempty"`
}

// Recognized reports whether any lifecycle-relevant construct was found at
// all, regardless of whether it was fully inspectable.
func (c Coverage) Recognized() int {
	return c.CancelBindings + c.Goroutines + c.Groups
}

func (c Coverage) Add(other Coverage) Coverage {
	return Coverage{
		CancelBindings:     c.CancelBindings + other.CancelBindings,
		Goroutines:         c.Goroutines + other.Goroutines,
		Groups:             c.Groups + other.Groups,
		UnsupportedTargets: append(append([]UnsupportedTarget{}, c.UnsupportedTargets...), other.UnsupportedTargets...),
	}
}

// Summarize computes Coverage for a single package's model. It reads only
// evidence the frontend already records (see the "unsupported" Kind
// produced by buildGoroutine in internal/frontend) and adds no new
// detection of its own.
func Summarize(program model.Program) Coverage {
	var c Coverage
	for _, fn := range program.Functions {
		c.CancelBindings += len(fn.Cancels)
		c.Groups += len(fn.Groups)
		c.Goroutines += len(fn.Goroutines)
		for _, g := range fn.Goroutines {
			msg, unsupported := unsupportedReason(g)
			if !unsupported {
				continue
			}
			c.UnsupportedTargets = append(c.UnsupportedTargets, UnsupportedTarget{
				Function: fn.Name, Position: g.Span, Message: msg,
			})
		}
	}
	return c
}

func unsupportedReason(g model.Goroutine) (string, bool) {
	for _, e := range g.Evidence {
		if e.Kind == "unsupported" {
			return e.Message, true
		}
	}
	return "", false
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
