package join_branch_wait_before_return

import "sync"

func Start(ok bool) {
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		work()
	}()
	if ok {
		wg.Wait()
		return
	}
	wg.Wait()
}

func work() {}
