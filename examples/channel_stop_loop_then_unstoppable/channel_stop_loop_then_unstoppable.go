package channel_stop_loop_then_unstoppable

func Start(stop <-chan struct{}) {
	go func() {
		for {
			select {
			case <-stop:
				return
			}
		}
		for {
		}
	}()
}
