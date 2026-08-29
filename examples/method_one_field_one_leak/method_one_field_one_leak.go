package main

import "context"

type Worker struct {
    cancel context.CancelFunc
    other  context.CancelFunc
}

func (w *Worker) Stop() { w.cancel() }

func Start() {
    ctx1, cancel1 := context.WithCancel(context.Background())
    ctx2, cancel2 := context.WithCancel(context.Background())
    w := &Worker{cancel: cancel1, other: cancel2}
    w.Stop()
    go func() { <-ctx2.Done() }()
    _ = ctx1
}

