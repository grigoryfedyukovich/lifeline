package p

import (
	"fmt"
	"sync"
)

// Handle bundles a *sync.WaitGroup together with an unrelated label field.
// NewHandle both starts the one worker it accounts for and returns the
// handle for its caller to join later.
type Handle struct {
	label string
	wg    *sync.WaitGroup
}

func NewHandle() *Handle {
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
	}()
	return &Handle{label: "worker", wg: &wg}
}

func Start() {
	h := NewHandle()
	// h is used (satisfying the compiler), but only through a field
	// unrelated to the stored group -- the handle's wg field is never
	// waited on, so the join obligation NewHandle created is dropped here.
	fmt.Println(h.label)
}
