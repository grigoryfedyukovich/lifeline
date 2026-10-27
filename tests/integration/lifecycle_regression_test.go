package integration

import (
	"os/exec"
	"strings"
	"testing"
)

func TestLifecycleRegressionsInStandaloneAndVet(t *testing.T) {
	root := repositoryRoot(t)
	binary := buildBinary(t, root)
	for _, tc := range []struct{ pattern, rule string }{{"./tests/testdata/facts/async_consumer", "LL1003"}, {"./tests/testdata/facts/async_errgroup", "LL1004"}} {
		t.Run(tc.rule, func(t *testing.T) {
			out, code := run(t, root, binary, "-timeout", "30s", "-fail-on", tc.rule, "-ci-exit-code", "7", tc.pattern)
			if code != 7 || !strings.Contains(out, "["+tc.rule+"]") {
				t.Fatalf("standalone: exit=%d output=%s", code, out)
			}
			cmd := exec.Command("go", "vet", "-vettool="+binary, tc.pattern)
			cmd.Dir = root
			vetOut, err := cmd.CombinedOutput()
			if err == nil || !strings.Contains(string(vetOut), "["+tc.rule+"]") {
				t.Fatalf("vet: err=%v output=%s", err, vetOut)
			}
		})
	}
}
