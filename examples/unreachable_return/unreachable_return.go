package unreachable_return

func Start() {
	go func() {
		for {
			if false {
				return
			}
		}
	}()
}
