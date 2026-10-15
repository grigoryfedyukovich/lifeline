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
	"github.com/gfedyukovich/lifeline/internal/model"
)

type goListError struct {
	ImportStack []string
	Pos         string
	Err         string
}

type listedPackage struct {
	Dir             string
	ImportPath      string
	Name            string
	GoFiles         []string
	CgoFiles        []string
	CompiledGoFiles []string
	TestGoFiles     []string
	XTestGoFiles    []string
	IgnoredGoFiles  []string
	DepOnly         bool
	Standard        bool
	Export          string
	Imports         []string
	ImportMap       map[string]string
	ForTest         string
	Module          *listedModule
	Error           *goListError
	DepsErrors      []goListError
}

type listedModule struct {
	Path string
	Dir  string
	Main bool
}

type loadedPackage struct {
	Fset *token.FileSet
	// Files contains every compiler input used for type checking, including
	// cmd/cgo generated support files when a package uses cgo.
	Files []*ast.File
	// AnalysisFiles is the source-corresponding subset Lifeline should inspect.
	// For cgo packages this includes the translated cgo1 file whose //line
	// directives map back to the original .go source, but excludes generated
	// _cgo_gotypes/_cgo_import helpers. For ordinary packages it equals Files.
	AnalysisFiles []*ast.File
	Pkg           *types.Package
	Info          *types.Info
}

// standaloneFact is the source-analysis counterpart of analyzer.FunctionFact.
// The standalone driver cannot use go/analysis object facts, so it records the
// same self-contained function effects after analyzing a dependency and exposes
// them to dependent packages through frontend.Input's Lookup* hooks.
//
// Facts are deliberately keyed by package path/name/receiver shape rather than
// by *types.Func identity: a dependency source package is type-checked in its
// own root universe while its importer-facing object comes from compiler export
// data, so the pointers are necessarily different even though they denote the
// same declared function.
type standaloneFact struct {
	lifecycle        model.Goroutine
	paramEffects     map[int]model.ParamEffect
	paramDoneEffects map[int]model.ParamEffect
	returnFieldSites []model.ReturnFieldSite
}

type standaloneFactStore struct {
	mu    sync.RWMutex
	facts map[string]standaloneFact
}

func newStandaloneFactStore() *standaloneFactStore {
	return &standaloneFactStore{facts: make(map[string]standaloneFact)}
}

func qualifierPath(p *types.Package) string {
	if p == nil {
		return ""
	}
	return p.Path()
}

func standaloneFactKey(fn *types.Func) string {
	if fn == nil || fn.Pkg() == nil {
		return ""
	}
	recv := ""
	if sig, ok := fn.Type().(*types.Signature); ok && sig.Recv() != nil {
		recv = types.TypeString(sig.Recv().Type(), qualifierPath)
	}
	return fn.Pkg().Path() + "\x00" + recv + "\x00" + fn.Name()
}

func cloneParamEffects(src map[int]model.ParamEffect) map[int]model.ParamEffect {
	if len(src) == 0 {
		return nil
	}
	out := make(map[int]model.ParamEffect, len(src))
	for k, v := range src {
		out[k] = v
	}
	return out
}

func (s *standaloneFactStore) recordPackage(loaded *loadedPackage, program model.Program) {
	if loaded == nil || loaded.Info == nil {
		return
	}
	byName := make(map[string]model.Function, len(program.Functions))
	for _, fn := range program.Functions {
		byName[fn.Name] = fn
	}
	pending := make(map[string]standaloneFact)
	for _, file := range loaded.AnalysisFiles {
		for _, decl := range file.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || fd.Body == nil {
				continue
			}
			obj, _ := loaded.Info.Defs[fd.Name].(*types.Func)
			if obj == nil {
				continue
			}
			fn, ok := byName[obj.FullName()]
			if !ok { // excluded by max_functions or interrupted before completion
				continue
			}
			body := fn.BodyLifecycle
			unresolved := engine.UnresolvedLoop(body.CFG)
			factLifecycle := model.Goroutine{
				InfiniteLoop:           body.InfiniteLoop,
				HasReturn:              body.HasReturn,
				ContextStop:            body.ContextStop,
				ChannelStop:            body.ChannelStop,
				ExplicitStop:           body.ExplicitStop,
				ImportedUnresolvedLoop: &unresolved,
			}
			pending[standaloneFactKey(obj)] = standaloneFact{
				lifecycle:        factLifecycle,
				paramEffects:     cloneParamEffects(fn.ParamEffects),
				paramDoneEffects: cloneParamEffects(fn.ParamDoneEffects),
				returnFieldSites: append([]model.ReturnFieldSite(nil), fn.ReturnFieldSites...),
			}
		}
	}
	if len(pending) == 0 {
		return
	}
	s.mu.Lock()
	for key, fact := range pending {
		s.facts[key] = fact
	}
	s.mu.Unlock()
}

