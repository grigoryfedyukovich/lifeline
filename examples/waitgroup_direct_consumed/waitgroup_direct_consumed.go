package p

import "sync"

func consume(wg *sync.WaitGroup) { wg.Wait() }

func Start() {
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done() }()
	consume(&wg)
}
