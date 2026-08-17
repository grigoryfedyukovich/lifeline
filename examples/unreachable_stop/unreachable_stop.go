package unreachable_stop

func StopWorker() {}

func Start() {
	go func() {
		for {
			if false {
				StopWorker()
			}
		}
	}()
}
