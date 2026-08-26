package cancel_stored_struct_consumed

import "context"

type worker struct {
	cancel context.CancelFunc
}

func Start(parent context.Context) {
	ctx, cancel := context.WithCancel(parent)
	w := worker{cancel: cancel}
	_ = ctx
	defer w.cancel()
}
