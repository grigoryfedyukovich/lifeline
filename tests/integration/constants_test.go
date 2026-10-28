package integration

import (
	"os/exec"
	"strings"
	"testing"
)

func TestConstantLoopFactsInStandaloneAndVet(t *testing.T) {
	root := repositoryRoot(t)
	binary := buildBinary(t, root)
	const pattern = "./tests/testdata/facts/constant_consumer"
	out, code := run(t, root, binary, "-timeout", "30s", "-fail-on", "LL1002", "-ci-exit-code", "7", pattern)
	if code != 7 || strings.Count(out, "[LL1002]") != 2 {
		t.Fatalf("standalone exit=%d output=%s", code, out)
	}
	cmd := exec.Command("go", "vet", "-vettool="+binary, pattern)
	cmd.Dir = root
	vetOut, err := cmd.CombinedOutput()
	if err == nil || strings.Count(string(vetOut), "[LL1002]") != 2 {
		t.Fatalf("vet err=%v output=%s", err, vetOut)
	}
}
