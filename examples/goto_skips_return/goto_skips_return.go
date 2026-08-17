package main

func main() {
	go func() {
		goto loop
		return
	loop:
		for {
		}
	}()
}
