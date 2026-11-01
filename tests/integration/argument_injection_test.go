package integration

import (
	"strings"
	"testing"
)

func TestCLIRejectsGoFlagsPassedAsPackagePatterns(t *testing.T) {
	root := repositoryRoot(t)
	binary := buildBinary(t, root)
	const pattern = "-toolexec=lifeline-test-command-must-not-run"
	for _, tc := range []struct {
		name string
		args []string
	}{
		{name: "analysis"},
		{name: "tests", args: []string{"-tests"}},
		{name: "cfg", args: []string{"-dump", "cfg"}},
		{name: "facts", args: []string{"-dump", "facts"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := append(tc.args, "--", pattern, "./examples/proper_context")
			out, code := run(t, root, binary, args...)
			if code != 2 || !strings.Contains(out, "invalid package pattern") || !strings.Contains(out, pattern) {
				t.Fatalf("flag-shaped package pattern: exit=%d output=%s", code, out)
			}
		})
	}
}
