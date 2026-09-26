package context_wrapper_typed_results

import "context"

func Make(parent context.Context) (context.CancelFunc, context.Context, error) {
	ctx, cancel := context.WithCancel(parent)
	return cancel, ctx, nil
}

func Start(parent context.Context) {
	cancel, ctx, _ := Make(parent)
	defer cancel()
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			}
		}
	}()
}
