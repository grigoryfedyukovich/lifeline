package standalone

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/gfedyukovich/lifeline/internal/config"
	"github.com/gfedyukovich/lifeline/internal/engine"
	"github.com/gfedyukovich/lifeline/internal/model"
	"github.com/gfedyukovich/lifeline/internal/report"
	"github.com/gfedyukovich/lifeline/internal/version"
)

const (
	ExitOK       = 0
	ExitInvalid  = 2
	ExitInternal = 3
)

type options struct {
	configPath   string
	format       string
	failOn       string
	ciExitCode   int
	timeout      string
	maxFunctions int
	includeTests bool
	printConfig  bool
	showVersion  bool
	dump         string
	dumpFormat   string
}

func Main(args []string, stdout, stderr io.Writer) (exit int) {
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintf(stderr, "lifeline: internal error: %v\nreproduce with: lifeline %s\n", r, strings.Join(args, " "))
			exit = ExitInternal
		}
	}()
	fs := flag.NewFlagSet("lifeline", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var opts options
	fs.StringVar(&opts.configPath, "config", "", "path to lifecycle configuration")
	fs.StringVar(&opts.format, "format", "", "output format: text, json, or sarif")
	fs.StringVar(&opts.failOn, "fail-on", "", "comma-separated rule IDs or all")
	fs.IntVar(&opts.ciExitCode, "ci-exit-code", 0, "exit code used when fail-on policy matches")
	fs.StringVar(&opts.timeout, "timeout", "", "overall analysis timeout")
	fs.IntVar(&opts.maxFunctions, "max-functions", 0, "maximum functions analyzed per package")
	fs.BoolVar(&opts.includeTests, "tests", false, "include same-package _test.go files")
	fs.BoolVar(&opts.printConfig, "print-config", false, "print effective configuration and exit")
	fs.BoolVar(&opts.showVersion, "version", false, "print version and exit")
	fs.StringVar(&opts.dump, "dump", "", "bypass diagnostics and dump an intermediate representation instead; currently only \"cfg\" is supported")
	fs.StringVar(&opts.dumpFormat, "dump-format", "text", "format for -dump: text or dot")
	fs.Usage = func() {
		fmt.Fprintf(stderr, "Usage: lifeline [flags] [package patterns]\n\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return ExitInvalid
	}
	if opts.showVersion {
		fmt.Fprintf(stdout, "%s %s (%s)\n", version.Tool, version.Version, version.Backend)
		return ExitOK
	}
	cfg, err := config.Load(opts.configPath)
	if err != nil {
		fmt.Fprintf(stderr, "lifeline: %v\n", err)
		return ExitInvalid
	}
	visited := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { visited[f.Name] = true })
	if visited["format"] {
		cfg.Format = opts.format
	}
	if visited["fail-on"] {
		cfg.FailOn = config.SplitCSV(opts.failOn)
	}
	if visited["ci-exit-code"] {
		cfg.CIExitCode = opts.ciExitCode
	}
	if visited["timeout"] {
		cfg.Timeout = opts.timeout
	}
	if visited["max-functions"] {
		cfg.MaxFunctions = opts.maxFunctions
	}
	if visited["tests"] {
		cfg.IncludeTests = opts.includeTests
	}
	if err := cfg.Validate(); err != nil {
		fmt.Fprintf(stderr, "lifeline: invalid effective configuration: %v\n", err)
		return ExitInvalid
	}
	if opts.printConfig {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(cfg); err != nil {
			fmt.Fprintf(stderr, "lifeline: encode config: %v\n", err)
			return ExitInternal
		}
		return ExitOK
	}
	patterns := fs.Args()
	if len(patterns) == 0 {
		patterns = []string{"./..."}
	}
	if opts.dump != "" {
		if opts.dump != "cfg" {
			fmt.Fprintf(stderr, "lifeline: unsupported -dump value %q (supported: cfg)\n", opts.dump)
			return ExitInvalid
		}
		if opts.dumpFormat != "text" && opts.dumpFormat != "dot" {
			fmt.Fprintf(stderr, "lifeline: unsupported -dump-format value %q (supported: text, dot)\n", opts.dumpFormat)
			return ExitInvalid
		}
		duration, err := cfg.Duration()
		if err != nil {
			fmt.Fprintf(stderr, "lifeline: invalid effective configuration: %v\n", err)
			return ExitInvalid
		}
		ctx, cancel := context.WithTimeout(context.Background(), duration)
		defer cancel()
		if err := dumpCFGs(ctx, patterns, cfg, opts.dumpFormat, stdout); err != nil {
			fmt.Fprintf(stderr, "lifeline: %v\n", err)
			return ExitInvalid
		}
		return ExitOK
	}
	duration, err := cfg.Duration()
	if err != nil {
		fmt.Fprintf(stderr, "lifeline: invalid effective configuration: %v\n", err)
		return ExitInvalid
	}
	ctx, cancel := context.WithTimeout(context.Background(), duration)
	defer cancel()
	start := time.Now()
	diags, coverage, err := analyzePatterns(ctx, patterns, cfg)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			diags = append(diags, timeoutDiagnostic(cfg, time.Since(start)))
		} else {
			fmt.Fprintf(stderr, "lifeline: %v\n", err)
			return ExitInvalid
		}
	}
	cwd, _ := os.Getwd()
	if err := report.Write(stdout, cfg.Format, diags, coverage, cwd); err != nil {
		fmt.Fprintf(stderr, "lifeline: render report: %v\n", err)
		return ExitInternal
	}
	if engine.FailsPolicy(diags, cfg.FailOn) {
		return cfg.CIExitCode
	}
	return ExitOK
}

func timeoutDiagnostic(cfg config.Config, elapsed time.Duration) engine.Diagnostic {
	return engine.Diagnostic{
		SchemaVersion: version.ReportSchema,
		RuleID:        "LL9001", Verdict: engine.Unknown,
		Message:  fmt.Sprintf("analysis exceeded timeout %s after %s", cfg.Timeout, elapsed.Round(time.Millisecond)),
		Position: model.Span{}, Protocol: "analysis-timeout",
		Evidence:    []model.Evidence{{Kind: "timeout", Message: "partial results, if any, are incomplete"}},
		Assumptions: []string{"the interrupted package may contain additional diagnostics"},
		Bounds:      map[string]any{"max_functions": cfg.MaxFunctions, "timeout": cfg.Timeout},
		ToolVersion: version.Version, Backend: version.Backend,
	}
}
