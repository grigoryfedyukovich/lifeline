package propererrgroup

import (
	"context"

	"golang.org/x/sync/errgroup"
)

func Serve(ctx context.Context) error {
	g, ctx := errgroup.WithContext(ctx)
	g.Go(func() error { return serve(ctx) })
	return g.Wait()
}

func serve(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}
