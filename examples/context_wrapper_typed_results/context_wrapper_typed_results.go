package context_wrapper_typed_results

import "context"

func Make(parent context.Context) (context.CancelFunc, context.Context, error) {
	ctx, cancel := context.WithCancel(parent)
	return cancel, ctx, nil
}

func Start(parent context.Context) {
	cancel, ctx, err := Make(parent)
	if err != nil {
		return
	}
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
