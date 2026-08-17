package breakthenunstoppable

func work() {}

func Start() {
	go func() {
		for {
			break
		}

		for {
			work()
		}
	}()
}
