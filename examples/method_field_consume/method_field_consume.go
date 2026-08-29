package main

import "context"

type Worker struct { cancel context.CancelFunc }

func (w *Worker) Stop() { w.cancel() }

func Start() {
    ctx, cancel := context.WithCancel(context.Background())
    w := &Worker{cancel: cancel}
    w.Stop()
    go func() { <-ctx.Done() }()
}

