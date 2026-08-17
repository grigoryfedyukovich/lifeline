package nested_break_not_outer

func Start() {
	go func() {
		for {
			for {
				break
			}
		}
	}()
}
