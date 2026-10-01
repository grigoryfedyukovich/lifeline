package delegatedloopworker

import "context"

func step(context.Context) {}

// Run delegates ctx from inside its loop, which is still credited as that
// loop's stop path (unchanged permissive behavior): the exported fact must
// say the loop is resolved.
func Run(ctx context.Context) {
	for {
		step(ctx)
	}
}
