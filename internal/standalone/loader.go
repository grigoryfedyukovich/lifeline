package standalone

import (
	"bytes"
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

func analyzePatterns(ctx context.Context, patterns []string, cfg config.Config) ([]engine.Diagnostic, engine.Coverage, error) {
	packages, err := listPackages(ctx, patterns)
	if err != nil {
		return nil, engine.Coverage{}, err
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
			return nil, engine.Coverage{}, fmt.Errorf("package %s: %s", p.ImportPath, p.Error.Err)
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
				loaded, err := loadOne(p, exports, cfg.IncludeTests)
				if err != nil {
					fail(fmt.Errorf("load %s: %w", p.ImportPath, err))
					return
				}
				program, err := frontend.Build(frontend.Input{Fset: loaded.Fset, Files: loaded.Files, Pkg: loaded.Pkg, Info: loaded.Info}, cfg)
				if err != nil {
					fail(fmt.Errorf("analyze %s: %w", p.ImportPath, err))
					return
				}
				results[i].diagnostics = engine.Analyze(program, cfg)
				results[i].coverage = engine.Summarize(program)
			}
		}()
	}
	wg.Wait()

	var all []engine.Diagnostic
	var totalCoverage engine.Coverage
	for i := range results {
		all = append(all, results[i].diagnostics...)
		totalCoverage = totalCoverage.Add(results[i].coverage)
	}
	if firstErr != nil {
		return all, totalCoverage, firstErr
	}
	if err := ctx.Err(); err != nil {
		return all, totalCoverage, err
	}
	return all, totalCoverage, nil
}

func listPackages(ctx context.Context, patterns []string) ([]listedPackage, error) {
	args := []string{"list", "-deps", "-export", "-json", "-e"}
	args = append(args, patterns...)
	cmd := exec.CommandContext(ctx, "go", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	dec := json.NewDecoder(bytes.NewReader(stdout.Bytes()))
	var packages []listedPackage
	for {
		var p listedPackage
		if decErr := dec.Decode(&p); decErr != nil {
			if decErr == io.EOF {
				break
			}
			return nil, fmt.Errorf("decode go list output: %w", decErr)
		}
		packages = append(packages, p)
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return nil, fmt.Errorf("go list failed: %s", msg)
	}
	return packages, nil
}

func loadOne(p listedPackage, exports map[string]string, includeTests bool) (*loadedPackage, error) {
	fset := token.NewFileSet()
	names := append([]string{}, p.GoFiles...)
	names = append(names, p.CgoFiles...)
	if includeTests {
		names = append(names, p.TestGoFiles...)
	}
	var files []*ast.File
	for _, name := range names {
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
	typed, err := conf.Check(p.ImportPath, fset, files, info)
	if err != nil {
		if len(typeErrors) > 5 {
			typeErrors = append(typeErrors[:5], fmt.Sprintf("... %d more type errors", len(typeErrors)-5))
		}
		return nil, fmt.Errorf("type checking failed:\n  %s\nfix type errors and rerun lifeline", strings.Join(typeErrors, "\n  "))
	}
	return &loadedPackage{Fset: fset, Files: files, Pkg: typed, Info: info}, nil
}
