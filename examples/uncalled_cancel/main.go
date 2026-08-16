package uncalledcancel

import "context"

// Start creates a cancellation obligation but neither calls nor transfers it.
func Start(parent context.Context) context.Context {
	ctx, cancel := context.WithCancel(parent)
	_ = cancel
	return ctx
}
