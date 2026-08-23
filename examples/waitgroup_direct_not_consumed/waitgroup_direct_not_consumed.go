package p

import "sync"

func ignore(wg *sync.WaitGroup) {}

func Start() {
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done() }()
	ignore(&wg)
}
