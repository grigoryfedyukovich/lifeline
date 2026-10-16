package standalone

import (
	"context"
	"errors"
	"fmt"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/gfedyukovich/lifeline/internal/config"
	"github.com/gfedyukovich/lifeline/internal/frontend"
)

func TestLoadOneHonorsCanceledContextBeforeParsing(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "p.go"), []byte("package p\nfunc F() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := loadOne(ctx, listedPackage{Dir: dir, ImportPath: "example.test/p", GoFiles: []string{"p.go"}}, nil, false)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("loadOne error = %v, want context.Canceled", err)
	}
}

func TestDecodeListedPackagesStreamsMultipleObjects(t *testing.T) {
	input := strings.NewReader(`
{"ImportPath":"example.test/dep","DepOnly":true,"Export":"/tmp/dep.a"}
{"ImportPath":"example.test/root","GoFiles":["root.go"],"ImportMap":{"dep":"example.test/dep"}}
`)
	got, err := decodeListedPackages(input)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("decoded %d packages, want 2", len(got))
	}
	if got[0].ImportPath != "example.test/dep" || !got[0].DepOnly {
		t.Fatalf("first package = %#v", got[0])
	}
	if got[1].ImportPath != "example.test/root" || got[1].ImportMap["dep"] != "example.test/dep" {
		t.Fatalf("second package = %#v", got[1])
	}
}

func TestBoundedBufferCapsRetainedOutput(t *testing.T) {
	var b boundedBuffer
	b.limit = 8
	if n, err := b.Write([]byte("123456")); err != nil || n != 6 {
		t.Fatalf("first Write = (%d, %v), want (6, nil)", n, err)
	}
	if n, err := b.Write([]byte("7890")); err != nil || n != 4 {
		t.Fatalf("second Write = (%d, %v), want (4, nil)", n, err)
	}
	if got := string(b.buf); got != "12345678" {
		t.Fatalf("retained stderr = %q, want %q", got, "12345678")
	}
	if !b.truncated {
		t.Fatal("bounded buffer did not record truncation")
	}
	if !strings.Contains(b.String(), "truncated") {
		t.Fatalf("String() = %q, want truncation marker", b.String())
	}
}

func TestListPackagesHonorsCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := listPackages(ctx, []string{"."}, false)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("listPackages error = %v, want context.Canceled", err)
	}
}

type countingImporter struct {
	mu       sync.Mutex
	calls    map[string]int
	packages map[string]*types.Package
}

func newCountingImporter() *countingImporter {
	return &countingImporter{
		calls:    make(map[string]int),
		packages: make(map[string]*types.Package),
	}
}

func (c *countingImporter) Import(path string) (*types.Package, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls[path]++
	if pkg := c.packages[path]; pkg != nil {
		return pkg, nil
	}
	name := path
	if i := strings.LastIndexByte(name, '/'); i >= 0 {
		name = name[i+1:]
	}
	pkg := types.NewPackage(path, name)
	pkg.MarkComplete()
	c.packages[path] = pkg
	return pkg, nil
}

func TestSharedExportImporterCachesAcrossMappedRoots(t *testing.T) {
	delegate := newCountingImporter()
	shared := newSharedExportImporter(delegate)
	left := mappedImporter{shared: shared, importMap: map[string]string{"dep": "example.test/dep"}}
	right := mappedImporter{shared: shared, importMap: map[string]string{"alias": "example.test/dep"}}

	const n = 16
	got := make(chan *types.Package, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			imp := left
			path := "dep"
			if i%2 != 0 {
				imp = right
				path = "alias"
			}
			pkg, err := imp.Import(path)
			if err != nil {
				t.Errorf("Import(%q): %v", path, err)
				return
			}
			got <- pkg
		}(i)
	}
	wg.Wait()
	close(got)

	var first *types.Package
	for pkg := range got {
		if first == nil {
			first = pkg
			continue
		}
		if pkg != first {
			t.Fatalf("mapped roots received different package objects: %p and %p", first, pkg)
		}
	}
	if first == nil || first.Path() != "example.test/dep" {
		t.Fatalf("imported package = %#v, want example.test/dep", first)
	}
	delegate.mu.Lock()
	calls := delegate.calls["example.test/dep"]
	delegate.mu.Unlock()
	if calls != 1 {
		t.Fatalf("delegate imports for example.test/dep = %d, want 1", calls)
	}
}

