package constructor_cancel_handle_dropped

import "context"

type Worker struct {
	ctx    context.Context
	cancel context.CancelFunc
}

func New(parent context.Context) *Worker {
	ctx, cancel := context.WithCancel(parent)
	return &Worker{ctx: ctx, cancel: cancel}
}

func Start(parent context.Context) {
	_ = New(parent)
}
