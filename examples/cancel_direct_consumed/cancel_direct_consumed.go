package p

import "context"

func consume(c context.CancelFunc) { c() }

func Start() {
	ctx, cancel := context.WithCancel(context.Background())
	go func() { <-ctx.Done() }()
	consume(cancel)
}
