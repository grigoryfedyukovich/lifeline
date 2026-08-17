package ctx_stop_loop_then_unstoppable

import "context"

func Start(ctx context.Context) {
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			}
		}
		for {
		}
	}()
}
