package unreachable_break

func Start() {
	go func() {
		for {
			if false {
				break
			}
		}
	}()
}
