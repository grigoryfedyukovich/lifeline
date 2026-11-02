// Typed constant conditions determine reachable loop exits
package constant_dead_exit

const running bool = 3 > 2

func Start() {
	go func() {
		for running {
			if false {
				return
			}
		}
	}()
}