func TestPackageLoaderReusesRunFileSet(t *testing.T) {
	loader := newPackageLoader(nil)
	ctx := context.Background()
	var loaded []*loadedPackage
	for _, name := range []string{"a", "b"} {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, name+".go"), []byte("package "+name+"\nfunc F() {}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		pkg, err := loader.loadOne(ctx, listedPackage{
			Dir:        dir,
			ImportPath: "example.test/" + name,
			GoFiles:    []string{name + ".go"},
		}, false)
		if err != nil {
			t.Fatalf("load %s: %v", name, err)
		}
		loaded = append(loaded, pkg)
	}
	if loaded[0].Fset != loaded[1].Fset || loaded[0].Fset != loader.fset {
		t.Fatal("package loader did not reuse its run-wide token.FileSet")
	}
}

func TestPackageLoaderConcurrentRootParsing(t *testing.T) {
	loader := newPackageLoader(nil)
	ctx := context.Background()
	const n = 12
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			dir := t.TempDir()
			name := fmt.Sprintf("p%d", i)
			fileName := name + ".go"
			if err := os.WriteFile(filepath.Join(dir, fileName), []byte("package "+name+"\nfunc F() int { return "+fmt.Sprint(i)+" }\n"), 0o644); err != nil {
				errs <- err
				return
			}
			loaded, err := loader.loadOne(ctx, listedPackage{
				Dir:        dir,
				ImportPath: "example.test/" + name,
				GoFiles:    []string{fileName},
			}, false)
			if err != nil {
				errs <- err
				return
			}
			if loaded.Fset != loader.fset {
				errs <- fmt.Errorf("%s received a different FileSet", name)
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestSelectAnalysisRootsWithTestsPrefersVariantAndExternalPackage(t *testing.T) {
	packages := []listedPackage{
		{ImportPath: "example.test/p", Name: "p", GoFiles: []string{"p.go"}},
		{ImportPath: "example.test/p [example.test/p.test]", ForTest: "example.test/p", Name: "p", GoFiles: []string{"p.go", "p_test.go"}},
		{ImportPath: "example.test/p_test [example.test/p.test]", ForTest: "example.test/p", Name: "p_test", GoFiles: []string{"x_test.go"}},
		{ImportPath: "example.test/p.test", Name: "main", GoFiles: []string{"generated.go"}},
		{ImportPath: "example.test/dep", Name: "dep", GoFiles: []string{"dep.go"}, DepOnly: true},
	}

	roots := selectAnalysisRoots(packages, true)
	if len(roots) != 2 {
		t.Fatalf("selectAnalysisRoots returned %d roots, want 2: %#v", len(roots), roots)
	}
	if roots[0].Name != "p" || roots[0].ForTest != "example.test/p" {
		t.Fatalf("first root = %#v, want same-package test variant", roots[0])
	}
	if roots[1].Name != "p_test" || roots[1].ForTest != "example.test/p" {
		t.Fatalf("second root = %#v, want external test package", roots[1])
	}
	for _, root := range roots {
		if root.ImportPath == "example.test/p" || root.ImportPath == "example.test/p.test" {
			t.Fatalf("selected duplicate/base or synthetic test harness: %#v", root)
		}
	}
}

func TestListPackagesWithTestsLoadsTestOnlyDependenciesAndExternalTests(t *testing.T) {
	dir := t.TempDir()
	write := func(name, contents string) {
		t.Helper()
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module example.test/lifeline_testgraph\n\ngo 1.20\n")
	write("dep/dep.go", "package dep\nfunc Production() {}\n")
	write("testdep/testdep.go", "package testdep\nfunc OnlyInTests() {}\n")
	write("p/p.go", `package p
import "example.test/lifeline_testgraph/dep"
func Production() { dep.Production() }
`)
	write("p/p_test.go", `package p
import "example.test/lifeline_testgraph/testdep"
func samePackageTestHelper() { testdep.OnlyInTests() }
`)
	write("p/x_test.go", `package p_test
import (
	p "example.test/lifeline_testgraph/p"
	"example.test/lifeline_testgraph/testdep"
)
func externalTestHelper() { p.Production(); testdep.OnlyInTests() }
`)

	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := os.Chdir(oldwd); err != nil {
			t.Errorf("restore cwd: %v", err)
		}
	}()

	packages, err := listPackages(context.Background(), []string{"./p"}, true)
	if err != nil {
		t.Fatalf("listPackages(-test): %v", err)
	}

	const testDep = "example.test/lifeline_testgraph/testdep"
	foundTestDep := false
	exports := make(map[string]string)
	for _, p := range packages {
		if p.ImportPath == testDep {
			foundTestDep = true
		}
		if p.Export != "" {
			exports[p.ImportPath] = p.Export
		}
	}
	if !foundTestDep {
		t.Fatalf("go list -test graph omitted test-only dependency %q", testDep)
	}

	roots := selectAnalysisRoots(packages, true)
	if len(roots) != 2 {
		var ids []string
		for _, p := range roots {
			ids = append(ids, p.ImportPath)
		}
		t.Fatalf("selected roots = %v, want same-package and external test roots", ids)
	}

	loader := newPackageLoader(exports)
	seenNames := make(map[string]bool)
	for _, root := range roots {
		if strings.HasSuffix(root.ImportPath, ".test") && root.Name == "main" {
			t.Fatalf("selected synthetic test harness %q", root.ImportPath)
		}
		loaded, err := loader.loadOne(context.Background(), root, true)
		if err != nil {
			t.Fatalf("load test root %q: %v", root.ImportPath, err)
		}
		seenNames[loaded.Pkg.Name()] = true
	}
	if !seenNames["p"] || !seenNames["p_test"] {
		t.Fatalf("loaded package names = %v, want p and p_test", seenNames)
	}
}

func requireCgoToolchain(t *testing.T) {
	t.Helper()
	out, err := exec.Command("go", "env", "CGO_ENABLED").Output()
	if err != nil {
		t.Skipf("go env CGO_ENABLED unavailable: %v", err)
	}
	if strings.TrimSpace(string(out)) != "1" {
		t.Skip("cgo is disabled for this Go environment")
	}
	cc, err := exec.Command("go", "env", "CC").Output()
	if err != nil || strings.TrimSpace(string(cc)) == "" {
		t.Skipf("cgo C compiler is unavailable: %v", err)
	}
	if _, err := exec.LookPath(strings.TrimSpace(string(cc))); err != nil {
		t.Skipf("cgo C compiler %q is unavailable: %v", strings.TrimSpace(string(cc)), err)
	}
}

func TestListPackagesCgoUsesCompiledInputsAndAnalyzesTranslatedSource(t *testing.T) {
	requireCgoToolchain(t)
	dir := t.TempDir()
	write := func(name, contents string) {
		t.Helper()
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module example.test/lifeline_cgo\n\ngo 1.20\n")
	write("p/cgo.go", `package p
/*
#include <stdlib.h>
*/
import "C"
import "context"

func Leak(parent context.Context) {
	_, cancel := context.WithCancel(parent)
	_ = cancel
	_ = C.sizeof_int
}
`)

	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := os.Chdir(oldwd); err != nil {
			t.Errorf("restore cwd: %v", err)
		}
	}()

	packages, err := listPackages(context.Background(), []string{"./p"}, false)
	if err != nil {
		t.Fatalf("listPackages(cgo): %v", err)
	}
	roots := selectAnalysisRoots(packages, false)
	if len(roots) != 1 {
		t.Fatalf("selected roots = %d, want 1", len(roots))
	}
	root := roots[0]
	if len(root.CgoFiles) == 0 {
		t.Fatalf("go list did not report CgoFiles: %#v", root)
	}
	if len(root.CompiledGoFiles) <= len(root.CgoFiles) {
		t.Fatalf("CompiledGoFiles = %v, want cmd/cgo-generated compiler inputs", root.CompiledGoFiles)
	}

	exports := make(map[string]string)
	for _, pkg := range packages {
		if pkg.Export != "" {
			exports[pkg.ImportPath] = pkg.Export
		}
	}
	loader := newPackageLoader(exports)
	loaded, err := loader.loadOne(context.Background(), root, false)
	if err != nil {
		t.Fatalf("load cgo root from CompiledGoFiles: %v", err)
	}
	if len(loaded.Files) <= len(loaded.AnalysisFiles) {
		t.Fatalf("type-check files = %d, analysis files = %d; want generated cgo helpers excluded from analysis", len(loaded.Files), len(loaded.AnalysisFiles))
	}
	if len(loaded.AnalysisFiles) != 1 {
		t.Fatalf("analysis files = %d, want translated user cgo source only", len(loaded.AnalysisFiles))
	}
	if got := filepath.Clean(loaded.Fset.Position(loaded.AnalysisFiles[0].Package).Filename); got != filepath.Join(dir, "p", "cgo.go") {
		t.Fatalf("translated cgo source maps to %q, want original cgo.go", got)
	}
	if frontend.IsGeneratedFile(loaded.AnalysisFiles[0]) {
		t.Fatal("cmd/cgo translation marker was not removed from analysis source")
	}

	cfg := config.Default()
	cfg.Timeout = "30s"
	diags, _, _, err := analyzePatterns(context.Background(), []string{"./p"}, cfg)
	if err != nil {
		t.Fatalf("analyzePatterns(cgo): %v", err)
	}
	for _, d := range diags {
		if d.RuleID == "LL1001" && strings.HasSuffix(d.Function, ".Leak") {
			return
		}
	}
	t.Fatalf("translated cgo source was not lifecycle-analyzed; diagnostics = %#v", diags)
}

func TestStripCgoTranslationMarkerPreservesSourceGeneratedMarker(t *testing.T) {
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "cgo.go")
	cachePath := filepath.Join(dir, "cgo1.generated")
	fset := token.NewFileSet()
	text := fmt.Sprintf(`// Code generated by cmd/cgo; DO NOT EDIT.

//line %s:1:1
// Code generated by user-tool; DO NOT EDIT.
package p
`, sourcePath)
	file, err := parser.ParseFile(fset, cachePath, text, parser.ParseComments)
	if err != nil {
		t.Fatal(err)
	}
	stripCgoTranslationMarker(fset, file, sourcePath)
	if !frontend.IsGeneratedFile(file) {
		t.Fatal("removing cmd/cgo marker also removed the source's generated-code marker")
	}
	for _, group := range file.Comments {
		for _, comment := range group.List {
			if strings.TrimSpace(comment.Text) == "// Code generated by cmd/cgo; DO NOT EDIT." && filepath.Clean(fset.Position(comment.Pos()).Filename) != sourcePath {
				t.Fatal("cmd/cgo translation marker remains")
			}
		}
	}
}

func TestAnalyzePatternsUsesSameModuleDependencyFacts(t *testing.T) {
	dir := t.TempDir()
	write := func(name, contents string) {
		t.Helper()
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module example.test/lifeline_facts\n\ngo 1.20\n")
	write("dep/dep.go", `package dep
import "context"

// Drop receives ownership-looking input but demonstrably does nothing with it.
// Without a cross-package effect summary the caller conservatively treats this
// as an opaque transfer and misses the leak.
func Drop(cancel context.CancelFunc) { _ = cancel }
`)
	write("root/root.go", `package root
import (
	"context"
	"example.test/lifeline_facts/dep"
)

func Leak(parent context.Context) {
	_, cancel := context.WithCancel(parent)
	dep.Drop(cancel)
}
`)

	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := os.Chdir(oldwd); err != nil {
			t.Errorf("restore cwd: %v", err)
		}
	}()

	cfg := config.Default()
	cfg.Timeout = "30s"
	diags, _, _, err := analyzePatterns(context.Background(), []string{"./root"}, cfg)
	if err != nil {
		t.Fatalf("analyzePatterns: %v", err)
	}
	for _, d := range diags {
		if d.RuleID == "LL1001" && strings.HasSuffix(d.Function, ".Leak") {
			return
		}
	}
	t.Fatalf("cross-package no-consume fact did not preserve caller obligation; diagnostics = %#v", diags)
}

func TestSelectAnalysisUnitsIncludesDirectStandardLibrarySourceOnly(t *testing.T) {
	roots := []listedPackage{{ImportPath: "example.test/root", GoFiles: []string{"root.go"}, Imports: []string{"context", "example.test/localdep"}}}
	packages := []listedPackage{
		{ImportPath: "example.test/root", GoFiles: []string{"root.go"}, Imports: []string{"context", "example.test/localdep"}},
		{ImportPath: "example.test/localdep", DepOnly: true, GoFiles: []string{"local.go"}, Module: &listedModule{Main: true}, Imports: []string{"strings"}},
		{ImportPath: "third.example/dep", DepOnly: true, GoFiles: []string{"dep.go"}, Module: &listedModule{Path: "third.example/dep"}},
		{ImportPath: "context", DepOnly: true, Standard: true, GoFiles: []string{"context.go"}, Imports: []string{"sync"}},
		{ImportPath: "strings", DepOnly: true, Standard: true, GoFiles: []string{"strings.go"}},
		{ImportPath: "sync", DepOnly: true, Standard: true, GoFiles: []string{"mutex.go"}},
	}
	units := selectAnalysisUnits(packages, roots)
	got := make(map[string]int)
	for _, unit := range units {
		got[unit.pkg.ImportPath] = unit.rootResult
	}
	if got["example.test/root"] != 0 {
		t.Fatalf("root result = %d, want 0", got["example.test/root"])
	}
	if got["example.test/localdep"] != -1 {
		t.Fatalf("same-module dependency result = %d, want summary-only", got["example.test/localdep"])
	}
	if got["third.example/dep"] != -1 {
		t.Fatalf("third-party dependency result = %d, want summary-only", got["third.example/dep"])
	}
	if got["context"] != -1 {
		t.Fatalf("direct standard-library dependency result = %d, want summary-only", got["context"])
	}
	if _, ok := got["strings"]; ok {
		t.Fatal("standard library imported only by a summary dependency should remain export-data-only")
	}
	if _, ok := got["sync"]; ok {
		t.Fatal("transitive standard-library dependency should remain export-data-only")
	}
}

func TestAnalyzePatternsUsesThirdPartyModuleDependencyFacts(t *testing.T) {
	workspace := t.TempDir()
	rootDir := filepath.Join(workspace, "rootmod")
	depDir := filepath.Join(workspace, "depmod")
	write := func(path, contents string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(depDir, "go.mod"), "module third.example/dep\n\ngo 1.20\n")
	write(filepath.Join(depDir, "dep.go"), `package dep
import "context"

// Drop is intentionally a no-op. The caller must retain its own obligation.
func Drop(cancel context.CancelFunc) { _ = cancel }
`)
	write(filepath.Join(rootDir, "go.mod"), `module example.test/rootmod

go 1.20

require third.example/dep v0.0.0
replace third.example/dep => ../depmod
`)
	write(filepath.Join(rootDir, "root", "root.go"), `package root
import (
	"context"
	"third.example/dep"
)

func Leak(parent context.Context) {
	_, cancel := context.WithCancel(parent)
	dep.Drop(cancel)
}
`)

	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(rootDir); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := os.Chdir(oldwd); err != nil {
			t.Errorf("restore cwd: %v", err)
		}
	}()

	packages, err := listPackages(context.Background(), []string{"./root"}, false)
	if err != nil {
		t.Fatalf("listPackages: %v", err)
	}
	var thirdParty listedPackage
	for _, pkg := range packages {
		if pkg.ImportPath == "third.example/dep" {
			thirdParty = pkg
			break
		}
	}
	if thirdParty.ImportPath == "" {
		t.Fatal("third-party dependency missing from go list graph")
	}
	if thirdParty.Standard || thirdParty.Module == nil || thirdParty.Module.Main {
		t.Fatalf("third-party package metadata = %#v, want non-standard non-main module", thirdParty)
	}

	cfg := config.Default()
	cfg.Timeout = "30s"
	diags, _, _, err := analyzePatterns(context.Background(), []string{"./root"}, cfg)
	if err != nil {
		t.Fatalf("analyzePatterns: %v", err)
	}
	for _, d := range diags {
		if d.RuleID == "LL1001" && strings.HasSuffix(d.Function, ".Leak") {
			return
		}
	}
	t.Fatalf("third-party no-consume fact did not preserve caller obligation; diagnostics = %#v", diags)
}

