package frontend

import "testing"

func TestCancelSentThroughChannelTransfersOwnership(t *testing.T) {
	diags := analyzeSource(t, `package p
import "context"
func handoff(parent context.Context, out chan<- context.CancelFunc) {
	_, cancel := context.WithCancel(parent)
	out <- cancel
}
`)
	for _, d := range diags {
		if d.RuleID == "LL1001" {
			t.Fatalf("channel handoff should transfer cancel ownership, got %#v", diags)
		}
	}
}

func TestHardTerminatorsResolveWorkerLoops(t *testing.T) {
	tests := []struct {
		name, source string
	}{
		{"os.Exit", `package p
import "os"
func Start() { go func() { for { os.Exit(1) } }() }
`},
		{"runtime.Goexit", `package p
import "runtime"
func Start() { go func() { for { runtime.Goexit() } }() }
`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			for _, d := range analyzeSource(t, tc.source) {
				if d.RuleID == "LL1002" {
					t.Fatalf("hard terminator should resolve the worker loop, got %#v", d)
				}
			}
		})
	}
}
