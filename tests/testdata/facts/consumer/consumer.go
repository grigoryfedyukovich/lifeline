package consumer

import "github.com/gfedyukovich/lifeline/tests/testdata/facts/worker"

func Start() {
	go worker.Run()
}
