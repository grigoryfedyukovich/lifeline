package propercancel

import (
	"context"
	"time"
)

// Work owns the derived context and releases its timer/cancellation resources.
func Work(parent context.Context) error {
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()
	return run(ctx)
}

func run(context.Context) error { return nil }
