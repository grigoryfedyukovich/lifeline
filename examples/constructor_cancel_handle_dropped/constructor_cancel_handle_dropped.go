package p

import (
	"context"
	"fmt"
)

// Handle bundles a cancellation function together with an unrelated label
// field, returned by a constructor rather than built inline by its caller.
type Handle struct {
	label  string
	cancel context.CancelFunc
}

func NewHandle(parent context.Context) (*Handle, context.Context) {
	ctx, cancel := context.WithCancel(parent)
	return &Handle{label: "worker", cancel: cancel}, ctx
}

func Start(parent context.Context) {
	h, ctx := NewHandle(parent)
	go func() { <-ctx.Done() }()
	// h is used (satisfying the compiler), but only through a field
	// unrelated to the stored cancellation function -- the handle's
	// cancel field is never called, so the obligation NewHandle created
	// is dropped here.
	fmt.Println(h.label)
}
