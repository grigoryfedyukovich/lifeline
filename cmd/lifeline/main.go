package main

import (
	"os"
	"strings"

	"golang.org/x/tools/go/analysis/unitchecker"

	"github.com/gfedyukovich/lifeline/analyzer"
	"github.com/gfedyukovich/lifeline/internal/standalone"
)

func main() {
	if isVetInvocation(os.Args[1:]) {
		unitchecker.Main(analyzer.Analyzer)
		return
	}
	os.Exit(standalone.Main(os.Args[1:], os.Stdout, os.Stderr))
}

func isVetInvocation(args []string) bool {
	for _, arg := range args {
		if arg == "-flags" || arg == "help" || strings.HasPrefix(arg, "-V=") || strings.HasSuffix(arg, ".cfg") {
			return true
		}
	}
	return false
}
