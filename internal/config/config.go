package config

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const SchemaVersion = 1

type Config struct {
	SchemaVersion int      `json:"schema_version"`
	Format        string   `json:"format"`
	CIExitCode    int      `json:"ci_exit_code"`
	Timeout       string   `json:"timeout"`
	MaxFunctions  int      `json:"max_functions"`
	IncludeTests  bool     `json:"include_tests"`
	FailOn        []string `json:"fail_on"`
	// FailOnIncomplete makes a run whose analysis was cut short by a bound
	// or a deadline (engine.Status.Incomplete) fail with ci_exit_code,
	// whatever its diagnostics say and whether or not LL9001 is hidden. It
	// is a policy on the run status, separate from fail_on, which is a
	// policy on diagnostics (audit finding F8).
	FailOnIncomplete bool `json:"fail_on_incomplete"`
	// FailOnUnsupported does the same when any part of the input was judged
	// under an approximation (engine.Unsupported.Total() > 0): goroutine
	// targets that could not be inspected, obligations handed off to code
	// outside the owning function, or path checks left unestablished.
	FailOnUnsupported bool     `json:"fail_on_unsupported"`
	Ignore            []string `json:"ignore"`
	IgnorePaths       []string `json:"ignore_paths"`
	ContextWrappers   []string `json:"context_wrappers"`
	StartWrappers     []string `json:"start_wrappers"`
	JoinWrappers      []string `json:"join_wrappers"`
	StopWrappers      []string `json:"stop_wrappers"`
}

func Default() Config {
	return Config{
		SchemaVersion:   SchemaVersion,
		Format:          "text",
		CIExitCode:      1,
		Timeout:         "5s",
		MaxFunctions:    10000,
		FailOn:          []string{},
		Ignore:          []string{},
		IgnorePaths:     []string{},
		ContextWrappers: []string{},
		StartWrappers:   []string{},
		JoinWrappers:    []string{},
		StopWrappers:    []string{},
	}
}

// MatchesIgnorePath reports whether relPath should be excluded from
// analysis under IgnorePaths. Each pattern is tried against both the full
// path and its base filename, using filepath.Match semantics: a single "*"
// or "?" does not cross a "/" separator. There is no "**" support; a
// pattern intended to match at any depth should target the base filename
// instead (e.g. "*.pb.go" rather than a directory-spanning pattern).
func (c Config) MatchesIgnorePath(relPath string) bool {
	for _, pattern := range c.IgnorePaths {
		if matched, _ := filepath.Match(pattern, relPath); matched {
			return true
		}
		if matched, _ := filepath.Match(pattern, filepath.Base(relPath)); matched {
			return true
		}
	}
	return false
}

func (c Config) Duration() (time.Duration, error) {
	d, err := time.ParseDuration(c.Timeout)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("timeout must be a positive Go duration: %q", c.Timeout)
	}
	return d, nil
}

// SplitCSV parses comma-separated CLI values while discarding surrounding
// whitespace and empty entries. Configuration files use real string arrays.
func SplitCSV(s string) []string {
	var out []string
	for _, item := range strings.Split(s, ",") {
		if item = strings.TrimSpace(item); item != "" {
			out = append(out, item)
		}
	}
	return out
}

func (c Config) Validate() error {
	if c.SchemaVersion != SchemaVersion {
		return fmt.Errorf("unsupported schema_version %d (expected %d)", c.SchemaVersion, SchemaVersion)
	}
	switch c.Format {
	case "text", "json", "sarif":
	default:
		return fmt.Errorf("format must be text, json, or sarif, got %q", c.Format)
	}
	if c.CIExitCode <= 0 || c.CIExitCode > 255 || c.CIExitCode == 2 || c.CIExitCode == 3 {
		return errors.New("ci_exit_code must be in 1..255 and distinct from reserved exit codes 2 and 3")
	}
	if c.MaxFunctions <= 0 {
		return errors.New("max_functions must be positive")
	}
	if d, err := time.ParseDuration(c.Timeout); err != nil || d <= 0 {
		return fmt.Errorf("timeout must be a positive Go duration: %q", c.Timeout)
	}
	for _, id := range append(append([]string{}, c.FailOn...), c.Ignore...) {
		if id == "all" {
			continue
		}
		if !IsKnownRule(id) {
			return fmt.Errorf("unknown rule identifier %q", id)
		}
	}
	return nil
}

