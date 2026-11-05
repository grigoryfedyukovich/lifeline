package frontend

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"

	"github.com/gfedyukovich/lifeline/internal/config"
)

func TestFilterFilesHonorsIncludeTests(t *testing.T) {
	fset := token.NewFileSet()
	production, err := parser.ParseFile(fset, "production.go", "package p\n", 0)
	if err != nil {
		t.Fatal(err)
	}
	// The adjusted position deliberately hides the physical suffix. Filtering
	// must still identify this as a test file.
	testFile, err := parser.ParseFile(fset, "production_test.go", "//line disguised.go:1\npackage p\n", 0)
	if err != nil {
		t.Fatal(err)
	}

	cfg := config.Default()
	got := FilterFiles(fset, []*ast.File{production, testFile}, cfg, "")
	if len(got) != 1 || got[0] != production {
		t.Fatalf("FilterFiles(include_tests=false) = %#v, want production file only", got)
	}

	cfg.IncludeTests = true
	got = FilterFiles(fset, []*ast.File{production, testFile}, cfg, "")
	if len(got) != 2 || got[0] != production || got[1] != testFile {
		t.Fatalf("FilterFiles(include_tests=true) = %#v, want both files", got)
	}
}
