package mixedconsumer

import "github.com/gfedyukovich/lifeline/tests/testdata/facts/mixed_worker"

func Start() {
	go mixedworker.Run()
}
