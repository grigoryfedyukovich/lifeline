package conditional_ctx_done

import "context"

func Start(ctx context.Context) {
	go func() {
		for {
			select {
			case <-ctx.Done():
				if shouldExit() {
					return
				}
			default:
			}
		}
	}()
}

func shouldExit() bool { return false }
