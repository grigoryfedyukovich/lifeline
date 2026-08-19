package standalone

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"

	"github.com/gfedyukovich/lifeline/internal/config"
	"github.com/gfedyukovich/lifeline/internal/engine"
	"github.com/gfedyukovich/lifeline/internal/frontend"
	"github.com/gfedyukovich/lifeline/internal/model"
)

// factsSchemaVersion versions -dump facts' JSON shape independently of the
// diagnostic report schema (version.ReportSchema): this is a development
// aid over an internal representation that is expected to change as later
// migration phases land, not a stable, versioned-forever contract the way
// the diagnostic report is. See docs/cfg-migration-plan.md section 8.4,
// "keep dumps versioned".
const factsSchemaVersion = "lifeline.facts/v1"

type factsFunction struct {
	SchemaVersion string        `json:"schema_version"`
	Function      string        `json:"function"`
	Workers       []workerFacts `json:"workers"`
}

type workerFacts struct {
	Start       string           `json:"start"`
	Stop        stopFacts        `json:"stop"`
	Termination terminationFacts `json:"termination"`
	Join        joinFacts        `json:"join"`
}

type stopFacts struct {
	// available reports whether any stop mechanism (a captured context,
	// currently the only kind tracked) exists in this body at all.
	Available bool `json:"available"`
	// Consumed reports whether StopCapabilityDataflow found at least one
	// block that provably reaches it (a trusted-stop edge, a return, or a
	// panic) -- not that every path does, matching the same
	// evidence-on-any-path philosophy the rest of Lifeline uses.
	Consumed bool `json:"consumed"`
}

type terminationFacts struct {
	ExitReachable  bool       `json:"exit_reachable"`
	PersistentSCCs []sccFacts `json:"persistent_sccs,omitempty"`
}

type sccFacts struct {
	Blocks   []string `json:"blocks"`
	Resolved bool     `json:"resolved"`
}

type joinFacts struct {
	// Analyzed is false for every worker as of this release: JoinObligation
	// dataflow (internal/engine/dataflow_lattices.go) exists and is tested
	// against synthetic fixtures, but is not yet wired to real WaitGroup
	// or errgroup bindings the way StopCapability is wired to real trusted-
	// stop edges. This field is honest about that rather than showing
	// fabricated data; see docs/cfg-migration-plan.md's status header.
	Analyzed bool   `json:"analyzed"`
	Note     string `json:"note,omitempty"`
}

// dumpFacts builds and renders lifecycle facts for every goroutine across
// the packages matching patterns, per docs/cfg-migration-plan.md section
// 8.2. Like dumpCFGs, this bypasses the normal diagnostic pipeline: it
// exists to make Phase 3's dataflow output inspectable and independently
// verifiable, not to report findings. It applies no ignore_paths/
// generated-file filtering, matching dumpCFGs' precedent.
func dumpFacts(ctx context.Context, patterns []string, cfg config.Config, format string, w io.Writer) error {
	cwd, _ := os.Getwd()
	packages, err := listPackages(ctx, patterns)
	if err != nil {
		return err
	}
	exports := map[string]string{}
	for _, p := range packages {
		if p.Export != "" {
			exports[p.ImportPath] = p.Export
		}
	}
	var roots []listedPackage
	for _, p := range packages {
		if p.DepOnly || p.ForTest != "" {
			continue
		}
		if p.Error != nil {
			return fmt.Errorf("package %s: %s", p.ImportPath, p.Error.Err)
		}
		if len(p.GoFiles)+len(p.CgoFiles) == 0 {
			continue
		}
		roots = append(roots, p)
	}
	sort.Slice(roots, func(i, j int) bool { return roots[i].ImportPath < roots[j].ImportPath })

	for _, p := range roots {
		if err := ctx.Err(); err != nil {
			return err
		}
		loaded, err := loadOne(p, exports, cfg.IncludeTests)
		if err != nil {
			return fmt.Errorf("load %s: %w", p.ImportPath, err)
		}
		program, err := frontend.Build(frontend.Input{Fset: loaded.Fset, Files: loaded.Files, Pkg: loaded.Pkg, Info: loaded.Info}, cfg)
		if err != nil {
			return fmt.Errorf("build %s: %w", p.ImportPath, err)
		}
		for _, fn := range program.Functions {
			if len(fn.Goroutines) == 0 {
				continue
			}
			ff := factsFunction{SchemaVersion: factsSchemaVersion, Function: fn.Name}
			for _, g := range fn.Goroutines {
				ff.Workers = append(ff.Workers, summarizeWorker(g, cwd))
			}
			if err := renderFacts(w, ff, format); err != nil {
				return err
			}
		}
	}
	return nil
}

func summarizeWorker(g model.Goroutine, cwd string) workerFacts {
	wf := workerFacts{Start: spanString(g.Span, cwd), Join: joinFacts{Analyzed: false, Note: "not yet wired to real WaitGroup/errgroup bindings"}}
	hasContext := len(g.AvailableContexts) > 0
	wf.Stop.Available = hasContext
	if g.CFG != nil {
		stop := engine.StopCapabilityDataflow(g.CFG, hasContext)
		for _, s := range stop {
			if s == engine.StopProvenConsumed {
				wf.Stop.Consumed = true
			}
		}
		term := engine.SummarizeTermination(g.CFG)
		wf.Termination.ExitReachable = term.ExitReachable
		for _, scc := range term.PersistentSCCs {
			var blocks []string
			for _, id := range scc.Blocks {
				blocks = append(blocks, fmt.Sprintf("B%d", id))
			}
			sort.Strings(blocks)
			wf.Termination.PersistentSCCs = append(wf.Termination.PersistentSCCs, sccFacts{Blocks: blocks, Resolved: scc.Resolved})
		}
	}
	return wf
}

func spanString(s model.Span, cwd string) string {
	if s.File == "" {
		return ""
	}
	file := s.File
	if cwd != "" {
		if rel, err := filepath.Rel(cwd, s.File); err == nil {
			file = rel
		}
	}
	return fmt.Sprintf("%s:%d:%d", file, s.StartLine, s.StartColumn)
}

func renderFacts(w io.Writer, ff factsFunction, format string) error {
	switch format {
	case "json":
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(ff)
	default:
		fmt.Fprintf(w, "func %s\n", ff.Function)
		for i, wk := range ff.Workers {
			fmt.Fprintf(w, "  worker %d (start %s):\n", i, wk.Start)
			fmt.Fprintf(w, "    stop: available=%v consumed=%v\n", wk.Stop.Available, wk.Stop.Consumed)
			fmt.Fprintf(w, "    termination: exit_reachable=%v\n", wk.Termination.ExitReachable)
			for _, scc := range wk.Termination.PersistentSCCs {
				fmt.Fprintf(w, "      persistent SCC %v: resolved=%v\n", scc.Blocks, scc.Resolved)
			}
			fmt.Fprintf(w, "    join: analyzed=%v", wk.Join.Analyzed)
			if wk.Join.Note != "" {
				fmt.Fprintf(w, " (%s)", wk.Join.Note)
			}
			fmt.Fprintln(w)
		}
		fmt.Fprintln(w)
		return nil
	}
}
