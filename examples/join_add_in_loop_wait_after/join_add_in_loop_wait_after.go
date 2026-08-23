package join_add_in_loop_wait_after

import "sync"

func Start(n int) {
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); work() }()
	}
	wg.Wait()
}

func work() {}