func TestAnalyzePatternsUsesDirectStandardLibraryFunctionSummary(t *testing.T) {
	dir := t.TempDir()
	write := func(name, contents string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(contents), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module example.test/stdsummary\n\ngo 1.20\n")
	write("root.go", `package stdsummary
import "strings"

func Start() {
	go strings.Clone("value")
}
`)

	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := os.Chdir(oldwd); err != nil {
			t.Errorf("restore cwd: %v", err)
		}
	}()

	cfg := config.Default()
	cfg.Timeout = "30s"
	diags, coverage, _, err := analyzePatterns(context.Background(), []string{"."}, cfg)
	if err != nil {
		t.Fatalf("analyzePatterns: %v", err)
	}
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics = %#v", diags)
	}
	for _, target := range coverage.UnsupportedTargets {
		if strings.Contains(target.Message, "no compatible fact") || strings.Contains(target.Message, "not locally available") {
			t.Fatalf("direct standard-library goroutine target remained unsupported: %#v", target)
		}
	}
}

func TestAnalysisLevelsPutDependencyBeforeCaller(t *testing.T) {
	units := []analysisUnit{
		{pkg: listedPackage{ImportPath: "example.test/root", Imports: []string{"example.test/dep"}}, rootResult: 0},
		{pkg: listedPackage{ImportPath: "example.test/dep"}, rootResult: -1},
	}
	levels, err := analysisLevels(units)
	if err != nil {
		t.Fatal(err)
	}
	if len(levels) != 2 || len(levels[0]) != 1 || len(levels[1]) != 1 || levels[0][0] != 1 || levels[1][0] != 0 {
		t.Fatalf("analysis levels = %#v, want dep then root", levels)
	}
}

