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

func TestVetHonorsIncludeTests(t *testing.T) {
	root := repositoryRoot(t)
	binary := buildBinary(t, root)

	withoutTests := exec.Command(
		"go", "vet",
		"-vettool="+binary,
		"-lifeline.config=tests/testdata/vet_include_tests/exclude_tests.yaml",
		"./tests/testdata/vet_include_tests",
	)
	withoutTests.Dir = root
	if out, err := withoutTests.CombinedOutput(); err != nil {
		t.Fatalf("go vet with include_tests=false: %v\n%s", err, out)
	} else if strings.TrimSpace(string(out)) != "" {
		t.Fatalf("go vet with include_tests=false produced unexpected output:\n%s", out)
	}

	withTests := exec.Command(
		"go", "vet",
		"-vettool="+binary,
		"-lifeline.config=tests/testdata/vet_include_tests/include_tests.yaml",
		"./tests/testdata/vet_include_tests",
	)
	withTests.Dir = root
	out, err := withTests.CombinedOutput()
	if err == nil {
		t.Fatalf("go vet with include_tests=true succeeded, want LL1001:\n%s", out)
	}
	text := string(out)
	if !strings.Contains(text, "fixture_test.go") || !strings.Contains(text, "LL1001") {
		t.Fatalf("go vet with include_tests=true output = %q, want fixture_test.go LL1001", text)
	}
}
