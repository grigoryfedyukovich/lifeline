package main

func stopWorker() {}

func main() {
	go func() {
		for {
			if false {
				stopWorker()
			}
			continue
		}
	}()
}
