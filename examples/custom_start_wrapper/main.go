package customstartwrapper

func process() {}

// Launch is a project-specific goroutine-start helper.
func Launch(worker func()) {
	go worker()
}

// Start contains a looping worker hidden behind Launch.
func Start() {
	Launch(func() {
		for {
			process()
		}
	})
}
