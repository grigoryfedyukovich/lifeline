package p

import "sync"

func Start() {
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done() }()
	hop1(&wg)
}

func hop1(wg *sync.WaitGroup) { hop2(wg) }
func hop2(wg *sync.WaitGroup) { hop3(wg) }
func hop3(wg *sync.WaitGroup) { /* leak the join */ }
