// Package analyzer exposes Lifeline as a go/analysis analyzer.
package analyzer

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/tools/go/analysis"

	"github.com/gfedyukovich/lifeline/internal/config"
	"github.com/gfedyukovich/lifeline/internal/engine"
	"github.com/gfedyukovich/lifeline/internal/frontend"
	"github.com/gfedyukovich/lifeline/internal/model"
	"github.com/gfedyukovich/lifeline/internal/version"
)

type options struct {
	configPath   string
	maxFunctions int
	timeout      string
	ignoreRules  string
}

// FunctionFact is a versioned, conservative summary of a function body. It is
// exported for named functions and imported only for direct cross-package
// goroutine targets. Incompatible versions are ignored rather than reinterpreted.
type FunctionFact struct {
	Version      int
	InfiniteLoop bool
	HasReturn    bool
	ContextStop  bool
	ChannelStop  bool
	ExplicitStop bool
	// LoopUnresolved is this function's own CFG/SCC-derived LL1002 verdict
	// (Phase 4, docs/cfg-migration-plan.md): whether its body, analyzed as
	// a goroutine, has a reachable persistent loop with no edge reaching
	// exit. This is the same computation internal/engine/cfg_verdict.go
	// runs locally for a same-package or closure target; exporting it lets
	// a cross-package target get the same precision instead of falling
	// back to the coarser flat booleans above (see engine.go's LL1002
	// loop). The prior booleans are kept for informational/JSON value and
	// as a fallback for a fact whose Version predates this field.
	LoopUnresolved bool
}

func (*FunctionFact) AFact() {}

// New constructs an analyzer with invocation-local flag storage. This avoids
// package-level mutable option state when several analyzer instances coexist in
// tests, editors, or a multichecker process.
func New() *analysis.Analyzer {
	opts := new(options)
	a := &analysis.Analyzer{
		Name:      "lifeline",
		Doc:       "reports goroutines with unclear cancellation, ownership, or join protocols",
		FactTypes: []analysis.Fact{new(FunctionFact)},
	}
	a.Flags.StringVar(&opts.configPath, "config", "", "path to lifeline YAML, TOML, or JSON configuration")
	a.Flags.IntVar(&opts.maxFunctions, "max-functions", 0, "override the maximum number of functions analyzed per package")
	a.Flags.StringVar(&opts.timeout, "timeout", "", "override the per-package analysis timeout metadata")
	a.Flags.StringVar(&opts.ignoreRules, "ignore", "", "comma-separated rule identifiers to suppress")
	a.Run = func(pass *analysis.Pass) (any, error) { return run(pass, opts) }
	return a
}

var Analyzer = New()

