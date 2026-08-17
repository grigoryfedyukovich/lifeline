package labeledouterbreak

import "context"

func shouldStop() bool { return false }
func work()            {}

func Start(ctx context.Context) {
	go func() {
	Outer:
		for {
			for {
				if shouldStop() {
					break Outer
				}
				break
			}
			work()
		}
	}()
}
