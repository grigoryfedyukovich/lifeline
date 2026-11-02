package integration

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/gfedyukovich/lifeline/internal/report"
)

// The experiment runner and this binary-level regression share expectations.
// Counts include duplicate rules; a clean result cannot hide a missing warning.
func TestRecentFeatureCorpus(t *testing.T) {
	root := repositoryRoot(t)
	binary := buildBinary(t, root)
	data, err := os.ReadFile(filepath.Join(root, "examples", "corpus.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		SchemaVersion int `json:"schema_version"`
		Cases         []struct {
			Name      string   `json:"name"`
			Rules     []string `json:"rules"`
			Functions []string `json:"functions"`
			HandedOff *int     `json:"handed_off"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.SchemaVersion != 1 || len(manifest.Cases) == 0 {
		t.Fatal("unsupported or empty corpus manifest")
	}
	seen := map[string]bool{}
	for _, tc := range manifest.Cases {
		if seen[tc.Name] {
			t.Fatalf("duplicate corpus case %q", tc.Name)
		}
		seen[tc.Name] = true
		t.Run(tc.Name, func(t *testing.T) {
			args := []string{"-format", "json", "-timeout", "30s"}
			config := filepath.Join(root, "examples", tc.Name, "lifeline.yaml")
			if _, err := os.Stat(config); err == nil {
				args = append(args, "-config", config)
			} else if !os.IsNotExist(err) {
				t.Fatal(err)
			}
			args = append(args, "./examples/"+tc.Name)
			out, code := run(t, root, binary, args...)
			if code != 0 {
				t.Fatalf("exit=%d output=%s", code, out)
			}
			var bundle report.Bundle
			if err := json.Unmarshal([]byte(out), &bundle); err != nil {
				t.Fatalf("invalid report: %v\n%s", err, out)
			}
			if bundle.Incomplete || bundle.Status.Incomplete {
				t.Fatalf("incomplete experiment: %+v", bundle.Status)
			}
			var rules, functions []string
			for _, d := range bundle.Diagnostics {
				rules = append(rules, d.RuleID)
				functions = append(functions, d.Function[strings.LastIndex(d.Function, ".")+1:])
			}
			slices.Sort(rules)
			slices.Sort(tc.Rules)
			if !slices.Equal(rules, tc.Rules) {
				t.Errorf("rules=%v, want %v\n%s", rules, tc.Rules, out)
			}
			if tc.Functions != nil {
				slices.Sort(functions)
				slices.Sort(tc.Functions)
				if !slices.Equal(functions, tc.Functions) {
					t.Errorf("functions=%v, want %v", functions, tc.Functions)
				}
			}
			if tc.HandedOff != nil && bundle.Status.Unsupported.HandedOff != *tc.HandedOff {
				t.Errorf("handed-off obligations=%d, want %d", bundle.Status.Unsupported.HandedOff, *tc.HandedOff)
			}
		})
	}
}
