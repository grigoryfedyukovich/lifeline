package status_truncated_unsupported

import (
	"context"
	"sync"
)

// Four named functions and no diagnostics: a run bounded below four
// functions is incomplete even though nothing it did analyze reports.

func a() {}
func b() {}
func c() {}

func Start(parent context.Context) {
	_, cancel := context.WithCancel(parent)
	defer cancel()
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done() }()
	wg.Wait()
}
