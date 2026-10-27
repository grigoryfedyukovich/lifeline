package async_consumer

import (
	"github.com/gfedyukovich/lifeline/tests/testdata/facts/async_worker"
	"sync"
)

func Start() {
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done() }()
	async_worker.Wait(&wg)
}
