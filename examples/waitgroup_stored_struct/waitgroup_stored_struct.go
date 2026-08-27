package p

import (
	"fmt"
	"sync"
)

// Handle bundles a *sync.WaitGroup with an unrelated label field, to show
// the two are tracked independently: reading label is not mistaken for
// joining wg.
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
	// h itself is used (satisfying the compiler), but only through a field
	// unrelated to the stored group -- h.wg.Wait() is never called
	// anywhere.
	fmt.Println(h.label)
}
