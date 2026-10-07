package standalone

import (
	"context"
	"errors"
	"os"
	"path/filepath"
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
