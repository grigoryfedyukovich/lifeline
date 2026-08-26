package waitgroup_stored_struct

import "sync"

type workerSet struct {
	wg sync.WaitGroup
}

func Start() {
	var s workerSet
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		select {}
	}()
}
