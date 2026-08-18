package integration

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

func repositoryRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}

var (
	binaryOnce sync.Once
	binaryPath string
	binaryDir  string
	binaryErr  error
)

func buildBinary(t *testing.T, root string) string {
	t.Helper()
	binaryOnce.Do(func() {
		binaryDir, binaryErr = os.MkdirTemp("", "lifeline-integration-")
		if binaryErr != nil {
			return
		}
		binaryPath = filepath.Join(binaryDir, "lifeline")
		cmd := exec.Command("go", "build", "-o", binaryPath, "./cmd/lifeline")
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			binaryErr = fmt.Errorf("build: %w\n%s", err, out)
		}
	})
	if binaryErr != nil {
		t.Fatal(binaryErr)
	}
	return binaryPath
}

func TestMain(m *testing.M) {
	code := m.Run()
	if binaryDir != "" {
		_ = os.RemoveAll(binaryDir)
	}
	os.Exit(code)
}

func run(t *testing.T, root, binary string, args ...string) (string, int) {
	t.Helper()
	cmd := exec.Command(binary, args...)
	cmd.Dir = root
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	if err == nil {
		return out.String(), 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return out.String(), exitErr.ExitCode()
	}
	t.Fatalf("run: %v", err)
	return "", -1
}

func golden(t *testing.T, root, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, "tests", "golden", name+".txt"))
	if err != nil {
		t.Fatal(err)
	}
	return strings.ReplaceAll(string(data), "\r\n", "\n")
}

func TestExamples(t *testing.T) {
	root := repositoryRoot(t)
	binary := buildBinary(t, root)
	for _, tc := range []struct {
		name    string
		pattern string
	}{
		{"ignored_context", "./examples/ignored_context"},
		{"lost_cancel", "./examples/lost_cancel"},
		{"proper_errgroup", "./examples/proper_errgroup"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, code := run(t, root, binary, tc.pattern)
			if code != 0 {
				t.Fatalf("exit code %d\n%s", code, got)
			}
			if got != golden(t, root, tc.name) {
				t.Fatalf("output mismatch\n--- got ---\n%s--- want ---\n%s", got, golden(t, root, tc.name))
			}
		})
	}
}

func TestCIPolicyIsOptIn(t *testing.T) {
	root := repositoryRoot(t)
	binary := buildBinary(t, root)
	_, code := run(t, root, binary, "./examples/ignored_context")
	if code != 0 {
		t.Fatalf("default diagnostics exit = %d, want 0", code)
	}
	out, code := run(t, root, binary, "-fail-on", "LL1002", "-ci-exit-code", "7", "./examples/ignored_context")
	if code != 7 || !strings.Contains(out, "LL1002") {
		t.Fatalf("policy run: exit=%d output=%s", code, out)
	}
}

func TestMachineFormats(t *testing.T) {
	root := repositoryRoot(t)
	binary := buildBinary(t, root)
	jsonOut, code := run(t, root, binary, "-format", "json", "./examples/lost_cancel")
	if code != 0 || !strings.Contains(jsonOut, `"schema_version": "lifeline.report/v1"`) || !strings.Contains(jsonOut, `"suggested_fix"`) {
		t.Fatalf("json output: exit=%d\n%s", code, jsonOut)
	}
	sarifOut, code := run(t, root, binary, "-format", "sarif", "./examples/lost_cancel")
	if code != 0 || !strings.Contains(sarifOut, `"version": "2.1.0"`) || !strings.Contains(sarifOut, `"ruleId": "LL1001"`) {
		t.Fatalf("sarif output: exit=%d\n%s", code, sarifOut)
	}
}