func (s *standaloneFactStore) lookup(fn *types.Func) (standaloneFact, bool) {
	key := standaloneFactKey(fn)
	if key == "" {
		return standaloneFact{}, false
	}
	s.mu.RLock()
	fact, ok := s.facts[key]
	s.mu.RUnlock()
	return fact, ok
}

func (s *standaloneFactStore) lookupFunctionSummary(fn *types.Func) (model.Goroutine, bool) {
	fact, ok := s.lookup(fn)
	return fact.lifecycle, ok
}

func (s *standaloneFactStore) lookupParamEffects(fn *types.Func, index int) (model.ParamEffect, bool) {
	fact, ok := s.lookup(fn)
	if !ok {
		return 0, false
	}
	effect, ok := fact.paramEffects[index]
	return effect, ok
}

func (s *standaloneFactStore) lookupParamDoneEffects(fn *types.Func, index int) (model.ParamEffect, bool) {
	fact, ok := s.lookup(fn)
	if !ok {
		return 0, false
	}
	effect, ok := fact.paramDoneEffects[index]
	return effect, ok
}

func (s *standaloneFactStore) lookupReturnFieldSites(fn *types.Func) ([]model.ReturnFieldSite, bool) {
	fact, ok := s.lookup(fn)
	if !ok {
		return nil, false
	}
	return append([]model.ReturnFieldSite(nil), fact.returnFieldSites...), true
}

// packageLoader owns the run-wide token/file universe and export-data importer.
// Root packages still receive independent types.Info/maps and are type-checked
// separately; only immutable dependency packages decoded from compiler export
// data are shared.
//
// go/importer's gc importer keeps a mutable package cache and is not documented
// as safe for concurrent use. sharedExportImporter serializes cache misses while
// allowing already-imported packages to be returned without re-decoding their
// export data.
type packageLoader struct {
	fset       *token.FileSet
	imports    *sharedExportImporter
	targetArch string
	sizes      types.Sizes
}

type sharedExportImporter struct {
	mu       sync.RWMutex
	delegate types.Importer
	cache    map[string]*types.Package
}

func newSharedExportImporter(delegate types.Importer) *sharedExportImporter {
	return &sharedExportImporter{delegate: delegate, cache: make(map[string]*types.Package)}
}

func (s *sharedExportImporter) Import(path string) (*types.Package, error) {
	return s.ImportFrom(path, "", 0)
}

func (s *sharedExportImporter) ImportFrom(path, srcDir string, mode types.ImportMode) (*types.Package, error) {
	s.mu.RLock()
	pkg := s.cache[path]
	s.mu.RUnlock()
	if pkg != nil {
		return pkg, nil
	}

	// Serialize delegate access: the gc importer mutates its own package map
	// while recursively decoding export data. Recheck after taking the write
	// lock so concurrent requests for the same package decode it only once.
	s.mu.Lock()
	defer s.mu.Unlock()
	if pkg := s.cache[path]; pkg != nil {
		return pkg, nil
	}

	var (
		imported *types.Package
		err      error
	)
	if from, ok := s.delegate.(types.ImporterFrom); ok {
		imported, err = from.ImportFrom(path, srcDir, mode)
	} else {
		if mode != 0 {
			return nil, fmt.Errorf("unsupported import mode %d for %q", mode, path)
		}
		imported, err = s.delegate.Import(path)
	}
	if err == nil && imported != nil {
		s.cache[path] = imported
	}
	return imported, err
}

