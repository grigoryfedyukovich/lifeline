package join_owner_return_branch

import "sync"

func Start(fail bool) error {
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); work() }()
	if fail {
		return errValue()
	}
	wg.Wait()
	return nil
}

func work() {}
func errValue() error { return nil }