func TestPackageLoaderUsesConfiguredTargetSizes(t *testing.T) {
	hostSizes := types.SizesFor("gc", runtime.GOARCH)
	if hostSizes == nil {
		t.Skipf("gc sizes unavailable for host architecture %q", runtime.GOARCH)
	}
	hostPtr := hostSizes.Sizeof(types.Typ[types.Uintptr])
	targetArch := "386"
	targetPtr := int64(4)
	if hostPtr == targetPtr {
		targetArch = "amd64"
		targetPtr = 8
	}
	if hostPtr == targetPtr {
		t.Skipf("could not choose target with pointer width different from host %q", runtime.GOARCH)
	}

	dir := t.TempDir()
	source := fmt.Sprintf(`package p
import "unsafe"
var _ [%d]byte = [unsafe.Sizeof(uintptr(0))]byte{}
`, targetPtr)
	if err := os.WriteFile(filepath.Join(dir, "p.go"), []byte(source), 0o644); err != nil {
		t.Fatal(err)
	}
	pkg := listedPackage{Dir: dir, ImportPath: "example.test/targetsizes", GoFiles: []string{"p.go"}}

	targetLoader, err := newPackageLoaderForArch(nil, targetArch)
	if err != nil {
		t.Fatalf("newPackageLoaderForArch(%q): %v", targetArch, err)
	}
	if targetLoader.targetArch != targetArch {
		t.Fatalf("loader targetArch = %q, want %q", targetLoader.targetArch, targetArch)
	}
	if _, err := targetLoader.loadOne(context.Background(), pkg, false); err != nil {
		t.Fatalf("target %s should type-check target-sized unsafe.Sizeof expression: %v", targetArch, err)
	}

	hostLoader, err := newPackageLoaderForArch(nil, runtime.GOARCH)
	if err != nil {
		t.Fatalf("newPackageLoaderForArch(host %q): %v", runtime.GOARCH, err)
	}
	if _, err := hostLoader.loadOne(context.Background(), pkg, false); err == nil {
		t.Fatalf("host-sized loader unexpectedly accepted source requiring %d-byte uintptr target; host uintptr is %d", targetPtr, hostPtr)
	}
}

