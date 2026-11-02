// Stop-before-wait through pointer alias
package stop_pointer_alias_early

import (
	"sync"
)

type holder struct{ stop chan struct{} }

func Start() {
	stop := make(chan struct{})
	h := &holder{}
	alias := h
	alias.stop = stop
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		select {
		case <-h.stop:
			return
		}
	}()
	close(stop)
	wg.Wait()
}
