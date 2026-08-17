package break_loop_then_unstoppable

func Start() {
	go func() {
		for {
			break
		}
		for {
		}
	}()
}
