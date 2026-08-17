package nestedloopsmixedresolution

func Start() {
	go func() {
		for {
			for {
				break
			}
		}
	}()
}