func TestTutorialExamples(t *testing.T) {
	root := repositoryRoot(t)
	binary := buildBinary(t, root)

	for _, tc := range []struct {
		name   string
		args   []string
		needle string
	}{
		{name: "proper_context", args: []string{"./examples/proper_context"}, needle: "no lifecycle diagnostics"},
		{name: "uncalled_cancel", args: []string{"./examples/uncalled_cancel"}, needle: "[LL1001]"},
		{name: "proper_cancel", args: []string{"./examples/proper_cancel"}, needle: "no lifecycle diagnostics"},
		{name: "unjoined_waitgroup", args: []string{"./examples/unjoined_waitgroup"}, needle: "[LL1003]"},
		{name: "proper_waitgroup", args: []string{"./examples/proper_waitgroup"}, needle: "no lifecycle diagnostics"},
		{name: "unjoined_errgroup", args: []string{"./examples/unjoined_errgroup"}, needle: "[LL1004]"},
		{name: "channel_shutdown", args: []string{"./examples/channel_shutdown"}, needle: "no lifecycle diagnostics"},
		{name: "custom_wrapper_without_config", args: []string{"./examples/custom_context_wrapper"}, needle: "no lifecycle diagnostics"},
		{name: "custom_wrapper_with_config", args: []string{"-config", "./examples/custom_context_wrapper/lifeline.yaml", "./examples/custom_context_wrapper"}, needle: "[LL1001]"},
		{name: "custom_start_without_config", args: []string{"./examples/custom_start_wrapper"}, needle: "no lifecycle diagnostics"},
		{name: "custom_start_with_config", args: []string{"-config", "./examples/custom_start_wrapper/lifeline.yaml", "./examples/custom_start_wrapper"}, needle: "[LL1002]"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, code := run(t, root, binary, tc.args...)
			if code != 0 {
				t.Fatalf("exit code %d\n%s", code, out)
			}
			if !strings.Contains(out, tc.needle) {
				t.Fatalf("output does not contain %q\n%s", tc.needle, out)
			}
		})
	}
}

func TestVetImportsVersionedFunctionFacts(t *testing.T) {
	root := repositoryRoot(t)
	binary := buildBinary(t, root)
	cmd := exec.Command("go", "vet", "-vettool="+binary, "./tests/testdata/facts/consumer")
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("go vet unexpectedly succeeded\n%s", out)
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("go vet: %v\n%s", err, out)
	}
	text := string(out)
	if !strings.Contains(text, "[LL1002]") || !strings.Contains(text, "versioned lifecycle fact") {
		t.Fatalf("cross-package fact diagnostic missing\n%s", text)
	}
}

func TestDumpCFG(t *testing.T) {
	root := repositoryRoot(t)
	binary := buildBinary(t, root)

	out, code := run(t, root, binary, "-dump", "cfg", "./examples/proper_context")
	if code != 0 {
		t.Fatalf("exit code %d\n%s", code, out)
	}
	// The goroutine closure's own control flow (the loop and select) is
	// what -dump cfg exists to show; the top-level named function that
	// merely starts it isn't the interesting part.
	for _, want := range []string{
		"func github.com/gfedyukovich/lifeline/examples/proper_context.Start.func1",
		"loop-header",
		"comm-case",
		"loop-back",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("dump output does not contain %q\n%s", want, out)
		}
	}

	dot, code := run(t, root, binary, "-dump", "cfg", "-dump-format", "dot", "./examples/proper_context")
	if code != 0 {
		t.Fatalf("dot dump exit code %d\n%s", code, dot)
	}
	if !strings.Contains(dot, "digraph") || !strings.Contains(dot, "->") {
		t.Fatalf("dot output does not look like a digraph\n%s", dot)
	}

	if _, code := run(t, root, binary, "-dump", "bogus", "./examples/proper_context"); code == 0 {
		t.Fatalf("expected a nonzero exit for an unsupported -dump value")
	}
	if _, code := run(t, root, binary, "-dump", "cfg", "-dump-format", "bogus", "./examples/proper_context"); code == 0 {
		t.Fatalf("expected a nonzero exit for an unsupported -dump-format value")
	}
}

func TestParallelPackageAnalysisIsDeterministic(t *testing.T) {
	root := repositoryRoot(t)
	binary := buildBinary(t, root)
	args := []string{
		"./examples/unjoined_waitgroup",
		"./examples/lost_cancel",
		"./examples/ignored_context",
	}
	first, code := run(t, root, binary, args...)
	if code != 0 {
		t.Fatalf("first run exit code %d\n%s", code, first)
	}
	second, code := run(t, root, binary, args...)
	if code != 0 {
		t.Fatalf("second run exit code %d\n%s", code, second)
	}
	if first != second {
		t.Fatalf("parallel output is nondeterministic\n--- first ---\n%s--- second ---\n%s", first, second)
	}
	ignored := strings.Index(first, "examples/ignored_context")
	lost := strings.Index(first, "examples/lost_cancel")
	wait := strings.Index(first, "examples/unjoined_waitgroup")
	if ignored < 0 || lost < 0 || wait < 0 || !(ignored < lost && lost < wait) {
		t.Fatalf("package output is not sorted deterministically\n%s", first)
	}
}
