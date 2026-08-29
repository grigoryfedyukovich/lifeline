package main

import "context"

type worker struct { cancel context.CancelFunc }

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	w := worker{cancel: cancel}
	_ = w
	go func() { <-ctx.Done() }()
}