// IsKnownRule reports whether id names a diagnostic rule accepted by
// configuration and source suppression directives.
func IsKnownRule(id string) bool {
	switch id {
	case "LL1001", "LL1002", "LL1003", "LL1004", "LL1005", "LL9001":
		return true
	default:
		return false
	}
}

func Load(path string) (Config, error) {
	return LoadFrom(path, "")
}

// LoadFrom loads path relative to baseDir. When path is empty, configuration
// discovery starts at baseDir instead. An empty baseDir preserves the
// historical behavior of using the process working directory.
//
// Drivers should pass the Go module root here. go vet runs an analyzer with a
// package-specific working directory, so resolving paths directly against the
// process cwd would make one command interpret the same flag differently for
// each package.
func LoadFrom(path, baseDir string) (Config, error) {
	cfg := Default()
	if path == "" {
		found, err := Find(baseDir)
		if err != nil {
			return cfg, err
		}
		if found == "" {
			return cfg, nil
		}
		path = found
	} else if !filepath.IsAbs(path) && baseDir != "" {
		path = filepath.Join(baseDir, path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return cfg, fmt.Errorf("read config %s: %w", path, err)
	}
	data = bytes.TrimPrefix(data, []byte{0xEF, 0xBB, 0xBF})
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".json":
		err = decodeJSON(data, &cfg)
	case ".yaml", ".yml":
		err = decodeFlat(data, ':', &cfg)
	case ".toml":
		err = decodeFlat(data, '=', &cfg)
	default:
		err = fmt.Errorf("unsupported config extension %q; use .json, .yaml, .yml, or .toml", ext)
	}
	if err != nil {
		return cfg, fmt.Errorf("parse config %s: %w", path, err)
	}
	if err := cfg.Validate(); err != nil {
		return cfg, fmt.Errorf("invalid config %s: %w", path, err)
	}
	return cfg, nil
}

// ModuleRoot returns the nearest directory at or above start that contains a
// go.mod file. start may name either a file or a directory. If no module is
// found, the absolute starting directory is returned so callers still have a
// stable path base outside module mode.
func ModuleRoot(start string) (string, error) {
	dir, err := startingDir(start)
	if err != nil {
		return "", err
	}
	fallback := dir
	for {
		if st, statErr := os.Stat(filepath.Join(dir, "go.mod")); statErr == nil && !st.IsDir() {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return fallback, nil
		}
		dir = parent
	}
}

func Find(start string) (string, error) {
	dir, err := startingDir(start)
	if err != nil {
		return "", err
	}
	moduleRoot, hasModule := nearestModuleRoot(dir)
	names := []string{"lifeline.yaml", ".lifeline.yaml", "lifeline.yml", ".lifeline.yml", "lifeline.toml", ".lifeline.toml", "lifeline.json", ".lifeline.json"}
	for {
		for _, name := range names {
			p := filepath.Join(dir, name)
			if st, err := os.Stat(p); err == nil && !st.IsDir() {
				return p, nil
			}
		}
		if hasModule && dir == moduleRoot {
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return "", nil
}

func startingDir(start string) (string, error) {
	if start == "" {
		var err error
		start, err = os.Getwd()
		if err != nil {
			return "", err
		}
	}
	abs, err := filepath.Abs(start)
	if err != nil {
		return "", err
	}
	if st, statErr := os.Stat(abs); statErr == nil && !st.IsDir() {
		return filepath.Dir(abs), nil
	}
	return abs, nil
}

func nearestModuleRoot(startDir string) (string, bool) {
	for dir := startDir; ; dir = filepath.Dir(dir) {
		if st, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil && !st.IsDir() {
			return dir, true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
	}
}

func decodeJSON(data []byte, cfg *Config) error {
	if err := rejectDuplicateJSONKeys(data); err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(cfg); err != nil {
		return err
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		return errors.New("config contains multiple JSON values")
	}
	return nil
}

func rejectDuplicateJSONKeys(data []byte) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	if delim, ok := tok.(json.Delim); !ok || delim != '{' {
		return errors.New("config must be a JSON object")
	}
	seen := map[string]struct{}{}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return err
		}
		key, ok := tok.(string)
		if !ok {
			return errors.New("config contains a non-string JSON key")
		}
		if _, exists := seen[key]; exists {
			return fmt.Errorf("duplicate key %q", key)
		}
		seen[key] = struct{}{}
		var value json.RawMessage
		if err := dec.Decode(&value); err != nil {
			return err
		}
	}
	if _, err := dec.Token(); err != nil {
		return err
	}
	return nil
}

