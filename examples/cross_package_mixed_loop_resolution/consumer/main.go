package consumer

import "github.com/gfedyukovich/lifeline/benchmarks/phase4/cross_package_mixed_loop_resolution/worker"

func Start() {
	go worker.Run()
}
