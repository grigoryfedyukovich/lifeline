package p

import "sync"

// Handle bundles a *sync.WaitGroup with an unrelated label field, the same
// as the waitgroup_stored_struct negative counterpart to this example --
// the only difference is that h.wg.Wait() is actually called here.
type Handle struct {
	label string
	wg    *sync.WaitGroup
}

func Start() {
	var wg sync.WaitGroup
	wg.Add(1)
	h := &Handle{label: "worker", wg: &wg}
	go func() {
		defer wg.Done()
	}()
	h.wg.Wait()
}
