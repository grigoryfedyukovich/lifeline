package p

import (
	"context"
	"fmt"
)

// Handle bundles a cancellation function together with an unrelated label
// field, to show the two are tracked independently: reading label is not
// mistaken for consuming cancel.
type Handle struct {
	label  string
	cancel context.CancelFunc
}

func Start() {
	ctx, cancel := context.WithCancel(context.Background())
	h := &Handle{label: "worker", cancel: cancel}
	go func() { <-ctx.Done() }()
	// h itself is used (satisfying the compiler), but only through a field
	// unrelated to the stored cancellation function -- h.cancel is never
	// called anywhere.
	fmt.Println(h.label)
}
