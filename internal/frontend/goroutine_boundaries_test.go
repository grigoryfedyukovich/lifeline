package frontend

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/gfedyukovich/lifeline/internal/config"
	"github.com/gfedyukovich/lifeline/internal/engine"
)

// B1: each goroutine owns its loops and termination evidence. Check the
// lifecycle summary as well as diagnostics, since CFG-based diagnostics can
// otherwise conceal contamination of the syntax-based summary.
func TestGoroutineLifecycleBoundaries(t *testing.T) {
	cases := []struct {
		name      string
		body      string
		outerLoop bool
	}{
		{
			name: "inner loop only",
			body: `go func() { for {} }() // want LL1002`,
		},
		{
			name: "reachable inner context stop inside outer loop",
			body: `for {
	go func() { for { select { case <-ctx.Done(): return } } }()
}`,
			outerLoop: true,
		},
		{
			name: "reachable inner return inside outer loop",
			body: `for {
	go func() { for { return } }()
}`,
			outerLoop: true,
		},
		{
			name: "both goroutines loop",
			body: `go func() { for {} }() // want LL1002
for {}`,
			outerLoop: true,
		},
		{
			name: "uninvoked literal loop",
			body: `_ = func() { for {} }`,
		},
	}
	for _, tc := range cases {
		for _, named := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/named=%t", tc.name, named), func(t *testing.T) {
				outerComment := ""
				if tc.outerLoop {
					outerComment = " // want LL1002"
				}
				source := "package p\nimport \"context\"\nfunc start(ctx context.Context) {\n"
				if named {
					source += "go worker(ctx)" + outerComment + "\n}\nfunc worker(ctx context.Context) {\n" + tc.body + "\n}\n"
				} else {
					source += "go func() {" + outerComment + "\n" + tc.body + "\n}()\n}\n"
				}
				program := buildProgramFor(t, source, nil)
				var wantLines []int
				for i, line := range strings.Split(source, "\n") {
					if strings.Contains(line, "// want LL1002") {
						wantLines = append(wantLines, i+1)
					}
				}
				var gotLines []int
				for _, d := range engine.Analyze(program, config.Default()) {
					if d.RuleID != "LL1002" {
						t.Fatalf("unexpected diagnostic: %#v", d)
					}
					gotLines = append(gotLines, d.Position.StartLine)
				}
				slices.Sort(gotLines)
				if !slices.Equal(gotLines, wantLines) {
					t.Errorf("LL1002 lines = %v, want %v", gotLines, wantLines)
				}

				outerCount := 0
				for _, fn := range program.Functions {
					for _, g := range fn.Goroutines {
						if g.Span.StartLine != 4 {
							continue
						}
						outerCount++
						if g.InfiniteLoop != tc.outerLoop {
							t.Errorf("outer InfiniteLoop = %t, want %t", g.InfiniteLoop, tc.outerLoop)
						}
						if g.HasReturn || g.ContextStop || g.ChannelStop || g.ExplicitStop {
							t.Errorf("outer summary inherited nested termination evidence: %#v", g)
						}
					}
				}
				if outerCount != 1 {
					t.Errorf("outer goroutine summaries = %d, want 1", outerCount)
				}
			})
		}
	}
}

func TestGoroutineLocalClosureTargets(t *testing.T) {
	tests := []struct {
		name        string
		params      string
		body        string
		warnings    int
		unsupported int
	}{
		{"short declaration", "", "worker := func() { for {} }; go worker()", 1, 0},
		{"var declaration", "", "var worker = func() { for {} }; go (worker)()", 1, 0},
		{"nested local", "", "func() { worker := func() { for {} }; go worker() }()", 1, 0},
		{"reassigned", "", "worker := func() { for {} }; (worker) = func() {}; go worker()", 0, 1},
		{"reassigned in closure", "", "worker := func() { for {} }; func() { worker = func() {} }(); go worker()", 0, 1},
		{"address taken", "", "worker := func() { for {} }; p := &worker; *p = func() {}; go worker()", 0, 1},
		{"range assignment", "", "worker := func() { for {} }; for _, worker = range []func(){func() {}} { go worker() }", 0, 1},
		{"replaceable parameter", "worker func()", "go worker(); worker = func() { for {} }; _ = worker", 0, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			program := buildProgramFor(t, "package p\nfunc Start("+tt.params+") {\n"+tt.body+"\n}", nil)
			diags, status := engine.AnalyzeWithStatus(program, config.Default())
			if len(diags) != tt.warnings || len(diags) > 0 && diags[0].RuleID != "LL1002" {
				t.Errorf("diagnostics = %#v, want %d LL1002", diags, tt.warnings)
			}
			if status.Unsupported.Targets != tt.unsupported {
				t.Errorf("unsupported targets = %d, want %d", status.Unsupported.Targets, tt.unsupported)
			}
		})
	}
}

func TestGoroutineLocalClosureCapturesStopContext(t *testing.T) {
	program := buildProgramFor(t, `package p
import "context"

func Start(parent context.Context) {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	worker := func() {
		for {
			select {
			case <-ctx.Done():
				return
			}
		}
	}
	go worker()
}
`, nil)
	diags, status := engine.AnalyzeWithStatus(program, config.Default())
	if len(diags) != 0 || status.Unsupported.Targets != 0 {
		t.Fatalf("stoppable local closure should be modeled and clean: diagnostics=%#v status=%#v", diags, status)
	}
	for _, fn := range program.Functions {
		if len(fn.Cancels) > 0 && fn.Cancels[0].UsedByChild &&
			len(fn.Goroutines) == 1 && fn.Goroutines[0].ContextStop {
			return
		}
	}
	t.Fatal("captured context was not credited as a child use and stop signal")
}
