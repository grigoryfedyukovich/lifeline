package join_helper_wait

import "sync"

func waitFor(wg *sync.WaitGroup) { wg.Wait() }

func Start() {
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); work() }()
	waitFor(&wg)
}

func work() {}
