package waitgroupsecondroundunjoined

import "sync"

func process(int) {}

// Start reuses one WaitGroup for two separate rounds of work but only
// waits on the first round: the second round's worker is started and
// never joined before Start returns. Both the naive "was Wait ever
// observed" check and a purely whole-function Add/Done tally would call
// this clean -- Joined is satisfied by the first Wait() call found
// anywhere in the function, and the totals balance globally since the
// second worker does call Done() eventually -- but the second round's own
// Wait() never happens (computeGroupRoundBalances / model.JoinGroup.UnjoinedRound).
func Start(a, b int) {
	var workers sync.WaitGroup

	workers.Add(1)
	go func() {
		defer workers.Done()
		process(a)
	}()
	workers.Wait()

	workers.Add(1)
	go func() {
		defer workers.Done()
		process(b)
	}()
}
