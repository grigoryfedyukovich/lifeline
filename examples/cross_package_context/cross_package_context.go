package cross_package_context

import (
	"context"
	"github.com/gfedyukovich/lifeline/examples/cross_package_context/worker"
)

func Start(ctx context.Context) {
	go worker.Run()
}
