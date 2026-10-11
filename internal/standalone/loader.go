package standalone

import (
	"context"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"

	"github.com/gfedyukovich/lifeline/internal/config"
	"github.com/gfedyukovich/lifeline/internal/engine"
	"github.com/gfedyukovich/lifeline/internal/frontend"
)

type goListError struct {
	ImportStack []string
	Pos         string
	Err         string
}

type listedPackage struct {
	Dir            string
	ImportPath     string
	Name           string
	GoFiles        []string
	CgoFiles       []string
	TestGoFiles    []string
	XTestGoFiles   []string
	IgnoredGoFiles []string
	DepOnly        bool
	Standard       bool
	Export         string
	ImportMap      map[string]string
	ForTest        string
	Error          *goListError
	DepsErrors     []goListError
}

type loadedPackage struct {
	Fset  *token.FileSet
	Files []*ast.File
	Pkg   *types.Package
	Info  *types.Info
}

func analyzePatterns(ctx context.Context, patterns []string, cfg config.Config) ([]engine.Diagnostic, engine.Coverage, engine.Status, error) {
	cwd, _ := os.Getwd()
	packages, err := listPackages(ctx, patterns)
	if err != nil {
		return nil, engine.Coverage{}, engine.Status{}, err
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
			return nil, engine.Coverage{}, engine.Status{}, fmt.Errorf("package %s: %s", p.ImportPath, p.Error.Err)
		}
		if len(p.GoFiles)+len(p.CgoFiles) == 0 {
			continue
		}
		roots = append(roots, p)
	}
	sort.Slice(roots, func(i, j int) bool { return roots[i].ImportPath < roots[j].ImportPath })
	type packageResult struct {
		diagnostics []engine.Diagnostic
		coverage    engine.Coverage
		status      engine.Status
	}
	results := make([]packageResult, len(roots))
	workCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	jobs := make(chan int, len(roots))
	for i := range roots {
		jobs <- i
	}
	close(jobs)

	workers := runtime.GOMAXPROCS(0)
	if workers > len(roots) {
		workers = len(roots)
	}
	if workers < 1 {
		workers = 1
	}
	var wg sync.WaitGroup
	var firstErr error
	var errOnce sync.Once
	fail := func(err error) {
		errOnce.Do(func() {
			firstErr = err
			cancel()
		})
	}
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				if workCtx.Err() != nil {
					return
				}
				p := roots[i]
				loaded, err := loadOne(workCtx, p, exports, cfg.IncludeTests)
				if err != nil {
					fail(fmt.Errorf("load %s: %w", p.ImportPath, err))
					return
				}
				kept := frontend.FilterFiles(loaded.Fset, loaded.Files, cfg, cwd)
				program, err := frontend.BuildContext(workCtx, frontend.Input{Fset: loaded.Fset, Files: kept, Pkg: loaded.Pkg, Info: loaded.Info, ExcludedFiles: len(loaded.Files) - len(kept)}, cfg)
				if err != nil {
					// BuildContext may have completed some functions before a deadline.
					// Preserve those diagnostics/coverage; main adds the timeout reason to
					// the run status independently of diagnostic filtering.
					if program.FunctionCount > 0 || len(program.Functions) > 0 {
						results[i].diagnostics, results[i].status = engine.AnalyzeWithStatus(program, cfg)
						results[i].coverage = engine.Summarize(program)
					}
					fail(fmt.Errorf("analyze %s: %w", p.ImportPath, err))
					return
				}
				results[i].diagnostics, results[i].status = engine.AnalyzeWithStatus(program, cfg)
				results[i].coverage = engine.Summarize(program)
			}
		}()
	}
	wg.Wait()

	var all []engine.Diagnostic
	var totalCoverage engine.Coverage
	var totalStatus engine.Status
	// All package results are available now, so reserve the exact unsupported-
	// target capacity once. Merge then appends each package into this run-owned
	// accumulator without repeatedly copying everything accumulated so far.
	totalUnsupported := 0
	for i := range results {
		totalUnsupported += len(results[i].coverage.UnsupportedTargets)
	}
	if totalUnsupported != 0 {
		totalCoverage.UnsupportedTargets = make([]engine.UnsupportedTarget, 0, totalUnsupported)
	}
	for i := range results {
		all = append(all, results[i].diagnostics...)
		totalCoverage.Merge(results[i].coverage)
		totalStatus = totalStatus.Add(results[i].status)
	}
	if firstErr != nil {
		return all, totalCoverage, totalStatus, firstErr
	}
	if err := ctx.Err(); err != nil {
		return all, totalCoverage, totalStatus, err
	}
	return all, totalCoverage, totalStatus, nil
}

