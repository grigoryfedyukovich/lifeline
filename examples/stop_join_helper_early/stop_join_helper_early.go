// Stop-before-wait through join helper
package stop_join_helper_early

import (
	"context"
	"sync"
)

func join(wg *sync.WaitGroup) { wg.Wait() }
func Start(parent context.Context) {
	ctx, cancel := context.WithCancel(parent)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); <-ctx.Done() }()
	cancel()
	join(&wg)
}
