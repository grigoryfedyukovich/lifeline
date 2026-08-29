package main

import "context"

type Worker struct { cancel context.CancelFunc }

func NewWorker() Worker {
    _, cancel := context.WithCancel(context.Background())
    return Worker{cancel: cancel}
}

func Start() {
    w1 := NewWorker()
    w2 := w1
    w2.cancel()
}

