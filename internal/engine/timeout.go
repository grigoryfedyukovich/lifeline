package engine

import (
	"fmt"
	"time"

	"github.com/gfedyukovich/lifeline/internal/config"
	"github.com/gfedyukovich/lifeline/internal/model"
	"github.com/gfedyukovich/lifeline/internal/version"
)

// TimeoutDiagnostic constructs the standard LL9001 notice for a cooperative
// deadline. The run status must still be marked separately with Status.WithTimeout;
// suppressing this diagnostic must never make a timed-out run look complete.
func TimeoutDiagnostic(cfg config.Config, elapsed time.Duration, packagePath string) Diagnostic {
	return Diagnostic{
		SchemaVersion: version.ReportSchema,
		RuleID:        "LL9001", Verdict: Unknown,
		Message:     fmt.Sprintf("analysis exceeded timeout %s after %s", cfg.Timeout, elapsed.Round(time.Millisecond)),
		Position:    model.Span{},
		Protocol:    "analysis-timeout",
		Evidence:    []model.Evidence{{Kind: "timeout", Message: "partial results, if any, are incomplete"}},
		Assumptions: []string{"the interrupted package may contain additional diagnostics"},
		Bounds:      map[string]any{"max_functions": cfg.MaxFunctions, "timeout": cfg.Timeout},
		ToolVersion: version.Version,
		Backend:     version.Backend,
		PackagePath: packagePath,
	}
}
