package worker

import "context"

type Handle struct{ Cancel context.CancelFunc }

func New(parent context.Context) *Handle {
	_, cancel := context.WithCancel(parent)
	return &Handle{Cancel: cancel}
}
