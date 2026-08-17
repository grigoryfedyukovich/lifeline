package inline_suppression

func Start() {
	go func() {
		for { //lifeline:ignore LL1002
		}
	}()
}
