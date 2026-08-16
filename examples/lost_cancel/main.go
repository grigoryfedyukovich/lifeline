package lostcancel

import "context"

func Start(parent context.Context) {
	ctx, _ := context.WithCancel(parent)
	go run(ctx)
}

func run(ctx context.Context) {
	<-ctx.Done()
}
