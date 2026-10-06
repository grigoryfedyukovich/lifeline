package engine

import (
	"fmt"
	"sort"
	"strings"

	"github.com/gfedyukovich/lifeline/internal/model"
)

// Status describes the run itself, independently of the diagnostics it
// produced (audit finding F8). Diagnostics are filtered by configuration and
// by suppression comments; the status never is. It separates two different
// questions that a single "incomplete" flag used to blur:
//
//   - Incomplete: did a bound or a deadline stop the analysis before it
//     covered its input? (a bounded, resource-driven gap: results describe
//     only part of the code)
//   - Unsupported: of what was analyzed, how much was judged under an
//     approximation or outside the model? (a semantic gap: the results are
//     real but rest on assumptions)
//
// A run can be complete and still rest on assumptions, and a run can stop
// early on code that is fully modeled; neither implies the other, and
// neither is a statement about semantic completeness in the sense of "no
// protocol violation exists".
type Status struct {
	Units       UnitStatus     `json:"units"`
	Incomplete  bool           `json:"incomplete"`
	Reasons     []StatusReason `json:"reasons,omitempty"`
	Unsupported Unsupported    `json:"unsupported"`
	Suppressed  Suppressed     `json:"suppressed"`
	Assumptions []string       `json:"assumptions,omitempty"`
}

// UnitStatus counts analysis units: named functions plus function literals
// (see audit finding F5). Skipped is Discovered - Analyzed: units dropped by
// a bound. ExcludedFiles are files filtered out before analysis, which never
// became units at all.
type UnitStatus struct {
	Discovered    int `json:"discovered"`
	Analyzed      int `json:"analyzed"`
	Skipped       int `json:"skipped"`
	ExcludedFiles int `json:"excluded_files"`
}

// StatusReason says why a run is incomplete. Kind is "max_functions" for a
// configured bound and "timeout" for a deadline.
type StatusReason struct {
	Kind    string `json:"kind"`
	Message string `json:"message"`
}

// Unsupported counts what was judged under an approximation, as the semantic
// dimension of the status. None of these is a failure of the analysis; each
// is a place where a clean result rests on a permissive default.
type Unsupported struct {
	// Targets are goroutine start sites whose body could not be inspected
	// (see Coverage.UnsupportedTargets).
	Targets int `json:"targets"`
	// HandedOff are cancel functions and join groups whose obligation was
	// assumed to have moved elsewhere (stored, returned, sent, captured, or
	// passed somewhere not followed) and is not judged further in the
	// function that created them. A constructor whose callers in this
	// package are checked at their own call sites (audit finding F2) is not
	// counted: that transfer is verified, not assumed.
	HandedOff int `json:"handed_off_obligations"`
	// UnestablishedPathChecks are bindings known to be discharged somewhere
	// but whose all-paths question could not be answered (the call sits in a
	// shape with no place in the function's graph, such as inside a closure
	// that may run): they are credited, not verified.
	UnestablishedPathChecks int `json:"unestablished_path_checks"`
}

// Total is the number of approximated places.
func (u Unsupported) Total() int { return u.Targets + u.HandedOff + u.UnestablishedPathChecks }

// Suppressed counts diagnostics that were found and then hidden, by the
// mechanism that hid them, and by rule. Hidden is not the same as absent.
type Suppressed struct {
	ByConfig  int            `json:"by_config"`
	ByComment int            `json:"by_comment"`
	ByRule    map[string]int `json:"by_rule,omitempty"`
}

func (s *Suppressed) note(mechanism, rule string) {
	switch mechanism {
	case "config":
		s.ByConfig++
	case "comment":
		s.ByComment++
	}
	if s.ByRule == nil {
		s.ByRule = map[string]int{}
	}
	s.ByRule[rule]++
}

// Total is the number of hidden diagnostics.
func (s Suppressed) Total() int { return s.ByConfig + s.ByComment }

