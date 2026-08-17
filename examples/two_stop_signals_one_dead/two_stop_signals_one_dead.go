package twostopsignalsonedead

import "context"

func work() {}

func Start(ctx context.Context) {
	go func() {
		for {
			select {
			case <-ctx.Done():
				if false {
					return
				}
			case <-make(chan struct{}):
				work()
			default:
				work()
			}
		}
	}()
}
