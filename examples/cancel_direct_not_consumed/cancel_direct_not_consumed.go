package p

import "context"

func ignore(c context.CancelFunc) {}

func Start() {
	ctx, cancel := context.WithCancel(context.Background())
	go func() { <-ctx.Done() }()
	ignore(cancel)
}