const maxGoListStderrBytes = 64 << 10

type boundedBuffer struct {
	buf       []byte
	limit     int
	truncated bool
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	remaining := b.limit - len(b.buf)
	if remaining > 0 {
		if remaining > len(p) {
			remaining = len(p)
		}
		b.buf = append(b.buf, p[:remaining]...)
	}
	if remaining < len(p) {
		b.truncated = true
	}
	return n, nil
}

func (b *boundedBuffer) String() string {
	if !b.truncated {
		return string(b.buf)
	}
	return string(b.buf) + "\n... go list stderr truncated"
}

func decodeListedPackages(r io.Reader) ([]listedPackage, error) {
	dec := json.NewDecoder(r)
	var packages []listedPackage
	for {
		var p listedPackage
		if err := dec.Decode(&p); err != nil {
			if err == io.EOF {
				return packages, nil
			}
			return nil, fmt.Errorf("decode go list output: %w", err)
		}
		packages = append(packages, p)
	}
}

func listPackages(ctx context.Context, patterns []string) ([]listedPackage, error) {
	args := []string{"list", "-deps", "-export", "-json", "-e"}
	args = append(args, patterns...)

	// Decode directly from go list's stdout pipe instead of first retaining the
	// complete JSON stream in memory. A private child context lets a malformed
	// stream terminate the subprocess promptly while preserving the caller's
	// cancellation reason when the outer context expires.
	cmdCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd := exec.CommandContext(cmdCtx, "go", args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("open go list stdout: %w", err)
	}
	stderr := &boundedBuffer{limit: maxGoListStderrBytes}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start go list: %w", err)
	}

	packages, decodeErr := decodeListedPackages(stdout)
	if decodeErr != nil {
		cancel()
	}
	waitErr := cmd.Wait()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if decodeErr != nil {
		return nil, decodeErr
	}
	if waitErr != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = waitErr.Error()
		}
		return nil, fmt.Errorf("go list failed: %s", msg)
	}
	return packages, nil
}

func loadOne(ctx context.Context, p listedPackage, exports map[string]string, includeTests bool) (*loadedPackage, error) {
	fset := token.NewFileSet()
	names := append([]string{}, p.GoFiles...)
	names = append(names, p.CgoFiles...)
	if includeTests {
		names = append(names, p.TestGoFiles...)
	}
	var files []*ast.File
	for _, name := range names {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		path := filepath.Join(p.Dir, name)
		file, err := parser.ParseFile(fset, path, nil, parser.ParseComments|parser.AllErrors)
		if err != nil {
			return nil, fmt.Errorf("%s: %w; fix syntax errors and rerun lifeline", path, err)
		}
		files = append(files, file)
	}
	lookup := func(path string) (io.ReadCloser, error) {
		resolved := path
		if mapped, ok := p.ImportMap[path]; ok {
			resolved = mapped
		}
		file := exports[resolved]
		if file == "" {
			return nil, fmt.Errorf("no export data for import %q", path)
		}
		return os.Open(file)
	}
	imp := importer.ForCompiler(fset, "gc", lookup)
	var typeErrors []string
	conf := types.Config{
		Importer: imp,
		Sizes:    types.SizesFor("gc", runtime.GOARCH),
		Error: func(err error) {
			typeErrors = append(typeErrors, err.Error())
		},
	}
	info := &types.Info{
		Types:      make(map[ast.Expr]types.TypeAndValue),
		Defs:       make(map[*ast.Ident]types.Object),
		Uses:       make(map[*ast.Ident]types.Object),
		Implicits:  make(map[ast.Node]types.Object),
		Selections: make(map[*ast.SelectorExpr]*types.Selection),
		Scopes:     make(map[ast.Node]*types.Scope),
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	typed, err := conf.Check(p.ImportPath, fset, files, info)
	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, ctxErr
	}
	if err != nil {
		if len(typeErrors) > 5 {
			typeErrors = append(typeErrors[:5], fmt.Sprintf("... %d more type errors", len(typeErrors)-5))
		}
		return nil, fmt.Errorf("type checking failed:\n  %s\nfix type errors and rerun lifeline", strings.Join(typeErrors, "\n  "))
	}
	return &loadedPackage{Fset: fset, Files: files, Pkg: typed, Info: info}, nil
}
