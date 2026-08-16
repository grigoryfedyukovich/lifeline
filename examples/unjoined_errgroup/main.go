package unjoinederrgroup

import (
	"context"

	"golang.org/x/sync/errgroup"
)

// Start launches an errgroup worker but drops the group without Wait.
func Start(ctx context.Context) {
	group, ctx := errgroup.WithContext(ctx)
	group.Go(func() error {
		return serve(ctx)
	})
}

func serve(context.Context) error { return nil }
