package properwaitgroup

import "sync"

func process(int) {}

// Run owns both worker startup and the matching join.
func Run(value int) {
	var workers sync.WaitGroup
	workers.Add(1)
	go func() {
		defer workers.Done()
		process(value)
	}()
	workers.Wait()
}
