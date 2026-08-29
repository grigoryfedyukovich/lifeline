package main

import "context"

type Holder struct { cancel context.CancelFunc }

func Start() {
    ctx1, cancel1 := context.WithCancel(context.Background())
    _ = ctx1
    h := Holder{cancel: cancel1}
    ctx2, cancel2 := context.WithCancel(context.Background())
    _ = ctx2
    h.cancel = cancel2
    cancel2()
    go func() { <-ctx1.Done() }()
    _ = h
}

