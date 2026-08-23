package join_multiple_adds_one_wait

import "sync"

func Start(n int) {
	var wg sync.WaitGroup
	if n > 0 {
		wg.Add(1)
		go func() { defer wg.Done(); work() }()
	}
	if n > 1 {
		wg.Add(1)
		go func() { defer wg.Done(); work() }()
	}
	wg.Wait()
}

func work() {}