type mappedImporter struct {
	shared    *sharedExportImporter
	importMap map[string]string
}

func (m mappedImporter) resolve(path string) string {
	if mapped, ok := m.importMap[path]; ok {
		return mapped
	}
	return path
}

func (m mappedImporter) Import(path string) (*types.Package, error) {
	return m.shared.Import(m.resolve(path))
}

func (m mappedImporter) ImportFrom(path, srcDir string, mode types.ImportMode) (*types.Package, error) {
	// After go list's ImportMap translation the path is canonical. The custom
	// compiler-export lookup is module-aware and does not use srcDir.
	return m.shared.ImportFrom(m.resolve(path), "", mode)
}

func newPackageLoader(exports map[string]string) *packageLoader {
	// Compatibility helper for unit tests and callers that do not have a Go
	// command/build context. Production standalone entry points use
	// newTargetPackageLoader so cross-compilation follows the target GOARCH.
	loader, err := newPackageLoaderForArch(exports, runtime.GOARCH)
	if err != nil {
		panic(err) // the running Go toolchain must know its own architecture
	}
	return loader
}

func newPackageLoaderForArch(exports map[string]string, arch string) (*packageLoader, error) {
	sizes := types.SizesFor("gc", arch)
	if sizes == nil {
		return nil, fmt.Errorf("unsupported gc target architecture %q", arch)
	}
	fset := token.NewFileSet()
	lookup := func(path string) (io.ReadCloser, error) {
		file := exports[path]
		if file == "" {
			return nil, fmt.Errorf("no export data for import %q", path)
		}
		return os.Open(file)
	}
	return &packageLoader{
		fset:       fset,
		imports:    newSharedExportImporter(importer.ForCompiler(fset, "gc", lookup)),
		targetArch: arch,
		sizes:      sizes,
	}, nil
}

func targetGOARCH(ctx context.Context) (string, error) {
	cmd := exec.CommandContext(ctx, "go", "env", "GOARCH")
	out, err := cmd.Output()
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			msg := strings.TrimSpace(string(exitErr.Stderr))
			if msg != "" {
				return "", fmt.Errorf("go env GOARCH failed: %s", msg)
			}
		}
		return "", fmt.Errorf("go env GOARCH failed: %w", err)
	}
	arch := strings.TrimSpace(string(out))
	if arch == "" {
		return "", fmt.Errorf("go env GOARCH returned an empty target architecture")
	}
	return arch, nil
}

func newTargetPackageLoader(ctx context.Context, exports map[string]string) (*packageLoader, error) {
	arch, err := targetGOARCH(ctx)
	if err != nil {
		return nil, err
	}
	return newPackageLoaderForArch(exports, arch)
}

type analysisUnit struct {
	pkg        listedPackage
	rootResult int // -1 for summary-only dependencies
}

// selectAnalysisUnits adds same-main-module source dependencies to the
// requested roots as summary-only units. This is the standalone equivalent of
// go/analysis running dependency packages to make object facts available to a
// dependent analyzer invocation. Standard-library and third-party dependency
// source is intentionally not pulled into standalone analysis yet: doing that
// would turn a narrow command into a source analysis of the entire transitive
// build graph. Missing external summaries therefore retain the frontend's
// conservative unknown/transfer behavior.
func selectAnalysisUnits(packages, roots []listedPackage) []analysisUnit {
	units := make([]analysisUnit, 0, len(roots))
	seen := make(map[string]struct{}, len(roots))
	for i, root := range roots {
		units = append(units, analysisUnit{pkg: root, rootResult: i})
		seen[root.ImportPath] = struct{}{}
	}

	// go list -test synthesizes a <pkg>.test main. Never add that as a
	// summary dependency even if go list marks it DepOnly for a particular
	// invocation shape.
	harness := make(map[string]struct{})
	for _, p := range packages {
		if p.ForTest != "" {
			harness[p.ForTest+".test"] = struct{}{}
		}
	}
	for _, p := range packages {
		if !p.DepOnly || p.ForTest != "" || p.Module == nil || !p.Module.Main || len(p.GoFiles)+len(p.CgoFiles) == 0 {
			continue
		}
		if _, synthetic := harness[p.ImportPath]; synthetic {
			continue
		}
		if _, exists := seen[p.ImportPath]; exists {
			continue
		}
		units = append(units, analysisUnit{pkg: p, rootResult: -1})
		seen[p.ImportPath] = struct{}{}
	}
	return units
}

