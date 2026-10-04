package context_wrapper_error_checked

import (
	"context"
	"errors"
)

var errUnavailable = errors.New("unavailable")

// Make follows the usual Go contract: when the error is non-nil there is
// no cancel function to call.
func Make(parent context.Context, ok bool) (context.Context, context.CancelFunc, error) {
	if !ok {
		return nil, nil, errUnavailable
	}
	ctx, cancel := context.WithCancel(parent)
	return ctx, cancel, nil
}

// Start returns early on the error path, before any cancel function
// exists. That path owes no call, so this is clean.
func Start(parent context.Context, ok bool) error {
	ctx, cancel, err := Make(parent, ok)
	if err != nil {
		return err
	}
	defer cancel()
	go func() {
		<-ctx.Done()
	}()
	return nil
}
