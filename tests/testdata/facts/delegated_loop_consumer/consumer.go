package delegatedloopconsumer

import (
	"context"

	delegatedloopworker "github.com/gfedyukovich/lifeline/tests/testdata/facts/delegated_loop_worker"
)

func Start(ctx context.Context) {
	go delegatedloopworker.Run(ctx)
}
