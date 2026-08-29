package main

import "context"

type worker struct { cancel context.CancelFunc }

func main() {
	_, cancel := context.WithCancel(context.Background())
	w := worker{cancel: cancel}
	p := &w
	p.cancel()
}