func resolvedImportPath(p listedPackage, path string) string {
	if mapped := p.ImportMap[path]; mapped != "" {
		return mapped
	}
	return path
}

// analysisLevels returns deterministic dependency-first waves. Packages in one
// wave have no selected dependency on each other and can therefore be analyzed
// concurrently; every summary needed by a later wave has been committed before
// that wave starts.
func analysisLevels(units []analysisUnit) ([][]int, error) {
	byPath := make(map[string]int, len(units))
	for i, unit := range units {
		byPath[unit.pkg.ImportPath] = i
	}
	indegree := make([]int, len(units))
	dependents := make([][]int, len(units))
	for i, unit := range units {
		seenDeps := make(map[int]struct{})
		for _, imported := range unit.pkg.Imports {
			depPath := resolvedImportPath(unit.pkg, imported)
			j, ok := byPath[depPath]
			if !ok || j == i {
				continue
			}
			if _, seen := seenDeps[j]; seen {
				continue
			}
			seenDeps[j] = struct{}{}
			indegree[i]++
			dependents[j] = append(dependents[j], i)
		}
	}

	var ready []int
	for i, degree := range indegree {
		if degree == 0 {
			ready = append(ready, i)
		}
	}
	sort.Slice(ready, func(i, j int) bool { return units[ready[i]].pkg.ImportPath < units[ready[j]].pkg.ImportPath })
	var levels [][]int
	processed := 0
	for len(ready) != 0 {
		level := append([]int(nil), ready...)
		levels = append(levels, level)
		processed += len(level)
		var next []int
		for _, i := range level {
			for _, dependent := range dependents[i] {
				indegree[dependent]--
				if indegree[dependent] == 0 {
					next = append(next, dependent)
				}
			}
		}
		sort.Slice(next, func(i, j int) bool { return units[next[i]].pkg.ImportPath < units[next[j]].pkg.ImportPath })
		ready = next
	}
	if processed != len(units) {
		return nil, fmt.Errorf("selected package graph contains an import cycle")
	}
	return levels, nil
}

func sourcePathsForAnalysis(p listedPackage, includeTests bool) []string {
	seen := make(map[string]struct{})
	var out []string
	add := func(name string) {
		if name == "" {
			return
		}
		path := name
		if !filepath.IsAbs(path) {
			path = filepath.Join(p.Dir, path)
		}
		if abs, err := filepath.Abs(path); err == nil {
			path = abs
		}
		path = filepath.Clean(path)
		if _, ok := seen[path]; ok {
			return
		}
		seen[path] = struct{}{}
		out = append(out, path)
	}
	for _, name := range p.GoFiles {
		add(name)
	}
	for _, name := range p.CgoFiles {
		add(name)
	}
	if includeTests && p.ForTest == "" {
		for _, name := range p.TestGoFiles {
			add(name)
		}
	}
	sort.Strings(out)
	return out
}

