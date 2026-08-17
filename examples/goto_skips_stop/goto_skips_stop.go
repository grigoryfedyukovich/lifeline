package main

func stopWorker() {}

func main() {
	go func() {
		goto loop
		stopWorker()
	loop:
		for {
		}
	}()
}
