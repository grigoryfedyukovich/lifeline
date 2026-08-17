package conditional_stop_case

import "context"

func Start(ctx context.Context) {
	go func() {
		for {
			select {
			case <-ctx.Done():
				if shouldExit() {
					return
				}
			}
		}
	}()
}

func shouldExit() bool { return false }
