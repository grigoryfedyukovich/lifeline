package start_wrapper_inline_priority

func Launch(fn func(), fallback func()) { go fn() }

func safeWorker() {}

func Start() {
	Launch(func() {
		for {
		}
	}, safeWorker)
}
