package safe_then_unstoppable_loop

func Start() {
	go func() {
		for {
			if stopFirstLoop() {
				break
			}
		}

		for {
			work()
		}
	}()
}

func stopFirstLoop() bool { return false }
func work()               {}
