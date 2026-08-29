package consumer

import "github.com/gfedyukovich/lifeline/examples/cross_package_mixed_loop_resolution/worker"

func Start() {
	go worker.Run()
}
