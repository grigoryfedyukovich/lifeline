package conditionalreturnafterctxdone

import "context"

func work()      {}
func flag() bool { return false }

func Start(ctx context.Context) {
	go func() {
		for {
			select {
			case <-ctx.Done():
				if flag() {
					return
				}
			default:
				work()
			}
		}
	}()
}