var setters = map[string]func(*Config, any) error{
	"schema_version":      func(c *Config, v any) error { n, e := asInt(v); c.SchemaVersion = n; return e },
	"format":              func(c *Config, v any) error { s, e := asString(v); c.Format = s; return e },
	"ci_exit_code":        func(c *Config, v any) error { n, e := asInt(v); c.CIExitCode = n; return e },
	"timeout":             func(c *Config, v any) error { s, e := asString(v); c.Timeout = s; return e },
	"max_functions":       func(c *Config, v any) error { n, e := asInt(v); c.MaxFunctions = n; return e },
	"include_tests":       func(c *Config, v any) error { b, e := asBool(v); c.IncludeTests = b; return e },
	"fail_on":             func(c *Config, v any) error { s, e := asStrings(v); c.FailOn = s; return e },
	"fail_on_incomplete":  func(c *Config, v any) error { b, e := asBool(v); c.FailOnIncomplete = b; return e },
	"fail_on_unsupported": func(c *Config, v any) error { b, e := asBool(v); c.FailOnUnsupported = b; return e },
	"ignore":              func(c *Config, v any) error { s, e := asStrings(v); c.Ignore = s; return e },
	"ignore_paths":        func(c *Config, v any) error { s, e := asStrings(v); c.IgnorePaths = s; return e },
	"context_wrappers":    func(c *Config, v any) error { s, e := asStrings(v); c.ContextWrappers = s; return e },
	"start_wrappers":      func(c *Config, v any) error { s, e := asStrings(v); c.StartWrappers = s; return e },
	"join_wrappers":       func(c *Config, v any) error { s, e := asStrings(v); c.JoinWrappers = s; return e },
	"stop_wrappers":       func(c *Config, v any) error { s, e := asStrings(v); c.StopWrappers = s; return e },
}

// decodeFlat deliberately implements a strict, reproducible subset sufficient
// for Lifeline's flat configuration. YAML supports "key: value" and indented
// string lists. TOML supports "key = value" and string arrays.
func decodeFlat(data []byte, sep byte, cfg *Config) error {
	s := bufio.NewScanner(bytes.NewReader(data))
	lineNo := 0
	pendingKey := ""
	pendingLine := 0
	var pendingValues []string
	seen := map[string]int{}
	flushPending := func() error {
		if pendingKey == "" {
			return nil
		}
		if err := setters[pendingKey](cfg, pendingValues); err != nil {
			return fmt.Errorf("line %d: key %s: %w", pendingLine, pendingKey, err)
		}
		pendingKey = ""
		pendingLine = 0
		pendingValues = nil
		return nil
	}
	for s.Scan() {
		lineNo++
		raw := strings.TrimSpace(stripComment(s.Text()))
		if raw == "" {
			continue
		}
		if strings.HasPrefix(raw, "-") {
			if sep != ':' || pendingKey == "" {
				return fmt.Errorf("line %d: list item without a preceding key", lineNo)
			}
			item, err := asString(strings.TrimSpace(strings.TrimPrefix(raw, "-")))
			if err != nil {
				return fmt.Errorf("line %d: %w", lineNo, err)
			}
			pendingValues = append(pendingValues, item)
			continue
		}
		if err := flushPending(); err != nil {
			return err
		}
		idx := strings.IndexByte(raw, sep)
		if idx <= 0 {
			return fmt.Errorf("line %d: expected %q separator", lineNo, string(sep))
		}
		key := strings.TrimSpace(raw[:idx])
		val := strings.TrimSpace(raw[idx+1:])
		set, ok := setters[key]
		if !ok {
			return fmt.Errorf("line %d: unknown key %q", lineNo, key)
		}
		if firstLine, exists := seen[key]; exists {
			return fmt.Errorf("line %d: duplicate key %q (first set on line %d)", lineNo, key, firstLine)
		}
		seen[key] = lineNo
		if val == "" && sep == ':' {
			pendingKey = key
			pendingLine = lineNo
			pendingValues = nil
			continue
		}
		parsed, err := parseValue(val)
		if err != nil {
			return fmt.Errorf("line %d: %w", lineNo, err)
		}
		if err := set(cfg, parsed); err != nil {
			return fmt.Errorf("line %d: key %s: %w", lineNo, key, err)
		}
	}
	if err := s.Err(); err != nil {
		return err
	}
	return flushPending()
}

