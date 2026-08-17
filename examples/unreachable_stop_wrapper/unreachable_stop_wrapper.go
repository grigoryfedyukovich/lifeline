package unreachablestopwrapper

import "context"

func StopWorker() {}
func work()       {}

func Start(ctx context.Context) {
	go func() {
		for {
			if false {
				StopWorker()
			}
			work()
		}
	}()
}