func TestTargetGOARCHUsesGoBuildEnvironment(t *testing.T) {
	target := "386"
	if runtime.GOARCH == target {
		target = "amd64"
	}
	t.Setenv("GOARCH", target)
	got, err := targetGOARCH(context.Background())
	if err != nil {
		t.Fatalf("targetGOARCH: %v", err)
	}
	if got != target {
		t.Fatalf("targetGOARCH = %q, want %q", got, target)
	}
	loader, err := newTargetPackageLoader(context.Background(), nil)
	if err != nil {
		t.Fatalf("newTargetPackageLoader: %v", err)
	}
	if loader.targetArch != target {
		t.Fatalf("loader targetArch = %q, want go env target %q", loader.targetArch, target)
	}
}

func TestClassifySourceExclusionsBeforeFullParse(t *testing.T) {
	dir := t.TempDir()
	write := func(name, contents string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(contents), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("keep.go", "package p\nfunc Keep() {}\n")
	// This file is intentionally not valid Go. Path exclusion must classify it
	// without asking the full parser to inspect its body.
	write("ignored.pb.go", "this is intentionally not Go\n")
	// PackageClauseOnly stops after the package clause, so a generated file can
	// also be classified without parsing its invalid body.
	write("generated.go", "// Code generated by test; DO NOT EDIT.\npackage p\nfunc broken(\n")

	cfg := config.Default()
	cfg.IgnorePaths = []string{"*.pb.go"}
	excluded, total, err := classifySourceExclusions(context.Background(), listedPackage{
		Dir:        dir,
		ImportPath: "example.test/p",
		GoFiles:    []string{"keep.go", "ignored.pb.go", "generated.go"},
	}, false, cfg, dir)
	if err != nil {
		t.Fatalf("classifySourceExclusions: %v", err)
	}
	if total != 3 || len(excluded) != 2 {
		t.Fatalf("total=%d excluded=%d, want 3 and 2", total, len(excluded))
	}
	for _, name := range []string{"ignored.pb.go", "generated.go"} {
		path, _ := filepath.Abs(filepath.Join(dir, name))
		if _, ok := excluded[filepath.Clean(path)]; !ok {
			t.Errorf("%s was not preclassified as excluded", name)
		}
	}
}

func TestLoadOneFilteredKeepsExcludedDeclarationsForTypeChecking(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "ignored.go"), []byte("package p\nfunc helper() int { return 7 }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "keep.go"), []byte("package p\nfunc Keep() int { return helper() }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ignored, _ := filepath.Abs(filepath.Join(dir, "ignored.go"))
	loader := newPackageLoader(nil)
	loaded, err := loader.loadOneFiltered(context.Background(), listedPackage{
		Dir:        dir,
		ImportPath: "example.test/p",
		GoFiles:    []string{"ignored.go", "keep.go"},
	}, false, map[string]struct{}{filepath.Clean(ignored): {}})
	if err != nil {
		t.Fatalf("loadOneFiltered: %v", err)
	}
	if len(loaded.Files) != 2 {
		t.Fatalf("type-check files=%d, want 2", len(loaded.Files))
	}
	if len(loaded.AnalysisFiles) != 1 {
		t.Fatalf("analysis files=%d, want 1", len(loaded.AnalysisFiles))
	}
	if got := filepath.Base(loaded.Fset.Position(loaded.AnalysisFiles[0].Package).Filename); got != "keep.go" {
		t.Fatalf("analysis file=%q, want keep.go", got)
	}
}

func TestAnalyzePatternsAllGeneratedSourcesShortCircuitsFrontend(t *testing.T) {
	dir := t.TempDir()
	write := func(name, contents string) {
		t.Helper()
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module example.test/allgenerated\n\ngo 1.20\n")
	write("p/generated.go", `// Code generated by test; DO NOT EDIT.
package p

func F() {}
`)

	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := os.Chdir(oldwd); err != nil {
			t.Errorf("restore cwd: %v", err)
		}
	}()

	cfg := config.Default()
	cfg.Timeout = "30s"
	diags, coverage, status, err := analyzePatterns(context.Background(), []string{"./p"}, cfg)
	if err != nil {
		t.Fatalf("analyzePatterns: %v", err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics=%#v, want none", diags)
	}
	if coverage.CancelBindings != 0 || coverage.Groups != 0 || coverage.Goroutines != 0 {
		t.Fatalf("coverage=%#v, want empty", coverage)
	}
	if status.Units.ExcludedFiles != 1 || status.Units.Discovered != 0 || status.Units.Analyzed != 0 {
		t.Fatalf("status units=%#v, want one excluded file and no function units", status.Units)
	}
}
