package nested_labeled_break_not_outer

func Start() {
	go func() {
		for {
			Inner: for {
				break Inner
			}
		}
	}()
}
