// An asynchronous helper wait does not join the owner
package join_helper_async

import (
	"sync"
)

func join(wg *sync.WaitGroup) { wg.Wait() }
func Start()                  { var wg sync.WaitGroup; wg.Add(1); go func() { defer wg.Done() }(); go join(&wg) }
