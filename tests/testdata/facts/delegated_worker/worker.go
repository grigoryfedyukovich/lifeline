package delegatedworker

import "context"

func ignore(context.Context) {}

// Run hands ctx to a call that returns normally and then loops forever.
// The delegation is not inside the loop, so it cannot be that loop's stop
// path: the exported fact must say the loop is unresolved (audit finding
// F1). Before the fix, the delegation was modeled as a jump to function
// exit, the loop became dead code, and the fact wrongly said "resolved".
func Run(ctx context.Context) {
	ignore(ctx)
	for {
	}
}