func stripComment(s string) string {
	var quote byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		if quote == '"' && c == '\\' {
			i++ // the escaped byte remains inside the double-quoted string
			continue
		}
		if quote == '\'' && c == '\'' && i+1 < len(s) && s[i+1] == '\'' {
			i++ // YAML-style doubled single quote
			continue
		}
		switch {
		case quote == 0 && (c == '\'' || c == '"'):
			quote = c
		case quote != 0 && c == quote:
			quote = 0
		case quote == 0 && c == '#':
			return s[:i]
		}
	}
	return s
}

func parseValue(v string) (any, error) {
	v = strings.TrimSpace(v)
	if strings.HasPrefix(v, "[") {
		if !strings.HasSuffix(v, "]") {
			return nil, errors.New("unterminated array")
		}
		inside := strings.TrimSpace(v[1 : len(v)-1])
		if inside == "" {
			return []string{}, nil
		}
		parts, err := splitArrayElements(inside)
		if err != nil {
			return nil, err
		}
		out := make([]string, 0, len(parts))
		for _, p := range parts {
			s, err := asString(strings.TrimSpace(p))
			if err != nil {
				return nil, err
			}
			out = append(out, s)
		}
		return out, nil
	}
	if v == "true" || v == "false" {
		return v == "true", nil
	}
	if n, err := strconv.Atoi(v); err == nil {
		return n, nil
	}
	return asString(v)
}

func splitArrayElements(s string) ([]string, error) {
	var parts []string
	start := 0
	var quote byte
	escaped := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if escaped {
			escaped = false
			continue
		}
		if quote == '"' && c == '\\' {
			escaped = true
			continue
		}
		if quote == '\'' && c == '\'' && i+1 < len(s) && s[i+1] == '\'' {
			i++
			continue
		}
		switch {
		case quote == 0 && (c == '\'' || c == '"'):
			quote = c
		case quote != 0 && c == quote:
			quote = 0
		case quote == 0 && c == ',':
			part := strings.TrimSpace(s[start:i])
			if part == "" {
				return nil, errors.New("array contains an empty element")
			}
			parts = append(parts, part)
			start = i + 1
		}
	}
	if quote != 0 {
		return nil, errors.New("unterminated quoted array element")
	}
	part := strings.TrimSpace(s[start:])
	if part == "" {
		return nil, errors.New("array contains an empty element")
	}
	return append(parts, part), nil
}

func asString(v any) (string, error) {
	s, ok := v.(string)
	if !ok {
		return "", fmt.Errorf("expected string, got %T", v)
	}
	s = strings.TrimSpace(s)
	if s == "" {
		return "", errors.New("string value cannot be empty")
	}
	if s[0] == '"' {
		if len(s) < 2 || s[len(s)-1] != '"' {
			return "", errors.New("unterminated double-quoted string")
		}
		unquoted, err := strconv.Unquote(s)
		if err != nil {
			return "", fmt.Errorf("invalid double-quoted string: %w", err)
		}
		s = unquoted
	} else if s[0] == '\'' {
		if len(s) < 2 || s[len(s)-1] != '\'' {
			return "", errors.New("unterminated single-quoted string")
		}
		s = strings.ReplaceAll(s[1:len(s)-1], "''", "'")
	} else if s[len(s)-1] == '"' || s[len(s)-1] == '\'' {
		return "", errors.New("unexpected closing quote in string value")
	}
	if s == "" {
		return "", errors.New("string value cannot be empty")
	}
	return s, nil
}

func asStrings(v any) ([]string, error) {
	s, ok := v.([]string)
	if !ok {
		return nil, fmt.Errorf("expected string list, got %T", v)
	}
	return s, nil
}

func asInt(v any) (int, error) {
	n, ok := v.(int)
	if !ok {
		return 0, fmt.Errorf("expected integer, got %T", v)
	}
	return n, nil
}

func asBool(v any) (bool, error) {
	b, ok := v.(bool)
	if !ok {
		return false, fmt.Errorf("expected boolean, got %T", v)
	}
	return b, nil
}
