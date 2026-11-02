package integration

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestPartialHandoffCannotPassUnsupportedPolicy(t *testing.T) {
	root := repositoryRoot(t)
	binary := buildBinary(t, root)
	const pkg = "./tests/testdata/facts/status_partial"
	for _, ignore := range []string{"", "ignore: [all]\n"} {
		cfg := writeConfig(t, ignore+"fail_on_unsupported: true\nci_exit_code: 7\n")
		r, code := runJSON(t, root, binary, "-config", cfg, "-timeout", "30s", pkg)
		if code != 7 || r.Status.Unsupported.HandedOff != 1 || len(r.Diagnostics) != 0 {
			t.Fatalf("exit=%d report=%+v", code, r)
		}
	}
	statusPath := filepath.Join(t.TempDir(), "status.jsonl")
	cmd := exec.Command("go", "vet", "-vettool="+binary, "-lifeline.status-out="+statusPath, pkg)
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("vet: %v\n%s", err, out)
	}
	data, err := os.ReadFile(statusPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var row struct {
			Package string `json:"package"`
			Status  struct {
				Unsupported struct {
					HandedOff int `json:"handed_off_obligations"`
				} `json:"unsupported"`
			} `json:"status"`
		}
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			t.Fatal(err)
		}
		if strings.HasSuffix(row.Package, "/facts/status_partial") {
			if row.Status.Unsupported.HandedOff != 1 {
				t.Fatalf("vet status=%s", line)
			}
			return
		}
	}
	t.Fatal("missing target status record")
}
func TestReturnedErrgroupWaitRemainsVerified(t *testing.T) {
	root := repositoryRoot(t)
	binary := buildBinary(t, root)
	r, code := runJSON(t, root, binary, "-timeout", "30s", "-fail-on-unsupported", "./examples/proper_errgroup")
	if code != 0 || r.Status.Unsupported.HandedOff != 0 || len(r.Diagnostics) != 0 {
		t.Fatalf("exit=%d report=%+v", code, r)
	}
}
