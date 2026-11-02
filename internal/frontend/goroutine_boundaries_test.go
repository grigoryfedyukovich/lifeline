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
