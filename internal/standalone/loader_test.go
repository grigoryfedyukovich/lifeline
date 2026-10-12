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
	_, err := listPackages(ctx, []string{"."})
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
