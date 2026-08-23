package p

import "sync"

func Start(cond bool) {
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done() }()
	if cond {
		wg.Wait()
	}
}
