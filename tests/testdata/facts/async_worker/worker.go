package async_worker

import "sync"

func Wait(g *sync.WaitGroup) { go g.Wait() }
