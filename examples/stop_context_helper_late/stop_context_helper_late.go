// Stop-before-wait through context helper
package stop_context_helper_late

import (
	"context"
	"sync"
)

type holder struct{ ctx context.Context }
type service struct{ worker holder }

func waitContext(s service) { <-s.worker.ctx.Done() }
func Start(parent context.Context) {
	ctx, cancel := context.WithCancel(parent)
	s := service{worker: holder{ctx: ctx}}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); waitContext(s) }()
	wg.Wait()
	cancel()
}
