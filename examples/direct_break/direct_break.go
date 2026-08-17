package direct_break

func Start() {
	go func() {
		for {
			break
		}
	}()
}
