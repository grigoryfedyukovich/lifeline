package context_wrapper_error_early_return

import (
	"context"
	"errors"
)

var errUnavailable = errors.New("unavailable")

func Make(parent context.Context, ok bool) (context.Context, context.CancelFunc, error) {
	if !ok {
		return nil, nil, errUnavailable
	}
	ctx, cancel := context.WithCancel(parent)
	return ctx, cancel, nil
}

// The error check is handled, but a second, unrelated early return after a
// successful Make skips the deferred cancel: that path leaks the context.
func Start(parent context.Context, ok, skip bool) error {
	ctx, cancel, err := Make(parent, ok)
	if err != nil {
		return err
	}
	if skip {
		return nil
	}
	defer cancel()
	go func() {
		<-ctx.Done()
	}()
	return nil
}
