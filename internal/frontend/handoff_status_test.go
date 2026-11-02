package frontend

import (
	"github.com/gfedyukovich/lifeline/internal/config"
	"github.com/gfedyukovich/lifeline/internal/engine"
	"testing"
)

func TestPartialCleanupCannotHideHandoff(t *testing.T) {
	for _, src := range []string{
		ctxHeader + `func f(parent context.Context,b bool)context.CancelFunc{_,cancel:=context.WithCancel(parent);if b{cancel()};return cancel}`,
		wgHeader + `func f(b bool)*sync.WaitGroup{var wg sync.WaitGroup;wg.Add(1);go func(){defer wg.Done()}();if b{wg.Wait()};return &wg}`,
	} {
		p := buildProgramFor(t, src, nil)
		_, st := engine.AnalyzeWithStatus(p, config.Default())
		if st.Unsupported.HandedOff != 1 {
			t.Fatalf("status=%+v", st)
		}
	}
}
func TestVerifiedReturnedCleanupHasNoHandoffGap(t *testing.T) {
	for _, src := range []string{
		ctxHeader + `func f(parent context.Context)context.CancelFunc{_,cancel:=context.WithCancel(parent);cancel();return cancel}`,
		wgHeader + `func wait(g *sync.WaitGroup)error{g.Wait();return nil};func f()error{var wg sync.WaitGroup;wg.Add(1);go func(){defer wg.Done()}();return wait(&wg)}`,
	} {
		p := buildProgramFor(t, src, nil)
		d, st := engine.AnalyzeWithStatus(p, config.Default())
		if len(d) != 0 || st.Unsupported.Total() != 0 {
			t.Fatalf("diagnostics=%#v status=%+v", d, st)
		}
	}
}
