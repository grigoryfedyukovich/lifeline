package channelshutdown

func process(int) {}

// Start uses channel closure as its explicit worker-stop protocol.
func Start(jobs <-chan int) {
	go func() {
		for job := range jobs {
			process(job)
		}
	}()
}
