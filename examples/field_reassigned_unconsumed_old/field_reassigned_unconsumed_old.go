package main

import "context"

type worker struct { cancel context.CancelFunc }

func main() {
	ctx, cancel1 := context.WithCancel(context.Background())
	_, cancel2 := context.WithCancel(context.Background())
	w := worker{cancel: cancel1}
	w.cancel = cancel2
	w.cancel()
	go func() { <-ctx.Done() }()
}
