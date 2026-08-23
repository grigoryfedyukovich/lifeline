package join_done_on_wrong_group

import "sync"

func Start() {
	var a, b sync.WaitGroup
	a.Add(1)
	b.Add(1)
	go func() { defer b.Done(); work() }()
	a.Wait()
}

func work() {}