// classifySourceExclusions performs the cheapest safe filtering work before
// Lifeline parses and type-checks a package. ignore_paths can be decided from
// the path alone. Generated files need only a package-clause/comment parse;
// parser.PackageClauseOnly deliberately avoids parsing the function bodies.
//
// The returned map is keyed by canonical original source path. loadOneFiltered
// still type-checks excluded files when any source in the package remains in
// scope, because declarations from an excluded file may be required by an
// included file. If every user source is excluded, analyzePatterns can skip
// Lifeline parsing/type-checking of that package entirely.
func classifySourceExclusions(ctx context.Context, p listedPackage, includeTests bool, cfg config.Config, cwd string) (map[string]struct{}, int, error) {
	paths := sourcePathsForAnalysis(p, includeTests)
	excluded := make(map[string]struct{})
	for _, path := range paths {
		if err := ctx.Err(); err != nil {
			return nil, 0, err
		}
		rel := path
		if cwd != "" {
			if r, err := filepath.Rel(cwd, path); err == nil {
				rel = r
			}
		}
		if cfg.MatchesIgnorePath(rel) {
			excluded[path] = struct{}{}
			continue
		}
		// A package-clause-only parse is sufficient for the generated-code
		// convention because the marker must occur before the package clause.
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.PackageClauseOnly|parser.ParseComments)
		if err != nil {
			return nil, 0, fmt.Errorf("classify %s: %w", path, err)
		}
		if frontend.IsGeneratedFile(file) {
			excluded[path] = struct{}{}
		}
	}
	return excluded, len(paths), nil
}

func analyzePatterns(ctx context.Context, patterns []string, cfg config.Config) ([]engine.Diagnostic, engine.Coverage, engine.Status, error) {
	cwd, _ := os.Getwd()
	packages, err := listPackages(ctx, patterns, cfg.IncludeTests)
	if err != nil {
		return nil, engine.Coverage{}, engine.Status{}, err
	}
	exports := map[string]string{}
	for _, p := range packages {
		if p.Export != "" {
			exports[p.ImportPath] = p.Export
		}
	}
	loader, err := newTargetPackageLoader(ctx, exports)
	if err != nil {
		return nil, engine.Coverage{}, engine.Status{}, err
	}
	roots := selectAnalysisRoots(packages, cfg.IncludeTests)
	units := selectAnalysisUnits(packages, roots)
	for _, unit := range units {
		if unit.pkg.Error != nil {
			return nil, engine.Coverage{}, engine.Status{}, fmt.Errorf("package %s: %s", unit.pkg.ImportPath, unit.pkg.Error.Err)
		}
	}
	levels, err := analysisLevels(units)
	if err != nil {
		return nil, engine.Coverage{}, engine.Status{}, err
	}
	facts := newStandaloneFactStore()

	type packageResult struct {
		diagnostics []engine.Diagnostic
		coverage    engine.Coverage
		status      engine.Status
	}
	results := make([]packageResult, len(roots))
	workCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	var firstErr error
	var errOnce sync.Once
	fail := func(err error) {
		errOnce.Do(func() {
			firstErr = err
			cancel()
		})
	}

	// Analyze dependency-first waves. This is the standalone substitute for
	// go/analysis fact scheduling: once a wave completes, its source-derived
	// function summaries are immutable and visible to every dependent package
	// in later waves through the Lookup* hooks below.
	for _, level := range levels {
		if workCtx.Err() != nil {
			break
		}
		jobs := make(chan int, len(level))
		for _, unitIndex := range level {
			jobs <- unitIndex
		}
		close(jobs)

		workers := runtime.GOMAXPROCS(0)
		if workers > len(level) {
			workers = len(level)
		}
		if workers < 1 {
			workers = 1
		}
		var wg sync.WaitGroup
		for range workers {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for unitIndex := range jobs {
					if workCtx.Err() != nil {
						return
					}
					unit := units[unitIndex]
					p := unit.pkg
					// A summary-only dependency is compiled as a dependency, not as
					// a request to run that dependency's own tests. Test variants
					// selected as actual roots already carry their test files in
					// GoFiles from go list -test.
					includePackageTests := cfg.IncludeTests && unit.rootResult >= 0
					excluded, sourceCount, err := classifySourceExclusions(workCtx, p, includePackageTests, cfg, cwd)
					if err != nil {
						fail(fmt.Errorf("classify %s: %w", p.ImportPath, err))
						return
					}
					if sourceCount > 0 && len(excluded) == sourceCount {
						// No lifecycle source survives. The old path still parsed and
						// type-checked the entire package before discovering this. Preserve
						// the same empty semantic result and auditable excluded-file count
						// without doing Lifeline's own source/type work.
						if unit.rootResult >= 0 {
							program := model.Program{PackagePath: p.ImportPath, ExcludedFiles: sourceCount}
							r := &results[unit.rootResult]
							r.diagnostics, r.status = engine.AnalyzeWithStatus(program, cfg)
							r.coverage = engine.Summarize(program)
						}
						continue
					}
					loaded, err := loader.loadOneFiltered(workCtx, p, includePackageTests, excluded)
					if err != nil {
						fail(fmt.Errorf("load %s: %w", p.ImportPath, err))
						return
					}
					program, buildErr := frontend.BuildContext(workCtx, frontend.Input{
						Fset:                   loaded.Fset,
						Files:                  loaded.AnalysisFiles,
						Pkg:                    loaded.Pkg,
						Info:                   loaded.Info,
						ExcludedFiles:          len(excluded),
						LookupFunctionSummary:  facts.lookupFunctionSummary,
						LookupParamEffects:     facts.lookupParamEffects,
						LookupParamDoneEffects: facts.lookupParamDoneEffects,
						LookupReturnFieldSites: facts.lookupReturnFieldSites,
					}, cfg)

					// Program contains only functions that BuildContext completed far
					// enough to export. Recording before handling cancellation mirrors
					// analyzer.exportFunctionFacts: useful completed summaries survive,
					// while no dependent wave runs after the failure anyway.
					facts.recordPackage(loaded, program)

					if unit.rootResult >= 0 {
						r := &results[unit.rootResult]
						if buildErr == nil || program.FunctionCount > 0 || len(program.Functions) > 0 {
							r.diagnostics, r.status = engine.AnalyzeWithStatus(program, cfg)
							r.coverage = engine.Summarize(program)
						}
					}
					if buildErr != nil {
						fail(fmt.Errorf("analyze %s: %w", p.ImportPath, buildErr))
						return
					}
				}
			}()
		}
		wg.Wait()
		if firstErr != nil {
			break
		}
	}

	var all []engine.Diagnostic
	var totalCoverage engine.Coverage
	var totalStatus engine.Status
	// All requested-root results are available now, so reserve the exact
	// unsupported-target capacity once. Summary-only dependencies do not
	// contribute diagnostics/coverage to the user's report.
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

