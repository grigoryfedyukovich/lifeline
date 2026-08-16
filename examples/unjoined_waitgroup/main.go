package unjoinedwaitgroup

import "sync"

func process(int) {}

// Start accounts for a worker but returns without joining it.
func Start(value int) {
	var workers sync.WaitGroup
	workers.Add(1)
	go func() {
		defer workers.Done()
		process(value)
	}()
}
