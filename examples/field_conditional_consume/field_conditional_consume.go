package main

import "context"

type Holder struct { cancel context.CancelFunc }

func Start(flag bool) {
    ctx, cancel := context.WithCancel(context.Background())
    h := Holder{cancel: cancel}
    go func() { <-ctx.Done() }()
    if flag {
        h.cancel()
    }
}