// selectAnalysisRoots converts go list's package graph into the source packages
// Lifeline should analyze. With tests disabled this is the ordinary non-dependency
// roots. With tests enabled, go list -test emits up to three records per root:
//
//   - the ordinary package,
//   - a same-package test variant (ForTest is set),
//   - an external package_test variant (ForTest is set),
//
// plus a synthetic <pkg>.test main. Prefer the same-package variant over the
// ordinary package so production files are not diagnosed twice, include the
// external test package as its own source root, and never analyze the synthetic
// test harness.
func selectAnalysisRoots(packages []listedPackage, includeTests bool) []listedPackage {
	if !includeTests {
		roots := make([]listedPackage, 0)
		for _, p := range packages {
			if p.DepOnly || p.ForTest != "" || len(p.GoFiles)+len(p.CgoFiles) == 0 {
				continue
			}
			roots = append(roots, p)
		}
		sort.Slice(roots, func(i, j int) bool { return roots[i].ImportPath < roots[j].ImportPath })
		return roots
	}

	baseByPath := make(map[string]listedPackage)
	sameTestByBase := make(map[string]listedPackage)
	var externalTests []listedPackage
	testHarnesses := make(map[string]struct{})

	// First collect ordinary roots. Variants are classified on a second pass so
	// package names are available even if go list orders records differently.
	for _, p := range packages {
		if p.DepOnly {
			continue
		}
		if p.ForTest == "" {
			baseByPath[p.ImportPath] = p
			continue
		}
		testHarnesses[p.ForTest+".test"] = struct{}{}
	}
	for _, p := range packages {
		if p.DepOnly || p.ForTest == "" || len(p.GoFiles)+len(p.CgoFiles) == 0 {
			continue
		}
		base, ok := baseByPath[p.ForTest]
		if ok && p.Name == base.Name {
			sameTestByBase[p.ForTest] = p
		} else {
			externalTests = append(externalTests, p)
		}
	}

	roots := make([]listedPackage, 0, len(baseByPath)+len(externalTests))
	for path, base := range baseByPath {
		if _, synthetic := testHarnesses[path]; synthetic && base.Name == "main" {
			continue
		}
		if len(base.GoFiles)+len(base.CgoFiles) == 0 {
			continue
		}
		if testVariant, ok := sameTestByBase[path]; ok {
			roots = append(roots, testVariant)
		} else {
			roots = append(roots, base)
		}
	}
	roots = append(roots, externalTests...)
	sort.Slice(roots, func(i, j int) bool { return roots[i].ImportPath < roots[j].ImportPath })
	return roots
}

