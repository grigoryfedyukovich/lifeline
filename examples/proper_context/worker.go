package propercontext

import "context"

var queue = make(chan int)

func process(int) {}

// Start launches a worker whose owner can stop it by cancelling ctx.
func Start(ctx context.Context) {
	go func() {
		for {
			select {
			case item := <-queue:
				process(item)
			case <-ctx.Done():
				return
			}
		}
	}()
}
