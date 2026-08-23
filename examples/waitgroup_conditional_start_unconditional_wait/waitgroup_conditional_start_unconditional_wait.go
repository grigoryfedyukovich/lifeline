package p

import "sync"

func Start(cond bool) {
	var wg sync.WaitGroup
	if cond {
		wg.Add(1)
		go func() { defer wg.Done() }()
	}
	wg.Wait()
}