func listPackages(ctx context.Context, patterns []string, includeTests bool) ([]listedPackage, error) {
	args := []string{"list", "-deps", "-compiled"}
	if includeTests {
		// Asking go list for the test graph is essential: same-package tests can
		// import packages that are absent from the ordinary build, and external
		// tests are compiled as a distinct package. Merely appending TestGoFiles
		// after an ordinary go list leaves those dependencies out of the export
		// map and cannot represent external tests at all.
		args = append(args, "-test")
	}
	args = append(args, "-export", "-json", "-e")
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

func stripCgoTranslationMarker(fset *token.FileSet, file *ast.File, sourcePath string) {
	if fset == nil || file == nil {
		return
	}
	sourcePath = filepath.Clean(sourcePath)
	const marker = "// Code generated by cmd/cgo; DO NOT EDIT."
	groups := file.Comments[:0]
	for _, group := range file.Comments {
		comments := group.List[:0]
		for _, comment := range group.List {
			commentPath := filepath.Clean(fset.Position(comment.Pos()).Filename)
			// cmd/cgo's own marker is emitted before the //line directive that
			// switches positions back to the user's source. A matching marker that
			// already maps to sourcePath came from the user's file and must remain.
			if strings.TrimSpace(comment.Text) == marker && commentPath != sourcePath {
				continue
			}
			comments = append(comments, comment)
		}
		group.List = comments
		if len(group.List) > 0 {
			groups = append(groups, group)
		}
	}
	file.Comments = groups
}

func loadOne(ctx context.Context, p listedPackage, exports map[string]string, includeTests bool) (*loadedPackage, error) {
	return newPackageLoader(exports).loadOne(ctx, p, includeTests)
}

func (l *packageLoader) loadOne(ctx context.Context, p listedPackage, includeTests bool) (*loadedPackage, error) {
	return l.loadOneFiltered(ctx, p, includeTests, nil)
}

func (l *packageLoader) loadOneFiltered(ctx context.Context, p listedPackage, includeTests bool, excludedSources map[string]struct{}) (*loadedPackage, error) {
	fset := l.fset
	// -compiled makes go list expose the exact Go inputs presented to the
	// compiler. That distinction matters for cgo: raw CgoFiles still import the
	// pseudo-package "C" and cannot be type-checked by go/types, while
	// CompiledGoFiles contains cmd/cgo's translated Go plus its generated type
	// declarations. Hand-written listedPackage values in unit tests may omit
	// CompiledGoFiles, so keep the historical source-file fallback.
	names := append([]string{}, p.CompiledGoFiles...)
	usingCompiledInputs := len(names) > 0
	if !usingCompiledInputs {
		names = append(names, p.GoFiles...)
		names = append(names, p.CgoFiles...)
		if includeTests && p.ForTest == "" {
			// Test variants returned by go list -test already include same-package
			// TestGoFiles in GoFiles. Only the ordinary-package fallback needs the
			// historical append behavior.
			names = append(names, p.TestGoFiles...)
		}
	}

	// The compiler inputs for a cgo package also contain generated helper files.
	// Type-check all of them, but only analyze files whose effective package
	// position maps to one of the package's original Go sources. cmd/cgo emits a
	// //line directive on its translated cgo1 file, so token.FileSet reports the
	// original CgoFiles path for that AST while helper files retain cache paths.
	sourcePaths := make(map[string]struct{}, len(p.GoFiles)+len(p.CgoFiles)+len(p.TestGoFiles)+len(p.XTestGoFiles))
	cgoSourcePaths := make(map[string]struct{}, len(p.CgoFiles))
	canonicalSourcePath := func(name string) string {
		path := name
		if !filepath.IsAbs(path) {
			path = filepath.Join(p.Dir, path)
		}
		if abs, err := filepath.Abs(path); err == nil {
			path = abs
		}
		return filepath.Clean(path)
	}
	addSource := func(name string) { sourcePaths[canonicalSourcePath(name)] = struct{}{} }
	for _, name := range p.GoFiles {
		addSource(name)
	}
	for _, name := range p.CgoFiles {
		path := canonicalSourcePath(name)
		sourcePaths[path] = struct{}{}
		cgoSourcePaths[path] = struct{}{}
	}
	if includeTests && p.ForTest == "" {
		for _, name := range p.TestGoFiles {
			addSource(name)
		}
		for _, name := range p.XTestGoFiles {
			addSource(name)
		}
	}

	var files []*ast.File
	var analysisFiles []*ast.File
	for _, name := range names {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		path := name
		if !filepath.IsAbs(path) {
			path = filepath.Join(p.Dir, path)
		}
		actual := path
		if abs, err := filepath.Abs(actual); err == nil {
			actual = abs
		}
		actual = filepath.Clean(actual)
		parseMode := parser.AllErrors
		// Type checking never consumes comments. For ordinary source files that
		// were already excluded by path/generated-marker classification, avoid
		// allocating their comment AST as well. cgo translated inputs need
		// comments until their //line mapping identifies the original source.
		if len(p.CgoFiles) > 0 || usingCompiledInputs && len(excludedSources) == 0 {
			parseMode |= parser.ParseComments
		} else if _, excluded := excludedSources[actual]; !excluded {
			parseMode |= parser.ParseComments
		}
		file, err := parser.ParseFile(fset, path, nil, parseMode)
		if err != nil {
			return nil, fmt.Errorf("%s: %w; fix syntax errors and rerun lifeline", path, err)
		}
		files = append(files, file)
		if !usingCompiledInputs || len(p.CgoFiles) == 0 {
			// Ordinary -compiled inputs are the package's real Go sources. A
			// preclassified exclusion still remains in Files for type resolution
			// but never reaches lifecycle analysis.
			if _, excluded := excludedSources[actual]; !excluded {
				analysisFiles = append(analysisFiles, file)
			}
			continue
		}
		if _, original := sourcePaths[actual]; original {
			// Non-cgo source files in a package that also uses cgo remain ordinary
			// compiler inputs; use their actual path rather than their possibly
			// rewritten token position.
			if _, excluded := excludedSources[actual]; !excluded {
				analysisFiles = append(analysisFiles, file)
			}
			continue
		}
		effective := fset.Position(file.Package).Filename
		if abs, err := filepath.Abs(effective); err == nil {
			effective = abs
		}
		effective = filepath.Clean(effective)
		if _, cgoSource := cgoSourcePaths[effective]; cgoSource {
			if _, excluded := excludedSources[effective]; excluded {
				continue
			}
			// cmd/cgo labels its translated cgo1 file as generated even though
			// that AST is the compiler-equivalent form of the user's source. Drop
			// only cmd/cgo's own marker so the translated user source remains
			// analyzable. If the original source itself carried a generated-code
			// marker, preclassification excluded it before this point.
			stripCgoTranslationMarker(fset, file, effective)
			analysisFiles = append(analysisFiles, file)
		}
	}
	var typeErrors []string
	conf := types.Config{
		Importer: mappedImporter{shared: l.imports, importMap: p.ImportMap},
		Sizes:    l.sizes,
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
	return &loadedPackage{Fset: fset, Files: files, AnalysisFiles: analysisFiles, Pkg: typed, Info: info}, nil
}
