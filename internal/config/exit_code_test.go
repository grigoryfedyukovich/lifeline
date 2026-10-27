package config

import (
	"fmt"
	"testing"
)

func TestCIExitCodeRange(t *testing.T) {
	for _, code := range []int{-256, -1, 0, 2, 3, 256, 257, 512, 65536} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			cfg := Default()
			cfg.CIExitCode = code
			if err := cfg.Validate(); err == nil {
				t.Fatalf("accepted invalid exit code %d", code)
			}
		})
	}
	for _, code := range []int{1, 4, 7, 127, 255} {
		cfg := Default()
		cfg.CIExitCode = code
		if err := cfg.Validate(); err != nil {
			t.Errorf("rejected %d: %v", code, err)
		}
	}
}
