// Stop-before-wait through array channel
package stop_array_channel_early

import (
	"sync"
)

func Start() {
	stop := make(chan struct{})
	stops := [2]chan struct{}{stop, nil}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		select {
		case <-stops[0]:
			return
		}
	}()
	close(stops[0])
	wg.Wait()
}
