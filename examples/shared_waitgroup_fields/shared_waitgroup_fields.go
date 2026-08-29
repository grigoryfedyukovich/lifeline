package main

import "sync"

type Workers struct { a, b sync.WaitGroup }

func Start() {
    var w Workers
    w.a.Add(1)
    go func() {
        defer w.a.Done()
    }()
    w.b.Add(1)
    go func() {
        defer w.b.Done()
    }()
    w.a.Wait()
}

