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

func TestLoadFromResolvesRelativePathAgainstModuleRoot(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	configDir := filepath.Join(root, "config")
	if err := os.Mkdir(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "lifeline.yaml"), []byte("schema_version: 1\ntimeout: 17s\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	packageDir := filepath.Join(root, "nested", "package")
	if err := os.MkdirAll(packageDir, 0o700); err != nil {
		t.Fatal(err)
	}

	moduleRoot, err := ModuleRoot(packageDir)
	if err != nil {
		t.Fatal(err)
	}
	if moduleRoot != root {
		t.Fatalf("ModuleRoot = %q, want %q", moduleRoot, root)
	}
	cfg, err := LoadFrom(filepath.Join("config", "lifeline.yaml"), moduleRoot)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Timeout != "17s" {
		t.Fatalf("timeout = %q, want 17s", cfg.Timeout)
	}
}

func TestDuplicateKeysAreRejected(t *testing.T) {
	tests := []struct {
		name, ext, data string
	}{
		{"yaml scalar", ".yaml", "schema_version: 1\ntimeout: 1s\ntimeout: 2s\n"},
		{"yaml block then inline", ".yaml", "schema_version: 1\nfail_on:\n  - LL1001\nfail_on: [LL1002]\n"},
		{"toml", ".toml", "schema_version = 1\ntimeout = \"1s\"\ntimeout = \"2s\"\n"},
		{"json", ".json", `{"schema_version":1,"timeout":"1s","timeout":"2s"}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "lifeline"+tc.ext)
			if err := os.WriteFile(path, []byte(tc.data), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := Load(path)
			if err == nil || !strings.Contains(err.Error(), "duplicate key") {
				t.Fatalf("expected duplicate-key error, got %v", err)
			}
		})
	}
}

func TestLoadAcceptsUTF8BOM(t *testing.T) {
	tests := []struct {
		ext, data string
	}{
		{".yaml", "schema_version: 1\ntimeout: 9s\n"},
		{".toml", "schema_version = 1\ntimeout = \"9s\"\n"},
		{".json", `{"schema_version":1,"timeout":"9s"}`},
	}
	bom := []byte{0xEF, 0xBB, 0xBF}
	for _, tc := range tests {
		t.Run(tc.ext, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "lifeline"+tc.ext)
			data := append(append([]byte{}, bom...), []byte(tc.data)...)
			if err := os.WriteFile(path, data, 0o600); err != nil {
				t.Fatal(err)
			}
			cfg, err := Load(path)
			if err != nil {
				t.Fatal(err)
			}
			if cfg.Timeout != "9s" {
				t.Fatalf("timeout = %q, want 9s", cfg.Timeout)
			}
		})
	}
}

func TestQuotedValuesPreserveCommentMarkersAndEscapes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "lifeline.yaml")
	data := "schema_version: 1\ncontext_wrappers: [\"example.com/a\\\"#b.WithCancel\", 'example.com/it''s#ok.WithCancel'] # comment\n"
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{`example.com/a"#b.WithCancel`, "example.com/it's#ok.WithCancel"}
	if !reflect.DeepEqual(cfg.ContextWrappers, want) {
		t.Fatalf("context_wrappers = %#v, want %#v", cfg.ContextWrappers, want)
	}
}

func TestFindStopsAtNearestModuleRoot(t *testing.T) {
	outer := t.TempDir()
	if err := os.WriteFile(filepath.Join(outer, "lifeline.yaml"), []byte("schema_version: 1\ntimeout: 41s\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	module := filepath.Join(outer, "module")
	pkg := filepath.Join(module, "nested", "pkg")
	if err := os.MkdirAll(pkg, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(module, "go.mod"), []byte("module example.com/inner\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	found, err := Find(pkg)
	if err != nil {
		t.Fatal(err)
	}
	if found != "" {
		t.Fatalf("Find crossed module root and returned %q", found)
	}
	inside := filepath.Join(module, ".lifeline.toml")
	if err := os.WriteFile(inside, []byte("schema_version = 1\ntimeout = \"13s\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	found, err = Find(pkg)
	if err != nil {
		t.Fatal(err)
	}
	if found != inside {
		t.Fatalf("Find = %q, want module-root config %q", found, inside)
	}
}
