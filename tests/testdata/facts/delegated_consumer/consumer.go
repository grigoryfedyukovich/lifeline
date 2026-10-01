package delegatedconsumer

import (
	"context"

	delegatedworker "github.com/gfedyukovich/lifeline/tests/testdata/facts/delegated_worker"
)

func Start(ctx context.Context) {
	go delegatedworker.Run(ctx)
}
