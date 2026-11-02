// Stop-before-wait through promoted context
package stop_promoted_context_late

import (
	"context"
	"sync"
)

type embedded struct{ ctx context.Context }
type service struct{ embedded }

func Start(parent context.Context) {
	ctx, cancel := context.WithCancel(parent)
	s := service{embedded: embedded{ctx: ctx}}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); <-s.ctx.Done() }()
	wg.Wait()
	cancel()
}