func run(pass *analysis.Pass, opts *options) (any, error) {
	cfg, err := config.Load(opts.configPath)
	if err != nil {
		return nil, err
	}
	if opts.maxFunctions > 0 {
		cfg.MaxFunctions = opts.maxFunctions
	}
	if opts.timeout != "" {
		cfg.Timeout = opts.timeout
	}
	if opts.ignoreRules != "" {
		cfg.Ignore = config.SplitCSV(opts.ignoreRules)
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	lookup := func(fn *types.Func) (model.Goroutine, bool) {
		if pass.ImportObjectFact == nil || fn == nil {
			return model.Goroutine{}, false
		}
		var fact FunctionFact
		if !pass.ImportObjectFact(fn, &fact) || fact.Version != version.FactVersion {
			return model.Goroutine{}, false
		}
		loopUnresolved := fact.LoopUnresolved
		return model.Goroutine{
			InfiniteLoop: fact.InfiniteLoop,
			HasReturn:    fact.HasReturn,
			ContextStop:  fact.ContextStop,
			ChannelStop:  fact.ChannelStop,
			ExplicitStop: fact.ExplicitStop,
			// ImportedUnresolvedLoop is always populated here rather than
			// left nil, since ImportObjectFact already required an exact
			// FactVersion match above: there's no "older fact" case to
			// leave this nil for once this point is reached.
			ImportedUnresolvedLoop: &loopUnresolved,
		}, true
	}
	cwd, _ := os.Getwd()
	program, err := frontend.Build(frontend.Input{
		Fset: pass.Fset, Files: frontend.FilterFiles(pass.Fset, pass.Files, cfg, cwd), Pkg: pass.Pkg, Info: pass.TypesInfo,
		LookupFunctionSummary: lookup,
	}, cfg)
	if err != nil {
		return nil, err
	}

	exportFunctionFacts(pass, program)
	diags := engine.Analyze(program, cfg)
	files := buildFileIndex(pass.Fset, pass.Files)
	for _, d := range diags {
		ad := analysis.Diagnostic{
			Pos:      posFor(files, d.Position, false),
			End:      posFor(files, d.Position, true),
			Message:  vetMessage(d),
			Category: d.RuleID,
		}
		if d.SuggestedFix != nil {
			fix := analysis.SuggestedFix{Message: d.SuggestedFix.Message}
			valid := true
			for _, edit := range d.SuggestedFix.Edits {
				start := posFor(files, edit.Span, false)
				end := posFor(files, edit.Span, true)
				if start == token.NoPos || end == token.NoPos {
					valid = false
					break
				}
				fix.TextEdits = append(fix.TextEdits, analysis.TextEdit{Pos: start, End: end, NewText: []byte(edit.NewText)})
			}
			if valid {
				ad.SuggestedFixes = []analysis.SuggestedFix{fix}
			}
		}
		pass.Report(ad)
	}
	return nil, nil
}

func exportFunctionFacts(pass *analysis.Pass, program model.Program) {
	if pass.ExportObjectFact == nil {
		return
	}
	byName := make(map[string]model.Goroutine, len(program.Functions))
	for _, fn := range program.Functions {
		byName[fn.Name] = fn.BodyLifecycle
	}
	for _, file := range pass.Files {
		for _, decl := range file.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || fd.Body == nil {
				continue
			}
			obj, _ := pass.TypesInfo.Defs[fd.Name].(*types.Func)
			if obj == nil {
				continue
			}
			summary, ok := byName[obj.FullName()]
			if !ok { // function excluded by max_functions
				continue
			}
			pass.ExportObjectFact(obj, &FunctionFact{
				Version: version.FactVersion, InfiniteLoop: summary.InfiniteLoop, HasReturn: summary.HasReturn,
				ContextStop: summary.ContextStop, ChannelStop: summary.ChannelStop, ExplicitStop: summary.ExplicitStop,
				LoopUnresolved: engine.UnresolvedLoop(summary.CFG),
			})
		}
	}
}

func vetMessage(d engine.Diagnostic) string {
	msg := fmt.Sprintf("[%s] %s", d.RuleID, d.Message)
	if len(d.Evidence) > 0 {
		parts := make([]string, 0, len(d.Evidence))
		for _, e := range d.Evidence {
			parts = append(parts, e.Message)
		}
		msg += "; evidence: " + strings.Join(parts, "; ")
	}
	if d.Suggestion != "" {
		msg += "; action: " + d.Suggestion
	}
	return msg
}

func buildFileIndex(fset *token.FileSet, files []*ast.File) map[string]*token.File {
	out := make(map[string]*token.File, len(files))
	for _, file := range files {
		if tf := fset.File(file.Pos()); tf != nil {
			out[filepath.Clean(tf.Name())] = tf
		}
	}
	return out
}

func posFor(files map[string]*token.File, span model.Span, end bool) token.Pos {
	tf := files[filepath.Clean(span.File)]
	if tf == nil {
		return token.NoPos
	}
	offset := span.StartOffset
	if end {
		offset = span.EndOffset
	}
	if offset < 0 || offset > tf.Size() {
		return token.NoPos
	}
	return tf.Pos(offset)
}
