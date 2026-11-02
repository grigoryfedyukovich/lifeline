package frontend

import (
	"fmt"
	"strings"
	"testing"

	"github.com/gfedyukovich/lifeline/internal/engine"
)

// Each family repeats independent owners, exercising recent recognition paths
// without conflating their costs with Go parsing/type checking. The paired
// corpus examples cover safe and unsafe protocols through the CLI.
var recentBenchmarkFamilies = []string{"closure-cleanup", "context-field-helper", "channel-array-alias"}

func BenchmarkBuildRecentLifecycle(b *testing.B) {
	for _, family := range recentBenchmarkFamilies {
		for _, owners := range []int{10, 50, 100} {
			b.Run(fmt.Sprintf("%s/owners=%d", family, owners), func(b *testing.B) {
				benchmarkBuild(b, recentLifecycleSource(family, owners))
			})
		}
	}
}

func recentLifecycleSource(family string, owners int) string {
	var source strings.Builder
	switch family {
	case "closure-cleanup":
		source.WriteString("package p\nimport \"context\"\n")
	case "context-field-helper":
		source.WriteString(`package p
import ("context"; "sync")
type embedded struct { ctx context.Context }
type service struct { embedded }
func waitContext(s *service) { <-s.ctx.Done() }
func join(wg *sync.WaitGroup) { wg.Wait() }
`)
	case "channel-array-alias":
		source.WriteString("package p\nimport \"sync\"\ntype holder struct { stops [2]chan struct{} }\n")
	default:
		panic("unknown benchmark family: " + family)
	}
	for i := 0; i < owners; i++ {
		switch family {
		case "closure-cleanup":
			fmt.Fprintf(&source, `func owner%d(parent context.Context) {
var _, cancel = context.WithCancel(parent)
release := func() { cancel() }
defer release()
}
`, i)
		case "context-field-helper":
			fmt.Fprintf(&source, `func owner%d(parent context.Context) {
ctx, cancel := context.WithCancel(parent)
s := &service{}
alias := s
alias.ctx = ctx
var wg sync.WaitGroup
wg.Add(1)
go func() { defer wg.Done(); waitContext(s) }()
join(&wg)
cancel()
}
`, i)
		case "channel-array-alias":
			fmt.Fprintf(&source, `func owner%d() {
stop := make(chan struct{})
h := &holder{}
alias := h
alias.stops[0] = stop
var wg sync.WaitGroup
wg.Add(1)
go func() { defer wg.Done(); select { case <-h.stops[0]: return } }()
wg.Wait()
close(stop)
}
`, i)
		}
	}
	return source.String()
}

func TestRecentBenchmarkFixtures(t *testing.T) {
	for _, family := range recentBenchmarkFamilies {
		t.Run(family, func(t *testing.T) {
			const owners = 8
			fixture := newBenchmarkFixture(t, recentLifecycleSource(family, owners))
			program, err := Build(fixture.in, fixture.cfg)
			if err != nil {
				t.Fatal(err)
			}
			if program.Truncated {
				t.Fatal("benchmark fixture hit max_functions")
			}
			diagnostics := engine.Analyze(program, fixture.cfg)
			want := owners
			if family == "closure-cleanup" {
				want = 0
			}
			if len(diagnostics) != want {
				t.Fatalf("diagnostics=%d, want %d: %#v", len(diagnostics), want, diagnostics)
			}
			for _, d := range diagnostics {
				if d.RuleID != "LL1005" {
					t.Errorf("unexpected rule %s: %s", d.RuleID, d.Message)
				}
			}
		})
	}
}
