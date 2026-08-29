package main

import "context"

type Worker struct { cancel context.CancelFunc }

func NewWorker() *Worker {
    _, cancel := context.WithCancel(context.Background())
    return &Worker{cancel: cancel}
}

func Start(flag bool) {
    w := NewWorker()
    if flag {
        w.cancel()
    }
}

