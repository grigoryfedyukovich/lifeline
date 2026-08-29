package main

import "context"

type worker struct { cancel context.CancelFunc }

func newWorker() worker {
	_, cancel := context.WithCancel(context.Background())
	return worker{cancel: cancel}
}

func main() {
	w := newWorker()
	x := w
	x.cancel()
}
