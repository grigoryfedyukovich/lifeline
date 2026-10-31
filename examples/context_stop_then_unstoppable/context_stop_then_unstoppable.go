// Package contextstopthenunstoppable has two loops in one goroutine: the
// first exits on ctx.Done(), the second never exits, so the goroutine still
// has an unresolved persistent loop. This also covers the "two loops, one
// unstoppable" shape.
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
