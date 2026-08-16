package analyzer

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"testing"

	"github.com/gfedyukovich/lifeline/internal/model"
)

func TestNewHasIndependentFlagStorage(t *testing.T) {
	a := New()
	b := New()
	if err := a.Flags.Set("ignore", "LL1001"); err != nil {
		t.Fatal(err)
	}
	if got := b.Flags.Lookup("ignore").Value.String(); got != "" {
		t.Fatalf("second analyzer inherited ignore=%q", got)
	}
}

func TestPositionIndex(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, filepath.Join(t.TempDir(), "x.go"), "package p\nfunc f() {}\n", 0)
	if err != nil {
		t.Fatal(err)
	}
	index := buildFileIndex(fset, []*ast.File{file})
	tf := fset.File(file.Pos())
	span := model.Span{File: tf.Name(), StartOffset: 10, EndOffset: 14}
	if got := posFor(index, span, false); got != tf.Pos(10) {
		t.Fatalf("start = %v, want %v", got, tf.Pos(10))
	}
	if got := posFor(index, span, true); got != tf.Pos(14) {
		t.Fatalf("end = %v, want %v", got, tf.Pos(14))
	}
}
