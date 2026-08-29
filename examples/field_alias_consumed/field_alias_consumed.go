package main

import "context"

type worker struct { cancel context.CancelFunc }

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	w := worker{cancel: cancel}
	x := w
	x.cancel()
	_ = ctx
}
