package standalone

import (
	"context"
	"errors"
	"fmt"
	"go/types"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/gfedyukovich/lifeline/internal/config"
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
