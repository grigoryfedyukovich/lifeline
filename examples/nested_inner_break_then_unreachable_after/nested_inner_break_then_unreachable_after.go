package nested_inner_break_then_unreachable_after

func Start() {
	go func() {
		for {
			for {
				break
			}
		}
		workAfterLoop()
	}()
}

func workAfterLoop() {}
