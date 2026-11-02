// An asynchronous helper wait does not join the owner
package join_helper_owner

import (
	"sync"
)

func join(wg *sync.WaitGroup) { wg.Wait() }
func Start()                  { var wg sync.WaitGroup; wg.Add(1); go func() { defer wg.Done() }(); join(&wg) }
