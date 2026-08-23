package p

import "context"

func Start() {
	ctx, cancel := context.WithCancel(context.Background())
	go func() { <-ctx.Done() }()
	hop1(cancel)
}

func hop1(c context.CancelFunc) { hop2(c) }
func hop2(c context.CancelFunc) { hop3(c) }
func hop3(c context.CancelFunc) { _ = c }
