package main

import "context"

type Holder struct { a, b context.CancelFunc }

func Start() {
    ctx, cancel := context.WithCancel(context.Background())
    h := Holder{a: cancel, b: cancel}
    h.a()
    go func() { <-ctx.Done() }()
    _ = h
}

