package constant_consumer

import "github.com/gfedyukovich/lifeline/tests/testdata/facts/constant_worker"

func Start() { go constant_worker.Run(); go constant_worker.ImpossibleExit() }
