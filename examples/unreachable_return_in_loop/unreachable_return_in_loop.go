package unreacreturninloop

import "context"

func work() {}

func Start(ctx context.Context) {
	go func() {
		for {
			if false {
				return
			}
			work()
		}
	}()
}
