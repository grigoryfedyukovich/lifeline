// Stop-before-wait through join wrapper
package stop_join_wrapper_early

import (
	"context"
	"sync"
)

// Join stands for an opaque, configured join operation.
func Join(wg *sync.WaitGroup) {}
func Start(parent context.Context) {
	ctx, cancel := context.WithCancel(parent)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); <-ctx.Done() }()
	cancel()
	Join(&wg)
}
