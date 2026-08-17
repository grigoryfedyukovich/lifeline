package start_wrapper_declared_target

func Launch(fn func()) { go fn() }

func worker() {
	for {
	}
}

func Start() {
	Launch(worker)
}
