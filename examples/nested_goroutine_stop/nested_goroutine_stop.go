// B1: a reachable inner stop cannot resolve the outer loop
package nested_goroutine_stop

import (
	"context"
)

func Start(ctx context.Context) {
	go func() {
		for {
			go func() {
				select {
				case <-ctx.Done():
					return
				}
			}()
		}
	}()
}
