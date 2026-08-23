package join_two_groups_mixed

import "sync"

func Start() {
	var a, b sync.WaitGroup
	a.Add(1)
	b.Add(1)
	go func() { defer a.Done(); work() }()
	go func() { defer b.Done(); work() }()
	a.Wait()
	// b is intentionally not joined.
}

func work() {}
