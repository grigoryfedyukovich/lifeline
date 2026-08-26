package constructor_waitgroup_handle_dropped

import "sync"

type Worker struct {
	wg sync.WaitGroup
}

func New() *Worker {
	w := &Worker{}
	w.wg.Add(1)
	go func() {
		defer w.wg.Done()
		select {}
	}()
	return w
}

func Start() {
	_ = New()
}
