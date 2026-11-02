package engine

import (
	"github.com/gfedyukovich/lifeline/internal/config"
	"github.com/gfedyukovich/lifeline/internal/model"
	"testing"
)

func TestHandoffRequiresAnAllPathDischarge(t *testing.T) {
	yes, no := true, false
	for _, tc := range []struct {
		name string
		all  *bool
		want int
	}{{"unestablished", nil, 2}, {"partial", &no, 2}, {"verified", &yes, 0}} {
		t.Run(tc.name, func(t *testing.T) {
			p := model.Program{FunctionCount: 1, Functions: []model.Function{{Name: "f", Cancels: []model.CancelBinding{{Called: true, Escapes: true, CalledOnAllPaths: tc.all}}, Groups: []model.JoinGroup{{Joined: true, Escapes: true, JoinedOnAllPaths: tc.all}}}}}
			for _, ignored := range [][]string{nil, {"all"}} {
				cfg := config.Default()
				cfg.Ignore = ignored
				cfg.FailOnUnsupported = true
				_, st := AnalyzeWithStatus(p, cfg)
				if st.Unsupported.HandedOff != tc.want || FailsStatusPolicy(st, cfg) != (tc.want > 0) {
					t.Fatalf("ignored=%v status=%+v", ignored, st)
				}
			}
		})
	}
}
