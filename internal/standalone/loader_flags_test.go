package standalone

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

func TestListPackagesRejectsFlagPatternsBeforeStartingGo(t *testing.T) {
	// No executable is available: the input error must be detected before
	// looking up or starting go, regardless of the pattern's position.
	t.Setenv("PATH", t.TempDir())
	for _, includeTests := range []bool{false, true} {
		for _, pattern := range []string{
			"-toolexec=lifeline-test-command-must-not-run",
			"-json=false",
			"-e=false",
		} {
			for _, afterValid := range []bool{false, true} {
				name := fmt.Sprintf("tests=%t/%s/after_valid=%t", includeTests, pattern, afterValid)
				t.Run(name, func(t *testing.T) {
					patterns := []string{pattern, "."}
					if afterValid {
						patterns = []string{".", pattern}
					}
					packages, err := listPackages(context.Background(), patterns, includeTests)
					want := fmt.Sprintf("invalid package pattern %q: must not start with '-'", pattern)
					if err == nil || !strings.Contains(err.Error(), want) {
						t.Fatalf("listPackages(%q, %t) error = %v, want %q", patterns, includeTests, err, want)
					}
					if len(packages) != 0 {
						t.Fatalf("listPackages returned %d packages for invalid input", len(packages))
					}
				})
			}
		}
	}
}
