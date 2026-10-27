package async_errgroup

import "golang.org/x/sync/errgroup"

func Start() { var group errgroup.Group; group.Go(func() error { return nil }); go group.Wait() }
