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
	SchemaVersion   int      `json:"schema_version"`
	Format          string   `json:"format"`
	CIExitCode      int      `json:"ci_exit_code"`
	Timeout         string   `json:"timeout"`
	MaxFunctions    int      `json:"max_functions"`
	IncludeTests    bool     `json:"include_tests"`
	FailOn          []string `json:"fail_on"`
	Ignore          []string `json:"ignore"`
	ContextWrappers []string `json:"context_wrappers"`
	StartWrappers   []string `json:"start_wrappers"`
	JoinWrappers    []string `json:"join_wrappers"`
	StopWrappers    []string `json:"stop_wrappers"`
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
		ContextWrappers: []string{},
		StartWrappers:   []string{},
		JoinWrappers:    []string{},
		StopWrappers:    []string{},
	}
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
	if c.CIExitCode <= 0 || c.CIExitCode == 2 || c.CIExitCode == 3 {
		return errors.New("ci_exit_code must be positive and distinct from reserved exit codes 2 and 3")
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
		if !knownRule(id) {
			return fmt.Errorf("unknown rule identifier %q", id)
		}
	}
	return nil
}

func knownRule(id string) bool {
	switch id {
	case "LL1001", "LL1002", "LL1003", "LL1004", "LL9001":
		return true
	default:
		return false
	}
}

func Load(path string) (Config, error) {
	cfg := Default()
	if path == "" {
		found, err := Find("")
		if err != nil {
			return cfg, err
		}
		if found == "" {
			return cfg, nil
		}
		path = found
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return cfg, fmt.Errorf("read config %s: %w", path, err)
	}
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

func Find(start string) (string, error) {
	if start == "" {
		var err error
		start, err = os.Getwd()
		if err != nil {
			return "", err
		}
	}
	start, err := filepath.Abs(start)
	if err != nil {
		return "", err
	}
	names := []string{"lifeline.yaml", ".lifeline.yaml", "lifeline.yml", ".lifeline.yml", "lifeline.toml", ".lifeline.toml", "lifeline.json", ".lifeline.json"}
	for dir := start; ; dir = filepath.Dir(dir) {
		for _, name := range names {
			p := filepath.Join(dir, name)
			if st, err := os.Stat(p); err == nil && !st.IsDir() {
				return p, nil
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
	}
	return "", nil
}

func decodeJSON(data []byte, cfg *Config) error {
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

var setters = map[string]func(*Config, any) error{
	"schema_version":   func(c *Config, v any) error { n, e := asInt(v); c.SchemaVersion = n; return e },
	"format":           func(c *Config, v any) error { s, e := asString(v); c.Format = s; return e },
	"ci_exit_code":     func(c *Config, v any) error { n, e := asInt(v); c.CIExitCode = n; return e },
	"timeout":          func(c *Config, v any) error { s, e := asString(v); c.Timeout = s; return e },
	"max_functions":    func(c *Config, v any) error { n, e := asInt(v); c.MaxFunctions = n; return e },
	"include_tests":    func(c *Config, v any) error { b, e := asBool(v); c.IncludeTests = b; return e },
	"fail_on":          func(c *Config, v any) error { s, e := asStrings(v); c.FailOn = s; return e },
	"ignore":           func(c *Config, v any) error { s, e := asStrings(v); c.Ignore = s; return e },
	"context_wrappers": func(c *Config, v any) error { s, e := asStrings(v); c.ContextWrappers = s; return e },
	"start_wrappers":   func(c *Config, v any) error { s, e := asStrings(v); c.StartWrappers = s; return e },
	"join_wrappers":    func(c *Config, v any) error { s, e := asStrings(v); c.JoinWrappers = s; return e },
	"stop_wrappers":    func(c *Config, v any) error { s, e := asStrings(v); c.StopWrappers = s; return e },
}

// decodeFlat deliberately implements a strict, reproducible subset sufficient
// for Lifeline's flat configuration. YAML supports "key: value" and indented
// string lists. TOML supports "key = value" and string arrays.
func decodeFlat(data []byte, sep byte, cfg *Config) error {
	s := bufio.NewScanner(bytes.NewReader(data))
	lineNo := 0
	pending := ""
	lists := map[string][]string{}
	for s.Scan() {
		lineNo++
		raw := strings.TrimSpace(stripComment(s.Text()))
		if raw == "" {
			continue
		}
		if strings.HasPrefix(raw, "-") {
			if sep != ':' || pending == "" {
				return fmt.Errorf("line %d: list item without a preceding key", lineNo)
			}
			item, err := asString(strings.TrimSpace(strings.TrimPrefix(raw, "-")))
			if err != nil {
				return fmt.Errorf("line %d: %w", lineNo, err)
			}
			lists[pending] = append(lists[pending], item)
			continue
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
		pending = ""
		if val == "" && sep == ':' {
			pending = key
			lists[key] = nil
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
	for key, values := range lists {
		if err := setters[key](cfg, values); err != nil {
			return fmt.Errorf("key %s: %w", key, err)
		}
	}
	return nil
}

func stripComment(s string) string {
	quote := rune(0)
	for i, r := range s {
		switch {
		case quote == 0 && (r == '\'' || r == '"'):
			quote = r
		case quote != 0 && r == quote:
			quote = 0
		case quote == 0 && r == '#':
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
		if quote != 0 && c == '\\' {
			escaped = true
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
	if len(s) >= 2 && ((s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '\'' && s[len(s)-1] == '\'')) {
		s = s[1 : len(s)-1]
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
