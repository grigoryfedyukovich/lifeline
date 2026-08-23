package p

import "sync"

func Start() {
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done() }()
	wg.Wait()
}
