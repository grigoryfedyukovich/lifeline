package integration

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Binary-level tests for audit finding F8, through both entry points.

type statusReport struct {
	Diagnostics []struct {
		RuleID string `json:"rule_id"`
	} `json:"diagnostics"`
	Incomplete bool `json:"incomplete"`
	Status     struct {
		Units struct {
			Discovered int `json:"discovered"`
			Analyzed   int `json:"analyzed"`
			Skipped    int `json:"skipped"`
		} `json:"units"`
		Incomplete bool `json:"incomplete"`
		Reasons    []struct {
			Kind string `json:"kind"`
		} `json:"reasons"`
		Unsupported struct {
			HandedOff int `json:"handed_off_obligations"`
		} `json:"unsupported"`
		Suppressed struct {
			ByConfig int `json:"by_config"`
		} `json:"suppressed"`
	} `json:"status"`
}

func runJSON(t *testing.T, root, binary string, args ...string) (statusReport, int) {
	t.Helper()
	var stdout bytes.Buffer
	cmd := exec.Command(binary, append([]string{"-format", "json"}, args...)...)
	cmd.Dir = root
	cmd.Stdout = &stdout
	err := cmd.Run()
	code := 0
	if exitErr, ok := err.(*exec.ExitError); ok {
		code = exitErr.ExitCode()
	} else if err != nil {
		t.Fatalf("run: %v", err)
	}
	var r statusReport
	if jerr := json.Unmarshal(stdout.Bytes(), &r); jerr != nil {
		t.Fatalf("not JSON: %v\n%s", jerr, stdout.String())
	}
	return r, code
}

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "lifeline.yaml")
	if err := os.WriteFile(path, []byte("schema_version: 1\n"+body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestStatus_StandaloneSuppressedTruncationStaysIncomplete(t *testing.T) {
	root := repositoryRoot(t)
	binary := buildBinary(t, root)
	pkg := "./examples/status_truncated_unsupported"

	shown, _ := runJSON(t, root, binary, "-max-functions", "2", pkg)
	hidden, _ := runJSON(t, root, binary, "-config", writeConfig(t, "ignore:\n  - LL9001\n"), "-max-functions", "2", pkg)

	if len(shown.Diagnostics) != 1 || shown.Diagnostics[0].RuleID != "LL9001" {
		t.Fatalf("truncation is shown by default: %#v", shown.Diagnostics)
	}
	if len(hidden.Diagnostics) != 0 {
		t.Fatalf("LL9001 ignored must hide it: %#v", hidden.Diagnostics)
	}
	for name, r := range map[string]statusReport{"shown": shown, "hidden": hidden} {
		if !r.Incomplete || !r.Status.Incomplete || r.Status.Units.Skipped != 3 || r.Status.Units.Discovered != 5 || len(r.Status.Reasons) != 1 || r.Status.Reasons[0].Kind != "max_functions" {
			t.Errorf("%s: status = %#v, want incomplete with 3 of 5 units skipped", name, r)
		}
	}
	if hidden.Status.Suppressed.ByConfig != 1 {
		t.Errorf("the hidden notice must be counted: %#v", hidden.Status.Suppressed)
	}
}

func TestStatus_StandaloneUnsupportedOnlyIsNotTheSameAsFullyModeled(t *testing.T) {
	root := repositoryRoot(t)
	binary := buildBinary(t, root)
	modeled, _ := runJSON(t, root, binary, "./examples/status_fully_modeled")
	unsupported, _ := runJSON(t, root, binary, "./examples/status_unsupported_only")
	if len(modeled.Diagnostics) != 0 || len(unsupported.Diagnostics) != 0 {
		t.Fatalf("neither fixture reports anything: %#v / %#v", modeled.Diagnostics, unsupported.Diagnostics)
	}
	if modeled.Status.Unsupported.HandedOff != 0 || unsupported.Status.Unsupported.HandedOff != 1 {
		t.Errorf("hand-offs: modeled %d, unsupported-only %d, want 0 and 1", modeled.Status.Unsupported.HandedOff, unsupported.Status.Unsupported.HandedOff)
	}
	if unsupported.Incomplete {
		t.Errorf("approximation is not incompleteness")
	}
	textModeled, _ := run(t, root, binary, "./examples/status_fully_modeled")
	textUnsupported, _ := run(t, root, binary, "./examples/status_unsupported_only")
	if textModeled == textUnsupported || !strings.Contains(textUnsupported, "approximations:") {
		t.Errorf("text output must tell them apart:\n%s\n---\n%s", textModeled, textUnsupported)
	}
}

func TestStatus_FailPoliciesReadTheStatusNotTheDiagnostics(t *testing.T) {
	root := repositoryRoot(t)
	binary := buildBinary(t, root)
	hide := writeConfig(t, "ignore:\n  - all\n")
	cases := []struct {
		name string
		args []string
		want int
	}{
		{"truncated, no policy", []string{"-max-functions", "2", "./examples/status_truncated_unsupported"}, 0},
		{"truncated, fail-on-incomplete", []string{"-fail-on-incomplete", "-max-functions", "2", "./examples/status_truncated_unsupported"}, 1},
		{"truncated with every diagnostic hidden, fail-on-incomplete", []string{"-config", hide, "-fail-on-incomplete", "-max-functions", "2", "./examples/status_truncated_unsupported"}, 1},
		{"complete, fail-on-incomplete", []string{"-fail-on-incomplete", "./examples/status_fully_modeled"}, 0},
		{"unsupported, no policy", []string{"./examples/status_unsupported_only"}, 0},
		{"unsupported, fail-on-unsupported", []string{"-fail-on-unsupported", "./examples/status_unsupported_only"}, 1},
		{"fully modeled, fail-on-unsupported", []string{"-fail-on-unsupported", "./examples/status_fully_modeled"}, 0},
		{"custom ci exit code", []string{"-fail-on-incomplete", "-ci-exit-code", "7", "-max-functions", "2", "./examples/status_truncated_unsupported"}, 7},
	}
	for _, c := range cases {
		if _, code := run(t, root, binary, c.args...); code != c.want {
			t.Errorf("%s: exit %d, want %d", c.name, code, c.want)
		}
	}
}

func TestStatus_PolicyKeysAreConfigurable(t *testing.T) {
	root := repositoryRoot(t)
	binary := buildBinary(t, root)
	cfg := writeConfig(t, "fail_on_incomplete: true\n")
	if _, code := run(t, root, binary, "-config", cfg, "-max-functions", "2", "./examples/status_truncated_unsupported"); code != 1 {
		t.Errorf("fail_on_incomplete in the config file: exit %d, want 1", code)
	}
	cfg = writeConfig(t, "fail_on_unsupported: true\n")
	if _, code := run(t, root, binary, "-config", cfg, "./examples/status_unsupported_only"); code != 1 {
		t.Errorf("fail_on_unsupported in the config file: exit %d, want 1", code)
	}
}

func TestStatus_StandaloneTimeoutIsStatusEvenWhenHidden(t *testing.T) {
	root := repositoryRoot(t)
	binary := buildBinary(t, root)
	pkg := "./examples/status_fully_modeled"
	shown, _ := runJSON(t, root, binary, "-timeout", "1ns", pkg)
	hidden, _ := runJSON(t, root, binary, "-config", writeConfig(t, "ignore:\n  - LL9001\n"), "-timeout", "1ns", pkg)
	if len(shown.Diagnostics) != 1 || shown.Diagnostics[0].RuleID != "LL9001" {
		t.Fatalf("a timeout is shown as LL9001 by default: %#v", shown.Diagnostics)
	}
	if len(hidden.Diagnostics) != 0 {
		t.Fatalf("a timeout notice is hidden by ignore like any other diagnostic: %#v", hidden.Diagnostics)
	}
	for name, r := range map[string]statusReport{"shown": shown, "hidden": hidden} {
		if !r.Incomplete || len(r.Status.Reasons) == 0 || r.Status.Reasons[len(r.Status.Reasons)-1].Kind != "timeout" {
			t.Errorf("%s: a timeout must be in the status: %#v", name, r)
		}
	}
	if _, code := run(t, root, binary, "-config", writeConfig(t, "ignore:\n  - LL9001\n"), "-fail-on-incomplete", "-timeout", "1ns", pkg); code != 1 {
		t.Errorf("a hidden timeout must still trip fail-on-incomplete: exit %d", code)
	}
}

// vet shows only diagnostics: the status must reach a consumer some other
// way. -status-out writes one JSON line per package.
func TestStatus_VetWritesACompanionStatusReport(t *testing.T) {
	root := repositoryRoot(t)
	binary := buildBinary(t, root)
	out := filepath.Join(t.TempDir(), "status.jsonl")
	cmd := exec.Command("go", "vet", "-vettool="+binary, "-lifeline.status-out="+out, "-lifeline.max-functions=2", "-lifeline.ignore=LL9001", "./examples/status_truncated_unsupported")
	cmd.Dir = root
	if combined, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go vet: %v\n%s", err, combined)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var r struct {
			Package string `json:"package"`
			Status  struct {
				Incomplete bool `json:"incomplete"`
				Units      struct {
					Skipped int `json:"skipped"`
				} `json:"units"`
				Suppressed struct {
					ByConfig int `json:"by_config"`
				} `json:"suppressed"`
			} `json:"status"`
		}
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			t.Fatalf("status line is not JSON: %v\n%s", err, line)
		}
		if strings.HasSuffix(r.Package, "examples/status_truncated_unsupported") {
			found = true
			if !r.Status.Incomplete || r.Status.Units.Skipped != 3 || r.Status.Suppressed.ByConfig != 1 {
				t.Errorf("vet's status for the package: %s", line)
			}
		}
	}
	if !found {
		t.Errorf("no status line for the analyzed package in:\n%s", data)
	}
}

