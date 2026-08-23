package stop_before_wait

import (
	"context"
	"sync"
)

func Start(parent context.Context) {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-ctx.Done()
	}()
	cancel()
	wg.Wait()
}
