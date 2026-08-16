package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestLoadYAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "lifeline.yaml")
	data := `schema_version: 1
format: json
ci_exit_code: 7
timeout: 2s
max_functions: 42
include_tests: true
fail_on:
  - LL1001
  - LL1002
context_wrappers: ["example.com/x.WithStop"]
`
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Format != "json" || cfg.CIExitCode != 7 || cfg.MaxFunctions != 42 || !cfg.IncludeTests {
		t.Fatalf("unexpected config: %+v", cfg)
	}
	if !reflect.DeepEqual(cfg.FailOn, []string{"LL1001", "LL1002"}) {
		t.Fatalf("fail_on = %#v", cfg.FailOn)
	}
}

func TestLoadTOML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "lifeline.toml")
	data := `schema_version = 1
format = "sarif"
timeout = "3s"
fail_on = ["all"]
`
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Format != "sarif" || cfg.Timeout != "3s" || !reflect.DeepEqual(cfg.FailOn, []string{"all"}) {
		t.Fatalf("unexpected config: %+v", cfg)
	}
}

func TestUnknownKeyIsError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "lifeline.yaml")
	if err := os.WriteFile(path, []byte("schema_version: 1\ntyop: true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), "unknown key") {
		t.Fatalf("expected unknown-key error, got %v", err)
	}
}

func TestDurationRejectsInvalidValueWithoutValidation(t *testing.T) {
	cfg := Default()
	cfg.Timeout = "not-a-duration"
	if _, err := cfg.Duration(); err == nil {
		t.Fatal("Duration accepted invalid timeout")
	}
}

func TestInlineArrayPreservesQuotedComma(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "lifeline.toml")
	data := `schema_version = 1
context_wrappers = ["example.com/a,b.WithCancel", "example.com/plain.WithCancel"]
`
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"example.com/a,b.WithCancel", "example.com/plain.WithCancel"}
	if !reflect.DeepEqual(cfg.ContextWrappers, want) {
		t.Fatalf("context_wrappers = %#v, want %#v", cfg.ContextWrappers, want)
	}
}

func TestSplitCSV(t *testing.T) {
	if got := SplitCSV(" LL1001, ,LL1002 "); !reflect.DeepEqual(got, []string{"LL1001", "LL1002"}) {
		t.Fatalf("SplitCSV = %#v", got)
	}
}
