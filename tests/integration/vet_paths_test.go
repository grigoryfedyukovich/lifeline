package integration

import (
	"os/exec"
	"strings"
	"testing"
)

func TestVetPathsResolveFromModuleRoot(t *testing.T) {
	root := repositoryRoot(t)
	binary := buildBinary(t, root)

	for _, tc := range []struct {
		name       string
		configPath string
		pattern    string
	}{
		{
			name:       "relative config path",
			configPath: "tests/testdata/vet_paths/ignore_rule.yaml",
			pattern:    "./examples/lost_cancel",
		},
		{
			name:       "module relative ignore path",
			configPath: "tests/testdata/vet_paths/lifeline.yaml",
			pattern:    "./examples/ignore_path",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd := exec.Command("go", "vet", "-vettool="+binary, "-lifeline.config="+tc.configPath, tc.pattern)
			cmd.Dir = root
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("go vet: %v\n%s", err, out)
			} else if strings.TrimSpace(string(out)) != "" {
				t.Fatalf("go vet produced unexpected output:\n%s", out)
			}
		})
	}
}
