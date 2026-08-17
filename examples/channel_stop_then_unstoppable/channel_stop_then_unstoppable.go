package channelstopthenunstoppable

func work() {}

func Start(stop <-chan struct{}) {
	go func() {
		for {
			select {
			case <-stop:
				return
			default:
				work()
			}
		}

		for {
			work()
		}
	}()
}
