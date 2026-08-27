package p

import "context"

// Handle bundles a cancellation function with an unrelated label field, the
// same as the cancel_stored_struct negative counterpart to this example --
// the only difference is that h.cancel is actually called here.
type Handle struct {
	label  string
	cancel context.CancelFunc
}

func Start() {
	ctx, cancel := context.WithCancel(context.Background())
	h := &Handle{label: "worker", cancel: cancel}
	go func() { <-ctx.Done() }()
	h.cancel()
}
