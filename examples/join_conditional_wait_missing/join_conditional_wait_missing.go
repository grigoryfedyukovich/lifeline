package join_conditional_wait_missing

import "sync"

func Start(wait bool) {
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); work() }()
	go func() { defer wg.Done(); work() }()
	if wait {
		wg.Wait()
	}
}

func work() {}