func TestStatus_VetStatusDistinguishesUnsupportedFromModeled(t *testing.T) {
	root := repositoryRoot(t)
	binary := buildBinary(t, root)
	handedOff := func(pkg string) int {
		out := filepath.Join(t.TempDir(), "status.jsonl")
		cmd := exec.Command("go", "vet", "-vettool="+binary, "-lifeline.status-out="+out, pkg)
		cmd.Dir = root
		if combined, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("go vet %s: %v\n%s", pkg, err, combined)
		}
		data, err := os.ReadFile(out)
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
			var r struct {
				Package string `json:"package"`
				Status  struct {
					Unsupported struct {
						HandedOff int `json:"handed_off_obligations"`
					} `json:"unsupported"`
				} `json:"status"`
			}
			if err := json.Unmarshal([]byte(line), &r); err == nil && strings.HasSuffix(r.Package, strings.TrimPrefix(pkg, "./")) {
				return r.Status.Unsupported.HandedOff
			}
		}
		t.Fatalf("no status for %s", pkg)
		return -1
	}
	if m, u := handedOff("./examples/status_fully_modeled"), handedOff("./examples/status_unsupported_only"); m != 0 || u != 1 {
		t.Errorf("vet status hand-offs: modeled %d, unsupported-only %d, want 0 and 1", m, u)
	}
}
