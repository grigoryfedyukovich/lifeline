package local_closure_goroutines

// StartHealthChecks minimizes JetStream's local health-check closure, which
// can be launched at two sites in the same function.
func StartHealthChecks(recovered bool, check func()) {
	checkHealth := func() { check() }
	if recovered {
		go checkHealth()
	}
	go checkHealth()
}

// StartEndlessWorker uses the same launch shape with no way out of the loop.
func StartEndlessWorker() {
	worker := func() {
		for {
		}
	}
	go worker()
}
