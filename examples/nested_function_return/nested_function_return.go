package nestedfunctionreturn

import "context"

func work() {}

func Start(ctx context.Context) {
	go func() {
		func() {
			return
		}()

		for {
			work()
		}
	}()
}
