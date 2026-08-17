package loop_break_scope

func Start() {
	go func() {
		for {
			switch {
			case true:
				break
			default:
			}
		}
	}()
}
