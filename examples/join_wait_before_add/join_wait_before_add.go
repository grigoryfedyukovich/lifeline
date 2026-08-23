package join_wait_before_add

import "sync"

func Start() {
	var wg sync.WaitGroup
	wg.Wait()
	wg.Add(1)
	go func() { defer wg.Done(); work() }()
}

func work() {}