// StatusOf computes the status of one package's model, before any filtering
// of its diagnostics. assumptions are the run-wide assumptions its results
// rest on.
func StatusOf(program model.Program, assumptions []string) Status {
	st := Status{
		Units: UnitStatus{
			Discovered:    program.FunctionCount,
			Analyzed:      len(program.Functions),
			ExcludedFiles: program.ExcludedFiles,
		},
		Assumptions: append([]string(nil), assumptions...),
	}
	if st.Units.Discovered > st.Units.Analyzed {
		st.Units.Skipped = st.Units.Discovered - st.Units.Analyzed
	}
	if program.Truncated {
		st.Incomplete = true
		st.Reasons = append(st.Reasons, StatusReason{
			Kind:    "max_functions",
			Message: fmt.Sprintf("analysis stopped after %d of %d functions", st.Units.Analyzed, st.Units.Discovered),
		})
	}
	for _, fn := range program.Functions {
		// A binding counts as handed off only when the hand-off is all the
		// evidence there is: Escapes is also set by an ordinary verified
		// discharge that happens to sit in a return statement
		// (`return g.Wait()`), and counting that would make a properly joined
		// group look approximated.
		for _, c := range fn.Cancels {
			switch {
			case c.Escapes && !c.Called && !verifiedHandOff(c.Evidence):
				st.Unsupported.HandedOff++
			case c.Called && c.CalledOnAllPaths == nil && !c.Escapes:
				st.Unsupported.UnestablishedPathChecks++
			}
		}
		for _, g := range fn.Groups {
			switch {
			case g.Escapes && !g.Joined && !verifiedHandOff(g.Evidence):
				st.Unsupported.HandedOff++
			case g.Joined && g.JoinedOnAllPaths == nil && !g.Escapes:
				st.Unsupported.UnestablishedPathChecks++
			}
		}
		for _, g := range fn.Goroutines {
			if _, unsupported := unsupportedReason(g); unsupported {
				st.Unsupported.Targets++
			}
		}
	}
	return st
}

// verifiedHandOff reports whether the evidence records a hand-off to callers
// that this package checks at their own call sites (recordReturnedField's
// ownership-transfer note), as opposed to one that is merely assumed.
func verifiedHandOff(evidence []model.Evidence) bool {
	for _, e := range evidence {
		if e.Kind == "ownership-transfer" && strings.Contains(e.Message, "checked at its own call site") {
			return true
		}
	}
	return false
}

// Add merges another package's status into s (units and counts add, reasons
// concatenate, incompleteness is sticky, assumptions are the union).
func (s Status) Add(o Status) Status {
	out := Status{
		Units: UnitStatus{
			Discovered:    s.Units.Discovered + o.Units.Discovered,
			Analyzed:      s.Units.Analyzed + o.Units.Analyzed,
			Skipped:       s.Units.Skipped + o.Units.Skipped,
			ExcludedFiles: s.Units.ExcludedFiles + o.Units.ExcludedFiles,
		},
		Incomplete: s.Incomplete || o.Incomplete,
		Reasons:    append(append([]StatusReason(nil), s.Reasons...), o.Reasons...),
		Unsupported: Unsupported{
			Targets:                 s.Unsupported.Targets + o.Unsupported.Targets,
			HandedOff:               s.Unsupported.HandedOff + o.Unsupported.HandedOff,
			UnestablishedPathChecks: s.Unsupported.UnestablishedPathChecks + o.Unsupported.UnestablishedPathChecks,
		},
		Suppressed: Suppressed{ByConfig: s.Suppressed.ByConfig + o.Suppressed.ByConfig, ByComment: s.Suppressed.ByComment + o.Suppressed.ByComment},
	}
	if len(s.Suppressed.ByRule)+len(o.Suppressed.ByRule) > 0 {
		out.Suppressed.ByRule = map[string]int{}
		for k, v := range s.Suppressed.ByRule {
			out.Suppressed.ByRule[k] += v
		}
		for k, v := range o.Suppressed.ByRule {
			out.Suppressed.ByRule[k] += v
		}
	}
	seen := map[string]bool{}
	for _, a := range append(append([]string(nil), s.Assumptions...), o.Assumptions...) {
		if !seen[a] {
			seen[a] = true
			out.Assumptions = append(out.Assumptions, a)
		}
	}
	sort.Strings(out.Assumptions)
	return out
}

// WithTimeout returns s marked incomplete because the deadline passed. A
// timeout is recorded whether or not its diagnostic is shown.
func (s Status) WithTimeout(message string) Status {
	s.Incomplete = true
	s.Reasons = append(append([]StatusReason(nil), s.Reasons...), StatusReason{Kind: "timeout", Message: message})
	return s
}
