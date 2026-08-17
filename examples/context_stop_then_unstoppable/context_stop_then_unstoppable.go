package contextstopthenunstoppable

import "context"

func work() {}

func Start(ctx context.Context) {
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			default:
				work()
			}
		}

		for {
			work()
		}
	}()
}
