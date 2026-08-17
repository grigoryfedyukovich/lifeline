package directbreakresolves

func Start() {
	go func() {
		for {
			if shouldStop() {
				break
			}
			work()
		}
	}()
}

func work() {}
func shouldStop() bool { return false }
