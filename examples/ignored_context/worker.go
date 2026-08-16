package ignoredcontext

import "context"

var queue = make(chan int)

func process(int) {}

func Start(ctx context.Context) {
	go func() {
		for {
			process(<-queue)
		}
	}()
}
