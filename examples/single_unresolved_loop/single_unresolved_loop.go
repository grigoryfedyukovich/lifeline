package singleunresolvedloop

func Start() {
	go func() {
		for {
			work()
		}
	}()
}

func work() {}
