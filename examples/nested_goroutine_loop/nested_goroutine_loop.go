// B1: only the inner goroutine owns its infinite loop
package nested_goroutine_loop

func Start() {
	go func() {
		go func() {
			for {
			}
		}()
	}()
}
