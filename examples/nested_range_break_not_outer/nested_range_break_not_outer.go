package nested_range_break_not_outer

func Start() {
	values := []int{1}
	go func() {
		for {
			for range values {
				break
			}
		}
	}()
}
