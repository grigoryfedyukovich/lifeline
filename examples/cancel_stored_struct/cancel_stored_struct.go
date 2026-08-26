package cancel_stored_struct

import "context"

type worker struct {
	cancel context.CancelFunc
}

func Start(parent context.Context) {
	ctx, cancel := context.WithCancel(parent)
	w := worker{cancel: cancel}
	_ = ctx
	_ = w
}
