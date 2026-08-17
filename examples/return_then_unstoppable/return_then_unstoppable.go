package p

func Start() {
	go func() {
		for {
			if stop() {
				return
			}
			work()
		}

		for {
			work()
		}
	}()
}

func work()      {}
func stop() bool { return false }
