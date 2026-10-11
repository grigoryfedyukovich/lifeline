package standalone

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
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
