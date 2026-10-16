package frontend

import (
	"fmt"
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"strings"
	"testing"

	"github.com/gfedyukovich/lifeline/internal/config"
)

// benchmarkFixture is deliberately prepared outside the timed section of each
// benchmark. These benchmarks measure Lifeline's frontend/model construction,
// not parser, importer, or go/types performance.
type benchmarkFixture struct {
	in  Input
	cfg config.Config
}

func newBenchmarkFixture(tb testing.TB, source string) benchmarkFixture {
	tb.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "benchmark.go", source, parser.ParseComments)
	if err != nil {
		tb.Fatal(err)
	}
	info := &types.Info{
		Types:      make(map[ast.Expr]types.TypeAndValue),
		Defs:       make(map[*ast.Ident]types.Object),
		Uses:       make(map[*ast.Ident]types.Object),
		Selections: make(map[*ast.SelectorExpr]*types.Selection),
		Scopes:     make(map[ast.Node]*types.Scope),
		Implicits:  make(map[ast.Node]types.Object),
	}
	pkg, err := (&types.Config{Importer: importer.Default()}).Check("example.test/benchmark", fset, []*ast.File{file}, info)
	if err != nil {
		tb.Fatal(err)
	}
	cfg := config.Default()
	cfg.MaxFunctions = 100_000
	return benchmarkFixture{
		in:  Input{Fset: fset, Files: []*ast.File{file}, Pkg: pkg, Info: info},
		cfg: cfg,
	}
}

func benchmarkBuild(b *testing.B, source string) {
	b.Helper()
	fixture := newBenchmarkFixture(b, source)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		program, err := Build(fixture.in, fixture.cfg)
		if err != nil {
			b.Fatal(err)
		}
		if program.Truncated {
			b.Fatal("benchmark fixture unexpectedly hit max_functions")
		}
	}
}

func TestBenchmarkFixturesBuild(t *testing.T) {
	for name, source := range map[string]string{
		"parameter-chain":   paramDelegationChainSource(25),
		"constructor-chain": constructorForwardingSource(25),
		"many-groups":       manyWaitGroupsSource(8),
	} {
		t.Run(name, func(t *testing.T) {
			fixture := newBenchmarkFixture(t, source)
			program, err := Build(fixture.in, fixture.cfg)
			if err != nil {
				t.Fatal(err)
			}
			if program.Truncated {
				t.Fatal("benchmark fixture unexpectedly hit max_functions")
			}
		})
	}
}

// BenchmarkBuildParamDelegationChain exercises the dependency-driven parameter
// effect solver added for audit finding F11. The declarations are deliberately
// caller-first: the old whole-package fixed point revisited every function on
// every propagation round, producing quadratic function visits on this shape.
func BenchmarkBuildParamDelegationChain(b *testing.B) {
	for _, n := range []int{100, 500, 1000} {
		b.Run(fmt.Sprintf("functions=%d", n+1), func(b *testing.B) {
			benchmarkBuild(b, paramDelegationChainSource(n))
		})
	}
}

func paramDelegationChainSource(n int) string {
	var src strings.Builder
	src.WriteString("package p\nimport \"context\"\n")
	// Emit callers before callees to make propagation order adversarial.
	for i := n; i >= 1; i-- {
		fmt.Fprintf(&src, "func f%d(cancel context.CancelFunc) { f%d(cancel) }\n", i, i-1)
	}
	src.WriteString("func f0(cancel context.CancelFunc) { cancel() }\n")
	return src.String()
}

// BenchmarkBuildConstructorForwarding exercises caller-instantiated returned
// obligations plus the constructor reverse-dependency worklist/index. The old
// implementation repeatedly rescanned the complete package and all known
// returned-field bindings while a forwarding chain converged.
func BenchmarkBuildConstructorForwarding(b *testing.B) {
	for _, n := range []int{50, 250, 1000} {
		b.Run(fmt.Sprintf("wrappers=%d", n), func(b *testing.B) {
			benchmarkBuild(b, constructorForwardingSource(n))
		})
	}
}

func constructorForwardingSource(n int) string {
	var src strings.Builder
	src.WriteString(`package p
import "context"
type Handle struct { cancel context.CancelFunc }
`)
	// Again put the ultimate caller and forwarding wrappers before New so the
	// summary solver cannot accidentally rely on declaration order.
	fmt.Fprintf(&src, "func use(parent context.Context) { h := W%d(parent); _ = h }\n", n)
	for i := n; i >= 1; i-- {
		callee := "New"
		if i > 1 {
			callee = fmt.Sprintf("W%d", i-1)
		}
		fmt.Fprintf(&src, "func W%d(parent context.Context) *Handle { return %s(parent) }\n", i, callee)
	}
	src.WriteString(`func New(parent context.Context) *Handle {
	_, cancel := context.WithCancel(parent)
	return &Handle{cancel: cancel}
}
`)
	return src.String()
}

// BenchmarkBuildManyWaitGroups exercises the per-function group-use index. A
// body with many independent groups used to make each group rescan unrelated
// statements, giving an O(groups*body) component before graph checks.
func BenchmarkBuildManyWaitGroups(b *testing.B) {
	for _, n := range []int{10, 50, 100} {
		b.Run(fmt.Sprintf("groups=%d", n), func(b *testing.B) {
			benchmarkBuild(b, manyWaitGroupsSource(n))
		})
	}
}

func manyWaitGroupsSource(n int) string {
	var src strings.Builder
	src.WriteString("package p\nimport \"sync\"\nfunc run() {\n")
	for i := 0; i < n; i++ {
		fmt.Fprintf(&src, "var wg%d sync.WaitGroup\n", i)
		fmt.Fprintf(&src, "wg%d.Add(1)\n", i)
		fmt.Fprintf(&src, "go func() { defer wg%d.Done() }()\n", i)
		fmt.Fprintf(&src, "wg%d.Wait()\n", i)
	}
	src.WriteString("}\n")
	return src.String()
}
